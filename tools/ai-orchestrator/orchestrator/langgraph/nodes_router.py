"""Route Agent nodes for hybrid multi-agent system.

This module implements the Route Agent that analyzes incidents and
assigns investigation tasks to domain-specific agents.

Phase HM3: Route Agent + Observability Agent MVP.
Phase HM4: Extended to support Change and Knowledge domains.
"""
from __future__ import annotations

import json
import os
import time
from dataclasses import dataclass, field
from typing import TYPE_CHECKING, Any

from ..constants import TRACE_EVENT_ROUTER_ROUTE
from ..middleware.base import AgentRequest, AgentResponse
from .helpers import append_context_fields
from .llm_logging import log_llm_dialogue
from .reporting import report_node_action

if TYPE_CHECKING:
    from ..middleware.chain import MiddlewareChain
    from ..runtime.resolved_context import ResolvedAgentContext
    from ..runtime.runtime import OrchestratorRuntime
    from ..state import GraphState
    from .config import OrchestratorConfig


# HM4: Support all three domains - observability, change, knowledge.
HM3_SUPPORTED_DOMAINS: frozenset[str] = frozenset({"observability", "change", "knowledge"})

# HM11: Domain -> Capability mapping for skill routing.
# Maps each domain to the capability that skills should declare to serve that domain.
DOMAIN_CAPABILITY_MAP: dict[str, str] = {
    "observability": "evidence.plan",  # Primary: metrics/logs/traces investigation
    # Future capabilities (not yet implemented):
    # "change": "change.analyze",
    # "knowledge": "knowledge.lookup",
}


@dataclass
class DomainTask:
    """A task assigned to a domain agent.

    Attributes:
        task_id: Unique identifier for this task.
        domain: Domain name (observability, change, knowledge).
        goal: What this task should accomplish.
        priority: Task priority (higher = more important).
        mode: Execution mode (reserved for future use).
        tool_scope: List of tool names this task can use.
        skill_scope: List of skill capabilities this task can use.
    """

    task_id: str
    domain: str  # observability, change, knowledge
    goal: str
    priority: int = 100
    mode: str = "hybrid"  # reserved for future use
    tool_scope: list[str] = field(default_factory=list)
    skill_scope: list[str] = field(default_factory=list)

    def to_dict(self) -> dict[str, Any]:
        """Convert to dictionary for JSON serialization."""
        return {
            "task_id": self.task_id,
            "domain": self.domain,
            "goal": self.goal,
            "priority": self.priority,
            "mode": self.mode,
            "tool_scope": self.tool_scope,
            "skill_scope": self.skill_scope,
        }

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "DomainTask":
        """Create from dictionary.

        Args:
            data: Dictionary containing task data.

        Returns:
            DomainTask instance.
        """
        return cls(
            task_id=str(data.get("task_id") or ""),
            domain=str(data.get("domain") or "observability"),
            goal=str(data.get("goal") or ""),
            priority=int(data.get("priority") or 100),
            mode=str(data.get("mode") or "hybrid"),
            tool_scope=list(data.get("tool_scope") or []),
            skill_scope=list(data.get("skill_scope") or []),
        )


def _parse_domain_tasks(content: str) -> list[dict[str, Any]]:
    """Parse domain tasks from LLM response.

    Args:
        content: Raw LLM response content.

    Returns:
        List of domain task dictionaries.
    """
    content = str(content or "").strip()
    if not content:
        return []

    # Try direct parse
    try:
        data = json.loads(content)
        if isinstance(data, dict) and "domain_tasks" in data:
            tasks = data["domain_tasks"]
            if isinstance(tasks, list):
                return [t for t in tasks if isinstance(t, dict)]
        if isinstance(data, list):
            return [t for t in data if isinstance(t, dict)]
    except json.JSONDecodeError:
        pass

    # Try to find JSON block
    import re
    match = re.search(r"```(?:json)?\s*([\s\S]*?)\s*```", content)
    if match:
        try:
            data = json.loads(match.group(1))
            if isinstance(data, dict) and "domain_tasks" in data:
                tasks = data["domain_tasks"]
                if isinstance(tasks, list):
                    return [t for t in tasks if isinstance(t, dict)]
            if isinstance(data, list):
                return [t for t in data if isinstance(t, dict)]
        except json.JSONDecodeError:
            pass

    return []


