"""Runtime tool catalog types for Function Calling migration.

This module defines the core contracts for tool discovery and execution
in the orchestrator runtime. These types provide a unified interface
for tools regardless of their origin (MCP server, platform toolset, etc.).

Key principles:
- ToolSpec is the single source of truth for tool metadata
- ToolCatalogSnapshot is immutable and job-scoped
- RuntimeToolGateway is the only way to execute tools
- ExecutedToolCall is the unified result model for all tool executions

All tool names use canonical dotted naming (e.g., 'incident.get', 'logs.query').
"""
from __future__ import annotations

from dataclasses import dataclass, field
from typing import TYPE_CHECKING, Any, Protocol

if TYPE_CHECKING:
    from .runtime import OrchestratorRuntime


def _normalize_to_canonical(name: str | None) -> str:
    """Normalize a tool name to canonical dotted form.

    This function handles:
    1. Stripping 'mcp.' prefix
    2. Converting underscore names to dotted canonical names

    Args:
        name: The raw tool name

    Returns:
        The canonical dotted tool name
    """
    value = str(name or "").strip().lower()
    if value.startswith("mcp."):
        value = value[4:]
    # Import here to avoid circular dependency
    from ..tooling.canonical_names import normalize_tool_name

    return normalize_tool_name(value)


@dataclass(frozen=True)
class ToolSpec:
    """Specification of a tool available for execution.

    This is the canonical representation of a tool's metadata and schema.
    All tool names use canonical form (no 'mcp.' prefix).

    Attributes:
        name: Canonical tool name (e.g., "prometheus_query", "loki_search").
        description: Human-readable description of what the tool does.
        input_schema: JSON Schema for the tool's input parameters.
        output_schema: JSON Schema for the tool's output (optional).
        kind: Primary classification (metrics, logs, traces, incidents, unknown).
        tags: Additional classification tags for discovery.
        provider_id: Identifier for the provider that exposes this tool.
        read_only: Whether the tool only reads data (no side effects).
        risk_level: Risk level for execution (low, medium, high).
        tool_class: A/B class (fc_selectable | runtime_owned).
        allowed_for_prompt_skill: Whether this tool can be used by prompt-first skills.
        allowed_for_graph_agent: Whether this tool can be used by LangGraph FC agent.
    """
    name: str
    description: str = ""
    input_schema: dict[str, Any] = field(default_factory=dict)
    output_schema: dict[str, Any] = field(default_factory=dict)
    kind: str = "unknown"
    tags: tuple[str, ...] = ()
    provider_id: str = ""
    read_only: bool = True
    risk_level: str = "low"
    tool_class: str = "fc_selectable"
    allowed_for_prompt_skill: bool = True
    allowed_for_graph_agent: bool = True

    def __post_init__(self) -> None:
        # Ensure name is canonical (no 'mcp.' prefix, dotted form)
        normalized = _normalize_to_canonical(self.name)
        if normalized != self.name:
            object.__setattr__(self, "name", normalized)

    def to_openai_tool(self) -> dict[str, Any]:
        """Convert to OpenAI function calling format.

        Returns:
            Dictionary in OpenAI tools format.
        """
        return {
            "type": "function",
            "function": {
                "name": self.name,
                "description": self.description,
                "parameters": self.input_schema,
            },
        }


@dataclass(frozen=True)
class ToolCatalogSnapshot:
    """Immutable snapshot of available tools for a job.

    Built once during job initialization and used throughout the job's lifetime.
    Provides O(1) lookup by tool name and iteration over all tools.

    Attributes:
        toolset_ids: Tuple of toolset IDs that contributed to this snapshot.
        tools: Tuple of all available ToolSpec instances.
        by_name: Dictionary mapping canonical tool name to ToolSpec.
    """
    toolset_ids: tuple[str, ...]
    tools: tuple[ToolSpec, ...]
    by_name: dict[str, ToolSpec]

    def has_tool(self, name: str) -> bool:
        """Check if a tool is available in the catalog.

        Args:
            name: Tool name (canonical dotted form or underscore alias).

        Returns:
            True if the tool exists in the catalog.
        """
        canonical = _normalize_to_canonical(name)
        return canonical in self.by_name

    def get_tool(self, name: str) -> ToolSpec | None:
        """Get a tool by its name.

        Args:
            name: Tool name (canonical dotted form or underscore alias).

        Returns:
            ToolSpec if found, None otherwise.
        """
        canonical = _normalize_to_canonical(name)
        return self.by_name.get(canonical)

    def tool_names(self) -> list[str]:
        """Get all available tool names.

        Returns:
            Sorted list of canonical tool names.
        """
        return sorted(self.by_name.keys())

    def filter_by_kind(self, kind: str) -> list[ToolSpec]:
        """Get all tools of a specific kind.

        Args:
            kind: The kind to filter by (e.g., "metrics", "logs").

        Returns:
            List of matching ToolSpec instances.
        """
        return [t for t in self.tools if t.kind == kind]

    def filter_by_tag(self, tag: str) -> list[ToolSpec]:
        """Get all tools with a specific tag.

        Args:
            tag: The tag to filter by.

        Returns:
            List of matching ToolSpec instances.
        """
        return [t for t in self.tools if tag in t.tags]

    def filter_by_tool_class(self, tool_class: str) -> list[ToolSpec]:
        """Get all tools of a specific tool class.

        Args:
            tool_class: The tool class to filter by (e.g., "fc_selectable", "runtime_owned").

        Returns:
            List of matching ToolSpec instances.
        """
        return [t for t in self.tools if t.tool_class == tool_class]

    def fc_tool_surface(self) -> "ToolCatalogSnapshot":
        """Get a snapshot containing only A-class (fc_selectable) tools.

        A-class tools are those that can be directly exposed to LLM function calling.
        B-class (runtime_owned) tools are excluded from FC surface.

        Returns:
            New ToolCatalogSnapshot with only fc_selectable tools.
        """
        fc_tools = [t for t in self.tools if t.tool_class == "fc_selectable"]
        fc_by_name = {t.name: t for t in fc_tools}
        return ToolCatalogSnapshot(
            toolset_ids=self.toolset_ids,
            tools=tuple(fc_tools),
            by_name=fc_by_name,
        )

    def to_openai_tools(self) -> list[dict[str, Any]]:
        """Convert all tools to OpenAI function calling format.

        Returns:
            List of dictionaries in OpenAI tools format.
        """
        return [t.to_openai_tool() for t in self.tools]


