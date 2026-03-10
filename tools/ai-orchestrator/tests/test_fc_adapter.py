"""Tests for fc_adapter.py - Function Calling Tool Adapter.

This test module covers:
- FunctionCallingToolAdapter: Conversion to OpenAI tools format
- NormalizedToolCall: Normalized tool call representation
- Tool call normalization and validation
"""
from __future__ import annotations

import pytest

from orchestrator.runtime.tool_catalog import (
    ToolSpec,
    ToolCatalogSnapshot,
    build_tool_catalog_snapshot,
)
from orchestrator.runtime.fc_adapter import (
    FunctionCallingToolAdapter,
    NormalizedToolCall,
)


class TestNormalizedToolCall:
    """Tests for NormalizedToolCall dataclass."""

    def test_basic_creation(self) -> None:
        """Test creating a basic NormalizedToolCall."""
        call = NormalizedToolCall(
            tool_name="prometheus_query",
            arguments={"query": "up"},
            call_id="call_123",
        )
        assert call.tool_name == "prometheus_query"
        assert call.arguments == {"query": "up"}
        assert call.call_id == "call_123"

    def test_canonical_name_normalization(self) -> None:
        """Test that mcp. prefix is stripped from tool name."""
        call = NormalizedToolCall(
            tool_name="mcp.loki_search",
            arguments={"query": "error"},
        )
        assert call.tool_name == "loki_search"

    def test_default_call_id(self) -> None:
        """Test that call_id defaults to empty string."""
        call = NormalizedToolCall(
            tool_name="my_tool",
            arguments={},
        )
        assert call.call_id == ""

    def test_frozen_dataclass(self) -> None:
        """Test that NormalizedToolCall is frozen."""
        call = NormalizedToolCall(
            tool_name="tool",
            arguments={},
        )
        with pytest.raises(AttributeError):
            call.tool_name = "new_tool"  # type: ignore[misc]


