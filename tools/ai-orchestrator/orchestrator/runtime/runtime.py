from __future__ import annotations

import json
import os
import time
import uuid
from typing import TYPE_CHECKING, Any, Callable

from ..sdk.errors import RCAApiError
from ..skills.script_runner import ScriptExecutorError, ScriptExecutorResult, ScriptExecutorRunner
from ..tooling.invoker import TOOLING_META_KEY, ToolInvokeError
from ..tooling.toolset_config import normalize_tool_name
from ..tools_rca_api import RCAApiClient
from .audit import redact_sensitive, summarize_request, summarize_response
from .evidence_publisher import EvidencePublishResult, EvidencePublisher
from .lease_manager import LeaseManager
from .post_finalize import PostFinalizeObserver, PostFinalizeSnapshot
from .retry import RetryExecutor, RetryPolicy
from .tool_catalog import ExecutedToolCall, ToolCatalogSnapshot, ToolSpec, build_tool_catalog_snapshot
from .fc_adapter import FunctionCallingToolAdapter, NormalizedToolCall
from .tool_registry import get_tool_metadata, get_tool_name_by_kind
from .toolcall_reporter import ToolCallReporter
from .verification_runner import VerificationBudget, VerificationRunner, VerificationStepResult

if TYPE_CHECKING:
    from ..skills.agent import PromptSkillAgent
    from ..skills.runtime import SkillCandidate, SkillCatalog
    from ..tooling.invoker import ToolInvoker, ToolInvokerChain
    from .skill_coordinator import SkillAgentConfig, SkillCoordinator


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


