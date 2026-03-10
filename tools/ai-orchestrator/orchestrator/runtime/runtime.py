from __future__ import annotations

from dataclasses import dataclass
import json
import time
import uuid
from typing import TYPE_CHECKING, Any, Callable

from ..constants import DegradeReason
from ..sdk.errors import RCAApiError
from ..skills.capabilities import (
    PromptSkillConsumeResult,
    get_capability_definition,
)
from ..skills.script_runner import ScriptExecutorError, ScriptExecutorResult, ScriptExecutorRunner
from ..tooling.invoker import TOOLING_META_KEY, ToolInvokeError
from ..tooling.toolset_config import normalize_tool_name
from ..tools_rca_api import RCAApiClient
from .audit import redact_sensitive, summarize_request, summarize_response
from .evidence_publisher import EvidencePublishResult, EvidencePublisher
from .lease_manager import LeaseManager
from .post_finalize import PostFinalizeObserver, PostFinalizeSnapshot
from .retry import RetryExecutor, RetryPolicy
from .tool_registry import get_tool_metadata, get_tool_name_by_kind
from .toolcall_reporter import ToolCallReporter
from .verification_runner import VerificationBudget, VerificationRunner, VerificationStepResult

if TYPE_CHECKING:
    from ..skills.agent import PromptSkillAgent
    from ..skills.runtime import SkillCandidate, SkillCatalog
    from ..tooling.invoker import ToolInvoker, ToolInvokerChain


_OBSERVED_MAX_LEN = 512
_MAX_SELECTED_SKILL_RESOURCES = 3

# Canonical set of observation tool names for skill.* audit events (passed as tool= to
# report_observation / _report_observation_best_effort). Excludes session_patch["source"]
# values (e.g. skill.prompt, skill.script). Used by contract tests to detect audit-surface
# regressions.
ALLOWED_SKILL_OBSERVATION_TOOLS: frozenset[str] = frozenset({
    "skill.select",
    "skill.resource_select",
    "skill.resource_load",
    "skill.consume",
    "skill.execute",
    "skill.fallback",
    "skill.tool_reuse",
    "skill.tool_plan",
})


@dataclass(frozen=True)
class KnowledgeContextBundle:
    selected_binding_keys: tuple[str, ...]
    skills: tuple[dict[str, Any], ...]

    @property
    def skill_ids(self) -> list[str]:
        return [str(item.get("skill_id") or "").strip() for item in self.skills if str(item.get("skill_id") or "").strip()]

    def to_agent_payload(self) -> list[dict[str, Any]]:
        return [dict(item) for item in self.skills]

    def to_selection_summary(self) -> list[dict[str, Any]]:
        out: list[dict[str, Any]] = []
        for item in self.skills:
            if not isinstance(item, dict):
                continue
            resources = item.get("resources")
            resource_ids: list[str] = []
            if isinstance(resources, list):
                resource_ids = [
                    str(resource.get("resource_id") or "").strip()
                    for resource in resources
                    if isinstance(resource, dict) and str(resource.get("resource_id") or "").strip()
                ]
            out.append(
                {
                    "binding_key": str(item.get("binding_key") or "").strip(),
                    "skill_id": str(item.get("skill_id") or "").strip(),
                    "version": str(item.get("version") or "").strip(),
                    "role": str(item.get("role") or "").strip(),
                    "resource_ids": resource_ids,
                }
            )
        return out


@dataclass(frozen=True)
class SkillToolCallResult:
    tool_name: str
    tool_request: dict[str, Any]
    tool_result: dict[str, Any]
    latency_ms: int


def _script_result_to_prompt_result(result: ScriptExecutorResult) -> PromptSkillConsumeResult:
    return PromptSkillConsumeResult(
        payload=dict(result.payload),
        session_patch=dict(result.session_patch),
        observations=[item for item in result.observations if isinstance(item, dict)],
    )


def _serialize_tool_results(tool_results: list[SkillToolCallResult]) -> list[dict[str, Any]]:
    return [
        {
            "tool": item.tool_name,
            "tool_name": item.tool_name,
            "tool_request": item.tool_request,
            "tool_result": item.tool_result,
            "latency_ms": item.latency_ms,
        }
        for item in tool_results
    ]


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


def _query_result_is_no_data(payload: dict[str, Any]) -> bool:
    if not isinstance(payload, dict):
        return False
    raw_json = payload.get("queryResultJSON")
    if not isinstance(raw_json, str) or not raw_json.strip():
        return False
    try:
        parsed = json.loads(raw_json)
    except json.JSONDecodeError:
        return False
    data = parsed.get("data") if isinstance(parsed, dict) else None
    if not isinstance(data, dict):
        return False
    result = data.get("result")
    return isinstance(result, list) and len(result) == 0


def _query_result_size_bytes(payload: dict[str, Any]) -> int:
    if not isinstance(payload, dict):
        return 0
    raw = payload.get("resultSizeBytes")
    if isinstance(raw, (int, float)):
        return max(int(raw), 0)
    raw_json = payload.get("queryResultJSON")
    if isinstance(raw_json, str):
        return len(raw_json.encode("utf-8"))
    compact = json.dumps(payload, ensure_ascii=False, separators=(",", ":"))
    return len(compact.encode("utf-8"))


def _query_toolcall_response(payload: dict[str, Any], *, source: str | None = None) -> dict[str, Any]:
    response = {
        "result_size_bytes": _query_result_size_bytes(payload),
        "row_count": int(payload.get("rowCount") or payload.get("row_count") or 0),
        "is_truncated": bool(payload.get("isTruncated") or payload.get("is_truncated")),
        "no_data": _query_result_is_no_data(payload),
    }
    normalized_source = str(source or "").strip()
    if normalized_source:
        response["source"] = normalized_source
    return response