def _default_observability_task(state: "GraphState") -> dict[str, Any]:
    """Create default observability task when router is disabled.

    Args:
        state: Current graph state.

    Returns:
        Default domain task dictionary.
    """
    incident_context = state.incident_context or {}
    service = str(incident_context.get("service") or "").strip()
    namespace = str(incident_context.get("namespace") or "").strip()

    goal = "Investigate observability data for root cause"
    if service:
        goal = f"Investigate observability data for service '{service}'"
        if namespace:
            goal += f" in namespace '{namespace}'"

    return {
        "task_id": "obs-default",
        "domain": "observability",
        "goal": goal,
        "priority": 100,
        "mode": "hybrid",
        "tool_scope": [],
        "skill_scope": [],
    }


def _validate_domain_task(task: dict[str, Any], state: "GraphState | None" = None) -> dict[str, Any]:
    """Validate and normalize a domain task.

    Args:
        task: Raw task dictionary.
        state: Optional graph state for tracking degradation reasons.

    Returns:
        Validated and normalized task dictionary.
    """
    domain = str(task.get("domain") or "observability").strip().lower()

    # HM4: Support observability, change, and knowledge domains
    if domain not in HM3_SUPPORTED_DOMAINS:
        original_domain = domain
        domain = "observability"  # Fallback to observability
        if state is not None:
            state.add_degrade_reason(
                f"domain_not_supported:{original_domain}_fallback_to_observability"
            )

    task_id = str(task.get("task_id") or "").strip()
    if not task_id:
        task_id = f"{domain}-{int(time.time())}"

    return {
        "task_id": task_id,
        "domain": domain,
        "goal": str(task.get("goal") or "Investigate incident"),
        "priority": max(1, min(1000, int(task.get("priority") or 100))),
        "mode": str(task.get("mode") or "hybrid"),
        "tool_scope": list(task.get("tool_scope") or []),
        "skill_scope": list(task.get("skill_scope") or []),
    }


def _enrich_task_with_skill_scope(
    task: dict[str, Any],
    skill_surface: dict[str, Any] | None,
) -> dict[str, Any]:
    """Enrich task with skill_scope based on available capabilities.

    HM11: Implements structured routing for skill integration.
    Checks if the domain has a corresponding capability with available skills.

    Args:
        task: Domain task dictionary.
        skill_surface: Skill surface from agent context, containing capability_map.

    Returns:
        Updated task with mode and skill_scope set appropriately.
    """
    domain = task.get("domain", "")
    capability = DOMAIN_CAPABILITY_MAP.get(domain)

    # No capability mapping for this domain - use native tools only
    if not capability:
        task["mode"] = "hybrid"  # Keep hybrid for tool flexibility
        task["skill_scope"] = []
        return task

    # No skill surface available - use native tools only
    if not skill_surface:
        task["mode"] = "hybrid"
        task["skill_scope"] = []
        return task

    # Check if capability has available skills
    capability_map = skill_surface.get("capability_map", {})
    available_skills = capability_map.get(capability, [])

    if available_skills:
        # Skills available for this capability
        task["skill_scope"] = [capability]
        task["mode"] = "hybrid"  # Try skill first, fallback to native
    else:
        # No skills for this capability
        task["skill_scope"] = []
        task["mode"] = "hybrid"

    return task


