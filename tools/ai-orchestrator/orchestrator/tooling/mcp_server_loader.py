"""MCP Server reference parsing and provider building.

This module handles parsing resolved tool providers from claim response JSON and
building ProviderConfig objects for the ToolInvoker.
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from typing import Any

from .canonical_names import normalize_tool_name
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
    # New fields for A/B class tools
    tool_class: str = "fc_selectable"
    aliases: tuple[str, ...] = ()
    # Per-surface visibility flags
    allowed_for_prompt_skill: bool = True
    allowed_for_graph_agent: bool = True


@dataclass(frozen=True)
class ResolvedToolProvider:
    """Structured resolved provider from platform claim.

    This is the Python equivalent of the proto ResolvedToolProvider.
    Contains fully resolved provider information with tool metadata.
    """

    provider_id: str
    mcp_server_id: str
    name: str
    provider_type: str
    server_kind: str  # builtin | external
    base_url: str
    allowed_tools: tuple[str, ...]
    tool_metadata: tuple[ToolMetadataRef, ...]
    priority: int
    scopes: str = ""
    timeout_sec: float = 10.0


def parse_resolved_tool_providers(
    providers: list[dict[str, Any]],
) -> list[ResolvedToolProvider]:
    """Parse ResolvedToolProvider list from proto response.

    This handles the new structured resolvedToolProviders field from
    StartAIJobResponse, which provides fully resolved provider information
    with tool metadata.

    Args:
        providers: List of ResolvedToolProvider dicts from proto response.

    Returns:
        List of ResolvedToolProvider objects. Empty list if input is empty.

    Example:
        >>> providers = parse_resolved_tool_providers([
        ...     {"providerID": "prometheus", "name": "Prometheus", ...}
        ... ])
    """
    if not isinstance(providers, list):
        return []

    result: list[ResolvedToolProvider] = []
    for item in providers:
        if not isinstance(item, dict):
            continue
        provider = _parse_resolved_tool_provider(item)
        if provider is not None:
            result.append(provider)

    return result


def _parse_resolved_tool_provider(payload: dict[str, Any]) -> ResolvedToolProvider | None:
    """Parse a single ResolvedToolProvider from a dict."""
    provider_id = str(payload.get("provider_id") or payload.get("providerID") or "").strip()
    mcp_server_id = str(payload.get("mcp_server_id") or payload.get("mcpServerID") or "").strip()
    name = str(payload.get("name") or "").strip()
    base_url = str(payload.get("base_url") or payload.get("baseURL") or "").strip()

    provider_type = str(payload.get("provider_type") or payload.get("providerType") or "mcp_http").strip().lower()
    server_kind = str(payload.get("server_kind") or payload.get("serverKind") or "external").strip().lower()

    # Builtin providers may have empty base_url; external providers require it
    if not name:
        return None
    if server_kind != "builtin" and not base_url:
        return None

    allowed_tools_raw = payload.get("allowed_tools") or payload.get("allowedTools") or []
    allowed_tools = _normalize_allowed_tools(allowed_tools_raw)
    if not allowed_tools:
        return None

    provider_type = str(payload.get("provider_type") or payload.get("providerType") or "mcp_http").strip().lower()
    server_kind = str(payload.get("server_kind") or payload.get("serverKind") or "external").strip().lower()
    priority = int(payload.get("priority") or 0)
    scopes = str(payload.get("scopes") or "").strip()
    timeout_sec = _coerce_timeout(payload.get("timeout_sec") or payload.get("timeoutSec"), default=10.0)

    # Parse tool metadata
    tool_metadata_raw = payload.get("tool_metadata") or payload.get("toolMetadata") or []
    tool_metadata = _parse_tool_metadata_list(tool_metadata_raw)

    return ResolvedToolProvider(
        provider_id=provider_id,
        mcp_server_id=mcp_server_id,
        name=name,
        provider_type=provider_type,
        server_kind=server_kind,
        base_url=base_url,
        allowed_tools=allowed_tools,
        tool_metadata=tool_metadata,
        priority=priority,
        scopes=scopes,
        timeout_sec=timeout_sec,
    )


def build_provider_configs_from_resolved_providers(
    providers: list[ResolvedToolProvider],
) -> list[ProviderConfig]:
    """Convert ResolvedToolProvider list to ProviderConfig list for ToolInvoker.

    Args:
        providers: List of ResolvedToolProvider objects.

    Returns:
        List of ProviderConfig objects.
    """
    configs: list[ProviderConfig] = []
    for provider in providers:
        config = ProviderConfig(
            provider_type=provider.provider_type,
            name=provider.name,
            base_url=provider.base_url,
            allow_tools=provider.allowed_tools,
            timeout_s=provider.timeout_sec,
            scopes=provider.scopes,
        )
        configs.append(config)
    return configs


def build_toolset_from_resolved_providers(
    providers: list[ResolvedToolProvider],
    *,
    toolset_id: str = "resolved_providers",
) -> tuple[list[ProviderConfig], frozenset[str]]:
    """Build provider configs and allowed tools from ResolvedToolProviders.

    This is the canonical path for the new claim provider snapshot.

    Args:
        providers: List of ResolvedToolProvider objects.
        toolset_id: Toolset ID to use for logging/debugging.

    Returns:
        Tuple of (provider_configs, all_allowed_tools).
    """
    configs = build_provider_configs_from_resolved_providers(providers)
    all_tools: set[str] = set()
    for provider in providers:
        for tool in provider.allowed_tools:
            all_tools.add(tool)
    return configs, frozenset(all_tools)


def _normalize_allowed_tools(raw: Any) -> tuple[str, ...]:
    """Normalize and dedupe allowed_tools list.

    Uses canonical dotted naming (e.g., 'incident.get' instead of 'get_incident').
    """
    if not isinstance(raw, list):
        return ()

    out: list[str] = []
    seen: set[str] = set()
    for item in raw:
        # Use the canonical normalization function
        normalized = normalize_tool_name(str(item or ""))
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


def _parse_bool_with_default(
    item: dict[str, Any],
    snake_key: str,
    camel_key: str,
    *,
    default: bool,
) -> bool:
    """Parse a boolean value with proper handling of explicit false values.

    This function correctly handles the case where a boolean field is explicitly
    set to false. Unlike `item.get(key) or default`, which would treat False as
    "missing" and return the default, this function preserves explicit false values.

    Args:
        item: The dictionary containing the value.
        snake_key: The snake_case key name.
        camel_key: The camelCase key name.
        default: The default value if neither key is present.

    Returns:
        The boolean value, preserving explicit false.
    """
    # Check snake_case key first
    if snake_key in item:
        val = item[snake_key]
        if val is not None:
            return bool(val)
    # Fall back to camelCase key
    if camel_key in item:
        val = item[camel_key]
        if val is not None:
            return bool(val)
    # Neither key present, use default
    return default


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

        raw_tool_name = str(item.get("tool_name") or item.get("toolName") or "").strip()
        if not raw_tool_name:
            continue

        # Normalize tool name to canonical dotted form
        tool_name = normalize_tool_name(raw_tool_name)

        # Parse tags
        tags_raw = item.get("tags") or []
        if isinstance(tags_raw, list):
            tags = tuple(str(t) for t in tags_raw if t)
        elif isinstance(tags_raw, tuple):
            tags = tags_raw
        else:
            tags = ()

        # Parse aliases (new field for A/B class tools)
        aliases_raw = item.get("aliases") or []
        if isinstance(aliases_raw, list):
            aliases = tuple(str(a) for a in aliases_raw if a)
        elif isinstance(aliases_raw, tuple):
            aliases = aliases_raw
        else:
            aliases = ()

        result.append(ToolMetadataRef(
            tool_name=tool_name,
            kind=str(item.get("kind") or "unknown"),
            domain=str(item.get("domain") or "general"),
            read_only=bool(item.get("read_only") or item.get("readOnly", True)),
            risk_level=str(item.get("risk_level") or item.get("riskLevel") or "low"),
            latency_tier=str(item.get("latency_tier") or "fast"),
            cost_hint=str(item.get("cost_hint") or "free"),
            tags=tags,
            description=str(item.get("description") or ""),
            tool_class=str(item.get("tool_class") or item.get("toolClass") or "fc_selectable"),
            aliases=aliases,
            # Per-surface visibility flags
            # Note: Must check for None explicitly to preserve explicit false values
            # Using "or" pattern loses false because False or X evaluates to X
            allowed_for_prompt_skill=_parse_bool_with_default(
                item, "allowed_for_prompt_skill", "allowedForPromptSkill", default=True
            ),
            allowed_for_graph_agent=_parse_bool_with_default(
                item, "allowed_for_graph_agent", "allowedForGraphAgent", default=True
            ),
        ))

    return tuple(result)