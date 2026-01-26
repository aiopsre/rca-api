from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor
import time
from typing import Any

from ..graph import OrchestratorConfig, build_graph
from ..runtime.runtime import OrchestratorRuntime
from ..state import GraphState
from ..tools_rca_api import RCAApiClient
from .health import detect_pubsub_ready
from .settings import Settings, load_settings


def _log(msg: str) -> None:
    print(msg, flush=True)


def _extract_jobs(payload: dict[str, Any]) -> list[dict[str, Any]]:
    jobs = payload.get("jobs")
    if isinstance(jobs, list):
        return [j for j in jobs if isinstance(j, dict)]
    return []


def _extract_job_id(job_obj: dict[str, Any]) -> str:
    return str(job_obj.get("jobID") or job_obj.get("job_id") or "").strip()


def _new_client(settings: Settings) -> RCAApiClient:
    return RCAApiClient(
        settings.base_url,
        settings.scopes,
        instance_id=settings.instance_id,
        timeout_s=10.0,
        mcp_scopes=settings.mcp_scopes,
        mcp_verify_remote_tools=settings.mcp_verify_remote_tools,
    )


def _invoke_graph(settings: Settings, graph_cfg: OrchestratorConfig, job_id: str, debug: bool) -> None:
    client = _new_client(settings)
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
    )
    if not runtime.start():
        if debug:
            _log(f"[DEBUG] skip job={job_id}: claim failed (already claimed by another instance)")
        return

    try:
        state = GraphState(job_id=job_id, instance_id=settings.instance_id, started=True)
        compiled_graph = build_graph(client, graph_cfg, runtime)
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
