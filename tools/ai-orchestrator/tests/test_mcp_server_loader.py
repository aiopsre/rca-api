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
    parse_mcpserver_refs,
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
        assert ref.allowed_tools == ("query_metrics", "query_range")
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
        assert refs[0].allowed_tools == ("query_metrics", "query_range", "other_tool")

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
        assert "query_metrics" in tools
        assert "query_range" in tools

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
        tools = runtime._tool_invoker.allowed_tools()
        assert "list_incidents" in tools
        assert "get_incident" in tools
        assert "query_metrics" in tools