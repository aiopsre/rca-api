from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor
import threading
import time
from typing import Any

from ..graph import OrchestratorConfig
from ..langgraph.registry import (
    UnknownPipelineError,
    get_template_builder,
    list_template_ids,
    normalize_pipeline,
)
from ..runtime.runtime import OrchestratorRuntime
from ..sdk.errors import RCAApiError
from ..skills import SkillCatalog, apply_session_patch_to_state, load_session_snapshot_into_state
from ..skills.agent import PromptSkillAgent
from ..state import GraphState
from ..tooling import (
    ToolInvoker,
    ToolInvokerChain,
    ToolsetConfig,
    build_tool_invoker,
    build_tool_invoker_chain,
    load_toolset_config,
    load_toolset_config_from_env,
)
from ..tools_rca_api import RCAApiClient
from .health import detect_pubsub_ready
from .settings import Settings, load_settings

_TOOLSET_CONFIG_CACHE_LOCK = threading.Lock()
_TOOLSET_CONFIG_CACHE_KEY: tuple[str, str] | None = None
_TOOLSET_CONFIG_CACHE_VALUE: ToolsetConfig | None = None
_TOOLSET_CONFIG_CACHE_ERROR: Exception | None = None
_TEMPLATE_REGISTRY_LOCK = threading.Lock()
_TEMPLATE_REGISTRY_LAST_ATTEMPT_TS = 0.0
_TEMPLATE_REGISTRY_REFRESH_SECONDS = 60.0


def _log(msg: str) -> None:
    print(msg, flush=True)


def _extract_jobs(payload: dict[str, Any]) -> list[dict[str, Any]]:
    jobs = payload.get("jobs")
    if isinstance(jobs, list):
        return [j for j in jobs if isinstance(j, dict)]
    return []


def _extract_job_id(job_obj: dict[str, Any]) -> str:
    return str(job_obj.get("jobID") or job_obj.get("job_id") or "").strip()


def _extract_pipeline(job_payload: dict[str, Any]) -> str:
    if not isinstance(job_payload, dict):
        return ""

    candidates: list[dict[str, Any]] = [job_payload]
    data = job_payload.get("data")
    if isinstance(data, dict):
        candidates.append(data)
        nested = data.get("job")
        if isinstance(nested, dict):
            candidates.append(nested)

    nested_job = job_payload.get("job")
    if isinstance(nested_job, dict):
        candidates.append(nested_job)

    for candidate in candidates:
        if "pipeline" in candidate:
            value = candidate.get("pipeline")
            if value is None:
                return ""
            return str(value).strip()
    return ""


def _new_client(settings: Settings) -> RCAApiClient:
    return RCAApiClient(
        settings.base_url,
        settings.scopes,
        instance_id=settings.instance_id,
        timeout_s=10.0,
        mcp_scopes=settings.mcp_scopes,
        mcp_verify_remote_tools=settings.mcp_verify_remote_tools,
    )


def _load_toolset_config_cached(settings: Settings) -> ToolsetConfig:
    global _TOOLSET_CONFIG_CACHE_KEY
    global _TOOLSET_CONFIG_CACHE_VALUE
    global _TOOLSET_CONFIG_CACHE_ERROR

    cache_key = (settings.toolset_config_path.strip(), settings.toolset_config_json.strip())
    with _TOOLSET_CONFIG_CACHE_LOCK:
        if _TOOLSET_CONFIG_CACHE_KEY == cache_key:
            if _TOOLSET_CONFIG_CACHE_ERROR is not None:
                raise _TOOLSET_CONFIG_CACHE_ERROR
            if _TOOLSET_CONFIG_CACHE_VALUE is None:
                raise RuntimeError("toolset config cache is empty")
            return _TOOLSET_CONFIG_CACHE_VALUE

    try:
        loaded = load_toolset_config_from_env(settings)
    except Exception as exc:  # noqa: BLE001
        with _TOOLSET_CONFIG_CACHE_LOCK:
            _TOOLSET_CONFIG_CACHE_KEY = cache_key
            _TOOLSET_CONFIG_CACHE_VALUE = None
            _TOOLSET_CONFIG_CACHE_ERROR = exc
        raise

    with _TOOLSET_CONFIG_CACHE_LOCK:
        _TOOLSET_CONFIG_CACHE_KEY = cache_key
        _TOOLSET_CONFIG_CACHE_VALUE = loaded
        _TOOLSET_CONFIG_CACHE_ERROR = None
    return loaded


