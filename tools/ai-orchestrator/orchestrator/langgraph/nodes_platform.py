"""Platform Special Agent nodes for hybrid multi-agent system.

This module implements the Platform Special Agent that synthesizes
findings from all domain agents into a cohesive diagnosis.

Phase HM5: Platform Special Agent for diagnosis summarization.
"""
from __future__ import annotations

import json
import os
import time
from typing import TYPE_CHECKING, Any

from ..constants import TRACE_EVENT_PLATFORM_SPECIAL_SUMMARIZE
from ..middleware.base import AgentRequest
from .reporting import report_node_action

if TYPE_CHECKING:
    from ..middleware.chain import MiddlewareChain
    from ..runtime.resolved_context import ResolvedAgentContext
    from ..runtime.runtime import OrchestratorRuntime
    from ..state import GraphState
    from .config import OrchestratorConfig


def _is_platform_special_agent_enabled() -> bool:
    """Check if platform special agent is enabled.

    Returns:
        True if platform special agent should be used.
    """
    env = os.environ.get("RCA_PLATFORM_SPECIAL_AGENT_ENABLED", "true").strip().lower()
    return env not in ("false", "0", "no", "off")


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

    Uses the independent graph LLM (HM7-1), not prompt_first skill agent.
    Falls back to legacy _skill_agent path for backward compatibility.

    Args:
        runtime: Orchestrator runtime instance.

    Returns:
        LLM instance or None if not configured.
    """
    # Primary path: use independent graph LLM (HM7-1)
    get_graph_llm = getattr(runtime, "get_graph_llm", None)
    if callable(get_graph_llm):
        llm = get_graph_llm()
        if llm is not None:
            return llm

    # Fallback: legacy _skill_agent path (for backward compatibility)
    skill_agent = getattr(runtime, "_skill_agent", None)
    if skill_agent is None:
        return None
    if not bool(getattr(skill_agent, "configured", False)):
        return None
    try:
        return skill_agent._get_llm()  # noqa: SLF001
    except Exception:  # noqa: BLE001
        return None


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


def _build_platform_special_system_prompt() -> str:
    """Build the system prompt for the platform special agent.

    Returns:
        System prompt string.
    """
    return """You are an RCA Platform Special Agent. Your job is to synthesize findings from domain agents into a cohesive diagnosis.

You will receive:
1. Merged findings from domain agents (observability, change, knowledge)
2. Incident context (service, namespace, severity, etc.)
3. Evidence metadata

Your task is to produce a diagnosis patch that:
1. Summarizes the root cause concisely
2. Provides confidence level based on evidence quality
3. Lists recommendations and next steps
4. Identifies remaining unknowns

Output format (JSON):
{
  "diagnosis_patch": {
    "summary": "Brief summary of root cause",
    "root_cause": {
      "summary": "Detailed root cause explanation",
      "statement": "One-line root cause statement",
      "confidence": 0.75
    },
    "recommendations": [
      {"type": "action_type", "action": "description", "risk": "low|medium|high"}
    ],
    "unknowns": ["Remaining unknown items"],
    "next_steps": ["Suggested next actions"]
  }
}

Rules:
- Base your diagnosis on the domain findings provided
- Set confidence between 0.0 and 1.0 based on evidence quality
- Be honest about unknowns and limitations
- Provide actionable recommendations
- Output ONLY the JSON, no other text"""


def _build_platform_special_user_prompt(state: "GraphState") -> str:
    """Build the user prompt for the platform special agent.

    Args:
        state: Current graph state.

    Returns:
        User prompt string.
    """
    from .quality import ensure_quality_gate

    incident_context = state.incident_context or {}
    merged_findings = state.merged_findings or {}

    context_parts = [
        f"Incident ID: {state.incident_id or 'unknown'}",
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

    alert_name = incident_context.get("alert_name")
    if alert_name:
        context_parts.append(f"Alert: {alert_name}")

    # Add merged findings summary
    domain_count = merged_findings.get("domain_count", 0)
    domains = merged_findings.get("domains", [])
    context_parts.append(f"\nDomains investigated: {domain_count} ({', '.join(str(d) for d in domains)})")

    # Add diagnosis patches from domain agents
    diagnosis_patch = merged_findings.get("diagnosis_patch") or {}
    if diagnosis_patch:
        context_parts.append(f"\nDomain diagnosis patches:\n{json.dumps(diagnosis_patch, indent=2)}")

    # Add evidence summary
    evidence_ids = state.evidence_ids or []
    context_parts.append(f"\nEvidence collected: {len(evidence_ids)} items")

    # Add quality gate status
    quality_gate = ensure_quality_gate(state)
    decision = quality_gate.get("decision", "unknown")
    context_parts.append(f"\nQuality gate: {decision}")

    return f"""Synthesize the following into a diagnosis:

{chr(10).join(context_parts)}

