"""Domain Agent nodes for hybrid multi-agent system.

This module implements domain-specific agents that execute investigation
tasks assigned by the Route Agent.

Phase HM3: Route Agent + Observability Agent MVP.
Phase HM4: Extended to support Change and Knowledge domains.
"""
from __future__ import annotations

import json
import time
from dataclasses import dataclass, field
from typing import TYPE_CHECKING, Any

from ..constants import TRACE_EVENT_DOMAIN_EXECUTE
from ..middleware.base import AgentRequest, AgentResponse
from .reporting import report_node_action

if TYPE_CHECKING:
    from ..middleware.chain import MiddlewareChain
    from ..runtime.resolved_context import ResolvedAgentContext
    from ..runtime.runtime import OrchestratorRuntime
    from ..state import GraphState
    from .config import OrchestratorConfig


@dataclass
class DomainFinding:
    """Finding from a domain agent investigation.

    Attributes:
        domain: Domain that produced this finding.
        summary: Brief summary of findings.
        evidence_candidates: List of evidence candidates.
        diagnosis_patch: Patch to apply to diagnosis.
        session_patch_proposal: Proposed session updates.
        status: Status of this finding (ok, degraded, error).
    """

    domain: str
    summary: str
    evidence_candidates: list[dict[str, Any]] = field(default_factory=list)
    diagnosis_patch: dict[str, Any] = field(default_factory=dict)
    session_patch_proposal: dict[str, Any] = field(default_factory=dict)
    status: str = "ok"

    def to_dict(self) -> dict[str, Any]:
        """Convert to dictionary for JSON serialization."""
        return {
            "domain": self.domain,
            "summary": self.summary,
            "evidence_candidates": self.evidence_candidates,
            "diagnosis_patch": self.diagnosis_patch,
            "session_patch_proposal": self.session_patch_proposal,
            "status": self.status,
        }

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "DomainFinding":
        """Create from dictionary.

        Args:
            data: Dictionary containing finding data.

        Returns:
            DomainFinding instance.
        """
        return cls(
            domain=str(data.get("domain") or ""),
            summary=str(data.get("summary") or ""),
            evidence_candidates=list(data.get("evidence_candidates") or []),
            diagnosis_patch=dict(data.get("diagnosis_patch") or {}),
            session_patch_proposal=dict(data.get("session_patch_proposal") or {}),
            status=str(data.get("status") or "ok"),
        )


def _is_domain_agent_enabled(domain: str) -> bool:
    """Check if a domain agent is enabled.

    Args:
        domain: Domain name (observability, change, knowledge).

    Returns:
        True if the domain agent is enabled.
    """
    import os

    if domain == "change":
        env = os.environ.get("RCA_DOMAIN_AGENT_CHANGE_ENABLED", "true").strip().lower()
    elif domain == "knowledge":
        env = os.environ.get("RCA_DOMAIN_AGENT_KNOWLEDGE_ENABLED", "true").strip().lower()
    else:
        # observability is always enabled when route agent is on
        return True

    return env not in ("false", "0", "no", "off")


def _find_task_for_domain(state: "GraphState", domain: str) -> dict[str, Any] | None:
    """Find the task for a specific domain.

    Args:
        state: Current graph state.
        domain: Domain name to find task for.

    Returns:
        Task dictionary or None if not found.
    """
    for task in state.domain_tasks:
        if isinstance(task, dict) and task.get("domain") == domain:
            return task
    return None


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

    Args:
        runtime: Orchestrator runtime instance.

    Returns:
        LLM instance or None if not configured.
    """
    skill_agent = getattr(runtime, "_skill_agent", None)
    if skill_agent is None:
        return None
    if not bool(getattr(skill_agent, "configured", False)):
        return None
    try:
        return skill_agent._get_llm()  # noqa: SLF001
    except Exception:  # noqa: BLE001
        return None


def _append_empty_finding(state: "GraphState", domain: str, reason: str) -> None:
    """Append an empty/degraded finding to state.

    Args:
        state: Current graph state.
        domain: Domain name.
        reason: Reason for the degraded finding.
    """
    finding = DomainFinding(
        domain=domain,
        summary=f"No finding: {reason}",
        status="degraded",
    )
    state.domain_findings.append(finding.to_dict())


def _build_observability_system_prompt(task: dict[str, Any]) -> str:
    """Build the system prompt for the observability agent.

    Args:
        task: Domain task dictionary.

    Returns:
        System prompt string.
    """
    return """You are an RCA Observability Agent. Your job is to investigate observability data (metrics, logs, traces) to find root causes.