class TestFunctionCallingToolAdapter:
    """Tests for FunctionCallingToolAdapter."""

    def test_to_openai_tools_from_snapshot(self) -> None:
        """Verify correct generation of OpenAI tools from snapshot."""
        spec1 = ToolSpec(
            name="prometheus_query",
            description="Query Prometheus metrics",
            input_schema={
                "type": "object",
                "properties": {
                    "query": {"type": "string"},
                    "step_seconds": {"type": "integer"},
                },
                "required": ["query"],
            },
        )
        spec2 = ToolSpec(
            name="loki_search",
            description="Search Loki logs",
            input_schema={
                "type": "object",
                "properties": {
                    "query": {"type": "string"},
                },
                "required": ["query"],
            },
        )

        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[spec1, spec2],
        )

        adapter = FunctionCallingToolAdapter(snapshot)
        openai_tools = adapter.to_openai_tools()

        assert len(openai_tools) == 2
        assert all(t["type"] == "function" for t in openai_tools)

        names = {t["function"]["name"] for t in openai_tools}
        assert "prometheus_query" in names
        assert "loki_search" in names

    def test_to_openai_tools_empty_snapshot(self) -> None:
        """Verify empty list when snapshot has no tools."""
        snapshot = build_tool_catalog_snapshot(
            toolset_ids=[],
            tool_specs=[],
        )

        adapter = FunctionCallingToolAdapter(snapshot)
        openai_tools = adapter.to_openai_tools()

        assert openai_tools == []

    def test_normalize_tool_calls_dict_format(self) -> None:
        """Verify normalization of dict format tool calls."""
        spec = ToolSpec(name="my_tool", description="Test")
        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[spec],
        )

        adapter = FunctionCallingToolAdapter(snapshot)

        # LangChain AIMessage.tool_calls format
        tool_calls = [
            {"name": "my_tool", "args": {"query": "test"}, "id": "call_1"},
            {"name": "other_tool", "args": {}, "id": "call_2"},
        ]

        normalized = adapter.normalize_tool_calls(tool_calls)

        assert len(normalized) == 2
        assert normalized[0].tool_name == "my_tool"
        assert normalized[0].arguments == {"query": "test"}
        assert normalized[0].call_id == "call_1"
        assert normalized[1].tool_name == "other_tool"

    def test_normalize_tool_calls_object_format(self) -> None:
        """Verify normalization of object format tool calls."""

        class MockToolCall:
            def __init__(self, name: str, args: dict, call_id: str) -> None:
                self.name = name
                self.args = args
                self.id = call_id

        spec = ToolSpec(name="my_tool", description="Test")
        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[spec],
        )

        adapter = FunctionCallingToolAdapter(snapshot)

        tool_calls = [
            MockToolCall("my_tool", {"q": "test"}, "call_1"),
        ]

        normalized = adapter.normalize_tool_calls(tool_calls)

        assert len(normalized) == 1
        assert normalized[0].tool_name == "my_tool"
        assert normalized[0].arguments == {"q": "test"}
        assert normalized[0].call_id == "call_1"

    def test_normalize_tool_calls_empty_name(self) -> None:
        """Verify tool calls with empty name are skipped."""
        spec = ToolSpec(name="my_tool", description="Test")
        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[spec],
        )

        adapter = FunctionCallingToolAdapter(snapshot)

        tool_calls = [
            {"name": "", "args": {}, "id": "call_1"},
            {"name": "my_tool", "args": {}, "id": "call_2"},
        ]

        normalized = adapter.normalize_tool_calls(tool_calls)

        assert len(normalized) == 1
        assert normalized[0].tool_name == "my_tool"

    def test_normalize_tool_calls_mcp_prefix(self) -> None:
        """Verify mcp. prefix is stripped during normalization."""
        spec = ToolSpec(name="my_tool", description="Test")
        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[spec],
        )

        adapter = FunctionCallingToolAdapter(snapshot)

        tool_calls = [
            {"name": "mcp.my_tool", "args": {}, "id": "call_1"},
        ]

        normalized = adapter.normalize_tool_calls(tool_calls)

        assert len(normalized) == 1
        assert normalized[0].tool_name == "my_tool"

    def test_validate_tool_calls_all_valid(self) -> None:
        """Verify validation passes for valid tool calls."""
        spec1 = ToolSpec(name="tool1", description="T1")
        spec2 = ToolSpec(name="tool2", description="T2")
        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[spec1, spec2],
        )

        adapter = FunctionCallingToolAdapter(snapshot)

        calls = [
            NormalizedToolCall(tool_name="tool1", arguments={}),
            NormalizedToolCall(tool_name="tool2", arguments={}),
        ]

        validated = adapter.validate_tool_calls(calls)

        assert len(validated) == 2
        assert validated[0].tool_name == "tool1"
        assert validated[1].tool_name == "tool2"

    def test_validate_tool_calls_rejects_unknown(self) -> None:
        """Verify validation rejects unknown tools."""
        spec = ToolSpec(name="known_tool", description="Known")
        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[spec],
        )

        adapter = FunctionCallingToolAdapter(snapshot)

        calls = [
            NormalizedToolCall(tool_name="known_tool", arguments={}),
            NormalizedToolCall(tool_name="unknown_tool", arguments={}),
        ]

        with pytest.raises(RuntimeError, match="Unknown tool: unknown_tool"):
            adapter.validate_tool_calls(calls)

    def test_has_tool(self) -> None:
        """Verify has_tool method works correctly."""
        spec = ToolSpec(name="my_tool", description="Test")
        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[spec],
        )

        adapter = FunctionCallingToolAdapter(snapshot)

        assert adapter.has_tool("my_tool") is True
        assert adapter.has_tool("mcp.my_tool") is True  # prefix handling
        assert adapter.has_tool("unknown_tool") is False

    def test_get_tool(self) -> None:
        """Verify get_tool method works correctly."""
        spec = ToolSpec(name="my_tool", description="Test Tool", kind="metrics")
        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[spec],
        )

        adapter = FunctionCallingToolAdapter(snapshot)

        found = adapter.get_tool("my_tool")
        assert found is not None
        assert found.name == "my_tool"
        assert found.kind == "metrics"

        not_found = adapter.get_tool("unknown_tool")
        assert not_found is None

    def test_tool_names(self) -> None:
        """Verify tool_names returns sorted list."""
        spec1 = ToolSpec(name="zebra", description="Z")
        spec2 = ToolSpec(name="alpha", description="A")
        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[spec1, spec2],
        )

        adapter = FunctionCallingToolAdapter(snapshot)
        names = adapter.tool_names()

        assert names == ["alpha", "zebra"]  # Sorted

    def test_normalize_tool_calls_none_args(self) -> None:
        """Verify None args is handled gracefully."""
        spec = ToolSpec(name="my_tool", description="Test")
        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[spec],
        )

        adapter = FunctionCallingToolAdapter(snapshot)

        # Test with None args in dict format
        tool_calls = [
            {"name": "my_tool", "args": None, "id": "call_1"},
        ]

        normalized = adapter.normalize_tool_calls(tool_calls)

        assert len(normalized) == 1
        assert normalized[0].arguments == {}

    def test_normalize_tool_calls_empty_list(self) -> None:
        """Verify empty list input returns empty list."""
        spec = ToolSpec(name="my_tool", description="Test")
        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[spec],
        )

        adapter = FunctionCallingToolAdapter(snapshot)
        normalized = adapter.normalize_tool_calls([])

        assert normalized == []

    def test_normalize_tool_calls_none_input(self) -> None:
        """Verify None input returns empty list."""
        spec = ToolSpec(name="my_tool", description="Test")
        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[spec],
        )

        adapter = FunctionCallingToolAdapter(snapshot)
        normalized = adapter.normalize_tool_calls(None)  # type: ignore[arg-type]

        assert normalized == []