def _select_tool_invoker(
    settings: Settings,
    client: RCAApiClient,
    pipeline: str,
) -> tuple[ToolInvoker | ToolInvokerChain, list[str], str]:
    if settings.toolset_config_path.strip() or settings.toolset_config_json.strip():
        config = _load_toolset_config_cached(settings)
        toolset_ids = config.get_toolset_chain(pipeline)
        invoker = build_tool_invoker_chain(config, toolset_ids)
        return invoker, toolset_ids, "local_override"
    return _select_tool_invoker_via_server(client, pipeline)


def _select_tool_invoker_via_server(
    client: RCAApiClient,
    pipeline: str,
) -> tuple[ToolInvoker | ToolInvokerChain, list[str], str]:
    client_base_url = str(getattr(client, "base_url", "") or "").strip()
    resolved = client.resolve_toolset(pipeline)
    if not isinstance(resolved, dict):
        raise RuntimeError("resolve_toolset returned invalid payload")

    normalized_pipeline = normalize_pipeline(pipeline)
    toolsets_payload = resolved.get("toolsets")
    if isinstance(toolsets_payload, list) and toolsets_payload:
        invoker, toolset_ids = _build_tool_invoker_chain_from_toolsets_payload(
            normalized_pipeline=normalized_pipeline,
            toolsets_payload=toolsets_payload,
            payload_name="resolve_toolset",
            default_base_url=client_base_url,
        )
        return invoker, toolset_ids, "server_resolve"

    toolset_payload = resolved.get("toolset")
    if not isinstance(toolset_payload, dict):
        toolset_payload = resolved
    if not isinstance(toolset_payload, dict):
        raise RuntimeError("resolve_toolset payload missing toolset object")

    toolset_id = _extract_toolset_id(toolset_payload)
    if not toolset_id:
        raise RuntimeError("resolve_toolset payload missing toolset_id")

    providers = toolset_payload.get("providers")
    if not isinstance(providers, list) or not providers:
        raise RuntimeError(f"resolve_toolset payload has empty providers: toolset={toolset_id}")
    normalized_providers = [
        _normalize_server_provider_payload(provider, default_base_url=client_base_url) for provider in providers
    ]

    config = load_toolset_config(
        {
            "pipelines": {normalized_pipeline: toolset_id},
            "toolsets": {
                toolset_id: {
                    "providers": normalized_providers,
                }
            },
        }
    )
    invoker = build_tool_invoker(config, toolset_id)
    return invoker, [toolset_id], "server_resolve"


def _supports_strategy_resolve(client: RCAApiClient) -> bool:
    return callable(getattr(client, "resolve_strategy", None))


def _resolve_strategy_via_server(client: RCAApiClient, pipeline: str) -> dict[str, Any]:
    resolved = client.resolve_strategy(pipeline)
    if not isinstance(resolved, dict):
        raise RuntimeError("resolve_strategy returned invalid payload")
    strategy = resolved.get("strategy")
    if isinstance(strategy, dict):
        return strategy
    return resolved


