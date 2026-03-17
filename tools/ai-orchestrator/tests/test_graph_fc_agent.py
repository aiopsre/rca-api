"""Tests for graph function-calling agent node.

This test module covers:
- run_tool_agent: Function-calling agent node for tool execution
- ToolExecutionResult: Execution envelope with source tracking
"""
from __future__ import annotations

import os
from typing import Any
from unittest.mock import MagicMock, patch

import pytest

from orchestrator.langgraph.config import OrchestratorConfig
from orchestrator.langgraph.executor import ToolExecutionResult
from orchestrator.runtime.tool_catalog import ToolSpec, ToolCatalogSnapshot, build_tool_catalog_snapshot


class TestToolExecutionResult:
    """Tests for ToolExecutionResult dataclass."""

    def test_basic_creation(self) -> None:
        """Test creating a basic ToolExecutionResult."""
        result = ToolExecutionResult(
            tool="prometheus_query",
            params={"query": "up"},
            query_type="metrics",
            purpose="Query metrics",
            status="ok",
            result={"data": "test"},
            error=None,
            latency_ms=100,
            group_idx=0,
            item_idx=0,
        )
        assert result.tool == "prometheus_query"
        assert result.params == {"query": "up"}
        assert result.status == "ok"
        assert result.source == "graph"  # Default value

    def test_source_field(self) -> None:
        """Test that source field can be set."""
        result = ToolExecutionResult(
            tool="loki_search",
            params={},
            query_type="logs",
            purpose="Search logs",
            status="error",
            result={},
            error="timeout",
            latency_ms=5000,
            group_idx=0,
            item_idx=0,
            source="fc_agent",
        )
        assert result.source == "fc_agent"

    def test_error_result(self) -> None:
        """Test creating an error result."""
        result = ToolExecutionResult(
            tool="unknown_tool",
            params={},
            query_type="unknown",
            purpose="Test",
            status="error",
            result={},
            error="tool not found",
            latency_ms=10,
            group_idx=0,
            item_idx=0,
        )
        assert result.status == "error"
        assert result.error == "tool not found"


class TestRunToolAgent:
    """Tests for run_tool_agent function."""

    def test_no_snapshot_returns_empty_results(self) -> None:
        """Verify that missing snapshot returns empty results."""
        from orchestrator.langgraph.nodes_dynamic import run_tool_agent

        # Mock state
        state = MagicMock()
        state.incident_id = "test-incident"
        state.session_id = "test-session"
        state.datasource_id = None
        state.a3_max_calls = 6
        state.a3_max_total_latency_ms = 8000
        state.tool_call_results = []
        state.tool_calls_written = 0
        state.incident_context = {}
        state.evidence_plan = {}
        state.add_degrade_reason = MagicMock()

        # Mock runtime - get_fc_adapter returns None (no snapshot available)
        runtime = MagicMock()
        runtime.get_fc_adapter.return_value = None

        # Mock config
        cfg = OrchestratorConfig()

        result = run_tool_agent(state, cfg, runtime)

        assert result.tool_call_results == []
        state.add_degrade_reason.assert_called_with("tool_catalog_snapshot_not_available")

    def test_no_tools_returns_empty_results(self) -> None:
        """Verify that empty tools returns empty results."""
        from orchestrator.langgraph.nodes_dynamic import run_tool_agent

        # Mock state
        state = MagicMock()
        state.incident_id = "test-incident"
        state.session_id = "test-session"
        state.datasource_id = None
        state.a3_max_calls = 6
        state.a3_max_total_latency_ms = 8000
        state.tool_call_results = []
        state.tool_calls_written = 0
        state.incident_context = {}
        state.evidence_plan = {}
        state.add_degrade_reason = MagicMock()

        # Create empty snapshot
        snapshot = build_tool_catalog_snapshot(toolset_ids=[], tool_specs=[])

        # Mock adapter that returns empty tools
        mock_adapter = MagicMock()
        mock_adapter.to_openai_tools_for_graph.return_value = []

        # Mock runtime
        runtime = MagicMock()
        runtime.get_fc_adapter.return_value = mock_adapter

        # Mock config
        cfg = OrchestratorConfig()

        result = run_tool_agent(state, cfg, runtime)

        assert result.tool_call_results == []
        state.add_degrade_reason.assert_called_with("no_tools_available")

    def test_llm_not_configured_returns_empty(self) -> None:
        """Verify that missing LLM config returns empty results."""
        from orchestrator.langgraph.nodes_dynamic import run_tool_agent

        # Mock state
        state = MagicMock()
        state.incident_id = "test-incident"
        state.session_id = "test-session"
        state.datasource_id = None
        state.a3_max_calls = 6
        state.a3_max_total_latency_ms = 8000
        state.tool_call_results = []
        state.tool_calls_written = 0
        state.incident_context = {}
        state.evidence_plan = {}
        state.add_degrade_reason = MagicMock()

        # Mock adapter with tools
        mock_adapter = MagicMock()
        mock_adapter.to_openai_tools_for_graph.return_value = [
            {"type": "function", "function": {"name": "test_tool"}}
        ]

        # Mock runtime with no skill agent (LLM not configured)
        runtime = MagicMock()
        runtime.get_fc_adapter.return_value = mock_adapter
        runtime._skill_agent = None

        # Mock config
        cfg = OrchestratorConfig()

        result = run_tool_agent(state, cfg, runtime)

        assert result.tool_call_results == []
        state.add_degrade_reason.assert_called_with("llm_not_configured")


