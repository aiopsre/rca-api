from __future__ import annotations

from dataclasses import dataclass
from typing import Any, Dict, Iterable, Mapping

"""
Adapter-local MCP tool metadata hints.

NOT a source of truth for tool availability.
Tool availability is determined by server-resolved strategy/toolset context.

This module is scoped to:
- Local cache/validation hints for adapter execution
- Adapter metadata for tool execution (input/output schemas, scopes)
- Fallback for offline development scenarios

For tool configuration, see:
- Server-side: internal/apiserver/pkg/orchestratorcfg/strategy_config.go
- DB config: toolset_config_dynamics table
- API: /v1/internal-strategy-config/toolsets
- Documentation: docs/tooling/tool-resolution.md

Migration Note:
  Environment-based config (RCA_TOOLSET_CONFIG_JSON/PATH) is deprecated.
  Use DB configuration via /v1/internal-strategy-config/toolsets API.
"""


RISK_LEVEL_READONLY = "readonly"


@dataclass(frozen=True)
class ToolMetadata:
    name: str
    description: str
    input_schema: Mapping[str, Any]
    output_schema: Mapping[str, Any]
    required_scopes: tuple[str, ...]
    risk_level: str = RISK_LEVEL_READONLY

    def to_dict(self) -> Dict[str, Any]:
        return {
            "name": self.name,
            "description": self.description,
            "input_schema": dict(self.input_schema),
            "output_schema": dict(self.output_schema),
            "required_scopes": list(self.required_scopes),
            "risk_level": self.risk_level,
        }


_TOOLS: tuple[ToolMetadata, ...] = (
    ToolMetadata(
        name="get_incident",
        description="Get one incident by incident_id.",
        input_schema={
            "type": "object",
            "properties": {"incident_id": {"type": "string"}},
            "required": ["incident_id"],
        },
        output_schema={"type": "object"},
        required_scopes=("incident.read",),
    ),
    ToolMetadata(
        name="list_alert_events_current",
        description="List current alert events with optional filters and page/limit.",
        input_schema={
            "type": "object",
            "properties": {
                "namespace": {"type": "string"},
                "service": {"type": "string"},
                "severity": {"type": "string"},
                "page": {"type": "integer"},
                "limit": {"type": "integer"},
            },
        },
        output_schema={"type": "object"},
        required_scopes=("alert.read",),
    ),
    ToolMetadata(
        name="get_evidence",
        description="Get one evidence by evidence_id.",
        input_schema={
            "type": "object",
            "properties": {"evidence_id": {"type": "string"}},
            "required": ["evidence_id"],
        },
        output_schema={"type": "object"},
        required_scopes=("evidence.read",),
    ),
    ToolMetadata(
        name="list_incident_evidence",
        description="List evidence by incident_id with page/limit.",
        input_schema={
            "type": "object",
            "properties": {
                "incident_id": {"type": "string"},
                "page": {"type": "integer"},
                "limit": {"type": "integer"},
            },
            "required": ["incident_id"],
        },
        output_schema={"type": "object"},
        required_scopes=("evidence.read",),
    ),
    ToolMetadata(
        name="query_metrics",
        description="Read-only metrics query with evidence guardrails.",
        input_schema={
            "type": "object",
            "properties": {
                "datasource_id": {"type": "string"},
                "expr": {"type": "string"},
                "time_range_start": {"type": "string"},
                "time_range_end": {"type": "string"},
                "step_seconds": {"type": "integer"},
            },
            "required": ["datasource_id", "expr", "time_range_start", "time_range_end"],
        },
        output_schema={"type": "object"},
        required_scopes=("evidence.query",),
    ),
    ToolMetadata(
        name="query_logs",
        description="Read-only logs query with evidence guardrails.",
        input_schema={
            "type": "object",
            "properties": {
                "datasource_id": {"type": "string"},
                "query": {"type": "string"},
                "query_json": {"type": "object"},
                "time_range_start": {"type": "string"},
                "time_range_end": {"type": "string"},
                "limit": {"type": "integer"},
            },
            "required": ["datasource_id", "query", "time_range_start", "time_range_end"],
        },
        output_schema={"type": "object"},
        required_scopes=("evidence.query",),
    ),
)

_TOOLS_BY_NAME: Dict[str, ToolMetadata] = {item.name: item for item in _TOOLS}


def list_tools() -> tuple[ToolMetadata, ...]:
    return _TOOLS


def iter_tool_names() -> Iterable[str]:
    return _TOOLS_BY_NAME.keys()


def get_tool(name: str) -> ToolMetadata | None:
    return _TOOLS_BY_NAME.get(str(name).strip())
