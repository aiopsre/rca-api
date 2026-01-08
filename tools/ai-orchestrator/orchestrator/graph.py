from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime, timezone
import json
import threading
import time
from typing import Any, Callable

from langgraph.graph import END, START, StateGraph

from .evidence_plan import BudgetTracker, build_candidates, rank_candidates
from .state import GraphState
from .tools_rca_api import RCAApiClient


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


class LeaseGuard:
    def __init__(self) -> None:
        self._mu = threading.Lock()
        self._lost = False
        self._reason = ""

    def mark_lost(self, reason: str) -> None:
        with self._mu:
            if self._lost:
                return
            self._lost = True
            self._reason = str(reason).strip() or "lease_renew_failed"

    def is_lost(self) -> bool:
        with self._mu:
            return self._lost

    def reason(self) -> str:
        with self._mu:
            return self._reason


def _extract_incident_id(job_obj: dict[str, Any]) -> str:
    incident_id = str(
        job_obj.get("incidentID")
        or job_obj.get("incident_id")
        or ""
    ).strip()
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


def load_job_and_start(state: GraphState, client: RCAApiClient, cfg: OrchestratorConfig) -> GraphState:
    job = client.get_job(state.job_id)
    state.incident_id = _extract_incident_id(job)
    hints = _extract_input_hints(job)
    state.input_hints = hints
    state.force_no_evidence, state.force_conflict = _resolve_force_switches(hints, cfg)
    state.a3_max_calls, state.a3_max_total_bytes, state.a3_max_total_latency_ms = _resolve_a3_budget(hints, cfg)
    state.started = True
    return state


