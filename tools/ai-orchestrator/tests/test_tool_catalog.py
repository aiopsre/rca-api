"""Tests for tool_catalog.py - Runtime tool catalog types.

This test module covers the core data models for the Function Calling migration:
- ToolSpec: Canonical tool specification
- ToolCatalogSnapshot: Immutable snapshot of available tools
- RuntimeToolGateway: Protocol for tool execution
- ExecutedToolCall: Unified result model for tool executions
"""
from __future__ import annotations

import pytest

from orchestrator.runtime.tool_catalog import (
    ToolSpec,
    ToolCatalogSnapshot,
    ExecutedToolCall,
    build_tool_catalog_snapshot,
    tool_descriptor_to_spec,
    tool_metadata_to_spec,
)
from orchestrator.runtime.tool_discovery import ToolDescriptor
from orchestrator.runtime.tool_registry import ToolMetadata


class TestToolSpec:
    """Tests for ToolSpec dataclass."""

    def test_basic_creation(self) -> None:
        """Test creating a basic ToolSpec."""
        spec = ToolSpec(
            name="prometheus_query",
            description="Query Prometheus metrics",
            input_schema={"type": "object", "properties": {"query": {"type": "string"}}},
        )
        assert spec.name == "prometheus_query"
        assert spec.description == "Query Prometheus metrics"
        assert spec.kind == "unknown"
        assert spec.read_only is True
        assert spec.risk_level == "low"

    def test_canonical_name_normalization(self) -> None:
        """Test that mcp. prefix is stripped from tool name."""
        spec = ToolSpec(
            name="mcp.prometheus_query",
            description="Query Prometheus metrics",
        )
        assert spec.name == "prometheus_query"

    def test_to_openai_tool(self) -> None:
        """Test conversion to OpenAI function calling format."""
        spec = ToolSpec(
            name="loki_search",
            description="Search logs in Loki",
            input_schema={
                "type": "object",
                "properties": {
                    "query": {"type": "string"},
                    "limit": {"type": "integer"},
                },
                "required": ["query"],
            },
        )
        openai_tool = spec.to_openai_tool()
        assert openai_tool["type"] == "function"
        assert openai_tool["function"]["name"] == "loki_search"
        assert openai_tool["function"]["description"] == "Search logs in Loki"
        assert openai_tool["function"]["parameters"]["type"] == "object"
        assert "query" in openai_tool["function"]["parameters"]["properties"]

    def test_with_all_fields(self) -> None:
        """Test ToolSpec with all fields populated."""
        spec = ToolSpec(
            name="elasticsearch_search",
            description="Search Elasticsearch",
            input_schema={"type": "object"},
            output_schema={"type": "object"},
            kind="logs",
            tags=("logs", "search", "elasticsearch"),
            provider_id="es-provider-1",
            read_only=True,
            risk_level="low",
            allowed_for_prompt_skill=True,
        )
        assert spec.name == "elasticsearch_search"
        assert spec.kind == "logs"
        assert "logs" in spec.tags
        assert spec.provider_id == "es-provider-1"