def _validate_tool_input_payload(
    tool_name: str,
    input_payload: dict[str, Any],
    error_prefix: str = "tool plan",
) -> dict[str, Any]:
    """Validate common tool input payload fields.

    This is the unified validation logic for tool input payloads,
    used by both JSON and FC tool plan validation paths (FC3D).

    Args:
        tool_name: Name of the tool.
        input_payload: Input arguments to validate.
        error_prefix: Prefix for error messages.

    Returns:
        Validated and normalized input_payload dict.

    Raises:
        RuntimeError: If input is invalid.
    """
    if not isinstance(input_payload, dict):
        raise RuntimeError(f"{error_prefix} requires object input")

    # Common required fields for all query tools
    datasource_id = str(input_payload.get("datasource_id") or "").strip()
    if not datasource_id:
        raise RuntimeError(f"{error_prefix} missing datasource_id")
    try:
        start_ts = _coerce_int(input_payload.get("start_ts"))
        end_ts = _coerce_int(input_payload.get("end_ts"))
    except (TypeError, ValueError) as exc:
        raise RuntimeError(f"{error_prefix} has invalid integer fields") from exc
    if start_ts <= 0 or end_ts <= 0 or end_ts < start_ts:
        raise RuntimeError(f"{error_prefix} has invalid time range")

    validated_input: dict[str, Any] = {
        "datasource_id": datasource_id,
        "start_ts": start_ts,
        "end_ts": end_ts,
    }

    # Determine tool kind from registry for type-specific validation
    # Normalize tool name to canonical form for registry lookup
    normalized_tool_name = normalize_tool_name(tool_name)
    tool_meta = get_tool_metadata(normalized_tool_name)
    tool_kind = tool_meta.kind if tool_meta else "unknown"

    if tool_kind == "logs":
        # Logs-specific validation: require query and limit
        query = str(input_payload.get("query") or "").strip()
        if not query:
            raise RuntimeError(f"{error_prefix} missing query")
        if query.lstrip().startswith("{"):
            raise RuntimeError(f"{error_prefix} must use queryText, not raw DSL")
        try:
            limit = _coerce_int(input_payload.get("limit"))
        except (TypeError, ValueError) as exc:
            raise RuntimeError(f"{error_prefix} has invalid integer fields") from exc
        if limit <= 0:
            raise RuntimeError(f"{error_prefix} has invalid limit")
        validated_input["query"] = query
        validated_input["limit"] = limit
    elif tool_kind == "metrics":
        # Metrics-specific validation: require promql and step_seconds
        promql = str(input_payload.get("promql") or "").strip()
        if not promql:
            raise RuntimeError(f"{error_prefix} missing promql")
        try:
            step_seconds = _coerce_int(input_payload.get("step_seconds"))
        except (TypeError, ValueError) as exc:
            raise RuntimeError(f"{error_prefix} has invalid integer fields") from exc
        if step_seconds <= 0:
            raise RuntimeError(f"{error_prefix} has invalid step_seconds")
        validated_input["promql"] = promql
        validated_input["step_seconds"] = step_seconds
    else:
        # Unknown or other tool kinds: accept additional params as-is
        for key in ("query", "promql", "limit", "step_seconds"):
            if key in input_payload:
                validated_input[key] = input_payload[key]

    return validated_input


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
        tool_invoker: ToolInvoker | ToolInvokerChain | None = None,
        skill_catalog: SkillCatalog | None = None,
        tool_catalog_snapshot: ToolCatalogSnapshot | None = None,
        graph_llm: Any | None = None,
        skill_agent_config: "SkillAgentConfig | None" = None,
    ) -> None:
        self._client = client
        self._job_id = str(job_id).strip()
        self._instance_id = str(instance_id).strip()
        self._log_func = log_func
        self._tool_invoker = tool_invoker
        self._skill_catalog = skill_catalog
        self._graph_llm = graph_llm
        self._skill_agent_config = skill_agent_config
        self._script_executor_runner = ScriptExecutorRunner()
        self._skill_coordinator: SkillCoordinator | None = None  # Lazy-initialized coordinator
        self._started = False
        self._tool_catalog_snapshot = tool_catalog_snapshot
        self._fc_adapter_cache: FunctionCallingToolAdapter | None = None  # Cached FC adapter for FC3A unification
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
        # MCL: PostFinalizeObserver and VerificationRunner are kept for future extension
        # but are no longer called from the active graph path
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
            budget=VerificationBudget(),
            dedupe_enabled=True,
        )

    def start(self) -> bool:
        claimed = self._execute_with_retry("job.start", lambda: self._lease_manager.start(self._job_id))
        self._started = bool(claimed)
        return self._started

    def get_graph_llm(self) -> Any | None:
        """Get LLM instance for graph agents (Route/Domain/Platform agents).

        This is independent of prompt_first skill agent.
        Returns None if not configured.

        Returns:
            LLM instance (ChatOpenAI) or None.
        """
        return self._graph_llm

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

    def execute_skill_script(
        self,
        *,
        skill_binding_key: str,
        input_payload: dict[str, Any],
        phase: str = "final",
        tool_requests: list[dict[str, Any]] | None = None,
        tool_results: list[dict[str, Any]] | None = None,
        knowledge_context: list[dict[str, Any]] | None = None,
        skill_resources: list[dict[str, Any]] | None = None,
        allowed_tools: list[str] | None = None,
        tool_calling_mode: str = "disabled",
    ) -> ScriptExecutorResult:
        """Execute a skill's script executor.

        This method provides deterministic script execution for skill bundles
        that define a scripts/executor.py entry point. It is independent of LLM
        and can be used for:
        - Data transformation and normalization
        - Conditional logic that doesn't require LLM reasoning
        - Tool call planning with deterministic rules

        Args:
            skill_binding_key: The binding key of the skill to execute.
            input_payload: Input payload for the script.
            phase: Execution phase ("plan_tools", "after_tools", "final").
            tool_requests: Tool requests from plan_tools phase (for after_tools).
            tool_results: Tool results for after_tools phase.
            knowledge_context: Knowledge skill context from progressive disclosure.
            skill_resources: Loaded skill resources from progressive disclosure.
            allowed_tools: Tools allowed for this capability.
            tool_calling_mode: Tool calling mode (disabled, evidence_plan_single_hop, etc).

        Returns:
            ScriptExecutorResult with:
            - payload: Script output payload
            - session_patch: Session updates
            - observations: Observation records
            - tool_calls: Tool calls to request (plan_tools phase only)

        Raises:
            ScriptExecutorError: If skill catalog not configured, skill not found,
                or script execution fails.
        """
        if self._skill_catalog is None:
            raise ScriptExecutorError("skill catalog is not configured")

        catalog_skill = self._skill_catalog.get_skill(skill_binding_key)
        if catalog_skill is None:
            raise ScriptExecutorError(f"skill not found: {skill_binding_key}")

        return self._script_executor_runner.run(
            bundle_root=catalog_skill.root_dir,
            input_payload=input_payload,
            ctx={
                "phase": phase,
                "tool_requests": tool_requests or [],
                "tool_results": tool_results or [],
                "knowledge_context": knowledge_context or [],
                "skill_resources": skill_resources or [],
                "allowed_tools": allowed_tools or [],
                "tool_calling_mode": tool_calling_mode,
            },
            module_suffix=skill_binding_key.replace("/", "_").replace(".", "_"),
        )

    def execute_capability_skill(
        self,
        capability: str,
        input_payload: dict[str, Any],
        stage_summary: dict[str, Any] | None = None,
    ) -> "SkillExecutionResult":
        """Execute a capability skill with full coordination.

        This is the main entry point for capability-first skill execution.
        Coordinates: selection → resource loading → execution.

        Implements the progressive disclosure pattern:
        Summary → Selection → Resource Loading → Execution

        Args:
            capability: The capability to execute (e.g., "evidence.plan").
            input_payload: Input payload for the skill.
            stage_summary: Summary of current stage for selection context.

        Returns:
            SkillExecutionResult with payload, session_patch, observations.
            Check result.success and result.fallback_used to determine behavior.
        """
        from .skill_coordinator import SkillCoordinator, SkillExecutionResult

        coordinator = self._ensure_skill_coordinator()
        if coordinator is None:
            return SkillExecutionResult(
                success=False,
                error_message="skill coordinator not available",
                fallback_used=True,
            )

        return coordinator.execute_capability_skill(
            capability=capability,
            input_payload=input_payload,
            stage_summary=stage_summary or {},
        )

    def _ensure_skill_coordinator(self) -> "SkillCoordinator | None":
        """Ensure skill coordinator is initialized.

        The coordinator is lazily initialized on first use.

        Returns:
            SkillCoordinator instance or None if not available.
        """
        if self._skill_coordinator is not None:
            return self._skill_coordinator

        if self._skill_catalog is None:
            return None

        from ..skills.agent import PromptSkillAgent
        from .skill_coordinator import SkillCoordinator

        # Create a PromptSkillAgent using skill_agent_config if available
        if self._skill_agent_config is not None:
            agent = PromptSkillAgent(
                model=self._skill_agent_config.model,
                base_url=self._skill_agent_config.base_url,
                api_key=self._skill_agent_config.api_key,
                timeout_seconds=self._skill_agent_config.timeout_seconds,
            )
        else:
            # Create an unconfigured agent - will have configured=False
            # This means skill selection will be skipped and native fallback used
            agent = PromptSkillAgent(
                model="",
                base_url="",
                api_key="",
                timeout_seconds=30.0,
            )

        self._skill_coordinator = SkillCoordinator(
            catalog=self._skill_catalog,
            agent=agent,
            runtime=self,
        )
        return self._skill_coordinator

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

    def get_incident(self, incident_id: str) -> dict[str, Any]:
        normalized = str(incident_id).strip()
        if not normalized:
            raise RuntimeError("incident_id is required")
        return self._execute_with_retry("incident.get", lambda: self._client.get_incident(normalized))

    def list_alert_events_current(
        self,
        *,
        namespace: str | None = None,
        service: str | None = None,
        severity: str | None = None,
        page: int = 1,
        limit: int = 20,
    ) -> dict[str, Any]:
        return self._execute_with_retry(
            "alert_events.list_current",
            lambda: self._client.list_alert_events_current(
                namespace=namespace,
                service=service,
                severity=severity,
                page=page,
                limit=limit,
            ),
        )

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

    @property
    def tool_invoker(self) -> "ToolInvoker | ToolInvokerChain | None":
        """Get the current tool invoker, or None if not set."""
        return self._tool_invoker

    def set_tool_invoker(self, invoker: "ToolInvoker | ToolInvokerChain") -> None:
        """Set the tool invoker directly, replacing any existing one.

        This is used in claim_provider_snapshot mode where the invoker is built
        solely from claim response resolved_tool_providers, not from strategy toolsets.

        Args:
            invoker: The tool invoker to set.
        """
        if invoker is None:
            return
        self._tool_invoker = invoker

    def merge_tool_invoker(self, invoker: "ToolInvoker | ToolInvokerChain") -> None:
        """Merge a secondary tool invoker with the existing one.

        Creates a ToolInvokerChain that tries the existing invoker first,
        then falls back to the secondary invoker for tools not allowed by the first.

        This is used after claim to add MCP server tool providers resolved from
        the platform-side binding snapshot.

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

    def get_tool_catalog_snapshot(self) -> ToolCatalogSnapshot | None:
        """Get the current tool catalog snapshot.

        Returns:
            ToolCatalogSnapshot if set, None otherwise.
        """
        return self._tool_catalog_snapshot

    def set_tool_catalog_snapshot(self, snapshot: ToolCatalogSnapshot) -> None:
        """Set or update the tool catalog snapshot.

        This should be called after merging MCP server invokers to update
        the snapshot with the complete set of available tools.

        Args:
            snapshot: The new ToolCatalogSnapshot to use.
        """
        self._tool_catalog_snapshot = snapshot
        # Invalidate adapter cache when snapshot changes (FC3A)
        self._fc_adapter_cache = None

    def get_fc_adapter(self) -> FunctionCallingToolAdapter | None:
        """Get the cached FunctionCallingToolAdapter for this job.

        This is the unified adapter for both graph and skills tool binding.
        The adapter is cached to ensure consistent tool access throughout the job.

        FC3A: graph and skills should use this method instead of creating
        their own FunctionCallingToolAdapter instances.

        Returns:
            FunctionCallingToolAdapter if snapshot is available, None otherwise.
        """
        if self._tool_catalog_snapshot is None:
            return None
        if self._fc_adapter_cache is None:
            self._fc_adapter_cache = FunctionCallingToolAdapter(self._tool_catalog_snapshot)
        return self._fc_adapter_cache

    # FC3C: RuntimeToolGateway protocol implementation
    def list_tools(self) -> list[ToolSpec]:
        """List all available tools from the catalog snapshot.

        This is the unified entry point for tool discovery.
        Both graph nodes and skills should use this method.

        Returns:
            List of ToolSpec instances for all available tools.
            Returns empty list if snapshot is not available.
        """
        if self._tool_catalog_snapshot is None:
            return []
        return list(self._tool_catalog_snapshot.tools)

    def execute_tool(
        self,
        tool_name: str,
        args: dict[str, Any],
        *,
        source: str = "runtime",
    ) -> ExecutedToolCall:
        """Execute a tool and return unified result model.

        This is the unified entry point for tool execution.
        Both graph nodes and skills should use this method.

        Args:
            tool_name: Canonical name of the tool to execute.
            args: Input arguments for the tool.
            source: Identifier for the caller (e.g., "skill.plan", "graph.node", "fc_agent").

        Returns:
            ExecutedToolCall with execution details and result.

        Raises:
            RuntimeError: If tool execution fails.
        """
        started_at = time.monotonic()
        canonical_name = normalize_tool_name(tool_name)

        # Validate tool exists in snapshot
        if self._tool_catalog_snapshot is not None:
            if not self._tool_catalog_snapshot.has_tool(canonical_name):
                latency_ms = max(1, int((time.monotonic() - started_at) * 1000))
                return ExecutedToolCall(
                    tool_name=canonical_name,
                    request_json=args,
                    response_json={},
                    latency_ms=latency_ms,
                    source=source,
                    status="error",
                    error=f"tool not found in catalog: {canonical_name}",
                )

        try:
            result = self.call_tool(tool=canonical_name, params=args)
            latency_ms = max(1, int((time.monotonic() - started_at) * 1000))

            # Extract metadata from result if available
            provider_id = ""
            provider_type = ""
            resolved_from_toolset_id = ""
            if isinstance(result, dict):
                meta = result.pop(TOOLING_META_KEY, None) if TOOLING_META_KEY in result else None
                if isinstance(meta, dict):
                    provider_id = str(meta.get("provider_id") or "")
                    provider_type = str(meta.get("provider_type") or "")
                    resolved_from_toolset_id = str(meta.get("resolved_from_toolset_id") or "")

            return ExecutedToolCall(
                tool_name=canonical_name,
                request_json=args,
                response_json=result,
                latency_ms=latency_ms,
                source=source,
                status="ok",
                provider_id=provider_id,
                provider_type=provider_type,
                resolved_from_toolset_id=resolved_from_toolset_id,
            )
        except Exception as exc:
            latency_ms = max(1, int((time.monotonic() - started_at) * 1000))
            return ExecutedToolCall(
                tool_name=canonical_name,
                request_json=args,
                response_json={},
                latency_ms=latency_ms,
                source=source,
                status="error",
                error=_trim_text(str(exc)),
            )

    def build_tool_catalog_snapshot_from_invoker(self) -> ToolCatalogSnapshot:
        """Build a ToolCatalogSnapshot from the current tool invoker.

        This method extracts tool information from the invoker and builds
        an immutable snapshot for function calling.

        Returns:
            ToolCatalogSnapshot built from the invoker's tools.
        """
        toolset_ids = self._current_toolset_chain()
        tool_specs: list[ToolSpec] = []

        if self._tool_invoker is not None:
            # Get allowed tools from invoker
            try:
                allowed = self._tool_invoker.allowed_tools()
            except Exception:  # noqa: BLE001
                allowed = []
            if not isinstance(allowed, list):
                allowed = []

            # Get provider summaries for additional metadata
            provider_summaries: list[dict[str, Any]] = []
            try:
                summaries = self._tool_invoker.provider_summaries()
                if isinstance(summaries, list):
                    provider_summaries = [s for s in summaries if isinstance(s, dict)]
            except Exception:  # noqa: BLE001
                pass

            # Build a map of tool -> provider info
            tool_to_provider: dict[str, dict[str, Any]] = {}
            for summary in provider_summaries:
                provider_id = str(summary.get("provider_id") or "").strip()
                allow_tools = summary.get("allow_tools")
                if isinstance(allow_tools, list):
                    for tool in allow_tools:
                        tool_name = normalize_tool_name(str(tool))
                        if tool_name:
                            tool_to_provider[tool_name] = {
                                "provider_id": provider_id,
                                "provider_type": str(summary.get("provider_type") or ""),
                            }

            # Create ToolSpec for each tool
            for tool_name in allowed:
                canonical_name = normalize_tool_name(tool_name)
                if not canonical_name:
                    continue

                provider_info = tool_to_provider.get(canonical_name, {})
                metadata = get_tool_metadata(canonical_name)

                # Use metadata tags if available, otherwise infer from tool name
                if metadata:
                    tags = metadata.tags
                    kind = metadata.kind
                    description = metadata.description
                    read_only = metadata.read_only
                    risk_level = metadata.risk_level
                    tool_class = metadata.tool_class
                    allowed_for_prompt_skill = metadata.allowed_for_prompt_skill
                    allowed_for_graph_agent = metadata.allowed_for_graph_agent
                else:
                    # Import here to avoid circular dependency
                    from .tool_discovery import infer_tags_from_tool_name
                    tags = infer_tags_from_tool_name(canonical_name)
                    kind = "unknown"
                    description = ""
                    read_only = True
                    risk_level = "low"
                    tool_class = "fc_selectable"
                    allowed_for_prompt_skill = True
                    allowed_for_graph_agent = True

                spec = ToolSpec(
                    name=canonical_name,
                    description=description,
                    input_schema={},
                    output_schema={},
                    kind=kind,
                    tags=tags,
                    provider_id=provider_info.get("provider_id", ""),
                    read_only=read_only,
                    risk_level=risk_level,
                    tool_class=tool_class,
                    allowed_for_prompt_skill=allowed_for_prompt_skill,
                    allowed_for_graph_agent=allowed_for_graph_agent,
                )
                tool_specs.append(spec)

        return build_tool_catalog_snapshot(
            toolset_ids=toolset_ids,
            tool_specs=tool_specs,
        )

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
