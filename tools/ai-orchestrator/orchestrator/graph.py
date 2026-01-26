from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime, timezone
import json
import time
from typing import Any, Callable

from langgraph.graph import END, START, StateGraph

from .evidence_plan import build_candidates, rank_candidates
from .runtime.runtime import OrchestratorRuntime
from .state import GraphState


QUALITY_GATE_PASS = "pass"
QUALITY_GATE_MISSING = "missing"
QUALITY_GATE_CONFLICT = "conflict"


@dataclass
class OrchestratorConfig:
    run_query: bool = False
    force_no_evidence: bool = False
    force_conflict: bool = False
    ds_base_url: str = ""
    auto_create_datasource: bool = True
    a3_max_calls: int = 6
    a3_max_total_bytes: int = 2 * 1024 * 1024
    a3_max_total_latency_ms: int = 8000
    post_finalize_observe: bool = True
    run_verification: bool = False
    verification_source: str = "ai_job"
    post_finalize_wait_timeout_seconds: int = 8
    post_finalize_wait_interval_ms: int = 500
    post_finalize_wait_max_interval_ms: int = 2000


def _extract_incident_id(job_obj: dict[str, Any]) -> str:
    incident_id = str(job_obj.get("incidentID") or job_obj.get("incident_id") or "").strip()
    if not incident_id:
        raise RuntimeError(f"incident_id missing in job payload: {job_obj}")
    return incident_id


def _coerce_bool(v: Any) -> bool:
    if isinstance(v, bool):
        return v
    if isinstance(v, (int, float)):
        return int(v) != 0
    if isinstance(v, str):
        return v.strip().lower() in {"1", "true", "yes", "y", "on"}
    return False


def _extract_input_hints(job_obj: dict[str, Any]) -> dict[str, Any]:
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


def _resolve_force_switches(hints: dict[str, Any], cfg: OrchestratorConfig) -> tuple[bool, bool]:
    force_no_evidence = cfg.force_no_evidence
    force_no_evidence = force_no_evidence or _coerce_bool(hints.get("FORCE_NO_EVIDENCE"))
    force_no_evidence = force_no_evidence or _coerce_bool(hints.get("force_no_evidence"))

    force_conflict = cfg.force_conflict
    force_conflict = force_conflict or _coerce_bool(hints.get("FORCE_CONFLICT"))
    force_conflict = force_conflict or _coerce_bool(hints.get("force_conflict"))
    return force_no_evidence, force_conflict


def _coerce_non_negative_int(value: Any, default: int) -> int:
    if isinstance(value, bool):
        return max(int(default), 0)
    try:
        return max(int(value), 0)
    except (TypeError, ValueError):
        return max(int(default), 0)


def _resolve_a3_budget(hints: dict[str, Any], cfg: OrchestratorConfig) -> tuple[int, int, int]:
    max_calls = _coerce_non_negative_int(
        hints.get("A3_MAX_CALLS", hints.get("a3_max_calls")),
        cfg.a3_max_calls,
    )
    max_total_bytes = _coerce_non_negative_int(
        hints.get("A3_MAX_TOTAL_BYTES", hints.get("a3_max_total_bytes")),
        cfg.a3_max_total_bytes,
    )
    max_total_latency_ms = _coerce_non_negative_int(
        hints.get("A3_MAX_TOTAL_LATENCY_MS", hints.get("a3_max_total_latency_ms")),
        cfg.a3_max_total_latency_ms,
    )
    return max_calls, max_total_bytes, max_total_latency_ms


def _ordered_unique_strings(values: list[str]) -> list[str]:
    out: list[str] = []
    seen: set[str] = set()
    for raw in values:
        value = str(raw).strip()
        if not value or value in seen:
            continue
        seen.add(value)
        out.append(value)
    return out


def _append_evidence(state: GraphState, evidence_id: str, source: str, no_data: bool, conflict_hint: bool = False) -> None:
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


def _query_result_is_no_data(result: dict[str, Any]) -> bool:
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


def _query_result_size_bytes(result: dict[str, Any]) -> int:
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


def _extract_incident_context(incident_obj: dict[str, Any]) -> dict[str, str]:
    if not isinstance(incident_obj, dict):
        return {"service": "", "namespace": "", "severity": ""}
    return {
        "service": str(incident_obj.get("service") or "").strip(),
        "namespace": str(incident_obj.get("namespace") or "").strip(),
        "severity": str(incident_obj.get("severity") or "").strip(),
    }


def _build_quality_gate_evidence_summary(state: GraphState) -> dict[str, Any]:
    sources: list[str] = []
    no_data = 0
    for item in state.evidence_meta:
        source = str(item.get("source") or "").strip()
        if source:
            sources.append(source)
        if _coerce_bool(item.get("no_data")):
            no_data += 1

    if not sources and state.evidence_ids:
        sources = ["unknown"]

    return {
        "total": len(state.evidence_ids),
        "no_data": no_data,
        "sources": _ordered_unique_strings(sources),
    }


def _has_conflict_signal(state: GraphState, evidence_summary: dict[str, Any]) -> bool:
    for item in state.evidence_meta:
        if _coerce_bool(item.get("conflict_hint")):
            return True

    total = int(evidence_summary.get("total") or 0)
    no_data = int(evidence_summary.get("no_data") or 0)
    sources = evidence_summary.get("sources")
    source_count = len(sources) if isinstance(sources, list) else 0
    return total >= 2 and no_data > 0 and no_data < total and source_count >= 2


def _evaluate_quality_gate(state: GraphState) -> tuple[str, list[str], dict[str, Any]]:
    evidence_summary = _build_quality_gate_evidence_summary(state)

    if state.force_conflict:
        reasons = ["FORCE_CONFLICT=1"]
        if state.force_no_evidence:
            reasons.append("FORCE_CONFLICT takes precedence over FORCE_NO_EVIDENCE when both are enabled")
        return QUALITY_GATE_CONFLICT, reasons, evidence_summary

    if state.force_no_evidence:
        return QUALITY_GATE_MISSING, ["FORCE_NO_EVIDENCE=1"], evidence_summary

    if int(evidence_summary.get("total") or 0) < 2:
        return QUALITY_GATE_MISSING, ["insufficient evidence: total evidence records < 2"], evidence_summary

    if int(evidence_summary.get("no_data") or 0) >= int(evidence_summary.get("total") or 0):
        return QUALITY_GATE_MISSING, ["all collected evidence are marked as no_data"], evidence_summary

    if _has_conflict_signal(state, evidence_summary):
        return QUALITY_GATE_CONFLICT, ["conflicting evidence signals detected across collected sources"], evidence_summary

    return QUALITY_GATE_PASS, ["evidence is sufficient and consistent"], evidence_summary