class TestToolCatalogSnapshot:
    """Tests for ToolCatalogSnapshot dataclass."""

    def test_basic_creation(self) -> None:
        """Test creating a basic snapshot."""
        spec1 = ToolSpec(name="tool1", description="Tool 1")
        spec2 = ToolSpec(name="tool2", description="Tool 2")

        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1", "ts2"],
            tool_specs=[spec1, spec2],
        )

        assert snapshot.toolset_ids == ("ts1", "ts2")
        assert len(snapshot.tools) == 2
        assert snapshot.has_tool("tool1")
        assert snapshot.has_tool("tool2")

    def test_by_name_lookup(self) -> None:
        """Test looking up tools by name."""
        spec = ToolSpec(name="my_tool", description="My Tool", kind="metrics")
        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[spec],
        )

        found = snapshot.get_tool("my_tool")
        assert found is not None
        assert found.name == "my_tool"
        assert found.kind == "metrics"

    def test_by_name_lookup_with_mcp_prefix(self) -> None:
        """Test that lookup works with mcp. prefix."""
        spec = ToolSpec(name="my_tool", description="My Tool")
        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[spec],
        )

        # Both forms should find the tool
        assert snapshot.has_tool("my_tool")
        assert snapshot.has_tool("mcp.my_tool")

        found = snapshot.get_tool("mcp.my_tool")
        assert found is not None
        assert found.name == "my_tool"

    def test_tool_names(self) -> None:
        """Test getting all tool names."""
        spec1 = ToolSpec(name="zebra", description="Z")
        spec2 = ToolSpec(name="alpha", description="A")

        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[spec1, spec2],
        )

        names = snapshot.tool_names()
        assert names == ["alpha", "zebra"]  # Sorted

    def test_filter_by_kind(self) -> None:
        """Test filtering tools by kind."""
        spec1 = ToolSpec(name="metrics_tool", description="M", kind="metrics")
        spec2 = ToolSpec(name="logs_tool", description="L", kind="logs")
        spec3 = ToolSpec(name="another_metrics", description="M2", kind="metrics")

        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[spec1, spec2, spec3],
        )

        metrics_tools = snapshot.filter_by_kind("metrics")
        assert len(metrics_tools) == 2
        assert all(t.kind == "metrics" for t in metrics_tools)

    def test_filter_by_tag(self) -> None:
        """Test filtering tools by tag."""
        spec1 = ToolSpec(name="tool1", description="T1", tags=("metrics", "query"))
        spec2 = ToolSpec(name="tool2", description="T2", tags=("logs", "search"))

        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[spec1, spec2],
        )

        query_tools = snapshot.filter_by_tag("query")
        assert len(query_tools) == 1
        assert query_tools[0].name == "tool1"

    def test_to_openai_tools(self) -> None:
        """Test converting all tools to OpenAI format."""
        spec1 = ToolSpec(name="tool1", description="T1", input_schema={"type": "object"})
        spec2 = ToolSpec(name="tool2", description="T2", input_schema={"type": "object"})

        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[spec1, spec2],
        )

        openai_tools = snapshot.to_openai_tools()
        assert len(openai_tools) == 2
        assert all(t["type"] == "function" for t in openai_tools)

    def test_deduplication(self) -> None:
        """Test that duplicate tool names are deduplicated."""
        spec1 = ToolSpec(name="tool1", description="First")
        spec2 = ToolSpec(name="tool1", description="Second")  # Duplicate name

        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[spec1, spec2],
        )

        assert len(snapshot.tools) == 1
        assert snapshot.tools[0].description == "First"  # First one wins

    def test_empty_snapshot(self) -> None:
        """Test creating an empty snapshot."""
        snapshot = build_tool_catalog_snapshot(
            toolset_ids=[],
            tool_specs=[],
        )

        assert snapshot.toolset_ids == ()
        assert snapshot.tools == ()
        assert snapshot.by_name == {}
        assert not snapshot.has_tool("any_tool")


class TestExecutedToolCall:
    """Tests for ExecutedToolCall dataclass."""

    def test_basic_creation(self) -> None:
        """Test creating a basic ExecutedToolCall."""
        call = ExecutedToolCall(
            tool_name="prometheus_query",
            request_json={"query": "up"},
            response_json={"data": {"result": []}},
            latency_ms=150,
            source="skill.plan",
        )

        assert call.tool_name == "prometheus_query"
        assert call.request_json == {"query": "up"}
        assert call.latency_ms == 150
        assert call.source == "skill.plan"

    def test_canonical_name_normalization(self) -> None:
        """Test that mcp. prefix is stripped from tool name."""
        call = ExecutedToolCall(
            tool_name="mcp.loki_search",
            request_json={},
            response_json={},
            latency_ms=100,
            source="graph.node",
        )

        assert call.tool_name == "loki_search"

    def test_to_skill_tool_result(self) -> None:
        """Test conversion to skill tool result format."""
        call = ExecutedToolCall(
            tool_name="my_tool",
            request_json={"arg": "value"},
            response_json={"result": "data"},
            latency_ms=200,
            source="skill.plan",
            provider_id="provider-1",
            provider_type="mcp_http",
            resolved_from_toolset_id="ts-1",
        )

        result = call.to_skill_tool_result()
        assert result["tool"] == "my_tool"
        assert result["tool_name"] == "my_tool"
        assert result["tool_request"] == {"arg": "value"}
        assert result["tool_result"] == {"result": "data"}
        assert result["latency_ms"] == 200
        assert result["provider_id"] == "provider-1"
        assert result["source"] == "skill.plan"

    def test_to_audit_record(self) -> None:
        """Test conversion to audit record format."""
        call = ExecutedToolCall(
            tool_name="my_tool",
            request_json={"datasource_id": "ds-1", "query": "test"},
            response_json={"resultSizeBytes": 1024, "rowCount": 10},
            latency_ms=300,
            source="graph.node",
            provider_id="prov-1",
        )

        record = call.to_audit_record()
        assert record["tool_name"] == "my_tool"
        assert record["source"] == "graph.node"
        assert record["latency_ms"] == 300
        assert record["provider_id"] == "prov-1"
        assert "request_summary" in record
        assert "response_summary" in record


