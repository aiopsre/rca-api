"""Cross-layer contract tests for Function Calling migration (FC3E).

These tests verify that graph and skills use consistent contracts:
- Same ToolSpec generates consistent binding for both graph and skills
- Same tool execution returns consistent ExecutedToolCall for both paths
- RuntimeToolGateway protocol is correctly implemented
"""
from __future__ import annotations

from typing import Any
from unittest.mock import MagicMock, patch

import pytest

from orchestrator.langgraph.config import OrchestratorConfig
from orchestrator.runtime.fc_adapter import FunctionCallingToolAdapter, NormalizedToolCall
from orchestrator.runtime.tool_catalog import (
    ExecutedToolCall,
    ToolCatalogSnapshot,
    ToolSpec,
    build_tool_catalog_snapshot,
)


class TestCrossLayerToolBinding:
    """Tests for consistent tool binding across graph and skills."""

    def test_same_toolspec_produces_consistent_openai_format(self) -> None:
        """Verify that same ToolSpec produces same OpenAI format for both paths."""
        # Create tool specs
        metrics_spec = ToolSpec(
            name="prometheus_query",
            description="Query Prometheus metrics",
            input_schema={
                "type": "object",
                "properties": {
                    "datasource_id": {"type": "string"},
                    "promql": {"type": "string"},
                    "start_ts": {"type": "integer"},
                    "end_ts": {"type": "integer"},
                    "step_seconds": {"type": "integer"},
                },
                "required": ["datasource_id", "promql", "start_ts", "end_ts", "step_seconds"],
            },
            kind="metrics",
        )
        logs_spec = ToolSpec(
            name="loki_search",
            description="Search Loki logs",
            input_schema={
                "type": "object",
                "properties": {
                    "datasource_id": {"type": "string"},
                    "query": {"type": "string"},
                    "start_ts": {"type": "integer"},
                    "end_ts": {"type": "integer"},
                    "limit": {"type": "integer"},
                },
                "required": ["datasource_id", "query", "start_ts", "end_ts", "limit"],
            },
            kind="logs",
        )

        # Create snapshot
        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[metrics_spec, logs_spec],
        )

        # Create adapter (used by both graph and skills)
        adapter = FunctionCallingToolAdapter(snapshot)

        # Get OpenAI format
        openai_tools = adapter.to_openai_tools()

        # Verify structure
        assert len(openai_tools) == 2
        tool_names = {t["function"]["name"] for t in openai_tools}
        assert tool_names == {"prometheus_query", "loki_search"}

        # Verify each tool has correct structure
        for tool in openai_tools:
            assert tool["type"] == "function"
            assert "function" in tool
            assert "name" in tool["function"]
            assert "description" in tool["function"]
            assert "parameters" in tool["function"]

    def test_canonical_name_consistency(self) -> None:
        """Verify that canonical names are consistently used across all layers."""
        # Create spec with canonical name (no mcp. prefix)
        spec = ToolSpec(
            name="prometheus_query",
            description="Query metrics",
            kind="metrics",
        )

        # Create spec with mcp. prefix (should be normalized)
        spec_with_prefix = ToolSpec(
            name="mcp.loki_search",
            description="Search logs",
            kind="logs",
        )

        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[spec, spec_with_prefix],
        )

        # Verify snapshot stores tools with canonical names
        assert snapshot.has_tool("prometheus_query")
        assert snapshot.has_tool("loki_search")  # mcp. prefix stripped in storage

        # has_tool accepts both forms and normalizes internally
        assert snapshot.has_tool("mcp.loki_search")  # normalized to loki_search

        # Verify by_name only contains canonical names
        assert "prometheus_query" in snapshot.by_name
        assert "loki_search" in snapshot.by_name
        assert "mcp.loki_search" not in snapshot.by_name

        # Verify adapter uses canonical names
        adapter = FunctionCallingToolAdapter(snapshot)
        openai_tools = adapter.to_openai_tools()
        tool_names = {t["function"]["name"] for t in openai_tools}
        assert tool_names == {"prometheus_query", "loki_search"}


