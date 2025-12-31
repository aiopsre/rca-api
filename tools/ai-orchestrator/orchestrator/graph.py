from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime, timezone
import time
from typing import Callable

from langgraph.graph import END, START, StateGraph

from .state import GraphState
from .tools_rca_api import RCAApiClient


@dataclass
class OrchestratorConfig:
    run_query: bool = False
    force_no_evidence: bool = False
    force_conflict: bool = False
    ds_base_url: str = ""
    auto_create_datasource: bool = True


def _extract_incident_id(job_obj: dict) -> str:
    incident_id = str(
        job_obj.get("incidentID")
        or job_obj.get("incident_id")
        or ""
    ).strip()
    if not incident_id:
        raise RuntimeError(f"incident_id missing in job payload: {job_obj}")
    return incident_id


def load_job_and_start(state: GraphState, client: RCAApiClient) -> GraphState:
    job = client.get_job(state.job_id)
    state.incident_id = _extract_incident_id(job)
    client.start_job(state.job_id)
    state.started = True
    return state


def collect_evidence(state: GraphState, client: RCAApiClient, cfg: OrchestratorConfig) -> GraphState:
    if not state.incident_id:
        raise RuntimeError("incident_id is required before collect_evidence")

    state.force_no_evidence = cfg.force_no_evidence
    state.force_conflict = cfg.force_conflict
    collected_evidence_ids: list[str] = []
    if cfg.force_conflict:
        state.missing_evidence = [
            "align metrics/logs/traces time window and re-query within the same interval",
            "collect error logs (5xx/timeout/panic/OOM) during the metric spike",
            "collect upstream/downstream traces or confirm tracing sampling/drop (RUN_QUERY=0 uses placeholders)",
        ]
        collected_evidence_ids.append(
            client.save_mock_evidence(
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
        )
        collected_evidence_ids.append(
            client.save_mock_evidence(
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
        )
    elif cfg.force_no_evidence:
        state.missing_evidence = ["logs", "traces"]
        collected_evidence_ids.append(
            client.save_mock_evidence(
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
        )
    elif not cfg.run_query:
        state.missing_evidence = []
        collected_evidence_ids.append(
            client.save_mock_evidence(
                incident_id=state.incident_id,
                summary="P0 mock evidence saved by orchestrator (RUN_QUERY=0).",
                raw={
                    "source": "orchestrator",
                    "mode": "mock",
                    "note": "RUN_QUERY=0 synthetic evidence",
                },
            )
        )
    else:
        if not cfg.ds_base_url.strip():
            raise RuntimeError("RUN_QUERY=1 requires DS_BASE_URL")
        if not cfg.auto_create_datasource:
            raise RuntimeError("AUTO_CREATE_DATASOURCE=0 is not supported in P0 without preloaded datasource ID")

        datasource_id = client.ensure_datasource(cfg.ds_base_url)
        state.datasource_id = datasource_id
        state.missing_evidence = []

        now_s = int(time.time())
        query_request = {
            "datasourceID": datasource_id,
            "queryText": "up",
            "queryJSON": "{}",
        }
        query_result = client.query_metrics(
            datasource_id=datasource_id,
            promql="up",
            start_ts=now_s - 600,
            end_ts=now_s,
            step_s=30,
        )
        collected_evidence_ids.append(
            client.save_evidence_from_query(
                incident_id=state.incident_id,
                kind="metrics",
                query=query_request,
                result=query_result,
            )
        )

    for evidence_id in collected_evidence_ids:
        if evidence_id not in state.evidence_ids:
            state.evidence_ids.append(evidence_id)
    return state


def write_tool_calls(state: GraphState, client: RCAApiClient) -> GraphState:
    if not state.incident_id:
        raise RuntimeError("incident_id is required before write_tool_calls")
    if not state.evidence_ids:
        raise RuntimeError("evidence_ids is empty before write_tool_calls")

    primary_evidence = state.evidence_ids[0]
    missing_evidence = state.missing_evidence or ["logs", "traces"]
    if state.force_conflict:
        collect_tool_name = "evidence.collectConflictPlaceholder"
    elif state.force_no_evidence:
        collect_tool_name = "evidence.collectPlaceholder"
    else:
        collect_tool_name = "evidence.queryMetrics" if state.datasource_id else "evidence.saveMock"
    if state.force_conflict:
        collect_response = {
            "evidence_ids": state.evidence_ids,
            "status": "conflict_signals",
            "reason": "FORCE_CONFLICT=1",
            "conflict_dimension": ["metrics_vs_logs", "logs_vs_traces"],
            "missing_evidence": missing_evidence,
        }
    elif state.force_no_evidence:
        collect_response = {
            "evidence_ids": [primary_evidence],
            "status": "no_data",
            "reason": "FORCE_NO_EVIDENCE=1",
            "missing_evidence": missing_evidence,
        }
    else:
        collect_response = {
            "evidence_ids": [primary_evidence],
            "status": "ok",
        }

    started_ms = int(time.time() * 1000)
    client.add_tool_call(
        job_id=state.job_id,
        seq=1,
        node_name="collect_evidence",
        tool_name=collect_tool_name,
        request_json={
            "incident_id": state.incident_id,
            "datasource_id": state.datasource_id,
            "force_no_evidence": state.force_no_evidence,
            "force_conflict": state.force_conflict,
        },
        response_json=collect_response,
        latency_ms=max(1, int(time.time() * 1000) - started_ms),
        status="ok",
    )

    synthesize_request = {
        "incident_id": state.incident_id,
        "evidence_ids": state.evidence_ids,
        "missing_evidence": missing_evidence,
    }
    if state.force_no_evidence:
        synthesize_request["target_confidence_max"] = 0.3
    if state.force_conflict:
        synthesize_request["target_confidence_max"] = 0.3
        synthesize_request["mode"] = "conflict_evidence"
    if state.force_conflict:
        synthesize_response = {
            "status": "ok",
            "result": "conflict_evidence_low_confidence",
            "root_cause": {
                "type": "conflict_evidence",
                "confidence": 0.25,
            },
            "missing_evidence": missing_evidence,
            "evidence_ids": state.evidence_ids,
        }
    elif state.force_no_evidence:
        synthesize_response = {
            "status": "ok",
            "result": "missing_evidence_low_confidence",
            "root_cause": {
                "type": "missing_evidence",
                "confidence": 0.15,
            },
            "missing_evidence": missing_evidence,
        }
    else:
        synthesize_response = {
            "status": "ok",
            "result": "diagnosis_json_ready",
        }

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


def _build_success_diagnosis(state: GraphState) -> dict:
    primary_evidence = state.evidence_ids[0]
    return {
        "schema_version": "1.0",
        "generated_at": _diagnosis_timestamp(),
        "incident_id": state.incident_id,
        "summary": "P0 orchestrator diagnosis based on collected evidence.",
        "root_cause": {
            "category": "app",
            "statement": "Suspected service-side issue correlated with recent 5xx increase.",
            "confidence": 0.4,
            "evidence_ids": [primary_evidence],
        },
        "timeline": [
            {
                "t": _diagnosis_timestamp(),
                "event": "evidence_collected",
                "ref": primary_evidence,
            }
        ],
        "hypotheses": [
            {
                "statement": "Application regression likely contributed to elevated 5xx.",
                "confidence": 0.4,
                "supporting_evidence_ids": [primary_evidence],
                "missing_evidence": ["logs", "traces"],
            }
        ],
        "recommendations": [
            {
                "type": "readonly_check",
                "action": "Verify recent deployment diff and inspect top error logs.",
                "risk": "low",
            }
        ],
        "unknowns": ["Upstream dependency saturation status"],
        "next_steps": ["Collect trace sample for top failing endpoint"],
    }


def _build_missing_evidence_diagnosis(state: GraphState) -> dict:
    primary_evidence = state.evidence_ids[0]
    missing_evidence = state.missing_evidence or ["logs", "traces"]
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


def _build_conflict_evidence_diagnosis(state: GraphState) -> dict:
    evidence_ids = state.evidence_ids[:]
    if len(evidence_ids) > 2:
        evidence_ids = evidence_ids[:2]

    missing_evidence = state.missing_evidence or [
        "align metrics/logs/traces time window and re-query within the same interval",
    ]
    return {
        "schema_version": "1.0",
        "generated_at": _diagnosis_timestamp(),
        "incident_id": state.incident_id,
        "summary": "Evidence signals conflict: metrics indicate degradation while logs/traces do not corroborate within the same window. RUN_QUERY=0 placeholders were saved.",
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
        "unknowns": ["Root cause remains uncertain due to evidence conflict and placeholder-only collection mode."],
        "next_steps": ["Collect corroborating logs/traces in the same interval as metric anomalies."],
    }


def _guard(node_name: str, fn: Callable[[GraphState], GraphState]) -> Callable[[GraphState], GraphState]:
    def wrapped(state: GraphState) -> GraphState:
        if state.last_error:
            return state
        try:
            return fn(state)
        except Exception as exc:  # noqa: BLE001
            state.last_error = f"{node_name}: {exc}"
            return state

    return wrapped


def finalize_job(state: GraphState, client: RCAApiClient) -> GraphState:
    error_message = (state.last_error or "").strip()

    if not error_message and not state.evidence_ids:
        error_message = "finalize_job: no evidence was collected"

    try:
        if error_message:
            state.last_error = error_message
            client.finalize_job(state.job_id, status="failed", diagnosis_json=None, error_message=error_message[:8192])
            state.finalized = True
            return state

        if state.force_conflict:
            diagnosis_json = _build_conflict_evidence_diagnosis(state)
        elif state.force_no_evidence:
            diagnosis_json = _build_missing_evidence_diagnosis(state)
        else:
            diagnosis_json = _build_success_diagnosis(state)
        state.diagnosis_json = diagnosis_json
        client.finalize_job(state.job_id, status="succeeded", diagnosis_json=diagnosis_json, error_message=None)
        state.finalized = True
        return state
    except Exception as exc:  # noqa: BLE001
        fallback = f"finalize_job: {exc}"
        state.last_error = fallback
        try:
            client.finalize_job(state.job_id, status="failed", diagnosis_json=None, error_message=fallback[:8192])
        except Exception:  # noqa: BLE001
            pass
        state.finalized = True
        return state


def build_graph(client: RCAApiClient, cfg: OrchestratorConfig):
    builder = StateGraph(GraphState)
    builder.add_node("load_job_and_start", _guard("load_job_and_start", lambda s: load_job_and_start(s, client)))
    builder.add_node("collect_evidence", _guard("collect_evidence", lambda s: collect_evidence(s, client, cfg)))
    builder.add_node("write_tool_calls", _guard("write_tool_calls", lambda s: write_tool_calls(s, client)))
    builder.add_node("finalize_job", lambda s: finalize_job(s, client))

    builder.add_edge(START, "load_job_and_start")
    builder.add_edge("load_job_and_start", "collect_evidence")
    builder.add_edge("collect_evidence", "write_tool_calls")
    builder.add_edge("write_tool_calls", "finalize_job")
    builder.add_edge("finalize_job", END)
    return builder.compile()
