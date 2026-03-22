"""Dynamic tool execution nodes for LangGraph.

This module provides nodes that can dynamically discover and execute
tools at runtime, rather than hardcoding specific tool names in the graph.
"""
from __future__ import annotations

import json
import os
import time
from typing import Any, TYPE_CHECKING

if TYPE_CHECKING:
    from ..middleware.chain import MiddlewareChain
    from ..runtime.resolved_context import ResolvedAgentContext
    from ..runtime.runtime import OrchestratorRuntime
    from ..state import GraphState

from ..constants import DegradeReason
from ..runtime.fc_adapter import FunctionCallingToolAdapter
from ..runtime.tool_catalog import ExecutedToolCall
from ..state.tool_call_plan import (
    ToolCallPlan,
    ToolCallItem,
    build_default_tool_call_plan,
)
from .config import OrchestratorConfig
from .executor import execute_group_concurrent, ToolExecutionResult
from .helpers import (
    append_evidence,
    query_result_is_no_data,
    query_result_size_bytes,
)
from .reporting import report_node_action, report_group_execution


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
        reason = DegradeReason.TOOL_DISCOVERY_EMPTY.value
        state.add_degrade_reason(reason)
        report_node_action(
            state,
            runtime,
            node_name="plan_tool_calls",
            tool_name="tool.discover",
            request_json={},
            response_json={
                "status": "no_tools",
                "tool_count": 0,
                "reason": reason,
            },
            started_ms=started_ms,
            status="ok",
            count_in_state=False,
        )
        state.tool_call_plan = {}
        return state

    # 3. Try to use Skills for planning
    plan_result: dict[str, Any] | None = None
    skill_error_reason: str | None = None
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
        except Exception as exc:  # noqa: BLE001
            plan_result = None
            skill_error_reason = f"{DegradeReason.SKILL_EXECUTE_FAILED.value}:{str(exc)[:64]}"

    # 4. Build the plan
    if isinstance(plan_result, dict) and plan_result.get("items"):
        state.tool_call_plan = plan_result
    else:
        # Fallback: generate default plan from available tools with reason tracking
        reason = skill_error_reason or DegradeReason.TOOL_DISCOVERY_EMPTY.value
        state.add_degrade_reason(reason)

        default_plan = build_default_tool_call_plan(
            available_tools=available_tools,
            incident_context=state.incident_context,
        )
        state.tool_call_plan = default_plan.to_dict()

        # Report fallback observation
        report_node_action(
            state,
            runtime,
            node_name="plan_tool_calls",
            tool_name="skill.fallback",
            request_json={"available_tools_count": len(available_tools)},
            response_json={
                "status": "fallback",
                "reason": reason,
                "plan_items_count": len(default_plan.items),
            },
            started_ms=started_ms,
            status="ok",
            count_in_state=False,
        )

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
    """Execute the tool call plan with concurrent group execution.

    Iterates through the tool call plan and executes each parallel group
    concurrently using ThreadPoolExecutor, collecting results and creating evidence.

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

    # Get concurrent execution configuration
    max_workers = getattr(cfg, 'tool_execution_max_workers', 5)
    group_timeout_s = getattr(cfg, 'tool_execution_group_timeout_s', 30.0)

    for group_idx, group_indices in enumerate(execution_groups):
        # Prepare the group's tool calls
        group_items = [
            (item_idx, plan.items[item_idx])
            for item_idx in group_indices
            if item_idx < len(plan.items)
        ]

        if not group_items:
            continue

        # Track group execution time
        group_start_ms = int(time.time() * 1000)

        # Execute the group concurrently
        group_results = execute_group_concurrent(
            items=group_items,
            runtime=runtime,
            group_idx=group_idx,
            max_workers=max_workers,
            group_timeout_s=group_timeout_s,
        )

        group_latency_ms = max(1, int(time.time() * 1000) - group_start_ms)

        # Process results
        group_success = 0
        group_error = 0
        group_timeout = 0

        for exec_result in group_results:
            executed_call = _execution_result_to_executed_call(exec_result)
            results.append(executed_call)

            if exec_result.status == "ok":
                group_success += 1
            else:
                group_error += 1
                if exec_result.error and "timeout" in exec_result.error.lower():
                    group_timeout += 1

            # Save evidence for successful calls
            if exec_result.status == "ok" and exec_result.result:
                item = plan.items[exec_result.item_idx]
                _save_tool_call_evidence(state, runtime, item, executed_call)

            # Report observation for each tool call
            _report_tool_observation(runtime, exec_result)

        # Report group-level metrics
        report_group_execution(
            state,
            runtime,
            group_idx=group_idx,
            parallel_count=len(group_items),
            group_latency_ms=group_latency_ms,
            success_count=group_success,
            error_count=group_error,
            timeout_count=group_timeout,
        )

    state.tool_call_results = results

    # Report overall execution summary
    success_count = sum(1 for r in results if r.status == "ok")
    error_count = sum(1 for r in results if r.status == "error")
    max_parallel = max((len(g) for g in execution_groups), default=0)

    report_node_action(
        state,
        runtime,
        node_name="execute_tool_calls",
        tool_name="tool.execute",
        request_json={
            "plan_items_count": len(plan.items),
            "execution_groups_count": len(execution_groups),
            "max_workers": max_workers,
        },
        response_json={
            "status": "ok",
            "success_count": success_count,
            "error_count": error_count,
            "total_results": len(results),
            "max_parallel_in_group": max_parallel,
        },
        started_ms=started_ms,
        status="ok",
        count_in_state=False,
    )

    return state


def _execution_result_to_executed_call(result: ToolExecutionResult) -> ExecutedToolCall:
    """Convert ToolExecutionResult to ExecutedToolCall.

    FC3B: Unified result model for tool executions.

    Args:
        result: ToolExecutionResult to convert.

    Returns:
        ExecutedToolCall suitable for state storage.
    """
    return ExecutedToolCall(
        tool_name=result.tool,
        request_json=result.params,
        response_json=result.result,
        latency_ms=result.latency_ms,
        source=result.source,
        status=result.status,
        error=result.error or "",
        group_idx=result.group_idx,
        item_idx=result.item_idx,
    )


def _report_tool_observation(
    runtime: "OrchestratorRuntime",
    exec_result: ToolExecutionResult,
) -> None:
    """Report an observation for a tool execution result.

    Args:
        runtime: Orchestrator runtime instance.
        exec_result: Tool execution result to report.
    """
    try:
        runtime.report_observation(
            tool=f"tool.execute.{exec_result.tool}",
            node_name="execute_tool_calls",
            params={
                "tool": exec_result.tool,
                "params": exec_result.params,
                "query_type": exec_result.query_type,
                "purpose": exec_result.purpose,
                "group_idx": exec_result.group_idx,
            },
            response={
                "status": exec_result.status,
                "latency_ms": exec_result.latency_ms,
                **({"error": exec_result.error} if exec_result.error else {}),
                "result_summary": _summarize_result(exec_result.result),
            },
            evidence_ids=[],
        )
    except Exception:  # noqa: BLE001
        pass


def _save_tool_call_evidence(
    state: "GraphState",
    runtime: "OrchestratorRuntime",
    item: ToolCallItem,
    result: ExecutedToolCall,
) -> None:
    """Save evidence from a tool call result.

    Args:
        state: Current graph state.
        runtime: Orchestrator runtime instance.
        item: Tool call item that was executed.
        result: ExecutedToolCall with execution result.
    """
    try:
        query_result = result.response_json
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


def run_tool_agent(
    state: "GraphState",
    cfg: OrchestratorConfig,
    runtime: "OrchestratorRuntime",
) -> "GraphState":
    """Use function-calling agent to iteratively execute tool calls.

    This node replaces the old plan_tool_calls + execute_tool_calls dual-node pattern,
    using LLM function calling for iterative tool calls until termination conditions are met.

    Termination conditions:
    - LLM no longer returns tool calls
    - Maximum round limit reached
    - Budget limit reached (calls/bytes/latency)
    - Unrecoverable error occurs

    Args:
        state: Current graph state.
        cfg: Orchestrator configuration.
        runtime: Orchestrator runtime instance.

    Returns:
        Updated graph state with tool_call_results and evidence.
    """
    started_ms = int(time.time() * 1000)

    # Phase HM2: Apply middleware chain if enabled
    # This integrates the middleware into the production execution path.
    middleware_chain: "MiddlewareChain | None" = getattr(cfg, "middleware_chain", None)
    middleware_enabled: bool = getattr(cfg, "middleware_enabled", False)
    if middleware_enabled and middleware_chain is not None:
        # Build minimal context for middleware
        from ..middleware.base import AgentRequest
        from ..runtime.resolved_context import ResolvedAgentContext

        context: "ResolvedAgentContext | None" = None
        if state.agent_context:
            try:
                context = ResolvedAgentContext.from_json(
                    json.dumps(state.agent_context)
                )
            except (json.JSONDecodeError, TypeError):
                context = None

        if context is not None:
            request = AgentRequest(
                system_prompt="",
                user_prompt="",
                metadata={"node": "run_tool_agent", "surface": "graph"},
            )
            # Prepare request through middleware chain
            # This allows middleware to inject context, record observations, etc.
            prepared = middleware_chain.prepare(
                state=state,
                context=context,
                request=request,
                config={"mode": "fc_surface", "surface": "graph"},
            )
            # The prepared request can be used for logging/observation
            # The actual tool filtering is handled by the adapter below
            if prepared.metadata:
                state.degrade_reasons.append(
                    f"middleware_applied:{','.join(prepared.metadata.keys())}"
                )

    # 1. Get FunctionCallingToolAdapter from runtime (FC3A: unified adapter)
    adapter = runtime.get_fc_adapter()
    if adapter is None:
        state.add_degrade_reason("tool_catalog_snapshot_not_available")
        report_node_action(
            state,
            runtime,
            node_name="run_tool_agent",
            tool_name="tool.catalog",
            request_json={},
            response_json={
                "status": "no_snapshot",
                "reason": "tool_catalog_snapshot_not_available",
            },
            started_ms=started_ms,
            status="ok",
            count_in_state=False,
        )
        state.tool_call_results = []
        return state

    # 2. Get OpenAI tools format from adapter
    # Use per-surface filtering for LangGraph FC agent visibility
    openai_tools = adapter.to_openai_tools_for_graph()
    if not openai_tools:
        state.add_degrade_reason("no_tools_available")
        report_node_action(
            state,
            runtime,
            node_name="run_tool_agent",
            tool_name="tool.catalog",
            request_json={},
            response_json={
                "status": "no_tools",
                "tool_count": 0,
            },
            started_ms=started_ms,
            status="ok",
            count_in_state=False,
        )
        state.tool_call_results = []
        return state

    # 3. Get LLM and bind tools
    llm = _get_llm_for_tool_agent(runtime)
    if llm is None:
        state.add_degrade_reason("llm_not_configured")
        report_node_action(
            state,
            runtime,
            node_name="run_tool_agent",
            tool_name="llm.bind_tools",
            request_json={"tool_count": len(openai_tools)},
            response_json={
                "status": "error",
                "reason": "llm_not_configured",
            },
            started_ms=started_ms,
            status="error",
            count_in_state=False,
        )
        state.tool_call_results = []
        return state

    llm_with_tools = llm.bind_tools(openai_tools)

    # 4. Build initial messages from state context
    messages = _build_initial_messages(state)

    # 5. Configuration for budget controls
    max_rounds = getattr(cfg, "tool_agent_max_rounds", 5)
    max_calls_per_round = getattr(cfg, "tool_agent_max_calls_per_round", 3)
    stop_on_error = getattr(cfg, "tool_agent_stop_on_error", False)

    results: list[dict[str, Any]] = []
    round_idx = 0
    total_tool_calls = 0
    total_latency_ms = 0

    # 6. Iterative execution loop
    while round_idx < max_rounds:
        try:
            response = llm_with_tools.invoke(messages)
        except Exception as exc:  # noqa: BLE001
            state.add_degrade_reason(f"llm_invoke_error:{str(exc)[:64]}")
            break

        tool_calls = getattr(response, "tool_calls", []) or []

        if not tool_calls:
            # LLM decided to stop
            break

        # Limit calls per round
        if len(tool_calls) > max_calls_per_round:
            tool_calls = tool_calls[:max_calls_per_round]

        # Normalize tool calls
        normalized = adapter.normalize_tool_calls(tool_calls)

        # Validate tool calls
        validated: list[Any] = []
        for call in normalized:
            # P1 fix: Use has_fc_tool_for_graph() to reject:
            # 1. runtime-owned tools (B-class) that were never exposed
            # 2. skills-only tools that are not visible to LangGraph
            # This enforces the per-surface visibility contract.
            if not adapter.has_fc_tool_for_graph(call.tool_name):
                state.add_degrade_reason(f"tool_not_on_graph_fc_surface:{call.tool_name}")
                continue
            validated.append(call)

        if not validated:
            break

        # CRITICAL: Append the assistant message with tool_calls BEFORE ToolMessages.
        # OpenAI-compatible backends require ToolMessage to immediately follow
        # the assistant message that produced the tool_calls.
        messages.append(response)

        # Execute each tool call
        round_results: list[dict[str, Any]] = []
        for call in validated:
            # Budget check: max calls
            if total_tool_calls >= state.a3_max_calls:
                state.add_degrade_reason("max_calls_reached")
                break

            # Budget check: max latency
            if total_latency_ms >= state.a3_max_total_latency_ms:
                state.add_degrade_reason("max_latency_reached")
                break

            # FC3C: Use unified execute_tool() method
            executed_call = runtime.execute_tool(
                tool_name=call.tool_name,
                args=call.arguments,
                source="graph.fc_agent",
            )

            # Set round index for tracking
            object.__setattr__(executed_call, "round_idx", round_idx)

            call_latency_ms = executed_call.latency_ms
            total_latency_ms += call_latency_ms
            total_tool_calls += 1

            results.append(executed_call)
            round_results.append(executed_call)

            # Handle error status
            if executed_call.status == "error":
                if stop_on_error:
                    state.add_degrade_reason(f"tool_error:{call.tool_name}")

            # Save evidence for successful calls
            if executed_call.status == "ok" and executed_call.response_json:
                _save_tool_call_evidence_fc(state, runtime, call, executed_call, int(time.time() * 1000 * 1000 - call_latency_ms * 1000))

            # Report observation for each tool call
            _report_tool_observation_fc(runtime, call, executed_call, round_idx)

            # Add tool result to messages for next round
            messages.append(_build_tool_result_message(call, executed_call.response_json, executed_call.error if executed_call.error else None))

            if stop_on_error and executed_call.error:
                break

        round_idx += 1

    state.tool_call_results = results
    state.tool_calls_written = total_tool_calls

    # Report summary
    success_count = sum(1 for r in results if r.status == "ok")
    error_count = sum(1 for r in results if r.status == "error")

    report_node_action(
        state,
        runtime,
        node_name="run_tool_agent",
        tool_name="tool.agent",
        request_json={
            "tool_count": len(openai_tools),
            "max_rounds": max_rounds,
            "max_calls": state.a3_max_calls,
        },
        response_json={
            "status": "ok",
            "rounds_completed": round_idx,
            "success_count": success_count,
            "error_count": error_count,
            "total_tool_calls": total_tool_calls,
            "total_latency_ms": total_latency_ms,
        },
        started_ms=started_ms,
        status="ok",
        count_in_state=False,
    )

    return state


def _get_llm_for_tool_agent(runtime: "OrchestratorRuntime") -> Any:
    """Get LLM instance for tool agent from runtime.

    Uses the independent graph LLM (HM7-1), not prompt_first skill agent.
    Falls back to legacy _skill_agent path for backward compatibility.

    Args:
        runtime: Orchestrator runtime instance.

    Returns:
        LLM instance or None if not configured.
    """
    # Primary path: use independent graph LLM (HM7-1)
    get_graph_llm = getattr(runtime, "get_graph_llm", None)
    if callable(get_graph_llm):
        llm = get_graph_llm()
        if llm is not None:
            return llm

    # Fallback: legacy _skill_agent path (for backward compatibility)
    skill_agent = getattr(runtime, "_skill_agent", None)
    if skill_agent is None:
        return None
    if not bool(getattr(skill_agent, "configured", False)):
        return None
    try:
        return skill_agent._get_llm()  # noqa: SLF001
    except Exception:  # noqa: BLE001
        return None


def _build_initial_messages(state: "GraphState") -> list[Any]:
    """Build initial messages for tool agent from state context.

    Args:
        state: Current graph state.

    Returns:
        List of messages for the LLM.
    """
    try:
        from langchain_core.messages import HumanMessage, SystemMessage
    except ImportError:
        return []

    system_content = (
        "You are an RCA (Root Cause Analysis) tool agent.\n"
        "Use the available tools to gather diagnostic information.\n"
        "Call tools when you need metrics, logs, or other observability data.\n"
        "Stop calling tools when you have enough information.\n"
        "Do not call more tools than necessary."
    )

    user_payload = {
        "incident_id": state.incident_id or "",
        "session_id": state.session_id or "",
        "incident_context": state.incident_context,
        "evidence_plan": state.evidence_plan,
    }

    if state.datasource_id:
        user_payload["datasource_id"] = state.datasource_id

    return [
        SystemMessage(content=system_content),
        HumanMessage(content=json.dumps(user_payload, ensure_ascii=False, separators=(",", ":"))),
    ]


def _build_tool_result_message(call: Any, result: dict[str, Any], error: str | None) -> Any:
    """Build a tool result message for the next LLM round.

    Args:
        call: NormalizedToolCall that was executed.
        result: Tool execution result.
        error: Error message if any.

    Returns:
        Tool message for the LLM.
    """
    try:
        from langchain_core.messages import ToolMessage
    except ImportError:
        # Fallback to dict
        return {
            "role": "tool",
            "name": call.tool_name,
            "content": json.dumps(result if not error else {"error": error}, ensure_ascii=False),
        }

    content = json.dumps(result, ensure_ascii=False) if not error else json.dumps({"error": error})
    return ToolMessage(content=content, tool_call_id=call.call_id or call.tool_name)


def _report_tool_observation_fc(
    runtime: "OrchestratorRuntime",
    call: Any,
    executed_call: ExecutedToolCall,
    round_idx: int,
) -> None:
    """Report an observation for a FC tool execution result.

    Args:
        runtime: Orchestrator runtime instance.
        call: NormalizedToolCall that was executed.
        executed_call: ExecutedToolCall with execution result.
        round_idx: Round index for tracking.
    """
    try:
        runtime.report_observation(
            tool=f"tool.fc.{call.tool_name}",
            node_name="run_tool_agent",
            params={
                "tool": call.tool_name,
                "params": call.arguments,
                "round_idx": round_idx,
            },
            response={
                "status": executed_call.status,
                "latency_ms": executed_call.latency_ms,
                **({"error": executed_call.error} if executed_call.error else {}),
                "result_summary": _summarize_result(executed_call.response_json),
            },
            evidence_ids=[],
        )
    except Exception:  # noqa: BLE001
        pass


def _save_tool_call_evidence_fc(
    state: "GraphState",
    runtime: "OrchestratorRuntime",
    call: Any,
    executed_call: ExecutedToolCall,
    started_ms: int,
) -> None:
    """Save evidence from a FC tool call result.

    Args:
        state: Current graph state.
        runtime: Orchestrator runtime instance.
        call: NormalizedToolCall that was executed.
        executed_call: ExecutedToolCall with execution result.
        started_ms: Start time in milliseconds.
    """
    try:
        query_result = executed_call.response_json
        if not isinstance(query_result, dict):
            return

        # Determine kind from tool name
        kind = _infer_kind_from_tool_name(call.tool_name)

        # Create query request for evidence
        query_request = {
            "tool": call.tool_name,
            "params": call.arguments,
            "queryText": f"FC query for {call.tool_name}",
        }

        published = runtime.save_evidence_from_query(
            incident_id=state.incident_id or "",
            node_name="run_tool_agent",
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
            node_name="run_tool_agent",
            tool_name="evidence.save_from_fc",
            request_json={
                "incident_id": state.incident_id,
                "kind": kind,
                "tool": call.tool_name,
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
        runtime._log(  # noqa: SLF001
            f"failed to save evidence from FC tool call: tool={call.tool_name} error={exc}"
        )


def _infer_kind_from_tool_name(tool_name: str) -> str:
    """Infer evidence kind from tool name.

    Args:
        tool_name: Canonical tool name.

    Returns:
        Kind string (metrics, logs, traces, or query).
    """
    name_lower = tool_name.lower()
    if "prometheus" in name_lower or "metric" in name_lower:
        return "metrics"
    if "loki" in name_lower or "log" in name_lower:
        return "logs"
    if "trace" in name_lower or "tempo" in name_lower:
        return "traces"
    return "query"