class TestCrossLayerExecutedToolCall:
    """Tests for consistent ExecutedToolCall across execution paths."""

    def test_executed_tool_call_is_frozen(self) -> None:
        """Verify ExecutedToolCall is immutable."""
        call = ExecutedToolCall(
            tool_name="prometheus_query",
            request_json={"query": "up"},
            response_json={"data": "test"},
            latency_ms=100,
            source="graph.fc_agent",
        )

        # Should be frozen
        with pytest.raises(Exception):  # FrozenInstanceError
            call.tool_name = "other_tool"  # type: ignore[misc]

    def test_executed_tool_call_to_skill_result(self) -> None:
        """Verify ExecutedToolCall converts correctly to skill result format."""
        call = ExecutedToolCall(
            tool_name="loki_search",
            request_json={"query": "error"},
            response_json={"lines": ["line1", "line2"]},
            latency_ms=250,
            source="skill.plan",
            status="ok",
            provider_id="provider-1",
            provider_type="mcp_http",
            resolved_from_toolset_id="ts-1",
        )

        result = call.to_skill_tool_result()

        assert result["tool"] == "loki_search"
        assert result["tool_name"] == "loki_search"
        assert result["tool_request"] == {"query": "error"}
        assert result["tool_result"] == {"lines": ["line1", "line2"]}
        assert result["latency_ms"] == 250
        assert result["status"] == "ok"
        assert result["provider_id"] == "provider-1"
        assert result["source"] == "skill.plan"

    def test_executed_tool_call_to_audit_record(self) -> None:
        """Verify ExecutedToolCall converts correctly to audit record format."""
        call = ExecutedToolCall(
            tool_name="prometheus_query",
            request_json={"datasource_id": "ds-1", "promql": "up"},
            response_json={"result": [{"values": []}]},
            latency_ms=150,
            source="graph.fc_agent",
            status="ok",
            error="",
        )

        record = call.to_audit_record()

        assert record["tool_name"] == "prometheus_query"
        assert record["source"] == "graph.fc_agent"
        assert record["latency_ms"] == 150
        assert record["status"] == "ok"
        assert "request_summary" in record
        assert "response_summary" in record

    def test_executed_tool_call_with_error(self) -> None:
        """Verify ExecutedToolCall correctly represents errors."""
        call = ExecutedToolCall(
            tool_name="unknown_tool",
            request_json={},
            response_json={},
            latency_ms=10,
            source="skill.plan",
            status="error",
            error="tool not found in catalog",
        )

        # Check attributes
        assert call.status == "error"
        assert call.error == "tool not found in catalog"

        # Check skill result format
        result = call.to_skill_tool_result()
        assert result["status"] == "error"
        assert result["error"] == "tool not found in catalog"

        # Check audit record format
        record = call.to_audit_record()
        assert record["status"] == "error"
        assert record["error"] == "tool not found in catalog"


class TestRuntimeToolGatewayContract:
    """Tests for RuntimeToolGateway protocol implementation."""

    def test_list_tools_returns_tool_specs(self) -> None:
        """Verify list_tools returns list of ToolSpec."""
        from orchestrator.runtime.runtime import OrchestratorRuntime

        # Create mock dependencies
        mock_client = MagicMock()
        mock_client.mcp_client = MagicMock()

        # Create tool specs
        specs = [
            ToolSpec(name="prometheus_query", description="Query metrics", kind="metrics"),
            ToolSpec(name="loki_search", description="Search logs", kind="logs"),
        ]
        snapshot = build_tool_catalog_snapshot(toolset_ids=["ts1"], tool_specs=specs)

        # Create runtime with snapshot
        runtime = OrchestratorRuntime(
            client=mock_client,
            job_id="test-job",
            instance_id="test-instance",
            heartbeat_interval_seconds=30,
            tool_catalog_snapshot=snapshot,
        )

        # List tools
        tools = runtime.list_tools()

        assert len(tools) == 2
        tool_names = {t.name for t in tools}
        assert tool_names == {"prometheus_query", "loki_search"}

    def test_execute_tool_returns_executed_tool_call(self) -> None:
        """Verify execute_tool returns ExecutedToolCall."""
        from orchestrator.runtime.runtime import OrchestratorRuntime

        # Create mock dependencies
        mock_client = MagicMock()
        mock_client.mcp_client = MagicMock()
        mock_client.mcp_client.call = MagicMock(return_value={"result": "ok"})

        # Create tool specs
        specs = [
            ToolSpec(name="prometheus_query", description="Query metrics", kind="metrics"),
        ]
        snapshot = build_tool_catalog_snapshot(toolset_ids=["ts1"], tool_specs=specs)

        # Create runtime with snapshot
        runtime = OrchestratorRuntime(
            client=mock_client,
            job_id="test-job",
            instance_id="test-instance",
            heartbeat_interval_seconds=30,
            tool_catalog_snapshot=snapshot,
        )

        # Mock call_tool to return a result
        with patch.object(runtime, "call_tool", return_value={"data": "test_result"}):
            result = runtime.execute_tool(
                tool_name="prometheus_query",
                args={"query": "up"},
                source="test.caller",
            )

        # Verify result is ExecutedToolCall
        assert isinstance(result, ExecutedToolCall)
        assert result.tool_name == "prometheus_query"
        assert result.request_json == {"query": "up"}
        assert result.source == "test.caller"
        assert result.status == "ok"

    def test_execute_tool_handles_unknown_tool(self) -> None:
        """Verify execute_tool handles unknown tools gracefully."""
        from orchestrator.runtime.runtime import OrchestratorRuntime

        # Create mock dependencies
        mock_client = MagicMock()
        mock_client.mcp_client = MagicMock()

        # Create empty snapshot (no tools)
        snapshot = build_tool_catalog_snapshot(toolset_ids=[], tool_specs=[])

        # Create runtime
        runtime = OrchestratorRuntime(
            client=mock_client,
            job_id="test-job",
            instance_id="test-instance",
            heartbeat_interval_seconds=30,
            tool_catalog_snapshot=snapshot,
        )

        # Execute unknown tool
        result = runtime.execute_tool(
            tool_name="unknown_tool",
            args={},
            source="test.caller",
        )

        # Should return error ExecutedToolCall
        assert result.status == "error"
        assert "tool not found" in result.error


