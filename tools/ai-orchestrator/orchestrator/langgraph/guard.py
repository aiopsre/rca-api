from __future__ import annotations

from typing import Any, Callable

from ..runtime.runtime import OrchestratorRuntime
from ..state import GraphState


def guard(
    node_name: str,
    fn: Callable[[GraphState], Any],
    runtime: OrchestratorRuntime,
) -> Callable[[GraphState], Any]:
    def wrapped(state: GraphState) -> Any:
        if state.last_error:
            return state
        if runtime.is_lease_lost():
            reason = runtime.lease_lost_reason() or "lease_renew_failed"
            state.last_error = f"{node_name}: lease_lost: {reason}"
            return state
        try:
            return fn(state)
        except Exception as exc:  # noqa: BLE001
            state.last_error = f"{node_name}: {exc}"
            return state

    return wrapped


def is_finalize_succeeded(state: GraphState) -> bool:
    if not state.finalized:
        return False
    return not bool(str(state.last_error or "").strip())