def _build_router_system_prompt() -> str:
    """Build the system prompt for the router agent.

    Returns:
        System prompt string.
    """
    # HM4: Support all three domains
    return """You are an RCA Router Agent. Your job is to analyze incidents and assign investigation tasks to domain-specific agents.

Available domains:
1. observability: For metrics, logs, and traces investigation (ALWAYS include)
2. change: For change events, deployments, and configuration analysis
3. knowledge: For knowledge base lookup, historical incidents, and runbooks

Output format (JSON):
{
  "domain_tasks": [
    {
      "task_id": "unique-task-id",
      "domain": "observability",
      "goal": "What this task should investigate",
      "priority": 100,
      "mode": "hybrid",
      "tool_scope": [],
      "skill_scope": []
    }
  ]
}

Rules:
- ALWAYS include at least one observability task
- Include change domain if the incident might be related to recent deployments/changes
- Include knowledge domain for historical context or known solutions
- Use priority 100 for primary tasks, 50 for secondary
- Use mode "hybrid" for tasks that can use both tools and skills
- Leave tool_scope and skill_scope empty to allow all available resources
- Optionally specify tool_scope with a list of tool names to restrict which tools the agent can use
- Output ONLY the JSON, no other text"""


def _build_router_user_prompt(state: "GraphState") -> str:
    """Build the user prompt for the router agent.

    Uses only incident_context, no raw payload.
    The router should make domain routing decisions based on stable
    summary fields, not raw alert payload content.

    Args:
        state: Current graph state.

    Returns:
        User prompt string.
    """
    from .prompt_context import build_router_prompt_context

    ctx = build_router_prompt_context(state)
    incident_context = ctx["incident_context"]
    incident_id = state.incident_id or "unknown"

    context_parts = [f"Incident ID: {incident_id}"]

    service = incident_context.get("service")
    if service:
        context_parts.append(f"Service: {service}")

    namespace = incident_context.get("namespace")
    if namespace:
        context_parts.append(f"Namespace: {namespace}")

    severity = incident_context.get("severity")
    if severity:
        context_parts.append(f"Severity: {severity}")

    append_context_fields(
        context_parts,
        incident_context,
        [
            ("Alert", "alert_name"),
            ("Fingerprint", "fingerprint"),
            ("Cluster", "cluster"),
            ("Workload", "workload_name"),
            ("Workload Kind", "workload_kind"),
            ("Incident Status", "status"),
            ("RCA Status", "rca_status"),
            ("Root Cause", "root_cause_summary"),
        ],
    )

    return f"""Analyze this incident and assign investigation tasks:

{chr(10).join(context_parts)}

Output the domain_tasks JSON."""


def _get_agent_context(state: "GraphState") -> "ResolvedAgentContext | None":
    """Get the ResolvedAgentContext from state.

    Args:
        state: Current graph state.

    Returns:
        ResolvedAgentContext instance or None.
    """
    from ..runtime.resolved_context import ResolvedAgentContext

    if not state.agent_context:
        return None

    try:
        return ResolvedAgentContext.from_json(json.dumps(state.agent_context))
    except (json.JSONDecodeError, TypeError):
        return None


def _get_llm(runtime: "OrchestratorRuntime") -> Any:
    """Get LLM instance from runtime.

    Uses the independent graph LLM (HM7-1).

    Args:
        runtime: Orchestrator runtime instance.

    Returns:
        LLM instance or None if not configured.
    """
    get_graph_llm = getattr(runtime, "get_graph_llm", None)
    if callable(get_graph_llm):
        llm = get_graph_llm()
        if llm is not None:
            return llm
    return None


def _invoke_llm(llm: Any, request: AgentRequest) -> Any:
    """Invoke LLM with the prepared request.

    Args:
        llm: LLM instance.
        request: Prepared agent request.

    Returns:
        LLM response.
    """
    try:
        from langchain_core.messages import HumanMessage, SystemMessage
    except ImportError:
        return None

    messages = [
        SystemMessage(content=request.system_prompt),
        HumanMessage(content=request.user_prompt),
    ]

    log_llm_dialogue(
        event="request",
        node_name="route_domains",
        messages=messages,
        extra={"domain": "router"},
    )
    try:
        response = llm.invoke(messages)
    except Exception as exc:  # noqa: BLE001
        log_llm_dialogue(
            event="error",
            node_name="route_domains",
            messages=messages,
            error=exc,
            extra={"domain": "router"},
        )
        raise
    log_llm_dialogue(
        event="response",
        node_name="route_domains",
        messages=messages,
        response=response,
        extra={"domain": "router"},
    )
    return response


