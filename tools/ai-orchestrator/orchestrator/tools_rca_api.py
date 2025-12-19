from __future__ import annotations

import json
import time
import uuid
from typing import Any, Dict, Optional

import requests


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


class RCAApiClient:
    def __init__(self, base_url: str, scopes: str | None, timeout_s: float = 10.0):
        self.base_url = base_url.rstrip("/")
        self.timeout_s = timeout_s
        self.scopes = (scopes or "").strip()
        self.session = requests.Session()
        self.session.headers.update({"Accept": "application/json"})
        if self.scopes:
            self.session.headers.update({"X-Scopes": self.scopes})

    def _request(
        self,
        method: str,
        path: str,
        json_body: Optional[Dict[str, Any]] = None,
        params: Optional[Dict[str, Any]] = None,
    ) -> Dict[str, Any]:
        url = f"{self.base_url}{path}"
        response = self.session.request(
            method=method.upper(),
            url=url,
            json=json_body,
            params=params,
            timeout=self.timeout_s,
        )

        body_text = response.text.strip()
        if not response.ok:
            raise RuntimeError(f"{method.upper()} {path} failed: http={response.status_code}, body={body_text}")

        if not body_text:
            return {}

        try:
            payload = response.json()
        except json.JSONDecodeError as exc:
            raise RuntimeError(f"{method.upper()} {path} returned non-JSON body: {body_text}") from exc

        if isinstance(payload, dict):
            return payload
        raise RuntimeError(f"{method.upper()} {path} returned invalid JSON type: {type(payload).__name__}")

    # jobs
    def list_jobs(self, status: str, limit: int = 10, offset: int = 0) -> Dict[str, Any]:
        payload = self._request(
            "GET",
            "/v1/ai/jobs",
            params={"status": status, "limit": int(limit), "offset": int(offset)},
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

    def start_job(self, job_id: str) -> None:
        self._request("POST", f"/v1/ai/jobs/{job_id}/start")

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
        self._request("POST", f"/v1/ai/jobs/{job_id}/tool-calls", json_body=body)

    def finalize_job(
        self,
        job_id: str,
        status: str,
        diagnosis_json: Dict[str, Any] | None,
        error_message: str | None = None,
    ) -> None:
        body: Dict[str, Any] = {
            "jobID": job_id,
            "status": status,
        }
        if diagnosis_json is not None:
            body["diagnosisJSON"] = json.dumps(diagnosis_json, ensure_ascii=False, separators=(",", ":"))
        if error_message:
            body["errorMessage"] = error_message
        self._request("POST", f"/v1/ai/jobs/{job_id}/finalize", json_body=body)

    # incidents
    def get_incident(self, incident_id: str) -> Dict[str, Any]:
        payload = self._request("GET", f"/v1/incidents/{incident_id}")
        data = payload.get("data", payload)
        if not isinstance(data, dict):
            raise RuntimeError(f"invalid get_incident response for {incident_id}")
        if "incident" in data and isinstance(data["incident"], dict):
            return data["incident"]
        if "incident" in payload and isinstance(payload["incident"], dict):
            return payload["incident"]
        return data

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

    def save_mock_evidence(self, incident_id: str, summary: str, raw: Dict[str, Any]) -> str:
        now_s = int(time.time())
        body = {
            "incidentID": incident_id,
            "idempotencyKey": f"orchestrator-mock-{uuid.uuid4().hex}",
            "type": "metrics",
            "queryText": "mock://orchestrator",
            "queryJSON": "{}",
            "timeRangeStart": _ts(now_s - 600),
            "timeRangeEnd": _ts(now_s),
            "resultJSON": json.dumps(raw, ensure_ascii=False, separators=(",", ":")),
            "summary": summary,
            "createdBy": "system",
        }
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
        body = {
            "datasourceID": datasource_id,
            "promql": promql,
            "timeRangeStart": _ts(start_ts),
            "timeRangeEnd": _ts(end_ts),
            "stepSeconds": int(step_s),
        }
        payload = self._request("POST", "/v1/evidence:queryMetrics", json_body=body)
        data = payload.get("data", payload)
        if isinstance(data, dict):
            return data
        raise RuntimeError(f"invalid query_metrics response: {payload}")

    def query_logs(
        self,
        datasource_id: str,
        query: str,
        start_ts: int,
        end_ts: int,
        limit: int,
    ) -> Dict[str, Any]:
        body = {
            "datasourceID": datasource_id,
            "queryText": query,
            "queryJSON": "{}",
            "timeRangeStart": _ts(start_ts),
            "timeRangeEnd": _ts(end_ts),
            "limit": int(limit),
        }
        payload = self._request("POST", "/v1/evidence:queryLogs", json_body=body)
        data = payload.get("data", payload)
        if isinstance(data, dict):
            return data
        raise RuntimeError(f"invalid query_logs response: {payload}")

    def save_evidence_from_query(self, incident_id: str, kind: str, query: Dict[str, Any], result: Dict[str, Any]) -> str:
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
            "idempotencyKey": f"orchestrator-query-{uuid.uuid4().hex}",
            "type": kind,
            "queryText": query_text,
            "queryJSON": query_json,
            "timeRangeStart": _ts(now_s - 600),
            "timeRangeEnd": _ts(now_s),
            "resultJSON": result_json,
            "summary": f"orchestrator collected {kind} evidence",
            "createdBy": "system",
        }
        datasource_id = str(query.get("datasourceID") or query.get("datasource_id") or "").strip()
        if datasource_id:
            body["datasourceID"] = datasource_id

        payload = self._request("POST", f"/v1/incidents/{incident_id}/evidence", json_body=body)
        evidence_id = _extract_first(payload, "evidenceID", "evidence_id")
        if not isinstance(evidence_id, str) or not evidence_id:
            raise RuntimeError(f"failed to parse evidence id from response: {payload}")
        return evidence_id
