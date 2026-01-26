from __future__ import annotations

import json
import time
from typing import Any

from ..state import GraphState
from .config import OrchestratorConfig


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


def extract_incident_context(incident_obj: dict[str, Any]) -> dict[str, str]:
    if not isinstance(incident_obj, dict):
        return {"service": "", "namespace": "", "severity": ""}
    return {
        "service": str(incident_obj.get("service") or "").strip(),
        "namespace": str(incident_obj.get("namespace") or "").strip(),
        "severity": str(incident_obj.get("severity") or "").strip(),
    }


def query_toolcall_response(result: dict[str, Any]) -> dict[str, Any]:
    return {
        "result_size_bytes": query_result_size_bytes(result),
        "row_count": int(result.get("rowCount") or result.get("row_count") or 0),
        "is_truncated": bool(result.get("isTruncated") or result.get("is_truncated")),
        "no_data": query_result_is_no_data(result),
    }


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
