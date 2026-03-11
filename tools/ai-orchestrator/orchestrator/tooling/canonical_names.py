"""Canonical tool name normalization.

This module provides the canonical tool naming convention for the RCA platform.
All tools should use dotted canonical names (e.g., 'incident.get', 'logs.query')
rather than underscore names (e.g., 'get_incident', 'query_logs').

The canonical naming convention follows the pattern: <domain>.<action>

A-class tools (fc_selectable): Use dotted canonical names
B-class tools (runtime_owned): May use underscore names for legacy compatibility

Migration note (2026-03-19):
- Dotted names are now the canonical form
- Underscore names are preserved as aliases for backward compatibility
- All new tools should use dotted names
"""

from __future__ import annotations

# Mapping from underscore names to dotted canonical names
# This is the single source of truth for name normalization
UNDERSCORE_TO_CANONICAL: dict[str, str] = {
    # Platform tools (incident, evidence, job, tool_call)
    "get_incident": "incident.get",
    "list_incidents": "incident.list",
    "get_evidence": "evidence.get",
    "search_evidence": "evidence.search",
    "get_ai_job": "job.get",
    "list_ai_jobs": "job.list",
    "list_tool_calls": "tool_call.list",
    # Observability tools (logs, metrics, traces)
    "query_logs": "logs.query",
    "query_metrics": "metrics.query",
    "query_range": "metrics.query_range",
    "query_traces": "traces.query",
    # Session tools
    "patch_session_context": "session.patch",
    # Knowledge base tools
    "save_knowledge_base_entry": "knowledge_base.save",
    # Evidence publishing (B-class, but may appear with underscore)
    "publish_evidence": "evidence.publish",
    # Common aliases
    "get_inc": "incident.get",
    "list_inc": "incident.list",
}

# Reverse mapping: canonical to primary alias (for display purposes)
CANONICAL_TO_ALIAS: dict[str, str] = {
    "incident.get": "get_incident",
    "incident.list": "list_incidents",
    "evidence.get": "get_evidence",
    "evidence.search": "search_evidence",
    "evidence.publish": "publish_evidence",
    "job.get": "get_ai_job",
    "job.list": "list_ai_jobs",
    "tool_call.list": "list_tool_calls",
    "logs.query": "query_logs",
    "metrics.query": "query_metrics",
    "metrics.query_range": "query_range",
    "traces.query": "query_traces",
    "session.patch": "patch_session_context",
    "knowledge_base.save": "save_knowledge_base_entry",
}


def normalize_tool_name(raw: str | None) -> str:
    """Normalize a tool name to its canonical dotted form.

    This function:
    1. Strips the 'mcp.' prefix if present
    2. Converts underscore names to dotted canonical names
    3. Returns lowercase, trimmed name

    Args:
        raw: The raw tool name (may include 'mcp.' prefix or underscore form)

    Returns:
        The canonical dotted tool name (lowercase, no 'mcp.' prefix)

    Examples:
        >>> normalize_tool_name("get_incident")
        'incident.get'
        >>> normalize_tool_name("mcp.incident.get")
        'incident.get'
        >>> normalize_tool_name("incident.get")
        'incident.get'
        >>> normalize_tool_name("logs.query")
        'logs.query'
    """
    value = str(raw or "").strip().lower()

    # Strip 'mcp.' prefix if present
    if value.startswith("mcp."):
        value = value[4:]

    # Check for underscore-to-canonical mapping
    if value in UNDERSCORE_TO_CANONICAL:
        return UNDERSCORE_TO_CANONICAL[value]

    return value


def get_alias_for_canonical(canonical: str) -> str | None:
    """Get the primary underscore alias for a canonical name.

    Args:
        canonical: The canonical dotted tool name

    Returns:
        The primary underscore alias, or None if no alias exists

    Examples:
        >>> get_alias_for_canonical("incident.get")
        'get_incident'
        >>> get_alias_for_canonical("unknown.tool")
        None
    """
    return CANONICAL_TO_ALIAS.get(canonical)


def is_dotted_name(name: str) -> bool:
    """Check if a tool name is in dotted canonical form.

    Args:
        name: The tool name to check

    Returns:
        True if the name contains a dot (canonical form)

    Examples:
        >>> is_dotted_name("incident.get")
        True
        >>> is_dotted_name("get_incident")
        False
    """
    return "." in str(name or "")


def get_all_aliases_for_canonical(canonical: str) -> list[str]:
    """Get all known aliases for a canonical name.

    Args:
        canonical: The canonical dotted tool name

    Returns:
        List of all aliases (including primary alias)

    Examples:
        >>> get_all_aliases_for_canonical("incident.get")
        ['get_incident', 'get_inc']
    """
    aliases = []
    for alias, canon in UNDERSCORE_TO_CANONICAL.items():
        if canon == canonical:
            aliases.append(alias)
    return aliases