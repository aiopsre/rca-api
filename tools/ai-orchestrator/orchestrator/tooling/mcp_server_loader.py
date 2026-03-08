"""MCP Server reference parsing and provider building.

This module handles parsing McpServerRef from claim response JSON and
building ProviderConfig objects for the ToolInvoker.
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from typing import Any

from .toolset_config import ProviderConfig


@dataclass(frozen=True)
class ToolMetadataRef:
    """Tool metadata from platform.

    This is the Python equivalent of the Go model ToolMetadataRef.
    Contains classification metadata for a tool passed from the platform.
    """

    tool_name: str
    kind: str = "unknown"
    domain: str = "general"
    read_only: bool = True
    risk_level: str = "low"
    latency_tier: str = "fast"
    cost_hint: str = "free"
    tags: tuple[str, ...] = ()
    description: str = ""


@dataclass(frozen=True)
class McpServerRef:
    """Reference to an MCP server resolved from the platform.

    This is the Python equivalent of the Go model McpServerRef.
    Contains the information needed to connect to an external MCP server.
    """

    mcp_server_id: str
    name: str
    base_url: str
    allowed_tools: tuple[str, ...]
    timeout_sec: float
    scopes: str
    auth_type: str
    tool_metadata: tuple[ToolMetadataRef, ...] = ()


def parse_mcpserver_refs(mcpserver_refs_json: str) -> list[McpServerRef]:
    """Parse McpServerRef list from JSON string.

    Args:
        mcpserver_refs_json: JSON string from claim response mcpServersJSON field.

    Returns:
        List of McpServerRef objects. Empty list if JSON is invalid or empty.

    Example:
        >>> refs = parse_mcpserver_refs('[{"name": "prometheus", "base_url": "http://..."}]')
    """
    normalized = str(mcpserver_refs_json or "").strip()
    if not normalized:
        return []

    try:
        payload = json.loads(normalized)
    except json.JSONDecodeError:
        return []

    if not isinstance(payload, list):
        return []

    refs: list[McpServerRef] = []
    for item in payload:
        if not isinstance(item, dict):
            continue
        ref = _parse_mcpserver_ref(item)
        if ref is not None:
            refs.append(ref)

    return refs


def _parse_mcpserver_ref(payload: dict[str, Any]) -> McpServerRef | None:
    """Parse a single McpServerRef from a dict."""
    mcp_server_id = str(payload.get("mcp_server_id") or payload.get("mcpServerID") or "").strip()
    name = str(payload.get("name") or "").strip()
    base_url = str(payload.get("base_url") or payload.get("baseURL") or "").strip()

    if not name or not base_url:
        return None

    allowed_tools_raw = payload.get("allowed_tools") or payload.get("allowedTools") or []
    allowed_tools = _normalize_allowed_tools(allowed_tools_raw)
    if not allowed_tools:
        return None

    timeout_sec = _coerce_timeout(payload.get("timeout_sec") or payload.get("timeoutSec"), default=10.0)
    scopes = str(payload.get("scopes") or "").strip()
    auth_type = str(payload.get("auth_type") or payload.get("authType") or "none").strip().lower() or "none"

    # Parse tool metadata from platform
    tool_metadata_raw = payload.get("tool_metadata") or payload.get("toolMetadata") or []
    tool_metadata = _parse_tool_metadata_list(tool_metadata_raw)

    return McpServerRef(
        mcp_server_id=mcp_server_id,
        name=name,
        base_url=base_url,
        allowed_tools=allowed_tools,
        timeout_sec=timeout_sec,
        scopes=scopes,
        auth_type=auth_type,
        tool_metadata=tool_metadata,
    )


def build_provider_configs_from_mcpserver_refs(refs: list[McpServerRef]) -> list[ProviderConfig]:
    """Convert McpServerRef list to ProviderConfig list for ToolInvoker.

    Args:
        refs: List of McpServerRef objects.

    Returns:
        List of ProviderConfig objects with type "mcp_http".
    """
    configs: list[ProviderConfig] = []
    for ref in refs:
        config = ProviderConfig(
            provider_type="mcp_http",
            name=ref.name,
            base_url=ref.base_url,
            allow_tools=ref.allowed_tools,
            timeout_s=ref.timeout_sec,
            scopes=ref.scopes,
        )
        configs.append(config)
    return configs


def build_toolset_from_mcpserver_refs(
    refs: list[McpServerRef],
    *,
    toolset_id: str = "mcp_servers",
) -> tuple[list[ProviderConfig], frozenset[str]]:
    """Build provider configs and allowed tools from McpServerRefs.

    This is a convenience function that combines building configs with
    computing the full set of allowed tools.

    Args:
        refs: List of McpServerRef objects.
        toolset_id: Toolset ID to use for logging/debugging.

    Returns:
        Tuple of (provider_configs, all_allowed_tools).
    """
    configs = build_provider_configs_from_mcpserver_refs(refs)
    all_tools: set[str] = set()
    for ref in refs:
        for tool in ref.allowed_tools:
            all_tools.add(tool)
    return configs, frozenset(all_tools)


def _normalize_allowed_tools(raw: Any) -> tuple[str, ...]:
    """Normalize and dedupe allowed_tools list."""
    if not isinstance(raw, list):
        return ()

    out: list[str] = []
    seen: set[str] = set()
    for item in raw:
        normalized = str(item or "").strip().lower()
        # Strip "mcp." prefix if present
        if normalized.startswith("mcp."):
            normalized = normalized[4:]
        if not normalized:
            continue
        if normalized in seen:
            continue
        seen.add(normalized)
        out.append(normalized)

    return tuple(out)


def _coerce_timeout(raw: Any, *, default: float) -> float:
    """Coerce timeout value to float."""
    if raw is None:
        return default
    if isinstance(raw, bool):
        return default
    try:
        value = float(raw)
    except (TypeError, ValueError):
        return default
    if value <= 0:
        return default
    return value


def _parse_tool_metadata_list(raw: Any) -> tuple[ToolMetadataRef, ...]:
    """Parse tool metadata from platform response.

    Args:
        raw: List of tool metadata dicts from platform.

    Returns:
        Tuple of ToolMetadataRef objects.
    """
    if not isinstance(raw, list):
        return ()

    result: list[ToolMetadataRef] = []
    for item in raw:
        if not isinstance(item, dict):
            continue

        tool_name = str(item.get("tool_name") or "").strip()
        if not tool_name:
            continue

        # Parse tags
        tags_raw = item.get("tags") or []
        if isinstance(tags_raw, list):
            tags = tuple(str(t) for t in tags_raw if t)
        elif isinstance(tags_raw, tuple):
            tags = tags_raw
        else:
            tags = ()

        result.append(ToolMetadataRef(
            tool_name=tool_name,
            kind=str(item.get("kind") or "unknown"),
            domain=str(item.get("domain") or "general"),
            read_only=bool(item.get("read_only", True)),
            risk_level=str(item.get("risk_level") or "low"),
            latency_tier=str(item.get("latency_tier") or "fast"),
            cost_hint=str(item.get("cost_hint") or "free"),
            tags=tags,
            description=str(item.get("description") or ""),
        ))

    return tuple(result)