Provide your diagnosis patch as JSON."""


def _parse_diagnosis_patch(content: str) -> dict[str, Any]:
    """Parse diagnosis patch from LLM response content.

    Args:
        content: Raw LLM response content.

    Returns:
        Parsed diagnosis patch dictionary.
    """
    import re

    content = str(content or "").strip()
    if not content:
        return {}

    # Try direct JSON parse
    try:
        data = json.loads(content)
        if isinstance(data, dict):
            # Check for wrapped format
            if "diagnosis_patch" in data:
                return data["diagnosis_patch"]
            return data
    except json.JSONDecodeError:
        pass

    # Try to extract JSON from code block
    match = re.search(r"```(?:json)?\s*([\s\S]*?)\s*```", content)
    if match:
        try:
            data = json.loads(match.group(1))
            if isinstance(data, dict):
                if "diagnosis_patch" in data:
                    return data["diagnosis_patch"]
                return data
        except json.JSONDecodeError:
            pass

    return {}


def sanitize_diagnosis_patch(payload: dict[str, Any]) -> dict[str, Any]:
    """Validate and sanitize a diagnosis patch from LLM output.

    Args:
        payload: Raw diagnosis patch dictionary.

    Returns:
        Sanitized diagnosis patch with safe defaults.
    """
    if not isinstance(payload, dict):
        return {}

    result: dict[str, Any] = {}

    # Summary
    summary = str(payload.get("summary") or "").strip()
    if summary:
        result["summary"] = summary[:1000]  # Cap length

    # Root cause
    root_cause = payload.get("root_cause")
    if isinstance(root_cause, dict):
        rc_summary = str(root_cause.get("summary") or "").strip()
        rc_statement = str(root_cause.get("statement") or "").strip()
        try:
            rc_confidence = max(0.0, min(1.0, float(root_cause.get("confidence") or 0.5)))
        except (TypeError, ValueError):
            rc_confidence = 0.5

        result["root_cause"] = {
            "summary": rc_summary[:500] if rc_summary else "",
            "statement": rc_statement[:200] if rc_statement else "",
            "confidence": rc_confidence,
        }

    # Recommendations
    recommendations = payload.get("recommendations")
    if isinstance(recommendations, list):
        result["recommendations"] = [
            {
                "type": str(r.get("type") or "readonly_check").strip(),
                "action": str(r.get("action") or "").strip()[:200],
                "risk": str(r.get("risk") or "low").strip(),
            }
            for r in recommendations
            if isinstance(r, dict) and r.get("action")
        ][:10]  # Cap count

    # Unknowns
    unknowns = payload.get("unknowns")
    if isinstance(unknowns, list):
        result["unknowns"] = [
            str(u).strip()[:200]
            for u in unknowns
            if str(u).strip()
        ][:10]

    # Next steps
    next_steps = payload.get("next_steps")
    if isinstance(next_steps, list):
        result["next_steps"] = [
            str(s).strip()[:200]
            for s in next_steps
            if str(s).strip()
        ][:10]

    return result


def run_platform_special_agent(
    state: "GraphState",
    cfg: "OrchestratorConfig",
    runtime: "OrchestratorRuntime",
) -> "GraphState":
    """Platform Special Agent: Synthesize domain findings into diagnosis.

    This agent takes merged findings from all domain agents and
    produces a cohesive diagnosis patch using LLM reasoning.

    Args:
        state: Current graph state.
        cfg: Orchestrator configuration.
        runtime: Orchestrator runtime instance.

    Returns:
        Updated graph state with platform_special_patch populated.
    """
    started_ms = int(time.time() * 1000)

    # Check if platform special agent is enabled
    if not _is_platform_special_agent_enabled():
        # Fallback: use deterministic diagnosis
        report_node_action(
            state,
            runtime,
            node_name="run_platform_special_agent",
            tool_name="agent.platform_special",
            request_json={},
            response_json={
                "status": "skipped",
                "reason": "platform_special_agent_disabled",
            },
            started_ms=started_ms,
            status="ok",
            count_in_state=False,
        )
        return state

    # Get LLM
    llm = _get_llm(runtime)
    if llm is None:
        state.add_degrade_reason("llm_not_configured_for_platform_special")
        report_node_action(
            state,
            runtime,
            node_name="run_platform_special_agent",
            tool_name="agent.platform_special",
            request_json={},
            response_json={
                "status": "error",
                "reason": "llm_not_configured",
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

    # Build request
    system_prompt = _build_platform_special_system_prompt()
    user_prompt = _build_platform_special_user_prompt(state)

    request = AgentRequest(
        system_prompt=system_prompt,
        user_prompt=user_prompt,
        metadata={
            "node": "run_platform_special_agent",
            "trace_event": TRACE_EVENT_PLATFORM_SPECIAL_SUMMARIZE,
        },
    )

    # Prepare through middleware (skills_only mode - no tools)
    middleware_config: dict[str, Any] = {
        "mode": "skills_only",
        "domain": "platform_special",
    }

    if middleware_enabled and middleware_chain is not None and agent_context is not None:
        prepared = middleware_chain.prepare(
            state=state,
            context=agent_context,
            request=request,
            config=middleware_config,
        )
    else:
        prepared = request

    agent_error: str | None = None
    diagnosis_patch: dict[str, Any] = {}

    try:
        # Invoke LLM (no tools for this agent - pure reasoning)
        messages = _build_messages(prepared.system_prompt, prepared.user_prompt)
        response = llm.invoke(messages)

        # Extract diagnosis patch from response
        content = getattr(response, "content", "") or ""
        diagnosis_patch = _parse_diagnosis_patch(content)

    except Exception as exc:  # noqa: BLE001
        agent_error = str(exc)[:128]
        state.add_degrade_reason(f"platform_special_agent_error:{str(exc)[:64]}")

    # Sanitize and store the patch
    if diagnosis_patch:
        state.platform_special_patch = sanitize_diagnosis_patch(diagnosis_patch)

    report_node_action(
        state,
        runtime,
        node_name="run_platform_special_agent",
        tool_name="agent.platform_special",
        request_json={
            "merged_findings_summary": {
                "domain_count": state.merged_findings.get("domain_count", 0) if state.merged_findings else 0,
                "domains": state.merged_findings.get("domains", []) if state.merged_findings else [],
            },
        },
        response_json={
            "platform_special_patch": state.platform_special_patch,
        },
        started_ms=started_ms,
        status="error" if agent_error else "ok",
        error=agent_error,
        count_in_state=False,
    )

    return state