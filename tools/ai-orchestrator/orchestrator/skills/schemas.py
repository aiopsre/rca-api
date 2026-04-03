"""JSON Schema definitions for capability outputs."""
from __future__ import annotations

from typing import Any

# Tool plan output schema
TOOL_PLAN_OUTPUT_SCHEMA: dict[str, Any] = {
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "type": "object",
    "properties": {
        "tool_call_plan": {
            "type": "object",
            "properties": {
                "items": {
                    "type": "array",
                    "items": {
                        "type": "object",
                        "required": ["tool"],
                        "properties": {
                            "tool": {"type": "string", "minLength": 1},
                            "params": {"type": "object"},
                            "query_type": {"type": "string"},
                            "purpose": {"type": "string"},
                            "evidence_kind": {"type": "string"},
                            "optional": {"type": "boolean"},
                            "depends_on": {"type": "array", "items": {"type": "string"}},
                            "call_id": {"type": "string"},
                        },
                    },
                    "minItems": 1,
                },
                "parallel_groups": {
                    "type": "array",
                    "items": {
                        "type": "array",
                        "items": {"type": "integer"},
                    },
                },
            },
            "required": ["items"],
        },
    },
    "required": ["tool_call_plan"],
}

# Evidence plan output schema
EVIDENCE_PLAN_OUTPUT_SCHEMA: dict[str, Any] = {
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "type": "object",
    "properties": {
        "evidence_plan_patch": {"type": "object"},
        "evidence_candidates": {
            "type": "array",
            "items": {"type": "object"},
        },
        "metrics_branch_meta": {"type": "object"},
        "logs_branch_meta": {"type": "object"},
    },
}

# Diagnosis enrich output schema
DIAGNOSIS_OUTPUT_SCHEMA: dict[str, Any] = {
    "$schema": "https://json-schema.org/draft/2020-12/schema",
    "type": "object",
    "properties": {
        "diagnosis_patch": {
            "type": "object",
            "properties": {
                "summary": {"type": "string"},
                "root_cause": {
                    "type": "object",
                    "properties": {
                        "summary": {"type": "string"},
                        "statement": {"type": "string"},
                    },
                },
                "recommendations": {"type": "array", "items": {"type": "object"}},
                "unknowns": {"type": "array", "items": {"type": "string"}},
                "next_steps": {"type": "array", "items": {"type": "string"}},
            },
        },
    },
}

# Registry mapping capability names to their schemas
CAPABILITY_SCHEMAS: dict[str, dict[str, Any]] = {
    "tool.plan": TOOL_PLAN_OUTPUT_SCHEMA,
    "evidence.plan": EVIDENCE_PLAN_OUTPUT_SCHEMA,
    "diagnosis.enrich": DIAGNOSIS_OUTPUT_SCHEMA,
}


def get_schema(capability: str) -> dict[str, Any] | None:
    """Get JSON Schema for a capability.

    Args:
        capability: The capability name (e.g., "tool.plan").

    Returns:
        The JSON Schema dict if defined, otherwise None.
    """
    return CAPABILITY_SCHEMAS.get(capability)