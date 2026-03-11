"""Function Calling Tool Adapter for the orchestrator runtime.

This module provides a unified adapter for converting tool specifications
to OpenAI/LangChain function calling format and normalizing tool calls
returned by LLMs.

Key principles:
- All tool names use canonical dotted form (e.g., 'incident.get', 'logs.query')
- Adapter works with ToolCatalogSnapshot for tool discovery
- NormalizedToolCall provides a consistent interface for tool execution
"""
from __future__ import annotations

from dataclasses import dataclass
from typing import TYPE_CHECKING, Any

if TYPE_CHECKING:
    from .tool_catalog import ToolCatalogSnapshot


def _normalize_tool_name(name: str | None) -> str:
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
class NormalizedToolCall:
    """Normalized tool call result from LLM function calling.

    All tool names use canonical form (no 'mcp.' prefix).

    Attributes:
        tool_name: Canonical name of the tool to call.
        arguments: Input arguments for the tool.
        call_id: Optional call ID from the LLM response.
    """
    tool_name: str
    arguments: dict[str, Any]
    call_id: str = ""

    def __post_init__(self) -> None:
        # Ensure tool_name is canonical (no 'mcp.' prefix, dotted form)
        normalized = _normalize_tool_name(self.tool_name)
        if normalized != self.tool_name:
            object.__setattr__(self, "tool_name", normalized)


class FunctionCallingToolAdapter:
    """Adapter for function calling tool conversion and normalization.

    Provides unified interface for:
    - Generating OpenAI/LangChain compatible tools format
    - Normalizing LLM-returned tool calls to canonical form
    - Validating tool calls against available tools

    All tool names use canonical form (no 'mcp.' prefix).
    """

    def __init__(self, snapshot: "ToolCatalogSnapshot") -> None:
        """Initialize the adapter with a tool catalog snapshot.

        Args:
            snapshot: The ToolCatalogSnapshot containing available tools.
        """
        self._snapshot = snapshot

    @property
    def snapshot(self) -> "ToolCatalogSnapshot":
        """Get the underlying tool catalog snapshot."""
        return self._snapshot

    def to_openai_tools(self) -> list[dict[str, Any]]:
        """Generate OpenAI function calling format tools list.

        Returns:
            List of dicts in OpenAI tools format, one per available tool.
        """
        return self._snapshot.to_openai_tools()

    def normalize_tool_calls(self, tool_calls: list[Any]) -> list[NormalizedToolCall]:
        """Normalize LLM-returned tool calls to canonical form.

        Handles both dict format (from LangChain AIMessage.tool_calls)
        and object format (from LangChain ToolCall objects).

        Args:
            tool_calls: List of tool calls from LLM response.

        Returns:
            List of NormalizedToolCall instances with canonical dotted names.
        """
        normalized: list[NormalizedToolCall] = []
        for tc in tool_calls or []:
            # LangChain ToolCall format: {"name": str, "args": dict, "id": str}
            if isinstance(tc, dict):
                name = str(tc.get("name", ""))
                args = tc.get("args", {})
                call_id = str(tc.get("id", ""))
            else:
                # LangChain ToolCall object
                name = str(getattr(tc, "name", ""))
                args = getattr(tc, "args", {}) or {}
                call_id = str(getattr(tc, "id", ""))

            if not name:
                continue

            # Normalize to canonical dotted name
            canonical_name = _normalize_tool_name(name)

            normalized.append(NormalizedToolCall(
                tool_name=canonical_name,
                arguments=args if isinstance(args, dict) else {},
                call_id=call_id,
            ))
        return normalized

    def validate_tool_calls(self, calls: list[NormalizedToolCall]) -> list[NormalizedToolCall]:
        """Validate tool calls against available tools in the snapshot.

        Args:
            calls: Normalized tool calls to validate.

        Returns:
            List of valid tool calls.

        Raises:
            RuntimeError: If any tool call references an unknown tool.
        """
        valid: list[NormalizedToolCall] = []
        for call in calls:
            if self._snapshot.has_tool(call.tool_name):
                valid.append(call)
            else:
                raise RuntimeError(f"Unknown tool: {call.tool_name}")
        return valid

    def has_tool(self, tool_name: str) -> bool:
        """Check if a tool is available in the snapshot.

        Args:
            tool_name: Tool name to check (canonical or with 'mcp.' prefix).

        Returns:
            True if the tool is available.
        """
        return self._snapshot.has_tool(tool_name)

    def get_tool(self, tool_name: str) -> Any:
        """Get a tool spec by name.

        Args:
            tool_name: Tool name to look up (canonical or with 'mcp.' prefix).

        Returns:
            ToolSpec if found, None otherwise.
        """
        return self._snapshot.get_tool(tool_name)

    def tool_names(self) -> list[str]:
        """Get all available tool names.

        Returns:
            Sorted list of canonical tool names.
        """
        return self._snapshot.tool_names()