class RuntimeToolGateway(Protocol):
    """Protocol for tool execution gateway.

    This is the single entry point for all tool execution in the orchestrator.
    Both graph nodes and skills must use this interface to execute tools.

    Methods:
        list_tools: Get all available tools as ToolSpec instances.
        execute: Execute a tool and return the result.
    """

    def list_tools(self) -> list[ToolSpec]:
        """List all available tools.

        Returns:
            List of ToolSpec instances for all available tools.
        """
        ...

    def execute(
        self,
        tool_name: str,
        args: dict[str, Any],
        *,
        source: str,
    ) -> dict[str, Any]:
        """Execute a tool with the given arguments.

        Args:
            tool_name: Canonical name of the tool to execute.
            args: Input arguments for the tool.
            source: Identifier for the caller (e.g., "skill.plan", "graph.node").

        Returns:
            Tool execution result as a dictionary.

        Raises:
            ToolNotFoundError: If the tool is not in the catalog.
            ToolExecutionError: If the tool execution fails.
            ToolNotAllowedError: If the tool is not allowed for the source.
        """
        ...


@dataclass(frozen=True)
class ExecutedToolCall:
    """Immutable record of a tool execution.

    This is the unified result model for all tool executions, used by:
    - Graph state for tracking tool calls
    - After-tools input for skills
    - Audit trace for observability

    All tool names use canonical form (no 'mcp.' prefix).

    Attributes:
        tool_name: Canonical name of the executed tool.
        request_json: The input arguments sent to the tool.
        response_json: The output returned by the tool.
        latency_ms: Execution time in milliseconds.
        source: Identifier for the caller (e.g., "skill.plan", "graph.node").
        status: Execution status ("ok" or "error").
        error: Error message if status is "error".
        provider_id: Identifier for the provider that executed the tool.
        provider_type: Type of the provider (e.g., "mcp_http", "mcp_api").
        resolved_from_toolset_id: Toolset ID from which the tool was resolved.
        round_idx: Round index for FC agent iterations (optional).
        group_idx: Group index for parallel execution (optional).
        item_idx: Item index within a group (optional).
    """
    tool_name: str
    request_json: dict[str, Any]
    response_json: dict[str, Any]
    latency_ms: int
    source: str
    status: str = "ok"
    error: str = ""
    provider_id: str = ""
    provider_type: str = ""
    resolved_from_toolset_id: str = ""
    round_idx: int = -1  # -1 indicates not set
    group_idx: int = -1
    item_idx: int = -1

    def __post_init__(self) -> None:
        # Ensure tool_name is canonical (no 'mcp.' prefix, dotted form)
        normalized = _normalize_to_canonical(self.tool_name)
        if normalized != self.tool_name:
            object.__setattr__(self, "tool_name", normalized)

    def to_skill_tool_result(self) -> dict[str, Any]:
        """Convert to skill tool result format.

        Returns:
            Dictionary suitable for after-tools consumption.
        """
        result = {
            "tool": self.tool_name,
            "tool_name": self.tool_name,
            "tool_request": self.request_json,
            "tool_result": self.response_json,
            "latency_ms": self.latency_ms,
            "status": self.status,
            "provider_id": self.provider_id,
            "provider_type": self.provider_type,
            "resolved_from_toolset_id": self.resolved_from_toolset_id,
            "source": self.source,
        }
        if self.error:
            result["error"] = self.error
        return result

    def to_audit_record(self) -> dict[str, Any]:
        """Convert to audit record format.

        Returns:
            Dictionary suitable for audit logging.
        """
        record = {
            "tool_name": self.tool_name,
            "source": self.source,
            "latency_ms": self.latency_ms,
            "status": self.status,
            "provider_id": self.provider_id,
            "provider_type": self.provider_type,
            "resolved_from_toolset_id": self.resolved_from_toolset_id,
            "request_summary": _summarize_request(self.tool_name, self.request_json),
            "response_summary": _summarize_response(self.response_json),
        }
        if self.error:
            record["error"] = self.error
        return record


