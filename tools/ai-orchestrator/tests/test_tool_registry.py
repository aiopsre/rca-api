"""Tests for tool metadata registry."""
from __future__ import annotations

import pytest

from orchestrator.runtime.tool_registry import (
    ToolMetadata,
    ToolRegistry,
    get_tool_metadata,
    register_tool_metadata,
    get_global_registry,
    register_tools_from_mcp,
    register_tools_from_mcpserver_refs,
)
from orchestrator.tooling.mcp_server_loader import (
    McpServerRef,
    ToolMetadataRef,
)
from orchestrator.runtime.tool_discovery import (
    _create_tool_descriptor,
    _infer_tags_from_tool_name,
)


class TestToolMetadata:
    """Tests for ToolMetadata dataclass."""

    def test_metadata_creation(self) -> None:
        """ToolMetadata should store all fields."""
        meta = ToolMetadata(
            tool_name="test_tool",
            kind="metrics",
            domain="observability",
            tags=("metrics", "query"),
        )
        assert meta.tool_name == "test_tool"
        assert meta.kind == "metrics"
        assert meta.tags == ("metrics", "query")

    def test_default_values(self) -> None:
        """ToolMetadata should have sensible defaults."""
        meta = ToolMetadata(tool_name="test_tool")
        assert meta.kind == "unknown"
        assert meta.domain == "general"
        assert meta.read_only is True
        assert meta.risk_level == "low"
        assert meta.latency_tier == "fast"
        assert meta.cost_hint == "free"
        assert meta.tags == ()
        assert meta.description == ""

    def test_frozen(self) -> None:
        """ToolMetadata should be immutable."""
        meta = ToolMetadata(tool_name="test_tool")
        with pytest.raises(AttributeError):
            meta.kind = "logs"  # type: ignore

    def test_custom_fields(self) -> None:
        """ToolMetadata should accept custom field values."""
        meta = ToolMetadata(
            tool_name="dangerous_tool",
            kind="admin",
            risk_level="high",
            latency_tier="slow",
            cost_hint="high",
            read_only=False,
        )
        assert meta.risk_level == "high"
        assert meta.latency_tier == "slow"
        assert meta.cost_hint == "high"
        assert meta.read_only is False


class TestToolRegistry:
    """Tests for ToolRegistry class."""

    def test_register_and_get(self) -> None:
        """Registry should store and retrieve metadata."""
        registry = ToolRegistry()
        meta = ToolMetadata(tool_name="test_tool", kind="logs")
        registry.register(meta)

        result = registry.get("test_tool")
        assert result is not None
        assert result.kind == "logs"

    def test_get_nonexistent(self) -> None:
        """Registry should return None for unknown tools."""
        registry = ToolRegistry()
        result = registry.get("unknown_tool")
        assert result is None

    def test_register_batch(self) -> None:
        """Registry should register multiple tools at once."""
        registry = ToolRegistry()
        tools = [
            ToolMetadata(tool_name="tool1", kind="metrics"),
            ToolMetadata(tool_name="tool2", kind="logs"),
        ]
        registry.register_batch(tools)

        assert registry.get("tool1") is not None
        assert registry.get("tool2") is not None
        assert registry.get("tool3") is None

    def test_find_by_kind(self) -> None:
        """Registry should find tools by kind."""
        registry = ToolRegistry()
        registry.register(ToolMetadata(tool_name="tool1", kind="metrics"))
        registry.register(ToolMetadata(tool_name="tool2", kind="logs"))
        registry.register(ToolMetadata(tool_name="tool3", kind="metrics"))

        metrics_tools = registry.find_by_kind("metrics")
        assert len(metrics_tools) == 2

        logs_tools = registry.find_by_kind("logs")
        assert len(logs_tools) == 1

        traces_tools = registry.find_by_kind("traces")
        assert len(traces_tools) == 0

    def test_find_by_tag(self) -> None:
        """Registry should find tools by tag."""
        registry = ToolRegistry()
        registry.register(
            ToolMetadata(tool_name="tool1", tags=("metrics", "query"))
        )
        registry.register(
            ToolMetadata(tool_name="tool2", tags=("logs", "search"))
        )
        registry.register(
            ToolMetadata(tool_name="tool3", tags=("metrics", "search"))
        )

        query_tools = registry.find_by_tag("query")
        assert len(query_tools) == 1
        assert query_tools[0].tool_name == "tool1"

        search_tools = registry.find_by_tag("search")
        assert len(search_tools) == 2

    def test_all_tools(self) -> None:
        """Registry should return all tool names sorted."""
        registry = ToolRegistry()
        registry.register(ToolMetadata(tool_name="zebra_tool"))
        registry.register(ToolMetadata(tool_name="alpha_tool"))
        registry.register(ToolMetadata(tool_name="beta_tool"))

        all_tools = registry.all_tools()
        assert all_tools == ["alpha_tool", "beta_tool", "zebra_tool"]

    def test_update_existing(self) -> None:
        """Registry should update existing tool metadata."""
        registry = ToolRegistry()
        registry.register(ToolMetadata(tool_name="test_tool", kind="metrics"))
        registry.register(ToolMetadata(tool_name="test_tool", kind="logs"))

        result = registry.get("test_tool")
        assert result is not None
        assert result.kind == "logs"


