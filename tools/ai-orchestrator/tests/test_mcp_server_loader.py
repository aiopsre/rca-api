"""Tests for MCP server loader and invoker builder functions."""

from __future__ import annotations

import pytest

from orchestrator.tooling import (
    ToolInvoker,
    ToolInvokerChain,
    build_tool_invoker_from_mcpserver_refs_json,
)
from orchestrator.tooling.mcp_server_loader import (
    McpServerRef,
    ToolMetadataRef,
    parse_mcpserver_refs,
    parse_resolved_tool_providers,
    build_provider_configs_from_mcpserver_refs,
)


class TestMcpServerRefParsing:
    """Tests for parsing McpServerRef from JSON."""

    def test_parse_empty_string_returns_empty_list(self):
        assert parse_mcpserver_refs("") == []
        assert parse_mcpserver_refs("  ") == []

    def test_parse_invalid_json_returns_empty_list(self):
        assert parse_mcpserver_refs("not json") == []
        assert parse_mcpserver_refs("{") == []

    def test_parse_non_list_returns_empty_list(self):
        assert parse_mcpserver_refs("{}") == []
        assert parse_mcpserver_refs('"string"') == []

    def test_parse_single_ref(self):
        json_str = '''[{
            "mcp_server_id": "ms-001",
            "name": "prometheus",
            "base_url": "http://prometheus.mcp:8080",
            "allowed_tools": ["query_metrics", "query_range"],
            "timeout_sec": 30,
            "scopes": "read:metrics",
            "auth_type": "bearer"
        }]'''
        refs = parse_mcpserver_refs(json_str)
        assert len(refs) == 1
        ref = refs[0]
        assert ref.mcp_server_id == "ms-001"
        assert ref.name == "prometheus"
        assert ref.base_url == "http://prometheus.mcp:8080"
        # Tools are normalized to canonical dotted names
        assert ref.allowed_tools == ("metrics.query", "metrics.query_range")
        assert ref.timeout_sec == 30.0
        assert ref.scopes == "read:metrics"
        assert ref.auth_type == "bearer"

    def test_parse_multiple_refs(self):
        json_str = '''[
            {"name": "prometheus", "base_url": "http://prometheus:8080", "allowed_tools": ["query_metrics"]},
            {"name": "loki", "base_url": "http://loki:8080", "allowed_tools": ["query_logs"]}
        ]'''
        refs = parse_mcpserver_refs(json_str)
        assert len(refs) == 2
        assert refs[0].name == "prometheus"
        assert refs[1].name == "loki"

    def test_parse_strips_mcp_prefix_from_tools(self):
        json_str = '''[{
            "name": "prometheus",
            "base_url": "http://prometheus:8080",
            "allowed_tools": ["mcp.query_metrics", "mcp.query_range", "other_tool"]
        }]'''
        refs = parse_mcpserver_refs(json_str)
        # Tools are normalized to canonical dotted names
        assert refs[0].allowed_tools == ("metrics.query", "metrics.query_range", "other_tool")

    def test_parse_skips_ref_missing_name(self):
        json_str = '''[{"base_url": "http://test", "allowed_tools": ["tool1"]}]'''
        refs = parse_mcpserver_refs(json_str)
        assert refs == []

    def test_parse_skips_ref_missing_base_url(self):
        json_str = '''[{"name": "test", "allowed_tools": ["tool1"]}]'''
        refs = parse_mcpserver_refs(json_str)
        assert refs == []

    def test_parse_skips_ref_missing_allowed_tools(self):
        json_str = '''[{"name": "test", "base_url": "http://test"}]'''
        refs = parse_mcpserver_refs(json_str)
        assert refs == []

    def test_parse_camelcase_field_compatibility(self):
        json_str = '''[{
            "mcpServerID": "ms-001",
            "name": "test",
            "baseURL": "http://test:8080",
            "allowedTools": ["tool1"],
            "timeoutSec": 15
        }]'''
        refs = parse_mcpserver_refs(json_str)
        assert len(refs) == 1
        assert refs[0].mcp_server_id == "ms-001"
        assert refs[0].base_url == "http://test:8080"
        assert refs[0].timeout_sec == 15.0

    def test_parse_default_values(self):
        json_str = '''[{
            "name": "test",
            "base_url": "http://test:8080",
            "allowed_tools": ["tool1"]
        }]'''
        refs = parse_mcpserver_refs(json_str)
        assert refs[0].timeout_sec == 10.0  # default
        assert refs[0].auth_type == "none"  # default
        assert refs[0].scopes == ""  # default
        assert refs[0].mcp_server_id == ""  # optional