Use the available tools to:
1. Query metrics for anomalies, spikes, or patterns
2. Search logs for errors, warnings, or suspicious patterns
3. Investigate traces for latency issues or failures

After investigation, provide:
1. A summary of your findings
2. Evidence candidates (what data supports your conclusions)
3. A diagnosis patch (potential root causes and confidence level)

Be thorough but efficient. Focus on the most relevant data first."""


def _build_observability_user_prompt(state: "GraphState", task: dict[str, Any]) -> str:
    """Build the user prompt for the observability agent.

    Args:
        state: Current graph state.
        task: Domain task dictionary.

    Returns:
        User prompt string.
    """
    incident_context = state.incident_context or {}
    incident_id = state.incident_id or "unknown"
    goal = task.get("goal", "Investigate observability data")

    context_parts = [
        f"Incident ID: {incident_id}",
        f"Task: {goal}",
    ]

    service = incident_context.get("service")
    if service:
        context_parts.append(f"Service: {service}")

    namespace = incident_context.get("namespace")
    if namespace:
        context_parts.append(f"Namespace: {namespace}")

    severity = incident_context.get("severity")
    if severity:
        context_parts.append(f"Severity: {severity}")

    # Add any additional context
    for key, value in incident_context.items():
        if key not in ("service", "namespace", "severity", "alert_name"):
            context_parts.append(f"{key}: {value}")

    return f"""Investigate this incident:

{chr(10).join(context_parts)}

Gather relevant observability data and report your findings."""


def _build_change_system_prompt(task: dict[str, Any]) -> str:
    """Build the system prompt for the change agent.

    Args:
        task: Domain task dictionary.

    Returns:
        System prompt string.
    """
    return """You are an RCA Change Agent. Your job is to investigate change events, deployments, and configuration changes that may correlate with the incident.

Use the available tools to:
1. Query deployment history for recent changes
2. Check configuration diffs for potential issues
3. Correlate change timelines with incident onset

After investigation, provide:
1. A summary of relevant changes found
2. Evidence candidates (which changes correlate with the incident)
3. A diagnosis patch (potential root causes related to changes)

Focus on changes that occurred around the time of the incident. Be precise with timestamps."""


def _build_change_user_prompt(state: "GraphState", task: dict[str, Any]) -> str:
    """Build the user prompt for the change agent.

    Args:
        state: Current graph state.
        task: Domain task dictionary.

    Returns:
        User prompt string.
    """
    incident_context = state.incident_context or {}
    incident_id = state.incident_id or "unknown"
    goal = task.get("goal", "Investigate change events")

    context_parts = [
        f"Incident ID: {incident_id}",
        f"Task: {goal}",
    ]

    service = incident_context.get("service")
    if service:
        context_parts.append(f"Service: {service}")

    namespace = incident_context.get("namespace")
    if namespace:
        context_parts.append(f"Namespace: {namespace}")

    # Add incident time for change correlation
    incident_time = incident_context.get("incident_time") or incident_context.get("triggered_at")
    if incident_time:
        context_parts.append(f"Incident Time: {incident_time}")

    # Add any additional context
    for key, value in incident_context.items():
        if key not in ("service", "namespace", "severity", "alert_name", "incident_time", "triggered_at"):
            context_parts.append(f"{key}: {value}")

    return f"""Investigate changes related to this incident:

{chr(10).join(context_parts)}

Look for deployments, configuration changes, or infrastructure updates around the incident time."""


def _build_knowledge_system_prompt(task: dict[str, Any]) -> str:
    """Build the system prompt for the knowledge agent.

    Args:
        task: Domain task dictionary.

    Returns:
        System prompt string.
    """
    return """You are an RCA Knowledge Agent. Your job is to search the knowledge base, historical incidents, and runbooks to find relevant context for the current incident.

Use the available tools to:
1. Search for similar historical incidents
2. Find relevant runbooks and documentation
3. Look up known solutions for similar error patterns

