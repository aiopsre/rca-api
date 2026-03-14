"""Skills middleware for injecting skill context into agent requests."""
from __future__ import annotations

from typing import TYPE_CHECKING, Any

from .base import AgentMiddleware, AgentRequest, AgentResponse

if TYPE_CHECKING:
    from ..state import GraphState
    from ..runtime.resolved_context import ResolvedAgentContext


class SkillsMiddleware(AgentMiddleware):
    """Middleware that injects skill context into prompts.

    This middleware reads skill_surface from the context and
    adds relevant skill information to the agent request.
    """

    def prepare(
        self,
        state: "GraphState",
        context: "ResolvedAgentContext",
        request: AgentRequest,
        config: dict[str, Any],
    ) -> AgentRequest:
        """Inject skill context into the request.

        Config options:
        - include_skill_ids: Whether to include skill IDs (default: True)
        - include_capability_map: Whether to include capability map (default: False)
        - filter_capabilities: List of capabilities to include (empty = all)
        """
        include_skill_ids = config.get("include_skill_ids", True)
        include_capability_map = config.get("include_capability_map", False)
        filter_capabilities = set(config.get("filter_capabilities") or [])

        skill_surface = context.skill_surface
        if not skill_surface or (not skill_surface.skill_ids and not skill_surface.capability_map):
            return request

        skill_parts: list[str] = []

        if include_skill_ids and skill_surface.skill_ids:
            skill_ids_str = ", ".join(skill_surface.skill_ids)
            skill_parts.append(f"Available Skills: {skill_ids_str}")

        if include_capability_map and skill_surface.capability_map:
            filtered_map = {}
            for cap, bindings in skill_surface.capability_map.items():
                if filter_capabilities and cap not in filter_capabilities:
                    continue
                filtered_map[cap] = bindings

            if filtered_map:
                import json
                cap_json = json.dumps(filtered_map, ensure_ascii=False, indent=2)
                skill_parts.append(f"Capability Map:\n{cap_json}")

        if skill_parts:
            existing_prompt = request.user_prompt
            skill_block = "\n\n".join(skill_parts)
            request.user_prompt = f"{skill_block}\n\n{existing_prompt}"

        return request