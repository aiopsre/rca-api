"""Node prompt context builder for hybrid multi-agent system.

This module provides unified entry points for building node-specific prompt
contexts from GraphState. Each node can request only the information it needs,
avoiding raw payload leakage to nodes that shouldn't see it.

Design principle:
- Raw facts stay in incident_record / alert_event_record
- incident_context is a stable, schema-agnostic summary
- Node prompt contexts are assembled on demand, not stored in state
"""
from __future__ import annotations

from typing import TYPE_CHECKING, Any

if TYPE_CHECKING:
    from ..state import GraphState


def build_router_prompt_context(state: "GraphState") -> dict[str, Any]:
    """Build prompt context for router node.

    Uses only incident_context, no raw payload.
    The router should make domain routing decisions based on stable
    summary fields, not raw alert payload content.

    Args:
        state: Current graph state.

    Returns:
        Context dictionary with incident_context only.
    """
    return {
        "incident_context": state.incident_context or {},
    }


def build_observability_prompt_context(state: "GraphState") -> dict[str, Any]:
    """Build prompt context for observability node.

    Includes incident_context + raw alert payload.
    This node is the appropriate place to expose request paths, trace
    identifiers, timing details, and other observability-specific data
    from the raw event payload.

    Args:
        state: Current graph state.

    Returns:
        Context dictionary with incident_context, alert_event_record,
        and raw_alert_excerpt if available.
    """
    from .helpers import render_alert_event_excerpt

    context: dict[str, Any] = {
        "incident_context": state.incident_context or {},
        "alert_event_record": state.alert_event_record or {},
    }

    # Include raw payload excerpt if available
    raw_excerpt = render_alert_event_excerpt(state.alert_event_record, max_len=4096)
    if raw_excerpt:
        context["raw_alert_excerpt"] = raw_excerpt

    return context


def build_change_prompt_context(state: "GraphState") -> dict[str, Any]:
    """Build prompt context for change node.

    Uses incident_context + time fields, no raw payload.
    The change agent should investigate deployment/config changes
    based on temporal correlation, not raw alert payload structure.

    Args:
        state: Current graph state.

    Returns:
        Context dictionary with incident_context and time_context.
    """
    incident_context = state.incident_context or {}

    return {
        "incident_context": incident_context,
        "time_context": {
            "start_at": incident_context.get("start_at"),
            "end_at": incident_context.get("end_at"),
            "triggered_at": incident_context.get("triggered_at"),
            "last_seen_at": incident_context.get("last_seen_at"),
        },
    }


def build_knowledge_prompt_context(state: "GraphState") -> dict[str, Any]:
    """Build prompt context for knowledge node.

    Uses incident_context + alert name/fingerprint/root cause hints.
    The knowledge agent searches historical incidents and runbooks
    using semantic identifiers, not raw payload content.

    Args:
        state: Current graph state.

    Returns:
        Context dictionary with incident_context and search_hints.
    """
    incident_context = state.incident_context or {}

    return {
        "incident_context": incident_context,
        "search_hints": {
            "alert_name": incident_context.get("alert_name"),
            "fingerprint": incident_context.get("fingerprint"),
            "root_cause_summary": incident_context.get("root_cause_summary"),
            "root_cause_type": incident_context.get("root_cause_type"),
        },
    }


def build_platform_special_prompt_context(state: "GraphState") -> dict[str, Any]:
    """Build prompt context for platform special node.

    Uses incident_context + merged findings + quality gate.
    This agent synthesizes findings from all domain agents into a
    cohesive diagnosis. Raw alert payload is optional and should
    only be included if it materially helps synthesis.

    Args:
        state: Current graph state.

    Returns:
        Context dictionary with incident_context, merged_findings,
        quality_gate, and optionally raw_alert_excerpt.
    """
    from .helpers import render_alert_event_excerpt
    from .quality_gate import ensure_quality_gate

    incident_context = state.incident_context or {}
    merged_findings = state.merged_findings or {}
    quality_gate = ensure_quality_gate(state)

    context: dict[str, Any] = {
        "incident_context": incident_context,
        "merged_findings": merged_findings,
        "quality_gate": quality_gate,
        "evidence_count": len(state.evidence_ids or []),
    }

    # Optionally include raw payload for synthesis
    raw_excerpt = render_alert_event_excerpt(state.alert_event_record, max_len=1200)
    if raw_excerpt:
        context["raw_alert_excerpt"] = raw_excerpt

    return context