class TestNormalizedToolCallContract:
    """Tests for NormalizedToolCall contract."""

    def test_normalized_tool_call_is_frozen(self) -> None:
        """Verify NormalizedToolCall is immutable."""
        call = NormalizedToolCall(
            tool_name="prometheus_query",
            arguments={"query": "up"},
        )

        # Should be frozen
        with pytest.raises(Exception):  # FrozenInstanceError
            call.tool_name = "other_tool"  # type: ignore[misc]

    def test_normalized_tool_call_strips_mcp_prefix(self) -> None:
        """Verify NormalizedToolCall normalizes tool names."""
        # Create from dict with mcp. prefix
        call = NormalizedToolCall(
            tool_name="mcp.loki_search",
            arguments={"query": "error"},
        )

        # Should strip mcp. prefix
        assert call.tool_name == "loki_search"


class TestEndToEndContract:
    """End-to-end contract tests across all layers."""

    def test_full_workflow_consistency(self) -> None:
        """Verify consistent behavior across the full workflow."""
        # 1. Create tools
        metrics_spec = ToolSpec(
            name="prometheus_query",
            description="Query Prometheus metrics",
            input_schema={
                "type": "object",
                "properties": {
                    "datasource_id": {"type": "string"},
                    "promql": {"type": "string"},
                },
                "required": ["datasource_id", "promql"],
            },
            kind="metrics",
        )

        # 2. Create snapshot
        snapshot = build_tool_catalog_snapshot(
            toolset_ids=["ts1"],
            tool_specs=[metrics_spec],
        )

        # 3. Create adapter
        adapter = FunctionCallingToolAdapter(snapshot)

        # 4. Get OpenAI format (for LLM binding)
        openai_tools = adapter.to_openai_tools()
        assert len(openai_tools) == 1
        assert openai_tools[0]["function"]["name"] == "prometheus_query"

        # 5. Normalize a tool call from LLM
        normalized = adapter.normalize_tool_calls([
            {"name": "prometheus_query", "args": {"datasource_id": "ds1", "promql": "up"}, "id": "call1"},
        ])
        assert len(normalized) == 1
        assert normalized[0].tool_name == "prometheus_query"

        # 6. Validate against catalog
        assert adapter.has_tool(normalized[0].tool_name)

        # 7. Create ExecutedToolCall from execution
        executed = ExecutedToolCall(
            tool_name=normalized[0].tool_name,
            request_json=normalized[0].arguments,
            response_json={"result": "ok"},
            latency_ms=100,
            source="test.workflow",
        )

        # 8. Verify can be converted to both skill and audit formats
        skill_result = executed.to_skill_tool_result()
        assert skill_result["tool_name"] == "prometheus_query"

        audit_record = executed.to_audit_record()
        assert audit_record["tool_name"] == "prometheus_query"
        assert audit_record["source"] == "test.workflow"