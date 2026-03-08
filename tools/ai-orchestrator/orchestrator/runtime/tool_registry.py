"""Tool metadata registry for explicit tool classification.

This module provides a registry for tool metadata, allowing tools to be
classified without relying on name-based inference.

Tool metadata is now managed by the platform (Go side) and synced to the
orchestrator via McpServerRef.tool_metadata field. The static definitions
have been removed in favor of platform-managed metadata.
"""
from __future__ import annotations

from dataclasses import dataclass
from typing import TYPE_CHECKING, Any
import threading

if TYPE_CHECKING:
    from ..tooling.mcp_server_loader import McpServerRef


@dataclass(frozen=True)
class ToolMetadata:
    """Metadata for a tool.

    Attributes:
        tool_name: The name of the tool.
        kind: Primary classification (metrics, logs, traces, incidents).
        domain: Domain classification (observability, incident, ...).
        read_only: Whether the tool only reads data (no side effects).
        risk_level: Risk level for execution (low, medium, high).
        latency_tier: Expected latency (fast, medium, slow).
        cost_hint: Cost indication (free, low, medium, high).
        tags: Additional classification tags.
        description: Human-readable description.
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


class ToolRegistry:
    """Registry for tool metadata with runtime extension support."""

    def __init__(self, initial: dict[str, ToolMetadata] | None = None) -> None:
        self._metadata: dict[str, ToolMetadata] = dict(initial or {})
        self._lock = threading.RLock()

    def get(self, tool_name: str) -> ToolMetadata | None:
        """Get metadata for a tool.

        Args:
            tool_name: The tool name to look up.

        Returns:
            ToolMetadata if registered, None otherwise.
        """
        with self._lock:
            return self._metadata.get(tool_name)

    def register(self, metadata: ToolMetadata) -> None:
        """Register or update tool metadata.

        Args:
            metadata: The tool metadata to register.
        """
        with self._lock:
            self._metadata[metadata.tool_name] = metadata

    def register_batch(self, metadata_list: list[ToolMetadata]) -> None:
        """Register multiple tool metadata entries.

        Args:
            metadata_list: List of tool metadata to register.
        """
        with self._lock:
            for metadata in metadata_list:
                self._metadata[metadata.tool_name] = metadata

    def register_from_mcpserver_ref(self, ref: McpServerRef) -> int:
        """Register tool metadata from McpServerRef.

        This is the primary method for registering tool metadata from
        the platform. The platform manages tool metadata in the database
        and syncs it to the orchestrator via McpServerRef.tool_metadata.

        Args:
            ref: McpServerRef containing tool_metadata from platform.

        Returns:
            Number of tools registered.
        """
        if not ref.tool_metadata:
            return 0

        count = 0
        with self._lock:
            for meta in ref.tool_metadata:
                # Convert ToolMetadataRef to ToolMetadata
                tool_meta = ToolMetadata(
                    tool_name=meta.tool_name,
                    kind=meta.kind,
                    domain=meta.domain,
                    read_only=meta.read_only,
                    risk_level=meta.risk_level,
                    latency_tier=meta.latency_tier,
                    cost_hint=meta.cost_hint,
                    tags=meta.tags,
                    description=meta.description,
                )
                self._metadata[meta.tool_name] = tool_meta
                count += 1
        return count

    def all_tools(self) -> list[str]:
        """Get all registered tool names.

        Returns:
            Sorted list of tool names.
        """
        with self._lock:
            return sorted(self._metadata.keys())

    def find_by_kind(self, kind: str) -> list[ToolMetadata]:
        """Find all tools of a specific kind.

        Args:
            kind: The kind to search for.

        Returns:
            List of matching tool metadata.
        """
        with self._lock:
            return [m for m in self._metadata.values() if m.kind == kind]

    def find_by_tag(self, tag: str) -> list[ToolMetadata]:
        """Find all tools with a specific tag.

        Args:
            tag: The tag to search for.

        Returns:
            List of matching tool metadata.
        """
        with self._lock:
            return [m for m in self._metadata.values() if tag in m.tags]


# Global registry instance - starts empty, filled by platform via McpServerRef
_global_registry = ToolRegistry()


def get_tool_metadata(tool_name: str) -> ToolMetadata | None:
    """Get metadata for a tool from the global registry.

    Args:
        tool_name: The tool name to look up.

    Returns:
        ToolMetadata if registered, None otherwise.
    """
    return _global_registry.get(tool_name)


def register_tool_metadata(metadata: ToolMetadata) -> None:
    """Register tool metadata in the global registry.

    Args:
        metadata: The tool metadata to register.
    """
    _global_registry.register(metadata)


def register_tools_from_mcpserver_refs(refs: list[McpServerRef]) -> int:
    """Register all tool metadata from McpServerRef list.

    This is the primary entry point for registering tool metadata from
    the platform. Call this during orchestrator initialization with the
    McpServerRef list from the platform.

    Args:
        refs: List of McpServerRef objects from platform.

    Returns:
        Total number of tools registered.
    """
    registry = get_global_registry()
    total = 0
    for ref in refs:
        total += registry.register_from_mcpserver_ref(ref)
    return total


def register_tools_from_mcp(
    tools_info: list[dict[str, Any]],
    fallback_infer_func: Any = None,
) -> list[str]:
    """Register tool metadata from MCP Server tool list.

    MCP Servers can provide tool metadata via:
    - tool.name: Tool name
    - tool.description: Tool description
    - tool.tags: Optional tags (custom extension)
    - tool.kind: Optional kind (custom extension)

    Args:
        tools_info: List of tool info from MCP Server.
        fallback_infer_func: Optional function to infer tags from tool name
            when tags are not provided. Should accept tool_name and return
            tuple of tags.

    Returns:
        List of registered tool names.
    """
    metadata_list: list[ToolMetadata] = []
    registered_names: list[str] = []

    for tool_info in tools_info:
        if not isinstance(tool_info, dict):
            continue

        tool_name = str(tool_info.get("name") or "").strip()
        if not tool_name:
            continue

        # Skip if already registered (preserve explicit registration)
        if _global_registry.get(tool_name) is not None:
            registered_names.append(tool_name)
            continue

        # Extract optional metadata from MCP tool definition
        description = str(tool_info.get("description") or "")
        tags = tool_info.get("tags")
        kind = str(tool_info.get("kind") or "unknown")

        # Convert tags to tuple
        if isinstance(tags, list):
            tags_tuple = tuple(str(t) for t in tags if t)
        elif isinstance(tags, tuple):
            tags_tuple = tags
        else:
            # Fallback to inference
            if fallback_infer_func is not None:
                tags_tuple = tuple(fallback_infer_func(tool_name))
            else:
                tags_tuple = ()

        metadata = ToolMetadata(
            tool_name=tool_name,
            kind=kind,
            description=description,
            tags=tags_tuple,
        )
        metadata_list.append(metadata)
        registered_names.append(tool_name)

    if metadata_list:
        _global_registry.register_batch(metadata_list)

    return registered_names


def get_global_registry() -> ToolRegistry:
    """Get the global tool registry instance.

    Returns:
        The global ToolRegistry instance.
    """
    return _global_registry