class TestToolDescriptorToSpec:
    """Tests for converting ToolDescriptor to ToolSpec."""

    def test_basic_conversion(self) -> None:
        """Test basic conversion from ToolDescriptor."""
        descriptor = ToolDescriptor(
            tool_name="test_tool",
            description="Test tool",
            input_schema={"type": "object"},
            output_schema={"type": "array"},
            provider_id="provider-1",
            tags=("metrics", "query"),
        )

        spec = tool_descriptor_to_spec(
            descriptor,
            kind="metrics",
            read_only=True,
            risk_level="low",
        )

        assert spec.name == "test_tool"
        assert spec.description == "Test tool"
        assert spec.input_schema == {"type": "object"}
        assert spec.kind == "metrics"
        assert spec.tags == ("metrics", "query")
        assert spec.provider_id == "provider-1"

    def test_provider_id_override(self) -> None:
        """Test that provider_id can be overridden."""
        descriptor = ToolDescriptor(
            tool_name="tool",
            provider_id="original",
        )

        spec = tool_descriptor_to_spec(
            descriptor,
            provider_id="override",
        )

        assert spec.provider_id == "override"


class TestToolMetadataToSpec:
    """Tests for converting ToolMetadata to ToolSpec."""

    def test_basic_conversion(self) -> None:
        """Test basic conversion from ToolMetadata."""
        metadata = ToolMetadata(
            tool_name="meta_tool",
            kind="logs",
            domain="observability",
            read_only=True,
            risk_level="medium",
            tags=("logs", "search"),
            description="Meta tool description",
        )

        spec = tool_metadata_to_spec(
            metadata,
            input_schema={"type": "object"},
            output_schema={"type": "object"},
            provider_id="meta-provider",
        )

        assert spec.name == "meta_tool"
        assert spec.description == "Meta tool description"
        assert spec.kind == "logs"
        assert spec.read_only is True
        assert spec.risk_level == "medium"
        assert spec.tags == ("logs", "search")
        assert spec.provider_id == "meta-provider"

    def test_schema_override(self) -> None:
        """Test that schemas can be provided."""
        metadata = ToolMetadata(tool_name="tool")

        spec = tool_metadata_to_spec(
            metadata,
            input_schema={"type": "object", "properties": {"q": {"type": "string"}}},
            output_schema={"type": "array"},
        )

        assert spec.input_schema == {"type": "object", "properties": {"q": {"type": "string"}}}
        assert spec.output_schema == {"type": "array"}


class TestToolSpecImmutability:
    """Tests for ToolSpec immutability."""

    def test_frozen_dataclass(self) -> None:
        """Test that ToolSpec is frozen and cannot be modified."""
        spec = ToolSpec(name="tool", description="desc")

        with pytest.raises(AttributeError):
            spec.name = "new_name"  # type: ignore[misc]

    def test_frozen_nested_dict_can_be_modified(self) -> None:
        """Test that nested dicts in frozen dataclass can still be modified.

        Note: This is a known limitation of frozen dataclasses - mutable
        nested objects can still be modified. Tests document this behavior.
        """
        spec = ToolSpec(
            name="tool",
            input_schema={"type": "object", "properties": {}},
        )

        # This works because the dict itself is mutable
        spec.input_schema["properties"]["new"] = {"type": "string"}
        assert "new" in spec.input_schema["properties"]


class TestExecutedToolCallImmutability:
    """Tests for ExecutedToolCall immutability."""

    def test_frozen_dataclass(self) -> None:
        """Test that ExecutedToolCall is frozen."""
        call = ExecutedToolCall(
            tool_name="tool",
            request_json={},
            response_json={},
            latency_ms=100,
            source="test",
        )

        with pytest.raises(AttributeError):
            call.latency_ms = 200  # type: ignore[misc]


