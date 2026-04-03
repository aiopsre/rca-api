"""Tests for dynamic tool execution components."""
from __future__ import annotations

import pytest

from orchestrator.runtime.tool_discovery import (
    ToolDescriptor,
    ToolDiscoveryResult,
    _infer_tags_from_tool_name,
    _create_tool_descriptor,
)
from orchestrator.state.tool_call_plan import (
    ToolCallItem,
    ToolCallPlan,
    build_default_tool_call_plan,
)
from orchestrator.skills.capabilities import (
    get_capability_definition,
    list_capabilities,
)


class TestToolDiscoveryTagInference:
    """Tests for tool tag inference from tool names."""

    def test_metrics_tag_from_prometheus(self):
        tags = _infer_tags_from_tool_name("prometheus_query")
        assert "metrics" in tags
        assert "query" in tags

    def test_metrics_tag_from_victoria(self):
        tags = _infer_tags_from_tool_name("victoria_metrics")
        assert "metrics" in tags

    def test_logs_tag_from_loki(self):
        tags = _infer_tags_from_tool_name("loki_search")
        assert "logs" in tags
        assert "search" in tags

    def test_traces_tag_from_jaeger(self):
        tags = _infer_tags_from_tool_name("jaeger_traces")
        assert "traces" in tags

    def test_incidents_tag_from_alertmanager(self):
        tags = _infer_tags_from_tool_name("alertmanager_alerts")
        assert "incidents" in tags

    def test_unknown_tool_returns_empty_tags(self):
        tags = _infer_tags_from_tool_name("unknown_random_tool")
        assert tags == ()


class TestToolDescriptor:
    """Tests for ToolDescriptor."""

    def test_create_basic_descriptor(self):
        desc = ToolDescriptor(
            tool_name="test_tool",
            description="A test tool",
        )
        assert desc.tool_name == "test_tool"
        assert desc.description == "A test tool"
        assert desc.tags == ()

    def test_create_descriptor_with_tags(self):
        desc = ToolDescriptor(
            tool_name="prometheus_query",
            tags=("metrics", "query"),
        )
        assert desc.tool_name == "prometheus_query"
        assert "metrics" in desc.tags

    def test_create_tool_descriptor_with_inferred_tags(self):
        desc = _create_tool_descriptor(
            tool_name="prometheus_query",
            provider_id="mcp_server_1",
        )
        assert desc.tool_name == "prometheus_query"
        assert desc.provider_id == "mcp_server_1"
        assert "metrics" in desc.tags


class TestToolDiscoveryResult:
    """Tests for ToolDiscoveryResult."""

    def test_empty_result(self):
        result = ToolDiscoveryResult(tools=(), by_tag={})
        assert result.tool_names() == []
        assert result.tools == ()

    def test_find_by_tag(self):
        desc1 = ToolDescriptor(tool_name="prometheus_query", tags=("metrics",))
        desc2 = ToolDescriptor(tool_name="loki_search", tags=("logs",))

        result = ToolDiscoveryResult(
            tools=(desc1, desc2),
            by_tag={"metrics": [desc1], "logs": [desc2]},
        )

        metrics_tools = result.find_by_tag("metrics")
        assert len(metrics_tools) == 1
        assert metrics_tools[0].tool_name == "prometheus_query"

        logs_tools = result.find_by_tag("logs")
        assert len(logs_tools) == 1
        assert logs_tools[0].tool_name == "loki_search"

    def test_find_by_pattern(self):
        desc1 = ToolDescriptor(tool_name="prometheus_query")
        desc2 = ToolDescriptor(tool_name="prometheus_write")
        desc3 = ToolDescriptor(tool_name="loki_search")

        result = ToolDiscoveryResult(tools=(desc1, desc2, desc3), by_tag={})

        prometheus_tools = result.find_by_pattern("prometheus_*")
        assert len(prometheus_tools) == 2

    def test_has_tools_for_tag(self):
        desc = ToolDescriptor(tool_name="prometheus_query", tags=("metrics",))
        result = ToolDiscoveryResult(tools=(desc,), by_tag={"metrics": [desc]})

        assert result.has_tools_for_tag("metrics") is True
        assert result.has_tools_for_tag("logs") is False


class TestToolCallItem:
    """Tests for ToolCallItem."""

    def test_create_basic_item(self):
        item = ToolCallItem(
            tool="prometheus_query",
            params={"promql": "sum(up)"},
        )
        assert item.tool == "prometheus_query"
        assert item.params == {"promql": "sum(up)"}
        assert item.query_type == ""
        assert item.optional is False

    def test_item_to_dict(self):
        item = ToolCallItem(
            tool="prometheus_query",
            params={"promql": "sum(up)"},
            query_type="metrics",
            purpose="Query for analysis",
        )
        d = item.to_dict()
        assert d["tool"] == "prometheus_query"
        assert d["params"] == {"promql": "sum(up)"}
        assert d["query_type"] == "metrics"
        assert d["purpose"] == "Query for analysis"
        assert "optional" not in d  # False is not serialized

    def test_item_from_dict(self):
        d = {
            "tool": "loki_search",
            "params": {"query": "error"},
            "query_type": "logs",
        }
        item = ToolCallItem.from_dict(d)
        assert item.tool == "loki_search"
        assert item.params == {"query": "error"}
        assert item.query_type == "logs"