def _ensure_quality_gate(state: GraphState) -> dict[str, Any]:
    if state.quality_gate_decision and state.quality_gate_reasons:
        if not state.quality_gate_evidence_summary:
            state.quality_gate_evidence_summary = _build_quality_gate_evidence_summary(state)
        return {
            "decision": state.quality_gate_decision,
            "reasons": state.quality_gate_reasons,
            "evidence_summary": state.quality_gate_evidence_summary,
        }

    decision, reasons, evidence_summary = _evaluate_quality_gate(state)
    state.quality_gate_decision = decision
    state.quality_gate_reasons = reasons
    state.quality_gate_evidence_summary = evidence_summary
    return {
        "decision": decision,
        "reasons": reasons,
        "evidence_summary": evidence_summary,
    }


def _query_toolcall_response(result: dict[str, Any]) -> dict[str, Any]:
    return {
        "result_size_bytes": _query_result_size_bytes(result),
        "row_count": int(result.get("rowCount") or result.get("row_count") or 0),
        "is_truncated": bool(result.get("isTruncated") or result.get("is_truncated")),
        "no_data": _query_result_is_no_data(result),
    }


def _report_node_action(
    state: GraphState,
    runtime: OrchestratorRuntime,
    *,
    node_name: str,
    tool_name: str,
    request_json: dict[str, Any],
    response_json: dict[str, Any] | None,
    started_ms: int,
    status: str,
    error: str | None = None,
    evidence_ids: list[str] | None = None,
    count_in_state: bool = True,
) -> None:
    runtime.report_tool_call(
        node_name=node_name,
        tool_name=tool_name,
        request_json=request_json,
        response_json=response_json,
        latency_ms=max(1, int(time.time() * 1000) - started_ms),
        status=status,
        error=error,
        evidence_ids=evidence_ids,
    )
    if count_in_state:
        state.tool_calls_written += 1


def _select_candidate(candidates: list[Any], query_type: str) -> Any | None:
    for candidate in candidates:
        if getattr(candidate, "query_type", "") == query_type:
            return candidate
    return None