def collect_evidence(state: GraphState, client: RCAApiClient, cfg: OrchestratorConfig) -> GraphState:
    if not state.incident_id:
        raise RuntimeError("incident_id is required before collect_evidence")

    state.evidence_meta = []
    state.evidence_plan = {}
    if state.force_conflict:
        state.missing_evidence = [
            "align metrics/logs/traces time window and re-query within the same interval",
            "collect error logs (5xx/timeout/panic/OOM) during the metric spike",
            "collect upstream/downstream traces or confirm tracing sampling/drop (RUN_QUERY=0 uses placeholders)",
        ]
        metrics_id = client.save_mock_evidence(
            incident_id=state.incident_id,
            summary="FORCE_CONFLICT metrics placeholder: error rate spike observed.",
            raw={
                "source": "orchestrator",
                "mode": "forced_conflict",
                "dimension": "metrics",
                "kind": "mock_conflict_signal",
                "observed": "5xx and latency increased in the same time window",
                "reason": "FORCE_CONFLICT=1",
            },
        )
        _append_evidence(state, metrics_id, source="metrics", no_data=False, conflict_hint=True)

        logs_id = client.save_mock_evidence(
            incident_id=state.incident_id,
            summary="FORCE_CONFLICT logs/trace placeholder: no corroborating error evidence.",
            raw={
                "source": "orchestrator",
                "mode": "forced_conflict",
                "dimension": "logs_traces",
                "kind": "no_data",
                "observed": "logs/traces do not corroborate metric anomaly in this window",
                "reason": "FORCE_CONFLICT=1; datasource query skipped and placeholders persisted",
            },
        )
        _append_evidence(state, logs_id, source="logs", no_data=True, conflict_hint=True)
        return state

    if state.force_no_evidence:
        state.missing_evidence = ["logs", "traces"]
        evidence_id = client.save_mock_evidence(
            incident_id=state.incident_id,
            summary="no evidence found (forced)",
            raw={
                "source": "orchestrator",
                "mode": "forced_missing_evidence",
                "kind": "no_data",
                "missing": state.missing_evidence,
                "reason": "FORCE_NO_EVIDENCE=1",
            },
        )
        _append_evidence(state, evidence_id, source="metrics", no_data=True)
        return state

    if not cfg.run_query:
        state.missing_evidence = []
        metrics_id = client.save_mock_evidence(
            incident_id=state.incident_id,
            summary="P0 mock metrics evidence saved by orchestrator (RUN_QUERY=0).",
            raw={
                "source": "orchestrator",
                "mode": "mock",
                "kind": "metrics_signal",
                "observed": "5xx and latency increased in the incident window",
            },
        )
        _append_evidence(state, metrics_id, source="metrics", no_data=False)

        logs_id = client.save_mock_evidence(
            incident_id=state.incident_id,
            summary="P0 mock logs evidence saved by orchestrator (RUN_QUERY=0).",
            raw={
                "source": "orchestrator",
                "mode": "mock",
                "kind": "logs_signal",
                "observed": "error logs align with metric spike in the same window",
            },
        )
        _append_evidence(state, logs_id, source="logs", no_data=False)
        return state

    if not cfg.ds_base_url.strip():
        raise RuntimeError("RUN_QUERY=1 requires DS_BASE_URL")
    if not cfg.auto_create_datasource:
        raise RuntimeError("AUTO_CREATE_DATASOURCE=0 is not supported in P0 without preloaded datasource ID")

    incident_context: dict[str, str] = {"service": "", "namespace": "", "severity": ""}
    try:
        incident_context = _extract_incident_context(client.get_incident(state.incident_id))
    except Exception:  # noqa: BLE001 - context enrichment is best effort.
        pass

    datasource_id = client.ensure_datasource(cfg.ds_base_url)
    state.datasource_id = datasource_id
    state.missing_evidence = []

    planning_context: dict[str, Any] = {
        "incident_id": state.incident_id,
        "service": incident_context.get("service", ""),
        "namespace": incident_context.get("namespace", ""),
        "severity": incident_context.get("severity", ""),
    }
    candidates = rank_candidates(build_candidates(planning_context), planning_context)
    budget_tracker = BudgetTracker(
        max_calls=state.a3_max_calls,
        max_total_bytes=state.a3_max_total_bytes,
        max_total_latency_ms=state.a3_max_total_latency_ms,
    )
    state.evidence_plan = {
        "version": "a3",
        "budget": budget_tracker.budget_snapshot(),
        "used": budget_tracker.used_snapshot(),
        "candidates": [item.to_plan_dict() for item in candidates],
        "executed": [],
        "skipped": [],
    }

    now_s = int(time.time())

    for idx, candidate in enumerate(candidates):
        if not budget_tracker.can_execute_query():
            for pending in candidates[idx:]:
                state.evidence_plan["skipped"].append({"name": pending.name, "reason": "budget_exhausted"})
            break

        window_seconds = max(_coerce_non_negative_int(candidate.params.get("window_seconds"), 600), 60)
        start_ts = now_s - window_seconds
        end_ts = now_s

        if candidate.query_type == "metrics":
            query_expr = str(candidate.params.get("expr") or "sum(up)")
            step_seconds = max(_coerce_non_negative_int(candidate.params.get("step_seconds"), 30), 1)
            started_ms = int(time.time() * 1000)
            try:
                query_result = client.query_metrics(
                    datasource_id=datasource_id,
                    promql=query_expr,
                    start_ts=start_ts,
                    end_ts=end_ts,
                    step_s=step_seconds,
                )
            except Exception as exc:  # noqa: BLE001 - continue candidate iteration on query failures.
                latency_ms = max(1, int(time.time() * 1000) - started_ms)
                budget_tracker.record_query(result_bytes=0, latency_ms=latency_ms)
                state.evidence_plan["used"] = budget_tracker.used_snapshot()
                state.evidence_plan["skipped"].append(
                    {"name": candidate.name, "reason": "query_failed", "detail": str(exc)[:200]}
                )
                continue

            latency_ms = max(1, int(time.time() * 1000) - started_ms)
            budget_tracker.record_query(
                result_bytes=_query_result_size_bytes(query_result),
                latency_ms=latency_ms,
            )
            state.evidence_plan["used"] = budget_tracker.used_snapshot()
            query_request = {
                "datasourceID": datasource_id,
                "queryText": query_expr,
                "queryJSON": "{}",
            }
            evidence_id = client.save_evidence_from_query(
                incident_id=state.incident_id,
                kind="metrics",
                query=query_request,
                result=query_result,
            )
            _append_evidence(state, evidence_id, source="metrics", no_data=_query_result_is_no_data(query_result))
            state.evidence_plan["executed"].append(candidate.name)
            continue

        if candidate.query_type == "logs":
            query_text = str(candidate.params.get("query") or '{job=~".+"} |= "error"')
            limit = max(_coerce_non_negative_int(candidate.params.get("limit"), 200), 1)
            started_ms = int(time.time() * 1000)
            try:
                query_result = client.query_logs(
                    datasource_id=datasource_id,
                    query=query_text,
                    start_ts=start_ts,
                    end_ts=end_ts,
                    limit=limit,
                )
            except Exception as exc:  # noqa: BLE001 - continue candidate iteration on query failures.
                latency_ms = max(1, int(time.time() * 1000) - started_ms)
                budget_tracker.record_query(result_bytes=0, latency_ms=latency_ms)
                state.evidence_plan["used"] = budget_tracker.used_snapshot()
                state.evidence_plan["skipped"].append(
                    {"name": candidate.name, "reason": "query_failed", "detail": str(exc)[:200]}
                )
                continue

            latency_ms = max(1, int(time.time() * 1000) - started_ms)
            budget_tracker.record_query(
                result_bytes=_query_result_size_bytes(query_result),
                latency_ms=latency_ms,
            )
            state.evidence_plan["used"] = budget_tracker.used_snapshot()
            query_request = {
                "datasourceID": datasource_id,
                "queryText": query_text,
                "queryJSON": "{}",
            }
            evidence_id = client.save_evidence_from_query(
                incident_id=state.incident_id,
                kind="logs",
                query=query_request,
                result=query_result,
            )
            _append_evidence(state, evidence_id, source="logs", no_data=_query_result_is_no_data(query_result))
            state.evidence_plan["executed"].append(candidate.name)
            continue

        state.evidence_plan["skipped"].append({"name": candidate.name, "reason": "unsupported_candidate"})

    if not state.evidence_ids:
        state.missing_evidence = ["logs", "traces"]
        fallback_id = client.save_mock_evidence(
            incident_id=state.incident_id,
            summary="A3 fallback mock evidence: all query candidates failed.",
            raw={
                "source": "orchestrator",
                "mode": "a3_fallback",
                "kind": "no_data",
                "reason": "all query candidates failed",
            },
        )
        _append_evidence(state, fallback_id, source="metrics", no_data=True)
        state.evidence_plan["skipped"].append({"name": "evidence.saveMockFallback", "reason": "all_queries_failed"})
    return state


