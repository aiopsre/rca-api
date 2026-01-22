from __future__ import annotations

import json
import os
import time
import uuid
from typing import Any, Dict, Optional

import requests

from .mcp_client import MCPClient
from .sdk.errors import OrchestratorErrorCategory, RCAApiError


def _ts(seconds: int) -> Dict[str, int]:
    return {"seconds": int(seconds), "nanos": 0}


def _extract_first(payload: Any, *keys: str) -> Optional[Any]:
    if not isinstance(payload, dict):
        return None

    candidates: list[Any] = [payload]
    data = payload.get("data")
    if isinstance(data, dict):
        candidates.append(data)
        for nested in ("job", "incident", "evidence", "datasource"):
            nested_obj = data.get(nested)
            if isinstance(nested_obj, dict):
                candidates.append(nested_obj)

    for nested in ("job", "incident", "evidence", "datasource"):
        nested_obj = payload.get(nested)
        if isinstance(nested_obj, dict):
            candidates.append(nested_obj)

    for key in keys:
        for candidate in candidates:
            if key in candidate:
                value = candidate[key]
                if value is not None and value != "":
                    return value
    return None


def _envelope_code_is_success(value: Any) -> bool:
    if value is None:
        return True
    if isinstance(value, bool):
        return bool(value) is False
    if isinstance(value, int):
        return value == 0
    if isinstance(value, float):
        return int(value) == 0
    if isinstance(value, str):
        normalized = value.strip()
        if not normalized:
            return True
        if normalized == "0":
            return True
    return False


