"""Tool surface middleware for controlling visible tools."""
from __future__ import annotations

from typing import TYPE_CHECKING, Any

from .base import AgentMiddleware, AgentRequest, AgentResponse

if TYPE_CHECKING:
    from ..state import GraphState
    from ..runtime.resolved_context import ResolvedAgentContext


# Explicit mapping from surface name to tool metadata field
# This ensures we don't rely on string concatenation and match
# the actual field names in ToolSpec.
SURFACE_TO_ALLOWED_FIELD: dict[str, str] = {
    "skills": "allowed_for_prompt_skill",
    "graph": "allowed_for_graph_agent",
}


class ToolSurfaceMiddleware(AgentMiddleware):
    """Middleware that controls which tools are visible to the agent.

    This middleware reads from the ToolCatalogSnapshot in the context
    and filters tools based on configuration.
    """

    def prepare(
        self,
        state: "GraphState",
        context: "ResolvedAgentContext",
        request: AgentRequest,
        config: dict[str, Any],
    ) -> AgentRequest:
        """Prepare request by setting visible tools.

        Config options:
        - mode: "skills_only" (no tools), "fc_surface" (A-class only), "all"
        - tool_scope: List of tool names to include (empty = all)
        - surface: "skills" or "graph" for per-surface visibility
        """
        mode = str(config.get("mode") or "").strip()

        # Skills-only mode: no tools visible
        if mode == "skills_only":
            request.visible_tools = []
            return request

        # Get tool catalog from context
        tool_surface = context.tool_surface
        if not tool_surface or not tool_surface.tool_catalog_snapshot:
            request.visible_tools = []
            return request

        catalog = tool_surface.tool_catalog_snapshot
        tools = catalog.get("tools", [])
        if not isinstance(tools, list):
            request.visible_tools = []
            return request

        # Determine which tools to show
        surface = str(config.get("surface") or "").strip()
        tool_scope = set(config.get("tool_scope") or [])

        # Resolve the allowed field name for the surface
        allowed_field: str | None = None
        if surface:
            allowed_field = SURFACE_TO_ALLOWED_FIELD.get(surface)
            if allowed_field is None:
                # Unknown surface: reject all tools for safety
                request.visible_tools = []
                return request

        visible: list[dict[str, Any]] = []
        for spec in tools:
            if not isinstance(spec, dict):
                continue

            name = str(spec.get("name") or "").strip()
            if not name:
                continue

            # Apply tool_scope filter
            if tool_scope and name not in tool_scope:
                continue

            # Apply per-surface visibility if specified
            if allowed_field is not None:
                if not spec.get(allowed_field, True):
                    continue

            # Apply tool_class filter for FC surface
            tool_class = str(spec.get("tool_class") or "fc_selectable")
            if mode == "fc_surface" and tool_class != "fc_selectable":
                continue

            visible.append({
                "name": name,
                "description": str(spec.get("description") or ""),
            })

        request.visible_tools = visible
        return request