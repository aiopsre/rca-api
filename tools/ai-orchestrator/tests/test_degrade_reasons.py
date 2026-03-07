"""Tests for degradation reason standardization."""
from __future__ import annotations

import pytest

from orchestrator.constants import DegradeReason
from orchestrator.state import GraphState


class TestDegradeReasonEnum:
    """Tests for DegradeReason enum values."""

    def test_all_values_are_lowercase_snake_case(self):
        """Verify all enum values are lowercase snake_case."""
        for reason in DegradeReason:
            # The value must be lowercase snake_case (not necessarily matching name.lower())
            value = reason.value
            assert value == value.lower(), f"{reason.name} value should be lowercase"
            assert "_" in value or value.isalpha(), f"{reason.name} value should be snake_case"

    def test_agent_related_reasons(self):
        """Test agent/LLM related reason codes."""
        assert DegradeReason.AGENT_NOT_CONFIGURED.value == "agent_not_configured"
        assert DegradeReason.AGENT_TIMEOUT.value == "agent_timeout"
        assert DegradeReason.AGENT_ERROR.value == "agent_error"

    def test_skill_related_reasons(self):
        """Test skill related reason codes."""
        assert DegradeReason.SKILL_NOT_FOUND.value == "skill_not_found"
        assert DegradeReason.SKILL_SELECTION_FAILED.value == "skill_selection_failed"
        assert DegradeReason.SKILL_EXECUTE_FAILED.value == "skill_execute_failed"
        assert DegradeReason.SKILL_SCHEMA_INVALID.value == "skill_schema_invalid"
        assert DegradeReason.SCRIPT_EXECUTE_FAILED.value == "script_execute_failed"
        assert DegradeReason.CONSUME_FAILED.value == "consume_failed"

    def test_tool_related_reasons(self):
        """Test tool related reason codes."""
        assert DegradeReason.TOOL_DISCOVERY_EMPTY.value == "tool_discovery_empty"
        assert DegradeReason.TOOL_NOT_FOUND.value == "tool_not_found"
        assert DegradeReason.TOOL_EXECUTE_FAILED.value == "tool_execute_failed"
        assert DegradeReason.TOOL_NOT_ALLOWED.value == "tool_not_allowed"

    def test_validation_related_reasons(self):
        """Test validation related reason codes."""
        assert DegradeReason.SCHEMA_VALIDATION_FAILED.value == "schema_validation_failed"
        assert DegradeReason.PAYLOAD_FIELDS_DROPPED.value == "payload_fields_dropped"
        assert DegradeReason.INVALID_OUTPUT_FORMAT.value == "invalid_output_format"

    def test_runtime_state_reasons(self):
        """Test runtime state reason codes."""
        assert DegradeReason.RUNTIME_NOT_STARTED.value == "runtime_not_started"
        assert DegradeReason.LEASE_LOST.value == "lease_lost"
        assert DegradeReason.JOB_STATUS_CONFLICT.value == "job_status_conflict"

    def test_budget_resource_reasons(self):
        """Test budget/resource reason codes."""
        assert DegradeReason.BUDGET_EXCEEDED.value == "budget_exceeded"
        assert DegradeReason.TIMEOUT_EXCEEDED.value == "timeout_exceeded"

    def test_unknown_reason(self):
        """Test unknown fallback reason code."""
        assert DegradeReason.UNKNOWN.value == "unknown"

    def test_enum_is_string_enum(self):
        """Verify DegradeReason inherits from str."""
        reason = DegradeReason.AGENT_NOT_CONFIGURED
        assert isinstance(reason, str)
        assert isinstance(reason.value, str)


class TestGraphStateDegradeReasons:
    """Tests for GraphState degrade_reasons field."""

    def test_degrade_reasons_default_empty(self):
        """Verify degrade_reasons starts as empty list."""
        state = GraphState(job_id="test-job")
        assert state.degrade_reasons == []

    def test_add_degrade_reason(self):
        """Test adding a degrade reason."""
        state = GraphState(job_id="test-job")
        state.add_degrade_reason(DegradeReason.AGENT_NOT_CONFIGURED.value)
        assert state.degrade_reasons == ["agent_not_configured"]

    def test_add_degrade_reason_with_context(self):
        """Test adding a degrade reason with context."""
        state = GraphState(job_id="test-job")
        state.add_degrade_reason(DegradeReason.SKILL_EXECUTE_FAILED.value, "timeout after 30s")
        assert state.degrade_reasons == ["skill_execute_failed:timeout after 30s"]

    def test_add_degrade_reason_no_duplicates(self):
        """Verify duplicate reasons are not added."""
        state = GraphState(job_id="test-job")
        state.add_degrade_reason(DegradeReason.AGENT_NOT_CONFIGURED.value)
        state.add_degrade_reason(DegradeReason.AGENT_NOT_CONFIGURED.value)
        assert state.degrade_reasons == ["agent_not_configured"]

    def test_add_multiple_degrade_reasons(self):
        """Test adding multiple different reasons."""
        state = GraphState(job_id="test-job")
        state.add_degrade_reason(DegradeReason.AGENT_NOT_CONFIGURED.value)
        state.add_degrade_reason(DegradeReason.TOOL_DISCOVERY_EMPTY.value)
        state.add_degrade_reason(DegradeReason.SKILL_SELECTION_FAILED.value, "no candidates")
        assert len(state.degrade_reasons) == 3
        assert "agent_not_configured" in state.degrade_reasons
        assert "tool_discovery_empty" in state.degrade_reasons
        assert "skill_selection_failed:no candidates" in state.degrade_reasons