class TestCanonicalNameInFC:
    """Tests for canonical name handling in FC adapter."""

    def test_mcp_prefix_stripped_in_openai_tools(self) -> None:
        """Verify OpenAI tools use canonical names (no mcp. prefix)."""
        spec = ToolSpec(name="mcp.elasticsearch_search", description="Search")
        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[spec],
        )

        adapter = FunctionCallingToolAdapter(snapshot)
        openai_tools = adapter.to_openai_tools()

        # ToolSpec already strips mcp. prefix in __post_init__
        assert openai_tools[0]["function"]["name"] == "elasticsearch_search"

    def test_tool_lookup_with_mcp_prefix(self) -> None:
        """Verify tool lookup works with mcp. prefix."""
        spec = ToolSpec(name="my_tool", description="Test")
        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[spec],
        )

        adapter = FunctionCallingToolAdapter(snapshot)

        # Both forms should work
        assert adapter.has_tool("my_tool")
        assert adapter.has_tool("mcp.my_tool")


class TestFCAdapterIntegration:
    """Integration tests for FC adapter with snapshot."""

    def test_full_workflow(self) -> None:
        """Test complete FC workflow: create adapter, normalize, validate."""
        # Create snapshot with multiple tools
        specs = [
            ToolSpec(
                name="prometheus_query",
                description="Query Prometheus",
                input_schema={"type": "object", "properties": {"query": {"type": "string"}}},
                kind="metrics",
            ),
            ToolSpec(
                name="loki_search",
                description="Search Loki",
                input_schema={"type": "object", "properties": {"query": {"type": "string"}}},
                kind="logs",
            ),
        ]
        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1", "ts2"],
            tool_specs=specs,
        )

        # Create adapter
        adapter = FunctionCallingToolAdapter(snapshot)

        # Generate OpenAI tools
        openai_tools = adapter.to_openai_tools()
        assert len(openai_tools) == 2

        # Normalize tool calls from LLM
        tool_calls = [
            {"name": "prometheus_query", "args": {"query": "up"}, "id": "call_1"},
            {"name": "loki_search", "args": {"query": "error"}, "id": "call_2"},
        ]
        normalized = adapter.normalize_tool_calls(tool_calls)
        assert len(normalized) == 2

        # Validate against snapshot
        validated = adapter.validate_tool_calls(normalized)
        assert len(validated) == 2

        # Verify tool names are canonical
        assert validated[0].tool_name == "prometheus_query"
        assert validated[1].tool_name == "loki_search"

    def test_validation_after_normalization(self) -> None:
        """Test that validation catches tools not in snapshot."""
        specs = [
            ToolSpec(name="tool1", description="T1"),
            ToolSpec(name="tool2", description="T2"),
        ]
        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=specs,
        )

        adapter = FunctionCallingToolAdapter(snapshot)

        # Tool calls include a tool not in the snapshot
        tool_calls = [
            {"name": "tool1", "args": {}, "id": "call_1"},
            {"name": "unknown_tool", "args": {}, "id": "call_2"},
        ]
        normalized = adapter.normalize_tool_calls(tool_calls)

        # Validation should fail
        with pytest.raises(RuntimeError, match="Unknown tool: unknown_tool"):
            adapter.validate_tool_calls(normalized)


