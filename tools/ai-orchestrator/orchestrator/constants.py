"""Standard constants for the AI Orchestrator."""

from enum import Enum


class DegradeReason(str, Enum):
    """Standard reason codes for degradation/fallback scenarios.

    These codes are used in observations, logs, and audit records
    to provide traceability for why a fallback occurred.
    """

    # Agent/LLM related
    AGENT_NOT_CONFIGURED = "agent_not_configured"
    AGENT_TIMEOUT = "agent_timeout"
    AGENT_ERROR = "agent_error"

    # Skill related
    SKILL_NOT_FOUND = "skill_not_found"
    SKILL_SELECTION_FAILED = "skill_selection_failed"
    SKILL_KNOWLEDGE_SELECTION_FAILED = "knowledge_selection_failed"
    SKILL_EXECUTE_FAILED = "skill_execute_failed"
    SKILL_SCHEMA_INVALID = "skill_schema_invalid"
    SCRIPT_EXECUTE_FAILED = "script_execute_failed"
    CONSUME_FAILED = "consume_failed"

    # Tool related
    TOOL_DISCOVERY_EMPTY = "tool_discovery_empty"
    TOOL_NOT_FOUND = "tool_not_found"
    TOOL_EXECUTE_FAILED = "tool_execute_failed"
    TOOL_NOT_ALLOWED = "tool_not_allowed"

    # MCP related
    MCP_REGISTRY_UNAVAILABLE = "mcp_registry_unavailable"
    MCP_CONNECTION_FAILED = "mcp_connection_failed"

    # Validation related
    SCHEMA_VALIDATION_FAILED = "schema_validation_failed"
    PAYLOAD_FIELDS_DROPPED = "payload_fields_dropped"
    INVALID_OUTPUT_FORMAT = "invalid_output_format"

    # Runtime state
    RUNTIME_NOT_STARTED = "runtime_not_started"
    LEASE_LOST = "lease_lost"
    JOB_STATUS_CONFLICT = "job_status_conflict"

    # Budget/Resource
    BUDGET_EXCEEDED = "budget_exceeded"
    TIMEOUT_EXCEEDED = "timeout_exceeded"

    # Default fallback
    UNKNOWN = "unknown"


# Observation types for skill execution
OBSERVATION_TYPE_SKILL_SELECT = "skill.select"
OBSERVATION_TYPE_SKILL_EXECUTE = "skill.execute"
OBSERVATION_TYPE_SKILL_FALLBACK = "skill.fallback"
OBSERVATION_TYPE_SKILL_TOOL_REUSE = "skill.tool_reuse"

# Trace event names for hybrid multi-agent
TRACE_EVENT_ROUTER_ROUTE = "router.route"
TRACE_EVENT_DOMAIN_EXECUTE = "domain.execute"
TRACE_EVENT_DOMAIN_MERGE = "domain.merge"
TRACE_EVENT_PLATFORM_SPECIAL_SUMMARIZE = "platform_special.summarize"