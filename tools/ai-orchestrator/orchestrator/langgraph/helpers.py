from __future__ import annotations

import json
import time
from typing import Any

from ..state import GraphState
from .config import OrchestratorConfig
from .llm_logging import log_llm_dialogue


def extract_incident_id(job_obj: dict[str, Any]) -> str:
    incident_id = str(job_obj.get("incidentID") or job_obj.get("incident_id") or "").strip()
    if not incident_id:
        raise RuntimeError(f"incident_id missing in job payload: {job_obj}")
    return incident_id


def coerce_bool(v: Any) -> bool:
    if isinstance(v, bool):
        return v
    if isinstance(v, (int, float)):
        return int(v) != 0
    if isinstance(v, str):
        return v.strip().lower() in {"1", "true", "yes", "y", "on"}
    return False


def extract_input_hints(job_obj: dict[str, Any]) -> dict[str, Any]:
    raw = (
        job_obj.get("inputHintsJSON")
        or job_obj.get("input_hints_json")
        or job_obj.get("inputHints")
        or job_obj.get("input_hints")
    )
    if isinstance(raw, dict):
        return raw
    if not isinstance(raw, str):
        return {}

    trimmed = raw.strip()
    if not trimmed:
        return {}
    try:
        parsed = json.loads(trimmed)
    except json.JSONDecodeError:
        return {}
    if isinstance(parsed, dict):
        return parsed
    return {}


def resolve_force_switches(hints: dict[str, Any], cfg: OrchestratorConfig) -> tuple[bool, bool]:
    force_no_evidence = cfg.force_no_evidence
    force_no_evidence = force_no_evidence or coerce_bool(hints.get("FORCE_NO_EVIDENCE"))
    force_no_evidence = force_no_evidence or coerce_bool(hints.get("force_no_evidence"))

    force_conflict = cfg.force_conflict
    force_conflict = force_conflict or coerce_bool(hints.get("FORCE_CONFLICT"))
    force_conflict = force_conflict or coerce_bool(hints.get("force_conflict"))
    return force_no_evidence, force_conflict


def coerce_non_negative_int(value: Any, default: int) -> int:
    if isinstance(value, bool):
        return max(int(default), 0)
    try:
        return max(int(value), 0)
    except (TypeError, ValueError):
        return max(int(default), 0)


def resolve_a3_budget(hints: dict[str, Any], cfg: OrchestratorConfig) -> tuple[int, int, int]:
    max_calls = coerce_non_negative_int(
        hints.get("A3_MAX_CALLS", hints.get("a3_max_calls")),
        cfg.a3_max_calls,
    )
    max_total_bytes = coerce_non_negative_int(
        hints.get("A3_MAX_TOTAL_BYTES", hints.get("a3_max_total_bytes")),
        cfg.a3_max_total_bytes,
    )
    max_total_latency_ms = coerce_non_negative_int(
        hints.get("A3_MAX_TOTAL_LATENCY_MS", hints.get("a3_max_total_latency_ms")),
        cfg.a3_max_total_latency_ms,
    )
    return max_calls, max_total_bytes, max_total_latency_ms


def ordered_unique_strings(values: list[str]) -> list[str]:
    out: list[str] = []
    seen: set[str] = set()
    for raw in values:
        value = str(raw).strip()
        if not value or value in seen:
            continue
        seen.add(value)
        out.append(value)
    return out


def append_evidence(state: GraphState, evidence_id: str, source: str, no_data: bool, conflict_hint: bool = False) -> None:
    normalized_id = str(evidence_id).strip()
    if normalized_id and normalized_id not in state.evidence_ids:
        state.evidence_ids.append(normalized_id)

    state.evidence_meta.append(
        {
            "evidence_id": normalized_id,
            "source": str(source).strip() or "unknown",
            "no_data": bool(no_data),
            "conflict_hint": bool(conflict_hint),
        }
    )


