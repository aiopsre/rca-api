from __future__ import annotations

from dataclasses import dataclass
import json
import time
from typing import Any, Callable
import uuid

from ..tools_rca_api import RCAApiClient


OBSERVED_MAX_LEN = 512


@dataclass(frozen=True)
class VerificationStepResult:
    step_index: int
    tool: str
    meets_expectation: bool
    observed: str


@dataclass(frozen=True)
class VerificationBudget:
    max_steps: int = 20
    max_total_latency_ms: int = 0
    max_total_bytes: int = 0

    def normalized(self) -> "VerificationBudget":
        return VerificationBudget(
            max_steps=max(int(self.max_steps), 0),
            max_total_latency_ms=max(int(self.max_total_latency_ms), 0),
            max_total_bytes=max(int(self.max_total_bytes), 0),
        )


def _normalize_tool_name(tool: str) -> str:
    """Normalize a tool name to canonical dotted form."""
    from ..tooling.canonical_names import normalize_tool_name

    return normalize_tool_name(tool)


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


def _coerce_float(value: Any) -> float | None:
    if isinstance(value, bool):
        return None
    if isinstance(value, (int, float)):
        return float(value)
    if isinstance(value, str):
        try:
            return float(value.strip())
        except ValueError:
            return None
    return None


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


def _first_numeric(value: Any) -> float | None:
    numeric = _coerce_float(value)
    if numeric is not None:
        return numeric
    if isinstance(value, list):
        for item in value:
            nested = _first_numeric(item)
            if nested is not None:
                return nested
    if isinstance(value, dict):
        for item in value.values():
            nested = _first_numeric(item)
            if nested is not None:
                return nested
    return None


def _extract_path(value: Any, field: str) -> Any:
    normalized = str(field).strip()
    if not normalized:
        return None
    cursor = value
    path = normalized.replace("[", ".").replace("]", "")
    for segment in [part for part in path.split(".") if part]:
        if isinstance(cursor, dict):
            if segment in cursor:
                cursor = cursor[segment]
                continue
            return None
        if isinstance(cursor, list):
            try:
                idx = int(segment)
            except ValueError:
                return None
            if idx < 0 or idx >= len(cursor):
                return None
            cursor = cursor[idx]
            continue
        return None
    return cursor


def _extract_numeric_by_key(value: Any, key: str) -> float | None:
    if isinstance(value, dict):
        if key in value:
            numeric = _first_numeric(value.get(key))
            if numeric is not None:
                return numeric
        for nested in value.values():
            numeric = _extract_numeric_by_key(nested, key)
            if numeric is not None:
                return numeric
    elif isinstance(value, list):
        for nested in value:
            numeric = _extract_numeric_by_key(nested, key)
            if numeric is not None:
                return numeric
    return None