class TestFCAdapterProperties:
    """Tests for FC adapter properties and edge cases."""

    def test_snapshot_property(self) -> None:
        """Verify snapshot property returns the underlying snapshot."""
        spec = ToolSpec(name="tool", description="Test")
        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[spec],
        )

        adapter = FunctionCallingToolAdapter(snapshot)
        assert adapter.snapshot is snapshot

    def test_args_not_dict_converted_to_empty(self) -> None:
        """Verify non-dict args are converted to empty dict."""

        class MockToolCall:
            def __init__(self) -> None:
                self.name = "tool"
                self.args = "not a dict"  # type: ignore[assignment]
                self.id = "call_1"

        spec = ToolSpec(name="tool", description="Test")
        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[spec],
        )

        adapter = FunctionCallingToolAdapter(snapshot)
        normalized = adapter.normalize_tool_calls([MockToolCall()])

        assert len(normalized) == 1
        assert normalized[0].arguments == {}


class TestFCValidationFixes:
    """Tests for FC validation fixes (P1/P2 issues).

    These tests verify that:
    - P1: Skill-level allowlist is enforced
    - P2: Input validation runs on FC tool plans
    - P2: Overlong sequences are rejected (not silently truncated)
    - P2: Duplicates are rejected (not silently skipped)
    """

    def test_rejects_overlong_tool_calls_in_normalize(self) -> None:
        """Verify that _normalize_tool_calls_to_plans rejects overlong sequences."""
        from orchestrator.skills.agent import PromptSkillAgent

        # Create a mock agent with minimal setup
        agent = PromptSkillAgent.__new__(PromptSkillAgent)

        # Create 3 tool calls when max is 2
        tool_calls = [
            {"name": "tool1", "args": {}, "id": "call_1"},
            {"name": "tool2", "args": {}, "id": "call_2"},
            {"name": "tool3", "args": {}, "id": "call_3"},
        ]

        with pytest.raises(RuntimeError, match="exceeds max_tool_calls"):
            agent._normalize_tool_calls_to_plans(tool_calls, max_tool_calls=2)

    def test_rejects_duplicate_tool_calls_in_normalize(self) -> None:
        """Verify that _normalize_tool_calls_to_plans rejects duplicate tools."""
        from orchestrator.skills.agent import PromptSkillAgent

        agent = PromptSkillAgent.__new__(PromptSkillAgent)

        # Create tool calls with duplicate tool name
        tool_calls = [
            {"name": "prometheus_query", "args": {"query": "up"}, "id": "call_1"},
            {"name": "prometheus_query", "args": {"query": "down"}, "id": "call_2"},
        ]

        with pytest.raises(RuntimeError, match="duplicate tool"):
            agent._normalize_tool_calls_to_plans(tool_calls, max_tool_calls=2)

    def test_accepts_valid_tool_call_count(self) -> None:
        """Verify that valid tool call counts are accepted."""
        from orchestrator.skills.agent import PromptSkillAgent

        agent = PromptSkillAgent.__new__(PromptSkillAgent)

        # Create exactly max_tool_calls tool calls
        tool_calls = [
            {"name": "tool1", "args": {}, "id": "call_1"},
            {"name": "tool2", "args": {}, "id": "call_2"},
        ]

        plans = agent._normalize_tool_calls_to_plans(tool_calls, max_tool_calls=2)
        assert len(plans) == 2

    def test_mcp_prefix_duplicate_detection(self) -> None:
        """Verify duplicates are detected after mcp. prefix normalization."""
        from orchestrator.skills.agent import PromptSkillAgent

        agent = PromptSkillAgent.__new__(PromptSkillAgent)

        # Same tool, one with mcp. prefix, one without
        tool_calls = [
            {"name": "mcp.loki_search", "args": {"query": "error"}, "id": "call_1"},
            {"name": "loki_search", "args": {"query": "warn"}, "id": "call_2"},
        ]

        with pytest.raises(RuntimeError, match="duplicate tool"):
            agent._normalize_tool_calls_to_plans(tool_calls, max_tool_calls=2)