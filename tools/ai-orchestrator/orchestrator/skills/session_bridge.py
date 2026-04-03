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


def write_session_patch_to_platform(
    state: GraphState,
    runtime: Any,
) -> bool:
    """将 Skills 输出的 session_patch 写回平台。

    此函数在 finalize 前调用，将 Skills 执行过程中产生的 session_patch
    通过 runtime.patch_job_session_context() API 写回平台。

    Args:
        state: 当前 GraphState，包含 session_patch 字段
        runtime: OrchestratorRuntime 实例，提供 patch_job_session_context() 方法

    Returns:
        True if patch was written successfully or patch was empty,
        False if write failed (best-effort mode, should not block finalize).
    """
    patch = state.session_patch
    if not patch:
        return True

    if not isinstance(patch, dict):
        return True

    try:
        runtime.patch_job_session_context(
            session_revision=None,  # 暂不实现乐观锁
            latest_summary=patch.get("latest_summary"),
            pinned_evidence_append=patch.get("pinned_evidence_append"),
            pinned_evidence_remove=patch.get("pinned_evidence_remove"),
            context_state_patch=patch.get("context_state_patch"),
            actor=patch.get("actor"),
            note=patch.get("note"),
            source=patch.get("source"),
        )
        return True
    except Exception:
        # Best effort 模式：失败不影响 finalize
        return False
