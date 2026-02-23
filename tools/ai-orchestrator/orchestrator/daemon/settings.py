from __future__ import annotations

from dataclasses import dataclass
import os
import socket


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
    verification_max_steps: int
    verification_max_total_latency_ms: int
    verification_max_total_bytes: int
    verification_dedupe_enabled: bool
    post_finalize_wait_timeout_seconds: int
    post_finalize_wait_interval_ms: int
    post_finalize_wait_max_interval_ms: int
    toolset_config_path: str
    toolset_config_json: str
    ds_type: str = "prometheus"
    skills_execution_mode: str = "catalog"
    skills_tool_calling_mode: str = "disabled"
    skills_cache_dir: str = "/tmp/rca-ai-orchestrator/skills-cache"
    skills_local_paths: str = ""
    agent_model: str = ""
    agent_base_url: str = ""
    agent_api_key: str = ""
    agent_timeout_seconds: float = 20.0


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


def _env_float(name: str, default: float) -> float:
    raw = os.getenv(name, "").strip()
    if not raw:
        return default
    try:
        return float(raw)
    except ValueError:
        return default


def load_settings() -> Settings:
    long_poll_wait_seconds = _env_int("LONG_POLL_WAIT_SECONDS", 20)
    long_poll_wait_seconds = max(0, min(30, long_poll_wait_seconds))
    default_instance_id = f"{socket.gethostname()}-{os.getpid()}"
    lease_heartbeat_interval_seconds = _env_int("LEASE_HEARTBEAT_INTERVAL_SECONDS", 10)
    lease_heartbeat_interval_seconds = max(1, lease_heartbeat_interval_seconds)
    a3_max_calls = max(0, _env_int("A3_MAX_CALLS", 6))
    a3_max_total_bytes = max(0, _env_int("A3_MAX_TOTAL_BYTES", 2 * 1024 * 1024))
    a3_max_total_latency_ms = max(0, _env_int("A3_MAX_TOTAL_LATENCY_MS", 8000))
    post_finalize_wait_timeout_seconds = max(0, _env_int("POST_FINALIZE_WAIT_TIMEOUT_SECONDS", 8))
    post_finalize_wait_interval_ms = max(50, _env_int("POST_FINALIZE_WAIT_INTERVAL_MS", 500))
    post_finalize_wait_max_interval_ms = max(
        post_finalize_wait_interval_ms,
        _env_int("POST_FINALIZE_WAIT_MAX_INTERVAL_MS", 2000),
    )
    skills_execution_mode = os.getenv("SKILLS_EXECUTION_MODE", "catalog").strip().lower() or "catalog"
    if skills_execution_mode not in {"catalog", "prompt_first"}:
        skills_execution_mode = "catalog"
    skills_tool_calling_mode = os.getenv("SKILLS_TOOL_CALLING_MODE", "disabled").strip().lower() or "disabled"
    if skills_tool_calling_mode not in {"disabled", "evidence_plan_single_hop", "evidence_plan_dual_tool"}:
        skills_tool_calling_mode = "disabled"
    ds_type = os.getenv("DS_TYPE", "prometheus").strip().lower() or "prometheus"
    if ds_type not in {"prometheus", "loki", "elasticsearch"}:
        ds_type = "prometheus"
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
        ds_type=ds_type,
        auto_create_datasource=_env_bool("AUTO_CREATE_DATASOURCE", True),
        debug=_env_bool("DEBUG", False),
        pull_limit=max(1, min(50, _env_int("PULL_LIMIT", 10))),
        long_poll_wait_seconds=long_poll_wait_seconds,
        a3_max_calls=a3_max_calls,
        a3_max_total_bytes=a3_max_total_bytes,
        a3_max_total_latency_ms=a3_max_total_latency_ms,
        run_verification=_env_bool("RUN_VERIFICATION", False),
        post_finalize_observe=_env_bool("POST_FINALIZE_OBSERVE", True),
        verification_source=os.getenv("VERIFICATION_SOURCE", "ai_job").strip() or "ai_job",
        verification_max_steps=max(0, _env_int("VERIFICATION_MAX_STEPS", 20)),
        verification_max_total_latency_ms=max(
            0,
            _env_int("VERIFICATION_MAX_TOTAL_LATENCY_MS", a3_max_total_latency_ms),
        ),
        verification_max_total_bytes=max(
            0,
            _env_int("VERIFICATION_MAX_TOTAL_BYTES", a3_max_total_bytes),
        ),
        verification_dedupe_enabled=_env_bool("VERIFICATION_DEDUPE_ENABLED", True),
        post_finalize_wait_timeout_seconds=post_finalize_wait_timeout_seconds,
        post_finalize_wait_interval_ms=post_finalize_wait_interval_ms,
        post_finalize_wait_max_interval_ms=post_finalize_wait_max_interval_ms,
        toolset_config_path=os.getenv("TOOLSET_CONFIG_PATH", "").strip(),
        toolset_config_json=os.getenv("TOOLSET_CONFIG_JSON", "").strip(),
        skills_execution_mode=skills_execution_mode,
        skills_tool_calling_mode=skills_tool_calling_mode,
        skills_cache_dir=os.getenv("SKILLS_CACHE_DIR", "/tmp/rca-ai-orchestrator/skills-cache").strip(),
        skills_local_paths=os.getenv("SKILLS_LOCAL_PATHS", "").strip(),
        agent_model=os.getenv("AGENT_MODEL", "").strip(),
        agent_base_url=os.getenv("AGENT_BASE_URL", "").strip(),
        agent_api_key=os.getenv("AGENT_API_KEY", "").strip(),
        agent_timeout_seconds=max(1.0, _env_float("AGENT_TIMEOUT_SECONDS", 20.0)),
    )
