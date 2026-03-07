"""Dynamic tool execution nodes for LangGraph.

This module provides nodes that can dynamically discover and execute
tools at runtime, rather than hardcoding specific tool names in the graph.
"""
from __future__ import annotations

import time
from typing import Any, TYPE_CHECKING

if TYPE_CHECKING:
    from ..runtime.runtime import OrchestratorRuntime
    from ..state import GraphState

from ..state.tool_call_plan import (
    ToolCallPlan,
    ToolCallItem,
    build_default_tool_call_plan,
)
from .config import OrchestratorConfig
from .helpers import (
    append_evidence,
    query_result_is_no_data,
    query_result_size_bytes,
)
from .reporting import report_node_action


def plan_tool_calls(
    state: "GraphState",
    cfg: OrchestratorConfig,
    runtime: "OrchestratorRuntime",
) -> "GraphState":
    """Plan tool calls based on available tools and incident context.

    This node discovers available tools and creates a tool call plan.
    If a skill with the 'tool.plan' capability is available, it will
    be used to generate the plan; otherwise, a default plan is created.

    Args:
        state: Current graph state.
        cfg: Orchestrator configuration.
        runtime: Orchestrator runtime instance.

    Returns:
        Updated graph state with tool_call_plan populated.
    """
    started_ms = int(time.time() * 1000)

    # 1. Discover available tools
    discovery = runtime.discover_tools()
    available_tools = [
        {
            "name": t.tool_name,
            "tags": list(t.tags),
            "description": t.description,
            "provider_id": t.provider_id,
        }
        for t in discovery.tools
    ]

    # 2. Check if we have tools to work with
    if not available_tools:
        report_node_action(
            state,
            runtime,
            node_name="plan_tool_calls",
            tool_name="tool.discover",
            request_json={},
            response_json={"status": "no_tools", "tool_count": 0},
            started_ms=started_ms,
            status="ok",
            count_in_state=False,
        )
        state.tool_call_plan = {}
        return state

    # 3. Try to use Skills for planning
    plan_result: dict[str, Any] | None = None
    prompt_skill = getattr(runtime, "consume_prompt_skill", None)

    if callable(prompt_skill):
        try:
            consumed = prompt_skill(
                capability="tool.plan",
                graph_state=state,
                input_payload={
                    "available_tools": available_tools,
                    "incident_id": state.incident_id,
                    "incident_context": state.incident_context,
                },
            )
            if isinstance(consumed, dict):
                plan_result = consumed.get("tool_call_plan")
        except Exception:  # noqa: BLE001
            plan_result = None

    # 4. Build the plan
    if isinstance(plan_result, dict) and plan_result.get("items"):
        state.tool_call_plan = plan_result
    else:
        # Fallback: generate default plan from available tools
        default_plan = build_default_tool_call_plan(
            available_tools=available_tools,
            incident_context=state.incident_context,
        )
        state.tool_call_plan = default_plan.to_dict()

    # 5. Report planning result
    plan = ToolCallPlan.from_dict(state.tool_call_plan)
    report_node_action(
        state,
        runtime,
        node_name="plan_tool_calls",
        tool_name="tool.plan",
        request_json={
            "available_tools_count": len(available_tools),
            "has_skill": callable(prompt_skill),
        },
        response_json={
            "status": "ok",
            "plan_items_count": len(plan.items),
            "plan_tools": [item.tool for item in plan.items],
        },
        started_ms=started_ms,
        status="ok",
        count_in_state=False,
    )

    return state


def execute_tool_calls(
    state: "GraphState",
    cfg: OrchestratorConfig,
    runtime: "OrchestratorRuntime",
) -> "GraphState":
    """Execute the tool call plan.

    Iterates through the tool call plan and executes each tool call,
    collecting results and creating evidence.

    Args:
        state: Current graph state.
        cfg: Orchestrator configuration.
        runtime: Orchestrator runtime instance.

    Returns:
        Updated graph state with tool_call_results populated.
    """
    started_ms = int(time.time() * 1000)

    if not state.tool_call_plan:
        report_node_action(
            state,
            runtime,
            node_name="execute_tool_calls",
            tool_name="tool.execute",
            request_json={},
            response_json={"status": "no_plan", "message": "No tool call plan found"},
            started_ms=started_ms,
            status="ok",
            count_in_state=False,
        )
        state.tool_call_results = []
        return state

    plan = ToolCallPlan.from_dict(state.tool_call_plan)
    if plan.is_empty():
        report_node_action(
            state,
            runtime,
            node_name="execute_tool_calls",
            tool_name="tool.execute",
            request_json={},
            response_json={"status": "empty_plan", "message": "Tool call plan has no items"},
            started_ms=started_ms,
            status="ok",
            count_in_state=False,
        )
        state.tool_call_results = []
        return state

    results: list[dict[str, Any]] = []
    execution_groups = plan.get_execution_order()

    for group_idx, group_indices in enumerate(execution_groups):
        for item_idx in group_indices:
            if item_idx >= len(plan.items):
                continue

            item = plan.items[item_idx]
            result = _execute_single_tool_call(state, runtime, item, group_idx)
            results.append(result)

            # Save evidence for successful calls
            if result.get("status") == "ok" and result.get("result"):
                _save_tool_call_evidence(state, runtime, item, result)

    state.tool_call_results = results

    # Report overall execution summary
    success_count = sum(1 for r in results if r.get("status") == "ok")
    error_count = sum(1 for r in results if r.get("status") == "error")

    report_node_action(
        state,
        runtime,
        node_name="execute_tool_calls",
        tool_name="tool.execute",
        request_json={
            "plan_items_count": len(plan.items),
            "execution_groups_count": len(execution_groups),
        },
        response_json={
            "status": "ok",
            "success_count": success_count,
            "error_count": error_count,
            "total_results": len(results),
        },
        started_ms=started_ms,
        status="ok",
        count_in_state=False,
    )

    return state