def query_result_is_no_data(result: dict[str, Any]) -> bool:
    if not isinstance(result, dict):
        return False

    for key in ("rows", "series", "result", "samples"):
        value = result.get(key)
        if isinstance(value, list):
            return len(value) == 0

    raw_json = result.get("queryResultJSON")
    if not isinstance(raw_json, str) or not raw_json.strip():
        return False

    try:
        payload = json.loads(raw_json)
    except json.JSONDecodeError:
        return False

    data = payload.get("data") if isinstance(payload, dict) else None
    if isinstance(data, dict):
        query_result = data.get("result")
        if isinstance(query_result, list):
            return len(query_result) == 0
    return False


def query_result_size_bytes(result: dict[str, Any]) -> int:
    if not isinstance(result, dict):
        return 0
    size = result.get("resultSizeBytes")
    if isinstance(size, (int, float)):
        return max(int(size), 0)
    size_alt = result.get("result_size_bytes")
    if isinstance(size_alt, (int, float)):
        return max(int(size_alt), 0)

    raw_json = result.get("queryResultJSON")
    if isinstance(raw_json, str):
        return len(raw_json.encode("utf-8"))

    compact = json.dumps(result, ensure_ascii=False, separators=(",", ":"))
    return len(compact.encode("utf-8"))


def _pick_text(source: dict[str, Any], *keys: str, max_len: int | None = None) -> str:
    if not isinstance(source, dict):
        return ""
    for key in keys:
        value = source.get(key)
        if value is None:
            continue
        text = str(value).strip()
        if not text:
            continue
        if max_len is not None and max_len >= 0 and len(text) > max_len:
            text = f"{text[: max_len - 3]}..."
        return text
    return ""


def _pick_bool(source: dict[str, Any], *keys: str) -> bool | None:
    if not isinstance(source, dict):
        return None
    for key in keys:
        if key not in source:
            continue
        value = source.get(key)
        if isinstance(value, bool):
            return value
        if isinstance(value, (int, float)):
            return bool(int(value))
        if isinstance(value, str):
            normalized = value.strip().lower()
            if normalized in {"true", "1", "yes", "y", "on"}:
                return True
            if normalized in {"false", "0", "no", "n", "off"}:
                return False
    return None


