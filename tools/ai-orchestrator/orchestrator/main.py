from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
import os
import socket
import time
from typing import Any, Dict, List

import requests

from .graph import OrchestratorConfig, build_graph
from .runtime.runtime import OrchestratorRuntime
from .state import GraphState
from .tools_rca_api import RCAApiClient


@dataclass
class Settings:
    base_url: str
    scopes: str
    mcp_scopes: str
    mcp_verify_remote_tools: bool
    instance_id: str
    poll_interval_ms: int
    lease_heartbeat_interval_seconds: int
    concurrency: int
    run_query: bool
    force_no_evidence: bool
    force_conflict: bool
    ds_base_url: str
    auto_create_datasource: bool
    debug: bool
    pull_limit: int
    long_poll_wait_seconds: int
    a3_max_calls: int
    a3_max_total_bytes: int
    a3_max_total_latency_ms: int
    run_verification: bool
    post_finalize_observe: bool
    verification_source: str


def _env_int(name: str, default: int) -> int:
    raw = os.getenv(name, "").strip()
    if not raw:
        return default
    try:
        return int(raw)
    except ValueError:
        return default


def _env_bool(name: str, default: bool) -> bool:
    raw = os.getenv(name, "").strip().lower()
    if raw == "":
        return default
    return raw in {"1", "true", "yes", "y", "on"}


def load_settings() -> Settings:
    long_poll_wait_seconds = _env_int("LONG_POLL_WAIT_SECONDS", 20)
    long_poll_wait_seconds = max(0, min(30, long_poll_wait_seconds))
    default_instance_id = f"{socket.gethostname()}-{os.getpid()}"
    lease_heartbeat_interval_seconds = _env_int("LEASE_HEARTBEAT_INTERVAL_SECONDS", 10)
    lease_heartbeat_interval_seconds = max(1, lease_heartbeat_interval_seconds)
    return Settings(
        base_url=os.getenv("BASE_URL", "http://127.0.0.1:5555").strip() or "http://127.0.0.1:5555",
        scopes=os.getenv("SCOPES", "").strip(),
        mcp_scopes=os.getenv("RCA_API_SCOPES", "").strip(),
        mcp_verify_remote_tools=_env_bool("MCP_VERIFY_REMOTE_TOOLS", False),
        instance_id=os.getenv("INSTANCE_ID", default_instance_id).strip() or default_instance_id,
        poll_interval_ms=max(100, _env_int("POLL_INTERVAL_MS", 1000)),
        lease_heartbeat_interval_seconds=lease_heartbeat_interval_seconds,
        concurrency=max(1, _env_int("CONCURRENCY", 1)),
        run_query=_env_bool("RUN_QUERY", False),
        force_no_evidence=_env_bool("FORCE_NO_EVIDENCE", False),
        force_conflict=_env_bool("FORCE_CONFLICT", False),
        ds_base_url=os.getenv("DS_BASE_URL", "").strip(),
        auto_create_datasource=_env_bool("AUTO_CREATE_DATASOURCE", True),
        debug=_env_bool("DEBUG", False),
        pull_limit=max(1, min(50, _env_int("PULL_LIMIT", 10))),
        long_poll_wait_seconds=long_poll_wait_seconds,
        a3_max_calls=max(0, _env_int("A3_MAX_CALLS", 6)),
        a3_max_total_bytes=max(0, _env_int("A3_MAX_TOTAL_BYTES", 2 * 1024 * 1024)),
        a3_max_total_latency_ms=max(0, _env_int("A3_MAX_TOTAL_LATENCY_MS", 8000)),
        run_verification=_env_bool("RUN_VERIFICATION", False),
        post_finalize_observe=_env_bool("POST_FINALIZE_OBSERVE", True),
        verification_source=os.getenv("VERIFICATION_SOURCE", "ai_job").strip() or "ai_job",
    )


def _log(msg: str) -> None:
    print(msg, flush=True)


