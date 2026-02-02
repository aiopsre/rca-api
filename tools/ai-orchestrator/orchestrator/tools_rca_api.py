from __future__ import annotations

import json
import os
import time
import uuid
from typing import Any, Dict, Optional

import requests

from .mcp_client import MCPClient
from .sdk.errors import OrchestratorErrorCategory, RCAApiError
from .sdk.runtime_client import RuntimeAPIClient
from .sdk.runtime_contract import (
    ClaimStartRequest,
    EvidencePublishRequest,
    FinalizeRequest,
    ListToolCallsRequest,
    ListVerificationRunsRequest,
    RenewHeartbeatRequest,
    ToolCallReportRequest,
    VerificationReportRequest,
)


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
        # Centralized runtime plane client: start/heartbeat/toolcall/evidence/finalize/verification.
        self.runtime = RuntimeAPIClient(
            lambda method, path, json_body=None, params=None, timeout_s=None: self._request(
                method=method,
                path=path,
                json_body=json_body,
                params=params,
                timeout_s=timeout_s,
            ),
            default_timeout_s=self.timeout_s,
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

    def resolve_toolset(self, pipeline: str) -> Dict[str, Any]:
        params: Dict[str, Any] = {}
        normalized_pipeline = str(pipeline or "").strip()
        if normalized_pipeline:
            params["pipeline"] = normalized_pipeline
        payload = self._request("GET", "/v1/orchestrator/toolsets/resolve", params=params or None)
        data = payload.get("data", payload)
        if not isinstance(data, dict):
            raise RuntimeError("invalid resolve_toolset response")
        return data

    def resolve_strategy(self, pipeline: str) -> Dict[str, Any]:
        params: Dict[str, Any] = {}
        normalized_pipeline = str(pipeline or "").strip()
        if normalized_pipeline:
            params["pipeline"] = normalized_pipeline
        payload = self._request("GET", "/v1/orchestrator/strategies/resolve", params=params or None)
        data = payload.get("data", payload)
        if not isinstance(data, dict):
            raise RuntimeError("invalid resolve_strategy response")
        strategy = data.get("strategy", data)
        if not isinstance(strategy, dict):
            raise RuntimeError("invalid resolve_strategy strategy payload")
        return strategy

    def register_templates(self, instance_id: str, templates: list[dict[str, Any]]) -> Dict[str, Any]:
        normalized_instance_id = str(instance_id or "").strip()
        if not normalized_instance_id:
            raise RuntimeError("instance_id is required")
        if not isinstance(templates, list) or not templates:
            raise RuntimeError("templates is required")
        body = {
            "instanceID": normalized_instance_id,
            "templates": templates,
        }
        payload = self._request("POST", "/v1/orchestrator/templates/register", json_body=body)
        data = payload.get("data", payload)
        if not isinstance(data, dict):
            raise RuntimeError("invalid register_templates response")
        return data

    def start_job(self, job_id: str) -> bool:
        return self.runtime.start_job(ClaimStartRequest(job_id=job_id))

    def renew_job_lease(self, job_id: str) -> None:
        self.runtime.renew_job_lease(RenewHeartbeatRequest(job_id=job_id))

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
        self.runtime.report_tool_call(
            ToolCallReportRequest(
                job_id=job_id,
                seq=seq,
                node_name=node_name,
                tool_name=tool_name,
                request_json=request_json,
                response_json=response_json,
                latency_ms=latency_ms,
                status=status,
                error_message=error,
                evidence_ids=evidence_ids,
            )
        )

    def list_tool_calls(
        self,
        job_id: str,
        *,
        limit: int = 200,
        offset: int = 0,
        seq: int | None = None,
    ) -> Dict[str, Any]:
        return self.runtime.list_tool_calls(
            ListToolCallsRequest(
                job_id=job_id,
                limit=limit,
                offset=offset,
                seq=seq,
            )
        )

    def finalize_job(
        self,
        job_id: str,
        status: str,
        diagnosis_json: Dict[str, Any] | None,
        error_message: str | None = None,
        evidence_ids: list[str] | None = None,
    ) -> None:
        self.runtime.finalize_job(
            FinalizeRequest(
                job_id=job_id,
                status=status,
                diagnosis_json=diagnosis_json,
                error_message=error_message,
                evidence_ids=evidence_ids,
            )
        )

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

    def create_incident_verification_run(
        self,
        *,
        incident_id: str,
        source: str,
        step_index: int,
        tool: str,
        observed: str,
        meets_expectation: bool,
        params_json: Dict[str, Any] | str | None = None,
        actor: str | None = None,
    ) -> Dict[str, Any]:
        return self.runtime.create_verification_run(
            VerificationReportRequest(
                incident_id=incident_id,
                source=source,
                step_index=step_index,
                tool=tool,
                observed=observed,
                meets_expectation=meets_expectation,
                params_json=params_json,
                actor=actor,
            )
        )

    def list_incident_verification_runs(
        self,
        incident_id: str,
        *,
        page: int = 1,
        limit: int = 200,
    ) -> Dict[str, Any]:
        return self.runtime.list_verification_runs(
            ListVerificationRunsRequest(
                incident_id=incident_id,
                page=page,
                limit=limit,
            )
        )

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
        request = EvidencePublishRequest.for_mock(
            incident_id=incident_id,
            summary=summary,
            raw=raw,
            job_id=job_id,
            idempotency_key=idempotency_key or f"orchestrator-mock-{uuid.uuid4().hex}",
            created_by=created_by,
        )
        return self.runtime.publish_evidence(request)

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
        request = EvidencePublishRequest.for_query(
            incident_id=incident_id,
            kind=kind,
            query=query,
            result=result,
            job_id=job_id,
            idempotency_key=idempotency_key or f"orchestrator-query-{uuid.uuid4().hex}",
            created_by=created_by,
        )
        return self.runtime.publish_evidence(request)
