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
from ..nodes_dynamic import (
    execute_tool_calls,
    make_execute_tool_calls_entry,
    make_plan_tool_calls_entry,
    plan_tool_calls,
)


def build_basic_rca_graph(
    runtime: OrchestratorRuntime,
    cfg: OrchestratorConfig,
    *,
    dynamic_tool_execution: bool = False,
):
    """Build the basic RCA graph.

    Args:
        runtime: Orchestrator runtime instance.
        cfg: Orchestrator configuration.
        dynamic_tool_execution: If True, use dynamic tool execution nodes
            instead of fixed query_metrics and query_logs nodes.

    Returns:
        Compiled LangGraph graph.
    """
    builder = StateGraph(GraphState)
    builder.add_node(
        "load_job_and_start",
        guard("load_job_and_start", lambda s: load_job_and_start(s, cfg, runtime), runtime),
    )
    builder.add_node(
        "plan_evidence",
        guard("plan_evidence", lambda s: plan_evidence(s, cfg, runtime), runtime),
    )

    if dynamic_tool_execution:
        # Dynamic tool execution path
        builder.add_node(
            "plan_tool_calls",
            guard("plan_tool_calls", lambda s: plan_tool_calls(s, cfg, runtime), runtime),
        )
        builder.add_node(
            "execute_tool_calls",
            guard("execute_tool_calls", lambda s: execute_tool_calls(s, cfg, runtime), runtime),
        )
    else:
        # Fixed tool execution path (backward compatible)
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

    if dynamic_tool_execution:
        # Dynamic path: plan_evidence -> plan_tool_calls -> execute_tool_calls -> merge_evidence
        builder.add_edge("plan_evidence", "plan_tool_calls")
        builder.add_edge("plan_tool_calls", "execute_tool_calls")
        builder.add_edge("execute_tool_calls", "merge_evidence")
    else:
        # Fixed path: plan_evidence -> query_metrics/query_logs (parallel) -> merge_evidence
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