def _prepare_query_branch_meta(
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
    window_seconds = max(_coerce_non_negative_int(getattr(candidate, "params", {}).get("window_seconds"), 600), 60)
    start_ts = now_s - window_seconds
    end_ts = now_s

    if query_type == "metrics":
        query_expr = str(getattr(candidate, "params", {}).get("expr") or "sum(up)")
        step_seconds = max(_coerce_non_negative_int(getattr(candidate, "params", {}).get("step_seconds"), 30), 1)
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
    limit = max(_coerce_non_negative_int(getattr(candidate, "params", {}).get("limit"), 200), 1)
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


def load_job_and_start(
    state: GraphState,
    cfg: OrchestratorConfig,
    runtime: OrchestratorRuntime,
) -> GraphState:
    started_ms = int(time.time() * 1000)
    request_payload = {
        "job_id": state.job_id,
        "instance_id": state.instance_id,
        "action": "start_claim_and_load_job",
    }

    job = runtime.get_job(state.job_id)
    state.incident_id = _extract_incident_id(job)
    hints = _extract_input_hints(job)
    state.input_hints = hints
    state.force_no_evidence, state.force_conflict = _resolve_force_switches(hints, cfg)
    state.a3_max_calls, state.a3_max_total_bytes, state.a3_max_total_latency_ms = _resolve_a3_budget(hints, cfg)
    state.started = True

    _report_node_action(
        state,
        runtime,
        node_name="load_job_and_start",
        tool_name="job.start_claim",
        request_json=request_payload,
        response_json={
            "status": "ok",
            "incident_id": state.incident_id,
            "heartbeat_initial": "active",
        },
        started_ms=started_ms,
        status="ok",
    )
    return state


def plan_evidence(
    state: GraphState,
    cfg: OrchestratorConfig,
    runtime: OrchestratorRuntime,
) -> GraphState:
    if not state.incident_id:
        raise RuntimeError("incident_id is required before plan_evidence")

    state.evidence_ids = []
    state.evidence_meta = []
    state.missing_evidence = []
    state.evidence_candidates = []
    state.metrics_query_output = {}
    state.logs_query_output = {}
    state.metrics_query_status = None
    state.logs_query_status = None
    state.metrics_query_error = None
    state.logs_query_error = None
    state.metrics_query_latency_ms = 0
    state.logs_query_latency_ms = 0
    state.metrics_query_result_size_bytes = 0
    state.logs_query_result_size_bytes = 0

    if state.force_conflict:
        state.evidence_mode = "force_conflict"
    elif state.force_no_evidence:
        state.evidence_mode = "force_no_evidence"
    elif not cfg.run_query:
        state.evidence_mode = "mock"
    else:
        state.evidence_mode = "query"

    state.evidence_plan = {
        "version": "a3",
        "budget": {
            "max_calls": state.a3_max_calls,
            "max_total_bytes": state.a3_max_total_bytes,
            "max_total_latency_ms": state.a3_max_total_latency_ms,
        },
        "used": {
            "calls": 0,
            "total_bytes": 0,
            "total_latency_ms": 0,
        },
        "candidates": [],
        "executed": [],
        "skipped": [],
    }

    if state.evidence_mode == "query":
        if not cfg.ds_base_url.strip():
            raise RuntimeError("RUN_QUERY=1 requires DS_BASE_URL")
        if not cfg.auto_create_datasource:
            raise RuntimeError("AUTO_CREATE_DATASOURCE=0 is not supported in P0 without preloaded datasource ID")

        incident_context = {"service": "", "namespace": "", "severity": ""}
        try:
            incident_context = _extract_incident_context(runtime.get_incident(state.incident_id))
        except Exception:  # noqa: BLE001
            pass
        state.incident_context = incident_context

        datasource_id = runtime.ensure_datasource(cfg.ds_base_url)
        state.datasource_id = datasource_id

        planning_context: dict[str, Any] = {
            "incident_id": state.incident_id,
            "service": incident_context.get("service", ""),
            "namespace": incident_context.get("namespace", ""),
            "severity": incident_context.get("severity", ""),
        }
        candidates = rank_candidates(build_candidates(planning_context), planning_context)
        state.evidence_candidates = [item.to_plan_dict() for item in candidates]
        state.evidence_plan["candidates"] = state.evidence_candidates

        metrics_candidate = _select_candidate(candidates, "metrics")
        logs_candidate = _select_candidate(candidates, "logs")
        state.metrics_branch_meta = _prepare_query_branch_meta(
            datasource_id=datasource_id,
            candidate=metrics_candidate,
            query_type="metrics",
        )
        state.logs_branch_meta = _prepare_query_branch_meta(
            datasource_id=datasource_id,
            candidate=logs_candidate,
            query_type="logs",
        )
    elif state.evidence_mode == "force_conflict":
        state.missing_evidence = [
            "align metrics/logs/traces time window and re-query within the same interval",
            "collect error logs (5xx/timeout/panic/OOM) during the metric spike",
            "collect upstream/downstream traces or confirm tracing sampling/drop (RUN_QUERY=0 uses placeholders)",
        ]
        state.metrics_branch_meta = {
            "mode": "mock",
            "query_type": "metrics",
            "raw": {
                "source": "orchestrator",
                "mode": "forced_conflict",
                "dimension": "metrics",
                "kind": "mock_conflict_signal",
                "observed": "5xx and latency increased in the same time window",
                "reason": "FORCE_CONFLICT=1",
            },
            "summary": "FORCE_CONFLICT metrics placeholder: error rate spike observed.",
            "query_hash_source": {
                "mode": "forced_conflict",
                "kind": "metrics",
                "query_text": "mock://orchestrator",
            },
            "no_data": False,
            "conflict_hint": True,
        }
        state.logs_branch_meta = {
            "mode": "mock",
            "query_type": "logs",
            "raw": {
                "source": "orchestrator",
                "mode": "forced_conflict",
                "dimension": "logs_traces",
                "kind": "no_data",
                "observed": "logs/traces do not corroborate metric anomaly in this window",
                "reason": "FORCE_CONFLICT=1; datasource query skipped and placeholders persisted",
            },
            "summary": "FORCE_CONFLICT logs/trace placeholder: no corroborating error evidence.",
            "query_hash_source": {
                "mode": "forced_conflict",
                "kind": "logs",
                "query_text": "mock://orchestrator",
            },
            "no_data": True,
            "conflict_hint": True,
        }
    elif state.evidence_mode == "force_no_evidence":
        state.missing_evidence = ["logs", "traces"]
        state.metrics_branch_meta = {
            "mode": "mock",
            "query_type": "metrics",
            "raw": {
                "source": "orchestrator",
                "mode": "forced_missing_evidence",
                "kind": "no_data",
                "missing": state.missing_evidence,
                "reason": "FORCE_NO_EVIDENCE=1",
            },
            "summary": "no evidence found (forced)",
            "query_hash_source": {
                "mode": "forced_missing_evidence",
                "kind": "metrics",
                "query_text": "mock://orchestrator",
            },
            "no_data": True,
            "conflict_hint": False,
        }
        state.logs_branch_meta = {
            "mode": "skip",
            "query_type": "logs",
            "reason": "forced_missing_evidence",
        }
    else:
        state.metrics_branch_meta = {
            "mode": "mock",
            "query_type": "metrics",
            "raw": {
                "source": "orchestrator",
                "mode": "mock",
                "kind": "metrics_signal",
                "observed": "5xx and latency increased in the incident window",
            },
            "summary": "P0 mock metrics evidence saved by orchestrator (RUN_QUERY=0).",
            "query_hash_source": {
                "mode": "mock",
                "kind": "metrics",
                "query_text": "mock://orchestrator",
            },
            "no_data": False,
            "conflict_hint": False,
        }
        state.logs_branch_meta = {
            "mode": "mock",
            "query_type": "logs",
            "raw": {
                "source": "orchestrator",
                "mode": "mock",
                "kind": "logs_signal",
                "observed": "error logs align with metric spike in the same window",
            },
            "summary": "P0 mock logs evidence saved by orchestrator (RUN_QUERY=0).",
            "query_hash_source": {
                "mode": "mock",
                "kind": "logs",
                "query_text": "mock://orchestrator",
            },
            "no_data": False,
            "conflict_hint": False,
        }

    started_ms = int(time.time() * 1000)
    _report_node_action(
        state,
        runtime,
        node_name="plan_evidence",
        tool_name="evidence.plan",
        request_json={
            "incident_id": state.incident_id,
            "mode": state.evidence_mode,
            "run_query": cfg.run_query,
        },
        response_json={
            "status": "ok",
            "mode": state.evidence_mode,
            "datasource_id": state.datasource_id,
            "metrics_branch_mode": state.metrics_branch_meta.get("mode"),
            "logs_branch_mode": state.logs_branch_meta.get("mode"),
            "candidates": state.evidence_candidates,
        },
        started_ms=started_ms,
        status="ok",
    )
    return state


def query_metrics_node(state: GraphState, runtime: OrchestratorRuntime) -> dict[str, Any]:
    meta = state.metrics_branch_meta if isinstance(state.metrics_branch_meta, dict) else {}
    mode = str(meta.get("mode") or "skip")
    started_ms = int(time.time() * 1000)

    if mode == "skip":
        _report_node_action(
            state,
            runtime,
            node_name="query_metrics",
            tool_name="evidence.metrics.skip",
            request_json={"reason": str(meta.get("reason") or "skipped")},
            response_json={"status": "skipped"},
            started_ms=started_ms,
            status="ok",
            count_in_state=False,
        )
        return {
            "metrics_query_status": "skipped",
            "metrics_query_output": {},
            "metrics_query_error": str(meta.get("reason") or "skipped"),
            "metrics_query_latency_ms": 0,
            "metrics_query_result_size_bytes": 0,
        }

    if mode == "mock":
        _report_node_action(
            state,
            runtime,
            node_name="query_metrics",
            tool_name="evidence.metrics.mock",
            request_json={"mode": "mock", "query_type": "metrics"},
            response_json={"status": "ok", "mode": "mock", "no_data": bool(meta.get("no_data"))},
            started_ms=started_ms,
            status="ok",
            count_in_state=False,
        )
        raw = meta.get("raw")
        return {
            "metrics_query_status": "ok",
            "metrics_query_output": raw if isinstance(raw, dict) else {},
            "metrics_query_error": None,
            "metrics_query_latency_ms": 0,
            "metrics_query_result_size_bytes": _query_result_size_bytes(raw if isinstance(raw, dict) else {}),
        }

    request_payload = meta.get("request_payload") if isinstance(meta.get("request_payload"), dict) else {}
    try:
        result = runtime.query_metrics(
            datasource_id=str(request_payload.get("datasource_id") or ""),
            promql=str(request_payload.get("promql") or "sum(up)"),
            start_ts=int(request_payload.get("start_ts") or int(time.time()) - 600),
            end_ts=int(request_payload.get("end_ts") or int(time.time())),
            step_s=max(int(request_payload.get("step_seconds") or 30), 1),
        )
    except Exception as exc:  # noqa: BLE001
        _report_node_action(
            state,
            runtime,
            node_name="query_metrics",
            tool_name="mcp.query_metrics",
            request_json=request_payload,
            response_json={"status": "error"},
            started_ms=started_ms,
            status="error",
            error=str(exc)[:512],
            count_in_state=False,
        )
        return {
            "metrics_query_status": "error",
            "metrics_query_output": {},
            "metrics_query_error": str(exc)[:512],
            "metrics_query_latency_ms": max(1, int(time.time() * 1000) - started_ms),
            "metrics_query_result_size_bytes": 0,
        }

    _report_node_action(
        state,
        runtime,
        node_name="query_metrics",
        tool_name="mcp.query_metrics",
        request_json=request_payload,
        response_json=_query_toolcall_response(result),
        started_ms=started_ms,
        status="ok",
        count_in_state=False,
    )
    return {
        "metrics_query_status": "ok",
        "metrics_query_output": result,
        "metrics_query_error": None,
        "metrics_query_latency_ms": max(1, int(time.time() * 1000) - started_ms),
        "metrics_query_result_size_bytes": _query_result_size_bytes(result),
    }


def query_logs_node(state: GraphState, runtime: OrchestratorRuntime) -> dict[str, Any]:
    meta = state.logs_branch_meta if isinstance(state.logs_branch_meta, dict) else {}
    mode = str(meta.get("mode") or "skip")
    started_ms = int(time.time() * 1000)

    if mode == "skip":
        _report_node_action(
            state,
            runtime,
            node_name="query_logs",
            tool_name="evidence.logs.skip",
            request_json={"reason": str(meta.get("reason") or "skipped")},
            response_json={"status": "skipped"},
            started_ms=started_ms,
            status="ok",
            count_in_state=False,
        )
        return {
            "logs_query_status": "skipped",
            "logs_query_output": {},
            "logs_query_error": str(meta.get("reason") or "skipped"),
            "logs_query_latency_ms": 0,
            "logs_query_result_size_bytes": 0,
        }

    if mode == "mock":
        _report_node_action(
            state,
            runtime,
            node_name="query_logs",
            tool_name="evidence.logs.mock",
            request_json={"mode": "mock", "query_type": "logs"},
            response_json={"status": "ok", "mode": "mock", "no_data": bool(meta.get("no_data"))},
            started_ms=started_ms,
            status="ok",
            count_in_state=False,
        )
        raw = meta.get("raw")
        return {
            "logs_query_status": "ok",
            "logs_query_output": raw if isinstance(raw, dict) else {},
            "logs_query_error": None,
            "logs_query_latency_ms": 0,
            "logs_query_result_size_bytes": _query_result_size_bytes(raw if isinstance(raw, dict) else {}),
        }

    request_payload = meta.get("request_payload") if isinstance(meta.get("request_payload"), dict) else {}
    try:
        result = runtime.query_logs(
            datasource_id=str(request_payload.get("datasource_id") or ""),
            query=str(request_payload.get("query") or '{job=~".+"} |= "error"'),
            start_ts=int(request_payload.get("start_ts") or int(time.time()) - 600),
            end_ts=int(request_payload.get("end_ts") or int(time.time())),
            limit=max(int(request_payload.get("limit") or 200), 1),
        )
    except Exception as exc:  # noqa: BLE001
        _report_node_action(
            state,
            runtime,
            node_name="query_logs",
            tool_name="mcp.query_logs",
            request_json=request_payload,
            response_json={"status": "error"},
            started_ms=started_ms,
            status="error",
            error=str(exc)[:512],
            count_in_state=False,
        )
        return {
            "logs_query_status": "error",
            "logs_query_output": {},
            "logs_query_error": str(exc)[:512],
            "logs_query_latency_ms": max(1, int(time.time() * 1000) - started_ms),
            "logs_query_result_size_bytes": 0,
        }

    _report_node_action(
        state,
        runtime,
        node_name="query_logs",
        tool_name="mcp.query_logs",
        request_json=request_payload,
        response_json=_query_toolcall_response(result),
        started_ms=started_ms,
        status="ok",
        count_in_state=False,
    )
    return {
        "logs_query_status": "ok",
        "logs_query_output": result,
        "logs_query_error": None,
        "logs_query_latency_ms": max(1, int(time.time() * 1000) - started_ms),
        "logs_query_result_size_bytes": _query_result_size_bytes(result),
    }


def _save_branch_evidence(
    *,
    state: GraphState,
    runtime: OrchestratorRuntime,
    branch_name: str,
    kind: str,
    meta: dict[str, Any],
    status: str,
    output: dict[str, Any],
    error: str | None,
) -> None:
    mode = str(meta.get("mode") or "skip")

    if mode == "skip":
        if state.evidence_plan.get("skipped") is None:
            state.evidence_plan["skipped"] = []
        if isinstance(state.evidence_plan.get("skipped"), list):
            state.evidence_plan["skipped"].append(
                {
                    "name": f"{branch_name}:skip",
                    "reason": str(meta.get("reason") or "skipped"),
                }
            )
        return

    if mode == "mock":
        started_ms = int(time.time() * 1000)
        saved = runtime.save_mock_evidence(
            incident_id=state.incident_id or "",
            node_name="merge_evidence",
            kind=kind,
            summary=str(meta.get("summary") or f"mock {kind} evidence"),
            raw=meta.get("raw") if isinstance(meta.get("raw"), dict) else {"kind": kind, "mode": "mock"},
            query_hash_source=meta.get("query_hash_source"),
        )
        evidence_id = saved.evidence_id
        no_data = bool(meta.get("no_data"))
        conflict_hint = bool(meta.get("conflict_hint"))
        _append_evidence(state, evidence_id, source=kind, no_data=no_data, conflict_hint=conflict_hint)
        _report_node_action(
            state,
            runtime,
            node_name="merge_evidence",
            tool_name="evidence.save_mock",
            request_json={
                "incident_id": state.incident_id,
                "kind": kind,
                "idempotency_key": saved.idempotency_key,
                "created_by": saved.created_by,
            },
            response_json={"status": "ok", "evidence_id": evidence_id, "no_data": no_data},
            started_ms=started_ms,
            status="ok",
            evidence_ids=[evidence_id],
        )
        return

    if status != "ok":
        if isinstance(state.evidence_plan.get("skipped"), list):
            state.evidence_plan["skipped"].append(
                {
                    "name": str(meta.get("candidate_name") or f"query_{kind}:auto"),
                    "reason": "query_failed",
                    "detail": str(error or "query_failed")[:200],
                }
            )
        return

    query_request = meta.get("query_request") if isinstance(meta.get("query_request"), dict) else {}
    started_ms = int(time.time() * 1000)
    published = runtime.save_evidence_from_query(
        incident_id=state.incident_id or "",
        node_name="merge_evidence",
        kind=kind,
        query=query_request,
        result=output,
        query_hash_source=query_request,
    )
    evidence_id = published.evidence_id
    no_data = _query_result_is_no_data(output)
    _append_evidence(state, evidence_id, source=kind, no_data=no_data, conflict_hint=False)
    _report_node_action(
        state,
        runtime,
        node_name="merge_evidence",
        tool_name="evidence.save_from_query",
        request_json={
            "incident_id": state.incident_id,
            "kind": kind,
            "query": query_request,
            "idempotency_key": published.idempotency_key,
            "created_by": published.created_by,
        },
        response_json={"status": "ok", "evidence_id": evidence_id, "no_data": no_data},
        started_ms=started_ms,
        status="ok",
        evidence_ids=[evidence_id],
    )
    if isinstance(state.evidence_plan.get("executed"), list):
        state.evidence_plan["executed"].append(str(meta.get("candidate_name") or f"query_{kind}:auto"))


def merge_evidence(
    state: GraphState,
    cfg: OrchestratorConfig,
    runtime: OrchestratorRuntime,
) -> GraphState:
    if not state.incident_id:
        raise RuntimeError("incident_id is required before merge_evidence")

    state.evidence_ids = []
    state.evidence_meta = []

    if not isinstance(state.evidence_plan.get("executed"), list):
        state.evidence_plan["executed"] = []
    if not isinstance(state.evidence_plan.get("skipped"), list):
        state.evidence_plan["skipped"] = []

    _save_branch_evidence(
        state=state,
        runtime=runtime,
        branch_name="metrics",
        kind="metrics",
        meta=state.metrics_branch_meta if isinstance(state.metrics_branch_meta, dict) else {},
        status=str(state.metrics_query_status or "skip"),
        output=state.metrics_query_output if isinstance(state.metrics_query_output, dict) else {},
        error=state.metrics_query_error,
    )
    _save_branch_evidence(
        state=state,
        runtime=runtime,
        branch_name="logs",
        kind="logs",
        meta=state.logs_branch_meta if isinstance(state.logs_branch_meta, dict) else {},
        status=str(state.logs_query_status or "skip"),
        output=state.logs_query_output if isinstance(state.logs_query_output, dict) else {},
        error=state.logs_query_error,
    )

    used_calls = 0
    used_bytes = 0
    used_latency = 0
    if state.metrics_branch_meta.get("mode") == "query":
        if state.metrics_query_status in {"ok", "error"}:
            used_calls += 1
        used_bytes += max(int(state.metrics_query_result_size_bytes), 0)
        used_latency += max(int(state.metrics_query_latency_ms), 0)
    if state.logs_branch_meta.get("mode") == "query":
        if state.logs_query_status in {"ok", "error"}:
            used_calls += 1
        used_bytes += max(int(state.logs_query_result_size_bytes), 0)
        used_latency += max(int(state.logs_query_latency_ms), 0)
    state.evidence_plan["used"] = {
        "calls": used_calls,
        "total_bytes": used_bytes,
        "total_latency_ms": used_latency,
    }

    if not state.evidence_ids:
        state.missing_evidence = ["logs", "traces"]
        fallback_started_ms = int(time.time() * 1000)
        fallback_saved = runtime.save_mock_evidence(
            incident_id=state.incident_id,
            node_name="merge_evidence",
            kind="metrics",
            summary="A3 fallback mock evidence: all query candidates failed.",
            raw={
                "source": "orchestrator",
                "mode": "a3_fallback",
                "kind": "no_data",
                "reason": "all query candidates failed",
            },
            query_hash_source={
                "mode": "a3_fallback",
                "kind": "metrics",
                "query_text": "mock://orchestrator",
            },
        )
        fallback_id = fallback_saved.evidence_id
        _append_evidence(state, fallback_id, source="metrics", no_data=True)
        _report_node_action(
            state,
            runtime,
            node_name="merge_evidence",
            tool_name="evidence.save_mock",
            request_json={
                "incident_id": state.incident_id,
                "kind": "metrics",
                "idempotency_key": fallback_saved.idempotency_key,
                "created_by": fallback_saved.created_by,
            },
            response_json={"evidence_id": fallback_id, "status": "ok"},
            started_ms=fallback_started_ms,
            status="ok",
            evidence_ids=[fallback_id],
        )
        if isinstance(state.evidence_plan.get("skipped"), list):
            state.evidence_plan["skipped"].append(
                {"name": "evidence.saveMockFallback", "reason": "all_queries_failed"}
            )
        return state

    missing: list[str] = state.missing_evidence[:]
    sources = {str(item.get("source") or "") for item in state.evidence_meta}
    if state.evidence_mode == "force_no_evidence":
        missing = ["logs", "traces"]
    elif state.evidence_mode == "query":
        if "logs" not in sources:
            missing.append("logs")
        if len(state.evidence_ids) < 2:
            missing.append("traces")
    state.missing_evidence = _ordered_unique_strings(missing)
    return state


def quality_gate_node(state: GraphState, runtime: OrchestratorRuntime) -> GraphState:
    quality_gate = _ensure_quality_gate(state)
    started_ms = int(time.time() * 1000)
    _report_node_action(
        state,
        runtime,
        node_name="quality_gate",
        tool_name="quality_gate.evaluate",
        request_json={
            "incident_id": state.incident_id,
            "evidence_ids": state.evidence_ids,
            "evidence_meta": state.evidence_meta,
        },
        response_json={"status": "ok", "quality_gate": quality_gate},
        started_ms=started_ms,
        status="ok",
        evidence_ids=state.evidence_ids,
    )
    return state


def summarize_diagnosis(state: GraphState, runtime: OrchestratorRuntime) -> GraphState:
    if not state.incident_id:
        raise RuntimeError("incident_id is required before summarize_diagnosis")
    if not state.evidence_ids:
        raise RuntimeError("evidence_ids is empty before summarize_diagnosis")

    primary_evidence = state.evidence_ids[0]
    missing_evidence = state.missing_evidence or ["logs", "traces"]
    quality_gate = _ensure_quality_gate(state)

    decision = str(quality_gate.get("decision") or "")
    synthesize_request = {
        "incident_id": state.incident_id,
        "instance_id": state.instance_id,
        "evidence_ids": state.evidence_ids,
        "missing_evidence": missing_evidence,
        "quality_gate": quality_gate,
    }
    if decision in {QUALITY_GATE_MISSING, QUALITY_GATE_CONFLICT}:
        synthesize_request["target_confidence_max"] = 0.3
    if decision == QUALITY_GATE_CONFLICT:
        synthesize_request["mode"] = "conflict_evidence"

    if decision == QUALITY_GATE_CONFLICT:
        synthesize_response = {
            "status": "ok",
            "result": "conflict_evidence_low_confidence",
            "root_cause": {
                "type": "conflict_evidence",
                "confidence": 0.25,
            },
            "missing_evidence": missing_evidence,
            "evidence_ids": state.evidence_ids,
            "quality_gate": quality_gate,
        }
    elif decision == QUALITY_GATE_MISSING:
        synthesize_response = {
            "status": "ok",
            "result": "missing_evidence_low_confidence",
            "root_cause": {
                "type": "missing_evidence",
                "confidence": 0.15,
            },
            "missing_evidence": missing_evidence,
            "evidence_ids": [primary_evidence],
            "quality_gate": quality_gate,
        }
    else:
        synthesize_response = {
            "status": "ok",
            "result": "diagnosis_json_ready",
            "root_cause": {
                "type": "unknown",
                "confidence": 0.65,
            },
            "evidence_ids": state.evidence_ids,
            "quality_gate": quality_gate,
        }
    if state.evidence_plan:
        synthesize_response["evidence_plan"] = state.evidence_plan

    started_ms = int(time.time() * 1000)
    _report_node_action(
        state,
        runtime,
        node_name="summarize_diagnosis",
        tool_name="diagnosis.generate",
        request_json=synthesize_request,
        response_json=synthesize_response,
        started_ms=started_ms,
        status="ok",
        evidence_ids=state.evidence_ids,
    )
    return state


def _diagnosis_timestamp() -> str:
    return datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def _build_success_diagnosis(state: GraphState) -> dict[str, Any]:
    evidence_ids = state.evidence_ids[:]
    primary_evidence = evidence_ids[0] if evidence_ids else ""
    return {
        "schema_version": "1.0",
        "generated_at": _diagnosis_timestamp(),
        "incident_id": state.incident_id,
        "summary": "Suspected root cause based on consistent available evidence.",
        "root_cause": {
            "type": "unknown",
            "category": "app",
            "summary": "Suspected service-side issue based on consistent evidence.",
            "statement": "Metrics and logs indicate correlated service degradation in the same window.",
            "confidence": 0.65,
            "evidence_ids": evidence_ids,
        },
        "timeline": [
            {
                "t": _diagnosis_timestamp(),
                "event": "evidence_collected",
                "ref": primary_evidence,
            }
        ],
        "observations": [
            {
                "title": "Evidence collected",
                "detail": "Metrics and logs are both available and consistent in the selected time window.",
            }
        ],
        "hypotheses": [
            {
                "statement": "Service-side regression likely contributed to elevated error rate.",
                "confidence": 0.55,
                "supporting_evidence_ids": evidence_ids,
                "missing_evidence": ["traces"],
            }
        ],
        "recommendations": [
            {
                "type": "readonly_check",
                "action": "Confirm hypothesis with traces or deployment diff for the same window.",
                "risk": "low",
            }
        ],
        "unknowns": ["Detailed upstream dependency impact requires traces."],
        "next_steps": ["Collect trace sample for top failing endpoint."],
    }


def _build_missing_evidence_diagnosis(state: GraphState) -> dict[str, Any]:
    primary_evidence = state.evidence_ids[0]
    missing_evidence = (state.missing_evidence or ["logs", "traces"])[:20]
    return {
        "schema_version": "1.0",
        "generated_at": _diagnosis_timestamp(),
        "incident_id": state.incident_id,
        "summary": "Insufficient evidence to determine root cause.",
        "root_cause": {
            "type": "missing_evidence",
            "category": "unknown",
            "summary": "Insufficient evidence to determine root cause.",
            "statement": "",
            "confidence": 0.15,
            "evidence_ids": [primary_evidence],
        },
        "missing_evidence": missing_evidence,
        "timeline": [
            {
                "t": _diagnosis_timestamp(),
                "event": "evidence_gap_detected",
                "ref": primary_evidence,
            }
        ],
        "observations": [
            {
                "title": "Evidence gap",
                "detail": "Logs/traces were not available or not found in the query window.",
            }
        ],
        "hypotheses": [
            {
                "statement": "Evidence gap prevents confident root-cause attribution.",
                "confidence": 0.15,
                "supporting_evidence_ids": [primary_evidence],
                "missing_evidence": missing_evidence,
            }
        ],
        "recommendations": [
            {
                "type": "readonly_check",
                "action": "Expand time window and collect logs/traces before concluding.",
                "risk": "low",
            }
        ],
        "unknowns": ["Root cause remains unknown because critical evidence is missing."],
        "next_steps": ["Re-run RCA after logs and traces become available."],
    }


def _build_conflict_evidence_diagnosis(state: GraphState) -> dict[str, Any]:
    evidence_ids = state.evidence_ids[:]
    if len(evidence_ids) > 2:
        evidence_ids = evidence_ids[:2]

    missing_evidence = (
        state.missing_evidence
        or [
            "align metrics/logs/traces time window and re-query within the same interval",
            "collect error logs (5xx/timeout/panic/OOM) during the metric spike",
            "collect upstream/downstream traces or confirm tracing sampling/drop",
        ]
    )[:20]
    return {
        "schema_version": "1.0",
        "generated_at": _diagnosis_timestamp(),
        "incident_id": state.incident_id,
        "summary": "Evidence signals conflict: metrics indicate degradation while logs/traces do not corroborate within the same window.",
        "root_cause": {
            "type": "conflict_evidence",
            "category": "unknown",
            "summary": "metrics vs logs/traces conflict within the same window",
            "statement": "",
            "confidence": 0.25,
            "evidence_ids": evidence_ids,
        },
        "missing_evidence": missing_evidence,
        "timeline": [
            {
                "t": _diagnosis_timestamp(),
                "event": "conflict_evidence_detected",
                "ref": evidence_ids[0] if evidence_ids else "",
            }
        ],
        "observations": [
            {
                "title": "Conflicting signals",
                "detail": "Metrics and logs/traces are inconsistent; avoid high-confidence conclusion.",
            }
        ],
        "hypotheses": [
            {
                "statement": "Current evidence is conflicting and insufficient for a decisive root cause.",
                "confidence": 0.25,
                "supporting_evidence_ids": evidence_ids,
                "missing_evidence": missing_evidence,
            }
        ],
        "recommendations": [
            {
                "type": "readonly_check",
                "action": "Re-run collection with aligned time windows and add trace/log datasource coverage.",
                "risk": "low",
            }
        ],
        "unknowns": ["Root cause remains uncertain due to evidence conflict."],
        "next_steps": ["Collect corroborating logs/traces in the same interval as metric anomalies."],
    }


def _guard(
    node_name: str,
    fn: Callable[[GraphState], Any],
    runtime: OrchestratorRuntime,
) -> Callable[[GraphState], Any]:
    def wrapped(state: GraphState) -> Any:
        if state.last_error:
            return state
        if runtime.is_lease_lost():
            reason = runtime.lease_lost_reason() or "lease_renew_failed"
            state.last_error = f"{node_name}: lease_lost: {reason}"
            return state
        try:
            return fn(state)
        except Exception as exc:  # noqa: BLE001
            state.last_error = f"{node_name}: {exc}"
            return state

    return wrapped


def _is_finalize_succeeded(state: GraphState) -> bool:
    if not state.finalized:
        return False
    return not bool(str(state.last_error or "").strip())


def finalize_job(
    state: GraphState,
    runtime: OrchestratorRuntime,
) -> GraphState:
    if runtime.is_lease_lost():
        if not state.last_error:
            state.last_error = f"lease_lost: {runtime.lease_lost_reason() or 'lease_renew_failed'}"
        return state

    error_message = (state.last_error or "").strip()

    if not error_message and not state.evidence_ids:
        error_message = "finalize_job: no evidence was collected"

    def _report_finalize(
        *,
        finalize_status: str,
        call_status: str,
        started_ms: int,
        error: str | None = None,
        request_error_message: str | None = None,
    ) -> None:
        _report_node_action(
            state,
            runtime,
            node_name="finalize_job",
            tool_name="ai_job.finalize",
            request_json={
                "job_id": state.job_id,
                "status": finalize_status,
                "evidence_ids": state.evidence_ids,
                "error_message": request_error_message,
            },
            response_json={
                "status": finalize_status,
                "finalized": True,
                "error": error,
            },
            started_ms=started_ms,
            status=call_status,
            error=error,
            evidence_ids=state.evidence_ids,
        )

    try:
        if error_message:
            state.last_error = error_message
            started_ms = int(time.time() * 1000)
            runtime.finalize(
                status="failed",
                diagnosis_json=None,
                error_message=error_message[:8192],
                evidence_ids=state.evidence_ids,
            )
            _report_finalize(
                finalize_status="failed",
                call_status="ok",
                started_ms=started_ms,
                request_error_message=error_message[:8192],
            )
            state.finalized = True
            return state

        quality_gate = _ensure_quality_gate(state)
        decision = str(quality_gate.get("decision") or "")
        if decision == QUALITY_GATE_CONFLICT:
            diagnosis_json = _build_conflict_evidence_diagnosis(state)
        elif decision == QUALITY_GATE_MISSING:
            diagnosis_json = _build_missing_evidence_diagnosis(state)
        else:
            diagnosis_json = _build_success_diagnosis(state)

        state.diagnosis_json = diagnosis_json
        started_ms = int(time.time() * 1000)
        runtime.finalize(
            status="succeeded",
            diagnosis_json=diagnosis_json,
            error_message=None,
            evidence_ids=state.evidence_ids,
        )
        _report_finalize(
            finalize_status="succeeded",
            call_status="ok",
            started_ms=started_ms,
            request_error_message=None,
        )
        state.finalized = True
        return state
    except Exception as exc:  # noqa: BLE001
        fallback = f"finalize_job: {exc}"
        error_text = str(exc)
        if "timed out" in error_text.lower():
            try:
                current_job = runtime.get_job(state.job_id)
                current_status = str(current_job.get("status") or "").strip().lower()
            except Exception:  # noqa: BLE001
                current_status = ""
            if current_status == "succeeded":
                state.last_error = None
                state.finalized = True
                return state

        state.last_error = fallback
        try:
            started_ms = int(time.time() * 1000)
            runtime.finalize(
                status="failed",
                diagnosis_json=None,
                error_message=fallback[:8192],
                evidence_ids=state.evidence_ids,
            )
            _report_finalize(
                finalize_status="failed",
                call_status="error",
                started_ms=started_ms,
                error=str(exc)[:512],
                request_error_message=fallback[:8192],
            )
        except Exception:  # noqa: BLE001
            pass
        state.finalized = True
        return state


def post_finalize_observe(
    state: GraphState,
    cfg: OrchestratorConfig,
    runtime: OrchestratorRuntime,
) -> GraphState:
    if not _is_finalize_succeeded(state):
        return state
    if not (cfg.post_finalize_observe or cfg.run_verification):
        return state

    incident_id = str(state.incident_id or "").strip()
    if not incident_id:
        return state

    started_ms = int(time.time() * 1000)
    try:
        snapshot = runtime.observe_post_finalize(
            incident_id=incident_id,
            wait_timeout_s=float(cfg.post_finalize_wait_timeout_seconds),
            wait_interval_s=float(cfg.post_finalize_wait_interval_ms) / 1000.0,
            wait_max_interval_s=float(cfg.post_finalize_wait_max_interval_ms) / 1000.0,
        )
    except Exception as exc:  # noqa: BLE001
        _report_node_action(
            state,
            runtime,
            node_name="post_finalize_observe",
            tool_name="post_finalize.observe",
            request_json={"incident_id": incident_id, "job_id": state.job_id},
            response_json={"status": "error"},
            started_ms=started_ms,
            status="error",
            error=str(exc)[:512],
            evidence_ids=state.evidence_ids,
        )
        state.post_finalize_snapshot = {
            "status": "error",
            "error": str(exc)[:512],
        }
        return state

    verification_plan = snapshot.verification_plan if isinstance(snapshot.verification_plan, dict) else {}
    kb_refs = snapshot.kb_refs if isinstance(snapshot.kb_refs, list) else []
    state.post_finalize_snapshot = {
        "incident_id": snapshot.incident_id,
        "job_id": snapshot.job_id,
        "target_toolcall_seq": snapshot.target_toolcall_seq,
        "kb_refs": kb_refs,
        "verification_plan": verification_plan,
    }
    state.post_finalize_verification_plan = verification_plan
    state.post_finalize_kb_refs = [item for item in kb_refs if isinstance(item, dict)]
    state.post_finalize_target_seq = snapshot.target_toolcall_seq

    _report_node_action(
        state,
        runtime,
        node_name="post_finalize_observe",
        tool_name="post_finalize.observe",
        request_json={"incident_id": incident_id, "job_id": state.job_id},
        response_json={
            "status": "ok",
            "kb_refs": len(state.post_finalize_kb_refs),
            "verification_steps": len(verification_plan.get("steps") or []),
            "target_toolcall_seq": state.post_finalize_target_seq,
        },
        started_ms=started_ms,
        status="ok",
        evidence_ids=state.evidence_ids,
    )
    return state


def run_verification(
    state: GraphState,
    cfg: OrchestratorConfig,
    runtime: OrchestratorRuntime,
) -> GraphState:
    if not _is_finalize_succeeded(state):
        return state
    if not cfg.run_verification:
        return state

    incident_id = str(state.incident_id or "").strip()
    if not incident_id:
        return state

    plan = state.post_finalize_verification_plan if isinstance(state.post_finalize_verification_plan, dict) else {}
    steps = plan.get("steps") if isinstance(plan, dict) else None
    if not isinstance(steps, list) or not steps:
        started_ms = int(time.time() * 1000)
        _report_node_action(
            state,
            runtime,
            node_name="run_verification",
            tool_name="verification.execute",
            request_json={
                "incident_id": incident_id,
                "source": cfg.verification_source,
                "steps": 0,
            },
            response_json={"status": "skipped", "reason": "no_verification_plan_steps"},
            started_ms=started_ms,
            status="ok",
            evidence_ids=state.evidence_ids,
        )
        state.verification_done = True
        state.verification_results = []
        return state

    started_ms = int(time.time() * 1000)
    try:
        results = runtime.run_verification(
            incident_id=incident_id,
            verification_plan=plan,
            source=cfg.verification_source,
        )
    except Exception as exc:  # noqa: BLE001
        _report_node_action(
            state,
            runtime,
            node_name="run_verification",
            tool_name="verification.execute",
            request_json={
                "incident_id": incident_id,
                "source": cfg.verification_source,
                "steps": len(steps),
            },
            response_json={"status": "error"},
            started_ms=started_ms,
            status="error",
            error=str(exc)[:512],
            evidence_ids=state.evidence_ids,
        )
        state.verification_done = True
        state.verification_results = [{"error": str(exc)[:512]}]
        return state

    results_payload: list[dict[str, Any]] = []
    for item in results:
        results_payload.append(
            {
                "step_index": int(getattr(item, "step_index", 0)),
                "tool": str(getattr(item, "tool", "")),
                "meets_expectation": bool(getattr(item, "meets_expectation", False)),
                "observed": str(getattr(item, "observed", "")),
            }
        )
    state.verification_results = results_payload
    state.verification_done = True

    _report_node_action(
        state,
        runtime,
        node_name="run_verification",
        tool_name="verification.execute",
        request_json={
            "incident_id": incident_id,
            "source": cfg.verification_source,
            "steps": len(steps),
        },
        response_json={
            "status": "ok",
            "steps": len(results_payload),
            "kb_refs": len(state.post_finalize_kb_refs),
        },
        started_ms=started_ms,
        status="ok",
        evidence_ids=state.evidence_ids,
    )
    return state


def build_graph(
    _client: Any,
    cfg: OrchestratorConfig,
    runtime: OrchestratorRuntime,
):
    guarded_query_metrics = _guard("query_metrics", lambda s: query_metrics_node(s, runtime), runtime)
    guarded_query_logs = _guard("query_logs", lambda s: query_logs_node(s, runtime), runtime)

    def _query_metrics_entry(state: GraphState) -> dict[str, Any]:
        out = guarded_query_metrics(state)
        if isinstance(out, GraphState):
            return {
                "metrics_query_status": "skipped",
                "metrics_query_output": {},
                "metrics_query_error": "guard_skipped",
                "metrics_query_latency_ms": 0,
                "metrics_query_result_size_bytes": 0,
            }
        return out

    def _query_logs_entry(state: GraphState) -> dict[str, Any]:
        out = guarded_query_logs(state)
        if isinstance(out, GraphState):
            return {
                "logs_query_status": "skipped",
                "logs_query_output": {},
                "logs_query_error": "guard_skipped",
                "logs_query_latency_ms": 0,
                "logs_query_result_size_bytes": 0,
            }
        return out

    builder = StateGraph(GraphState)
    builder.add_node(
        "load_job_and_start",
        _guard("load_job_and_start", lambda s: load_job_and_start(s, cfg, runtime), runtime),
    )
    builder.add_node(
        "plan_evidence",
        _guard("plan_evidence", lambda s: plan_evidence(s, cfg, runtime), runtime),
    )
    builder.add_node("query_metrics", _query_metrics_entry)
    builder.add_node("query_logs", _query_logs_entry)
    builder.add_node(
        "merge_evidence",
        _guard("merge_evidence", lambda s: merge_evidence(s, cfg, runtime), runtime),
    )
    builder.add_node(
        "quality_gate",
        _guard("quality_gate", lambda s: quality_gate_node(s, runtime), runtime),
    )
    builder.add_node(
        "summarize_diagnosis",
        _guard("summarize_diagnosis", lambda s: summarize_diagnosis(s, runtime), runtime),
    )
    builder.add_node("finalize_job", lambda s: finalize_job(s, runtime))
    builder.add_node("post_finalize_observe", lambda s: post_finalize_observe(s, cfg, runtime))
    builder.add_node("run_verification", lambda s: run_verification(s, cfg, runtime))

    builder.add_edge(START, "load_job_and_start")
    builder.add_edge("load_job_and_start", "plan_evidence")

    builder.add_edge("plan_evidence", "query_metrics")
    builder.add_edge("plan_evidence", "query_logs")
    builder.add_edge("query_metrics", "merge_evidence")
    builder.add_edge("query_logs", "merge_evidence")

    builder.add_edge("merge_evidence", "quality_gate")
    builder.add_edge("quality_gate", "summarize_diagnosis")
    builder.add_edge("summarize_diagnosis", "finalize_job")
    builder.add_edge("finalize_job", "post_finalize_observe")
    builder.add_edge("post_finalize_observe", "run_verification")
    builder.add_edge("run_verification", END)
    return builder.compile()