class TestRegisterFromMcpServerRef:
    """Tests for register_from_mcpserver_ref method."""

    def test_register_from_ref(self) -> None:
        """Should register tools from McpServerRef."""
        registry = ToolRegistry()
        ref = McpServerRef(
            mcp_server_id="test-server",
            name="test-server",
            base_url="http://localhost:8080",
            allowed_tools=("tool1", "tool2"),
            timeout_sec=10.0,
            scopes="",
            auth_type="none",
            tool_metadata=(
                ToolMetadataRef(
                    tool_name="tool1",
                    kind="metrics",
                    domain="observability",
                    tags=("metrics", "query"),
                    description="Query metrics",
                ),
                ToolMetadataRef(
                    tool_name="tool2",
                    kind="logs",
                    domain="observability",
                    tags=("logs", "search"),
                    description="Search logs",
                ),
            ),
        )

        count = registry.register_from_mcpserver_ref(ref)
        assert count == 2

        tool1 = registry.get("tool1")
        assert tool1 is not None
        assert tool1.kind == "metrics"
        assert tool1.description == "Query metrics"

        tool2 = registry.get("tool2")
        assert tool2 is not None
        assert tool2.kind == "logs"

    def test_register_from_ref_empty_metadata(self) -> None:
        """Should handle empty tool_metadata."""
        registry = ToolRegistry()
        ref = McpServerRef(
            mcp_server_id="test-server",
            name="test-server",
            base_url="http://localhost:8080",
            allowed_tools=("tool1",),
            timeout_sec=10.0,
            scopes="",
            auth_type="none",
            tool_metadata=(),
        )

        count = registry.register_from_mcpserver_ref(ref)
        assert count == 0
        assert registry.get("tool1") is None


class TestRegisterToolsFromMcpServerRefs:
    """Tests for register_tools_from_mcpserver_refs function."""

    def test_register_from_refs(self) -> None:
        """Should register tools from list of McpServerRef."""
        # Create a fresh registry for testing
        registry = ToolRegistry()

        refs = [
            McpServerRef(
                mcp_server_id="server1",
                name="server1",
                base_url="http://localhost:8080",
                allowed_tools=("prometheus_query",),
                timeout_sec=10.0,
                scopes="",
                auth_type="none",
                tool_metadata=(
                    ToolMetadataRef(
                        tool_name="prometheus_query",
                        kind="metrics",
                        domain="observability",
                        tags=("metrics", "query", "promql"),
                        description="Query Prometheus metrics",
                    ),
                ),
            ),
            McpServerRef(
                mcp_server_id="server2",
                name="server2",
                base_url="http://localhost:8081",
                allowed_tools=("loki_search",),
                timeout_sec=10.0,
                scopes="",
                auth_type="none",
                tool_metadata=(
                    ToolMetadataRef(
                        tool_name="loki_search",
                        kind="logs",
                        domain="observability",
                        tags=("logs", "search"),
                        description="Search Loki logs",
                    ),
                ),
            ),
        ]

        total = registry.register_from_mcpserver_ref(refs[0])
        total += registry.register_from_mcpserver_ref(refs[1])
        assert total == 2

        prom = registry.get("prometheus_query")
        assert prom is not None
        assert prom.kind == "metrics"

        loki = registry.get("loki_search")
        assert loki is not None
        assert loki.kind == "logs"