def build_incident_context(
    incident_obj: dict[str, Any],
    alert_event_obj: dict[str, Any] | None = None,
) -> dict[str, Any]:
    if not isinstance(incident_obj, dict):
        incident_obj = {}
    if not isinstance(alert_event_obj, dict):
        alert_event_obj = {}

    context: dict[str, Any] = {}

    def put(key: str, value: Any) -> None:
        if value is None:
            return
        if isinstance(value, bool):
            context[key] = value
            return
        text = str(value).strip()
        if text:
            context[key] = text

    put("incident_id", _pick_text(incident_obj, "incidentID", "incident_id"))
    put("service", _pick_text(incident_obj, "service"))
    put("namespace", _pick_text(incident_obj, "namespace"))
    put("severity", _pick_text(incident_obj, "severity"))
    put("cluster", _pick_text(incident_obj, "cluster"))
    put("environment", _pick_text(incident_obj, "environment"))
    put("workload_kind", _pick_text(incident_obj, "workloadKind", "workload_kind"))
    put("workload_name", _pick_text(incident_obj, "workloadName", "workload_name"))
    put("pod", _pick_text(incident_obj, "pod"))
    put("node", _pick_text(incident_obj, "node"))
    put("version", _pick_text(incident_obj, "version"))
    put("source", _pick_text(incident_obj, "source"))
    put("alert_name", _pick_text(incident_obj, "alertName", "alert_name"))
    put("fingerprint", _pick_text(incident_obj, "fingerprint"))
    put("rule_id", _pick_text(incident_obj, "ruleID", "rule_id"))
    put("status", _pick_text(incident_obj, "status"))
    put("rca_status", _pick_text(incident_obj, "rcaStatus", "rca_status"))
    put("root_cause_summary", _pick_text(incident_obj, "rootCauseSummary", "root_cause_summary"))
    put("root_cause_type", _pick_text(incident_obj, "rootCauseType", "root_cause_type"))
    put("trace_id", _pick_text(incident_obj, "traceID", "trace_id"))
    put("log_trace_key", _pick_text(incident_obj, "logTraceKey", "log_trace_key"))
    put("change_id", _pick_text(incident_obj, "changeID", "change_id"))
    put("start_at", _pick_text(incident_obj, "startAt", "start_at"))
    put("end_at", _pick_text(incident_obj, "endAt", "end_at"))
    put("labels_json", _pick_text(incident_obj, "labelsJSON", "labels_json", max_len=2048))
    put("annotations_json", _pick_text(incident_obj, "annotationsJSON", "annotations_json", max_len=2048))

    # last_seen_at from incident (canonical field in summary contract)
    put("last_seen_at", _pick_text(incident_obj, "lastSeenAt", "last_seen_at"))

    put("alert_event_id", _pick_text(alert_event_obj, "eventID", "event_id"))
    put("alert_service", _pick_text(alert_event_obj, "service"))
    put("alert_namespace", _pick_text(alert_event_obj, "namespace"))
    put("alert_cluster", _pick_text(alert_event_obj, "cluster"))
    put("alert_workload", _pick_text(alert_event_obj, "workload"))
    put("alert_status", _pick_text(alert_event_obj, "status"))
    put("alert_severity", _pick_text(alert_event_obj, "severity"))
    put("alert_name", _pick_text(alert_event_obj, "alertName", "alert_name"))
    put("alert_dedup_key", _pick_text(alert_event_obj, "dedupKey", "dedup_key"))
    put("alert_source", _pick_text(alert_event_obj, "source"))
    put("alert_last_seen_at", _pick_text(alert_event_obj, "lastSeenAt", "last_seen_at"))
    put("alert_created_at", _pick_text(alert_event_obj, "createdAt", "created_at"))
    put("alert_updated_at", _pick_text(alert_event_obj, "updatedAt", "updated_at"))
    put("alert_starts_at", _pick_text(alert_event_obj, "startsAt", "starts_at"))
    put("alert_ends_at", _pick_text(alert_event_obj, "endsAt", "ends_at"))
    put("alert_acked_at", _pick_text(alert_event_obj, "ackedAt", "acked_at"))
    put("alert_acked_by", _pick_text(alert_event_obj, "ackedBy", "acked_by"))
    put("alert_is_current", _pick_bool(alert_event_obj, "isCurrent", "is_current"))
    put("alert_is_silenced", _pick_bool(alert_event_obj, "isSilenced", "is_silenced"))
    put("alert_silence_id", _pick_text(alert_event_obj, "silenceID", "silence_id"))
    put("alert_labels_json", _pick_text(alert_event_obj, "labelsJSON", "labels_json", max_len=2048))
    put("alert_annotations_json", _pick_text(alert_event_obj, "annotationsJSON", "annotations_json", max_len=2048))

    raw_event_json = _pick_text(alert_event_obj, "rawEventJSON", "raw_event_json", max_len=65536)

    alert_trace_id = _pick_text(alert_event_obj, "traceID", "trace_id")
    if not context.get("trace_id") and alert_trace_id:
        put("trace_id", alert_trace_id)

    # Add has_* flags for presence detection
    # Check both incident-side and alert-side labels/annotations
    incident_labels = context.get("labels_json")
    alert_labels = context.get("alert_labels_json")
    context["has_labels_json"] = bool(
        (incident_labels and incident_labels.strip()) or
        (alert_labels and alert_labels.strip())
    )

    incident_annotations = context.get("annotations_json")
    alert_annotations = context.get("alert_annotations_json")
    context["has_annotations_json"] = bool(
        (incident_annotations and incident_annotations.strip()) or
        (alert_annotations and alert_annotations.strip())
    )

    context["has_raw_event"] = bool(raw_event_json and raw_event_json.strip())

    # Check trace_id from structured fields only (schema-agnostic)
    # Raw JSON substring matching is intentionally avoided to prevent
    # false positives/negatives from encoded text or nested structures.
    # If trace info only exists in raw JSON, observability agent can
    # still access it via raw_alert_excerpt.
    context["has_trace_id"] = bool(context.get("trace_id"))

    # Check change_id presence
    change_id = context.get("change_id")
    context["has_change_id"] = bool(change_id)

    # Add triggered_at from incident or alert event
    triggered_at = (
        _pick_text(incident_obj, "triggeredAt", "triggered_at")
        or _pick_text(alert_event_obj, "triggeredAt", "triggered_at")
        or _pick_text(alert_event_obj, "startsAt", "starts_at")
    )
    if triggered_at:
        context["triggered_at"] = triggered_at

    # Ensure last_seen_at fallback to alert_last_seen_at if not set from incident
    if not context.get("last_seen_at"):
        alert_last_seen = context.get("alert_last_seen_at")
        if alert_last_seen:
            context["last_seen_at"] = alert_last_seen

    return context


