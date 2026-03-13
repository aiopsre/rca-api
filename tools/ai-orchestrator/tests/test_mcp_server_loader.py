"""Tests for MCP server loader and invoker builder functions."""

from __future__ import annotations

import pytest

from orchestrator.tooling import (
    ToolInvoker,
    ToolInvokerChain,
)
from orchestrator.tooling.mcp_server_loader import (
    ToolMetadataRef,
    parse_resolved_tool_providers,
)


class TestResolvedToolProviderParsing:
    """Tests for parse_resolved_tool_providers function."""

    def test_parse_empty_list_returns_empty(self):
        """Empty list returns empty list."""
        result = parse_resolved_tool_providers([])
        assert result == []

    def test_parse_non_list_returns_empty(self):
        """Non-list input returns empty list."""
        result = parse_resolved_tool_providers("not a list")
        assert result == []
        result = parse_resolved_tool_providers({"key": "value"})
        assert result == []

    def test_parse_external_provider_with_base_url(self):
        """External provider with base_url is parsed correctly."""
        providers = [{
            "providerID": "prometheus-1",
            "mcpServerID": "prometheus-server",
            "name": "Prometheus",
            "providerType": "mcp_http",
            "serverKind": "external",
            "baseURL": "https://prometheus.example.com",
            "allowedTools": ["metrics.query", "metrics.query_range"],
            "priority": 10,
        }]
        result = parse_resolved_tool_providers(providers)
        assert len(result) == 1
        assert result[0].provider_id == "prometheus-1"
        assert result[0].server_kind == "external"
        assert result[0].base_url == "https://prometheus.example.com"
        assert result[0].allowed_tools == ("metrics.query", "metrics.query_range")

    def test_parse_builtin_provider_without_base_url(self):
        """Builtin provider can have empty base_url."""
        providers = [{
            "providerID": "builtin-readonly",
            "mcpServerID": "rca-api-builtin-readonly",
            "name": "RCA API Builtin Readonly",
            "providerType": "builtin",
            "serverKind": "builtin",
            "baseURL": "",  # Empty for builtin
            "allowedTools": ["incident.get", "evidence.search"],
            "priority": 0,
        }]
        result = parse_resolved_tool_providers(providers)
        assert len(result) == 1
        assert result[0].provider_id == "builtin-readonly"
        assert result[0].server_kind == "builtin"
        assert result[0].base_url == ""
        assert result[0].provider_type == "builtin"

    def test_parse_external_provider_without_base_url_is_skipped(self):
        """External provider without base_url is skipped."""
        providers = [{
            "providerID": "broken-provider",
            "name": "Broken Provider",
            "providerType": "mcp_http",
            "serverKind": "external",
            "baseURL": "",  # Missing for external
            "allowedTools": ["some.tool"],
        }]
        result = parse_resolved_tool_providers(providers)
        assert len(result) == 0

    def test_parse_mixed_builtin_and_external(self):
        """Parse both builtin and external providers."""
        providers = [
            {
                "providerID": "builtin-readonly",
                "name": "Builtin",
                "providerType": "builtin",
                "serverKind": "builtin",
                "baseURL": "",
                "allowedTools": ["incident.get"],
                "priority": 0,
            },
            {
                "providerID": "prometheus-1",
                "name": "Prometheus",
                "providerType": "mcp_http",
                "serverKind": "external",
                "baseURL": "https://prometheus.example.com",
                "allowedTools": ["metrics.query"],
                "priority": 10,
            },
        ]
        result = parse_resolved_tool_providers(providers)
        assert len(result) == 2
        # Verify both are parsed correctly
        assert result[0].server_kind == "builtin"
        assert result[1].server_kind == "external"


