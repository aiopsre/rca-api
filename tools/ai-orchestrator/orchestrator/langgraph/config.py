from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any


@dataclass
class OrchestratorConfig:
    run_query: bool = False
    force_no_evidence: bool = False
    force_conflict: bool = False
    ds_base_url: str = ""
    ds_type: str = "prometheus"
    metrics_ds_type: str = "prometheus"
    logs_ds_type: str = "prometheus"
    auto_create_datasource: bool = True
    a3_max_calls: int = 6
    a3_max_total_bytes: int = 2 * 1024 * 1024
    a3_max_total_latency_ms: int = 8000
    post_finalize_observe: bool = True
    run_verification: bool = False
    verification_source: str = "ai_job"
    post_finalize_wait_timeout_seconds: int = 8
    post_finalize_wait_interval_ms: int = 500
    post_finalize_wait_max_interval_ms: int = 2000
    tool_execution_max_workers: int = 5
    tool_execution_group_timeout_s: float = 30.0

    # Tool agent budget controls (Phase FC2B)
    tool_agent_max_rounds: int = 5  # Maximum iteration rounds
    tool_agent_max_calls_per_round: int = 3  # Maximum calls per round
    tool_agent_round_timeout_s: float = 60.0  # Timeout per round
    tool_agent_stop_on_error: bool = False  # Stop on error

    # Hybrid Multi-Agent middleware (Phase HM2)
    # middleware_chain is set at job processing time, not at config creation time.
    # It's built per-job using job-specific context (session, skills, tools).
    middleware_chain: Any = None  # MiddlewareChain | None
    middleware_enabled: bool = True  # Controlled by RCA_HYBRID_MIDDLEWARE_ENABLED