def write_tool_calls(state: GraphState, client: RCAApiClient) -> GraphState:
    if not state.incident_id:
        raise RuntimeError("incident_id is required before write_tool_calls")
    if not state.evidence_ids:
        raise RuntimeError("evidence_ids is empty before write_tool_calls")

    primary_evidence = state.evidence_ids[0]
    missing_evidence = state.missing_evidence or ["logs", "traces"]
    quality_gate = _ensure_quality_gate(state)

    if state.force_conflict:
        collect_tool_name = "evidence.collectConflictPlaceholder"
    elif state.force_no_evidence:
        collect_tool_name = "evidence.collectPlaceholder"
    else:
        collect_tool_name = "mcp.query_metrics" if state.datasource_id else "evidence.saveMock"

    decision = str(quality_gate.get("decision") or "")
    if decision == QUALITY_GATE_CONFLICT:
        collect_response = {
            "evidence_ids": state.evidence_ids,
            "status": "conflict_signals",
            "missing_evidence": missing_evidence,
            "quality_gate": quality_gate,
        }
    elif decision == QUALITY_GATE_MISSING:
        collect_response = {
            "evidence_ids": [primary_evidence],
            "status": "no_data",
            "missing_evidence": missing_evidence,
            "quality_gate": quality_gate,
        }
    else:
        collect_response = {
            "evidence_ids": state.evidence_ids,
            "status": "ok",
            "quality_gate": quality_gate,
        }
    if state.evidence_plan:
        collect_response["evidence_plan"] = state.evidence_plan

    started_ms = int(time.time() * 1000)
    client.add_tool_call(
        job_id=state.job_id,
        seq=1,
        node_name="collect_evidence",
        tool_name=collect_tool_name,
        request_json={
            "incident_id": state.incident_id,
            "datasource_id": state.datasource_id,
            "instance_id": state.instance_id,
            "force_no_evidence": state.force_no_evidence,
            "force_conflict": state.force_conflict,
        },
        response_json=collect_response,
        latency_ms=max(1, int(time.time() * 1000) - started_ms),
        status="ok",
    )

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
    client.add_tool_call(
        job_id=state.job_id,
        seq=2,
        node_name="synthesize",
        tool_name="diagnosis.generate",
        request_json=synthesize_request,
        response_json=synthesize_response,
        latency_ms=max(1, int(time.time() * 1000) - started_ms),
        status="ok",
    )

    state.tool_calls_written = 2
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
    fn: Callable[[GraphState], GraphState],
    lease_guard: LeaseGuard | None = None,
) -> Callable[[GraphState], GraphState]:
    def wrapped(state: GraphState) -> GraphState:
        if state.last_error:
            return state
        if lease_guard is not None and lease_guard.is_lost():
            reason = lease_guard.reason() or "lease_renew_failed"
            state.last_error = f"{node_name}: lease_lost: {reason}"
            return state
        try:
            return fn(state)
        except Exception as exc:  # noqa: BLE001
            state.last_error = f"{node_name}: {exc}"
            return state

    return wrapped


