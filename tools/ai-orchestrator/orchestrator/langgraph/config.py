from __future__ import annotations

from dataclasses import dataclass


@dataclass
class OrchestratorConfig:
    run_query: bool = False
    force_no_evidence: bool = False
    force_conflict: bool = False
    ds_base_url: str = ""
    ds_type: str = "prometheus"
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