class TestBuildProviderConfigs:
    """Tests for converting McpServerRef to ProviderConfig."""

    def test_build_provider_configs_empty_list(self):
        configs = build_provider_configs_from_mcpserver_refs([])
        assert configs == []

    def test_build_provider_configs_single_ref(self):
        refs = [
            McpServerRef(
                mcp_server_id="ms-001",
                name="prometheus",
                base_url="http://prometheus:8080",
                allowed_tools=("query_metrics", "query_range"),
                timeout_sec=30.0,
                scopes="read:metrics",
                auth_type="bearer",
            )
        ]
        configs = build_provider_configs_from_mcpserver_refs(refs)
        assert len(configs) == 1
        cfg = configs[0]
        assert cfg.provider_type == "mcp_http"
        assert cfg.name == "prometheus"
        assert cfg.base_url == "http://prometheus:8080"
        assert cfg.allow_tools == ("query_metrics", "query_range")
        assert cfg.timeout_s == 30.0
        assert cfg.scopes == "read:metrics"


class TestBuildToolInvokerFromMcpServerRefsJson:
    """Tests for building ToolInvoker from JSON."""

    def test_empty_json_returns_none(self):
        assert build_tool_invoker_from_mcpserver_refs_json("") is None
        assert build_tool_invoker_from_mcpserver_refs_json("[]") is None

    def test_invalid_json_returns_none(self):
        assert build_tool_invoker_from_mcpserver_refs_json("not json") is None

    def test_single_mcp_server_builds_invoker(self):
        json_str = '''[{
            "name": "prometheus",
            "base_url": "http://prometheus:8080",
            "allowed_tools": ["query_metrics", "query_range"]
        }]'''
        invoker = build_tool_invoker_from_mcpserver_refs_json(json_str)
        assert invoker is not None
        assert isinstance(invoker, ToolInvoker)
        assert invoker.toolset_id == "mcp_servers"
        tools = invoker.allowed_tools()
        # Tools are normalized to canonical dotted names
        assert "metrics.query" in tools
        assert "metrics.query_range" in tools

    def test_custom_toolset_id(self):
        json_str = '''[{
            "name": "prometheus",
            "base_url": "http://prometheus:8080",
            "allowed_tools": ["query_metrics"]
        }]'''
        invoker = build_tool_invoker_from_mcpserver_refs_json(
            json_str, toolset_id="custom_mcp"
        )
        assert invoker is not None
        assert invoker.toolset_id == "custom_mcp"


class TestRuntimeMergeToolInvoker:
    """Tests for OrchestratorRuntime.merge_tool_invoker method."""

    def test_merge_tool_invoker_with_none_does_nothing(self):
        from orchestrator.runtime.runtime import OrchestratorRuntime
        from orchestrator.tools_rca_api import RCAApiClient

        client = RCAApiClient("http://test", "")
        runtime = OrchestratorRuntime(
            client=client,
            job_id="job-001",
            instance_id="inst-001",
            heartbeat_interval_seconds=30,
        )
        original_invoker = runtime._tool_invoker
        runtime.merge_tool_invoker(None)
        assert runtime._tool_invoker is original_invoker

    def test_merge_tool_invoker_sets_when_none(self):
        from orchestrator.runtime.runtime import OrchestratorRuntime
        from orchestrator.tools_rca_api import RCAApiClient

        client = RCAApiClient("http://test", "")
        runtime = OrchestratorRuntime(
            client=client,
            job_id="job-001",
            instance_id="inst-001",
            heartbeat_interval_seconds=30,
        )
        # Initially no tool invoker
        assert runtime._tool_invoker is None

        # Merge one in
        json_str = '''[{
            "name": "prometheus",
            "base_url": "http://prometheus:8080",
            "allowed_tools": ["query_metrics"]
        }]'''
        mcp_invoker = build_tool_invoker_from_mcpserver_refs_json(json_str)
        runtime.merge_tool_invoker(mcp_invoker)

        # Now we have the MCP invoker
        assert runtime._tool_invoker is mcp_invoker

    def test_merge_tool_invoker_creates_chain(self):
        from orchestrator.runtime.runtime import OrchestratorRuntime
        from orchestrator.tools_rca_api import RCAApiClient
        from orchestrator.tooling import load_toolset_config, build_tool_invoker

        # Create a primary invoker
        config = load_toolset_config({
            "pipelines": {"basic_rca": "primary"},
            "toolsets": {
                "primary": {
                    "providers": [{
                        "type": "mcp_http",
                        "name": "platform",
                        "base_url": "http://platform:8080",
                        "allow_tools": ["list_incidents", "get_incident"],
                    }]
                }
            }
        })
        primary_invoker = build_tool_invoker(config, "primary")

        client = RCAApiClient("http://test", "")
        runtime = OrchestratorRuntime(
            client=client,
            job_id="job-001",
            instance_id="inst-001",
            heartbeat_interval_seconds=30,
            tool_invoker=primary_invoker,
        )

        # Merge MCP server invoker
        json_str = '''[{
            "name": "prometheus",
            "base_url": "http://prometheus:8080",
            "allowed_tools": ["query_metrics"]
        }]'''
        mcp_invoker = build_tool_invoker_from_mcpserver_refs_json(json_str)
        runtime.merge_tool_invoker(mcp_invoker)

        # Now we have a chain
        assert isinstance(runtime._tool_invoker, ToolInvokerChain)
        assert runtime._tool_invoker.toolset_ids == ["primary", "mcp_servers"]

        # All tools are available through the chain
        # Tools are normalized to canonical dotted names
        tools = runtime._tool_invoker.allowed_tools()
        assert "incident.list" in tools
        assert "incident.get" in tools
        assert "metrics.query" in tools