def _compact_json(payload: dict[str, Any]) -> str:
    try:
        raw = json.dumps(payload, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    except Exception:  # noqa: BLE001
        raw = json.dumps(
            {
                "status": "error",
                "reason": "observed_serialization_failed",
            },
            ensure_ascii=False,
            sort_keys=True,
            separators=(",", ":"),
        )
    if len(raw) <= OBSERVED_MAX_LEN:
        return raw
    fallback = json.dumps(
        {
            "status": str(payload.get("status") or "truncated"),
            "reason": "observed_exceeds_limit",
            "original_length": len(raw),
        },
        ensure_ascii=False,
        sort_keys=True,
        separators=(",", ":"),
    )
    if len(fallback) <= OBSERVED_MAX_LEN:
        return fallback
    return '{"reason":"observed_exceeds_limit","status":"truncated"}'


def _estimate_payload_bytes(call_payload: Any) -> int:
    payload = _extract_result_payload(call_payload)
    if isinstance(payload, dict):
        raw_query = payload.get("queryResultJSON")
        if isinstance(raw_query, str):
            return len(raw_query.encode("utf-8"))
        for key in ("resultSizeBytes", "responseSizeBytes"):
            value = payload.get(key)
            if isinstance(value, (int, float)):
                return max(int(value), 0)
    try:
        compact = json.dumps(payload, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    except Exception:  # noqa: BLE001
        compact = str(payload)
    return len(compact.encode("utf-8"))


def _dedupe_key(source: str, step_index: int, tool: str) -> tuple[str, int, str]:
    return (
        str(source).strip().lower(),
        int(step_index),
        str(tool).strip().lower(),
    )


class VerificationRunner:
    def __init__(
        self,
        *,
        client: RCAApiClient,
        execute_with_retry: Callable[[str, Callable[[], Any]], Any],
        call_tool: Callable[[str, dict[str, Any], str | None], dict[str, Any]],
        log_func: Callable[[str], None] | None = None,
        budget: VerificationBudget | None = None,
        dedupe_enabled: bool = True,
    ) -> None:
        self._client = client
        self._execute_with_retry = execute_with_retry
        self._call_tool = call_tool
        self._log_func = log_func
        self._budget = (budget or VerificationBudget()).normalized()
        self._dedupe_enabled = bool(dedupe_enabled)

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

        normalized_source = str(source).strip() or "ai_job"
        existing_run_keys: set[tuple[str, int, str]] = set()
        if self._dedupe_enabled:
            existing_run_keys = self._load_existing_run_keys(normalized_incident_id)

        total_steps = len(steps)
        results: list[VerificationStepResult] = []
        executed_steps = 0
        used_latency_ms = 0
        used_bytes = 0

        for index, raw_step in enumerate(steps, start=1):
            budget_reason = self._budget_stop_reason(
                executed_steps=executed_steps,
                used_latency_ms=used_latency_ms,
                used_bytes=used_bytes,
            )
            if budget_reason:
                if index <= total_steps:
                    self._publish_budget_stop(
                        incident_id=normalized_incident_id,
                        source=normalized_source,
                        actor=actor,
                        step_index=index,
                        reason=budget_reason,
                        executed_steps=executed_steps,
                        used_latency_ms=used_latency_ms,
                        used_bytes=used_bytes,
                    )
                break

            if not isinstance(raw_step, dict):
                result = VerificationStepResult(
                    step_index=index,
                    tool="unknown",
                    meets_expectation=False,
                    observed=_compact_json(
                        {
                            "status": "error",
                            "reason": "invalid_step_payload",
                        }
                    ),
                )
                self._publish_result(
                    incident_id=normalized_incident_id,
                    source=normalized_source,
                    actor=actor,
                    params={},
                    result=result,
                )
                existing_run_keys.add(_dedupe_key(normalized_source, index, "unknown"))
                results.append(result)
                continue

            tool_name = str(raw_step.get("tool") or "").strip() or "unknown"
            dedupe_key = _dedupe_key(normalized_source, index, tool_name)
            if dedupe_key in existing_run_keys:
                result = VerificationStepResult(
                    step_index=index,
                    tool=tool_name,
                    meets_expectation=False,
                    observed=_compact_json(
                        {
                            "status": "skipped",
                            "reason": "deduped_existing_verification_run",
                        }
                    ),
                )
                if self._log_func is not None:
                    self._log_func(
                        "verification step skipped "
                        f"incident_id={normalized_incident_id} step={index} tool={tool_name} reason=deduped"
                    )
                results.append(result)
                continue

            step_params = raw_step.get("params")
            params_obj = step_params if isinstance(step_params, dict) else {}
            expected_obj = raw_step.get("expected")
            expected = expected_obj if isinstance(expected_obj, dict) else {}

            call_result: dict[str, Any] | None = None
            call_error: str | None = None
            step_started = time.monotonic()
            try:
                call_tool = _normalize_tool_name(tool_name)
                idempotency_key = (
                    f"orchestrator-verification-{normalized_incident_id}-{index}-{uuid.uuid4().hex}"
                )
                call_result = self._call_tool(
                    call_tool,
                    params_obj,
                    idempotency_key,
                )
            except Exception as exc:  # noqa: BLE001
                call_error = str(exc)

            elapsed_ms = max(1, int((time.monotonic() - step_started) * 1000))
            step_bytes = _estimate_payload_bytes(call_result)
            executed_steps += 1
            used_latency_ms += elapsed_ms
            used_bytes += step_bytes

            meets_expectation, observed_obj = self._evaluate_step(
                expected=expected,
                call_result=call_result,
                call_error=call_error,
            )
            observed_obj["latency_ms"] = elapsed_ms
            observed_obj["result_size_bytes"] = step_bytes
            result = VerificationStepResult(
                step_index=index,
                tool=tool_name,
                meets_expectation=meets_expectation,
                observed=_compact_json(observed_obj),
            )
            self._publish_result(
                incident_id=normalized_incident_id,
                source=normalized_source,
                actor=actor,
                params=params_obj,
                result=result,
            )
            existing_run_keys.add(dedupe_key)
            if self._log_func is not None:
                self._log_func(
                    "verification step "
                    f"incident_id={normalized_incident_id} step={result.step_index} "
                    f"tool={result.tool} meets_expectation={int(result.meets_expectation)}"
                )
            results.append(result)

            budget_reason = self._budget_stop_reason(
                executed_steps=executed_steps,
                used_latency_ms=used_latency_ms,
                used_bytes=used_bytes,
            )
            if budget_reason and index < total_steps:
                self._publish_budget_stop(
                    incident_id=normalized_incident_id,
                    source=normalized_source,
                    actor=actor,
                    step_index=index + 1,
                    reason=budget_reason,
                    executed_steps=executed_steps,
                    used_latency_ms=used_latency_ms,
                    used_bytes=used_bytes,
                )
                break
        return results

    def _load_existing_run_keys(self, incident_id: str) -> set[tuple[str, int, str]]:
        out: set[tuple[str, int, str]] = set()
        page = 1
        limit = 200

        while True:
            payload = self._execute_with_retry(
                f"verification.list_runs:page={page}",
                lambda: self._client.list_incident_verification_runs(
                    incident_id,
                    page=page,
                    limit=limit,
                ),
            )
            if not isinstance(payload, dict):
                break
            runs = payload.get("runs")
            if not isinstance(runs, list):
                runs = []
            for run in runs:
                if not isinstance(run, dict):
                    continue
                source = str(run.get("source") or "").strip()
                tool = str(run.get("tool") or "").strip()
                step_index_raw = run.get("stepIndex")
                if step_index_raw is None:
                    step_index_raw = run.get("step_index")
                try:
                    step_index = int(step_index_raw)
                except (TypeError, ValueError):
                    continue
                if not source or not tool or step_index <= 0:
                    continue
                out.add(_dedupe_key(source, step_index, tool))

            total = payload.get("totalCount")
            if total is None:
                total = payload.get("total_count")
            try:
                total_count = int(total)
            except (TypeError, ValueError):
                total_count = 0

            if not runs or len(runs) < limit:
                break
            if total_count > 0 and page * limit >= total_count:
                break
            page += 1
            if page > 100:
                break

        return out

    def _publish_result(
        self,
        *,
        incident_id: str,
        source: str,
        actor: str | None,
        params: dict[str, Any],
        result: VerificationStepResult,
    ) -> None:
        response = self._execute_with_retry(
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
        if not isinstance(response, dict):
            return
        warnings = response.get("warnings")
        if isinstance(warnings, list) and warnings and self._log_func is not None:
            warning_text = ",".join(str(item).strip() for item in warnings if str(item).strip())
            if warning_text:
                self._log_func(
                    "verification publish warnings "
                    f"incident_id={incident_id} step={result.step_index} tool={result.tool} warnings={warning_text}"
                )

    def _publish_budget_stop(
        self,
        *,
        incident_id: str,
        source: str,
        actor: str | None,
        step_index: int,
        reason: str,
        executed_steps: int,
        used_latency_ms: int,
        used_bytes: int,
    ) -> None:
        observed = _compact_json(
            {
                "status": "stopped",
                "reason": reason,
                "budget": {
                    "max_steps": self._budget.max_steps,
                    "max_total_latency_ms": self._budget.max_total_latency_ms,
                    "max_total_bytes": self._budget.max_total_bytes,
                },
                "used": {
                    "executed_steps": executed_steps,
                    "total_latency_ms": used_latency_ms,
                    "total_bytes": used_bytes,
                },
            }
        )
        self._publish_result(
            incident_id=incident_id,
            source=source,
            actor=actor,
            params={},
            result=VerificationStepResult(
                step_index=max(step_index, 1),
                tool="verification.budget",
                meets_expectation=False,
                observed=observed,
            ),
        )

    def _budget_stop_reason(
        self,
        *,
        executed_steps: int,
        used_latency_ms: int,
        used_bytes: int,
    ) -> str | None:
        if self._budget.max_steps > 0 and executed_steps >= self._budget.max_steps:
            return "max_steps_reached"
        if self._budget.max_total_latency_ms > 0 and used_latency_ms >= self._budget.max_total_latency_ms:
            return "max_total_latency_ms_reached"
        if self._budget.max_total_bytes > 0 and used_bytes >= self._budget.max_total_bytes:
            return "max_total_bytes_reached"
        return None

    def _evaluate_step(
        self,
        *,
        expected: dict[str, Any],
        call_result: dict[str, Any] | None,
        call_error: str | None,
    ) -> tuple[bool, dict[str, Any]]:
        expected_type = str(expected.get("type") or "").strip()
        if call_error:
            return False, {
                "status": "error",
                "reason": "tool_execution_failed",
                "detail": str(call_error)[:200],
                "expected": {"type": expected_type},
            }

        if expected_type == "exists":
            exists = _result_exists(call_result)
            return exists, {
                "status": "ok",
                "reason": "exists_check",
                "matched": exists,
                "expected": {"type": "exists"},
            }

        if expected_type == "contains_keyword":
            keyword = str(expected.get("keyword") or "").strip()
            if not keyword:
                return False, {
                    "status": "error",
                    "reason": "missing_keyword",
                    "expected": {"type": "contains_keyword"},
                }
            payload_for_match = _extract_result_payload(call_result)
            lower_text = ""
            try:
                lower_text = json.dumps(
                    payload_for_match,
                    ensure_ascii=False,
                    sort_keys=True,
                    separators=(",", ":"),
                ).lower()
            except Exception:  # noqa: BLE001
                lower_text = str(payload_for_match).lower()
            matched = keyword.lower() in lower_text
            return matched, {
                "status": "ok",
                "reason": "contains_keyword_check",
                "matched": matched,
                "expected": {"type": "contains_keyword", "keyword": keyword},
            }

        if expected_type in {"threshold_below", "threshold_above"}:
            threshold = _coerce_float(expected.get("value"))
            if threshold is None:
                return False, {
                    "status": "error",
                    "reason": "invalid_threshold_value",
                    "expected": {"type": expected_type},
                }

            field = str(expected.get("field") or "").strip()
            matched, observed = self._evaluate_threshold(
                expected_type=expected_type,
                field=field,
                threshold=threshold,
                call_result=call_result,
            )
            return matched, observed

        return False, {
            "status": "skipped",
            "reason": "unsupported_expected_type",
            "expected": {"type": expected_type},
        }

    def _evaluate_threshold(
        self,
        *,
        expected_type: str,
        field: str,
        threshold: float,
        call_result: dict[str, Any] | None,
    ) -> tuple[bool, dict[str, Any]]:
        payload = _extract_result_payload(call_result)
        parsed_query_payload = None
        if isinstance(payload, dict) and "queryResultJSON" in payload:
            parsed_query_payload = _parse_json_object(payload.get("queryResultJSON"))
        value = None
        resolved_field = field

        if field:
            value_obj = _extract_path(payload, field)
            if value_obj is None and isinstance(parsed_query_payload, dict):
                value_obj = _extract_path(parsed_query_payload, field)
            numeric = _coerce_float(value_obj)
            if numeric is None:
                numeric = _first_numeric(value_obj)
            if numeric is None:
                numeric = _extract_numeric_by_key(payload, field)
            if numeric is None and isinstance(parsed_query_payload, dict):
                numeric = _extract_numeric_by_key(parsed_query_payload, field)
            value = numeric
        else:
            resolved_field = "auto"
            value = _first_numeric(payload)
            if value is None and isinstance(parsed_query_payload, dict):
                value = _first_numeric(parsed_query_payload)

        if value is None:
            return False, {
                "status": "error",
                "reason": "threshold_field_not_resolved",
                "expected": {
                    "type": expected_type,
                    "field": field,
                    "value": threshold,
                },
            }

        if expected_type == "threshold_below":
            matched = value < threshold
        else:
            matched = value > threshold
        return matched, {
            "status": "ok",
            "reason": "threshold_check",
            "matched": matched,
            "expected": {
                "type": expected_type,
                "field": field,
                "value": threshold,
            },
            "observed": {
                "field": resolved_field,
                "value": value,
            },
        }