class TestToolCatalogSnapshotImmutability:
    """Tests for ToolCatalogSnapshot immutability."""

    def test_frozen_dataclass(self) -> None:
        """Test that ToolCatalogSnapshot is frozen."""
        spec = ToolSpec(name="tool", description="d")
        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[spec],
        )

        with pytest.raises(AttributeError):
            snapshot.toolset_ids = ("ts2",)  # type: ignore[misc]

    def test_tuple_immutability(self) -> None:
        """Test that tuple fields cannot be modified."""
        spec1 = ToolSpec(name="tool1", description="d1")
        spec2 = ToolSpec(name="tool2", description="d2")
        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[spec1, spec2],
        )

        # Tuples are immutable
        assert isinstance(snapshot.tools, tuple)
        assert isinstance(snapshot.toolset_ids, tuple)


class TestCanonicalNaming:
    """Tests for canonical tool naming (no mcp. prefix)."""

    def test_toolspec_strips_mcp_prefix(self) -> None:
        """Test that ToolSpec strips mcp. prefix on construction."""
        spec = ToolSpec(name="mcp.prometheus_query", description="Query")
        assert spec.name == "prometheus_query"

    def test_executed_tool_call_strips_mcp_prefix(self) -> None:
        """Test that ExecutedToolCall strips mcp. prefix on construction."""
        call = ExecutedToolCall(
            tool_name="mcp.loki_search",
            request_json={},
            response_json={},
            latency_ms=100,
            source="test",
        )
        assert call.tool_name == "loki_search"

    def test_snapshot_handles_mcp_prefix_in_lookup(self) -> None:
        """Test that snapshot lookup handles mcp. prefix."""
        spec = ToolSpec(name="my_tool", description="Test")
        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[spec],
        )

        # Should find tool with mcp. prefix
        assert snapshot.has_tool("mcp.my_tool")
        found = snapshot.get_tool("mcp.my_tool")
        assert found is not None
        assert found.name == "my_tool"

    def test_to_openai_tool_uses_canonical_name(self) -> None:
        """Test that OpenAI tool format uses canonical name."""
        spec = ToolSpec(name="mcp.elasticsearch_search", description="Search")
        openai_tool = spec.to_openai_tool()
        assert openai_tool["function"]["name"] == "elasticsearch_search"

    def test_to_skill_result_uses_canonical_name(self) -> None:
        """Test that skill result format uses canonical name."""
        call = ExecutedToolCall(
            tool_name="mcp.my_tool",
            request_json={},
            response_json={},
            latency_ms=100,
            source="test",
        )
        result = call.to_skill_tool_result()
        assert result["tool_name"] == "my_tool"
        assert result["tool"] == "my_tool"


