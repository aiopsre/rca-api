"""Resolved Agent Context for hybrid multi-agent system.

This module defines the unified agent input context that is passed from
the platform (Go) to the worker (Python) at claim time.

Phase HM1: Platform/worker unified Agent input.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any


@dataclass(frozen=True)
class ToolSurface:
    """Tool surface summary built from resolved_tool_providers.

    Contains the tool catalog snapshot that defines what tools are
    visible to different agent surfaces (skills vs graph agents).
    """

    tool_catalog_snapshot: dict[str, Any] = field(default_factory=dict)

    def to_dict(self) -> dict[str, Any]:
        """Convert to dictionary for JSON serialization."""
        return {"tool_catalog_snapshot": self.tool_catalog_snapshot}


@dataclass(frozen=True)
class SkillSurface:
    """Skill surface summary built from skillsets.

    Contains the skill IDs and capability map that define what skills
    are available for this job.
    """

    skill_ids: list[str] = field(default_factory=list)
    capability_map: dict[str, list[str]] = field(default_factory=dict)

    def to_dict(self) -> dict[str, Any]:
        """Convert to dictionary for JSON serialization."""
        return {
            "skill_ids": self.skill_ids,
            "capability_map": self.capability_map,
        }


@dataclass(frozen=True)
class ResolvedAgentContext:
    """Unified agent input context.

    This is the single source of truth for agent execution context,
    assembled at claim time and passed from platform to worker.

    Key principles:
    - AIJob is still the only execution envelope
    - rca-apiserver is still the control plane source of truth
    - runtime is still the only tool execution gateway
    - basic_rca is still the only production template
    """

    job_id: str
    pipeline: str
    template_id: str
    session_snapshot: dict[str, Any] = field(default_factory=dict)
    tool_surface: ToolSurface = field(default_factory=ToolSurface)
    skill_surface: SkillSurface = field(default_factory=SkillSurface)
    platform_hints: dict[str, Any] = field(default_factory=dict)
    run_policies: dict[str, Any] = field(default_factory=dict)

    def to_dict(self) -> dict[str, Any]:
        """Convert to dictionary for JSON serialization."""
        return {
            "job_id": self.job_id,
            "pipeline": self.pipeline,
            "template_id": self.template_id,
            "session_snapshot": self.session_snapshot,
            "tool_surface": self.tool_surface.to_dict(),
            "skill_surface": self.skill_surface.to_dict(),
            "platform_hints": self.platform_hints,
            "run_policies": self.run_policies,
        }

    def to_json(self) -> str:
        """Convert to JSON string."""
        import json
        return json.dumps(self.to_dict(), ensure_ascii=False)

    @classmethod
    def from_json(cls, raw: str) -> "ResolvedAgentContext":
        """Parse from JSON string.

        Args:
            raw: JSON string containing agent context data.

        Returns:
            ResolvedAgentContext instance.
        """
        import json

        data = json.loads(raw)

        tool_surface_data = data.get("tool_surface", {})
        if isinstance(tool_surface_data, dict):
            # Support both Go schema (tools array) and Python schema (tool_catalog_snapshot)
            # Go sends: tool_surface: { toolset_ids, tools: [...] }
            # Python expects: tool_surface: { tool_catalog_snapshot: {...} }
            tool_catalog_snapshot = tool_surface_data.get("tool_catalog_snapshot", {})

            # If tool_catalog_snapshot is empty but tools array exists, convert it
            if not tool_catalog_snapshot and "tools" in tool_surface_data:
                tools_list = tool_surface_data.get("tools", [])
                if isinstance(tools_list, list):
                    tool_catalog_snapshot = {"tools": tools_list}

            tool_surface = ToolSurface(
                tool_catalog_snapshot=tool_catalog_snapshot
            )
        else:
            tool_surface = ToolSurface()

        skill_surface_data = data.get("skill_surface", {})
        if isinstance(skill_surface_data, dict):
            skill_surface = SkillSurface(
                skill_ids=skill_surface_data.get("skill_ids", []),
                capability_map=skill_surface_data.get("capability_map", {}),
            )
        else:
            skill_surface = SkillSurface()

        return cls(
            job_id=str(data.get("job_id", "")),
            pipeline=str(data.get("pipeline", "")),
            template_id=str(data.get("template_id", "")),
            session_snapshot=data.get("session_snapshot", {}) or {},
            tool_surface=tool_surface,
            skill_surface=skill_surface,
            platform_hints=data.get("platform_hints", {}) or {},
            run_policies=data.get("run_policies", {}) or {},
        )

    @classmethod
    def from_json_or_empty(cls, raw: str | None) -> "ResolvedAgentContext":
        """Parse from JSON string or return empty context.

        Args:
            raw: JSON string containing agent context data, or None/empty.

        Returns:
            ResolvedAgentContext instance (empty if raw is None or empty).
        """
        if not raw or not raw.strip():
            return cls(job_id="", pipeline="", template_id="")
        return cls.from_json(raw)