class TestBuildToolInvokerFromResolvedProviders:
    """Tests for build_tool_invoker_from_resolved_providers in invoker.py.

    These tests verify that builtin providers use platform_base_url when
    they have empty base_url.
    """

    def test_only_external_providers_creates_invoker(self):
        """External providers result in a valid ToolInvoker."""
        from orchestrator.tooling.invoker import build_tool_invoker_from_resolved_providers

        providers = [{
            "providerID": "prometheus-1",
            "name": "Prometheus",
            "providerType": "mcp_http",
            "serverKind": "external",
            "baseURL": "https://prometheus.example.com",
            "allowedTools": ["metrics.query"],
            "priority": 10,
        }]
        invoker = build_tool_invoker_from_resolved_providers(providers)
        assert invoker is not None
        assert "metrics.query" in invoker.allowed_tools()

    def test_only_builtin_providers_with_platform_url_creates_invoker(self):
        """Builtin providers with platform_base_url create valid invoker."""
        from orchestrator.tooling.invoker import build_tool_invoker_from_resolved_providers

        providers = [{
            "providerID": "builtin-readonly",
            "name": "Builtin Readonly",
            "providerType": "builtin",
            "serverKind": "builtin",
            "baseURL": "",  # Empty for builtin, will use platform_base_url
            "allowedTools": ["incident.get", "evidence.search"],
            "priority": 0,
        }]
        # With platform_base_url, builtin providers should be included
        invoker = build_tool_invoker_from_resolved_providers(
            providers,
            platform_base_url="https://rca-api.example.com",
        )
        assert invoker is not None
        assert "incident.get" in invoker.allowed_tools()
        assert "evidence.search" in invoker.allowed_tools()

    def test_only_builtin_providers_without_platform_url_returns_none(self):
        """Builtin providers without platform_base_url return None."""
        from orchestrator.tooling.invoker import build_tool_invoker_from_resolved_providers

        providers = [{
            "providerID": "builtin-readonly",
            "name": "Builtin Readonly",
            "providerType": "builtin",
            "serverKind": "builtin",
            "baseURL": "",  # Empty and no platform_base_url
            "allowedTools": ["incident.get"],
            "priority": 0,
        }]
        # Without platform_base_url, builtin providers with empty base_url are skipped
        invoker = build_tool_invoker_from_resolved_providers(providers)
        assert invoker is None

    def test_mixed_providers_with_platform_url_includes_both(self):
        """Mixed providers with platform_base_url include both builtin and external."""
        from orchestrator.tooling.invoker import build_tool_invoker_from_resolved_providers

        providers = [
            {
                "providerID": "builtin-readonly",
                "name": "Builtin Readonly",
                "providerType": "builtin",
                "serverKind": "builtin",
                "baseURL": "",  # Will use platform_base_url
                "allowedTools": ["incident.get"],
                "priority": 0,
            },
            {
                "providerID": "prometheus-1",
                "name": "Prometheus",
                "providerType": "mcp_http",
                "serverKind": "external",
                "baseURL": "https://prometheus.example.com",
                "allowedTools": ["metrics.query"],
                "priority": 10,
            },
        ]
        invoker = build_tool_invoker_from_resolved_providers(
            providers,
            platform_base_url="https://rca-api.example.com",
        )
        assert invoker is not None
        # Both builtin and external tools should be present
        assert "incident.get" in invoker.allowed_tools()
        assert "metrics.query" in invoker.allowed_tools()

    def test_builtin_provider_with_own_base_url_uses_it(self):
        """Builtin provider with non-empty base_url uses its own URL."""
        from orchestrator.tooling.invoker import build_tool_invoker_from_resolved_providers

        providers = [{
            "providerID": "builtin-readonly",
            "name": "Builtin Readonly",
            "providerType": "builtin",
            "serverKind": "builtin",
            "baseURL": "https://custom-rca-api.example.com",  # Has its own URL
            "allowedTools": ["incident.get"],
            "priority": 0,
        }]
        invoker = build_tool_invoker_from_resolved_providers(providers)
        assert invoker is not None
        assert "incident.get" in invoker.allowed_tools()

    def test_empty_providers_returns_none(self):
        """Empty providers list returns None."""
        from orchestrator.tooling.invoker import build_tool_invoker_from_resolved_providers

        invoker = build_tool_invoker_from_resolved_providers([])
        assert invoker is None