def _summarize_request(tool_name: str, request: dict[str, Any]) -> dict[str, Any]:
    """Create a summary of a tool request for audit logging.

    Args:
        tool_name: The tool name.
        request: The request payload.

    Returns:
        Summary dictionary.
    """
    if not isinstance(request, dict):
        return {"type": "invalid"}

    summary: dict[str, Any] = {
        "keys": sorted(str(k) for k in request.keys())[:8],
    }

    # Include datasource_id if present
    if "datasource_id" in request:
        summary["datasource_id"] = str(request["datasource_id"])[:64]

    return summary


def _summarize_response(response: dict[str, Any]) -> dict[str, Any]:
    """Create a summary of a tool response for audit logging.

    Args:
        response: The response payload.

    Returns:
        Summary dictionary.
    """
    if not isinstance(response, dict):
        return {"type": "invalid"}

    summary: dict[str, Any] = {
        "keys": sorted(str(k) for k in response.keys())[:8],
    }

    # Include size information if present
    if "resultSizeBytes" in response:
        summary["result_size_bytes"] = response["resultSizeBytes"]
    if "rowCount" in response:
        summary["row_count"] = response["rowCount"]

    # Check for errors
    if response.get("error"):
        summary["has_error"] = True

    return summary


def build_tool_catalog_snapshot(
    *,
    toolset_ids: list[str],
    tool_specs: list[ToolSpec],
) -> ToolCatalogSnapshot:
    """Build a ToolCatalogSnapshot from tool specs.

    Args:
        toolset_ids: List of toolset IDs that contributed the tools.
        tool_specs: List of ToolSpec instances.

    Returns:
        Immutable ToolCatalogSnapshot.
    """
    # Deduplicate by canonical name, keeping the first occurrence
    seen: set[str] = set()
    unique_specs: list[ToolSpec] = []
    for spec in tool_specs:
        canonical = _normalize_to_canonical(spec.name)
        if canonical not in seen:
            seen.add(canonical)
            unique_specs.append(spec)

    by_name = {spec.name: spec for spec in unique_specs}

    return ToolCatalogSnapshot(
        toolset_ids=tuple(toolset_ids),
        tools=tuple(unique_specs),
        by_name=by_name,
    )


def tool_descriptor_to_spec(
    descriptor: Any,
    *,
    provider_id: str = "",
    kind: str = "unknown",
    read_only: bool = True,
    risk_level: str = "low",
) -> ToolSpec:
    """Convert a ToolDescriptor to ToolSpec.

    Args:
        descriptor: ToolDescriptor instance from tool_discovery.py.
        provider_id: Provider identifier.
        kind: Tool kind classification.
        read_only: Whether the tool is read-only.
        risk_level: Risk level for the tool.

    Returns:
        ToolSpec instance.
    """
    # Import here to avoid circular dependency
    from .tool_discovery import ToolDescriptor

    if not isinstance(descriptor, ToolDescriptor):
        raise TypeError(f"Expected ToolDescriptor, got {type(descriptor).__name__}")

    return ToolSpec(
        name=descriptor.tool_name,
        description=descriptor.description,
        input_schema=descriptor.input_schema,
        output_schema=descriptor.output_schema,
        kind=kind,
        tags=descriptor.tags,
        provider_id=provider_id or descriptor.provider_id,
        read_only=read_only,
        risk_level=risk_level,
    )


def tool_metadata_to_spec(
    metadata: Any,
    *,
    input_schema: dict[str, Any] | None = None,
    output_schema: dict[str, Any] | None = None,
    provider_id: str = "",
) -> ToolSpec:
    """Convert ToolMetadata to ToolSpec.

    Args:
        metadata: ToolMetadata instance from tool_registry.py.
        input_schema: Optional input schema override.
        output_schema: Optional output schema override.
        provider_id: Provider identifier.

    Returns:
        ToolSpec instance.
    """
    # Import here to avoid circular dependency
    from .tool_registry import ToolMetadata

    if not isinstance(metadata, ToolMetadata):
        raise TypeError(f"Expected ToolMetadata, got {type(metadata).__name__}")

    return ToolSpec(
        name=metadata.tool_name,
        description=metadata.description,
        input_schema=input_schema or {},
        output_schema=output_schema or {},
        kind=metadata.kind,
        tags=metadata.tags,
        provider_id=provider_id,
        read_only=metadata.read_only,
        risk_level=metadata.risk_level,
        tool_class=metadata.tool_class,
        allowed_for_prompt_skill=True,  # Default, can be overridden
        allowed_for_graph_agent=True,  # Default, can be overridden
    )