"""Tool discovery mechanism for dynamic tool execution.

This module provides capabilities to discover available tools at runtime,
allowing the orchestrator to work with any MCP Server that provides tools
dynamically rather than hardcoded tool names.
"""
from __future__ import annotations

from dataclasses import dataclass, field
from typing import TYPE_CHECKING, Any
import fnmatch

if TYPE_CHECKING:
    from .runtime import OrchestratorRuntime


@dataclass(frozen=True)
class ToolDescriptor:
    """Describes a tool available for execution.

    Attributes:
        tool_name: The name of the tool (e.g., "prometheus_query", "loki_search").
        description: Human-readable description of what the tool does.
        input_schema: JSON Schema for the tool's input parameters.
        output_schema: JSON Schema for the tool's output.
        provider_id: Identifier for the provider that exposes this tool.
        tags: Classification tags (e.g., "metrics", "logs", "traces", "incidents").
    """
    tool_name: str
    description: str = ""
    input_schema: dict[str, Any] = field(default_factory=dict)
    output_schema: dict[str, Any] = field(default_factory=dict)
    provider_id: str = ""
    tags: tuple[str, ...] = ()


@dataclass
class ToolDiscoveryResult:
    """Result of tool discovery containing available tools.

    Provides methods to search and filter tools by various criteria.
    """
    tools: tuple[ToolDescriptor, ...]
    by_tag: dict[str, list[ToolDescriptor]] = field(default_factory=dict)

    def find_by_tag(self, tag: str) -> list[ToolDescriptor]:
        """Find tools matching a specific tag.

        Args:
            tag: The tag to search for (e.g., "metrics", "logs", "traces").

        Returns:
            List of tools with the specified tag.
        """
        return self.by_tag.get(tag, [])

    def find_by_pattern(self, pattern: str) -> list[ToolDescriptor]:
        """Find tools whose names match a glob pattern.

        Args:
            pattern: Glob pattern to match against tool names
                     (e.g., "prometheus_*", "*query*").

        Returns:
            List of tools matching the pattern.
        """
        return [t for t in self.tools if fnmatch.fnmatch(t.tool_name, pattern)]

    def find_by_name(self, name: str) -> ToolDescriptor | None:
        """Find a tool by its exact name.

        Args:
            name: The exact tool name to find.

        Returns:
            The tool descriptor if found, None otherwise.
        """
        for tool in self.tools:
            if tool.tool_name == name:
                return tool
        return None

    def tool_names(self) -> list[str]:
        """Get list of all available tool names.

        Returns:
            Sorted list of tool names.
        """
        return sorted(t.tool_name for t in self.tools)

    def has_tools_for_tag(self, tag: str) -> bool:
        """Check if any tools are available for a given tag.

        Args:
            tag: The tag to check.

        Returns:
            True if at least one tool has the tag.
        """
        return bool(self.by_tag.get(tag))


def _infer_tags_from_tool_name(tool_name: str) -> tuple[str, ...]:
    """Infer classification tags from tool name.

    Analyzes the tool name for common patterns to determine its category.

    Args:
        tool_name: The name of the tool.

    Returns:
        Tuple of inferred tags.
    """
    name_lower = tool_name.lower()
    tags: list[str] = []

    # Metrics-related patterns
    if any(k in name_lower for k in ["metric", "promql", "prometheus", "victoria"]):
        tags.append("metrics")

    # Logs-related patterns
    if any(k in name_lower for k in ["log", "loki", "elasticsearch"]):
        tags.append("logs")

    # Traces-related patterns
    if any(k in name_lower for k in ["trace", "jaeger", "span", "tempo"]):
        tags.append("traces")

    # Incident/alert-related patterns
    if any(k in name_lower for k in ["incident", "alert", "event", "alertmanager"]):
        tags.append("incidents")

    # Query-related patterns (generic)
    if "query" in name_lower:
        tags.append("query")

    # Search-related patterns
    if "search" in name_lower:
        tags.append("search")

    return tuple(tags)