class TestCapabilitiesValidationReasons:
    """Tests for capabilities.py validation."""

    def test_logs_branch_meta_invalid_type(self):
        """Test logs_branch_meta validation with invalid type."""
        from orchestrator.skills.capabilities import _sanitize_logs_branch_meta

        result, dropped = _sanitize_logs_branch_meta("not a dict")
        assert result is None
        assert len(dropped) == 1
        assert "logs_branch_meta" in dropped[0]

    def test_logs_branch_meta_missing_mode(self):
        """Test logs_branch_meta validation with missing mode."""
        from orchestrator.skills.capabilities import _sanitize_logs_branch_meta

        result, dropped = _sanitize_logs_branch_meta({"query_type": "logs"})
        assert result is None
        assert any("mode" in d for d in dropped)

    def test_metrics_branch_meta_invalid_type(self):
        """Test metrics_branch_meta validation with invalid type."""
        from orchestrator.skills.capabilities import _sanitize_metrics_branch_meta

        result, dropped = _sanitize_metrics_branch_meta(None)
        assert result is None
        assert len(dropped) == 1
        assert "metrics_branch_meta" in dropped[0]

    def test_metrics_branch_meta_missing_promql(self):
        """Test metrics_branch_meta validation with missing promql."""
        from orchestrator.skills.capabilities import _sanitize_metrics_branch_meta

        result, dropped = _sanitize_metrics_branch_meta({
            "mode": "query",
            "query_type": "metrics",
            "request_payload": {},
            "query_request": {"queryText": "test"},
        })
        assert result is None
        assert any("promql" in d for d in dropped)


class TestRuntimeReasonConstants:
    """Tests for runtime.py using DegradeReason constants."""

    def test_agent_not_configured_import(self):
        """Verify DegradeReason.AGENT_NOT_CONFIGURED is properly defined."""
        from orchestrator.constants import DegradeReason

        assert DegradeReason.AGENT_NOT_CONFIGURED.value == "agent_not_configured"

    def test_skill_selection_failed_import(self):
        """Verify DegradeReason.SKILL_SELECTION_FAILED is properly defined."""
        from orchestrator.constants import DegradeReason

        assert DegradeReason.SKILL_SELECTION_FAILED.value == "skill_selection_failed"

    def test_payload_fields_dropped_import(self):
        """Verify DegradeReason.PAYLOAD_FIELDS_DROPPED is properly defined."""
        from orchestrator.constants import DegradeReason

        assert DegradeReason.PAYLOAD_FIELDS_DROPPED.value == "payload_fields_dropped"


class TestObservationTypeConstants:
    """Tests for observation type constants."""

    def test_observation_type_skill_select(self):
        """Test skill.select observation type."""
        from orchestrator.constants import OBSERVATION_TYPE_SKILL_SELECT

        assert OBSERVATION_TYPE_SKILL_SELECT == "skill.select"

    def test_observation_type_skill_execute(self):
        """Test skill.execute observation type."""
        from orchestrator.constants import OBSERVATION_TYPE_SKILL_EXECUTE

        assert OBSERVATION_TYPE_SKILL_EXECUTE == "skill.execute"

    def test_observation_type_skill_fallback(self):
        """Test skill.fallback observation type."""
        from orchestrator.constants import OBSERVATION_TYPE_SKILL_FALLBACK

        assert OBSERVATION_TYPE_SKILL_FALLBACK == "skill.fallback"

    def test_observation_type_skill_tool_reuse(self):
        """Test skill.tool_reuse observation type."""
        from orchestrator.constants import OBSERVATION_TYPE_SKILL_TOOL_REUSE

        assert OBSERVATION_TYPE_SKILL_TOOL_REUSE == "skill.tool_reuse"