from __future__ import annotations

import time
from typing import Any, Callable

from ..evidence_plan import build_candidates, rank_candidates
from ..runtime.runtime import OrchestratorRuntime
from ..state import GraphState
from .config import OrchestratorConfig
from .diagnosis import (
    build_conflict_evidence_diagnosis,
    build_missing_evidence_diagnosis,
    build_success_diagnosis,
)
from .guard import guard, is_finalize_succeeded
from .helpers import (
    append_evidence,
    extract_incident_context,
    extract_incident_id,
    extract_input_hints,
    ordered_unique_strings,
    prepare_query_branch_meta,
    query_result_is_no_data,
    query_result_size_bytes,
    query_toolcall_response,
    resolve_a3_budget,
    resolve_force_switches,
    select_candidate,
)
from .quality_gate import (
    QUALITY_GATE_CONFLICT,
    QUALITY_GATE_MISSING,
    ensure_quality_gate,
)
from .reporting import report_node_action


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
    state.incident_id = extract_incident_id(job)
    hints = extract_input_hints(job)
    state.input_hints = hints
    state.force_no_evidence, state.force_conflict = resolve_force_switches(hints, cfg)
    state.a3_max_calls, state.a3_max_total_bytes, state.a3_max_total_latency_ms = resolve_a3_budget(hints, cfg)
    state.started = True

    report_node_action(
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
            incident_context = extract_incident_context(runtime.get_incident(state.incident_id))
        except Exception:  # noqa: BLE001
            pass
        state.incident_context = incident_context

        metrics_ds_type = str(getattr(cfg, "metrics_ds_type", cfg.ds_type) or cfg.ds_type).strip().lower() or cfg.ds_type
        logs_ds_type = str(getattr(cfg, "logs_ds_type", cfg.ds_type) or cfg.ds_type).strip().lower() or cfg.ds_type
        metrics_datasource_id = runtime.ensure_datasource(cfg.ds_base_url, metrics_ds_type)
        logs_datasource_id = runtime.ensure_datasource(cfg.ds_base_url, logs_ds_type)
        state.datasource_id = metrics_datasource_id

        planning_context: dict[str, Any] = {
            "incident_id": state.incident_id,
            "service": incident_context.get("service", ""),
            "namespace": incident_context.get("namespace", ""),
            "severity": incident_context.get("severity", ""),
        }
        candidates = rank_candidates(build_candidates(planning_context), planning_context)
        state.evidence_candidates = [item.to_plan_dict() for item in candidates]
        state.evidence_plan["candidates"] = state.evidence_candidates

        metrics_candidate = select_candidate(candidates, "metrics")
        logs_candidate = select_candidate(candidates, "logs")
        state.metrics_branch_meta = prepare_query_branch_meta(
            datasource_id=metrics_datasource_id,
            candidate=metrics_candidate,
            query_type="metrics",
        )
        state.logs_branch_meta = prepare_query_branch_meta(
            datasource_id=logs_datasource_id,
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

    prompt_skill = getattr(runtime, "consume_prompt_skill", None)
    prompt_skill_result: dict[str, Any] | None = None
    if callable(prompt_skill):
        consumed = prompt_skill(capability="evidence.plan", graph_state=state)
        if isinstance(consumed, dict):
            prompt_skill_result = consumed

    started_ms = int(time.time() * 1000)
    response_json = {
        "status": "ok",
        "mode": state.evidence_mode,
        "datasource_id": state.datasource_id,
        "metrics_branch_mode": state.metrics_branch_meta.get("mode"),
        "logs_branch_mode": state.logs_branch_meta.get("mode"),
        "candidates": state.evidence_candidates,
    }
    if state.evidence_plan:
        response_json["evidence_plan"] = state.evidence_plan
    if isinstance(prompt_skill_result, dict):
        response_json["skill"] = {
            "status": "applied",
            "skill_id": str(prompt_skill_result.get("skill_id") or "").strip(),
            "binding_key": str(prompt_skill_result.get("selected_binding_key") or "").strip(),
        }
    report_node_action(
        state,
        runtime,
        node_name="plan_evidence",
        tool_name="evidence.plan",
        request_json={
            "incident_id": state.incident_id,
            "mode": state.evidence_mode,
            "run_query": cfg.run_query,
        },
        response_json=response_json,
        started_ms=started_ms,
        status="ok",
    )
    return state


def query_metrics_node(state: GraphState, runtime: OrchestratorRuntime) -> dict[str, Any]:
    meta = state.metrics_branch_meta if isinstance(state.metrics_branch_meta, dict) else {}
    mode = str(meta.get("mode") or "skip")
    started_ms = int(time.time() * 1000)

    if mode == "skip":
        report_node_action(
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
        report_node_action(
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
            "metrics_query_result_size_bytes": query_result_size_bytes(raw if isinstance(raw, dict) else {}),
        }

    request_payload = meta.get("request_payload") if isinstance(meta.get("request_payload"), dict) else {}
    if (
        mode == "query"
        and bool(meta.get("tool_result_reusable"))
        and str(meta.get("tool_result_source") or "") == "skill_prompt_first"
        and str(state.metrics_query_status or "") == "ok"
        and isinstance(state.metrics_query_output, dict)
        and state.metrics_query_output
    ):
        try:
            runtime.report_observation(
                tool="skill.tool_reuse",
                node_name="query_metrics",
                params={
                    "request_payload": request_payload,
                    "source": "skill_prompt_first",
                },
                response={
                    "status": "ok",
                    "source": "skill_prompt_first",
                    "promql": str(request_payload.get("promql") or ""),
                },
                evidence_ids=[],
            )
        except Exception:  # noqa: BLE001
            pass
        report_node_action(
            state,
            runtime,
            node_name="query_metrics",
            tool_name="evidence.metrics.reuse",
            request_json=request_payload,
            response_json={
                **query_toolcall_response(state.metrics_query_output),
                "source": "skill_prompt_first",
            },
            started_ms=started_ms,
            status="ok",
            count_in_state=False,
        )
        return {
            "metrics_query_status": "ok",
            "metrics_query_output": state.metrics_query_output if isinstance(state.metrics_query_output, dict) else {},
            "metrics_query_error": None,
            "metrics_query_latency_ms": int(state.metrics_query_latency_ms or 0),
            "metrics_query_result_size_bytes": int(state.metrics_query_result_size_bytes or 0),
        }

    try:
        result = runtime.query_metrics(
            datasource_id=str(request_payload.get("datasource_id") or ""),
            promql=str(request_payload.get("promql") or "sum(up)"),
            start_ts=int(request_payload.get("start_ts") or int(time.time()) - 600),
            end_ts=int(request_payload.get("end_ts") or int(time.time())),
            step_s=max(int(request_payload.get("step_seconds") or 30), 1),
        )
    except Exception as exc:  # noqa: BLE001
        report_node_action(
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

    report_node_action(
        state,
        runtime,
        node_name="query_metrics",
        tool_name="mcp.query_metrics",
        request_json=request_payload,
        response_json=query_toolcall_response(result),
        started_ms=started_ms,
        status="ok",
        count_in_state=False,
    )
    return {
        "metrics_query_status": "ok",
        "metrics_query_output": result,
        "metrics_query_error": None,
        "metrics_query_latency_ms": max(1, int(time.time() * 1000) - started_ms),
        "metrics_query_result_size_bytes": query_result_size_bytes(result),
    }


def query_logs_node(state: GraphState, runtime: OrchestratorRuntime) -> dict[str, Any]:
    meta = state.logs_branch_meta if isinstance(state.logs_branch_meta, dict) else {}
    mode = str(meta.get("mode") or "skip")
    started_ms = int(time.time() * 1000)

    if mode == "skip":
        report_node_action(
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
        report_node_action(
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
            "logs_query_result_size_bytes": query_result_size_bytes(raw if isinstance(raw, dict) else {}),
        }

    request_payload = meta.get("request_payload") if isinstance(meta.get("request_payload"), dict) else {}
    if (
        mode == "query"
        and bool(meta.get("tool_result_reusable"))
        and str(meta.get("tool_result_source") or "") == "skill_prompt_first"
        and str(state.logs_query_status or "") == "ok"
        and isinstance(state.logs_query_output, dict)
        and state.logs_query_output
    ):
        try:
            runtime.report_observation(
                tool="skill.tool_reuse",
                node_name="query_logs",
                params={
                    "request_payload": request_payload,
                    "source": "skill_prompt_first",
                },
                response={
                    "status": "ok",
                    "source": "skill_prompt_first",
                    "query": str(request_payload.get("query") or ""),
                },
                evidence_ids=[],
            )
        except Exception:  # noqa: BLE001
            pass
        report_node_action(
            state,
            runtime,
            node_name="query_logs",
            tool_name="evidence.logs.reuse",
            request_json=request_payload,
            response_json={
                **query_toolcall_response(state.logs_query_output),
                "source": "skill_prompt_first",
            },
            started_ms=started_ms,
            status="ok",
            count_in_state=False,
        )
        return {
            "logs_query_status": "ok",
            "logs_query_output": state.logs_query_output if isinstance(state.logs_query_output, dict) else {},
            "logs_query_error": None,
            "logs_query_latency_ms": int(state.logs_query_latency_ms or 0),
            "logs_query_result_size_bytes": int(state.logs_query_result_size_bytes or 0),
        }
    try:
        result = runtime.query_logs(
            datasource_id=str(request_payload.get("datasource_id") or ""),
            query=str(request_payload.get("query") or '{job=~".+"} |= "error"'),
            start_ts=int(request_payload.get("start_ts") or int(time.time()) - 600),
            end_ts=int(request_payload.get("end_ts") or int(time.time())),
            limit=max(int(request_payload.get("limit") or 200), 1),
        )
    except Exception as exc:  # noqa: BLE001
        report_node_action(
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

    report_node_action(
        state,
        runtime,
        node_name="query_logs",
        tool_name="mcp.query_logs",
        request_json=request_payload,
        response_json=query_toolcall_response(result),
        started_ms=started_ms,
        status="ok",
        count_in_state=False,
    )
    return {
        "logs_query_status": "ok",
        "logs_query_output": result,
        "logs_query_error": None,
        "logs_query_latency_ms": max(1, int(time.time() * 1000) - started_ms),
        "logs_query_result_size_bytes": query_result_size_bytes(result),
    }


def save_branch_evidence(
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
        append_evidence(state, evidence_id, source=kind, no_data=no_data, conflict_hint=conflict_hint)
        report_node_action(
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
    no_data = query_result_is_no_data(output)
    append_evidence(state, evidence_id, source=kind, no_data=no_data, conflict_hint=False)
    report_node_action(
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

    save_branch_evidence(
        state=state,
        runtime=runtime,
        branch_name="metrics",
        kind="metrics",
        meta=state.metrics_branch_meta if isinstance(state.metrics_branch_meta, dict) else {},
        status=str(state.metrics_query_status or "skip"),
        output=state.metrics_query_output if isinstance(state.metrics_query_output, dict) else {},
        error=state.metrics_query_error,
    )
    save_branch_evidence(
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
        append_evidence(state, fallback_id, source="metrics", no_data=True)
        report_node_action(
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
    state.missing_evidence = ordered_unique_strings(missing)
    return state


def quality_gate_node(state: GraphState, runtime: OrchestratorRuntime) -> GraphState:
    quality_gate = ensure_quality_gate(state)
    started_ms = int(time.time() * 1000)
    report_node_action(
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


def _build_native_diagnosis(state: GraphState) -> dict[str, Any]:
    quality_gate = ensure_quality_gate(state)
    decision = str(quality_gate.get("decision") or "")
    if decision == QUALITY_GATE_CONFLICT:
        return build_conflict_evidence_diagnosis(state)
    if decision == QUALITY_GATE_MISSING:
        return build_missing_evidence_diagnosis(state)
    return build_success_diagnosis(state)


def _merge_diagnosis_patch(diagnosis_json: dict[str, Any], diagnosis_patch: dict[str, Any]) -> dict[str, Any]:
    if not diagnosis_patch:
        return diagnosis_json
    merged = dict(diagnosis_json)
    summary = diagnosis_patch.get("summary")
    if isinstance(summary, str) and summary.strip():
        merged["summary"] = summary.strip()

    root_cause_patch = diagnosis_patch.get("root_cause")
    if isinstance(root_cause_patch, dict):
        current_root_cause = merged.get("root_cause")
        if not isinstance(current_root_cause, dict):
            current_root_cause = {}
        current_root_cause = dict(current_root_cause)
        root_summary = root_cause_patch.get("summary")
        if isinstance(root_summary, str) and root_summary.strip():
            current_root_cause["summary"] = root_summary.strip()
        if "statement" in root_cause_patch:
            root_statement = root_cause_patch.get("statement")
            if isinstance(root_statement, str) and root_statement.strip():
                current_root_cause["statement"] = root_statement.strip()
            elif isinstance(root_statement, str):
                current_root_cause.pop("statement", None)
        merged["root_cause"] = current_root_cause

    recommendations = diagnosis_patch.get("recommendations")
    if isinstance(recommendations, list):
        merged["recommendations"] = [item for item in recommendations if isinstance(item, dict)]
    unknowns = diagnosis_patch.get("unknowns")
    if isinstance(unknowns, list):
        merged["unknowns"] = [str(item).strip() for item in unknowns if str(item).strip()]
    next_steps = diagnosis_patch.get("next_steps")
    if isinstance(next_steps, list):
        merged["next_steps"] = [str(item).strip() for item in next_steps if str(item).strip()]
    return merged

def summarize_diagnosis(state: GraphState, runtime: OrchestratorRuntime) -> GraphState:
    if not state.incident_id:
        raise RuntimeError("incident_id is required before summarize_diagnosis")
    if not state.evidence_ids:
        raise RuntimeError("evidence_ids is empty before summarize_diagnosis")

    primary_evidence = state.evidence_ids[0]
    missing_evidence = state.missing_evidence or ["logs", "traces"]
    quality_gate = ensure_quality_gate(state)

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

    diagnosis_json = _build_native_diagnosis(state)
    state.diagnosis_json = diagnosis_json
    consume_skill = getattr(runtime, "consume_prompt_skill", None)
    if callable(consume_skill):
        enriched = consume_skill(capability="diagnosis.enrich", graph_state=state)
        if isinstance(enriched, dict):
            diagnosis_json = state.diagnosis_json if isinstance(state.diagnosis_json, dict) else diagnosis_json
            synthesize_response["skill"] = {
                "status": "applied",
                "skill_id": str(enriched.get("skill_id") or "").strip(),
                "binding_key": str(enriched.get("selected_binding_key") or "").strip(),
            }

    state.diagnosis_json = diagnosis_json
    synthesize_response["diagnosis_json"] = diagnosis_json

    started_ms = int(time.time() * 1000)
    report_node_action(
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


def finalize_job(
    state: GraphState,
    runtime: OrchestratorRuntime,
) -> GraphState:
    if runtime.is_lease_lost():
        if not state.last_error:
            state.last_error = f"lease_lost: {runtime.lease_lost_reason() or 'lease_renew_failed'}"
        return state

    # 写回 Skills 输出的 session_patch（best effort）
    from ..skills import write_session_patch_to_platform

    write_session_patch_to_platform(state, runtime)

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
        report_node_action(
            state,
            runtime,
            node_name="finalize_job",
            tool_name="ai_job.finalize",
            request_json={
                "job_id": state.job_id,
                "status": finalize_status,
                "evidence_ids": state.evidence_ids,
                "error_message": request_error_message,
                "degrade_reasons": state.degrade_reasons,
            },
            response_json={
                "status": finalize_status,
                "finalized": True,
                "error": error,
                "degrade_reasons": state.degrade_reasons,
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

        diagnosis_json = state.diagnosis_json if isinstance(state.diagnosis_json, dict) else _build_native_diagnosis(state)
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
    if not is_finalize_succeeded(state):
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
        report_node_action(
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

    report_node_action(
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
    if not is_finalize_succeeded(state):
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
        report_node_action(
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
        report_node_action(
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

    report_node_action(
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


def make_query_metrics_entry(runtime: OrchestratorRuntime) -> Callable[[GraphState], dict[str, Any]]:
    guarded_query_metrics = guard("query_metrics", lambda s: query_metrics_node(s, runtime), runtime)

    def query_metrics_entry(state: GraphState) -> dict[str, Any]:
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

    return query_metrics_entry


def make_query_logs_entry(runtime: OrchestratorRuntime) -> Callable[[GraphState], dict[str, Any]]:
    guarded_query_logs = guard("query_logs", lambda s: query_logs_node(s, runtime), runtime)

    def query_logs_entry(state: GraphState) -> dict[str, Any]:
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

    return query_logs_entry