class TestToolMetadataParsing:
    """Tests for parsing tool_metadata from McpServerRef."""

    def test_parse_tool_metadata(self):
        json_str = '''[{
            "name": "prometheus",
            "base_url": "http://prometheus:8080",
            "allowed_tools": ["query_metrics", "query_range"],
            "tool_metadata": [
                {
                    "tool_name": "query_metrics",
                    "kind": "metrics",
                    "domain": "observability",
                    "read_only": true,
                    "risk_level": "low",
                    "latency_tier": "fast",
                    "cost_hint": "free",
                    "tags": ["metrics", "query"],
                    "description": "Query Prometheus metrics"
                },
                {
                    "tool_name": "query_range",
                    "kind": "metrics",
                    "domain": "observability",
                    "tags": ["metrics", "query", "range"],
                    "description": "Query Prometheus metrics over a time range"
                }
            ]
        }]'''
        refs = parse_mcpserver_refs(json_str)
        assert len(refs) == 1
        ref = refs[0]

        assert len(ref.tool_metadata) == 2

        # tool_name is normalized to canonical dotted form
        meta1 = ref.tool_metadata[0]
        assert meta1.tool_name == "metrics.query"
        assert meta1.kind == "metrics"
        assert meta1.domain == "observability"
        assert meta1.read_only is True
        assert meta1.risk_level == "low"
        assert meta1.latency_tier == "fast"
        assert meta1.cost_hint == "free"
        assert meta1.tags == ("metrics", "query")
        assert meta1.description == "Query Prometheus metrics"

        meta2 = ref.tool_metadata[1]
        assert meta2.tool_name == "metrics.query_range"
        assert meta2.kind == "metrics"
        assert meta2.tags == ("metrics", "query", "range")

    def test_parse_empty_tool_metadata(self):
        json_str = '''[{
            "name": "prometheus",
            "base_url": "http://prometheus:8080",
            "allowed_tools": ["query_metrics"]
        }]'''
        refs = parse_mcpserver_refs(json_str)
        assert len(refs) == 1
        assert refs[0].tool_metadata == ()

    def test_parse_tool_metadata_camel_case(self):
        json_str = '''[{
            "name": "prometheus",
            "base_url": "http://prometheus:8080",
            "allowed_tools": ["query_metrics"],
            "toolMetadata": [
                {
                    "tool_name": "query_metrics",
                    "kind": "metrics"
                }
            ]
        }]'''
        refs = parse_mcpserver_refs(json_str)
        assert len(refs) == 1
        assert len(refs[0].tool_metadata) == 1
        # tool_name is normalized to canonical dotted form
        assert refs[0].tool_metadata[0].tool_name == "metrics.query"

    def test_parse_tool_metadata_default_values(self):
        json_str = '''[{
            "name": "prometheus",
            "base_url": "http://prometheus:8080",
            "allowed_tools": ["query_metrics"],
            "tool_metadata": [
                {"tool_name": "query_metrics"}
            ]
        }]'''
        refs = parse_mcpserver_refs(json_str)
        meta = refs[0].tool_metadata[0]

        # tool_name is normalized to canonical dotted form
        assert meta.tool_name == "metrics.query"
        assert meta.kind == "unknown"
        assert meta.domain == "general"
        assert meta.read_only is True
        assert meta.risk_level == "low"
        assert meta.latency_tier == "fast"
        assert meta.cost_hint == "free"
        assert meta.tags == ()
        assert meta.description == ""

    def test_parse_tool_metadata_skips_invalid_entries(self):
        json_str = '''[{
            "name": "prometheus",
            "base_url": "http://prometheus:8080",
            "allowed_tools": ["query_metrics"],
            "tool_metadata": [
                {"tool_name": "valid_tool", "kind": "metrics"},
                {"kind": "logs"},
                "not a dict",
                null,
                {"tool_name": "", "kind": "traces"},
                {"tool_name": "another_valid", "kind": "traces"}
            ]
        }]'''
        refs = parse_mcpserver_refs(json_str)
        assert len(refs[0].tool_metadata) == 2
        assert refs[0].tool_metadata[0].tool_name == "valid_tool"
        assert refs[0].tool_metadata[1].tool_name == "another_valid"

    def test_tool_metadata_ref_dataclass(self):
        """Test ToolMetadataRef dataclass directly."""
        meta = ToolMetadataRef(
            tool_name="test_tool",
            kind="custom",
            domain="test_domain",
            read_only=False,
            risk_level="high",
            latency_tier="slow",
            cost_hint="medium",
            tags=("tag1", "tag2"),
            description="Test description",
        )
        assert meta.tool_name == "test_tool"
        assert meta.kind == "custom"
        assert meta.domain == "test_domain"
        assert meta.read_only is False
        assert meta.risk_level == "high"
        assert meta.latency_tier == "slow"
        assert meta.cost_hint == "medium"
        assert meta.tags == ("tag1", "tag2")
        assert meta.description == "Test description"


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