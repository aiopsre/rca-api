from __future__ import annotations

from typing import Any

from ..state import GraphState


def load_session_snapshot_into_state(state: GraphState, snapshot: dict[str, Any] | None) -> GraphState:
    payload = snapshot if isinstance(snapshot, dict) else {}
    state.session_snapshot = dict(payload)
    state.session_id = str(payload.get("session_id") or state.session_id or "").strip() or None
    latest_summary = payload.get("latest_summary")
    if isinstance(latest_summary, dict):
        state.latest_summary = dict(latest_summary)
    pinned_evidence = payload.get("pinned_evidence")
    if isinstance(pinned_evidence, list):
        refs: list[str] = []
        for item in pinned_evidence:
            if not isinstance(item, dict):
                continue
            for key in ("evidence_id", "evidenceID", "id"):
                value = str(item.get(key) or "").strip()
                if value:
                    refs.append(value)
                    break
        state.pinned_evidence_refs = refs
    context_state = payload.get("context_state")
    if isinstance(context_state, dict):
        state.session_context = dict(context_state)
    return state


def apply_session_patch_to_state(state: GraphState, snapshot: dict[str, Any] | None) -> GraphState:
    if isinstance(snapshot, dict):
        return load_session_snapshot_into_state(state, snapshot)
    return state