def _build_tool_invoker_chain_from_toolsets_payload(
    *,
    normalized_pipeline: str,
    toolsets_payload: list[Any],
    payload_name: str,
    default_base_url: str = "",
) -> tuple[ToolInvokerChain, list[str]]:
    toolset_ids: list[str] = []
    toolsets: dict[str, dict[str, Any]] = {}
    for index, toolset_item in enumerate(toolsets_payload, start=1):
        if not isinstance(toolset_item, dict):
            raise RuntimeError(f"{payload_name} payload has invalid toolsets[{index}] type")
        toolset_id = _extract_toolset_id(toolset_item)
        if not toolset_id:
            raise RuntimeError(f"{payload_name} payload missing toolset_id at toolsets[{index}]")
        if toolset_id in toolsets:
            raise RuntimeError(f"{payload_name} payload duplicated toolset_id={toolset_id}")
        providers = toolset_item.get("providers")
        if not isinstance(providers, list) or not providers:
            raise RuntimeError(f"{payload_name} payload has empty providers: toolset={toolset_id}")
        normalized_providers = [
            _normalize_server_provider_payload(provider, default_base_url=default_base_url) for provider in providers
        ]
        toolset_ids.append(toolset_id)
        toolsets[toolset_id] = {"providers": normalized_providers}

    config = load_toolset_config(
        {
            "pipelines": {normalized_pipeline: toolset_ids},
            "toolsets": toolsets,
        }
    )
    invoker = build_tool_invoker_chain(config, toolset_ids)
    return invoker, toolset_ids


def _extract_template_id(strategy_payload: dict[str, Any]) -> str:
    for key in ("templateID", "templateId", "template_id"):
        value = str(strategy_payload.get(key) or "").strip()
        if value:
            return value
    return ""


def _build_tool_invoker_from_strategy(
    settings: Settings,
    client: RCAApiClient,
    *,
    pipeline: str,
    strategy_payload: dict[str, Any],
) -> tuple[ToolInvoker | ToolInvokerChain, list[str], str]:
    # Keep local override semantics from Phase H/J for toolsets only.
    if settings.toolset_config_path.strip() or settings.toolset_config_json.strip():
        return _select_tool_invoker(settings, client, pipeline)

    normalized_pipeline = normalize_pipeline(pipeline)
    toolsets_payload = strategy_payload.get("toolsets")
    if not isinstance(toolsets_payload, list) or not toolsets_payload:
        raise RuntimeError("resolve_strategy payload missing non-empty toolsets")
    client_base_url = str(getattr(client, "base_url", "") or "").strip()
    invoker, toolset_ids = _build_tool_invoker_chain_from_toolsets_payload(
        normalized_pipeline=normalized_pipeline,
        toolsets_payload=toolsets_payload,
        payload_name="resolve_strategy",
        default_base_url=client_base_url,
    )
    return invoker, toolset_ids, "strategy_resolve"


def _normalize_server_provider_payload(provider_payload: Any, *, default_base_url: str) -> dict[str, Any]:
    if not isinstance(provider_payload, dict):
        raise RuntimeError("toolset provider entry must be an object")
    normalized = dict(provider_payload)
    provider_type = str(normalized.get("type") or "").strip().lower()
    if provider_type == "mcp_http":
        base_url = str(normalized.get("base_url") or normalized.get("baseURL") or "").strip()
        if not base_url:
            normalized["baseURL"] = str(default_base_url or "").strip().rstrip("/")
    return normalized


def _report_toolset_selection_observation(
    *,
    runtime: OrchestratorRuntime,
    job_id: str,
    pipeline: str,
    template_id: str,
    template_builder: Any,
    toolsets: list[str],
    toolset_source: str,
    tool_invoker: ToolInvoker | ToolInvokerChain | None,
) -> None:
    report_observation = getattr(runtime, "report_observation", None)
    if not callable(report_observation):
        return

    providers: list[dict[str, Any]] = []
    available_tools: list[str] = []
    if tool_invoker is not None:
        provider_summaries = getattr(tool_invoker, "provider_summaries", None)
        if callable(provider_summaries):
            try:
                raw = provider_summaries()
                if isinstance(raw, list):
                    providers = [item for item in raw if isinstance(item, dict)]
            except Exception:  # noqa: BLE001
                providers = []
        list_allowed_tools = getattr(tool_invoker, "allowed_tools", None)
        if callable(list_allowed_tools):
            try:
                raw_tools = list_allowed_tools()
                if isinstance(raw_tools, list):
                    available_tools = [str(item).strip() for item in raw_tools if str(item).strip()]
            except Exception:  # noqa: BLE001
                available_tools = []

    template_name = str(getattr(template_builder, "__name__", type(template_builder).__name__) or "unknown")
    try:
        report_observation(
            tool="toolset.select",
            node_name="runner.pre_graph",
            params={
                "pipeline": normalize_pipeline(pipeline),
                "template": template_name,
            },
            response={
                "status": "ok",
                "template_id": str(template_id).strip(),
                "toolsets": [str(item).strip() for item in toolsets if str(item).strip()],
                "source": str(toolset_source).strip(),
                "providers": providers,
                "available_tools": available_tools,
            },
            evidence_ids=[],
        )
    except Exception as exc:  # noqa: BLE001
        _log(f"job={job_id} pre-graph toolset observation failed: {exc}")


