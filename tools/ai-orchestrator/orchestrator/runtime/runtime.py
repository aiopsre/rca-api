from __future__ import annotations

import json
import time
import uuid
from typing import TYPE_CHECKING, Any, Callable

from ..sdk.errors import RCAApiError
from ..tooling.invoker import TOOLING_META_KEY, ToolInvokeError
from ..tools_rca_api import RCAApiClient
from .evidence_publisher import EvidencePublishResult, EvidencePublisher
from .lease_manager import LeaseManager
from .post_finalize import PostFinalizeObserver, PostFinalizeSnapshot
from .retry import RetryExecutor, RetryPolicy
from .toolcall_reporter import ToolCallReporter
from .verification_runner import VerificationBudget, VerificationRunner, VerificationStepResult

if TYPE_CHECKING:
    from ..skills.agent import PromptSkillAgent
    from ..skills.runtime import SkillCandidate, SkillCatalog
    from ..tooling.invoker import ToolInvoker, ToolInvokerChain


_OBSERVED_MAX_LEN = 512


def _trim_text(value: Any, *, max_len: int = 160) -> str:
    text = str(value).strip()
    if len(text) <= max_len:
        return text
    return f"{text[: max_len - 3]}..."


def _compact_observation_payload(payload: dict[str, Any]) -> dict[str, Any]:
    try:
        raw = json.dumps(payload, ensure_ascii=False, sort_keys=True, separators=(",", ":"), default=str)
    except Exception:  # noqa: BLE001
        return {
            "status": "error",
            "reason": "observed_serialization_failed",
        }

    if len(raw) <= _OBSERVED_MAX_LEN:
        return payload

    fallback = {
        "status": str(payload.get("status") or "truncated"),
        "reason": "observed_exceeds_limit",
        "original_length": len(raw),
    }
    compact = json.dumps(fallback, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    if len(compact) <= _OBSERVED_MAX_LEN:
        return fallback
    return {
        "status": "truncated",
        "reason": "observed_exceeds_limit",
    }


def _summarize_tool_result(payload: dict[str, Any]) -> dict[str, Any]:
    summary: dict[str, Any] = {
        "result_type": "dict",
        "keys": sorted(str(key) for key in payload.keys())[:8],
    }
    output = payload.get("output")
    if isinstance(output, dict):
        summary["output_keys"] = sorted(str(key) for key in output.keys())[:8]
    elif output is not None:
        summary["output_type"] = type(output).__name__
    return summary


def _error_category(exc: Exception) -> str:
    if isinstance(exc, RCAApiError):
        return exc.category.value
    if isinstance(exc, ToolInvokeError):
        if exc.reason:
            return exc.reason
        if exc.retryable:
            return "retryable_tool_invoke_error"
        return "tool_invoke_error"
    if bool(getattr(exc, "retryable", False)):
        return "retryable_error"
    return "unknown"


def _is_toolcall_status_conflict(exc: RCAApiError) -> bool:
    if not isinstance(exc, RCAApiError):
        return False
    if int(exc.http_status or 0) != 409:
        return False
    envelope_code = str(exc.envelope_code or "").strip().lower()
    message = str(exc).strip().lower()
    return (
        "aitoolcallstatusconflict" in envelope_code
        or "can only be written for queued/running jobs" in message
    )


def _normalize_query_tool_output(tool: str, payload: dict[str, Any]) -> dict[str, Any]:
    # Providers may return either query payload directly or MCP envelope shape.
    query_payload = payload
    output = payload.get("output")
    if isinstance(output, dict):
        query_payload = output

    raw_result = query_payload.get("queryResultJSON")
    if isinstance(raw_result, str) and raw_result.strip():
        return query_payload

    if bool(payload.get("truncated")):
        preview = str(query_payload.get("preview") or "").strip()
        fallback_payload: dict[str, Any] = {
            "mcp_truncated": True,
            "reason": str(query_payload.get("reason") or "max_response_bytes_exceeded"),
        }
        if preview:
            fallback_payload["preview"] = preview
        warnings = payload.get("warnings")
        if isinstance(warnings, list) and warnings:
            fallback_payload["warnings"] = warnings

        fallback_result = json.dumps(fallback_payload, ensure_ascii=False, separators=(",", ":"))
        return {
            "queryResultJSON": fallback_result,
            "resultSizeBytes": len(fallback_result.encode("utf-8")),
            "rowCount": 0,
            "isTruncated": True,
        }

    raise RuntimeError(f"invalid {tool} tool result payload: missing queryResultJSON")


class OrchestratorRuntime:
    def __init__(
        self,
        *,
        client: RCAApiClient,
        job_id: str,
        instance_id: str,
        heartbeat_interval_seconds: int,
        log_func: Callable[[str], None] | None = None,
        retry_policy: RetryPolicy | None = None,
        verification_max_steps: int = 20,
        verification_max_total_latency_ms: int = 0,
        verification_max_total_bytes: int = 0,
        verification_dedupe_enabled: bool = True,
        tool_invoker: ToolInvoker | ToolInvokerChain | None = None,
        skill_catalog: SkillCatalog | None = None,
        skills_execution_mode: str = "catalog",
        skill_agent: PromptSkillAgent | None = None,
    ) -> None:
        self._client = client
        self._job_id = str(job_id).strip()
        self._instance_id = str(instance_id).strip()
        self._log_func = log_func
        self._tool_invoker = tool_invoker
        self._skill_catalog = skill_catalog
        self._skills_execution_mode = str(skills_execution_mode or "catalog").strip().lower() or "catalog"
        self._skill_agent = skill_agent
        self._started = False
        if not self._job_id:
            raise RuntimeError("job_id is required")

        # Runtime owns lease identity propagation for all job lifecycle calls.
        if self._instance_id:
            self._client.session.headers.update({"X-Orchestrator-Instance-ID": self._instance_id})
            self._client.instance_id = self._instance_id

        self._retry_executor = RetryExecutor(
            policy=retry_policy,
            log_func=log_func,
        )
        self._lease_manager = LeaseManager(
            client=self._client,
            heartbeat_interval_seconds=heartbeat_interval_seconds,
            instance_id=self._instance_id,
            log_func=log_func,
            execute_with_retry=self._execute_with_retry,
        )
        self._toolcall_reporter = ToolCallReporter(client=self._client, job_id=self._job_id)
        self._evidence_publisher = EvidencePublisher(
            client=self._client,
            job_id=self._job_id,
            execute_with_retry=self._execute_with_retry,
        )
        self._post_finalize_observer = PostFinalizeObserver(
            client=self._client,
            execute_with_retry=self._execute_with_retry,
            log_func=log_func,
        )
        self._verification_runner = VerificationRunner(
            client=self._client,
            execute_with_retry=self._execute_with_retry,
            call_tool=self.call_tool,
            log_func=self._log_func,
            budget=VerificationBudget(
                max_steps=verification_max_steps,
                max_total_latency_ms=verification_max_total_latency_ms,
                max_total_bytes=verification_max_total_bytes,
            ),
            dedupe_enabled=verification_dedupe_enabled,
        )

    def start(self) -> bool:
        claimed = self._execute_with_retry("job.start", lambda: self._lease_manager.start(self._job_id))
        self._started = bool(claimed)
        return self._started

    def _execute_with_retry(self, operation: str, fn: Callable[[], Any]) -> Any:
        return self._retry_executor.run(operation, fn)

    def _log(self, message: str) -> None:
        if self._log_func is not None:
            self._log_func(message)

    def _current_toolset_chain(self) -> list[str]:
        if self._tool_invoker is None:
            return []
        if hasattr(self._tool_invoker, "toolset_ids"):
            raw = getattr(self._tool_invoker, "toolset_ids")
            if isinstance(raw, list):
                return [str(item).strip() for item in raw if str(item).strip()]
        if hasattr(self._tool_invoker, "toolset_id"):
            raw_single = str(getattr(self._tool_invoker, "toolset_id") or "").strip()
            if raw_single:
                return [raw_single]
        return []

    def skill_ids(self) -> list[str]:
        if self._skill_catalog is None:
            return []
        return self._skill_catalog.skill_ids()

    def skill_candidates(self, capability: str) -> list["SkillCandidate"]:
        if self._skill_catalog is None:
            return []
        return self._skill_catalog.candidates_for_capability(capability)

    def merge_session_patch(self, graph_state: Any, patch: dict[str, Any] | None) -> None:
        _merge_state_session_patch(graph_state, patch)

    def report_observation(
        self,
        *,
        tool: str,
        node_name: str,
        params: dict[str, Any] | None,
        response: dict[str, Any] | None,
        evidence_ids: list[str] | None = None,
    ) -> int | None:
        normalized_tool = str(tool).strip() or "observation"
        normalized_node = str(node_name).strip() or "runtime"
        if not self._started:
            self._log(
                "observation skipped "
                f"job={self._job_id} node={normalized_node} tool={normalized_tool} reason=runtime_not_started"
            )
            return None
        if self.is_lease_lost():
            self._log(
                "observation skipped "
                f"job={self._job_id} node={normalized_node} tool={normalized_tool} "
                f"reason=lease_lost lease_reason={self.lease_lost_reason()}"
            )
            return None

        request_json = params if isinstance(params, dict) else {}
        response_json = response if isinstance(response, dict) else {}

        status = str(response_json.get("status") or "ok").strip() or "ok"
        latency_raw = response_json.get("latency_ms", 0)
        try:
            latency_ms = max(int(latency_raw), 0)
        except (TypeError, ValueError):
            latency_ms = 0
        error_text = str(response_json.get("error") or "").strip() or None

        return self.report_tool_call(
            node_name=normalized_node,
            tool_name=normalized_tool,
            request_json=_compact_observation_payload(request_json),
            response_json=_compact_observation_payload(response_json),
            latency_ms=latency_ms,
            status=status,
            error=error_text,
            evidence_ids=evidence_ids,
        )

    def _report_observation_best_effort(
        self,
        *,
        tool: str,
        node_name: str,
        params: dict[str, Any] | None,
        response: dict[str, Any] | None,
        evidence_ids: list[str] | None = None,
    ) -> None:
        try:
            self.report_observation(
                tool=tool,
                node_name=node_name,
                params=params,
                response=response,
                evidence_ids=evidence_ids,
            )
        except Exception as exc:  # noqa: BLE001
            self._log(
                "observation report failed "
                f"job={self._job_id} node={node_name} tool={tool} error={_trim_text(exc)}"
            )

    def call_tool(
        self,
        tool: str,
        params: dict[str, Any] | None,
        idempotency_key: str | None = None,
    ) -> dict[str, Any]:
        normalized_tool = str(tool).strip()
        if not normalized_tool:
            raise RuntimeError("tool is required")
        started_at = time.monotonic()
        provider_id = ""
        provider_type = ""
        resolved_from_toolset_id = ""
        toolset_chain = self._current_toolset_chain()
        normalized_params = params if isinstance(params, dict) else {}

        self._log(
            "tool invoke start "
            f"job={self._job_id} tool={normalized_tool} "
            f"invoker={int(self._tool_invoker is not None)} idempotency_key={idempotency_key or ''}"
        )

        def _call() -> dict[str, Any]:
            if self._tool_invoker is not None:
                return self._tool_invoker.call(
                    tool=normalized_tool,
                    input_payload=normalized_params,
                    idempotency_key=idempotency_key,
                )
            return self._client.mcp_client.call(
                tool=normalized_tool,
                input_payload=normalized_params,
                idempotency_key=idempotency_key,
            )

        try:
            raw_result = self._execute_with_retry(f"tool.call:{normalized_tool}", _call)
            if not isinstance(raw_result, dict):
                raise RuntimeError(f"tool={normalized_tool} returned non-dict payload")
            tool_result = dict(raw_result)
            meta = tool_result.pop(TOOLING_META_KEY, None)
            if isinstance(meta, dict):
                provider_id = str(meta.get("provider_id") or "").strip()
                provider_type = str(meta.get("provider_type") or "").strip()
                resolved_from_toolset_id = str(meta.get("resolved_from_toolset_id") or "").strip()
            if not resolved_from_toolset_id and self._tool_invoker is not None:
                if hasattr(self._tool_invoker, "toolset_id"):
                    resolved_from_toolset_id = str(getattr(self._tool_invoker, "toolset_id") or "").strip()
                elif hasattr(self._tool_invoker, "toolset_ids"):
                    raw_ids = getattr(self._tool_invoker, "toolset_ids")
                    if isinstance(raw_ids, list) and raw_ids:
                        resolved_from_toolset_id = str(raw_ids[0] or "").strip()
            latency_ms = max(1, int((time.monotonic() - started_at) * 1000))
            observation = {
                "status": "ok",
                "latency_ms": latency_ms,
                "provider_id": provider_id or "rca_api_mcp",
                "provider_type": provider_type or "mcp_api",
                "resolved_from_toolset_id": resolved_from_toolset_id,
                "toolset_chain": toolset_chain,
                "route_policy": "first_match",
                "result_summary": _summarize_tool_result(tool_result),
            }
            self._report_observation_best_effort(
                tool="tool.invoke",
                node_name="runtime.call_tool",
                params={
                    "tool": normalized_tool,
                    "idempotency_key": idempotency_key or "",
                    "params": normalized_params,
                },
                response=observation,
            )
            self._log(
                "tool invoke done "
                f"job={self._job_id} tool={normalized_tool} status=ok latency_ms={latency_ms} "
                f"provider_id={observation['provider_id']} provider_type={observation['provider_type']} "
                f"resolved_from_toolset_id={observation['resolved_from_toolset_id']}"
            )
            return tool_result
        except ToolInvokeError as exc:
            latency_ms = max(1, int((time.monotonic() - started_at) * 1000))
            category = _error_category(exc)
            observation_tool = "tool.invoke_rejected" if category == "allow_tools_denied" else "tool.invoke"
            self._report_observation_best_effort(
                tool=observation_tool,
                node_name="runtime.call_tool",
                params={
                    "tool": normalized_tool,
                    "idempotency_key": idempotency_key or "",
                },
                response={
                    "status": "error",
                    "latency_ms": latency_ms,
                    "provider_id": provider_id,
                    "provider_type": provider_type,
                    "resolved_from_toolset_id": resolved_from_toolset_id,
                    "toolset_chain": toolset_chain,
                    "route_policy": "first_match",
                    "error_category": category,
                    "retryable": bool(exc.retryable),
                    "error": _trim_text(exc),
                },
            )
            self._log(
                "tool invoke failed "
                f"job={self._job_id} tool={normalized_tool} status=error latency_ms={latency_ms} "
                f"error_category={category} error={_trim_text(exc)}"
            )
            raise
        except Exception as exc:  # noqa: BLE001
            latency_ms = max(1, int((time.monotonic() - started_at) * 1000))
            category = _error_category(exc)
            self._report_observation_best_effort(
                tool="tool.invoke",
                node_name="runtime.call_tool",
                params={
                    "tool": normalized_tool,
                    "idempotency_key": idempotency_key or "",
                },
                response={
                    "status": "error",
                    "latency_ms": latency_ms,
                    "provider_id": provider_id,
                    "provider_type": provider_type,
                    "resolved_from_toolset_id": resolved_from_toolset_id,
                    "toolset_chain": toolset_chain,
                    "route_policy": "first_match",
                    "error_category": category,
                    "error": _trim_text(exc),
                },
            )
            self._log(
                "tool invoke failed "
                f"job={self._job_id} tool={normalized_tool} status=error latency_ms={latency_ms} "
                f"error_category={category} error={_trim_text(exc)}"
            )
            raise

    def report_tool_call(
        self,
        *,
        node_name: str,
        tool_name: str,
        request_json: dict[str, Any],
        response_json: dict[str, Any] | None,
        latency_ms: int,
        status: str,
        error: str | None = None,
        evidence_ids: list[str] | None = None,
    ) -> int:
        seq = self._toolcall_reporter.allocate_seq()
        try:
            return self._execute_with_retry(
                f"tool_call.report:{node_name}:{tool_name}:seq={seq}",
                lambda: self._toolcall_reporter.report(
                    node_name=node_name,
                    tool_name=tool_name,
                    request_json=request_json,
                    response_json=response_json,
                    latency_ms=latency_ms,
                    status=status,
                    error=error,
                    evidence_ids=evidence_ids,
                    seq=seq,
                ),
            )
        except RCAApiError as exc:
            if _is_toolcall_status_conflict(exc):
                self._log(
                    "toolcall report skipped "
                    f"job={self._job_id} node={node_name} tool={tool_name} seq={seq} "
                    "reason=job_status_conflict_after_finalize"
                )
                return seq
            raise

    def finalize(
        self,
        *,
        status: str,
        diagnosis_json: dict[str, Any] | None,
        error_message: str | None = None,
        evidence_ids: list[str] | None = None,
    ) -> None:
        self._execute_with_retry(
            "job.finalize",
            lambda: self._client.finalize_job(
                self._job_id,
                status=status,
                diagnosis_json=diagnosis_json,
                error_message=error_message,
                evidence_ids=evidence_ids,
            ),
        )

    def get_job(self, job_id: str | None = None) -> dict[str, Any]:
        target_job_id = str(job_id or self._job_id).strip() or self._job_id
        return self._execute_with_retry("job.get", lambda: self._client.get_job(target_job_id))

    def get_job_session_context(self, job_id: str | None = None) -> dict[str, Any]:
        target_job_id = str(job_id or self._job_id).strip() or self._job_id
        return self._execute_with_retry(
            "job.session_context.get",
            lambda: self._client.get_job_session_context(target_job_id),
        )

    def patch_job_session_context(
        self,
        *,
        session_revision: str | None = None,
        latest_summary: dict[str, Any] | None = None,
        pinned_evidence_append: list[dict[str, Any]] | None = None,
        pinned_evidence_remove: list[str] | None = None,
        context_state_patch: dict[str, Any] | None = None,
        actor: str | None = None,
        note: str | None = None,
        source: str | None = None,
    ) -> dict[str, Any]:
        return self._execute_with_retry(
            "job.session_context.patch",
            lambda: self._client.patch_job_session_context(
                self._job_id,
                session_revision=session_revision,
                latest_summary=latest_summary,
                pinned_evidence_append=pinned_evidence_append,
                pinned_evidence_remove=pinned_evidence_remove,
                context_state_patch=context_state_patch,
                actor=actor,
                note=note,
                source=source,
            ),
        )

    def execute_skill(
        self,
        skill_id: str,
        *,
        input_payload: dict[str, Any] | None,
        graph_state: Any,
    ) -> dict[str, Any]:
        if self._skill_catalog is None:
            raise RuntimeError("skill catalog is not configured")
        normalized_skill_id = str(skill_id).strip()
        if not normalized_skill_id:
            raise RuntimeError("skill_id is required")
        raise RuntimeError(
            "skill execution is disabled in the bundle+binding phase: "
            f"skill_id={normalized_skill_id} input_keys={sorted((input_payload or {}).keys())}"
        )

    def consume_diagnosis_enrich_skill(
        self,
        *,
        graph_state: Any,
        input_payload: dict[str, Any],
    ) -> dict[str, Any] | None:
        if self._skill_catalog is None or self._skills_execution_mode != "prompt_first":
            return None

        capability = "diagnosis.enrich"
        stage = "summarize_diagnosis"
        candidates = self.skill_candidates(capability)
        if not candidates:
            return None

        candidate_skill_ids = [candidate.skill_id for candidate in candidates]
        stage_summary = _build_diagnosis_stage_summary(input_payload)
        if self._skill_agent is None or not bool(getattr(self._skill_agent, "configured", False)):
            self._report_observation_best_effort(
                tool="skill.fallback",
                node_name="skills.diagnosis_enrich",
                params={"capability": capability, "stage": stage},
                response={
                    "status": "fallback",
                    "reason": "agent_not_configured",
                    "candidate_count": len(candidates),
                    "candidate_skill_ids": candidate_skill_ids,
                },
                evidence_ids=input_payload.get("evidence_ids") if isinstance(input_payload.get("evidence_ids"), list) else [],
            )
            return None

        selection_started = time.monotonic()
        try:
            selection = self._skill_agent.select_skill(
                capability=capability,
                stage=stage,
                stage_summary=stage_summary,
                candidates=[candidate.to_summary_dict() for candidate in candidates],
            )
            selection_latency_ms = max(1, int((time.monotonic() - selection_started) * 1000))
            self._report_observation_best_effort(
                tool="skill.select",
                node_name="skills.diagnosis_enrich",
                params={
                    "capability": capability,
                    "stage": stage,
                },
                response={
                    "status": "ok",
                    "latency_ms": selection_latency_ms,
                    "candidate_count": len(candidates),
                    "candidate_skill_ids": candidate_skill_ids,
                    "selected_binding_key": str(selection.selected_binding_key or ""),
                    "reason": str(selection.reason or ""),
                },
                evidence_ids=input_payload.get("evidence_ids") if isinstance(input_payload.get("evidence_ids"), list) else [],
            )
        except Exception as exc:  # noqa: BLE001
            selection_latency_ms = max(1, int((time.monotonic() - selection_started) * 1000))
            self._report_observation_best_effort(
                tool="skill.fallback",
                node_name="skills.diagnosis_enrich",
                params={
                    "capability": capability,
                    "stage": stage,
                },
                response={
                    "status": "fallback",
                    "reason": "selection_failed",
                    "latency_ms": selection_latency_ms,
                    "candidate_count": len(candidates),
                    "candidate_skill_ids": candidate_skill_ids,
                    "error": _trim_text(exc),
                },
                evidence_ids=input_payload.get("evidence_ids") if isinstance(input_payload.get("evidence_ids"), list) else [],
            )
            return None

        selected_binding_key = str(selection.selected_binding_key or "").strip()
        if not selected_binding_key:
            return None
        selected_candidate = next((item for item in candidates if item.binding_key == selected_binding_key), None)
        if selected_candidate is None:
            self._report_observation_best_effort(
                tool="skill.fallback",
                node_name="skills.diagnosis_enrich",
                params={"capability": capability, "stage": stage},
                response={
                    "status": "fallback",
                    "reason": "selected_binding_not_found",
                    "candidate_count": len(candidates),
                    "candidate_skill_ids": candidate_skill_ids,
                    "selected_binding_key": selected_binding_key,
                },
                evidence_ids=input_payload.get("evidence_ids") if isinstance(input_payload.get("evidence_ids"), list) else [],
            )
            return None

        consume_started = time.monotonic()
        try:
            skill = self._skill_catalog.get_skill(selected_binding_key)
            skill_document = self._skill_catalog.load_skill_document(selected_binding_key)
            output = self._skill_agent.run_diagnosis_enrich(
                skill_id=skill.summary.skill_id,
                skill_version=skill.summary.version,
                skill_document=skill_document,
                input_payload=input_payload,
            )
        except Exception as exc:  # noqa: BLE001
            consume_latency_ms = max(1, int((time.monotonic() - consume_started) * 1000))
            self._report_observation_best_effort(
                tool="skill.fallback",
                node_name="skills.diagnosis_enrich",
                params={"capability": capability, "stage": stage, "selected_binding_key": selected_binding_key},
                response={
                    "status": "fallback",
                    "reason": "consume_failed",
                    "latency_ms": consume_latency_ms,
                    "skill_id": selected_candidate.skill_id,
                    "selected_binding_key": selected_binding_key,
                    "error": _trim_text(exc),
                },
                evidence_ids=input_payload.get("evidence_ids") if isinstance(input_payload.get("evidence_ids"), list) else [],
            )
            return None

        diagnosis_patch, dropped_fields = _sanitize_diagnosis_patch(output.diagnosis_patch)
        session_patch = _sanitize_session_patch(output.session_patch)
        if session_patch and not str(session_patch.get("actor") or "").strip():
            session_patch["actor"] = f"skill:{selected_candidate.skill_id}"
        if session_patch and not str(session_patch.get("source") or "").strip():
            session_patch["source"] = "skill.prompt"
        observations = [item for item in output.observations if isinstance(item, dict)]
        consume_latency_ms = max(1, int((time.monotonic() - consume_started) * 1000))
        self._report_observation_best_effort(
            tool="skill.consume",
            node_name="skills.diagnosis_enrich",
            params={
                "capability": capability,
                "stage": stage,
                "selected_binding_key": selected_binding_key,
            },
            response={
                "status": "ok",
                "latency_ms": consume_latency_ms,
                "skill_id": selected_candidate.skill_id,
                "selected_binding_key": selected_binding_key,
                "candidate_count": len(candidates),
                "candidate_skill_ids": candidate_skill_ids,
                "diagnosis_patch_keys": sorted(diagnosis_patch.keys()),
                "session_patch_keys": sorted(session_patch.keys()),
                "observation_count": len(observations),
                "dropped_fields": dropped_fields,
            },
            evidence_ids=input_payload.get("evidence_ids") if isinstance(input_payload.get("evidence_ids"), list) else [],
        )
        if dropped_fields:
            self._report_observation_best_effort(
                tool="skill.fallback",
                node_name="skills.diagnosis_enrich",
                params={"capability": capability, "stage": stage, "selected_binding_key": selected_binding_key},
                response={
                    "status": "fallback",
                    "reason": "diagnosis_patch_fields_dropped",
                    "skill_id": selected_candidate.skill_id,
                    "selected_binding_key": selected_binding_key,
                    "fields": dropped_fields,
                },
                evidence_ids=input_payload.get("evidence_ids") if isinstance(input_payload.get("evidence_ids"), list) else [],
            )
        return {
            "selected_binding_key": selected_binding_key,
            "skill_id": selected_candidate.skill_id,
            "version": selected_candidate.version,
            "diagnosis_patch": diagnosis_patch,
            "session_patch": session_patch,
            "observations": observations,
        }

    def get_incident(self, incident_id: str) -> dict[str, Any]:
        normalized = str(incident_id).strip()
        if not normalized:
            raise RuntimeError("incident_id is required")
        return self._execute_with_retry("incident.get", lambda: self._client.get_incident(normalized))

    def ensure_datasource(self, ds_base_url: str) -> str:
        return self._execute_with_retry("datasource.ensure", lambda: self._client.ensure_datasource(ds_base_url))

    def _query_via_tool_invoker(
        self,
        *,
        operation: str,
        tool: str,
        input_payload: dict[str, Any],
    ) -> dict[str, Any]:
        if self._tool_invoker is None:
            raise RuntimeError("tool invoker is not configured")
        idempotency_key = f"orchestrator-{operation}-{uuid.uuid4().hex}"
        raw_result = self._execute_with_retry(
            operation,
            lambda: self._tool_invoker.call(
                tool=tool,
                input_payload=input_payload,
                idempotency_key=idempotency_key,
            ),
        )
        if not isinstance(raw_result, dict):
            raise RuntimeError(f"tool={tool} returned non-dict payload")
        result = dict(raw_result)
        result.pop(TOOLING_META_KEY, None)
        return _normalize_query_tool_output(tool, result)

    def query_metrics(
        self,
        *,
        datasource_id: str,
        promql: str,
        start_ts: int,
        end_ts: int,
        step_s: int,
    ) -> dict[str, Any]:
        request = {
            "datasource_id": datasource_id,
            "expr": promql,
            "time_range_start": {"seconds": int(start_ts), "nanos": 0},
            "time_range_end": {"seconds": int(end_ts), "nanos": 0},
            "step_seconds": int(step_s),
        }
        if self._tool_invoker is not None:
            return self._query_via_tool_invoker(
                operation="query.metrics.tool",
                tool="mcp.query_metrics",
                input_payload=request,
            )
        return self._execute_with_retry(
            "query.metrics",
            lambda: self._client.query_metrics(
                datasource_id=datasource_id,
                promql=promql,
                start_ts=start_ts,
                end_ts=end_ts,
                step_s=step_s,
            ),
        )

    def query_logs(
        self,
        *,
        datasource_id: str,
        query: str,
        start_ts: int,
        end_ts: int,
        limit: int,
    ) -> dict[str, Any]:
        request = {
            "datasource_id": datasource_id,
            "query": query,
            "query_json": {},
            "time_range_start": {"seconds": int(start_ts), "nanos": 0},
            "time_range_end": {"seconds": int(end_ts), "nanos": 0},
            "limit": int(limit),
        }
        if self._tool_invoker is not None:
            return self._query_via_tool_invoker(
                operation="query.logs.tool",
                tool="mcp.query_logs",
                input_payload=request,
            )
        return self._execute_with_retry(
            "query.logs",
            lambda: self._client.query_logs(
                datasource_id=datasource_id,
                query=query,
                start_ts=start_ts,
                end_ts=end_ts,
                limit=limit,
            ),
        )

    def save_mock_evidence(
        self,
        *,
        incident_id: str,
        node_name: str,
        kind: str,
        summary: str,
        raw: dict[str, Any],
        query_hash_source: Any = None,
    ) -> EvidencePublishResult:
        return self._evidence_publisher.save_mock_evidence(
            incident_id=incident_id,
            node_name=node_name,
            kind=kind,
            summary=summary,
            raw=raw,
            query_hash_source=query_hash_source,
        )

    def save_evidence_from_query(
        self,
        *,
        incident_id: str,
        node_name: str,
        kind: str,
        query: dict[str, Any],
        result: dict[str, Any],
        query_hash_source: Any = None,
    ) -> EvidencePublishResult:
        return self._evidence_publisher.save_evidence_from_query(
            incident_id=incident_id,
            node_name=node_name,
            kind=kind,
            query=query,
            result=result,
            query_hash_source=query_hash_source,
        )

    def is_lease_lost(self) -> bool:
        return self._lease_manager.is_lease_lost()

    def lease_lost_reason(self) -> str:
        return self._lease_manager.lease_lost_reason()

    def observe_post_finalize(
        self,
        *,
        incident_id: str,
        wait_timeout_s: float = 0.0,
        wait_interval_s: float = 0.5,
        wait_max_interval_s: float = 2.0,
    ) -> PostFinalizeSnapshot:
        if float(wait_timeout_s) > 0:
            return self._post_finalize_observer.observe_with_wait(
                incident_id=incident_id,
                job_id=self._job_id,
                timeout_s=wait_timeout_s,
                interval_s=wait_interval_s,
                max_interval_s=wait_max_interval_s,
            )
        return self._post_finalize_observer.observe(incident_id=incident_id, job_id=self._job_id)

    def run_verification(
        self,
        *,
        incident_id: str,
        verification_plan: dict[str, Any],
        source: str = "ai_job",
    ) -> list[VerificationStepResult]:
        return self._verification_runner.run(
            incident_id=incident_id,
            verification_plan=verification_plan,
            source=source,
            actor=f"ai:{self._job_id}",
        )

    def shutdown(self) -> None:
        self._started = False
        self._lease_manager.shutdown()


def _merge_state_session_patch(graph_state: Any, patch: Any) -> None:
    if not isinstance(patch, dict) or graph_state is None:
        return
    existing = getattr(graph_state, "session_patch", None)
    if not isinstance(existing, dict):
        existing = {}
        setattr(graph_state, "session_patch", existing)
    for key, value in patch.items():
        if key in {"pinned_evidence_append"} and isinstance(value, list):
            current = existing.get(key)
            if not isinstance(current, list):
                current = []
            current.extend(item for item in value if isinstance(item, dict))
            existing[key] = current
            continue
        if key in {"pinned_evidence_remove"} and isinstance(value, list):
            current_remove = existing.get(key)
            if not isinstance(current_remove, list):
                current_remove = []
            current_remove.extend(str(item).strip() for item in value if str(item).strip())
            existing[key] = current_remove
            continue
        existing[key] = value


def _build_diagnosis_stage_summary(input_payload: dict[str, Any]) -> dict[str, Any]:
    return {
        "incident_id": str(input_payload.get("incident_id") or "").strip(),
        "quality_gate_decision": str(input_payload.get("quality_gate_decision") or "").strip(),
        "quality_gate_reasons": input_payload.get("quality_gate_reasons")
        if isinstance(input_payload.get("quality_gate_reasons"), list)
        else [],
        "missing_evidence": input_payload.get("missing_evidence") if isinstance(input_payload.get("missing_evidence"), list) else [],
        "evidence_ids": input_payload.get("evidence_ids") if isinstance(input_payload.get("evidence_ids"), list) else [],
        "has_incident_context": isinstance(input_payload.get("incident_context"), dict) and bool(input_payload.get("incident_context")),
        "has_input_hints": isinstance(input_payload.get("input_hints"), dict) and bool(input_payload.get("input_hints")),
        "diagnosis_summary": str(
            ((input_payload.get("diagnosis_json") or {}) if isinstance(input_payload.get("diagnosis_json"), dict) else {}).get("summary")
            or ""
        ).strip(),
    }


def _sanitize_diagnosis_patch(patch: Any) -> tuple[dict[str, Any], list[str]]:
    if not isinstance(patch, dict):
        return {}, []
    sanitized: dict[str, Any] = {}
    dropped: list[str] = []

    summary = patch.get("summary")
    if isinstance(summary, str) and summary.strip():
        sanitized["summary"] = summary.strip()
    elif "summary" in patch and summary is not None:
        dropped.append("summary")

    root_cause = patch.get("root_cause")
    if isinstance(root_cause, dict):
        allowed_root_cause: dict[str, Any] = {}
        summary_value = root_cause.get("summary")
        statement_value = root_cause.get("statement")
        if isinstance(summary_value, str) and summary_value.strip():
            allowed_root_cause["summary"] = summary_value.strip()
        elif "summary" in root_cause and summary_value is not None:
            dropped.append("root_cause.summary")
        if isinstance(statement_value, str) and statement_value.strip():
            allowed_root_cause["statement"] = statement_value.strip()
        elif "statement" in root_cause and statement_value is not None:
            dropped.append("root_cause.statement")
        if allowed_root_cause:
            sanitized["root_cause"] = allowed_root_cause
        forbidden_root_keys = set(root_cause.keys()) - {"summary", "statement"}
        for key in sorted(forbidden_root_keys):
            dropped.append(f"root_cause.{key}")
    elif "root_cause" in patch and root_cause is not None:
        dropped.append("root_cause")

    recommendations = patch.get("recommendations")
    if isinstance(recommendations, list):
        normalized_recommendations = [item for item in recommendations if isinstance(item, dict)]
        if normalized_recommendations:
            sanitized["recommendations"] = normalized_recommendations
        elif recommendations:
            dropped.append("recommendations")
    elif "recommendations" in patch and recommendations is not None:
        dropped.append("recommendations")

    for key in ("unknowns", "next_steps"):
        value = patch.get(key)
        if isinstance(value, list):
            normalized_list = [str(item).strip() for item in value if str(item).strip()]
            if normalized_list:
                sanitized[key] = normalized_list
            elif value:
                dropped.append(key)
        elif key in patch and value is not None:
            dropped.append(key)

    forbidden_top_level = set(patch.keys()) - {"summary", "root_cause", "recommendations", "unknowns", "next_steps"}
    dropped.extend(sorted(forbidden_top_level))
    return sanitized, sorted(set(dropped))


def _sanitize_session_patch(patch: Any) -> dict[str, Any]:
    if not isinstance(patch, dict):
        return {}
    sanitized: dict[str, Any] = {}
    latest_summary = patch.get("latest_summary")
    if isinstance(latest_summary, dict):
        sanitized["latest_summary"] = latest_summary
    pinned_append = patch.get("pinned_evidence_append")
    if isinstance(pinned_append, list):
        normalized_append = [item for item in pinned_append if isinstance(item, dict)]
        if normalized_append:
            sanitized["pinned_evidence_append"] = normalized_append
    pinned_remove = patch.get("pinned_evidence_remove")
    if isinstance(pinned_remove, list):
        normalized_remove = [str(item).strip() for item in pinned_remove if str(item).strip()]
        if normalized_remove:
            sanitized["pinned_evidence_remove"] = normalized_remove
    context_state_patch = patch.get("context_state_patch")
    if isinstance(context_state_patch, dict):
        sanitized["context_state_patch"] = context_state_patch
    for key in ("actor", "note", "source"):
        value = patch.get(key)
        if isinstance(value, str) and value.strip():
            sanitized[key] = value.strip()
    return sanitized