After investigation, provide:
1. A summary of relevant knowledge found
2. Evidence candidates (historical incidents, runbooks, etc.)
3. A diagnosis patch (suggested solutions from knowledge base)

Focus on actionable insights that can help resolve the current incident."""


def _build_knowledge_user_prompt(state: "GraphState", task: dict[str, Any]) -> str:
    """Build the user prompt for the knowledge agent.

    Args:
        state: Current graph state.
        task: Domain task dictionary.

    Returns:
        User prompt string.
    """
    incident_context = state.incident_context or {}
    incident_id = state.incident_id or "unknown"
    goal = task.get("goal", "Search knowledge base")

    context_parts = [
        f"Incident ID: {incident_id}",
        f"Task: {goal}",
    ]

    service = incident_context.get("service")
    if service:
        context_parts.append(f"Service: {service}")

    # Add alert name for knowledge matching
    alert_name = incident_context.get("alert_name")
    if alert_name:
        context_parts.append(f"Alert: {alert_name}")

    # Add error patterns if available
    error_message = incident_context.get("error_message") or incident_context.get("error")
    if error_message:
        context_parts.append(f"Error: {error_message}")

    # Add any additional context
    for key, value in incident_context.items():
        if key not in ("service", "namespace", "severity", "alert_name", "incident_time", "triggered_at", "error_message", "error"):
            context_parts.append(f"{key}: {value}")

    return f"""Search knowledge base for this incident:

{chr(10).join(context_parts)}