def _extract_skillset_ids(strategy_payload: dict[str, Any]) -> list[str]:
    raw = (
        strategy_payload.get("skillsetIDs")
        or strategy_payload.get("skillsetIds")
        or strategy_payload.get("skillset_ids")
        or []
    )
    if not isinstance(raw, list):
        return []
    out: list[str] = []
    seen: set[str] = set()
    for item in raw:
        normalized = str(item or "").strip()
        if not normalized or normalized in seen:
            continue
        seen.add(normalized)
        out.append(normalized)
    return out


def _parse_skills_local_paths(raw: str) -> list[str]:
    return [item.strip() for item in str(raw or "").split(",") if item.strip()]


def _build_skill_catalog(
    *,
    settings: Settings,
    client: RCAApiClient,
    pipeline: str,
    strategy_payload: dict[str, Any] | None,
) -> tuple[SkillCatalog | None, list[str], str]:
    local_override_paths = _parse_skills_local_paths(settings.skills_local_paths)
    strategy_skillsets = _extract_skillset_ids(strategy_payload or {})
    if not strategy_skillsets:
        return None, [], ""

    resolved_payload: dict[str, Any] = {"skillsets": []}
    if strategy_skillsets:
        resolved_payload = client.resolve_skillsets(pipeline)
        if not isinstance(resolved_payload, dict):
            raise RuntimeError("resolve_skillsets returned invalid payload")
    raw_skillsets = resolved_payload.get("skillsets")
    if raw_skillsets is None:
        raw_skillsets = []
    if not isinstance(raw_skillsets, list):
        raise RuntimeError("resolve_skillsets payload missing skillsets list")

    skill_catalog = SkillCatalog.from_resolved_skillsets(
        skillsets_payload=raw_skillsets,
        cache_dir=settings.skills_cache_dir,
        local_override_paths=local_override_paths,
    )
    if not skill_catalog.has_skills():
        return None, strategy_skillsets, "empty"
    if local_override_paths and strategy_skillsets:
        source = "strategy_resolve+local_override"
    elif local_override_paths:
        source = "local_override"
    else:
        source = "strategy_resolve"
    selected_skillsets = skill_catalog.skillset_ids() or strategy_skillsets
    return skill_catalog, selected_skillsets, source


def _build_prompt_skill_agent(settings: Settings) -> PromptSkillAgent | None:
    if settings.skills_execution_mode != "prompt_first":
        return None
    if not (settings.agent_model and settings.agent_base_url and settings.agent_api_key):
        return None
    return PromptSkillAgent(
        model=settings.agent_model,
        base_url=settings.agent_base_url,
        api_key=settings.agent_api_key,
        timeout_seconds=settings.agent_timeout_seconds,
    )


def _report_skillset_selection_observation(
    *,
    runtime: OrchestratorRuntime,
    job_id: str,
    pipeline: str,
    skill_catalog: SkillCatalog | None,
    skillsets: list[str],
    skillset_source: str,
) -> None:
    if skill_catalog is None:
        return
    report_observation = getattr(runtime, "report_observation", None)
    if not callable(report_observation):
        return
    try:
        report_observation(
            tool="skillset.select",
            node_name="runner.pre_graph",
            params={"pipeline": normalize_pipeline(pipeline)},
            response={
                "status": "ok",
                "skillsets": [str(item).strip() for item in skillsets if str(item).strip()],
                "source": str(skillset_source).strip(),
                "skills": skill_catalog.describe(),
            },
            evidence_ids=[],
        )
    except Exception as exc:  # noqa: BLE001
        _log(f"job={job_id} pre-graph skillset observation failed: {exc}")