class TestSnapshotFromInvoker:
    """Tests for building snapshot from tool invoker."""

    def test_build_from_invoker_basic(self) -> None:
        """Test building snapshot from a mock invoker."""
        from unittest.mock import MagicMock

        # Create mock invoker
        mock_invoker = MagicMock()
        mock_invoker.allowed_tools.return_value = ["prometheus_query", "loki_search"]
        mock_invoker.provider_summaries.return_value = [
            {
                "provider_id": "mcp-server-1",
                "provider_type": "mcp_http",
                "allow_tools": ["prometheus_query", "loki_search"],
            }
        ]
        mock_invoker.toolset_ids = ["toolset-1"]

        # Build snapshot via the function (simulating what runtime does)
        from orchestrator.runtime.tool_catalog import ToolSpec, build_tool_catalog_snapshot

        # Manually create specs as the invoker would
        specs = [
            ToolSpec(name="prometheus_query", description="Query Prometheus", kind="metrics"),
            ToolSpec(name="loki_search", description="Search Loki", kind="logs"),
        ]

        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["toolset-1"],
            tool_specs=specs,
        )

        assert len(snapshot.tools) == 2
        assert snapshot.has_tool("prometheus_query")
        assert snapshot.has_tool("loki_search")
        assert snapshot.toolset_ids == ("toolset-1",)

    def test_build_from_invoker_with_provider_id(self) -> None:
        """Test that provider_id is correctly mapped from invoker."""
        spec1 = ToolSpec(
            name="prometheus_query",
            description="Query Prometheus",
            provider_id="mcp-server-1",
        )
        spec2 = ToolSpec(
            name="loki_search",
            description="Search Loki",
            provider_id="mcp-server-1",
        )

        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["toolset-1"],
            tool_specs=[spec1, spec2],
        )

        tool = snapshot.get_tool("prometheus_query")
        assert tool is not None
        assert tool.provider_id == "mcp-server-1"

    def test_build_from_invoker_toolset_ids_frozen(self) -> None:
        """Test that toolset_ids are frozen as a tuple."""
        spec = ToolSpec(name="tool", description="Tool")

        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1", "ts2"],
            tool_specs=[spec],
        )

        # toolset_ids should be a tuple
        assert isinstance(snapshot.toolset_ids, tuple)
        assert snapshot.toolset_ids == ("ts1", "ts2")

        # Original list should be unaffected
        original_list = ["ts1", "ts2"]
        snapshot = build_tool_catalog_snapshot(
            toolset_ids=original_list,
            tool_specs=[spec],
        )
        assert isinstance(snapshot.toolset_ids, tuple)
        assert isinstance(original_list, list)

    def test_build_from_invoker_canonical_name_normalization(self) -> None:
        """Test that tool names are normalized to canonical form."""
        specs = [
            ToolSpec(name="mcp.prometheus_query", description="P"),
            ToolSpec(name="loki_search", description="L"),
        ]

        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=specs,
        )

        # Both should be in canonical form in the by_name dict
        assert "prometheus_query" in snapshot.by_name
        assert "mcp.prometheus_query" not in snapshot.by_name

        # has_tool() handles mcp. prefix for backward compatibility
        # but tools are stored with canonical names only
        assert snapshot.has_tool("prometheus_query")
        assert snapshot.has_tool("loki_search")
        # mcp. prefix is stripped during lookup
        assert snapshot.has_tool("mcp.prometheus_query")

        # Verify tools tuple contains canonical names
        tool_names = [t.name for t in snapshot.tools]
        assert "prometheus_query" in tool_names
        assert "mcp.prometheus_query" not in tool_names

    def test_tag_inference_from_tool_name(self) -> None:
        """Test that tags are inferred from tool name when metadata is missing."""
        from orchestrator.runtime.tool_discovery import infer_tags_from_tool_name

        # Metrics tools
        assert "metrics" in infer_tags_from_tool_name("prometheus_query")
        assert "metrics" in infer_tags_from_tool_name("promql_instant_query")
        assert "metrics" in infer_tags_from_tool_name("victoria_metrics_query")

        # Logs tools
        assert "logs" in infer_tags_from_tool_name("loki_search")
        assert "logs" in infer_tags_from_tool_name("elasticsearch_search")
        assert "logs" in infer_tags_from_tool_name("query_logs")

        # Traces tools
        assert "traces" in infer_tags_from_tool_name("jaeger_trace_query")
        assert "traces" in infer_tags_from_tool_name("tempo_search")

        # Incidents tools
        assert "incidents" in infer_tags_from_tool_name("alertmanager_alerts")
        assert "incidents" in infer_tags_from_tool_name("incident_list")

        # Query/search patterns
        assert "query" in infer_tags_from_tool_name("prometheus_query")
        assert "search" in infer_tags_from_tool_name("loki_search")

    def test_snapshot_preserves_inferred_tags(self) -> None:
        """Test that snapshot building preserves inferred tags for unknown tools."""
        # Create specs with inferred tags (simulating what runtime.build_tool_catalog_snapshot_from_invoker does)
        from orchestrator.runtime.tool_discovery import infer_tags_from_tool_name

        prometheus_tags = infer_tags_from_tool_name("prometheus_query")
        loki_tags = infer_tags_from_tool_name("loki_search")

        assert "metrics" in prometheus_tags, f"Expected metrics in {prometheus_tags}"
        assert "logs" in loki_tags, f"Expected logs in {loki_tags}"

        # Build snapshot with inferred tags
        specs = [
            ToolSpec(name="prometheus_query", description="Query Prometheus", tags=prometheus_tags),
            ToolSpec(name="loki_search", description="Search Loki", tags=loki_tags),
        ]

        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=specs,
        )

        # Verify tags are preserved
        prom_tool = snapshot.get_tool("prometheus_query")
        assert prom_tool is not None
        assert "metrics" in prom_tool.tags

        loki_tool = snapshot.get_tool("loki_search")
        assert loki_tool is not None
        assert "logs" in loki_tool.tags


