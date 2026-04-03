"""Unified audit record for MCP/Skill invocations."""
from __future__ import annotations

from dataclasses import dataclass, field
from enum import Enum
from typing import Any
import time


class AuditKind(str, Enum):
    """Audit event kinds."""
    SKILL_SELECT = "skill.select"
    SKILL_RESOURCE_SELECT = "skill.resource_select"
    SKILL_RESOURCE_LOAD = "skill.resource_load"
    SKILL_CONSUME = "skill.consume"
    SKILL_EXECUTE = "skill.execute"
    SKILL_FALLBACK = "skill.fallback"
    TOOL_INVOKE = "tool.invoke"
    TOOL_REUSE = "tool.reuse"
    TOOL_PLAN = "tool.plan"


@dataclass
class AuditRecord:
    """Unified audit record structure."""
    # Identity
    incident_id: str = ""
    ai_job_id: str = ""

    # Execution context
    kind: AuditKind = AuditKind.TOOL_INVOKE
    node_name: str = ""
    seq: int = 0

    # Skill context (when applicable)
    skill_id: str = ""
    skill_version: str = ""
    capability: str = ""
    binding_key: str = ""

    # Tool context (when applicable)
    tool_name: str = ""
    provider_id: str = ""
    provider_type: str = ""
    toolset_id: str = ""

    # Request/Response (redacted)
    request_summary: dict[str, Any] = field(default_factory=dict)
    response_summary: dict[str, Any] = field(default_factory=dict)

    # Metrics
    latency_ms: int = 0
    status: str = "ok"

    # Error info
    error: str | None = None
    degrade_reason: str | None = None

    # Evidence
    evidence_ids: list[str] = field(default_factory=list)

    # Timestamp
    timestamp_ms: int = field(default_factory=lambda: int(time.time() * 1000))

    def to_observation_params(self) -> dict[str, Any]:
        """Convert to observation params format."""
        return {
            "skill_id": self.skill_id,
            "skill_version": self.skill_version,
            "capability": self.capability,
            "binding_key": self.binding_key,
            "tool_name": self.tool_name,
            "provider_id": self.provider_id,
            "provider_type": self.provider_type,
            "toolset_id": self.toolset_id,
        }

    def to_observation_response(self) -> dict[str, Any]:
        """Convert to observation response format."""
        result = {
            "status": self.status,
            "latency_ms": self.latency_ms,
        }
        if self.error:
            result["error"] = self.error
        if self.degrade_reason:
            result["degrade_reason"] = self.degrade_reason
        return result


# Sensitive field patterns for redaction
SENSITIVE_FIELD_PATTERNS = {
    "api_key", "apikey", "api-key",
    "token", "access_token", "refresh_token", "auth_token",
    "password", "passwd", "pwd",
    "secret", "secret_key", "private_key",
    "authorization", "auth",
    "credential", "credentials",
}

# Fields to redact (case-insensitive match)
REDACT_VALUE = "***REDACTED***"


def _should_redact(key: str) -> bool:
    """Check if a key should be redacted."""
    key_lower = key.lower().replace("-", "_").replace(" ", "_")
    for pattern in SENSITIVE_FIELD_PATTERNS:
        if pattern in key_lower:
            return True
    return False


def redact_sensitive(data: dict[str, Any], *, max_depth: int = 5) -> dict[str, Any]:
    """Redact sensitive fields from a dictionary.

    Args:
        data: Dictionary to redact.
        max_depth: Maximum recursion depth.

    Returns:
        Redacted dictionary.
    """
    if not isinstance(data, dict) or max_depth <= 0:
        return data if isinstance(data, dict) else {}

    result: dict[str, Any] = {}
    for key, value in data.items():
        if _should_redact(key):
            result[key] = REDACT_VALUE
        elif isinstance(value, dict):
            result[key] = redact_sensitive(value, max_depth=max_depth - 1)
        elif isinstance(value, list):
            result[key] = [
                redact_sensitive(item, max_depth=max_depth - 1) if isinstance(item, dict) else item
                for item in value
            ]
        else:
            result[key] = value
    return result


def summarize_request(tool_name: str, params: dict[str, Any]) -> dict[str, Any]:
    """Create a summary of a tool request for audit.

    Args:
        tool_name: Name of the tool being called.
        params: Request parameters.

    Returns:
        Summarized and redacted request.
    """
    redacted = redact_sensitive(params)

    # For query tools, extract key fields
    if "query" in tool_name.lower() or "mcp" in tool_name.lower():
        summary: dict[str, Any] = {"tool": tool_name}
        if "query" in redacted:
            query = str(redacted.get("query", ""))[:200]
            summary["query"] = query if len(query) == 200 else redacted.get("query")
        if "promql" in redacted:
            promql = str(redacted.get("promql", ""))[:200]
            summary["promql"] = promql if len(promql) == 200 else redacted.get("promql")
        if "datasource_id" in redacted:
            summary["datasource_id"] = redacted["datasource_id"]
        if "start_ts" in redacted or "end_ts" in redacted:
            summary["time_range"] = {
                "start_ts": redacted.get("start_ts"),
                "end_ts": redacted.get("end_ts"),
            }
        return summary

    # Default: return redacted params with size limit
    return _truncate_dict(redacted, max_keys=10, max_value_len=200)


def summarize_response(response: dict[str, Any] | None) -> dict[str, Any]:
    """Create a summary of a tool response for audit.

    Args:
        response: Response payload.

    Returns:
        Summarized response.
    """
    if not isinstance(response, dict):
        return {"status": "empty"}

    redacted = redact_sensitive(response)

    summary: dict[str, Any] = {
        "status": str(redacted.get("status", "ok")),
    }

    # Extract size info
    if "resultSizeBytes" in redacted or "result_size_bytes" in redacted:
        summary["result_size_bytes"] = redacted.get("resultSizeBytes") or redacted.get("result_size_bytes")
    if "rowCount" in redacted or "row_count" in redacted:
        summary["row_count"] = redacted.get("rowCount") or redacted.get("row_count")
    if "isTruncated" in redacted or "is_truncated" in redacted:
        summary["is_truncated"] = redacted.get("isTruncated") or redacted.get("is_truncated")

    # Include error if present
    if "error" in redacted:
        summary["error"] = str(redacted["error"])[:500]

    return summary


def _truncate_dict(data: dict[str, Any], *, max_keys: int = 10, max_value_len: int = 200) -> dict[str, Any]:
    """Truncate a dictionary to prevent oversized audit records."""
    result: dict[str, Any] = {}
    for i, (key, value) in enumerate(data.items()):
        if i >= max_keys:
            result["_truncated"] = f"{len(data) - max_keys} more keys"
            break
        if isinstance(value, str) and len(value) > max_value_len:
            result[key] = f"{value[:max_value_len]}... (truncated {len(value)} chars)"
        elif isinstance(value, dict):
            result[key] = _truncate_dict(value, max_keys=max_keys, max_value_len=max_value_len)
        else:
            result[key] = value
    return result