def _apply_session_patch_if_needed(runtime: OrchestratorRuntime, state: GraphState) -> GraphState:
    patch = state.session_patch if isinstance(state.session_patch, dict) else {}
    if not patch:
        return state
    request_kwargs = {
        "latest_summary": patch.get("latest_summary") if isinstance(patch.get("latest_summary"), dict) else None,
        "pinned_evidence_append": patch.get("pinned_evidence_append")
        if isinstance(patch.get("pinned_evidence_append"), list)
        else None,
        "pinned_evidence_remove": patch.get("pinned_evidence_remove")
        if isinstance(patch.get("pinned_evidence_remove"), list)
        else None,
        "context_state_patch": patch.get("context_state_patch") if isinstance(patch.get("context_state_patch"), dict) else None,
        "actor": str(patch.get("actor") or "").strip() or None,
        "note": str(patch.get("note") or "").strip() or None,
        "source": str(patch.get("source") or "").strip() or None,
    }
    response = _patch_session_context_with_single_retry(
        runtime=runtime,
        state=state,
        request_kwargs=request_kwargs,
    )
    apply_session_patch_to_state(state, response)
    return state


def _patch_session_context_with_single_retry(
    *,
    runtime: OrchestratorRuntime,
    state: GraphState,
    request_kwargs: dict[str, Any],
) -> dict[str, Any]:
    session_revision = str(state.session_snapshot.get("session_revision") or "").strip() or None
    try:
        return runtime.patch_job_session_context(
            session_revision=session_revision,
            **request_kwargs,
        )
    except RCAApiError as exc:
        if not _is_session_revision_conflict(exc):
            raise
        refreshed_snapshot = runtime.get_job_session_context()
        apply_session_patch_to_state(state, refreshed_snapshot)
        refreshed_revision = str(state.session_snapshot.get("session_revision") or "").strip() or None
        return runtime.patch_job_session_context(
            session_revision=refreshed_revision,
            **request_kwargs,
        )


def _is_session_revision_conflict(exc: RCAApiError) -> bool:
    if not isinstance(exc, RCAApiError):
        return False
    if int(exc.http_status or 0) != 409:
        return False
    envelope_code = str(exc.envelope_code or "").strip().lower()
    return "sessioncontextrevisionconflict" in envelope_code


def _extract_toolset_id(toolset_payload: dict[str, Any]) -> str:
    for key in ("toolsetID", "toolsetId", "toolset_id"):
        value = str(toolset_payload.get(key) or "").strip()
        if value:
            return value
    return ""


def _build_template_registration_payload() -> list[dict[str, str]]:
    payload: list[dict[str, str]] = []
    seen: set[str] = set()
    for raw_template_id in list_template_ids():
        template_id = str(raw_template_id or "").strip()
        if not template_id or template_id in seen:
            continue
        seen.add(template_id)
        payload.append(
            {
                "templateID": template_id,
                "version": "",
            }
        )
    return payload


def _register_templates_if_due(
    *,
    settings: Settings,
    client: RCAApiClient,
    force: bool = False,
) -> None:
    global _TEMPLATE_REGISTRY_LAST_ATTEMPT_TS

    now = time.time()
    with _TEMPLATE_REGISTRY_LOCK:
        if not force and _TEMPLATE_REGISTRY_LAST_ATTEMPT_TS > 0:
            if (now - _TEMPLATE_REGISTRY_LAST_ATTEMPT_TS) < _TEMPLATE_REGISTRY_REFRESH_SECONDS:
                return
        _TEMPLATE_REGISTRY_LAST_ATTEMPT_TS = now

    try:
        templates = _build_template_registration_payload()
        if not templates:
            return
        client.register_templates(settings.instance_id, templates)
        if settings.debug:
            _log(
                "[DEBUG] template registry refreshed "
                f"instance_id={settings.instance_id} template_count={len(templates)}"
            )
    except Exception as exc:  # noqa: BLE001
        _log(f"template registry refresh failed: {exc}")