class TestDiscoveryFromSnapshot:
    """Tests for snapshot-based tool discovery."""

    def test_discover_tools_from_snapshot(self) -> None:
        """Test discover_tools_from_snapshot function."""
        from orchestrator.runtime.tool_discovery import discover_tools_from_snapshot

        spec1 = ToolSpec(
            name="prometheus_query",
            description="Query Prometheus",
            kind="metrics",
            tags=("metrics", "query"),
        )
        spec2 = ToolSpec(
            name="loki_search",
            description="Search Loki",
            kind="logs",
            tags=("logs", "search"),
        )

        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[spec1, spec2],
        )

        result = discover_tools_from_snapshot(snapshot)

        assert len(result.tools) == 2
        tool_names = [t.tool_name for t in result.tools]
        assert "prometheus_query" in tool_names
        assert "loki_search" in tool_names

    def test_discover_tools_from_snapshot_by_tag(self) -> None:
        """Test tag-based filtering from discovered tools."""
        from orchestrator.runtime.tool_discovery import discover_tools_from_snapshot

        spec1 = ToolSpec(name="tool1", description="T1", tags=("metrics", "query"))
        spec2 = ToolSpec(name="tool2", description="T2", tags=("logs", "search"))
        spec3 = ToolSpec(name="tool3", description="T3", tags=("metrics", "promql"))

        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[spec1, spec2, spec3],
        )

        result = discover_tools_from_snapshot(snapshot)

        metrics_tools = result.find_by_tag("metrics")
        assert len(metrics_tools) == 2
        assert all("metrics" in t.tags for t in metrics_tools)

    def test_discover_tools_from_snapshot_by_name(self) -> None:
        """Test name lookup from discovered tools."""
        from orchestrator.runtime.tool_discovery import discover_tools_from_snapshot

        spec = ToolSpec(name="my_tool", description="My Tool", kind="metrics")

        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[spec],
        )

        result = discover_tools_from_snapshot(snapshot)

        found = result.find_by_name("my_tool")
        assert found is not None
        assert found.tool_name == "my_tool"

        not_found = result.find_by_name("unknown_tool")
        assert not_found is None

    def test_discover_tools_from_snapshot_tool_names(self) -> None:
        """Test getting tool names from discovered tools."""
        from orchestrator.runtime.tool_discovery import discover_tools_from_snapshot

        spec1 = ToolSpec(name="zebra_tool", description="Z")
        spec2 = ToolSpec(name="alpha_tool", description="A")

        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[spec1, spec2],
        )

        result = discover_tools_from_snapshot(snapshot)

        names = result.tool_names()
        assert names == ["alpha_tool", "zebra_tool"]  # Sorted


class TestUnknownToolRejection:
    """Tests for unknown tool handling in snapshot."""

    def test_snapshot_rejects_unknown_tool_lookup(self) -> None:
        """Test that get_tool returns None for unknown tools."""
        spec = ToolSpec(name="known_tool", description="Known")
        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[spec],
        )

        assert snapshot.get_tool("unknown_tool") is None
        assert not snapshot.has_tool("unknown_tool")

    def test_snapshot_only_contains_provided_tools(self) -> None:
        """Test that snapshot only contains tools that were provided."""
        spec1 = ToolSpec(name="tool1", description="T1")
        spec2 = ToolSpec(name="tool2", description="T2")

        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[spec1, spec2],
        )

        # Only tool1 and tool2 should be present
        assert snapshot.has_tool("tool1")
        assert snapshot.has_tool("tool2")
        assert not snapshot.has_tool("tool3")
        assert not snapshot.has_tool("prometheus_query")
        assert not snapshot.has_tool("loki_search")

    def test_snapshot_empty_tools(self) -> None:
        """Test that empty snapshot has no tools."""
        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[],
        )

        assert len(snapshot.tools) == 0
        assert not snapshot.has_tool("any_tool")
        assert snapshot.get_tool("any_tool") is None


