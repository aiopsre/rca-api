from __future__ import annotations

import time
from typing import Any

from ..runtime.runtime import OrchestratorRuntime
from ..state import GraphState


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