def extract_incident_context(incident_obj: dict[str, Any]) -> dict[str, Any]:
    return build_incident_context(incident_obj, None)


def select_current_alert_event(
    current_alert_events: dict[str, Any] | list[Any] | None,
    incident_obj: dict[str, Any] | None = None,
) -> dict[str, Any]:
    if isinstance(current_alert_events, dict):
        candidates = current_alert_events.get("events") or current_alert_events.get("items") or []
    else:
        candidates = current_alert_events or []
    if not isinstance(candidates, list) or not candidates:
        return {}

    normalized_candidates = [item for item in candidates if isinstance(item, dict)]
    if not normalized_candidates:
        return {}

    incident_obj = incident_obj or {}
    incident_fingerprint = _pick_text(incident_obj, "fingerprint")
    incident_id = _pick_text(incident_obj, "incidentID", "incident_id")

    def score(item: dict[str, Any]) -> tuple[int, int, int, int]:
        item_fingerprint = _pick_text(item, "fingerprint")
        item_incident_id = _pick_text(item, "incidentID", "incident_id")
        item_is_current = _pick_bool(item, "isCurrent", "is_current") or False
        exact_fingerprint = 1 if incident_fingerprint and item_fingerprint == incident_fingerprint else 0
        exact_incident = 1 if incident_id and item_incident_id == incident_id else 0
        any_fingerprint = 1 if item_fingerprint else 0
        return (exact_fingerprint, exact_incident, 1 if item_is_current else 0, any_fingerprint)

    normalized_candidates.sort(key=score, reverse=True)
    return normalized_candidates[0]


def append_context_fields(
    context_parts: list[str],
    context: dict[str, Any],
    fields: list[tuple[str, str]],
) -> None:
    if not isinstance(context, dict):
        return
    for label, key in fields:
        value = context.get(key)
        if value is None:
            continue
        text = str(value).strip()
        if text:
            context_parts.append(f"{label}: {text}")


def render_alert_event_excerpt(alert_event_obj: dict[str, Any], max_len: int = 2048) -> str:
    """Render an excerpt of the raw alert event payload.

    This function extracts the rawEventJSON field and returns it as-is,
    without schema-dependent fallbacks. If rawEventJSON is not available,
    returns an empty string to signal absence of raw payload.

    Args:
        alert_event_obj: Alert event dictionary.
        max_len: Maximum length of the excerpt.

    Returns:
        Raw payload excerpt or empty string if not available.
    """
    if not isinstance(alert_event_obj, dict):
        return ""

    raw_payload = _pick_text(alert_event_obj, "rawEventJSON", "raw_event_json", max_len=65536)
    if not raw_payload:
        # Return empty string instead of falling back to full object dump
        # This signals that raw payload is not available and avoids
        # schema-dependent fallback in a layer that should stay schema-agnostic
        return ""

    if max_len >= 0 and len(raw_payload) > max_len:
        if max_len < 4:
            return raw_payload[:max_len]
        return f"{raw_payload[: max_len - 3]}..."
    return raw_payload


