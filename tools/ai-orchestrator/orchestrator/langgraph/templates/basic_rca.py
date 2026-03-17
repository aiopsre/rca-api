from __future__ import annotations

from langgraph.graph import END, START, StateGraph

from ...runtime.runtime import OrchestratorRuntime
from ...state import GraphState
from ..config import OrchestratorConfig
from ..guard import guard
from ..nodes import (
    finalize_job,
    load_job_and_start,
    merge_evidence,
    post_finalize_observe,
    quality_gate_node,
    run_verification,
    summarize_diagnosis,
    summarize_diagnosis_agentized,
)
from ..nodes_agents import (
    merge_domain_findings,
    run_change_agent,
    run_knowledge_agent,
    run_observability_agent,
)
from ..nodes_platform import run_platform_special_agent
from ..nodes_router import route_domains


def build_basic_rca_graph(
    runtime: OrchestratorRuntime,
    cfg: OrchestratorConfig,
):
    """Build the basic RCA graph with hybrid multi-agent.

    Execution flow:
        START -> load_job_and_start -> route_domains
            -> run_observability_agent -> run_change_agent -> run_knowledge_agent
            -> merge_domain_findings -> merge_evidence
            -> run_platform_special_agent -> summarize_diagnosis_agentized
            -> quality_gate -> summarize_diagnosis -> finalize_job -> END

    Fine-grained rollback switches:
        RCA_DOMAIN_AGENT_CHANGE_ENABLED=false - Skip change domain agent
        RCA_DOMAIN_AGENT_KNOWLEDGE_ENABLED=false - Skip knowledge domain agent
        RCA_PLATFORM_SPECIAL_AGENT_ENABLED=false - Use deterministic diagnosis

    Args:
        runtime: Orchestrator runtime instance.
        cfg: Orchestrator configuration.

    Returns:
        Compiled LangGraph graph.
    """
    builder = StateGraph(GraphState)

    # Common nodes
    builder.add_node(
        "load_job_and_start",
        guard("load_job_and_start", lambda s: load_job_and_start(s, cfg, runtime), runtime),
    )
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

    # Hybrid multi-agent nodes
    builder.add_node(
        "route_domains",
        guard("route_domains", lambda s: route_domains(s, cfg, runtime), runtime),
    )
    builder.add_node(
        "run_observability_agent",
        guard("run_observability_agent", lambda s: run_observability_agent(s, cfg, runtime), runtime),
    )
    builder.add_node(
        "run_change_agent",
        guard("run_change_agent", lambda s: run_change_agent(s, cfg, runtime), runtime),
    )
    builder.add_node(
        "run_knowledge_agent",
        guard("run_knowledge_agent", lambda s: run_knowledge_agent(s, cfg, runtime), runtime),
    )
    builder.add_node(
        "merge_domain_findings",
        guard("merge_domain_findings", lambda s: merge_domain_findings(s, cfg, runtime), runtime),
    )
    builder.add_node(
        "run_platform_special_agent",
        guard("run_platform_special_agent", lambda s: run_platform_special_agent(s, cfg, runtime), runtime),
    )
    builder.add_node(
        "summarize_diagnosis_agentized",
        guard("summarize_diagnosis_agentized", lambda s: summarize_diagnosis_agentized(s, cfg, runtime), runtime),
    )

    # Edges
    builder.add_edge(START, "load_job_and_start")
    builder.add_edge("load_job_and_start", "route_domains")
    builder.add_edge("route_domains", "run_observability_agent")
    builder.add_edge("run_observability_agent", "run_change_agent")
    builder.add_edge("run_change_agent", "run_knowledge_agent")
    builder.add_edge("run_knowledge_agent", "merge_domain_findings")
    builder.add_edge("merge_domain_findings", "merge_evidence")
    builder.add_edge("merge_evidence", "run_platform_special_agent")
    builder.add_edge("run_platform_special_agent", "summarize_diagnosis_agentized")
    builder.add_edge("summarize_diagnosis_agentized", "quality_gate")
    builder.add_edge("quality_gate", "summarize_diagnosis")
    builder.add_edge("summarize_diagnosis", "finalize_job")
    builder.add_edge("finalize_job", "post_finalize_observe")
    builder.add_edge("post_finalize_observe", "run_verification")
    builder.add_edge("run_verification", END)

    return builder.compile()