def finalize_job(state: GraphState, client: RCAApiClient, lease_guard: LeaseGuard | None = None) -> GraphState:
    if lease_guard is not None and lease_guard.is_lost():
        if not state.last_error:
            state.last_error = f"lease_lost: {lease_guard.reason() or 'lease_renew_failed'}"
        return state

    error_message = (state.last_error or "").strip()

    if not error_message and not state.evidence_ids:
        error_message = "finalize_job: no evidence was collected"

    try:
        if error_message:
            state.last_error = error_message
            client.finalize_job(state.job_id, status="failed", diagnosis_json=None, error_message=error_message[:8192])
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
        client.finalize_job(state.job_id, status="succeeded", diagnosis_json=diagnosis_json, error_message=None)
        state.finalized = True
        return state
    except Exception as exc:  # noqa: BLE001
        fallback = f"finalize_job: {exc}"
        # Handle uncertain client-side timeout: finalize may have succeeded server-side.
        error_text = str(exc)
        if "timed out" in error_text.lower():
            try:
                current_job = client.get_job(state.job_id)
                current_status = str(current_job.get("status") or "").strip().lower()
            except Exception:  # noqa: BLE001
                current_status = ""
            if current_status == "succeeded":
                state.last_error = None
                state.finalized = True
                return state

        state.last_error = fallback
        try:
            client.finalize_job(state.job_id, status="failed", diagnosis_json=None, error_message=fallback[:8192])
        except Exception:  # noqa: BLE001
            pass
        state.finalized = True
        return state


def build_graph(client: RCAApiClient, cfg: OrchestratorConfig, lease_guard: LeaseGuard | None = None):
    builder = StateGraph(GraphState)
    builder.add_node(
        "load_job_and_start",
        _guard("load_job_and_start", lambda s: load_job_and_start(s, client, cfg), lease_guard),
    )
    builder.add_node(
        "collect_evidence",
        _guard("collect_evidence", lambda s: collect_evidence(s, client, cfg), lease_guard),
    )
    builder.add_node(
        "write_tool_calls",
        _guard("write_tool_calls", lambda s: write_tool_calls(s, client), lease_guard),
    )
    builder.add_node("finalize_job", lambda s: finalize_job(s, client, lease_guard))

    builder.add_edge(START, "load_job_and_start")
    builder.add_edge("load_job_and_start", "collect_evidence")
    builder.add_edge("collect_evidence", "write_tool_calls")
    builder.add_edge("write_tool_calls", "finalize_job")
    builder.add_edge("finalize_job", END)
    return builder.compile()