class TestBudgetEnforcement:
    """Tests for budget enforcement in run_tool_agent."""

    def test_max_calls_budget_enforced(self) -> None:
        """Verify that max_calls budget is enforced."""
        from orchestrator.langgraph.nodes_dynamic import run_tool_agent

        # Create snapshot with tools
        spec = ToolSpec(name="test_tool", description="Test", kind="metrics")
        snapshot = build_tool_catalog_snapshot(toolset_ids=["ts1"], tool_specs=[spec])

        # Mock state with low max_calls
        state = MagicMock()
        state.incident_id = "test-incident"
        state.a3_max_calls = 1  # Very low limit
        state.a3_max_total_latency_ms = 8000
        state.tool_call_results = []
        state.tool_calls_written = 0
        state.datasource_id = "ds1"
        state.session_id = "session1"
        state.incident_context = {}
        state.evidence_plan = {}
        state.evidence_ids = []
        state.evidence_meta = []
        state.add_degrade_reason = MagicMock()

        # Mock LLM that returns multiple tool calls
        mock_response = MagicMock()
        mock_response.tool_calls = [
            {"name": "test_tool", "args": {}, "id": "call1"},
            {"name": "test_tool", "args": {}, "id": "call2"},  # Should be limited
        ]

        mock_llm_with_tools = MagicMock()
        mock_llm_with_tools.invoke.return_value = mock_response

        mock_llm = MagicMock()
        mock_llm.bind_tools.return_value = mock_llm_with_tools

        mock_skill_agent = MagicMock()
        mock_skill_agent.configured = True
        mock_skill_agent._get_llm.return_value = mock_llm

        # Mock runtime
        runtime = MagicMock()
        runtime.get_tool_catalog_snapshot.return_value = snapshot
        runtime._skill_agent = mock_skill_agent
        runtime.call_tool.return_value = {"result": "ok"}
        runtime.report_observation = MagicMock()
        runtime.report_tool_call = MagicMock()

        # Mock config
        cfg = OrchestratorConfig(tool_agent_max_calls_per_round=1)

        result = run_tool_agent(state, cfg, runtime)

        # Should have at most 1 call due to max_calls_per_round
        assert len(result.tool_call_results) <= 1


class TestHelperFunctions:
    """Tests for helper functions."""

    def test_infer_kind_from_tool_name_metrics(self) -> None:
        """Verify kind inference for metrics tools."""
        from orchestrator.langgraph.nodes_dynamic import _infer_kind_from_tool_name

        assert _infer_kind_from_tool_name("prometheus_query") == "metrics"
        assert _infer_kind_from_tool_name("prometheus_instant") == "metrics"
        assert _infer_kind_from_tool_name("metrics_query") == "metrics"

    def test_infer_kind_from_tool_name_logs(self) -> None:
        """Verify kind inference for logs tools."""
        from orchestrator.langgraph.nodes_dynamic import _infer_kind_from_tool_name

        assert _infer_kind_from_tool_name("loki_search") == "logs"
        assert _infer_kind_from_tool_name("logs_query") == "logs"

    def test_infer_kind_from_tool_name_traces(self) -> None:
        """Verify kind inference for traces tools."""
        from orchestrator.langgraph.nodes_dynamic import _infer_kind_from_tool_name

        assert _infer_kind_from_tool_name("tempo_query") == "traces"
        assert _infer_kind_from_tool_name("trace_search") == "traces"

    def test_infer_kind_from_tool_name_unknown(self) -> None:
        """Verify kind inference for unknown tools."""
        from orchestrator.langgraph.nodes_dynamic import _infer_kind_from_tool_name

        assert _infer_kind_from_tool_name("unknown_tool") == "query"
        assert _infer_kind_from_tool_name("custom_tool") == "query"


class TestConfigExtensions:
    """Tests for OrchestratorConfig extensions (FC2B)."""

    def test_default_budget_values(self) -> None:
        """Verify default budget values are set."""
        cfg = OrchestratorConfig()

        assert cfg.tool_agent_max_rounds == 5
        assert cfg.tool_agent_max_calls_per_round == 3
        assert cfg.tool_agent_round_timeout_s == 60.0
        assert cfg.tool_agent_stop_on_error is False

    def test_custom_budget_values(self) -> None:
        """Verify custom budget values can be set."""
        cfg = OrchestratorConfig(
            tool_agent_max_rounds=10,
            tool_agent_max_calls_per_round=5,
            tool_agent_round_timeout_s=120.0,
            tool_agent_stop_on_error=True,
        )

        assert cfg.tool_agent_max_rounds == 10
        assert cfg.tool_agent_max_calls_per_round == 5
        assert cfg.tool_agent_round_timeout_s == 120.0
        assert cfg.tool_agent_stop_on_error is True