def route_domains(
    state: "GraphState",
    cfg: "OrchestratorConfig",
    runtime: "OrchestratorRuntime",
) -> "GraphState":
    """Route Agent: Analyze incident and assign investigation tasks to domains.

    This node uses LLM to analyze the incident context and decompose
    the investigation into domain-specific tasks.

    Args:
        state: Current graph state.
        cfg: Orchestrator configuration.
        runtime: Orchestrator runtime instance.

    Returns:
        Updated graph state with domain_tasks populated.
    """
    started_ms = int(time.time() * 1000)

    # Build context for LLM
    agent_context = _get_agent_context(state)
    middleware_chain: "MiddlewareChain | None" = getattr(cfg, "middleware_chain", None)
    middleware_enabled: bool = getattr(cfg, "middleware_enabled", False)

    # Build request
    system_prompt = _build_router_system_prompt()
    user_prompt = _build_router_user_prompt(state)

    request = AgentRequest(
        system_prompt=system_prompt,
        user_prompt=user_prompt,
        metadata={"node": "route_domains", "trace_event": TRACE_EVENT_ROUTER_ROUTE},
    )

    # Prepare through middleware
    if middleware_enabled and middleware_chain is not None and agent_context is not None:
        prepared = middleware_chain.prepare(
            state=state,
            context=agent_context,
            request=request,
            config={"mode": "skills_only", "domain": "router", "include_incident": False},
        )
    else:
        prepared = request

    # Invoke LLM
    llm = _get_llm(runtime)
    if llm is None:
        state.add_degrade_reason("llm_not_configured")
        state.domain_tasks = [_default_observability_task(state)]
        state.route_context = {
            "routed_at": int(time.time() * 1000),
            "domain_count": 1,
            "domains": ["observability"],
            "mode": "fallback_no_llm",
        }
        report_node_action(
            state,
            runtime,
            node_name="route_domains",
            tool_name="agent.route",
            request_json={"incident_id": state.incident_id},
            response_json={
                "status": "fallback",
                "reason": "llm_not_configured",
                "domain_tasks": state.domain_tasks,
            },
            started_ms=started_ms,
            status="ok",
            count_in_state=False,
        )
        return state

    parsed_tasks: list[dict[str, Any]] = []
    try:
        response = _invoke_llm(llm, prepared)
        if response is not None:
            content = getattr(response, "content", "") or ""
            parsed_tasks = _parse_domain_tasks(content)
    except Exception as exc:  # noqa: BLE001
        state.add_degrade_reason(f"router_llm_error:{str(exc)[:64]}")

    # Process response through middleware
    if middleware_enabled and middleware_chain is not None and agent_context is not None and parsed_tasks:
        agent_response = AgentResponse(
            content="",
            parsed={"domain_tasks": parsed_tasks},
        )
        processed = middleware_chain.after_llm(
            state, agent_context, agent_response, {"domain": "router"}
        )
        parsed_tasks = list(processed.parsed.get("domain_tasks") or parsed_tasks)

    # Validate and set domain tasks
    if parsed_tasks:
        validated_tasks = [_validate_domain_task(t, state) for t in parsed_tasks]
    else:
        validated_tasks = [_default_observability_task(state)]

    # HM11: Enrich tasks with skill_scope based on available capabilities
    skill_surface = None
    if agent_context is not None:
        skill_surface = agent_context.skill_surface.to_dict() if agent_context.skill_surface else None

    state.domain_tasks = [
        _enrich_task_with_skill_scope(t, skill_surface)
        for t in validated_tasks
    ]

    # Store route context
    state.route_context = {
        "routed_at": int(time.time() * 1000),
        "domain_count": len(state.domain_tasks),
        "domains": [t.get("domain") for t in state.domain_tasks],
        "mode": "routed",
    }

    # Report
    report_node_action(
        state,
        runtime,
        node_name="route_domains",
        tool_name="agent.route",
        request_json={"incident_context": state.incident_context},
        response_json={"domain_tasks": state.domain_tasks},
        started_ms=started_ms,
        status="ok",
        count_in_state=False,
    )

    return state