class TestToolMetadataSurfaceFields:
    """Tests for preserving surface-specific metadata in fallback path."""

    def test_parse_tool_metadata_preserves_tool_class(self):
        """Tool class is correctly parsed from metadata."""
        from orchestrator.tooling.mcp_server_loader import _parse_tool_metadata_list

        raw = [
            {
                "tool_name": "incident.get",
                "kind": "incident",
                "tool_class": "fc_selectable",
            },
            {
                "tool_name": "session.patch",
                "kind": "session",
                "tool_class": "runtime_owned",
            },
        ]

        result = _parse_tool_metadata_list(raw)
        assert len(result) == 2
        assert result[0].tool_class == "fc_selectable"
        assert result[1].tool_class == "runtime_owned"

    def test_parse_tool_metadata_preserves_surface_visibility(self):
        """Surface visibility flags are correctly parsed from metadata."""
        from orchestrator.tooling.mcp_server_loader import _parse_tool_metadata_list

        raw = [
            {
                "tool_name": "skills.only",
                "tool_class": "fc_selectable",
                "allowed_for_prompt_skill": True,
                "allowed_for_graph_agent": False,
            },
            {
                "tool_name": "graph.only",
                "tool_class": "fc_selectable",
                "allowed_for_prompt_skill": False,
                "allowed_for_graph_agent": True,
            },
            {
                "tool_name": "hidden.tool",
                "tool_class": "fc_selectable",
                "allowed_for_prompt_skill": False,
                "allowed_for_graph_agent": False,
            },
        ]

        result = _parse_tool_metadata_list(raw)
        assert len(result) == 3

        # skills.only
        assert result[0].allowed_for_prompt_skill is True
        assert result[0].allowed_for_graph_agent is False

        # graph.only
        assert result[1].allowed_for_prompt_skill is False
        assert result[1].allowed_for_graph_agent is True

        # hidden.tool
        assert result[2].allowed_for_prompt_skill is False
        assert result[2].allowed_for_graph_agent is False

    def test_parse_tool_metadata_defaults_missing_fields(self):
        """Missing fields default to fc_selectable/True for backward compatibility."""
        from orchestrator.tooling.mcp_server_loader import _parse_tool_metadata_list

        raw = [
            {
                "tool_name": "legacy.tool",
                "kind": "unknown",
                # No tool_class or surface fields
            },
        ]

        result = _parse_tool_metadata_list(raw)
        assert len(result) == 1
        assert result[0].tool_class == "fc_selectable"
        assert result[0].allowed_for_prompt_skill is True
        assert result[0].allowed_for_graph_agent is True

    def test_parse_tool_metadata_with_camel_case(self):
        """Camel case field names (from Go JSON) are correctly parsed."""
        from orchestrator.tooling.mcp_server_loader import _parse_tool_metadata_list

        raw = [
            {
                "toolName": "incident.get",
                "toolClass": "fc_selectable",
                "allowedForPromptSkill": False,
                "allowedForGraphAgent": True,
            },
        ]

        result = _parse_tool_metadata_list(raw)
        assert len(result) == 1
        assert result[0].tool_class == "fc_selectable"
        assert result[0].allowed_for_prompt_skill is False
        assert result[0].allowed_for_graph_agent is True

    def test_resolved_providers_metadata_preserves_surface_fields(self):
        """Tool metadata from resolved providers preserves surface fields."""
        providers = [
            {
                "providerID": "builtin-readonly",
                "mcpServerID": "rca-api-builtin-readonly",
                "name": "Builtin Readonly",
                "providerType": "builtin",
                "serverKind": "builtin",
                "baseURL": "",
                "allowedTools": ["incident.get", "session.patch"],
                "toolMetadata": [
                    {
                        "tool_name": "incident.get",
                        "tool_class": "fc_selectable",
                        "allowed_for_prompt_skill": True,
                        "allowed_for_graph_agent": True,
                    },
                    {
                        "tool_name": "session.patch",
                        "tool_class": "runtime_owned",
                        "allowed_for_prompt_skill": False,
                        "allowed_for_graph_agent": False,
                    },
                ],
                "priority": 0,
            },
        ]

        result = parse_resolved_tool_providers(providers)
        assert len(result) == 1
        assert len(result[0].tool_metadata) == 2

        # incident.get should be fc_selectable
        incident_meta = result[0].tool_metadata[0]
        assert incident_meta.tool_name == "incident.get"
        assert incident_meta.tool_class == "fc_selectable"
        assert incident_meta.allowed_for_prompt_skill is True
        assert incident_meta.allowed_for_graph_agent is True

        # session.patch should be runtime_owned
        session_meta = result[0].tool_metadata[1]
        assert session_meta.tool_name == "session.patch"
        assert session_meta.tool_class == "runtime_owned"
        assert session_meta.allowed_for_prompt_skill is False
        assert session_meta.allowed_for_graph_agent is False