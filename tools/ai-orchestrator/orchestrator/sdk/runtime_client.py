from __future__ import annotations

from typing import Any, Protocol
import uuid

from .errors import OrchestratorErrorCategory, RCAApiError
from .runtime_contract import (
    ClaimStartRequest,
    ClaimStartResponse,
    EvidencePublishRequest,
    FinalizeRequest,
    GetJobSessionContextRequest,
    ListToolCallsRequest,
    ListVerificationRunsRequest,
    PatchJobSessionContextRequest,
    RenewHeartbeatRequest,
    ToolCallReportRequest,
    VerificationReportRequest,
)


class RuntimeRequestFn(Protocol):
    def __call__(
        self,
        method: str,
        path: str,
        json_body: dict[str, Any] | None = None,
        params: dict[str, Any] | None = None,
        timeout_s: float | None = None,
    ) -> dict[str, Any]:
        ...


def extract_first(payload: Any, *keys: str) -> Any | None:
    if not isinstance(payload, dict):
        return None

    candidates: list[Any] = [payload]
    data = payload.get("data")
    if isinstance(data, dict):
        candidates.append(data)
        for nested in ("job", "incident", "evidence", "datasource", "run"):
            nested_obj = data.get(nested)
            if isinstance(nested_obj, dict):
                candidates.append(nested_obj)

    for nested in ("job", "incident", "evidence", "datasource", "run"):
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


def extract_data(payload: dict[str, Any], *, default: dict[str, Any]) -> dict[str, Any]:
    data = payload.get("data", payload)
    if isinstance(data, dict):
        return data
    return default


class RuntimeAPIClient:
    """Worker runtime interaction client for canonical job/evidence/verification calls."""

    def __init__(self, request_fn: RuntimeRequestFn, *, default_timeout_s: float) -> None:
        self._request = request_fn
        self._default_timeout_s = float(default_timeout_s)

    def start_job(self, request: ClaimStartRequest) -> ClaimStartResponse:
        try:
            payload = self._request("POST", request.path())
            return ClaimStartResponse.from_api_response(payload)
        except RCAApiError as exc:
            if exc.http_status == 409:
                return ClaimStartResponse()
            if exc.category in {OrchestratorErrorCategory.CONFLICT, OrchestratorErrorCategory.OWNER_LOST}:
                return ClaimStartResponse()
            raise

    def renew_job_lease(self, request: RenewHeartbeatRequest) -> None:
        self._request("POST", request.path())

    def get_job_session_context(self, request: GetJobSessionContextRequest) -> dict[str, Any]:
        payload = self._request("GET", request.path())
        return extract_data(payload, default={})

    def patch_job_session_context(self, request: PatchJobSessionContextRequest) -> dict[str, Any]:
        payload = self._request("PATCH", request.path(), json_body=request.to_api_body())
        return extract_data(payload, default={})

    def report_tool_call(self, request: ToolCallReportRequest) -> None:
        self._request("POST", request.path(), json_body=request.to_api_body())

    def list_tool_calls(self, request: ListToolCallsRequest) -> dict[str, Any]:
        payload = self._request("GET", request.path(), params=request.to_api_params())
        return extract_data(payload, default={"totalCount": 0, "toolCalls": []})

    def finalize_job(self, request: FinalizeRequest) -> None:
        timeout_s = max(self._default_timeout_s, 30.0)
        self._request(
            "POST",
            request.path(),
            json_body=request.to_api_body(),
            timeout_s=timeout_s,
        )

    def publish_evidence(self, request: EvidencePublishRequest) -> str:
        fallback_idempotency_key = f"orchestrator-evidence-{uuid.uuid4().hex}"
        payload = self._request(
            "POST",
            request.path(),
            json_body=request.to_api_body(fallback_idempotency_key=fallback_idempotency_key),
        )
        evidence_id = extract_first(payload, "evidenceID", "evidence_id")
        if not isinstance(evidence_id, str) or not evidence_id:
            raise RuntimeError(f"failed to parse evidence id from response: {payload}")
        return evidence_id

    def create_verification_run(self, request: VerificationReportRequest) -> dict[str, Any]:
        payload = self._request("POST", request.path(), json_body=request.to_api_body())
        return extract_data(payload, default={})

    def list_verification_runs(self, request: ListVerificationRunsRequest) -> dict[str, Any]:
        payload = self._request("GET", request.path(), params=request.to_api_params())
        return extract_data(payload, default={"totalCount": 0, "runs": []})