class TestToolCallPlan:
    """Tests for ToolCallPlan."""

    def test_empty_plan(self):
        plan = ToolCallPlan()
        assert plan.is_empty() is True
        assert plan.item_count() == 0

    def test_add_item(self):
        plan = ToolCallPlan()
        item = ToolCallItem(tool="test_tool", params={})
        idx = plan.add_item(item)
        assert idx == 0
        assert plan.item_count() == 1

    def test_plan_to_dict(self):
        item = ToolCallItem(tool="test_tool", params={"key": "value"})
        plan = ToolCallPlan(items=[item])
        plan.add_parallel_group([0])

        d = plan.to_dict()
        assert len(d["items"]) == 1
        assert d["items"][0]["tool"] == "test_tool"
        assert d["parallel_groups"] == [[0]]

    def test_plan_from_dict(self):
        d = {
            "items": [
                {"tool": "tool1", "params": {}},
                {"tool": "tool2", "params": {}},
            ],
            "parallel_groups": [[0, 1]],
        }
        plan = ToolCallPlan.from_dict(d)
        assert plan.item_count() == 2
        assert len(plan.parallel_groups) == 1
        assert plan.parallel_groups[0] == [0, 1]

    def test_get_execution_order_with_groups(self):
        item1 = ToolCallItem(tool="tool1", params={})
        item2 = ToolCallItem(tool="tool2", params={})
        item3 = ToolCallItem(tool="tool3", params={})

        plan = ToolCallPlan(items=[item1, item2, item3])
        plan.add_parallel_group([0, 1])
        plan.add_parallel_group([2])

        order = plan.get_execution_order()
        assert order == [[0, 1], [2]]

    def test_get_execution_order_without_groups(self):
        item1 = ToolCallItem(tool="tool1", params={})
        item2 = ToolCallItem(tool="tool2", params={})

        plan = ToolCallPlan(items=[item1, item2])

        order = plan.get_execution_order()
        assert order == [[0, 1]]  # All items in parallel by default

    def test_validate_valid_plan(self):
        item1 = ToolCallItem(tool="tool1", params={})
        item2 = ToolCallItem(tool="tool2", params={})

        plan = ToolCallPlan(items=[item1, item2])
        plan.add_parallel_group([0, 1])

        errors = plan.validate()
        assert errors == []

    def test_validate_empty_tool_name(self):
        item = ToolCallItem(tool="", params={})
        plan = ToolCallPlan(items=[item])

        errors = plan.validate()
        assert len(errors) == 1
        assert "tool" in errors[0]


class TestBuildDefaultToolCallPlan:
    """Tests for default tool call plan builder."""

    def test_build_plan_with_metrics_and_logs(self):
        tools = [
            {"name": "prometheus_query", "tags": ["metrics"]},
            {"name": "loki_search", "tags": ["logs"]},
        ]
        plan = build_default_tool_call_plan(tools, {"service": "api", "namespace": "prod"})

        assert plan.item_count() == 2
        tool_names = [item.tool for item in plan.items]
        assert "prometheus_query" in tool_names
        assert "loki_search" in tool_names

    def test_build_plan_with_only_metrics(self):
        tools = [
            {"name": "prometheus_query", "tags": ["metrics"]},
        ]
        plan = build_default_tool_call_plan(tools, {})

        assert plan.item_count() == 1
        assert plan.items[0].tool == "prometheus_query"
        assert plan.items[0].query_type == "metrics"

    def test_build_plan_empty_tools(self):
        plan = build_default_tool_call_plan([], {})
        assert plan.is_empty() is True

    def test_build_plan_ignores_unknown_tools(self):
        tools = [
            {"name": "unknown_tool", "tags": ["unknown"]},
        ]
        plan = build_default_tool_call_plan(tools, {})
        assert plan.is_empty() is True


class TestToolPlanCapability:
    """Tests for tool.plan capability definition."""

    def test_capability_is_registered(self):
        assert "tool.plan" in list_capabilities()

    def test_capability_definition_exists(self):
        definition = get_capability_definition("tool.plan")
        assert definition is not None
        assert definition.capability == "tool.plan"
        assert definition.stage == "plan_tool_calls"

    def test_capability_output_contract(self):
        definition = get_capability_definition("tool.plan")
        contract = definition.output_contract

        assert "payload" in contract
        assert "tool_call_plan" in contract["payload"]