def query_toolcall_response(result: dict[str, Any]) -> dict[str, Any]:
    return {
        "result_size_bytes": query_result_size_bytes(result),
        "row_count": int(result.get("rowCount") or result.get("row_count") or 0),
        "is_truncated": bool(result.get("isTruncated") or result.get("is_truncated")),
        "no_data": query_result_is_no_data(result),
    }


def invoke_llm_with_optional_tools(
    llm: Any,
    messages: list[Any],
    openai_tools: list[dict[str, Any]],
    *,
    node_name: str,
    extra: dict[str, Any] | None = None,
) -> Any:
    """Invoke an LLM with tools only when the tool list is non-empty.

    OpenAI-compatible backends reject an empty ``tools`` array. For agents that
    can legitimately run without tools, we must fall back to a plain message
    invoke instead of binding an empty tool list.
    """
    log_llm_dialogue(
        event="request",
        node_name=node_name,
        messages=messages,
        tools=openai_tools,
        extra=extra,
    )
    if openai_tools:
        llm = llm.bind_tools(openai_tools)
    try:
        response = llm.invoke(messages)
    except Exception as exc:  # noqa: BLE001
        log_llm_dialogue(
            event="error",
            node_name=node_name,
            messages=messages,
            tools=openai_tools,
            error=exc,
            extra=extra,
        )
        raise
    log_llm_dialogue(
        event="response",
        node_name=node_name,
        messages=messages,
        tools=openai_tools,
        response=response,
        extra=extra,
    )
    return response


def select_candidate(candidates: list[Any], query_type: str) -> Any | None:
    for candidate in candidates:
        if getattr(candidate, "query_type", "") == query_type:
            return candidate
    return None


def prepare_query_branch_meta(
    *,
    datasource_id: str,
    candidate: Any,
    query_type: str,
) -> dict[str, Any]:
    if candidate is None:
        return {
            "mode": "skip",
            "query_type": query_type,
            "reason": "candidate_not_found",
        }

    now_s = int(time.time())
    window_seconds = max(coerce_non_negative_int(getattr(candidate, "params", {}).get("window_seconds"), 600), 60)
    start_ts = now_s - window_seconds
    end_ts = now_s

    if query_type == "metrics":
        query_expr = str(getattr(candidate, "params", {}).get("expr") or "sum(up)")
        step_seconds = max(coerce_non_negative_int(getattr(candidate, "params", {}).get("step_seconds"), 30), 1)
        return {
            "mode": "query",
            "query_type": "metrics",
            "candidate_name": str(getattr(candidate, "name", "query_metrics:auto")),
            "request_payload": {
                "datasource_id": datasource_id,
                "promql": query_expr,
                "start_ts": start_ts,
                "end_ts": end_ts,
                "step_seconds": step_seconds,
            },
            "query_request": {
                "datasourceID": datasource_id,
                "queryText": query_expr,
                "queryJSON": "{}",
            },
        }

    query_text = str(getattr(candidate, "params", {}).get("query") or '{job=~".+"} |= "error"')
    limit = max(coerce_non_negative_int(getattr(candidate, "params", {}).get("limit"), 200), 1)
    return {
        "mode": "query",
        "query_type": "logs",
        "candidate_name": str(getattr(candidate, "name", "query_logs:auto")),
        "request_payload": {
            "datasource_id": datasource_id,
            "query": query_text,
            "start_ts": start_ts,
            "end_ts": end_ts,
            "limit": limit,
        },
        "query_request": {
            "datasourceID": datasource_id,
            "queryText": query_text,
            "queryJSON": "{}",
        },
    }