def _invoke_graph(settings: Settings, graph_cfg: OrchestratorConfig, job_id: str, debug: bool) -> None:
    client = _new_client(settings)
    selected_pipeline = ""
    selected_template_id = ""
    template_builder = None
    tool_invoker = None
    skill_catalog = None
    selected_toolsets: list[str] = []
    selected_toolset_source = ""
    selected_skillsets: list[str] = []
    selected_skillset_source = ""
    selection_error_message = ""
    prefetched_job: dict[str, Any] = {}

    try:
        prefetched_job = client.get_job(job_id)
        selected_pipeline = _extract_pipeline(prefetched_job if isinstance(prefetched_job, dict) else {})
        if _supports_strategy_resolve(client):
            strategy_payload = _resolve_strategy_via_server(client, selected_pipeline)
            selected_template_id = _extract_template_id(strategy_payload)
            if not selected_template_id:
                raise RuntimeError("resolve_strategy payload missing template_id")
            template_builder = get_template_builder(selected_template_id)
            tool_invoker, selected_toolsets, selected_toolset_source = _build_tool_invoker_from_strategy(
                settings,
                client,
                pipeline=selected_pipeline,
                strategy_payload=strategy_payload,
            )
            skill_catalog, selected_skillsets, selected_skillset_source = _build_skill_catalog(
                settings=settings,
                client=client,
                pipeline=selected_pipeline,
                strategy_payload=strategy_payload,
            )
        else:
            selected_template_id = normalize_pipeline(selected_pipeline)
            template_builder = get_template_builder(selected_template_id)
            tool_invoker, selected_toolsets, selected_toolset_source = _select_tool_invoker(
                settings, client, selected_pipeline
            )
            skill_catalog, selected_skillsets, selected_skillset_source = _build_skill_catalog(
                settings=settings,
                client=client,
                pipeline=selected_pipeline,
                strategy_payload=None,
            )
    except UnknownPipelineError as exc:
        _log(
            f"job={job_id} template selection failed: "
            f"pipeline={selected_pipeline or '<empty>'} template_id={selected_template_id or '<empty>'} error={exc}"
        )
        selection_error_message = f"template_selection_failed: {exc}"
    except Exception as exc:  # noqa: BLE001
        if selected_pipeline:
            _log(
                "job="
                f"{job_id} strategy/toolset selection failed: pipeline={normalize_pipeline(selected_pipeline)} "
                f"template_id={selected_template_id or '<empty>'} toolsets={selected_toolsets or ['<empty>']} "
                f"error={exc}"
            )
            if _supports_strategy_resolve(client):
                selection_error_message = f"strategy_selection_failed: {exc}"
            else:
                selection_error_message = f"toolset_selection_failed: {exc}"
        else:
            _log(f"job={job_id} prefetch job for strategy/template selection failed: {exc}")
            selection_error_message = f"template_selection_prefetch_failed: {exc}"

    runtime = OrchestratorRuntime(
        client=client,
        job_id=job_id,
        instance_id=settings.instance_id,
        heartbeat_interval_seconds=settings.lease_heartbeat_interval_seconds,
        log_func=_log,
        verification_max_steps=settings.verification_max_steps,
        verification_max_total_latency_ms=settings.verification_max_total_latency_ms,
        verification_max_total_bytes=settings.verification_max_total_bytes,
        verification_dedupe_enabled=settings.verification_dedupe_enabled,
        tool_invoker=tool_invoker,
        skill_catalog=skill_catalog,
        skills_execution_mode=settings.skills_execution_mode,
        skills_tool_calling_mode=settings.skills_tool_calling_mode,
        skill_agent=_build_prompt_skill_agent(settings),
    )
    if not runtime.start():
        if debug:
            _log(f"[DEBUG] skip job={job_id}: claim failed (already claimed by another instance)")
        return

    if selection_error_message:
        try:
            runtime.finalize(
                status="failed",
                diagnosis_json=None,
                error_message=selection_error_message,
                evidence_ids=[],
            )
        except Exception as finalize_exc:  # noqa: BLE001
            _log(f"job={job_id} finalize after template/toolset selection failure error: {finalize_exc}")
        runtime.shutdown()
        return

    if template_builder is None:
        _log(f"job={job_id} template selection failed: pipeline={selected_pipeline or '<empty>'} error=empty_builder")
        try:
            runtime.finalize(
                status="failed",
                diagnosis_json=None,
                error_message="template_selection_failed: empty_builder",
                evidence_ids=[],
            )
        except Exception as finalize_exc:  # noqa: BLE001
            _log(f"job={job_id} finalize after empty template builder error: {finalize_exc}")
        runtime.shutdown()
        return

    _report_toolset_selection_observation(
        runtime=runtime,
        job_id=job_id,
        pipeline=selected_pipeline,
        template_id=selected_template_id,
        template_builder=template_builder,
        toolsets=selected_toolsets,
        toolset_source=selected_toolset_source,
        tool_invoker=tool_invoker,
    )
    _report_skillset_selection_observation(
        runtime=runtime,
        job_id=job_id,
        pipeline=selected_pipeline,
        skill_catalog=skill_catalog,
        skillsets=selected_skillsets,
        skillset_source=selected_skillset_source,
    )

    if debug:
        selected_toolsets_text = ",".join(selected_toolsets) if selected_toolsets else "<none>"
        selected_skillsets_text = ",".join(selected_skillsets) if selected_skillsets else "<none>"
        _log(
            f"[DEBUG] job={job_id} selected pipeline={selected_pipeline or 'basic_rca'} "
            f"template_id={selected_template_id or '<empty>'} "
            f"template={template_builder.__name__} toolsets={selected_toolsets_text} "
            f"skillsets={selected_skillsets_text}"
        )

    try:
        state = GraphState(job_id=job_id, instance_id=settings.instance_id, started=True)
        if isinstance(prefetched_job, dict):
            state.session_id = (
                str(prefetched_job.get("sessionID") or prefetched_job.get("session_id") or "").strip() or None
            )
        try:
            session_snapshot = runtime.get_job_session_context()
            load_session_snapshot_into_state(state, session_snapshot)
        except Exception as exc:  # noqa: BLE001
            if debug:
                _log(f"[DEBUG] job={job_id} session snapshot load skipped: {exc}")
        compiled_graph = template_builder(runtime, graph_cfg)
        final_state = compiled_graph.invoke(state)
    finally:
        runtime.shutdown()

    if isinstance(final_state, dict):
        final_state = GraphState.model_validate(final_state)
    try:
        final_state = _apply_session_patch_if_needed(runtime, final_state)
    except Exception as exc:  # noqa: BLE001
        _log(f"job={job_id} session patch apply failed: {exc}")

    if debug:
        _log(
            "[DEBUG] "
            f"job={job_id} finalized={final_state.finalized} "
            f"started={final_state.started} evidence_ids={final_state.evidence_ids} "
            f"last_error={final_state.last_error!r}"
        )


