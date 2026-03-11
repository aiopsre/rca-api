from __future__ import annotations

import os

from langgraph.graph import END, START, StateGraph

from ...runtime.runtime import OrchestratorRuntime
from ...state import GraphState
from ..config import OrchestratorConfig
from ..guard import guard
from ..nodes import (
    finalize_job,
    load_job_and_start,
    merge_evidence,
    plan_evidence,
    post_finalize_observe,
    quality_gate_node,
    run_verification,
    summarize_diagnosis,
)
from ..nodes_dynamic import (
    execute_tool_calls,
    plan_tool_calls,
    run_tool_agent,
)


def _is_fc_graph_agent_enabled() -> bool:
    """Check if FC graph agent is enabled.

    FC4D: Default is true, but respects env var override for rollback.
    Set RCA_FC_GRAPH_AGENT_ENABLED=false or RCA_FC_COMPAT_DYNAMIC_TOOL_NODES_ENABLED=true
    to use the legacy dual-node path.

    Returns:
        True if FC graph agent should be used.
    """
    import os

    # P2: Check compat flag first - if set, force legacy dual-node path
    compat_env = os.environ.get("RCA_FC_COMPAT_DYNAMIC_TOOL_NODES_ENABLED", "").strip().lower()
    if compat_env in ("true", "1", "yes", "on"):
        return False

    env_val = os.environ.get("RCA_FC_GRAPH_AGENT_ENABLED", "").strip().lower()
    if env_val in ("false", "0", "no", "off"):
        return False
    if env_val in ("true", "1", "yes", "on"):
        return True
    # Default: FC path enabled
    return True


def build_basic_rca_graph(
    runtime: OrchestratorRuntime,
    cfg: OrchestratorConfig,
):
    """Build the basic RCA graph with dynamic tool execution.

    The graph discovers available tools at runtime and executes them
    dynamically using the function-calling agent.

    FC4D: The function-calling agent (run_tool_agent) is the default path.
    Set RCA_FC_GRAPH_AGENT_ENABLED=false to use the legacy dual-node path
    (plan_tool_calls + execute_tool_calls) for rollback scenarios.

    Args:
        runtime: Orchestrator runtime instance.
        cfg: Orchestrator configuration.

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

    # FC4D: Check feature flag for tool execution path
    # Default is FC agent, but allow rollback to dual-node path via env var
    fc_enabled = _is_fc_graph_agent_enabled()

    if fc_enabled:
        # Primary path: single-node function-calling agent
        builder.add_node(
            "run_tool_agent",
            guard("run_tool_agent", lambda s: run_tool_agent(s, cfg, runtime), runtime),
        )
        builder.add_edge("plan_evidence", "run_tool_agent")
        builder.add_edge("run_tool_agent", "merge_evidence")
    else:
        # Fallback path: dual-node plan + execute (for rollback scenarios)
        builder.add_node(
            "plan_tool_calls",
            guard("plan_tool_calls", lambda s: plan_tool_calls(s, cfg, runtime), runtime),
        )
        builder.add_node(
            "execute_tool_calls",
            guard("execute_tool_calls", lambda s: execute_tool_calls(s, cfg, runtime), runtime),
        )
        builder.add_edge("plan_evidence", "plan_tool_calls")
        builder.add_edge("plan_tool_calls", "execute_tool_calls")
        builder.add_edge("execute_tool_calls", "merge_evidence")

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
    builder.add_edge("merge_evidence", "quality_gate")
    builder.add_edge("quality_gate", "summarize_diagnosis")
    builder.add_edge("summarize_diagnosis", "finalize_job")
    builder.add_edge("finalize_job", "post_finalize_observe")
    builder.add_edge("post_finalize_observe", "run_verification")
    builder.add_edge("run_verification", END)
    return builder.compile()

