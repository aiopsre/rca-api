from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
import os
import time
from typing import Any, Dict, List

from .graph import OrchestratorConfig, build_graph
from .state import GraphState
from .tools_rca_api import RCAApiClient


@dataclass
class Settings:
    base_url: str
    scopes: str
    poll_interval_ms: int
    concurrency: int
    run_query: bool
    ds_base_url: str
    auto_create_datasource: bool
    debug: bool
    pull_limit: int


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
    return Settings(
        base_url=os.getenv("BASE_URL", "http://127.0.0.1:5555").strip() or "http://127.0.0.1:5555",
        scopes=os.getenv("SCOPES", "").strip(),
        poll_interval_ms=max(100, _env_int("POLL_INTERVAL_MS", 1000)),
        concurrency=max(1, _env_int("CONCURRENCY", 1)),
        run_query=_env_bool("RUN_QUERY", False),
        ds_base_url=os.getenv("DS_BASE_URL", "").strip(),
        auto_create_datasource=_env_bool("AUTO_CREATE_DATASOURCE", True),
        debug=_env_bool("DEBUG", False),
        pull_limit=max(1, min(50, _env_int("PULL_LIMIT", 10))),
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


def _invoke_graph(compiled_graph, job_id: str, debug: bool) -> None:
    state = GraphState(job_id=job_id)
    final_state = compiled_graph.invoke(state)
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
    _log(
        "orchestrator starting "
        f"base_url={settings.base_url} "
        f"poll_interval_ms={settings.poll_interval_ms} "
        f"concurrency={settings.concurrency} "
        f"run_query={int(settings.run_query)}"
    )

    client = RCAApiClient(settings.base_url, settings.scopes, timeout_s=10.0)
    graph_cfg = OrchestratorConfig(
        run_query=settings.run_query,
        ds_base_url=settings.ds_base_url,
        auto_create_datasource=settings.auto_create_datasource,
    )
    compiled_graph = build_graph(client, graph_cfg)

    sleep_s = settings.poll_interval_ms / 1000.0
    while True:
        try:
            listed = client.list_jobs(status="queued", limit=settings.pull_limit, offset=0)
            jobs = _extract_jobs(listed)
            if settings.debug:
                _log(f"[DEBUG] pulled queued jobs: count={len(jobs)}")

            if not jobs:
                time.sleep(sleep_s)
                continue

            if settings.concurrency <= 1:
                for job in jobs:
                    job_id = _extract_job_id(job)
                    if not job_id:
                        continue
                    if settings.debug:
                        _log(f"[DEBUG] invoking graph for job={job_id}")
                    _invoke_graph(compiled_graph, job_id, settings.debug)
            else:
                max_workers = min(settings.concurrency, len(jobs))
                with ThreadPoolExecutor(max_workers=max_workers) as pool:
                    futures = []
                    for job in jobs:
                        job_id = _extract_job_id(job)
                        if not job_id:
                            continue
                        futures.append(pool.submit(_invoke_graph, compiled_graph, job_id, settings.debug))
                    for future in futures:
                        future.result()
        except Exception as exc:  # noqa: BLE001
            _log(f"poll loop error: {exc}")
        finally:
            time.sleep(sleep_s)


if __name__ == "__main__":
    main()