def main() -> None:
    settings = load_settings()
    pubsub_enabled, subscribe_ready = detect_pubsub_ready(settings.base_url, settings.scopes)
    redis_enabled = pubsub_enabled
    _log(
        "orchestrator redis profile "
        f"enabled={int(redis_enabled)} "
        f"pubsub={int(pubsub_enabled)} "
        f"subscribe_ready={int(subscribe_ready)} "
        "fallback=db_watermark_longpoll"
    )
    _log(
        "orchestrator starting "
        f"base_url={settings.base_url} "
        f"instance_id={settings.instance_id} "
        f"mcp_scopes_set={int(bool(settings.mcp_scopes))} "
        f"mcp_verify_remote_tools={int(settings.mcp_verify_remote_tools)} "
        f"poll_interval_ms={settings.poll_interval_ms} "
        f"lease_heartbeat_interval_seconds={settings.lease_heartbeat_interval_seconds} "
        f"concurrency={settings.concurrency} "
        f"run_query={int(settings.run_query)} "
        f"force_no_evidence={int(settings.force_no_evidence)} "
        f"force_conflict={int(settings.force_conflict)} "
        f"ds_type={settings.ds_type} "
        f"long_poll_wait_seconds={settings.long_poll_wait_seconds} "
        f"a3_max_calls={settings.a3_max_calls} "
        f"a3_max_total_bytes={settings.a3_max_total_bytes} "
        f"a3_max_total_latency_ms={settings.a3_max_total_latency_ms} "
        f"post_finalize_observe={int(settings.post_finalize_observe)} "
        f"run_verification={int(settings.run_verification)} "
        f"verification_source={settings.verification_source} "
        f"verification_max_steps={settings.verification_max_steps} "
        f"verification_max_total_latency_ms={settings.verification_max_total_latency_ms} "
        f"verification_max_total_bytes={settings.verification_max_total_bytes} "
        f"verification_dedupe_enabled={int(settings.verification_dedupe_enabled)} "
        f"post_finalize_wait_timeout_seconds={settings.post_finalize_wait_timeout_seconds} "
        f"post_finalize_wait_interval_ms={settings.post_finalize_wait_interval_ms} "
        f"post_finalize_wait_max_interval_ms={settings.post_finalize_wait_max_interval_ms} "
        f"skills_execution_mode={settings.skills_execution_mode} "
        f"skills_tool_calling_mode={settings.skills_tool_calling_mode} "
        f"skills_cache_dir={settings.skills_cache_dir} "
        f"skills_local_paths_set={int(bool(settings.skills_local_paths))}"
        f" agent_model_set={int(bool(settings.agent_model))} "
        f"agent_base_url_set={int(bool(settings.agent_base_url))}"
    )

    poll_client = _new_client(settings)
    _register_templates_if_due(settings=settings, client=poll_client, force=True)
    graph_cfg = OrchestratorConfig(
        run_query=settings.run_query,
        force_no_evidence=settings.force_no_evidence,
        force_conflict=settings.force_conflict,
        ds_base_url=settings.ds_base_url,
        ds_type=settings.ds_type,
        auto_create_datasource=settings.auto_create_datasource,
        a3_max_calls=settings.a3_max_calls,
        a3_max_total_bytes=settings.a3_max_total_bytes,
        a3_max_total_latency_ms=settings.a3_max_total_latency_ms,
        post_finalize_observe=settings.post_finalize_observe,
        run_verification=settings.run_verification,
        verification_source=settings.verification_source,
        post_finalize_wait_timeout_seconds=settings.post_finalize_wait_timeout_seconds,
        post_finalize_wait_interval_ms=settings.post_finalize_wait_interval_ms,
        post_finalize_wait_max_interval_ms=settings.post_finalize_wait_max_interval_ms,
    )

    sleep_s = settings.poll_interval_ms / 1000.0
    wait_seconds = 0
    while True:
        try:
            _register_templates_if_due(settings=settings, client=poll_client, force=False)
            listed = poll_client.list_jobs(
                status="queued",
                limit=settings.pull_limit,
                offset=0,
                wait_seconds=wait_seconds,
            )
            jobs = _extract_jobs(listed)
            if settings.debug:
                _log(f"[DEBUG] pulled queued jobs: wait_seconds={wait_seconds} count={len(jobs)}")

            if not jobs:
                wait_seconds = settings.long_poll_wait_seconds
                if wait_seconds <= 0:
                    time.sleep(sleep_s)
                continue

            wait_seconds = 0
            if settings.concurrency <= 1:
                for job in jobs:
                    job_id = _extract_job_id(job)
                    if not job_id:
                        continue
                    if settings.debug:
                        _log(f"[DEBUG] invoking graph for job={job_id}")
                    _invoke_graph(settings, graph_cfg, job_id, settings.debug)
            else:
                max_workers = min(settings.concurrency, len(jobs))
                with ThreadPoolExecutor(max_workers=max_workers) as pool:
                    futures = []
                    for job in jobs:
                        job_id = _extract_job_id(job)
                        if not job_id:
                            continue
                        futures.append(pool.submit(_invoke_graph, settings, graph_cfg, job_id, settings.debug))
                    for future in futures:
                        future.result()
        except Exception as exc:  # noqa: BLE001
            _log(f"poll loop error: {exc}")
            time.sleep(sleep_s)