class RCAApiClient:
    def __init__(
        self,
        base_url: str,
        scopes: str | None,
        instance_id: str | None = None,
        timeout_s: float = 10.0,
        mcp_scopes: str | None = None,
        mcp_timeout_s: float | None = None,
        mcp_verify_remote_tools: bool = False,
    ):
        self.base_url = base_url.rstrip("/")
        self.timeout_s = timeout_s
        self.scopes = (scopes or "").strip()
        self.instance_id = (instance_id or "").strip()
        self.session = requests.Session()
        self.session.headers.update({"Accept": "application/json"})
        if self.scopes:
            self.session.headers.update({"X-Scopes": self.scopes})
        if self.instance_id:
            self.session.headers.update({"X-Orchestrator-Instance-ID": self.instance_id})

        mcp_scopes_value = mcp_scopes
        if mcp_scopes_value is None:
            mcp_scopes_value = os.getenv("RCA_API_SCOPES", "")
        self.mcp_client = MCPClient(
            base_url=self.base_url,
            scopes=mcp_scopes_value,
            timeout_s=self.timeout_s if mcp_timeout_s is None else mcp_timeout_s,
            max_retries=3,
            verify_remote_tools=mcp_verify_remote_tools,
        )

    def _request(
        self,
        method: str,
        path: str,
        json_body: Optional[Dict[str, Any]] = None,
        params: Optional[Dict[str, Any]] = None,
        timeout_s: Optional[float] = None,
    ) -> Dict[str, Any]:
        url = f"{self.base_url}{path}"
        request_timeout = self.timeout_s if timeout_s is None else max(float(timeout_s), 0.1)
        try:
            response = self.session.request(
                method=method.upper(),
                url=url,
                json=json_body,
                params=params,
                timeout=request_timeout,
            )
        except requests.RequestException as exc:
            raise RCAApiError.from_transport_error(method=method, path=path, cause=exc) from exc

        body_text = response.text.strip()
        payload: dict[str, Any] | None = None
        if body_text:
            try:
                parsed = response.json()
            except json.JSONDecodeError as exc:
                raise RCAApiError(
                    category=OrchestratorErrorCategory.UNKNOWN,
                    message=f"{method.upper()} {path} returned non-JSON body: {body_text}",
                    method=method,
                    path=path,
                    http_status=response.status_code,
                    body_text=body_text,
                    cause=exc,
                ) from exc
            if not isinstance(parsed, dict):
                raise RCAApiError(
                    category=OrchestratorErrorCategory.UNKNOWN,
                    message=f"{method.upper()} {path} returned invalid JSON type: {type(parsed).__name__}",
                    method=method,
                    path=path,
                    http_status=response.status_code,
                    body_text=body_text,
                )
            payload = parsed

        envelope_code = payload.get("code") if isinstance(payload, dict) else None
        if envelope_code is None and isinstance(payload, dict):
            envelope_code = payload.get("reason")
        envelope_message = payload.get("message") if isinstance(payload, dict) else None
        details = payload.get("details") if isinstance(payload, dict) else None
        if details is None and isinstance(payload, dict):
            details = payload.get("metadata")

        if not response.ok:
            raise RCAApiError.from_response(
                method=method,
                path=path,
                http_status=response.status_code,
                envelope_code=envelope_code,
                envelope_message=envelope_message or body_text or f"http={response.status_code}",
                details=details,
                payload=payload,
                body_text=body_text,
            )

        if isinstance(payload, dict) and ("code" in payload) and not _envelope_code_is_success(envelope_code):
            raise RCAApiError.from_response(
                method=method,
                path=path,
                http_status=response.status_code,
                envelope_code=envelope_code,
                envelope_message=envelope_message or "business error",
                details=details,
                payload=payload,
                body_text=body_text,
            )

        if isinstance(payload, dict):
            return payload
        return {}

    def _mcp_call(
        self,
        tool: str,
        input_payload: Dict[str, Any],
        idempotency_key: str | None = None,
    ) -> Dict[str, Any]:
        return self.mcp_client.call(
            tool=tool,
            input_payload=input_payload,
            idempotency_key=idempotency_key,
        )

    @staticmethod
    def _extract_mcp_output(tool: str, payload: Dict[str, Any]) -> Dict[str, Any]:
        output = payload.get("output")
        if isinstance(output, dict):
            return output
        raise RuntimeError(f"invalid MCP output for tool={tool}: {payload}")

    @staticmethod
    def _normalize_mcp_query_output(tool: str, payload: Dict[str, Any]) -> Dict[str, Any]:
        output = RCAApiClient._extract_mcp_output(tool, payload)
        raw_result = output.get("queryResultJSON")
        if isinstance(raw_result, str) and raw_result.strip():
            return output

        if not bool(payload.get("truncated")):
            raise RuntimeError(f"invalid {tool} MCP response: {payload}")

        # MCP response may be truncated at envelope level; keep workflow moving with a compact placeholder.
        preview = str(output.get("preview") or "").strip()
        fallback_payload: Dict[str, Any] = {
            "mcp_truncated": True,
            "reason": str(output.get("reason") or "max_response_bytes_exceeded"),
        }
        if preview:
            fallback_payload["preview"] = preview
        warnings = payload.get("warnings")
        if isinstance(warnings, list) and warnings:
            fallback_payload["warnings"] = warnings

        fallback_result = json.dumps(fallback_payload, ensure_ascii=False, separators=(",", ":"))
        return {
            "queryResultJSON": fallback_result,
            "resultSizeBytes": len(fallback_result.encode("utf-8")),
            "rowCount": 0,
            "isTruncated": True,
        }

    # jobs
    def list_jobs(
        self,
        status: str,
        limit: int = 10,
        offset: int = 0,
        wait_seconds: int = 0,
    ) -> Dict[str, Any]:
        wait = max(int(wait_seconds), 0)
        params: Dict[str, Any] = {"status": status, "limit": int(limit), "offset": int(offset)}
        if wait > 0:
            params["wait_seconds"] = wait
        request_timeout = max(self.timeout_s, float(wait) + 5.0) if wait > 0 else self.timeout_s
        payload = self._request(
            "GET",
            "/v1/ai/jobs",
            params=params,
            timeout_s=request_timeout,
        )
        data = payload.get("data", payload)
        if not isinstance(data, dict):
            return {"totalCount": 0, "jobs": []}
        return data

    def get_job(self, job_id: str) -> Dict[str, Any]:
        payload = self._request("GET", f"/v1/ai/jobs/{job_id}")
        data = payload.get("data", payload)
        if not isinstance(data, dict):
            raise RuntimeError(f"invalid get_job response for {job_id}")
        if "job" in data and isinstance(data["job"], dict):
            return data["job"]
        if "job" in payload and isinstance(payload["job"], dict):
            return payload["job"]
        return data

    def start_job(self, job_id: str) -> bool:
        try:
            self._request("POST", f"/v1/ai/jobs/{job_id}/start")
            return True
        except RCAApiError as exc:
            if exc.http_status == 409:
                return False
            if exc.category in {OrchestratorErrorCategory.CONFLICT, OrchestratorErrorCategory.OWNER_LOST}:
                return False
            raise

    def renew_job_lease(self, job_id: str) -> None:
        self._request("POST", f"/v1/ai/jobs/{job_id}/heartbeat")

    def add_tool_call(
        self,
        job_id: str,
        seq: int,
        node_name: str,
        tool_name: str,
        request_json: Dict[str, Any],
        response_json: Dict[str, Any] | None,
        latency_ms: int,
        status: str,
        error: str | None = None,
        evidence_ids: list[str] | None = None,
    ) -> None:
        body: Dict[str, Any] = {
            "jobID": job_id,
            "seq": int(seq),
            "nodeName": node_name,
            "toolName": tool_name,
            "requestJSON": json.dumps(request_json, ensure_ascii=False, separators=(",", ":")),
            "status": status,
            "latencyMs": int(max(latency_ms, 0)),
        }
        if response_json is not None:
            body["responseJSON"] = json.dumps(response_json, ensure_ascii=False, separators=(",", ":"))
        if error:
            body["errorMessage"] = error
        if evidence_ids:
            body["evidenceIDs"] = [str(item).strip() for item in evidence_ids if str(item).strip()]
        self._request("POST", f"/v1/ai/jobs/{job_id}/tool-calls", json_body=body)

    def finalize_job(
        self,
        job_id: str,
        status: str,
        diagnosis_json: Dict[str, Any] | None,
        error_message: str | None = None,
        evidence_ids: list[str] | None = None,
    ) -> None:
        body: Dict[str, Any] = {
            "jobID": job_id,
            "status": status,
        }
        if diagnosis_json is not None:
            body["diagnosisJSON"] = json.dumps(diagnosis_json, ensure_ascii=False, separators=(",", ":"))
        if error_message:
            body["errorMessage"] = error_message
        if evidence_ids:
            body["evidenceIDs"] = [str(item).strip() for item in evidence_ids if str(item).strip()]
        # Finalization may include DB writes and incident writeback; allow a longer timeout to reduce false negatives.
        self._request("POST", f"/v1/ai/jobs/{job_id}/finalize", json_body=body, timeout_s=max(self.timeout_s, 30.0))

    # incidents
    def get_incident(self, incident_id: str) -> Dict[str, Any]:
        payload = self._mcp_call(
            tool="get_incident",
            input_payload={"incident_id": incident_id},
            idempotency_key=f"orchestrator-mcp-get-incident-{uuid.uuid4().hex}",
        )
        output = self._extract_mcp_output("get_incident", payload)
        if str(output.get("incidentID") or "").strip():
            return output
        raise RuntimeError(f"invalid get_incident MCP response for incident_id={incident_id}: {payload}")

    def list_alert_events_current(
        self,
        namespace: str | None = None,
        service: str | None = None,
        severity: str | None = None,
        page: int = 1,
        limit: int = 20,
    ) -> Dict[str, Any]:
        input_payload: Dict[str, Any] = {
            "page": int(page),
            "limit": int(limit),
        }
        if namespace:
            input_payload["namespace"] = str(namespace).strip()
        if service:
            input_payload["service"] = str(service).strip()
        if severity:
            input_payload["severity"] = str(severity).strip()
        payload = self._mcp_call(
            tool="list_alert_events_current",
            input_payload=input_payload,
            idempotency_key=f"orchestrator-mcp-list-alerts-{uuid.uuid4().hex}",
        )
        return self._extract_mcp_output("list_alert_events_current", payload)

    def get_evidence(self, evidence_id: str) -> Dict[str, Any]:
        payload = self._mcp_call(
            tool="get_evidence",
            input_payload={"evidence_id": evidence_id},
            idempotency_key=f"orchestrator-mcp-get-evidence-{uuid.uuid4().hex}",
        )
        output = self._extract_mcp_output("get_evidence", payload)
        if str(output.get("evidenceID") or "").strip():
            return output
        raise RuntimeError(f"invalid get_evidence MCP response for evidence_id={evidence_id}: {payload}")

    def list_incident_evidence(
        self,
        incident_id: str,
        page: int = 1,
        limit: int = 20,
    ) -> Dict[str, Any]:
        payload = self._mcp_call(
            tool="list_incident_evidence",
            input_payload={
                "incident_id": incident_id,
                "page": int(page),
                "limit": int(limit),
            },
            idempotency_key=f"orchestrator-mcp-list-evidence-{uuid.uuid4().hex}",
        )
        return self._extract_mcp_output("list_incident_evidence", payload)

    # datasource / evidence (P0 minimal)
    def ensure_datasource(self, ds_base_url: str) -> str:
        if not ds_base_url.strip():
            raise RuntimeError("DS_BASE_URL is required when RUN_QUERY=1")

        list_payload = self._request("GET", "/v1/datasources", params={"offset": 0, "limit": 200})
        list_data = list_payload.get("data", list_payload)
        datasources = []
        if isinstance(list_data, dict):
            raw_list = list_data.get("datasources", [])
            if isinstance(raw_list, list):
                datasources = raw_list

        normalized_base = ds_base_url.rstrip("/")
        for item in datasources:
            if not isinstance(item, dict):
                continue
            base_url = str(item.get("baseURL") or item.get("base_url") or "").rstrip("/")
            ds_type = str(item.get("type") or "").lower()
            ds_id = str(item.get("datasourceID") or item.get("datasource_id") or "").strip()
            if ds_type == "prometheus" and ds_id and base_url == normalized_base:
                return ds_id

        create_body = {
            "type": "prometheus",
            "name": f"orchestrator-prom-{int(time.time())}",
            "baseURL": ds_base_url,
            "authType": "none",
            "timeoutMs": 5000,
            "isEnabled": True,
        }
        created = self._request("POST", "/v1/datasources", json_body=create_body)
        ds_id = _extract_first(created, "datasourceID", "datasource_id")
        if not isinstance(ds_id, str) or not ds_id:
            raise RuntimeError(f"failed to parse datasource id from response: {created}")
        return ds_id

    def save_mock_evidence(
        self,
        incident_id: str,
        summary: str,
        raw: Dict[str, Any],
        *,
        job_id: str | None = None,
        idempotency_key: str | None = None,
        created_by: str | None = None,
    ) -> str:
        now_s = int(time.time())
        body = {
            "incidentID": incident_id,
            "idempotencyKey": (idempotency_key or f"orchestrator-mock-{uuid.uuid4().hex}"),
            "type": "metrics",
            "queryText": "mock://orchestrator",
            "queryJSON": "{}",
            "timeRangeStart": _ts(now_s - 600),
            "timeRangeEnd": _ts(now_s),
            "resultJSON": json.dumps(raw, ensure_ascii=False, separators=(",", ":")),
            "summary": summary,
            "createdBy": created_by or "system",
        }
        normalized_job_id = str(job_id or "").strip()
        if normalized_job_id:
            body["jobID"] = normalized_job_id
        payload = self._request("POST", f"/v1/incidents/{incident_id}/evidence", json_body=body)
        evidence_id = _extract_first(payload, "evidenceID", "evidence_id")
        if not isinstance(evidence_id, str) or not evidence_id:
            raise RuntimeError(f"failed to parse evidence id from response: {payload}")
        return evidence_id

    def query_metrics(
        self,
        datasource_id: str,
        promql: str,
        start_ts: int,
        end_ts: int,
        step_s: int,
    ) -> Dict[str, Any]:
        payload = self._mcp_call(
            tool="query_metrics",
            input_payload={
                "datasource_id": datasource_id,
                "expr": promql,
                "time_range_start": _ts(start_ts),
                "time_range_end": _ts(end_ts),
                "step_seconds": int(step_s),
            },
            idempotency_key=f"orchestrator-mcp-query-metrics-{uuid.uuid4().hex}",
        )
        return self._normalize_mcp_query_output("query_metrics", payload)

    def query_logs(
        self,
        datasource_id: str,
        query: str,
        start_ts: int,
        end_ts: int,
        limit: int,
    ) -> Dict[str, Any]:
        payload = self._mcp_call(
            tool="query_logs",
            input_payload={
                "datasource_id": datasource_id,
                "query": query,
                "query_json": {},
                "time_range_start": _ts(start_ts),
                "time_range_end": _ts(end_ts),
                "limit": int(limit),
            },
            idempotency_key=f"orchestrator-mcp-query-logs-{uuid.uuid4().hex}",
        )
        return self._normalize_mcp_query_output("query_logs", payload)

    def save_evidence_from_query(
        self,
        incident_id: str,
        kind: str,
        query: Dict[str, Any],
        result: Dict[str, Any],
        *,
        job_id: str | None = None,
        idempotency_key: str | None = None,
        created_by: str | None = None,
    ) -> str:
        now_s = int(time.time())
        query_text = str(query.get("queryText") or query.get("query_text") or "orchestrator_query")
        query_json = json.dumps(query, ensure_ascii=False, separators=(",", ":"))

        raw_result = result.get("queryResultJSON")
        if isinstance(raw_result, str):
            result_json = raw_result
        else:
            result_json = json.dumps(result, ensure_ascii=False, separators=(",", ":"))

        body: Dict[str, Any] = {
            "incidentID": incident_id,
            "idempotencyKey": (idempotency_key or f"orchestrator-query-{uuid.uuid4().hex}"),
            "type": kind,
            "queryText": query_text,
            "queryJSON": query_json,
            "timeRangeStart": _ts(now_s - 600),
            "timeRangeEnd": _ts(now_s),
            "resultJSON": result_json,
            "summary": f"orchestrator collected {kind} evidence",
            "createdBy": created_by or "system",
        }
        normalized_job_id = str(job_id or "").strip()
        if normalized_job_id:
            body["jobID"] = normalized_job_id
        datasource_id = str(query.get("datasourceID") or query.get("datasource_id") or "").strip()
        if datasource_id:
            body["datasourceID"] = datasource_id

        payload = self._request("POST", f"/v1/incidents/{incident_id}/evidence", json_body=body)
        evidence_id = _extract_first(payload, "evidenceID", "evidence_id")
        if not isinstance(evidence_id, str) or not evidence_id:
            raise RuntimeError(f"failed to parse evidence id from response: {payload}")
        return evidence_id
