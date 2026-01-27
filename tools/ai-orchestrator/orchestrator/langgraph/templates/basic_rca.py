from __future__ import annotations

from langgraph.graph import END, START, StateGraph

from ...runtime.runtime import OrchestratorRuntime
from ...state import GraphState
from ..config import OrchestratorConfig
from ..guard import guard
from ..nodes import (
    finalize_job,
    load_job_and_start,
    make_query_logs_entry,
    make_query_metrics_entry,
    merge_evidence,
    plan_evidence,
    post_finalize_observe,
    quality_gate_node,
    run_verification,
    summarize_diagnosis,
)


def build_basic_rca_graph(
    runtime: OrchestratorRuntime,
    cfg: OrchestratorConfig,
):
    builder = StateGraph(GraphState)
    builder.add_node(
        "load_job_and_start",
        guard("load_job_and_start", lambda s: load_job_and_start(s, cfg, runtime), runtime),
    )
    builder.add_node(
        "plan_evidence",
        guard("plan_evidence", lambda s: plan_evidence(s, cfg, runtime), runtime),
    )
    builder.add_node("query_metrics", make_query_metrics_entry(runtime))
    builder.add_node("query_logs", make_query_logs_entry(runtime))
    builder.add_node(
        "merge_evidence",
        guard("merge_evidence", lambda s: merge_evidence(s, cfg, runtime), runtime),
    )
    builder.add_node(
        "quality_gate",
        guard("quality_gate", lambda s: quality_gate_node(s, runtime), runtime),
    )
    builder.add_node(
        "summarize_diagnosis",
        guard("summarize_diagnosis", lambda s: summarize_diagnosis(s, runtime), runtime),
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