Find relevant historical incidents, runbooks, or documentation that can help diagnose and resolve this issue."""


def _build_messages(system_prompt: str, user_prompt: str) -> list[Any]:
    """Build messages for LLM invocation.

    Args:
        system_prompt: System prompt string.
        user_prompt: User prompt string.

    Returns:
        List of messages.
    """
    try:
        from langchain_core.messages import HumanMessage, SystemMessage
    except ImportError:
        return []

    return [
        SystemMessage(content=system_prompt),
        HumanMessage(content=user_prompt),
    ]


def _execute_tool_calls(
    state: "GraphState",
    runtime: "OrchestratorRuntime",
    adapter: Any,
    tool_calls: list[Any],
    domain: str = "observability",
) -> list[dict[str, Any]]:
    """Execute tool calls from the agent.

    Args:
        state: Current graph state.
        runtime: Orchestrator runtime instance.
        adapter: Function calling adapter.
        tool_calls: List of tool calls to execute.
        domain: Domain name for source tracking.

    Returns:
        List of execution results.
    """
    results: list[dict[str, Any]] = []

    for call in tool_calls:
        tool_name = getattr(call, "name", "") or getattr(call, "tool_name", "")
        if not tool_name:
            continue

        # Get arguments
        args = {}
        if hasattr(call, "args"):
            args = call.args or {}
        elif hasattr(call, "arguments"):
            args = call.arguments or {}

        try:
            executed_call = runtime.execute_tool(
                tool_name=tool_name,
                args=args,
                source=f"graph.{domain}_agent",
            )
            results.append({
                "tool": tool_name,
                "status": executed_call.status,
                "result": executed_call.response_json,
                "error": executed_call.error,
            })

            # Save evidence if successful
            if executed_call.status == "ok" and executed_call.response_json:
                _save_evidence_from_tool_call(state, runtime, tool_name, args, executed_call, domain)

        except Exception as exc:  # noqa: BLE001
            results.append({
                "tool": tool_name,
                "status": "error",
                "result": None,
                "error": str(exc),
            })

    return results


def _save_evidence_from_tool_call(
    state: "GraphState",
    runtime: "OrchestratorRuntime",
    tool_name: str,
    args: dict[str, Any],
    executed_call: Any,
    domain: str = "observability",
) -> None:
    """Save evidence from a successful tool call.

    Args:
        state: Current graph state.
        runtime: Orchestrator runtime instance.
        tool_name: Tool name.
        args: Tool arguments.
        executed_call: Executed tool call result.
        domain: Domain name for evidence categorization.
    """
    try:
        from .helpers import append_evidence, query_result_is_no_data

        query_result = executed_call.response_json
        if not isinstance(query_result, dict):
            return

        # Determine kind based on tool name and domain
        kind = "metrics"
        name_lower = tool_name.lower()
        if domain == "change":
            kind = "changes"
        elif domain == "knowledge":
            kind = "knowledge"
        elif "log" in name_lower or "loki" in name_lower:
            kind = "logs"
        elif "trace" in name_lower or "tempo" in name_lower:
            kind = "traces"

        query_request = {
            "tool": tool_name,
            "params": args,
            "queryText": f"{domain.capitalize()} agent query for {tool_name}",
        }

        published = runtime.save_evidence_from_query(
            incident_id=state.incident_id or "",
            node_name=f"run_{domain}_agent",
            kind=kind,
            query=query_request,
            result=query_result,
            query_hash_source=query_request,
        )

        evidence_id = published.evidence_id
        no_data = query_result_is_no_data(query_result)
        append_evidence(state, evidence_id, source=kind, no_data=no_data, conflict_hint=False)

    except Exception:  # noqa: BLE001
        pass


def _extract_finding_from_response(
    domain: str,
    content: str,
    state: "GraphState",
) -> dict[str, Any]:
    """Extract a finding from the LLM response.

    Args:
        domain: Domain name.
        content: LLM response content.
        state: Current graph state.

    Returns:
        Finding dictionary.
    """
    finding = DomainFinding(
        domain=domain,
        summary="Investigation completed",
        status="ok",
    )

    # Try to extract structured finding from response
    content_str = str(content or "").strip()
    if not content_str:
        finding.summary = "No response from agent"
        finding.status = "degraded"
        return finding.to_dict()

    # Look for structured output in the response
    try:
        # Try to find JSON in response
        import re
        json_match = re.search(r"```(?:json)?\s*([\s\S]*?)\s*```", content_str)
        if json_match:
            data = json.loads(json_match.group(1))
            if isinstance(data, dict):
                if "summary" in data:
                    finding.summary = str(data["summary"])
                if "evidence_candidates" in data:
                    finding.evidence_candidates = list(data["evidence_candidates"] or [])
                if "diagnosis_patch" in data:
                    finding.diagnosis_patch = dict(data["diagnosis_patch"] or {})
                if "session_patch_proposal" in data:
                    finding.session_patch_proposal = dict(data["session_patch_proposal"] or {})
    except (json.JSONDecodeError, TypeError):
        pass

    # Use the content as summary if no structured output found
    if finding.summary == "Investigation completed":
        # Take first 500 chars as summary
        finding.summary = content_str[:500] if len(content_str) > 500 else content_str

    return finding.to_dict()


def run_observability_agent(
    state: "GraphState",
    cfg: "OrchestratorConfig",
    runtime: "OrchestratorRuntime",
) -> "GraphState":
    """Observability Domain Agent: Investigate observability data.

    This agent handles the observability domain - metrics, logs, traces.
    It reuses the existing run_tool_agent pattern but with middleware
    for domain-specific context injection.

    Args:
        state: Current graph state.
        cfg: Orchestrator configuration.
        runtime: Orchestrator runtime instance.

    Returns:
        Updated graph state with domain_findings populated.
    """
    started_ms = int(time.time() * 1000)

    # Find observability task
    obs_task = _find_task_for_domain(state, "observability")
    if obs_task is None:
        state.add_degrade_reason("no_observability_task")
        _append_empty_finding(state, "observability", "no_task")
        report_node_action(
            state,
            runtime,
            node_name="run_observability_agent",
            tool_name="agent.observability",
            request_json={},
            response_json={
                "status": "error",
                "reason": "no_observability_task",
            },
            started_ms=started_ms,
            status="error",
            count_in_state=False,
        )
        return state

    # Get LLM
    llm = _get_llm(runtime)
    if llm is None:
        state.add_degrade_reason("llm_not_configured")
        _append_empty_finding(state, "observability", "no_llm")
        report_node_action(
            state,
            runtime,
            node_name="run_observability_agent",
            tool_name="agent.observability",
            request_json={"task": obs_task},
            response_json={
                "status": "error",
                "reason": "llm_not_configured",
            },
            started_ms=started_ms,
            status="error",
            count_in_state=False,
        )
        return state

    # Get FC adapter for tool surface
    adapter = runtime.get_fc_adapter()
    if adapter is None:
        state.add_degrade_reason("no_fc_adapter")
        _append_empty_finding(state, "observability", "no_adapter")
        report_node_action(
            state,
            runtime,
            node_name="run_observability_agent",
            tool_name="agent.observability",
            request_json={"task": obs_task},
            response_json={
                "status": "error",
                "reason": "no_fc_adapter",
            },
            started_ms=started_ms,
            status="error",
            count_in_state=False,
        )
        return state

    # Build agent context
    agent_context = _get_agent_context(state)
    middleware_chain: "MiddlewareChain | None" = getattr(cfg, "middleware_chain", None)
    middleware_enabled: bool = getattr(cfg, "middleware_enabled", False)

    # Build request with middleware
    system_prompt = _build_observability_system_prompt(obs_task)
    user_prompt = _build_observability_user_prompt(state, obs_task)

    # Extract task-level scopes for tool/skill filtering
    tool_scope = list(obs_task.get("tool_scope") or [])
    skill_scope = list(obs_task.get("skill_scope") or [])

    request = AgentRequest(
        system_prompt=system_prompt,
        user_prompt=user_prompt,
        metadata={
            "node": "run_observability_agent",
            "trace_event": TRACE_EVENT_DOMAIN_EXECUTE,
        },
    )

    # Build middleware config with task-level scopes
    middleware_config: dict[str, Any] = {
        "mode": "fc_surface",
        "surface": "graph",
        "domain": "observability",
    }
    # P1: Pass tool_scope to middleware for task-level filtering
    if tool_scope:
        middleware_config["tool_scope"] = tool_scope

    if middleware_enabled and middleware_chain is not None and agent_context is not None:
        prepared = middleware_chain.prepare(
            state=state,
            context=agent_context,
            request=request,
            config=middleware_config,
        )
    else:
        prepared = request

    # Get tools for observability domain
    # P1: If tool_scope is specified, filter tools to only those in scope
    if tool_scope:
        # Filter tools based on task-level scope
        all_tools = adapter.to_openai_tools_for_graph()
        tool_scope_set = set(tool_scope)
        openai_tools = [
            t for t in all_tools
            if isinstance(t, dict) and t.get("function", {}).get("name") in tool_scope_set
        ]
        if len(openai_tools) < len(all_tools):
            state.add_degrade_reason(f"tools_filtered_by_scope:{len(all_tools)-len(openai_tools)}_removed")
    else:
        openai_tools = adapter.to_openai_tools_for_graph()

    agent_error: str | None = None
    try:
        # Invoke LLM with tools
        llm_with_tools = llm.bind_tools(openai_tools)
        messages = _build_messages(prepared.system_prompt, prepared.user_prompt)
        response = llm_with_tools.invoke(messages)

        # Process tool calls if any
        tool_calls = getattr(response, "tool_calls", []) or []
        if tool_calls:
            _execute_tool_calls(state, runtime, adapter, tool_calls)

        # Extract finding
        content = getattr(response, "content", "") or ""
        finding = _extract_finding_from_response("observability", content, state)

    except Exception as exc:  # noqa: BLE001
        agent_error = str(exc)[:128]
        state.add_degrade_reason(f"observability_agent_error:{str(exc)[:64]}")
        finding = DomainFinding(
            domain="observability",
            summary=f"Agent error: {exc}",
            status="error",
        ).to_dict()

    state.domain_findings.append(finding)

    # P2: Report error status when agent fails
    report_node_action(
        state,
        runtime,
        node_name="run_observability_agent",
        tool_name="agent.observability",
        request_json={
            "task": obs_task,
            "tool_scope": tool_scope if tool_scope else None,
        },
        response_json={
            "finding": finding,
            "tool_count": len(openai_tools),
        },
        started_ms=started_ms,
        status="error" if agent_error else "ok",
        error=agent_error,
        count_in_state=False,
    )

    return state


def run_change_agent(
    state: "GraphState",
    cfg: "OrchestratorConfig",
    runtime: "OrchestratorRuntime",
) -> "GraphState":
    """Change Domain Agent: Investigate change events and deployments.

    This agent handles the change domain - deployments, configurations,
    infrastructure changes that may correlate with the incident.

    Args:
        state: Current graph state.
        cfg: Orchestrator configuration.
        runtime: Orchestrator runtime instance.

    Returns:
        Updated graph state with domain_findings populated.
    """
    started_ms = int(time.time() * 1000)

    # Check if change agent is enabled
    if not _is_domain_agent_enabled("change"):
        report_node_action(
            state,
            runtime,
            node_name="run_change_agent",
            tool_name="agent.change",
            request_json={},
            response_json={
                "status": "skipped",
                "reason": "change_agent_disabled",
            },
            started_ms=started_ms,
            status="ok",
            count_in_state=False,
        )
        return state

    # Find change task
    change_task = _find_task_for_domain(state, "change")
    if change_task is None:
        # No change task assigned - this is OK, not all incidents need change analysis
        report_node_action(
            state,
            runtime,
            node_name="run_change_agent",
            tool_name="agent.change",
            request_json={},
            response_json={
                "status": "skipped",
                "reason": "no_change_task",
            },
            started_ms=started_ms,
            status="ok",
            count_in_state=False,
        )
        return state

    # Get LLM
    llm = _get_llm(runtime)
    if llm is None:
        state.add_degrade_reason("llm_not_configured_for_change")
        _append_empty_finding(state, "change", "no_llm")
        report_node_action(
            state,
            runtime,
            node_name="run_change_agent",
            tool_name="agent.change",
            request_json={"task": change_task},
            response_json={
                "status": "error",
                "reason": "llm_not_configured",
            },
            started_ms=started_ms,
            status="error",
            count_in_state=False,
        )
        return state

    # Get FC adapter for tool surface
    adapter = runtime.get_fc_adapter()
    if adapter is None:
        state.add_degrade_reason("no_fc_adapter_for_change")
        _append_empty_finding(state, "change", "no_adapter")
        report_node_action(
            state,
            runtime,
            node_name="run_change_agent",
            tool_name="agent.change",
            request_json={"task": change_task},
            response_json={
                "status": "error",
                "reason": "no_fc_adapter",
            },
            started_ms=started_ms,
            status="error",
            count_in_state=False,
        )
        return state

    # Build agent context
    agent_context = _get_agent_context(state)
    middleware_chain: "MiddlewareChain | None" = getattr(cfg, "middleware_chain", None)
    middleware_enabled: bool = getattr(cfg, "middleware_enabled", False)

    # Build request with middleware
    system_prompt = _build_change_system_prompt(change_task)
    user_prompt = _build_change_user_prompt(state, change_task)

    # Extract task-level scopes for tool/skill filtering
    tool_scope = list(change_task.get("tool_scope") or [])

    request = AgentRequest(
        system_prompt=system_prompt,
        user_prompt=user_prompt,
        metadata={
            "node": "run_change_agent",
            "trace_event": TRACE_EVENT_DOMAIN_EXECUTE,
        },
    )

    # Build middleware config with task-level scopes
    middleware_config: dict[str, Any] = {
        "mode": "fc_surface",
        "surface": "graph",
        "domain": "change",
    }
    if tool_scope:
        middleware_config["tool_scope"] = tool_scope

    if middleware_enabled and middleware_chain is not None and agent_context is not None:
        prepared = middleware_chain.prepare(
            state=state,
            context=agent_context,
            request=request,
            config=middleware_config,
        )
    else:
        prepared = request

    # Get tools for change domain
    if tool_scope:
        all_tools = adapter.to_openai_tools_for_graph()
        tool_scope_set = set(tool_scope)
        openai_tools = [
            t for t in all_tools
            if isinstance(t, dict) and t.get("function", {}).get("name") in tool_scope_set
        ]
        if len(openai_tools) < len(all_tools):
            state.add_degrade_reason(f"change_tools_filtered_by_scope:{len(all_tools)-len(openai_tools)}_removed")
    else:
        openai_tools = adapter.to_openai_tools_for_graph()

    agent_error: str | None = None
    try:
        # Invoke LLM with tools
        llm_with_tools = llm.bind_tools(openai_tools)
        messages = _build_messages(prepared.system_prompt, prepared.user_prompt)
        response = llm_with_tools.invoke(messages)

        # Process tool calls if any
        tool_calls = getattr(response, "tool_calls", []) or []
        if tool_calls:
            _execute_tool_calls(state, runtime, adapter, tool_calls, domain="change")

        # Extract finding
        content = getattr(response, "content", "") or ""
        finding = _extract_finding_from_response("change", content, state)

    except Exception as exc:  # noqa: BLE001
        agent_error = str(exc)[:128]
        state.add_degrade_reason(f"change_agent_error:{str(exc)[:64]}")
        finding = DomainFinding(
            domain="change",
            summary=f"Agent error: {exc}",
            status="error",
        ).to_dict()

    state.domain_findings.append(finding)

    report_node_action(
        state,
        runtime,
        node_name="run_change_agent",
        tool_name="agent.change",
        request_json={
            "task": change_task,
            "tool_scope": tool_scope if tool_scope else None,
        },
        response_json={
            "finding": finding,
            "tool_count": len(openai_tools),
        },
        started_ms=started_ms,
        status="error" if agent_error else "ok",
        error=agent_error,
        count_in_state=False,
    )

    return state


def run_knowledge_agent(
    state: "GraphState",
    cfg: "OrchestratorConfig",
    runtime: "OrchestratorRuntime",
) -> "GraphState":
    """Knowledge Domain Agent: Search knowledge base and documentation.

    This agent handles the knowledge domain - searching historical
    incidents, runbooks, documentation, and known solutions.

    Args:
        state: Current graph state.
        cfg: Orchestrator configuration.
        runtime: Orchestrator runtime instance.

    Returns:
        Updated graph state with domain_findings populated.
    """
    started_ms = int(time.time() * 1000)

    # Check if knowledge agent is enabled
    if not _is_domain_agent_enabled("knowledge"):
        report_node_action(
            state,
            runtime,
            node_name="run_knowledge_agent",
            tool_name="agent.knowledge",
            request_json={},
            response_json={
                "status": "skipped",
                "reason": "knowledge_agent_disabled",
            },
            started_ms=started_ms,
            status="ok",
            count_in_state=False,
        )
        return state

    # Find knowledge task
    knowledge_task = _find_task_for_domain(state, "knowledge")
    if knowledge_task is None:
        # No knowledge task assigned - this is OK, not all incidents need knowledge lookup
        report_node_action(
            state,
            runtime,
            node_name="run_knowledge_agent",
            tool_name="agent.knowledge",
            request_json={},
            response_json={
                "status": "skipped",
                "reason": "no_knowledge_task",
            },
            started_ms=started_ms,
            status="ok",
            count_in_state=False,
        )
        return state

    # Get LLM
    llm = _get_llm(runtime)
    if llm is None:
        state.add_degrade_reason("llm_not_configured_for_knowledge")
        _append_empty_finding(state, "knowledge", "no_llm")
        report_node_action(
            state,
            runtime,
            node_name="run_knowledge_agent",
            tool_name="agent.knowledge",
            request_json={"task": knowledge_task},
            response_json={
                "status": "error",
                "reason": "llm_not_configured",
            },
            started_ms=started_ms,
            status="error",
            count_in_state=False,
        )
        return state

    # Get FC adapter for tool surface
    adapter = runtime.get_fc_adapter()
    if adapter is None:
        state.add_degrade_reason("no_fc_adapter_for_knowledge")
        _append_empty_finding(state, "knowledge", "no_adapter")
        report_node_action(
            state,
            runtime,
            node_name="run_knowledge_agent",
            tool_name="agent.knowledge",
            request_json={"task": knowledge_task},
            response_json={
                "status": "error",
                "reason": "no_fc_adapter",
            },
            started_ms=started_ms,
            status="error",
            count_in_state=False,
        )
        return state

    # Build agent context
    agent_context = _get_agent_context(state)
    middleware_chain: "MiddlewareChain | None" = getattr(cfg, "middleware_chain", None)
    middleware_enabled: bool = getattr(cfg, "middleware_enabled", False)

    # Build request with middleware
    system_prompt = _build_knowledge_system_prompt(knowledge_task)
    user_prompt = _build_knowledge_user_prompt(state, knowledge_task)

    # Extract task-level scopes for tool/skill filtering
    tool_scope = list(knowledge_task.get("tool_scope") or [])

    request = AgentRequest(
        system_prompt=system_prompt,
        user_prompt=user_prompt,
        metadata={
            "node": "run_knowledge_agent",
            "trace_event": TRACE_EVENT_DOMAIN_EXECUTE,
        },
    )

    # Build middleware config with task-level scopes
    middleware_config: dict[str, Any] = {
        "mode": "fc_surface",
        "surface": "graph",
        "domain": "knowledge",
    }
    if tool_scope:
        middleware_config["tool_scope"] = tool_scope

    if middleware_enabled and middleware_chain is not None and agent_context is not None:
        prepared = middleware_chain.prepare(
            state=state,
            context=agent_context,
            request=request,
            config=middleware_config,
        )
    else:
        prepared = request

    # Get tools for knowledge domain
    if tool_scope:
        all_tools = adapter.to_openai_tools_for_graph()
        tool_scope_set = set(tool_scope)
        openai_tools = [
            t for t in all_tools
            if isinstance(t, dict) and t.get("function", {}).get("name") in tool_scope_set
        ]
        if len(openai_tools) < len(all_tools):
            state.add_degrade_reason(f"knowledge_tools_filtered_by_scope:{len(all_tools)-len(openai_tools)}_removed")
    else:
        openai_tools = adapter.to_openai_tools_for_graph()

    agent_error: str | None = None
    try:
        # Invoke LLM with tools
        llm_with_tools = llm.bind_tools(openai_tools)
        messages = _build_messages(prepared.system_prompt, prepared.user_prompt)
        response = llm_with_tools.invoke(messages)

        # Process tool calls if any
        tool_calls = getattr(response, "tool_calls", []) or []
        if tool_calls:
            _execute_tool_calls(state, runtime, adapter, tool_calls, domain="knowledge")

        # Extract finding
        content = getattr(response, "content", "") or ""
        finding = _extract_finding_from_response("knowledge", content, state)

    except Exception as exc:  # noqa: BLE001
        agent_error = str(exc)[:128]
        state.add_degrade_reason(f"knowledge_agent_error:{str(exc)[:64]}")
        finding = DomainFinding(
            domain="knowledge",
            summary=f"Agent error: {exc}",
            status="error",
        ).to_dict()

    state.domain_findings.append(finding)

    report_node_action(
        state,
        runtime,
        node_name="run_knowledge_agent",
        tool_name="agent.knowledge",
        request_json={
            "task": knowledge_task,
            "tool_scope": tool_scope if tool_scope else None,
        },
        response_json={
            "finding": finding,
            "tool_count": len(openai_tools),
        },
        started_ms=started_ms,
        status="error" if agent_error else "ok",
        error=agent_error,
        count_in_state=False,
    )

    return state


def merge_domain_findings(
    state: "GraphState",
    cfg: "OrchestratorConfig",
    runtime: "OrchestratorRuntime",
) -> "GraphState":
    """Merge findings from all domain agents.

    This node collects findings from all domain agents and merges
    them into a unified structure for downstream processing.

    Args:
        state: Current graph state.
        cfg: Orchestrator configuration.
        runtime: Orchestrator runtime instance.

    Returns:
        Updated graph state with merged_findings populated.
    """
    started_ms = int(time.time() * 1000)

    merged_candidates: list[dict[str, Any]] = []
    merged_diagnosis_patch: dict[str, Any] = {}
    merged_session_patch: dict[str, Any] = {}

    for finding in state.domain_findings:
        if not isinstance(finding, dict):
            continue

        # Merge evidence candidates
        candidates = finding.get("evidence_candidates") or []
        if isinstance(candidates, list):
            merged_candidates.extend(candidates)

        # Merge diagnosis patches (later domains override)
        patch = finding.get("diagnosis_patch") or {}
        if isinstance(patch, dict):
            merged_diagnosis_patch.update(patch)

        # Merge session patches
        session = finding.get("session_patch_proposal") or {}
        if isinstance(session, dict):
            merged_session_patch.update(session)

    state.merged_findings = {
        "evidence_candidates": merged_candidates,
        "diagnosis_patch": merged_diagnosis_patch,
        "session_patch_proposal": merged_session_patch,
        "domain_count": len(state.domain_findings),
        "domains": [f.get("domain") for f in state.domain_findings if isinstance(f, dict)],
    }

    # Copy evidence candidates to state for merge_evidence compatibility
    state.evidence_candidates = merged_candidates

    report_node_action(
        state,
        runtime,
        node_name="merge_domain_findings",
        tool_name="agent.merge",
        request_json={
            "domain_count": len(state.domain_findings),
        },
        response_json={
            "status": "ok",
            "merged_findings": state.merged_findings,
        },
        started_ms=started_ms,
        status="ok",
        count_in_state=False,
    )

    return state