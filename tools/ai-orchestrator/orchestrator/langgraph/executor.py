"""Concurrent tool execution utilities.

This module provides utilities for executing multiple tool calls concurrently
within parallel groups, using ThreadPoolExecutor for I/O-bound operations.

Phase FC2D: ToolExecutionResult serves as the execution envelope for tool calls,
with a source field to track the caller context.
"""
from __future__ import annotations

import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass
from typing import Any, TYPE_CHECKING

if TYPE_CHECKING:
    from ..runtime.runtime import OrchestratorRuntime
    from ..state.tool_call_plan import ToolCallItem


@dataclass
class ToolExecutionResult:
    """Result of a single tool execution (execution envelope).

    This is the execution envelope for tool calls, tracking the tool name,
    parameters, result, and execution metadata.

    Attributes:
        tool: The name of the tool that was executed.
        params: Parameters passed to the tool.
        query_type: Classification of the tool (e.g., "metrics", "logs").
        purpose: Human-readable explanation of why this tool was called.
        status: Execution status ("ok" or "error").
        result: The tool result data.
        error: Error message if status is "error".
        latency_ms: Execution time in milliseconds.
        group_idx: Index of the parallel group this execution belongs to.
        item_idx: Index of the item within the plan.
        source: Caller context (e.g., "graph", "skill", "fc_agent").
    """
    tool: str
    params: dict[str, Any]
    query_type: str
    purpose: str
    status: str
    result: dict[str, Any]
    error: str | None
    latency_ms: int
    group_idx: int
    item_idx: int
    source: str = "graph"  # New: caller context


def execute_group_concurrent(
    items: list[tuple[int, "ToolCallItem"]],  # (item_idx, item)
    runtime: "OrchestratorRuntime",
    group_idx: int,
    max_workers: int = 5,
    group_timeout_s: float = 30.0,
) -> list[ToolExecutionResult]:
    """Execute a group of tool calls concurrently.

    Uses ThreadPoolExecutor to run tool calls in parallel within the same
    execution group. Results are sorted by item_idx to preserve order.

    Args:
        items: List of (item_idx, ToolCallItem) tuples.
        runtime: Orchestrator runtime instance.
        group_idx: Index of this parallel group.
        max_workers: Maximum concurrent workers (default: 5).
        group_timeout_s: Timeout for the entire group (default: 30.0s).

    Returns:
        List of execution results sorted by item_idx.
    """
    if not items:
        return []

    # Single item optimization: no thread pool overhead
    if len(items) == 1:
        item_idx, item = items[0]
        return [_execute_single(item, item_idx, runtime, group_idx)]

    results: list[ToolExecutionResult] = []
    items_by_idx = {idx: it for idx, it in items}

    with ThreadPoolExecutor(max_workers=min(max_workers, len(items))) as executor:
        futures = {
            executor.submit(_execute_single, item, item_idx, runtime, group_idx): item_idx
            for item_idx, item in items
        }

        for future in as_completed(futures, timeout=group_timeout_s):
            item_idx = futures[future]
            try:
                result = future.result()
                results.append(result)
            except Exception as exc:
                # Timeout or other error from future
                item = items_by_idx.get(item_idx)
                if item:
                    results.append(ToolExecutionResult(
                        tool=item.tool,
                        params=item.params,
                        query_type=item.query_type,
                        purpose=item.purpose,
                        status="error",
                        result={},
                        error=f"concurrent execution error: {exc}",
                        latency_ms=0,
                        group_idx=group_idx,
                        item_idx=item_idx,
                    ))

    # Sort by item_idx to preserve order
    results.sort(key=lambda r: r.item_idx)
    return results


def _execute_single(
    item: "ToolCallItem",
    item_idx: int,
    runtime: "OrchestratorRuntime",
    group_idx: int,
) -> ToolExecutionResult:
    """Execute a single tool call.

    Args:
        item: Tool call item to execute.
        item_idx: Index of the item within the plan.
        runtime: Orchestrator runtime instance.
        group_idx: Index of the parallel group this execution belongs to.

    Returns:
        ToolExecutionResult with execution details.
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

    return ToolExecutionResult(
        tool=item.tool,
        params=item.params,
        query_type=item.query_type,
        purpose=item.purpose,
        status=status,
        result=result,
        error=error,
        latency_ms=latency_ms,
        group_idx=group_idx,
        item_idx=item_idx,
    )