def _create_tool_descriptor(
    tool_name: str,
    *,
    provider_id: str = "",
    description: str = "",
    input_schema: dict[str, Any] | None = None,
    output_schema: dict[str, Any] | None = None,
    tags: tuple[str, ...] | None = None,
) -> ToolDescriptor:
    """Create a ToolDescriptor with inferred tags.

    Args:
        tool_name: Name of the tool.
        provider_id: Provider identifier.
        description: Tool description.
        input_schema: Input parameter schema.
        output_schema: Output schema.
        tags: Optional explicit tags (will be merged with inferred tags).

    Returns:
        ToolDescriptor with inferred and explicit tags.
    """
    inferred_tags = _infer_tags_from_tool_name(tool_name)

    if tags:
        # Merge explicit tags with inferred tags, removing duplicates
        all_tags = tuple(dict.fromkeys(tags + inferred_tags))
    else:
        all_tags = inferred_tags

    return ToolDescriptor(
        tool_name=tool_name,
        description=description,
        input_schema=input_schema or {},
        output_schema=output_schema or {},
        provider_id=provider_id,
        tags=all_tags,
    )


def discover_tools(runtime: "OrchestratorRuntime") -> ToolDiscoveryResult:
    """Discover all tools available through the runtime.

    Examines the tool invoker chain to find all allowed tools and creates
    descriptors with inferred tags.

    Args:
        runtime: The orchestrator runtime instance.

    Returns:
        ToolDiscoveryResult containing all available tools.
    """
    tools: list[ToolDescriptor] = []

    tool_invoker = getattr(runtime, "_tool_invoker", None)
    if tool_invoker is None:
        return ToolDiscoveryResult(tools=tuple(tools), by_tag={})

    # Get allowed tools from invoker
    try:
        allowed = tool_invoker.allowed_tools()
    except Exception:  # noqa: BLE001
        allowed = []

    if not isinstance(allowed, list):
        allowed = []

    # Get provider summaries for additional metadata
    provider_summaries: list[dict[str, Any]] = []
    try:
        summaries = tool_invoker.provider_summaries()
        if isinstance(summaries, list):
            provider_summaries = [s for s in summaries if isinstance(s, dict)]
    except Exception:  # noqa: BLE001
        pass

    # Build a map of tool -> provider info
    tool_to_provider: dict[str, dict[str, Any]] = {}
    for summary in provider_summaries:
        provider_id = str(summary.get("provider_id") or "").strip()
        allow_tools = summary.get("allow_tools")
        if isinstance(allow_tools, list):
            for tool in allow_tools:
                tool_name = str(tool).strip()
                if tool_name:
                    tool_to_provider[tool_name] = {
                        "provider_id": provider_id,
                        "provider_type": str(summary.get("provider_type") or ""),
                    }

    # Create descriptors for each tool
    for tool_name in allowed:
        normalized_name = str(tool_name).strip()
        if not normalized_name:
            continue

        provider_info = tool_to_provider.get(normalized_name, {})
        descriptor = _create_tool_descriptor(
            tool_name=normalized_name,
            provider_id=provider_info.get("provider_id", ""),
            description=provider_info.get("description", ""),
        )
        tools.append(descriptor)

    # Build tag index
    by_tag: dict[str, list[ToolDescriptor]] = {}
    for tool in tools:
        for tag in tool.tags:
            by_tag.setdefault(tag, []).append(tool)

    return ToolDiscoveryResult(tools=tuple(tools), by_tag=by_tag)


def discover_tools_by_capability(
    runtime: "OrchestratorRuntime",
    capability: str,
) -> list[ToolDescriptor]:
    """Discover tools that can fulfill a specific capability.

    Maps capability names to tool tags for discovery.

    Args:
        runtime: The orchestrator runtime instance.
        capability: The capability to find tools for (e.g., "metrics", "logs").

    Returns:
        List of tools that can fulfill the capability.
    """
    # Map capabilities to tags
    capability_to_tags: dict[str, list[str]] = {
        "metrics": ["metrics", "query"],
        "logs": ["logs", "search"],
        "traces": ["traces"],
        "incidents": ["incidents"],
        "query": ["query", "metrics"],
        "search": ["search", "logs"],
    }

    tags = capability_to_tags.get(capability.lower(), [capability.lower()])

    discovery = discover_tools(runtime)
    results: list[ToolDescriptor] = []

    seen: set[str] = set()
    for tag in tags:
        for tool in discovery.find_by_tag(tag):
            if tool.tool_name not in seen:
                seen.add(tool.tool_name)
                results.append(tool)

    return results