def _coerce_int(value: Any) -> int:
    if isinstance(value, bool):
        raise ValueError("boolean is not a valid integer")
    return int(value)


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
        skills_tool_calling_mode: str = "disabled",
        skill_agent: PromptSkillAgent | None = None,
    ) -> None:
        self._client = client
        self._job_id = str(job_id).strip()
        self._instance_id = str(instance_id).strip()
        self._log_func = log_func
        self._tool_invoker = tool_invoker
        self._skill_catalog = skill_catalog
        self._skills_execution_mode = str(skills_execution_mode or "catalog").strip().lower() or "catalog"
        self._skills_tool_calling_mode = str(skills_tool_calling_mode or "disabled").strip().lower() or "disabled"
        self._skill_agent = skill_agent
        self._script_executor_runner = ScriptExecutorRunner()
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

    def knowledge_skill_candidates(self, capability: str) -> list["SkillCandidate"]:
        if self._skill_catalog is None:
            return []
        if hasattr(self._skill_catalog, "knowledge_candidates_for_capability"):
            return self._skill_catalog.knowledge_candidates_for_capability(capability)
        return []

    def executor_skill_candidates(self, capability: str) -> list["SkillCandidate"]:
        if self._skill_catalog is None:
            return []
        if hasattr(self._skill_catalog, "executor_candidates_for_capability"):
            return self._skill_catalog.executor_candidates_for_capability(capability)
        return self._skill_catalog.candidates_for_capability(capability)

    def _effective_prompt_skill_tools(self, candidate: "SkillCandidate") -> list[str]:
        binding_allowed = {
            normalize_tool_name(tool)
            for tool in getattr(candidate, "allowed_tools", ())
            if normalize_tool_name(tool)
        }
        if not binding_allowed:
            return []
        toolset_allowed: set[str] = set()
        if self._tool_invoker is not None and hasattr(self._tool_invoker, "allowed_tools"):
            try:
                raw_allowed = self._tool_invoker.allowed_tools()
            except Exception:  # noqa: BLE001
                raw_allowed = []
            if isinstance(raw_allowed, list):
                toolset_allowed = {normalize_tool_name(tool) for tool in raw_allowed if normalize_tool_name(tool)}
        if toolset_allowed:
            binding_allowed &= toolset_allowed
        return sorted(binding_allowed)

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
                    "params": redact_sensitive(normalized_params),
                    "request_summary": summarize_request(normalized_tool, normalized_params),
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
                    "params": redact_sensitive(normalized_params),
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
                    "params": redact_sensitive(normalized_params),
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

    def consume_prompt_skill(
        self,
        *,
        capability: str,
        graph_state: Any,
    ) -> dict[str, Any] | None:
        definition = get_capability_definition(capability)
        if definition is None:
            return None
        if definition.capability == "evidence.plan":
            return self._consume_prompt_skill_with_roles(
                capability=definition.capability,
                stage=definition.stage,
                graph_state=graph_state,
                input_payload=definition.build_input(graph_state),
                output_contract=definition.output_contract,
                sanitize_output=definition.sanitize_output,
                apply_result=definition.apply_result,
            )
        return self._consume_single_prompt_skill(
            capability=definition.capability,
            stage=definition.stage,
            graph_state=graph_state,
            input_payload=definition.build_input(graph_state),
            output_contract=definition.output_contract,
            sanitize_output=definition.sanitize_output,
            apply_result=definition.apply_result,
        )

    def consume_diagnosis_enrich_skill(
        self,
        *,
        graph_state: Any,
        input_payload: dict[str, Any],
    ) -> dict[str, Any] | None:
        definition = get_capability_definition("diagnosis.enrich")
        if definition is None:
            return None
        return self._consume_single_prompt_skill(
            capability=definition.capability,
            stage=definition.stage,
            graph_state=graph_state,
            input_payload=input_payload,
            output_contract=definition.output_contract,
            sanitize_output=definition.sanitize_output,
            apply_result=definition.apply_result,
        )

    def _consume_single_prompt_skill(
        self,
        *,
        capability: str,
        stage: str,
        graph_state: Any,
        input_payload: dict[str, Any],
        output_contract: dict[str, Any],
        sanitize_output: Callable[[PromptSkillConsumeResult], tuple[PromptSkillConsumeResult, list[str]]],
        apply_result: Callable[[Any, PromptSkillConsumeResult, Callable[[Any, dict[str, Any] | None], None]], None],
    ) -> dict[str, Any] | None:
        if self._skill_catalog is None or self._skills_execution_mode != "prompt_first":
            return None

        candidates = self.executor_skill_candidates(capability)
        if not candidates:
            return None

        evidence_ids = input_payload.get("evidence_ids") if isinstance(input_payload.get("evidence_ids"), list) else []
        candidate_skill_ids = [candidate.skill_id for candidate in candidates]
        node_name = _skill_node_name(capability)
        stage_summary = _build_stage_summary(capability, input_payload)
        if self._skill_agent is None or not bool(getattr(self._skill_agent, "configured", False)):
            self._report_observation_best_effort(
                tool="skill.fallback",
                node_name=node_name,
                params={"capability": capability, "stage": stage},
                response={
                    "status": "fallback",
                    "reason": DegradeReason.AGENT_NOT_CONFIGURED.value,
                    "candidate_count": len(candidates),
                    "candidate_skill_ids": candidate_skill_ids,
                },
                evidence_ids=evidence_ids,
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
                node_name=node_name,
                params={"capability": capability, "stage": stage},
                response={
                    "status": "ok",
                    "latency_ms": selection_latency_ms,
                    "candidate_count": len(candidates),
                    "candidate_skill_ids": candidate_skill_ids,
                    "selected_binding_key": str(selection.selected_binding_key or ""),
                    "reason": str(selection.reason or ""),
                },
                evidence_ids=evidence_ids,
            )
        except Exception as exc:  # noqa: BLE001
            selection_latency_ms = max(1, int((time.monotonic() - selection_started) * 1000))
            self._report_observation_best_effort(
                tool="skill.fallback",
                node_name=node_name,
                params={"capability": capability, "stage": stage},
                response={
                    "status": "fallback",
                    "reason": DegradeReason.SKILL_SELECTION_FAILED.value,
                    "latency_ms": selection_latency_ms,
                    "candidate_count": len(candidates),
                    "candidate_skill_ids": candidate_skill_ids,
                    "error": _trim_text(exc),
                },
                evidence_ids=evidence_ids,
            )
            return None

        selected_binding_key = str(selection.selected_binding_key or "").strip()
        if not selected_binding_key:
            return None
        selected_candidate = self._resolve_candidate_by_binding_key(candidates, selected_binding_key)
        if selected_candidate is None:
            self._report_observation_best_effort(
                tool="skill.fallback",
                node_name=node_name,
                params={"capability": capability, "stage": stage},
                response={
                    "status": "fallback",
                    "reason": DegradeReason.SKILL_NOT_FOUND.value,
                    "candidate_count": len(candidates),
                    "candidate_skill_ids": candidate_skill_ids,
                    "selected_binding_key": selected_binding_key,
                },
                evidence_ids=evidence_ids,
            )
            return None

        consume_started = time.monotonic()
        try:
            skill_document = self._skill_catalog.load_skill_document(selected_binding_key)
            selected_skill_resources = self._select_skill_resources(
                capability=capability,
                stage=stage,
                node_name=node_name,
                stage_summary=stage_summary,
                evidence_ids=evidence_ids,
                selected_candidate=selected_candidate,
                role="executor",
                skill_document=skill_document,
                knowledge_context_summary=[],
            )
            raw_output, post_apply_context, success_observation_tool = self._execute_selected_skill(
                capability=capability,
                stage=stage,
                graph_state=graph_state,
                node_name=node_name,
                evidence_ids=evidence_ids,
                selected_candidate=selected_candidate,
                input_payload=input_payload,
                knowledge_context=[],
                skill_resources=selected_skill_resources,
                output_contract=output_contract,
                skill_document=skill_document,
            )
        except Exception as exc:  # noqa: BLE001
            consume_latency_ms = max(1, int((time.monotonic() - consume_started) * 1000))
            self._report_observation_best_effort(
                tool="skill.fallback",
                node_name=node_name,
                params={"capability": capability, "stage": stage, "selected_binding_key": selected_binding_key},
                response={
                    "status": "fallback",
                    "reason": DegradeReason.SCRIPT_EXECUTE_FAILED.value if isinstance(exc, ScriptExecutorError) else DegradeReason.CONSUME_FAILED.value,
                    "latency_ms": consume_latency_ms,
                    "skill_id": selected_candidate.skill_id,
                    "selected_binding_key": selected_binding_key,
                    "executor_mode": selected_candidate.executor_mode,
                    "error": _trim_text(exc),
                },
                evidence_ids=evidence_ids,
            )
            return None

        normalized_output, dropped_fields = sanitize_output(raw_output)
        if normalized_output.session_patch and not str(normalized_output.session_patch.get("actor") or "").strip():
            normalized_output.session_patch["actor"] = f"skill:{selected_candidate.skill_id}"
        if normalized_output.session_patch and not str(normalized_output.session_patch.get("source") or "").strip():
            normalized_output.session_patch["source"] = (
                "skill.script" if selected_candidate.executor_mode == "script" else "skill.prompt"
            )
        apply_result(graph_state, normalized_output, self.merge_session_patch)
        if isinstance(post_apply_context, dict):
            self._apply_prompt_skill_post_merge(
                graph_state=graph_state,
                result=normalized_output,
                post_apply_context=post_apply_context,
            )
        consume_latency_ms = max(1, int((time.monotonic() - consume_started) * 1000))
        self._report_observation_best_effort(
            tool=success_observation_tool,
            node_name=node_name,
            params={
                "capability": capability,
                "stage": stage,
                "selected_binding_key": selected_binding_key,
                "executor_mode": selected_candidate.executor_mode,
            },
            response={
                "status": "ok",
                "latency_ms": consume_latency_ms,
                "skill_id": selected_candidate.skill_id,
                "selected_binding_key": selected_binding_key,
                "candidate_count": len(candidates),
                "candidate_skill_ids": candidate_skill_ids,
                "executor_mode": selected_candidate.executor_mode,
                "skill_resource_ids": [str(item.get("resource_id") or "") for item in selected_skill_resources],
                "payload_keys": sorted(normalized_output.payload.keys()),
                "session_patch_keys": sorted(normalized_output.session_patch.keys()),
                "observation_count": len(normalized_output.observations),
                "dropped_fields": dropped_fields,
            },
            evidence_ids=evidence_ids,
        )
        if dropped_fields:
            self._report_observation_best_effort(
                tool="skill.fallback",
                node_name=node_name,
                params={"capability": capability, "stage": stage, "selected_binding_key": selected_binding_key},
                response={
                    "status": "fallback",
                    "reason": DegradeReason.PAYLOAD_FIELDS_DROPPED.value,
                    "skill_id": selected_candidate.skill_id,
                    "selected_binding_key": selected_binding_key,
                    "fields": dropped_fields,
                },
                evidence_ids=evidence_ids,
            )
        return {
            "selected_binding_key": selected_candidate.binding_key,
            "skill_id": selected_candidate.skill_id,
            "version": selected_candidate.version,
            "capability": capability,
            "payload": normalized_output.payload,
            "session_patch": normalized_output.session_patch,
            "observations": normalized_output.observations,
        }

    def _consume_prompt_skill_with_roles(
        self,
        *,
        capability: str,
        stage: str,
        graph_state: Any,
        input_payload: dict[str, Any],
        output_contract: dict[str, Any],
        sanitize_output: Callable[[PromptSkillConsumeResult], tuple[PromptSkillConsumeResult, list[str]]],
        apply_result: Callable[[Any, PromptSkillConsumeResult, Callable[[Any, dict[str, Any] | None], None]], None],
    ) -> dict[str, Any] | None:
        if self._skill_catalog is None or self._skills_execution_mode != "prompt_first":
            return None

        knowledge_candidates = self.knowledge_skill_candidates(capability)
        executor_candidates = self.executor_skill_candidates(capability)
        if not executor_candidates:
            return None

        evidence_ids = input_payload.get("evidence_ids") if isinstance(input_payload.get("evidence_ids"), list) else []
        node_name = _skill_node_name(capability)
        stage_summary = _build_stage_summary(capability, input_payload)
        if self._skill_agent is None or not bool(getattr(self._skill_agent, "configured", False)):
            self._report_observation_best_effort(
                tool="skill.fallback",
                node_name=node_name,
                params={"capability": capability, "stage": stage},
                response={
                    "status": "fallback",
                    "reason": DegradeReason.AGENT_NOT_CONFIGURED.value,
                    "knowledge_candidate_count": len(knowledge_candidates),
                    "knowledge_candidate_skill_ids": [item.skill_id for item in knowledge_candidates],
                    "executor_candidate_count": len(executor_candidates),
                    "executor_candidate_skill_ids": [item.skill_id for item in executor_candidates],
                },
                evidence_ids=evidence_ids,
            )
            return None

        knowledge_bundle = KnowledgeContextBundle(selected_binding_keys=(), skills=())
        if knowledge_candidates:
            selection_started = time.monotonic()
            try:
                if not hasattr(self._skill_agent, "select_knowledge_skills"):
                    raise RuntimeError("agent_missing_select_knowledge_skills")
                knowledge_selection = self._skill_agent.select_knowledge_skills(
                    capability=capability,
                    stage=stage,
                    stage_summary=stage_summary,
                    candidates=[candidate.to_summary_dict() for candidate in knowledge_candidates],
                )
                selection_latency_ms = max(1, int((time.monotonic() - selection_started) * 1000))
                knowledge_bundle = self._load_knowledge_context_bundle(
                    capability=capability,
                    stage=stage,
                    node_name=node_name,
                    stage_summary=stage_summary,
                    evidence_ids=evidence_ids,
                    candidates=knowledge_candidates,
                    selected_binding_keys=list(getattr(knowledge_selection, "selected_binding_keys", []) or []),
                )
                self._report_observation_best_effort(
                    tool="skill.select",
                    node_name=node_name,
                    params={"capability": capability, "stage": stage, "selection_role": "knowledge"},
                    response={
                        "status": "ok",
                        "selection_role": "knowledge",
                        "latency_ms": selection_latency_ms,
                        "candidate_count": len(knowledge_candidates),
                        "candidate_skill_ids": [item.skill_id for item in knowledge_candidates],
                        "selected_binding_keys": list(knowledge_bundle.selected_binding_keys),
                        "selected_skill_ids": knowledge_bundle.skill_ids,
                        "reason": str(getattr(knowledge_selection, "reason", "") or ""),
                    },
                    evidence_ids=evidence_ids,
                )
            except Exception as exc:  # noqa: BLE001
                selection_latency_ms = max(1, int((time.monotonic() - selection_started) * 1000))
                self._report_observation_best_effort(
                    tool="skill.fallback",
                    node_name=node_name,
                    params={"capability": capability, "stage": stage, "selection_role": "knowledge"},
                    response={
                        "status": "fallback",
                        "selection_role": "knowledge",
                        "reason": DegradeReason.SKILL_KNOWLEDGE_SELECTION_FAILED.value,
                        "latency_ms": selection_latency_ms,
                        "candidate_count": len(knowledge_candidates),
                        "candidate_skill_ids": [item.skill_id for item in knowledge_candidates],
                        "error": _trim_text(exc),
                    },
                    evidence_ids=evidence_ids,
                )
                return None

        executor_started = time.monotonic()
        try:
            executor_selection = self._skill_agent.select_skill(
                capability=capability,
                stage=stage,
                stage_summary=self._executor_stage_summary(
                    stage_summary=stage_summary,
                    knowledge_bundle=knowledge_bundle,
                ),
                candidates=[candidate.to_summary_dict() for candidate in executor_candidates],
            )
            executor_latency_ms = max(1, int((time.monotonic() - executor_started) * 1000))
            selected_executor_key = str(getattr(executor_selection, "selected_binding_key", "") or "").strip()
            self._report_observation_best_effort(
                tool="skill.select",
                node_name=node_name,
                params={"capability": capability, "stage": stage, "selection_role": "executor"},
                response={
                    "status": "ok",
                    "selection_role": "executor",
                    "latency_ms": executor_latency_ms,
                    "candidate_count": len(executor_candidates),
                    "candidate_skill_ids": [item.skill_id for item in executor_candidates],
                    "selected_binding_key": selected_executor_key,
                    "knowledge_selected_binding_keys": list(knowledge_bundle.selected_binding_keys),
                    "knowledge_selected_skill_ids": knowledge_bundle.skill_ids,
                    "reason": str(getattr(executor_selection, "reason", "") or ""),
                },
                evidence_ids=evidence_ids,
            )
        except Exception as exc:  # noqa: BLE001
            executor_latency_ms = max(1, int((time.monotonic() - executor_started) * 1000))
            self._report_observation_best_effort(
                tool="skill.fallback",
                node_name=node_name,
                params={"capability": capability, "stage": stage, "selection_role": "executor"},
                response={
                    "status": "fallback",
                    "selection_role": "executor",
                    "reason": "executor_selection_failed",
                    "latency_ms": executor_latency_ms,
                    "candidate_count": len(executor_candidates),
                    "candidate_skill_ids": [item.skill_id for item in executor_candidates],
                    "knowledge_selected_binding_keys": list(knowledge_bundle.selected_binding_keys),
                    "knowledge_selected_skill_ids": knowledge_bundle.skill_ids,
                    "error": _trim_text(exc),
                },
                evidence_ids=evidence_ids,
            )
            return None

        if not selected_executor_key:
            self._report_observation_best_effort(
                tool="skill.fallback",
                node_name=node_name,
                params={"capability": capability, "stage": stage},
                response={
                    "status": "fallback",
                    "reason": "executor_not_selected",
                    "candidate_count": len(executor_candidates),
                    "candidate_skill_ids": [item.skill_id for item in executor_candidates],
                    "knowledge_selected_binding_keys": list(knowledge_bundle.selected_binding_keys),
                    "knowledge_selected_skill_ids": knowledge_bundle.skill_ids,
                },
                evidence_ids=evidence_ids,
            )
            return None

        selected_candidate = self._resolve_candidate_by_binding_key(executor_candidates, selected_executor_key)
        if selected_candidate is None:
            self._report_observation_best_effort(
                tool="skill.fallback",
                node_name=node_name,
                params={"capability": capability, "stage": stage},
                response={
                    "status": "fallback",
                    "reason": "selected_executor_not_found",
                    "selected_binding_key": selected_executor_key,
                    "candidate_skill_ids": [item.skill_id for item in executor_candidates],
                },
                evidence_ids=evidence_ids,
            )
            return None

        consume_started = time.monotonic()
        try:
            skill_document = self._skill_catalog.load_skill_document(selected_candidate.binding_key)
            selected_skill_resources = self._select_skill_resources(
                capability=capability,
                stage=stage,
                node_name=node_name,
                stage_summary=self._resource_selection_stage_summary(
                    stage_summary=stage_summary,
                    knowledge_bundle=knowledge_bundle,
                ),
                evidence_ids=evidence_ids,
                selected_candidate=selected_candidate,
                role="executor",
                skill_document=skill_document,
                knowledge_context_summary=knowledge_bundle.to_selection_summary(),
            )
            raw_output, post_apply_context, success_observation_tool = self._execute_selected_skill(
                capability=capability,
                stage=stage,
                graph_state=graph_state,
                node_name=node_name,
                evidence_ids=evidence_ids,
                selected_candidate=selected_candidate,
                input_payload=input_payload,
                knowledge_context=knowledge_bundle.to_agent_payload(),
                skill_resources=selected_skill_resources,
                output_contract=output_contract,
                skill_document=skill_document,
            )
        except Exception as exc:  # noqa: BLE001
            consume_latency_ms = max(1, int((time.monotonic() - consume_started) * 1000))
            self._report_observation_best_effort(
                tool="skill.fallback",
                node_name=node_name,
                params={"capability": capability, "stage": stage, "selected_binding_key": selected_candidate.binding_key},
                response={
                    "status": "fallback",
                    "reason": DegradeReason.SCRIPT_EXECUTE_FAILED.value if isinstance(exc, ScriptExecutorError) else DegradeReason.CONSUME_FAILED.value,
                    "latency_ms": consume_latency_ms,
                    "skill_id": selected_candidate.skill_id,
                    "selected_binding_key": selected_candidate.binding_key,
                    "executor_mode": selected_candidate.executor_mode,
                    "knowledge_selected_binding_keys": list(knowledge_bundle.selected_binding_keys),
                    "knowledge_selected_skill_ids": knowledge_bundle.skill_ids,
                    "error": _trim_text(exc),
                },
                evidence_ids=evidence_ids,
            )
            return None

        normalized_output, dropped_fields = sanitize_output(raw_output)
        if normalized_output.session_patch and not str(normalized_output.session_patch.get("actor") or "").strip():
            normalized_output.session_patch["actor"] = f"skill:{selected_candidate.skill_id}"
        if normalized_output.session_patch and not str(normalized_output.session_patch.get("source") or "").strip():
            normalized_output.session_patch["source"] = (
                "skill.script" if selected_candidate.executor_mode == "script" else "skill.prompt"
            )
        apply_result(graph_state, normalized_output, self.merge_session_patch)
        if isinstance(post_apply_context, dict):
            self._apply_prompt_skill_post_merge(
                graph_state=graph_state,
                result=normalized_output,
                post_apply_context=post_apply_context,
            )
        consume_latency_ms = max(1, int((time.monotonic() - consume_started) * 1000))
        self._report_observation_best_effort(
            tool=success_observation_tool,
            node_name=node_name,
            params={
                "capability": capability,
                "stage": stage,
                "selected_binding_key": selected_candidate.binding_key,
                "executor_mode": selected_candidate.executor_mode,
            },
            response={
                "status": "ok",
                "latency_ms": consume_latency_ms,
                "skill_id": selected_candidate.skill_id,
                "selected_binding_key": selected_candidate.binding_key,
                "executor_mode": selected_candidate.executor_mode,
                "knowledge_selected_binding_keys": list(knowledge_bundle.selected_binding_keys),
                "knowledge_selected_skill_ids": knowledge_bundle.skill_ids,
                "skill_resource_ids": [str(item.get("resource_id") or "") for item in selected_skill_resources],
                "payload_keys": sorted(normalized_output.payload.keys()),
                "session_patch_keys": sorted(normalized_output.session_patch.keys()),
                "observation_count": len(normalized_output.observations),
                "dropped_fields": dropped_fields,
            },
            evidence_ids=evidence_ids,
        )
        if dropped_fields:
            self._report_observation_best_effort(
                tool="skill.fallback",
                node_name=node_name,
                params={"capability": capability, "stage": stage, "selected_binding_key": selected_candidate.binding_key},
                response={
                    "status": "fallback",
                    "reason": DegradeReason.PAYLOAD_FIELDS_DROPPED.value,
                    "skill_id": selected_candidate.skill_id,
                    "selected_binding_key": selected_candidate.binding_key,
                    "fields": dropped_fields,
                },
                evidence_ids=evidence_ids,
            )
        return {
            "selected_binding_key": selected_candidate.binding_key,
            "skill_id": selected_candidate.skill_id,
            "version": selected_candidate.version,
            "capability": capability,
            "knowledge_skill_ids": knowledge_bundle.skill_ids,
            "knowledge_binding_keys": list(knowledge_bundle.selected_binding_keys),
            "payload": normalized_output.payload,
            "session_patch": normalized_output.session_patch,
            "observations": normalized_output.observations,
        }

    def _load_knowledge_context_bundle(
        self,
        *,
        capability: str,
        stage: str,
        node_name: str,
        stage_summary: dict[str, Any],
        evidence_ids: list[str],
        candidates: list["SkillCandidate"],
        selected_binding_keys: list[str],
    ) -> KnowledgeContextBundle:
        selected: list[dict[str, Any]] = []
        normalized_keys: list[str] = []
        for raw_key in selected_binding_keys:
            candidate = self._resolve_candidate_by_binding_key(candidates, raw_key)
            if candidate is None:
                continue
            skill_document = self._skill_catalog.load_skill_document(candidate.binding_key)
            selected_resources = self._select_skill_resources(
                capability=capability,
                stage=stage,
                node_name=node_name,
                stage_summary=stage_summary,
                evidence_ids=evidence_ids,
                selected_candidate=candidate,
                role="knowledge",
                skill_document=skill_document,
                knowledge_context_summary=[],
            )
            normalized_keys.append(candidate.binding_key)
            selected.append(
                {
                    "binding_key": candidate.binding_key,
                    "skill_id": candidate.skill_id,
                    "version": candidate.version,
                    "name": candidate.name,
                    "description": candidate.description,
                    "compatibility": candidate.compatibility,
                    "role": candidate.role,
                    "document": skill_document,
                    "resources": selected_resources,
                }
            )
        return KnowledgeContextBundle(
            selected_binding_keys=tuple(normalized_keys),
            skills=tuple(selected),
        )

    def _select_skill_resources(
        self,
        *,
        capability: str,
        stage: str,
        node_name: str,
        stage_summary: dict[str, Any],
        evidence_ids: list[str],
        selected_candidate: "SkillCandidate",
        role: str,
        skill_document: str,
        knowledge_context_summary: list[dict[str, Any]],
    ) -> list[dict[str, Any]]:
        if self._skill_catalog is None:
            return []
        available_resources = self._skill_catalog.list_skill_resources(selected_candidate.binding_key)
        if not available_resources:
            return []
        if self._skill_agent is None or not hasattr(self._skill_agent, "select_skill_resources"):
            return []

        selection_started = time.monotonic()
        try:
            selection = self._skill_agent.select_skill_resources(
                capability=capability,
                skill_id=selected_candidate.skill_id,
                skill_version=selected_candidate.version,
                role=role,
                skill_document=skill_document,
                stage_summary=stage_summary,
                available_resources=[item.to_summary_dict() for item in available_resources],
                knowledge_context=knowledge_context_summary,
            )
            selection_latency_ms = max(1, int((time.monotonic() - selection_started) * 1000))
        except Exception as exc:  # noqa: BLE001
            self._report_observation_best_effort(
                tool="skill.fallback",
                node_name=node_name,
                params={
                    "capability": capability,
                    "stage": stage,
                    "binding_key": selected_candidate.binding_key,
                    "role": role,
                },
                response={
                    "status": "fallback",
                    "reason": "resource_selection_failed",
                    "latency_ms": max(1, int((time.monotonic() - selection_started) * 1000)),
                    "skill_id": selected_candidate.skill_id,
                    "candidate_resource_ids": [item.resource_id for item in available_resources],
                    "error": _trim_text(exc),
                },
                evidence_ids=evidence_ids,
            )
            return []

        raw_selected_ids = list(getattr(selection, "selected_resource_ids", []) or [])
        self._report_observation_best_effort(
            tool="skill.resource_select",
            node_name=node_name,
            params={
                "capability": capability,
                "stage": stage,
                "binding_key": selected_candidate.binding_key,
                "role": role,
            },
            response={
                "status": "ok",
                "latency_ms": selection_latency_ms,
                "skill_id": selected_candidate.skill_id,
                "candidate_resource_ids": [item.resource_id for item in available_resources],
                "selected_resource_ids": [str(item).strip() for item in raw_selected_ids if str(item).strip()],
                "reason": str(getattr(selection, "reason", "") or ""),
            },
            evidence_ids=evidence_ids,
        )

        available_by_id = {item.resource_id: item for item in available_resources}
        selected_ids: list[str] = []
        invalid_ids: list[str] = []
        overflow_ids: list[str] = []
        seen_ids: set[str] = set()
        for raw_id in raw_selected_ids:
            resource_id = str(raw_id or "").strip()
            if not resource_id or resource_id in seen_ids:
                continue
            seen_ids.add(resource_id)
            if resource_id not in available_by_id:
                invalid_ids.append(resource_id)
                continue
            if len(selected_ids) >= _MAX_SELECTED_SKILL_RESOURCES:
                overflow_ids.append(resource_id)
                continue
            selected_ids.append(resource_id)
        if invalid_ids or overflow_ids:
            self._report_observation_best_effort(
                tool="skill.fallback",
                node_name=node_name,
                params={
                    "capability": capability,
                    "stage": stage,
                    "binding_key": selected_candidate.binding_key,
                    "role": role,
                },
                response={
                    "status": "fallback",
                    "reason": "resource_selection_filtered",
                    "skill_id": selected_candidate.skill_id,
                    "invalid_resource_ids": invalid_ids,
                    "overflow_resource_ids": overflow_ids,
                },
                evidence_ids=evidence_ids,
            )
        if not selected_ids:
            return []

        load_started = time.monotonic()
        loaded_resources = self._skill_catalog.load_skill_resources(selected_candidate.binding_key, selected_ids)
        loaded_payload = [item.to_agent_payload() for item in loaded_resources]
        loaded_ids = [item["resource_id"] for item in loaded_payload if str(item.get("resource_id") or "").strip()]
        missing_ids = [resource_id for resource_id in selected_ids if resource_id not in loaded_ids]
        if missing_ids:
            self._report_observation_best_effort(
                tool="skill.fallback",
                node_name=node_name,
                params={
                    "capability": capability,
                    "stage": stage,
                    "binding_key": selected_candidate.binding_key,
                    "role": role,
                },
                response={
                    "status": "fallback",
                    "reason": "resource_load_incomplete",
                    "skill_id": selected_candidate.skill_id,
                    "selected_resource_ids": selected_ids,
                    "missing_resource_ids": missing_ids,
                },
                evidence_ids=evidence_ids,
            )
        self._report_observation_best_effort(
            tool="skill.resource_load",
            node_name=node_name,
            params={
                "capability": capability,
                "stage": stage,
                "binding_key": selected_candidate.binding_key,
                "role": role,
            },
            response={
                "status": "ok",
                "latency_ms": max(1, int((time.monotonic() - load_started) * 1000)),
                "skill_id": selected_candidate.skill_id,
                "resource_ids": loaded_ids,
                "resource_count": len(loaded_payload),
                "total_bytes": sum(len(str(item.get("content") or "").encode("utf-8")) for item in loaded_payload),
            },
            evidence_ids=evidence_ids,
        )
        return loaded_payload

    def _resolve_candidate_by_binding_key(
        self,
        candidates: list["SkillCandidate"],
        binding_key: str,
    ) -> "SkillCandidate" | None:
        normalized = str(binding_key or "").strip()
        if not normalized:
            return None
        legacy_parts = normalized.split("\x00")
        legacy_executor_key = None
        if len(legacy_parts) == 3:
            legacy_executor_key = f"{legacy_parts[0]}\x00{legacy_parts[1]}\x00{legacy_parts[2]}\x00executor"
        for item in candidates:
            if item.binding_key == normalized:
                return item
            if legacy_executor_key and item.binding_key == legacy_executor_key:
                return item
        return None

    def _executor_stage_summary(
        self,
        *,
        stage_summary: dict[str, Any],
        knowledge_bundle: KnowledgeContextBundle,
    ) -> dict[str, Any]:
        summarized = dict(stage_summary)
        summarized["knowledge_candidate_count"] = len(knowledge_bundle.selected_binding_keys)
        summarized["knowledge_skill_ids"] = knowledge_bundle.skill_ids
        return summarized

    def _resource_selection_stage_summary(
        self,
        *,
        stage_summary: dict[str, Any],
        knowledge_bundle: KnowledgeContextBundle,
    ) -> dict[str, Any]:
        summarized = self._executor_stage_summary(stage_summary=stage_summary, knowledge_bundle=knowledge_bundle)
        summarized["knowledge_context"] = knowledge_bundle.to_selection_summary()
        return summarized

    def _execute_selected_skill(
        self,
        *,
        capability: str,
        stage: str,
        graph_state: Any,
        node_name: str,
        evidence_ids: list[str],
        selected_candidate: "SkillCandidate",
        input_payload: dict[str, Any],
        knowledge_context: list[dict[str, Any]],
        skill_resources: list[dict[str, Any]],
        output_contract: dict[str, Any],
        skill_document: str,
    ) -> tuple[PromptSkillConsumeResult, dict[str, Any] | None, str]:
        if (
            capability == "evidence.plan"
            and self._skills_tool_calling_mode in {"evidence_plan_single_hop", "evidence_plan_dual_tool"}
        ):
            raw_output, post_apply_context = self._consume_evidence_plan_with_optional_tools(
                stage=stage,
                graph_state=graph_state,
                node_name=node_name,
                evidence_ids=evidence_ids,
                selected_candidate=selected_candidate,
                input_payload=input_payload,
                knowledge_context=knowledge_context,
                skill_resources=skill_resources,
                output_contract=output_contract,
                skill_document=skill_document,
            )
            return (
                raw_output,
                post_apply_context,
                "skill.execute" if selected_candidate.executor_mode == "script" else "skill.consume",
            )
        if selected_candidate.executor_mode == "script":
            return (
                self._execute_script_skill(
                    capability=capability,
                    selected_candidate=selected_candidate,
                    input_payload=input_payload,
                    knowledge_context=knowledge_context,
                    skill_resources=skill_resources,
                    tool_calling_mode="disabled",
                ),
                None,
                "skill.execute",
            )
        raw_output, post_apply_context = self._execute_prompt_skill(
            capability=capability,
            stage=stage,
            graph_state=graph_state,
            node_name=node_name,
            evidence_ids=evidence_ids,
            selected_candidate=selected_candidate,
            input_payload=input_payload,
            knowledge_context=knowledge_context,
            skill_resources=skill_resources,
            output_contract=output_contract,
            skill_document=skill_document,
        )
        return raw_output, post_apply_context, "skill.consume"

    def _execute_script_skill(
        self,
        *,
        capability: str,
        selected_candidate: "SkillCandidate",
        input_payload: dict[str, Any],
        knowledge_context: list[dict[str, Any]],
        skill_resources: list[dict[str, Any]],
        tool_calling_mode: str,
    ) -> PromptSkillConsumeResult:
        result = self._run_script_skill_phase(
            capability=capability,
            selected_candidate=selected_candidate,
            input_payload=input_payload,
            knowledge_context=knowledge_context,
            skill_resources=skill_resources,
            phase="final",
            tool_requests=[],
            tool_results=[],
            tool_calling_mode=tool_calling_mode,
        )
        if result.tool_calls:
            raise ScriptExecutorError("script executor tool_calls are not supported for this capability")
        return _script_result_to_prompt_result(result)

    def _run_script_skill_phase(
        self,
        *,
        capability: str,
        selected_candidate: "SkillCandidate",
        input_payload: dict[str, Any],
        knowledge_context: list[dict[str, Any]],
        skill_resources: list[dict[str, Any]],
        phase: str,
        tool_requests: list[dict[str, Any]],
        tool_results: list[dict[str, Any]],
        tool_calling_mode: str,
    ) -> ScriptExecutorResult:
        if self._skill_catalog is None:
            raise ScriptExecutorError("skill catalog is not configured")
        catalog_skill = self._skill_catalog.get_skill(selected_candidate.binding_key)
        return self._script_executor_runner.run(
            bundle_root=catalog_skill.root_dir,
            input_payload=input_payload,
            ctx={
                "capability": capability,
                "skill_id": selected_candidate.skill_id,
                "version": selected_candidate.version,
                "role": selected_candidate.role,
                "knowledge_context": knowledge_context,
                "skill_resources": skill_resources,
                "allowed_tools": list(selected_candidate.allowed_tools),
                "phase": phase,
                "tool_requests": list(tool_requests),
                "tool_results": list(tool_results),
                "tool_calling_mode": tool_calling_mode,
            },
            module_suffix=f"{selected_candidate.skill_id}_{selected_candidate.version}_{selected_candidate.binding_key}",
        )

    def _execute_prompt_skill(
        self,
        *,
        capability: str,
        stage: str,
        graph_state: Any,
        node_name: str,
        evidence_ids: list[str],
        selected_candidate: "SkillCandidate",
        input_payload: dict[str, Any],
        knowledge_context: list[dict[str, Any]],
        skill_resources: list[dict[str, Any]],
        output_contract: dict[str, Any],
        skill_document: str,
    ) -> tuple[PromptSkillConsumeResult, dict[str, Any] | None]:
        return (
            self._skill_agent.consume_skill(
                capability=capability,
                skill_id=selected_candidate.skill_id,
                skill_version=selected_candidate.version,
                skill_document=skill_document,
                input_payload=input_payload,
                knowledge_context=knowledge_context,
                skill_resources=skill_resources,
                output_contract=output_contract,
            ),
            None,
        )

    def _consume_evidence_plan_with_optional_tools(
        self,
        *,
        stage: str,
        graph_state: Any,
        node_name: str,
        evidence_ids: list[str],
        selected_candidate: "SkillCandidate",
        input_payload: dict[str, Any],
        knowledge_context: list[dict[str, Any]],
        skill_resources: list[dict[str, Any]],
        output_contract: dict[str, Any],
        skill_document: str,
    ) -> tuple[PromptSkillConsumeResult, dict[str, Any] | None]:
        available_tools = self._available_evidence_plan_prompt_tools(selected_candidate)
        tool_plan_started = time.monotonic()
        if selected_candidate.executor_mode == "script":
            initial_result = self._run_script_skill_phase(
                capability="evidence.plan",
                selected_candidate=selected_candidate,
                input_payload=input_payload,
                knowledge_context=knowledge_context,
                skill_resources=skill_resources,
                phase="plan_tools",
                tool_requests=[],
                tool_results=[],
                tool_calling_mode=self._skills_tool_calling_mode,
            )
            if initial_result.tool_calls and initial_result.payload:
                raise ScriptExecutorError("script executor plan_tools phase must return tool_calls or payload, not both")
            if initial_result.tool_calls and initial_result.session_patch:
                raise ScriptExecutorError("script executor plan_tools phase must not return session_patch with tool_calls")
            try:
                tool_requests = self._validate_skill_tool_sequence(
                    raw_plans=initial_result.tool_calls,
                    allowed_tools=available_tools,
                )
            except Exception as exc:  # noqa: BLE001
                raise ScriptExecutorError(f"script executor returned invalid tool_calls: {exc}") from exc
        else:
            if not available_tools:
                self._report_observation_best_effort(
                    tool="skill.fallback",
                    node_name=node_name,
                    params={
                        "capability": "evidence.plan",
                        "stage": stage,
                        "selected_binding_key": selected_candidate.binding_key,
                    },
                    response={
                        "status": "fallback",
                        "reason": "tool_calling_not_allowed",
                        "skill_id": selected_candidate.skill_id,
                        "selected_binding_key": selected_candidate.binding_key,
                        "allowed_tools": self._effective_prompt_skill_tools(selected_candidate),
                    },
                    evidence_ids=evidence_ids,
                )
                return (
                    self._skill_agent.consume_skill(
                        capability="evidence.plan",
                        skill_id=selected_candidate.skill_id,
                        skill_version=selected_candidate.version,
                        skill_document=skill_document,
                        input_payload=input_payload,
                        knowledge_context=knowledge_context,
                        skill_resources=skill_resources,
                        output_contract=output_contract,
                    ),
                    None,
                )
            initial_result = None
            tool_requests = self._plan_evidence_prompt_tool_sequence(
                selected_candidate=selected_candidate,
                skill_document=skill_document,
                input_payload=input_payload,
                knowledge_context=knowledge_context,
                skill_resources=skill_resources,
                available_tools=available_tools,
            )
        tool_plan_latency_ms = max(1, int((time.monotonic() - tool_plan_started) * 1000))
        self._report_observation_best_effort(
            tool="skill.tool_plan",
            node_name=node_name,
            params={
                "capability": "evidence.plan",
                "stage": stage,
                "selected_binding_key": selected_candidate.binding_key,
            },
            response={
                "status": "ok",
                "latency_ms": tool_plan_latency_ms,
                "skill_id": selected_candidate.skill_id,
                "selected_binding_key": selected_candidate.binding_key,
                "allowed_tools": [tool.removeprefix("mcp.") for tool in available_tools],
                "tool_call_count": len(tool_requests),
                "tool_calls": [
                    {
                        "tool": str(item.get("tool") or ""),
                        "reason": str(item.get("reason") or ""),
                    }
                    for item in tool_requests
                ],
            },
            evidence_ids=evidence_ids,
        )
        if not tool_requests:
            if selected_candidate.executor_mode == "script":
                if initial_result is None:
                    raise ScriptExecutorError("script executor initial result missing")
                return _script_result_to_prompt_result(initial_result), None
            return (
                self._skill_agent.consume_skill(
                    capability="evidence.plan",
                    skill_id=selected_candidate.skill_id,
                    skill_version=selected_candidate.version,
                    skill_document=skill_document,
                    input_payload=input_payload,
                    knowledge_context=knowledge_context,
                    skill_resources=skill_resources,
                    output_contract=output_contract,
                ),
                None,
            )

        tool_results: list[SkillToolCallResult] = []
        for tool_request in tool_requests:
            tool_results.append(
                self._run_prompt_skill_tool(
                    graph_state=graph_state,
                    selected_candidate=selected_candidate,
                    evidence_ids=evidence_ids,
                    tool_request=tool_request,
                )
            )
        if selected_candidate.executor_mode == "script":
            raw_output = self._consume_script_skill_after_tools(
                selected_candidate=selected_candidate,
                input_payload=input_payload,
                knowledge_context=knowledge_context,
                skill_resources=skill_resources,
                tool_requests=tool_requests,
                tool_results=tool_results,
            )
        else:
            raw_output = self._consume_prompt_skill_after_tools(
                capability="evidence.plan",
                selected_candidate=selected_candidate,
                skill_document=skill_document,
                input_payload=input_payload,
                knowledge_context=knowledge_context,
                skill_resources=skill_resources,
                tool_results=tool_results,
                output_contract=output_contract,
            )
        return (
            raw_output,
            {
                "kind": "evidence_plan_tool_warm",
                "tool_results": _serialize_tool_results(tool_results),
            },
        )

    def _available_evidence_plan_prompt_tools(self, selected_candidate: "SkillCandidate") -> list[str]:
        """Generate available tools list dynamically from skill binding.

        Returns tool names with mcp. prefix for use in prompt skill tool planning.
        """
        effective_tools = self._effective_prompt_skill_tools(selected_candidate)
        # Dynamically generate available tools with mcp. prefix
        return [f"mcp.{tool}" for tool in effective_tools if tool]

    def _plan_evidence_prompt_tool_sequence(
        self,
        *,
        selected_candidate: "SkillCandidate",
        skill_document: str,
        input_payload: dict[str, Any],
        knowledge_context: list[dict[str, Any]],
        skill_resources: list[dict[str, Any]],
        available_tools: list[str],
    ) -> list[dict[str, Any]]:
        max_tool_calls = 1 if self._skills_tool_calling_mode == "evidence_plan_single_hop" else 2
        if max_tool_calls == 1 and hasattr(self._skill_agent, "plan_tool_call"):
            tool_plan = self._skill_agent.plan_tool_call(
                capability="evidence.plan",
                skill_id=selected_candidate.skill_id,
                skill_version=selected_candidate.version,
                skill_document=skill_document,
                input_payload=input_payload,
                knowledge_context=knowledge_context,
                skill_resources=skill_resources,
                available_tools=available_tools,
            )
            if not str(getattr(tool_plan, "tool", "") or "").strip():
                return []
            return [self._validate_prompt_skill_tool_plan(tool_plan, allowed_tools=available_tools)]

        if not hasattr(self._skill_agent, "plan_tool_calls"):
            raise RuntimeError("agent_missing_plan_tool_calls")
        tool_sequence = self._skill_agent.plan_tool_calls(
            capability="evidence.plan",
            skill_id=selected_candidate.skill_id,
            skill_version=selected_candidate.version,
            skill_document=skill_document,
            input_payload=input_payload,
            knowledge_context=knowledge_context,
            skill_resources=skill_resources,
            available_tools=available_tools,
            max_tool_calls=max_tool_calls,
        )
        raw_plans = getattr(tool_sequence, "tool_calls", [])
        return self._validate_skill_tool_sequence(raw_plans=raw_plans, allowed_tools=available_tools)

    def _validate_skill_tool_sequence(
        self,
        *,
        raw_plans: Any,
        allowed_tools: list[str],
    ) -> list[dict[str, Any]]:
        if not isinstance(raw_plans, list):
            raw_plans = []
        max_tool_calls = 1 if self._skills_tool_calling_mode == "evidence_plan_single_hop" else 2
        if len(raw_plans) > max_tool_calls:
            raise RuntimeError("prompt skill tool sequence exceeds max_tool_calls")
        validated: list[dict[str, Any]] = []
        seen_tools: set[str] = set()
        for raw_plan in raw_plans:
            tool_request = self._validate_prompt_skill_tool_plan(raw_plan, allowed_tools=allowed_tools)
            tool_name = str(tool_request.get("tool") or "")
            if tool_name in seen_tools:
                raise RuntimeError(f"prompt skill tool sequence repeats tool: {tool_name}")
            seen_tools.add(tool_name)
            validated.append(tool_request)
        return validated

    def _consume_prompt_skill_after_tools(
        self,
        *,
        capability: str,
        selected_candidate: "SkillCandidate",
        skill_document: str,
        input_payload: dict[str, Any],
        knowledge_context: list[dict[str, Any]],
        skill_resources: list[dict[str, Any]],
        tool_results: list[SkillToolCallResult],
        output_contract: dict[str, Any],
    ) -> PromptSkillConsumeResult:
        serialized_tool_results = [
            {
                "tool": item.tool_name,
                "tool_request": item.tool_request,
                "tool_result": item.tool_result,
                "latency_ms": item.latency_ms,
            }
            for item in tool_results
        ]
        if hasattr(self._skill_agent, "consume_after_tools"):
            return self._skill_agent.consume_after_tools(
                capability=capability,
                skill_id=selected_candidate.skill_id,
                skill_version=selected_candidate.version,
                skill_document=skill_document,
                input_payload=input_payload,
                knowledge_context=knowledge_context,
                skill_resources=skill_resources,
                tool_results=serialized_tool_results,
                output_contract=output_contract,
            )
        if len(serialized_tool_results) == 1 and hasattr(self._skill_agent, "consume_after_tool"):
            item = serialized_tool_results[0]
            return self._skill_agent.consume_after_tool(
                capability=capability,
                skill_id=selected_candidate.skill_id,
                skill_version=selected_candidate.version,
                skill_document=skill_document,
                input_payload=input_payload,
                knowledge_context=knowledge_context,
                skill_resources=skill_resources,
                tool_request=item["tool_request"],
                tool_result=item["tool_result"],
                output_contract=output_contract,
            )
        raise RuntimeError("agent_missing_consume_after_tools")

    def _consume_script_skill_after_tools(
        self,
        *,
        selected_candidate: "SkillCandidate",
        input_payload: dict[str, Any],
        knowledge_context: list[dict[str, Any]],
        skill_resources: list[dict[str, Any]],
        tool_requests: list[dict[str, Any]],
        tool_results: list[SkillToolCallResult],
    ) -> PromptSkillConsumeResult:
        serialized_tool_results = _serialize_tool_results(tool_results)
        final_result = self._run_script_skill_phase(
            capability="evidence.plan",
            selected_candidate=selected_candidate,
            input_payload=input_payload,
            knowledge_context=knowledge_context,
            skill_resources=skill_resources,
            phase="after_tools",
            tool_requests=[dict(item) for item in tool_requests],
            tool_results=serialized_tool_results,
            tool_calling_mode=self._skills_tool_calling_mode,
        )
        if final_result.tool_calls:
            raise ScriptExecutorError("script executor after_tools phase must not return tool_calls")
        return _script_result_to_prompt_result(final_result)

    def _apply_prompt_skill_post_merge(
        self,
        *,
        graph_state: Any,
        result: PromptSkillConsumeResult,
        post_apply_context: dict[str, Any],
    ) -> None:
        if str(post_apply_context.get("kind") or "") != "evidence_plan_tool_warm":
            return
        tool_results = post_apply_context.get("tool_results")
        if not isinstance(tool_results, list):
            return
        for item in tool_results:
            if not isinstance(item, dict):
                continue
            tool_name = str(item.get("tool_name") or "")
            tool_request = item.get("tool_request")
            tool_result = item.get("tool_result")
            latency_ms = item.get("latency_ms")
            if not isinstance(tool_request, dict) or not isinstance(tool_result, dict):
                continue
            # Use ToolMetadata.kind to determine warm logic
            tool_meta = get_tool_metadata(tool_name)
            tool_kind = tool_meta.kind if tool_meta else "unknown"
            if tool_kind == "logs":
                self._warm_logs_query_state(
                    graph_state=graph_state,
                    tool_request=tool_request,
                    tool_result=tool_result,
                    latency_ms=int(latency_ms or 0),
                )
            elif tool_kind == "metrics":
                self._warm_metrics_query_state(
                    graph_state=graph_state,
                    tool_request=tool_request,
                    tool_result=tool_result,
                    latency_ms=int(latency_ms or 0),
                )

    def _validate_prompt_skill_tool_plan(self, tool_plan: Any, *, allowed_tools: list[str]) -> dict[str, Any]:
        if isinstance(tool_plan, dict):
            tool_name = str(tool_plan.get("tool") or "").strip()
            input_payload = tool_plan.get("input")
            reason = str(tool_plan.get("reason") or "").strip()
        else:
            tool_name = str(getattr(tool_plan, "tool", "") or "").strip()
            input_payload = getattr(tool_plan, "input_payload", None)
            reason = str(getattr(tool_plan, "reason", "") or "").strip()

        # Check if tool is in allowed_tools (both have mcp. prefix from _available_evidence_plan_prompt_tools)
        if tool_name not in allowed_tools:
            raise RuntimeError(f"prompt skill tool is not allowed: {tool_name}")

        if not isinstance(input_payload, dict):
            raise RuntimeError("prompt skill tool plan requires object input")

        # Common required fields for all query tools
        datasource_id = str(input_payload.get("datasource_id") or "").strip()
        if not datasource_id:
            raise RuntimeError("prompt skill tool plan missing datasource_id")
        try:
            start_ts = _coerce_int(input_payload.get("start_ts"))
            end_ts = _coerce_int(input_payload.get("end_ts"))
        except (TypeError, ValueError) as exc:
            raise RuntimeError("prompt skill tool plan has invalid integer fields") from exc
        if start_ts <= 0 or end_ts <= 0 or end_ts < start_ts:
            raise RuntimeError("prompt skill tool plan has invalid time range")

        validated_input: dict[str, Any] = {
            "datasource_id": datasource_id,
            "start_ts": start_ts,
            "end_ts": end_ts,
        }

        # Determine tool kind from registry for type-specific validation
        tool_meta = get_tool_metadata(tool_name)
        tool_kind = tool_meta.kind if tool_meta else "unknown"

        if tool_kind == "logs":
            # Logs-specific validation: require query and limit
            query = str(input_payload.get("query") or "").strip()
            if not query:
                raise RuntimeError("prompt skill tool plan missing query")
            if query.lstrip().startswith("{"):
                raise RuntimeError("prompt skill tool plan must use queryText, not raw DSL")
            try:
                limit = _coerce_int(input_payload.get("limit"))
            except (TypeError, ValueError) as exc:
                raise RuntimeError("prompt skill tool plan has invalid integer fields") from exc
            if limit <= 0:
                raise RuntimeError("prompt skill tool plan has invalid limit")
            validated_input["query"] = query
            validated_input["limit"] = limit
        elif tool_kind == "metrics":
            # Metrics-specific validation: require promql and step_seconds
            promql = str(input_payload.get("promql") or "").strip()
            if not promql:
                raise RuntimeError("prompt skill tool plan missing promql")
            try:
                step_seconds = _coerce_int(input_payload.get("step_seconds"))
            except (TypeError, ValueError) as exc:
                raise RuntimeError("prompt skill tool plan has invalid integer fields") from exc
            if step_seconds <= 0:
                raise RuntimeError("prompt skill tool plan has invalid step_seconds")
            validated_input["promql"] = promql
            validated_input["step_seconds"] = step_seconds
        else:
            # Unknown or other tool kinds: accept additional params as-is
            for key in ("query", "promql", "limit", "step_seconds"):
                if key in input_payload:
                    validated_input[key] = input_payload[key]

        return {
            "tool": tool_name,
            "input_payload": validated_input,
            "reason": reason,
        }

    def _run_prompt_skill_tool(
        self,
        *,
        graph_state: Any,
        selected_candidate: "SkillCandidate",
        evidence_ids: list[str],
        tool_request: dict[str, Any],
    ) -> SkillToolCallResult:
        tool_name = str(tool_request.get("tool") or "")
        input_payload = tool_request.get("input_payload")
        if not isinstance(input_payload, dict):
            raise RuntimeError("prompt skill tool request missing input payload")
        started_at = time.monotonic()
        try:
            # Execute tool via MCP - no fallback, fail if MCP unavailable
            result = self.call_tool(tool=tool_name, params=input_payload)
        except Exception as exc:
            latency_ms = max(1, int((time.monotonic() - started_at) * 1000))
            self.report_tool_call(
                node_name="skill.evidence.plan",
                tool_name=tool_name or "mcp.unknown",
                request_json=input_payload,
                response_json={"status": "error"},
                latency_ms=latency_ms,
                status="error",
                error=_trim_text(exc),
                evidence_ids=evidence_ids,
            )
            if hasattr(graph_state, "tool_calls_written"):
                graph_state.tool_calls_written += 1
            raise
        latency_ms = max(1, int((time.monotonic() - started_at) * 1000))
        self.report_tool_call(
            node_name="skill.evidence.plan",
            tool_name=tool_name,
            request_json=input_payload,
            response_json=_query_toolcall_response(result),
            latency_ms=latency_ms,
            status="ok",
            evidence_ids=evidence_ids,
        )
        if hasattr(graph_state, "tool_calls_written"):
            graph_state.tool_calls_written += 1
        return SkillToolCallResult(
            tool_name=tool_name,
            tool_request=dict(input_payload),
            tool_result=result,
            latency_ms=latency_ms,
        )

    def _warm_logs_query_state(
        self,
        *,
        graph_state: Any,
        tool_request: dict[str, Any],
        tool_result: dict[str, Any],
        latency_ms: int,
    ) -> None:
        setattr(graph_state, "logs_query_request", dict(tool_request))
        setattr(graph_state, "logs_query_status", "ok")
        setattr(graph_state, "logs_query_output", dict(tool_result))
        setattr(graph_state, "logs_query_error", None)
        setattr(graph_state, "logs_query_latency_ms", max(int(latency_ms), 0))
        setattr(graph_state, "logs_query_result_size_bytes", _query_result_size_bytes(tool_result))
        current_logs_branch_meta = getattr(graph_state, "logs_branch_meta", None)
        if not isinstance(current_logs_branch_meta, dict):
            current_logs_branch_meta = {}
        merged_logs_branch_meta = dict(current_logs_branch_meta)
        merged_logs_branch_meta["tool_result_source"] = "skill_prompt_first"
        merged_logs_branch_meta["tool_result_reusable"] = True
        request_payload = merged_logs_branch_meta.get("request_payload")
        if not isinstance(request_payload, dict):
            request_payload = {}
        request_payload["query"] = str(tool_request.get("query") or "")
        merged_logs_branch_meta["request_payload"] = request_payload
        query_request = merged_logs_branch_meta.get("query_request")
        if not isinstance(query_request, dict):
            query_request = {}
        query_request["queryText"] = str(tool_request.get("query") or "")
        merged_logs_branch_meta["query_request"] = query_request
        setattr(graph_state, "logs_branch_meta", merged_logs_branch_meta)

    def _warm_metrics_query_state(
        self,
        *,
        graph_state: Any,
        tool_request: dict[str, Any],
        tool_result: dict[str, Any],
        latency_ms: int,
    ) -> None:
        setattr(graph_state, "metrics_query_request", dict(tool_request))
        setattr(graph_state, "metrics_query_status", "ok")
        setattr(graph_state, "metrics_query_output", dict(tool_result))
        setattr(graph_state, "metrics_query_error", None)
        setattr(graph_state, "metrics_query_latency_ms", max(int(latency_ms), 0))
        setattr(graph_state, "metrics_query_result_size_bytes", _query_result_size_bytes(tool_result))
        current_metrics_branch_meta = getattr(graph_state, "metrics_branch_meta", None)
        if not isinstance(current_metrics_branch_meta, dict):
            current_metrics_branch_meta = {}
        merged_metrics_branch_meta = dict(current_metrics_branch_meta)
        merged_metrics_branch_meta["tool_result_source"] = "skill_prompt_first"
        merged_metrics_branch_meta["tool_result_reusable"] = True
        request_payload = merged_metrics_branch_meta.get("request_payload")
        if not isinstance(request_payload, dict):
            request_payload = {}
        request_payload["promql"] = str(tool_request.get("promql") or "")
        if int(tool_request.get("step_seconds") or 0) > 0:
            request_payload["step_seconds"] = int(tool_request.get("step_seconds") or 0)
        merged_metrics_branch_meta["request_payload"] = request_payload
        query_request = merged_metrics_branch_meta.get("query_request")
        if not isinstance(query_request, dict):
            query_request = {}
        query_request["queryText"] = str(tool_request.get("promql") or "")
        merged_metrics_branch_meta["query_request"] = query_request
        setattr(graph_state, "metrics_branch_meta", merged_metrics_branch_meta)

    def get_incident(self, incident_id: str) -> dict[str, Any]:
        normalized = str(incident_id).strip()
        if not normalized:
            raise RuntimeError("incident_id is required")
        return self._execute_with_retry("incident.get", lambda: self._client.get_incident(normalized))

    def ensure_datasource(self, ds_base_url: str, ds_type: str = "prometheus") -> str:
        return self._execute_with_retry(
            "datasource.ensure",
            lambda: self._client.ensure_datasource(ds_base_url, ds_type),
        )

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
        if self._tool_invoker is None:
            raise RuntimeError("tool_invoker is not configured, cannot query metrics")
        tool_name = get_tool_name_by_kind("metrics")
        if not tool_name:
            raise RuntimeError("no metrics tool registered in ToolRegistry")
        request = {
            "datasource_id": datasource_id,
            "expr": promql,
            "time_range_start": {"seconds": int(start_ts), "nanos": 0},
            "time_range_end": {"seconds": int(end_ts), "nanos": 0},
            "step_seconds": int(step_s),
        }
        return self._query_via_tool_invoker(
            operation="query.metrics.tool",
            tool=tool_name,
            input_payload=request,
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
        if self._tool_invoker is None:
            raise RuntimeError("tool_invoker is not configured, cannot query logs")
        tool_name = get_tool_name_by_kind("logs")
        if not tool_name:
            raise RuntimeError("no logs tool registered in ToolRegistry")
        request = {
            "datasource_id": datasource_id,
            "query": query,
            "query_json": {},
            "time_range_start": {"seconds": int(start_ts), "nanos": 0},
            "time_range_end": {"seconds": int(end_ts), "nanos": 0},
            "limit": int(limit),
        }
        return self._query_via_tool_invoker(
            operation="query.logs.tool",
            tool=tool_name,
            input_payload=request,
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

    def get_claim_response(self):
        """Return the claim response from the last successful start() call.

        The response contains resolved skillsets and MCP server references for the job's pipeline.
        Returns ClaimStartResponse or None if not yet claimed.
        """
        return self._lease_manager.get_claim_response()

    def merge_tool_invoker(self, invoker: "ToolInvoker | ToolInvokerChain") -> None:
        """Merge a secondary tool invoker with the existing one.

        Creates a ToolInvokerChain that tries the existing invoker first,
        then falls back to the secondary invoker for tools not allowed by the first.

        This is used after claim to add MCP server tool providers resolved from
        the platform's McpServerConfigM.

        Args:
            invoker: Secondary invoker to merge (typically from MCP server refs).
        """
        if invoker is None:
            return
        if self._tool_invoker is None:
            self._tool_invoker = invoker
            return
        # Import locally to avoid circular import at module load time
        from ..tooling.invoker import ToolInvokerChain

        self._tool_invoker = ToolInvokerChain(toolset_invokers=[self._tool_invoker, invoker])

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

    def discover_tools(self) -> "ToolDiscoveryResult":
        """Discover all tools available through this runtime.

        Returns information about all tools that can be called via call_tool(),
        including their names, descriptions, tags, and provider information.

        Returns:
            ToolDiscoveryResult containing all available tools.
        """
        from .tool_discovery import discover_tools

        return discover_tools(self)

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


def _build_stage_summary(capability: str, input_payload: dict[str, Any]) -> dict[str, Any]:
    definition = get_capability_definition(capability)
    if definition is None:
        return {}
    return definition.build_stage_summary(input_payload)


def _skill_node_name(capability: str) -> str:
    normalized = str(capability or "").strip().replace(".", "_")
    return f"skills.{normalized or 'unknown'}"
