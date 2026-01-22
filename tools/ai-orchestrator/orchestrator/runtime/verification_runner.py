from __future__ import annotations

from dataclasses import dataclass
import json
from typing import Any, Callable
import uuid

from ..tools_rca_api import RCAApiClient


@dataclass(frozen=True)
class VerificationStepResult:
    step_index: int
    tool: str
    meets_expectation: bool
    observed: str


def _normalize_tool_name(tool: str) -> str:
    normalized = str(tool).strip()
    if normalized.startswith("mcp."):
        return normalized[4:]
    return normalized


def _to_json_preview(payload: Any, max_len: int = 200) -> str:
    try:
        raw = json.dumps(payload, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    except Exception:  # noqa: BLE001
        raw = str(payload)
    if len(raw) <= max_len:
        return raw
    return raw[: max_len - 3] + "..."


def _truncate_text(text: str, max_len: int = 480) -> str:
    normalized = str(text or "").strip()
    if len(normalized) <= max_len:
        return normalized
    return normalized[: max_len - 3] + "..."


def _parse_json_object(raw: Any) -> dict[str, Any] | None:
    if isinstance(raw, dict):
        return raw
    if not isinstance(raw, str):
        return None
    value = raw.strip()
    if not value:
        return None
    try:
        parsed = json.loads(value)
    except json.JSONDecodeError:
        return None
    if isinstance(parsed, dict):
        return parsed
    return None


def _extract_result_payload(call_payload: Any) -> Any:
    if not isinstance(call_payload, dict):
        return call_payload
    output = call_payload.get("output")
    if output is not None:
        return output
    return call_payload


def _query_result_is_empty(raw: Any) -> bool:
    if raw is None:
        return True
    if isinstance(raw, str):
        trimmed = raw.strip()
        if not trimmed:
            return True
        parsed = _parse_json_object(trimmed)
        if isinstance(parsed, dict):
            data = parsed.get("data")
            if isinstance(data, dict):
                result = data.get("result")
                if isinstance(result, list):
                    return len(result) == 0
            return len(parsed) == 0
        return trimmed in {"[]", "{}"}
    if isinstance(raw, list):
        return len(raw) == 0
    if isinstance(raw, dict):
        if not raw:
            return True
        data = raw.get("data")
        if isinstance(data, dict):
            result = data.get("result")
            if isinstance(result, list):
                return len(result) == 0
        return False
    return False


def _result_exists(call_payload: Any) -> bool:
    result_payload = _extract_result_payload(call_payload)
    if result_payload is None:
        return False
    if isinstance(result_payload, dict):
        if "queryResultJSON" in result_payload:
            return not _query_result_is_empty(result_payload.get("queryResultJSON"))
        if "rowCount" in result_payload:
            try:
                return int(result_payload.get("rowCount")) > 0
            except (TypeError, ValueError):
                pass
        return bool(result_payload)
    if isinstance(result_payload, list):
        return len(result_payload) > 0
    if isinstance(result_payload, str):
        return bool(result_payload.strip())
    return True


class VerificationRunner:
    def __init__(
        self,
        *,
        client: RCAApiClient,
        execute_with_retry: Callable[[str, Callable[[], Any]], Any],
        log_func: Callable[[str], None] | None = None,
    ) -> None:
        self._client = client
        self._execute_with_retry = execute_with_retry
        self._log_func = log_func

    def run(
        self,
        *,
        incident_id: str,
        verification_plan: dict[str, Any],
        source: str = "ai_job",
        actor: str | None = None,
    ) -> list[VerificationStepResult]:
        normalized_incident_id = str(incident_id).strip()
        if not normalized_incident_id:
            raise RuntimeError("incident_id is required for verification run")

        steps = verification_plan.get("steps") if isinstance(verification_plan, dict) else None
        if not isinstance(steps, list):
            return []

        results: list[VerificationStepResult] = []
        normalized_source = str(source).strip() or "ai_job"

        for index, raw_step in enumerate(steps, start=1):
            if not isinstance(raw_step, dict):
                result = VerificationStepResult(
                    step_index=index,
                    tool="unknown",
                    meets_expectation=False,
                    observed="invalid verification step payload",
                )
                self._publish_result(
                    incident_id=normalized_incident_id,
                    source=normalized_source,
                    actor=actor,
                    params={},
                    result=result,
                )
                results.append(result)
                continue

            tool_name = str(raw_step.get("tool") or "").strip() or "unknown"
            params = raw_step.get("params")
            step_params = params if isinstance(params, dict) else {}
            expected = raw_step.get("expected")
            expected_obj = expected if isinstance(expected, dict) else {}

            call_result: dict[str, Any] | None = None
            call_error: str | None = None
            try:
                call_tool = _normalize_tool_name(tool_name)
                idempotency_key = (
                    f"orchestrator-verification-{normalized_incident_id}-{index}-{uuid.uuid4().hex}"
                )
                call_result = self._execute_with_retry(
                    f"verification.call:{tool_name}:step={index}",
                    lambda: self._client.mcp_client.call(
                        tool=call_tool,
                        input_payload=step_params,
                        idempotency_key=idempotency_key,
                    ),
                )
            except Exception as exc:  # noqa: BLE001
                call_error = str(exc)

            meets_expectation, observed = self._evaluate_step(
                expected=expected_obj,
                call_result=call_result,
                call_error=call_error,
            )
            result = VerificationStepResult(
                step_index=index,
                tool=tool_name,
                meets_expectation=meets_expectation,
                observed=observed,
            )
            self._publish_result(
                incident_id=normalized_incident_id,
                source=normalized_source,
                actor=actor,
                params=step_params,
                result=result,
            )
            if self._log_func is not None:
                self._log_func(
                    "verification step "
                    f"incident_id={normalized_incident_id} step={result.step_index} "
                    f"tool={result.tool} meets_expectation={int(result.meets_expectation)}"
                )
            results.append(result)
        return results

    def _publish_result(
        self,
        *,
        incident_id: str,
        source: str,
        actor: str | None,
        params: dict[str, Any],
        result: VerificationStepResult,
    ) -> None:
        self._execute_with_retry(
            f"verification.publish:{result.tool}:step={result.step_index}",
            lambda: self._client.create_incident_verification_run(
                incident_id=incident_id,
                source=source,
                step_index=result.step_index,
                tool=result.tool,
                params_json=params,
                observed=result.observed,
                meets_expectation=result.meets_expectation,
                actor=actor,
            ),
        )

    def _evaluate_step(
        self,
        *,
        expected: dict[str, Any],
        call_result: dict[str, Any] | None,
        call_error: str | None,
    ) -> tuple[bool, str]:
        expected_type = str(expected.get("type") or "").strip()
        if call_error:
            return False, _truncate_text(f"tool execution failed: {call_error}")

        if expected_type == "exists":
            exists = _result_exists(call_result)
            if exists:
                return True, "expected exists matched: non-empty result"
            return False, "expected exists not matched: result is empty"

        if expected_type == "contains_keyword":
            keyword = str(expected.get("keyword") or "").strip()
            if not keyword:
                return False, "expected contains_keyword not matched: keyword is empty"
            payload_for_match = _extract_result_payload(call_result)
            text = _to_json_preview(payload_for_match, max_len=2048).lower()
            matched = keyword.lower() in text
            if matched:
                return True, _truncate_text(f"keyword matched: {keyword}")
            return False, _truncate_text(f"keyword not found: {keyword}; observed={_to_json_preview(payload_for_match)}")

        return False, _truncate_text(f"unsupported expected.type={expected_type!r}; conservative false")