def _execute_single_tool_call(
    state: "GraphState",
    runtime: "OrchestratorRuntime",
    item: ToolCallItem,
    group_idx: int,
) -> dict[str, Any]:
    """Execute a single tool call.

    Args:
        state: Current graph state.
        runtime: Orchestrator runtime instance.
        item: Tool call item to execute.
        group_idx: Index of the parallel group this call belongs to.

    Returns:
        Dictionary with execution result.
    """
    call_started_ms = int(time.time() * 1000)

    try:
        result = runtime.call_tool(tool=item.tool, params=item.params)
        status = "ok"
        error = None
    except Exception as exc:  # noqa: BLE001
        result = {}
        status = "error"
        error = str(exc)[:512]

    latency_ms = max(1, int(time.time() * 1000) - call_started_ms)

    # Report the observation
    try:
        runtime.report_observation(
            tool=f"tool.execute.{item.tool}",
            node_name="execute_tool_calls",
            params={
                "tool": item.tool,
                "params": item.params,
                "query_type": item.query_type,
                "purpose": item.purpose,
                "group_idx": group_idx,
            },
            response={
                "status": status,
                "latency_ms": latency_ms,
                **({"error": error} if error else {}),
                "result_summary": _summarize_result(result),
            },
            evidence_ids=[],
        )
    except Exception:  # noqa: BLE001
        pass

    return {
        "tool": item.tool,
        "params": item.params,
        "query_type": item.query_type,
        "purpose": item.purpose,
        "status": status,
        "result": result,
        "error": error,
        "latency_ms": latency_ms,
        "group_idx": group_idx,
    }


def _save_tool_call_evidence(
    state: "GraphState",
    runtime: "OrchestratorRuntime",
    item: ToolCallItem,
    result: dict[str, Any],
) -> None:
    """Save evidence from a tool call result.

    Args:
        state: Current graph state.
        runtime: Orchestrator runtime instance.
        item: Tool call item that was executed.
        result: Execution result.
    """
    try:
        query_result = result.get("result", {})
        if not isinstance(query_result, dict):
            return

        # Determine the kind based on query_type
        kind = item.query_type or item.evidence_kind or "query"

        # Create query request for evidence
        query_request = {
            "tool": item.tool,
            "params": item.params,
            "queryText": item.purpose or f"Dynamic {kind} query",
        }

        started_ms = int(time.time() * 1000)

        published = runtime.save_evidence_from_query(
            incident_id=state.incident_id or "",
            node_name="execute_tool_calls",
            kind=kind,
            query=query_request,
            result=query_result,
            query_hash_source=query_request,
        )

        evidence_id = published.evidence_id
        no_data = query_result_is_no_data(query_result)
        append_evidence(state, evidence_id, source=kind, no_data=no_data, conflict_hint=False)

        report_node_action(
            state,
            runtime,
            node_name="execute_tool_calls",
            tool_name="evidence.save_from_tool_call",
            request_json={
                "incident_id": state.incident_id,
                "kind": kind,
                "tool": item.tool,
                "idempotency_key": published.idempotency_key,
            },
            response_json={
                "status": "ok",
                "evidence_id": evidence_id,
                "no_data": no_data,
                "result_size_bytes": query_result_size_bytes(query_result),
            },
            started_ms=started_ms,
            status="ok",
            evidence_ids=[evidence_id],
        )

    except Exception as exc:  # noqa: BLE001
        # Log but don't fail the entire execution
        runtime._log(  # noqa: SLF001
            f"failed to save evidence from tool call: tool={item.tool} error={exc}"
        )


def _summarize_result(result: dict[str, Any]) -> dict[str, Any]:
    """Summarize a tool result for logging.

    Args:
        result: Tool result dictionary.

    Returns:
        Summary dictionary.
    """
    summary: dict[str, Any] = {
        "result_type": "dict",
        "keys": sorted(str(key) for key in result.keys())[:8],
    }

    output = result.get("output")
    if isinstance(output, dict):
        summary["output_keys"] = sorted(str(key) for key in output.keys())[:8]
    elif output is not None:
        summary["output_type"] = type(output).__name__

    # Add result size info
    result_size = query_result_size_bytes(result)
    if result_size > 0:
        summary["result_size_bytes"] = result_size

    return summary