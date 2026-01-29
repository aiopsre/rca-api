from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor
import threading
import time
from typing import Any

from ..graph import OrchestratorConfig
from ..langgraph.registry import UnknownPipelineError, get_template_builder, normalize_pipeline
from ..runtime.runtime import OrchestratorRuntime
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


def _select_tool_invoker_via_server(client: RCAApiClient, pipeline: str) -> tuple[ToolInvoker, list[str], str]:
    resolved = client.resolve_toolset(pipeline)
    if not isinstance(resolved, dict):
        raise RuntimeError("resolve_toolset returned invalid payload")

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

    normalized_pipeline = normalize_pipeline(pipeline)
    config = load_toolset_config(
        {
            "pipelines": {normalized_pipeline: toolset_id},
            "toolsets": {
                toolset_id: {
                    "providers": providers,
                }
            },
        }
    )
    invoker = build_tool_invoker(config, toolset_id)
    return invoker, [toolset_id], "server_resolve"


def _report_toolset_selection_observation(
    *,
    runtime: OrchestratorRuntime,
    job_id: str,
    pipeline: str,
    template_builder: Any,
    toolsets: list[str],
    toolset_source: str,
    tool_invoker: ToolInvoker | ToolInvokerChain | None,
) -> None:
    report_observation = getattr(runtime, "report_observation", None)
    if not callable(report_observation):
        return

    providers: list[dict[str, Any]] = []
    if tool_invoker is not None:
        provider_summaries = getattr(tool_invoker, "provider_summaries", None)
        if callable(provider_summaries):
            try:
                raw = provider_summaries()
                if isinstance(raw, list):
                    providers = [item for item in raw if isinstance(item, dict)]
            except Exception:  # noqa: BLE001
                providers = []

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
                "toolsets": [str(item).strip() for item in toolsets if str(item).strip()],
                "source": str(toolset_source).strip(),
                "providers": providers,
            },
            evidence_ids=[],
        )
    except Exception as exc:  # noqa: BLE001
        _log(f"job={job_id} pre-graph toolset observation failed: {exc}")


def _extract_toolset_id(toolset_payload: dict[str, Any]) -> str:
    for key in ("toolsetID", "toolsetId", "toolset_id"):
        value = str(toolset_payload.get(key) or "").strip()
        if value:
            return value
    return ""


def _invoke_graph(settings: Settings, graph_cfg: OrchestratorConfig, job_id: str, debug: bool) -> None:
    client = _new_client(settings)
    selected_pipeline = ""
    template_builder = None
    tool_invoker = None
    selected_toolsets: list[str] = []
    selected_toolset_source = ""
    selection_error_message = ""

    try:
        prefetched_job = client.get_job(job_id)
        selected_pipeline = _extract_pipeline(prefetched_job if isinstance(prefetched_job, dict) else {})
        template_builder = get_template_builder(selected_pipeline)
    except UnknownPipelineError as exc:
        _log(
            f"job={job_id} template selection failed: "
            f"pipeline={selected_pipeline or '<empty>'} error={exc}"
        )
        selection_error_message = str(exc)
    except Exception as exc:  # noqa: BLE001
        _log(f"job={job_id} prefetch job for template selection failed: {exc}")
        selection_error_message = f"template_selection_prefetch_failed: {exc}"

    if not selection_error_message:
        try:
            tool_invoker, selected_toolsets, selected_toolset_source = _select_tool_invoker(
                settings, client, selected_pipeline
            )
        except Exception as exc:  # noqa: BLE001
            _log(
                "job="
                f"{job_id} toolset selection failed: pipeline={normalize_pipeline(selected_pipeline)} "
                f"toolsets={selected_toolsets or ['<empty>']} error={exc}"
            )
            selection_error_message = f"toolset_selection_failed: {exc}"

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
        template_builder=template_builder,
        toolsets=selected_toolsets,
        toolset_source=selected_toolset_source,
        tool_invoker=tool_invoker,
    )

    if debug:
        selected_toolsets_text = ",".join(selected_toolsets) if selected_toolsets else "<none>"
        _log(
            f"[DEBUG] job={job_id} selected pipeline={selected_pipeline or 'basic_rca'} "
            f"template={template_builder.__name__} toolsets={selected_toolsets_text}"
        )

    try:
        state = GraphState(job_id=job_id, instance_id=settings.instance_id, started=True)
        compiled_graph = template_builder(runtime, graph_cfg)
        final_state = compiled_graph.invoke(state)
    finally:
        runtime.shutdown()

    if isinstance(final_state, dict):
        final_state = GraphState.model_validate(final_state)

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
        f"post_finalize_wait_max_interval_ms={settings.post_finalize_wait_max_interval_ms}"
    )

    poll_client = _new_client(settings)
    graph_cfg = OrchestratorConfig(
        run_query=settings.run_query,
        force_no_evidence=settings.force_no_evidence,
        force_conflict=settings.force_conflict,
        ds_base_url=settings.ds_base_url,
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