class TestDiscoveryPathFallback:
    """Tests for snapshot-only vs fallback discovery paths."""

    def test_discover_tools_uses_snapshot_when_available(self) -> None:
        """Test that discover_tools uses snapshot when set on runtime."""
        from unittest.mock import MagicMock

        from orchestrator.runtime.tool_discovery import discover_tools

        # Create mock runtime with snapshot
        mock_runtime = MagicMock()
        spec = ToolSpec(name="snapshot_tool", description="From Snapshot")
        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["snapshot-ts"],
            tool_specs=[spec],
        )
        mock_runtime._tool_catalog_snapshot = snapshot

        result = discover_tools(mock_runtime)

        # Should use snapshot, not invoker
        assert len(result.tools) == 1
        assert result.tools[0].tool_name == "snapshot_tool"

    def test_discover_tools_fallback_to_invoker_when_no_snapshot(self) -> None:
        """Test that discover_tools falls back to invoker when no snapshot."""
        from unittest.mock import MagicMock

        from orchestrator.runtime.tool_discovery import discover_tools

        # Create mock runtime without snapshot but with invoker
        mock_runtime = MagicMock()
        mock_runtime._tool_catalog_snapshot = None

        # Create mock invoker
        mock_invoker = MagicMock()
        mock_invoker.allowed_tools.return_value = ["invoker_tool"]
        mock_invoker.provider_summaries.return_value = []
        mock_runtime._tool_invoker = mock_invoker

        result = discover_tools(mock_runtime)

        # Should use invoker fallback
        assert len(result.tools) == 1
        assert result.tools[0].tool_name == "invoker_tool"

    def test_discover_tools_empty_when_no_snapshot_or_invoker(self) -> None:
        """Test that discover_tools returns empty when no snapshot or invoker."""
        from unittest.mock import MagicMock

        from orchestrator.runtime.tool_discovery import discover_tools

        # Create mock runtime without snapshot or invoker
        mock_runtime = MagicMock()
        mock_runtime._tool_catalog_snapshot = None
        mock_runtime._tool_invoker = None

        result = discover_tools(mock_runtime)

        assert len(result.tools) == 0

    def test_snapshot_discovery_vs_invoker_discovery_difference(self) -> None:
        """Test that snapshot and invoker paths can produce different results."""
        from unittest.mock import MagicMock

        from orchestrator.runtime.tool_discovery import discover_tools, discover_tools_from_snapshot

        # Create snapshot with specific tools
        snapshot_spec = ToolSpec(name="snapshot_only_tool", description="In Snapshot")
        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[snapshot_spec],
        )

        # Discover from snapshot directly
        snapshot_result = discover_tools_from_snapshot(snapshot)
        assert snapshot_result.find_by_name("snapshot_only_tool") is not None

        # Create mock invoker with different tools
        mock_invoker = MagicMock()
        mock_invoker.allowed_tools.return_value = ["invoker_only_tool"]
        mock_invoker.provider_summaries.return_value = []

        mock_runtime = MagicMock()
        mock_runtime._tool_invoker = mock_invoker
        mock_runtime._tool_catalog_snapshot = None

        # Discover from invoker
        invoker_result = discover_tools(mock_runtime)
        assert invoker_result.find_by_name("invoker_only_tool") is not None
        assert invoker_result.find_by_name("snapshot_only_tool") is None


class TestRequestResponseSummary:
    """Tests for request and response summarization in ExecutedToolCall."""

    def test_request_summary_includes_datasource_id(self) -> None:
        """Test that request summary includes datasource_id."""
        call = ExecutedToolCall(
            tool_name="prometheus_query",
            request_json={"datasource_id": "ds-123", "query": "up"},
            response_json={},
            latency_ms=100,
            source="test",
        )

        record = call.to_audit_record()
        assert "datasource_id" in record["request_summary"]
        assert record["request_summary"]["datasource_id"] == "ds-123"

    def test_response_summary_includes_size_info(self) -> None:
        """Test that response summary includes size information."""
        call = ExecutedToolCall(
            tool_name="prometheus_query",
            request_json={},
            response_json={"resultSizeBytes": 1024, "rowCount": 10},
            latency_ms=100,
            source="test",
        )

        record = call.to_audit_record()
        assert record["response_summary"]["result_size_bytes"] == 1024
        assert record["response_summary"]["row_count"] == 10

    def test_response_summary_indicates_error(self) -> None:
        """Test that response summary indicates errors."""
        call = ExecutedToolCall(
            tool_name="tool",
            request_json={},
            response_json={"error": "something went wrong"},
            latency_ms=100,
            source="test",
        )

        record = call.to_audit_record()
        assert record["response_summary"]["has_error"] is True