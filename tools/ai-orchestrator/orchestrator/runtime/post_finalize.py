from __future__ import annotations

from dataclasses import dataclass
import json
from typing import Any, Callable

from ..tools_rca_api import RCAApiClient


@dataclass(frozen=True)
class PostFinalizeSnapshot:
    incident_id: str
    job_id: str
    verification_plan: dict[str, Any] | None
    kb_refs: list[dict[str, Any]]
    target_toolcall_seq: int | None


def _parse_json_object(value: Any) -> dict[str, Any] | None:
    if isinstance(value, dict):
        return value
    if not isinstance(value, str):
        return None
    raw = value.strip()
    if not raw:
        return None
    try:
        parsed = json.loads(raw)
    except json.JSONDecodeError:
        return None
    if isinstance(parsed, dict):
        return parsed
    return None


def _coerce_positive_int(value: Any) -> int | None:
    try:
        parsed = int(value)
    except (TypeError, ValueError):
        return None
    if parsed > 0:
        return parsed
    return None


class PostFinalizeObserver:
    def __init__(
        self,
        *,
        client: RCAApiClient,
        execute_with_retry: Callable[[str, Callable[[], Any]], Any],
        log_func: Callable[[str], None] | None = None,
    ) -> None:
        self._client = client
        self._execute_with_retry = execute_with_retry
        self._log_func = log_func

    def observe(self, *, incident_id: str, job_id: str) -> PostFinalizeSnapshot:
        normalized_incident_id = str(incident_id).strip()
        normalized_job_id = str(job_id).strip()
        if not normalized_incident_id:
            raise RuntimeError("incident_id is required for post-finalize observation")
        if not normalized_job_id:
            raise RuntimeError("job_id is required for post-finalize observation")

        incident = self._execute_with_retry(
            "post_finalize.get_incident",
            lambda: self._client.get_incident(normalized_incident_id),
        )
        diagnosis_obj = _parse_json_object(
            incident.get("diagnosisJSON") if isinstance(incident, dict) else None
        ) or _parse_json_object(
            incident.get("diagnosis_json") if isinstance(incident, dict) else None
        )
        verification_plan = (
            diagnosis_obj.get("verification_plan") if isinstance(diagnosis_obj, dict) else None
        )
        if not isinstance(verification_plan, dict):
            verification_plan = None

        tool_calls = self._execute_with_retry(
            "post_finalize.list_tool_calls",
            lambda: self._list_all_tool_calls(normalized_job_id),
        )
        target_seq, kb_refs = self._extract_kb_refs(tool_calls)

        snapshot = PostFinalizeSnapshot(
            incident_id=normalized_incident_id,
            job_id=normalized_job_id,
            verification_plan=verification_plan,
            kb_refs=kb_refs,
            target_toolcall_seq=target_seq,
        )
        if self._log_func is not None:
            step_count = 0
            if isinstance(snapshot.verification_plan, dict):
                steps = snapshot.verification_plan.get("steps")
                if isinstance(steps, list):
                    step_count = len(steps)
            self._log_func(
                "post_finalize snapshot "
                f"job_id={snapshot.job_id} incident_id={snapshot.incident_id} "
                f"target_seq={snapshot.target_toolcall_seq} kb_refs={len(snapshot.kb_refs)} "
                f"verification_steps={step_count}"
            )
        return snapshot

    def _list_all_tool_calls(self, job_id: str) -> list[dict[str, Any]]:
        limit = 200
        offset = 0
        out: list[dict[str, Any]] = []
        while True:
            page = self._client.list_tool_calls(job_id=job_id, limit=limit, offset=offset)
            if not isinstance(page, dict):
                break
            items = page.get("toolCalls")
            if not isinstance(items, list):
                items = page.get("tool_calls")
            if not isinstance(items, list):
                items = []
            page_items = [item for item in items if isinstance(item, dict)]
            out.extend(page_items)

            total = _coerce_positive_int(page.get("totalCount"))
            if total is None:
                total = _coerce_positive_int(page.get("total_count"))
            if not page_items:
                break
            if len(page_items) < limit:
                break
            if total is not None and len(out) >= total:
                break
            offset += limit
            if offset > 5000:
                break
        out.sort(key=lambda item: _coerce_positive_int(item.get("seq")) or 0)
        return out

    def _extract_kb_refs(self, tool_calls: list[dict[str, Any]]) -> tuple[int | None, list[dict[str, Any]]]:
        target_seq: int | None = None
        refs_out: list[dict[str, Any]] = []
        dedup: set[str] = set()

        for tool_call in tool_calls:
            response_obj = _parse_json_object(tool_call.get("responseJSON")) or _parse_json_object(
                tool_call.get("response_json")
            )
            if not isinstance(response_obj, dict):
                continue
            kb_refs = response_obj.get("kb_refs")
            if not isinstance(kb_refs, list) or not kb_refs:
                continue
            seq = _coerce_positive_int(tool_call.get("seq"))
            if seq is not None:
                target_seq = seq
            for ref in kb_refs:
                if not isinstance(ref, dict):
                    continue
                marker = json.dumps(ref, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
                if marker in dedup:
                    continue
                dedup.add(marker)
                refs_out.append(ref)
        return target_seq, refs_out