def _extract_jobs(payload: Dict[str, Any]) -> List[Dict[str, Any]]:
    jobs = payload.get("jobs")
    if isinstance(jobs, list):
        return [j for j in jobs if isinstance(j, dict)]
    return []


def _extract_job_id(job_obj: Dict[str, Any]) -> str:
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


def _parse_prometheus_metric_value(line: str) -> float:
    if not line:
        return 0.0
    parts = line.strip().split()
    if not parts:
        return 0.0
    try:
        return float(parts[-1])
    except ValueError:
        return 0.0


def _detect_pubsub_ready(base_url: str, scopes: str, timeout_s: float = 2.0) -> tuple[bool, bool]:
    url = f"{base_url.rstrip('/')}/metrics"
    headers = {"Accept": "text/plain"}
    if scopes:
        headers["X-Scopes"] = scopes
    try:
        response = requests.get(url, headers=headers, timeout=max(timeout_s, 0.5))
    except Exception:  # noqa: BLE001
        return False, False
    if not response.ok:
        return False, False

    found = False
    ready = False
    for raw in response.text.splitlines():
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        if line.startswith("redis_pubsub_subscribe_ready"):
            found = True
            if _parse_prometheus_metric_value(line) > 0:
                ready = True
            continue
        if not found and line.startswith("redis_pubsub_subscribe_state"):
            found = True
            if _parse_prometheus_metric_value(line) > 0:
                ready = True
    return found, ready


def _is_finalize_succeeded(state: GraphState) -> bool:
    if not state.finalized:
        return False
    return not bool(str(state.last_error or "").strip())


def _invoke_graph(settings: Settings, graph_cfg: OrchestratorConfig, job_id: str, debug: bool) -> None:
    client = _new_client(settings)
    runtime = OrchestratorRuntime(
        client=client,
        job_id=job_id,
        instance_id=settings.instance_id,
        heartbeat_interval_seconds=settings.lease_heartbeat_interval_seconds,
        log_func=_log,
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

    should_post_finalize = (
        _is_finalize_succeeded(final_state)
        and bool(str(final_state.incident_id or "").strip())
        and (settings.post_finalize_observe or settings.run_verification)
    )
    snapshot = None
    if should_post_finalize:
        incident_id = str(final_state.incident_id or "").strip()
        try:
            snapshot = runtime.observe_post_finalize(incident_id=incident_id)
        except Exception as exc:  # noqa: BLE001
            _log(f"post-finalize observe error: job={job_id} incident={incident_id} error={exc}")

    if settings.run_verification and _is_finalize_succeeded(final_state):
        incident_id = str(final_state.incident_id or "").strip()
        if not incident_id:
            _log(f"verification skipped: job={job_id} incident_id missing")
        elif snapshot is None:
            _log(f"verification skipped: job={job_id} post-finalize snapshot unavailable")
        else:
            verification_plan = snapshot.verification_plan
            steps = verification_plan.get("steps") if isinstance(verification_plan, dict) else None
            if not isinstance(steps, list) or not steps:
                if debug:
                    _log(f"[DEBUG] verification skipped: job={job_id} no verification_plan steps")
            else:
                try:
                    results = runtime.run_verification(
                        incident_id=incident_id,
                        verification_plan=verification_plan,
                        source=settings.verification_source,
                    )
                    _log(
                        "verification completed "
                        f"job={job_id} incident={incident_id} steps={len(results)} "
                        f"source={settings.verification_source}"
                    )
                except Exception as exc:  # noqa: BLE001
                    _log(f"verification run error: job={job_id} incident={incident_id} error={exc}")

    if debug:
        _log(
            "[DEBUG] "
            f"job={job_id} finalized={final_state.finalized} "
            f"started={final_state.started} evidence_ids={final_state.evidence_ids} "
            f"last_error={final_state.last_error!r}"
        )


def main() -> None:
    settings = load_settings()
    pubsub_enabled, subscribe_ready = _detect_pubsub_ready(settings.base_url, settings.scopes)
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
        f"verification_source={settings.verification_source}"
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


if __name__ == "__main__":
    main()