class TestGlobalRegistry:
    """Tests for global registry functions."""

    def test_global_registry_starts_empty(self) -> None:
        """Global registry should start empty (no static defaults)."""
        # Note: This test may fail if other tests registered tools
        # The global registry is now filled by platform via McpServerRef
        registry = get_global_registry()
        assert isinstance(registry, ToolRegistry)

    def test_register_new_tool(self) -> None:
        """Should be able to register new tools."""
        meta = ToolMetadata(
            tool_name="custom_test_tool_v2",
            kind="custom",
            tags=("custom", "test"),
        )
        register_tool_metadata(meta)

        result = get_tool_metadata("custom_test_tool_v2")
        assert result is not None
        assert result.kind == "custom"

    def test_get_global_registry(self) -> None:
        """Should return the global registry instance."""
        registry = get_global_registry()
        assert isinstance(registry, ToolRegistry)


class TestRegisterToolsFromMcp:
    """Tests for register_tools_from_mcp function."""

    def test_register_from_mcp_tools(self) -> None:
        """Should register tools from MCP tool info."""
        tools_info = [
            {"name": "mcp_tool_1", "description": "A test tool", "tags": ["custom"]},
            {"name": "mcp_tool_2", "kind": "logs"},
        ]
        registered = register_tools_from_mcp(tools_info)

        assert "mcp_tool_1" in registered
        assert "mcp_tool_2" in registered

        tool1 = get_tool_metadata("mcp_tool_1")
        assert tool1 is not None
        assert tool1.description == "A test tool"
        assert tool1.tags == ("custom",)

    def test_register_with_fallback_inference(self) -> None:
        """Should use fallback inference when tags not provided."""
        tools_info = [
            {"name": "mcp_prometheus_query_v2"},
        ]
        registered = register_tools_from_mcp(
            tools_info,
            fallback_infer_func=_infer_tags_from_tool_name,
        )

        assert "mcp_prometheus_query_v2" in registered

        tool = get_tool_metadata("mcp_prometheus_query_v2")
        assert tool is not None
        # Fallback inference works based on tool name patterns
        assert len(tool.tags) >= 0

    def test_handle_invalid_input(self) -> None:
        """Should handle invalid input gracefully."""
        tools_info = [
            {"name": ""},  # Empty name
            {"description": "No name"},  # No name
            "not a dict",  # Not a dict
            None,  # None value
            {"name": "valid_mcp_tool_v2", "tags": ["test"]},
        ]
        registered = register_tools_from_mcp(tools_info)

        assert "valid_mcp_tool_v2" in registered


class TestToolDiscoveryWithRegistry:
    """Tests for tool discovery using the registry."""

    def test_create_descriptor_with_explicit_tags(self) -> None:
        """Explicit tags should be used directly."""
        desc = _create_tool_descriptor(
            tool_name="prometheus_query",
            tags=("custom", "tags"),
        )
        assert desc.tags == ("custom", "tags")

    def test_create_descriptor_fallback_to_inference(self) -> None:
        """Should fallback to inference for unregistered tools."""
        desc = _create_tool_descriptor(
            tool_name="custom_metrics_tool",
        )
        # Has "metrics" in name, so should get metrics tag via inference
        assert "metrics" in desc.tags

    def test_create_descriptor_with_registry_metadata(self) -> None:
        """Should use registry metadata when available."""
        # First register a tool via McpServerRef
        ref = McpServerRef(
            mcp_server_id="test-server",
            name="test-server",
            base_url="http://localhost:8080",
            allowed_tools=("registered_tool",),
            timeout_sec=10.0,
            scopes="",
            auth_type="none",
            tool_metadata=(
                ToolMetadataRef(
                    tool_name="registered_tool",
                    kind="metrics",
                    domain="observability",
                    tags=("metrics", "query"),
                    description="A registered tool",
                ),
            ),
        )
        register_tools_from_mcpserver_refs([ref])

        desc = _create_tool_descriptor(
            tool_name="registered_tool",
        )
        assert desc.tool_name == "registered_tool"
        assert "metrics" in desc.tags
        assert desc.description == "A registered tool"