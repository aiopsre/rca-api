"""Reporting utilities for LangGraph nodes.

This module provides functions for reporting tool calls, observations,
and execution metrics.
"""
from __future__ import annotations

import time
from typing import Any

from ..runtime.runtime import OrchestratorRuntime
from ..state import GraphState


# Concurrent execution metric names
TOOL_GROUP_LATENCY_MS = "tool_group_latency_ms"
TOOL_GROUP_PARALLEL_COUNT = "tool_group_parallel_count"
TOOL_GROUP_TIMEOUT_TOTAL = "tool_group_timeout_total"


def report_node_action(
    state: GraphState,
    runtime: OrchestratorRuntime,
    *,
    node_name: str,
    tool_name: str,
    request_json: dict[str, Any],
    response_json: dict[str, Any] | None,
    started_ms: int,
    status: str,
    error: str | None = None,
    evidence_ids: list[str] | None = None,
    count_in_state: bool = True,
) -> None:
    runtime.report_tool_call(
        node_name=node_name,
        tool_name=tool_name,
        request_json=request_json,
        response_json=response_json,
        latency_ms=max(1, int(time.time() * 1000) - started_ms),
        status=status,
        error=error,
        evidence_ids=evidence_ids,
    )
    if count_in_state:
        state.tool_calls_written += 1


def report_group_execution(
    state: GraphState,
    runtime: OrchestratorRuntime,
    *,
    group_idx: int,
    parallel_count: int,
    group_latency_ms: int,
    success_count: int,
    error_count: int,
    timeout_count: int,
) -> None:
    """Report parallel group execution metrics.

    Args:
        state: Current graph state.
        runtime: Orchestrator runtime instance.
        group_idx: Index of the parallel group.
        parallel_count: Number of tools executed in parallel in this group.
        group_latency_ms: Total latency for the group execution.
        success_count: Number of successful tool executions.
        error_count: Number of failed tool executions.
        timeout_count: Number of timed out tool executions.
    """
    try:
        runtime.report_observation(
            tool="tool.execute.group",
            node_name="execute_tool_calls",
            params={
                "group_idx": group_idx,
                "parallel_count": parallel_count,
            },
            response={
                "status": "ok",
                "group_latency_ms": group_latency_ms,
                "success_count": success_count,
                "error_count": error_count,
                "timeout_count": timeout_count,
            },
            evidence_ids=[],
        )
    except Exception:  # noqa: BLE001
        pass
