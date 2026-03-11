"""Tests for canonical tool name normalization."""

from __future__ import annotations

import pytest

from orchestrator.tooling.canonical_names import (
    normalize_tool_name,
    UNDERSCORE_TO_CANONICAL,
    CANONICAL_TO_ALIAS,
    get_alias_for_canonical,
    is_dotted_name,
    get_all_aliases_for_canonical,
)


class TestNormalizeToolName:
    """Tests for normalize_tool_name function."""

    def test_normalize_none_returns_empty(self):
        assert normalize_tool_name(None) == ""

    def test_normalize_empty_string(self):
        assert normalize_tool_name("") == ""

    def test_normalize_whitespace_only(self):
        assert normalize_tool_name("   ") == ""

    def test_normalize_already_canonical(self):
        # Dotted names pass through unchanged
        assert normalize_tool_name("incident.get") == "incident.get"
        assert normalize_tool_name("logs.query") == "logs.query"
        assert normalize_tool_name("metrics.query_range") == "metrics.query_range"

    def test_normalize_underscore_to_canonical(self):
        # Platform tools
        assert normalize_tool_name("get_incident") == "incident.get"
        assert normalize_tool_name("list_incidents") == "incident.list"
        assert normalize_tool_name("get_evidence") == "evidence.get"
        assert normalize_tool_name("search_evidence") == "evidence.search"
        assert normalize_tool_name("get_ai_job") == "job.get"
        assert normalize_tool_name("list_ai_jobs") == "job.list"
        assert normalize_tool_name("list_tool_calls") == "tool_call.list"

        # Observability tools
        assert normalize_tool_name("query_logs") == "logs.query"
        assert normalize_tool_name("query_metrics") == "metrics.query"
        assert normalize_tool_name("query_range") == "metrics.query_range"
        assert normalize_tool_name("query_traces") == "traces.query"

        # Session/Knowledge base tools
        assert normalize_tool_name("patch_session_context") == "session.patch"
        assert normalize_tool_name("save_knowledge_base_entry") == "knowledge_base.save"
        assert normalize_tool_name("publish_evidence") == "evidence.publish"

    def test_normalize_strips_mcp_prefix(self):
        assert normalize_tool_name("mcp.get_incident") == "incident.get"
        assert normalize_tool_name("mcp.logs.query") == "logs.query"
        assert normalize_tool_name("mcp.query_metrics") == "metrics.query"

    def test_normalize_is_case_insensitive(self):
        assert normalize_tool_name("GET_INCIDENT") == "incident.get"
        assert normalize_tool_name("Get_Incident") == "incident.get"
        assert normalize_tool_name("QUERY_METRICS") == "metrics.query"
        assert normalize_tool_name("Logs.Query") == "logs.query"

    def test_normalize_strips_whitespace(self):
        assert normalize_tool_name("  get_incident  ") == "incident.get"
        assert normalize_tool_name("\tquery_metrics\n") == "metrics.query"

    def test_normalize_unknown_name_passthrough(self):
        # Unknown names pass through as-is (lowercased)
        assert normalize_tool_name("unknown_tool") == "unknown_tool"
        assert normalize_tool_name("custom.operation") == "custom.operation"

    def test_normalize_common_aliases(self):
        assert normalize_tool_name("get_inc") == "incident.get"
        assert normalize_tool_name("list_inc") == "incident.list"

    def test_normalize_mcp_with_underscore(self):
        # mcp.get_incident should strip mcp. then convert
        assert normalize_tool_name("mcp.get_incident") == "incident.get"
        assert normalize_tool_name("mcp.list_incidents") == "incident.list"


class TestCanonicalMapping:
    """Tests for the canonical mapping dictionaries."""

    def test_underscore_to_canonical_has_required_mappings(self):
        # Platform incident tools
        assert "get_incident" in UNDERSCORE_TO_CANONICAL
        assert "list_incidents" in UNDERSCORE_TO_CANONICAL
        assert "get_evidence" in UNDERSCORE_TO_CANONICAL
        assert "search_evidence" in UNDERSCORE_TO_CANONICAL

        # Observability tools
        assert "query_logs" in UNDERSCORE_TO_CANONICAL
        assert "query_metrics" in UNDERSCORE_TO_CANONICAL
        assert "query_range" in UNDERSCORE_TO_CANONICAL
        assert "query_traces" in UNDERSCORE_TO_CANONICAL

    def test_canonical_to_alias_exists(self):
        # Verify reverse mapping exists for key tools
        assert "incident.get" in CANONICAL_TO_ALIAS
        assert "incident.list" in CANONICAL_TO_ALIAS
        assert "logs.query" in CANONICAL_TO_ALIAS
        assert "metrics.query" in CANONICAL_TO_ALIAS

    def test_mappings_are_consistent(self):
        # Verify mapping consistency: underscore -> canonical -> alias
        for underscore, canonical in UNDERSCORE_TO_CANONICAL.items():
            if underscore.startswith("get_") or underscore.startswith("list_") or underscore.startswith("query_"):
                # Primary aliases should have reverse mapping
                if canonical in CANONICAL_TO_ALIAS:
                    assert CANONICAL_TO_ALIAS[canonical] in UNDERSCORE_TO_CANONICAL


class TestHelperFunctions:
    """Tests for helper functions."""

    def test_get_alias_for_canonical(self):
        assert get_alias_for_canonical("incident.get") == "get_incident"
        assert get_alias_for_canonical("logs.query") == "query_logs"
        assert get_alias_for_canonical("metrics.query") == "query_metrics"
        assert get_alias_for_canonical("unknown.tool") is None

    def test_is_dotted_name(self):
        assert is_dotted_name("incident.get") is True
        assert is_dotted_name("get_incident") is False
        assert is_dotted_name("") is False
        assert is_dotted_name("tool") is False

    def test_get_all_aliases_for_canonical(self):
        aliases = get_all_aliases_for_canonical("incident.get")
        assert "get_incident" in aliases
        assert "get_inc" in aliases

        aliases = get_all_aliases_for_canonical("logs.query")
        assert "query_logs" in aliases

        aliases = get_all_aliases_for_canonical("unknown.tool")
        assert aliases == []


class TestEdgeCases:
    """Tests for edge cases."""

    def test_double_mcp_prefix(self):
        # Only strips one mcp. prefix, remaining name is not mapped
        assert normalize_tool_name("mcp.mcp.get_incident") == "mcp.get_incident"

    def test_mcp_prefix_with_canonical(self):
        # mcp.logs.query -> logs.query
        assert normalize_tool_name("mcp.logs.query") == "logs.query"

    def test_numbers_in_name(self):
        assert normalize_tool_name("tool_v2") == "tool_v2"

    def test_special_characters(self):
        # Dots are preserved for unknown names
        assert normalize_tool_name("my.tool.name") == "my.tool.name"