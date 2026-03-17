from __future__ import annotations

from dataclasses import dataclass
import os
import socket
from typing import Any


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
    metrics_ds_type: str = "prometheus"
    logs_ds_type: str = "prometheus"
    skills_execution_mode: str = "catalog"
    skills_tool_calling_mode: str = "disabled"
    skills_cache_dir: str = "/tmp/rca-ai-orchestrator/skills-cache"
    skills_local_paths: str = ""
    agent_model: str = ""
    agent_base_url: str = ""
    agent_api_key: str = ""
    agent_timeout_seconds: float = 20.0
    health_port: int = 8080
    health_host: str = "0.0.0.0"
    tool_execution_max_workers: int = 5
    tool_execution_group_timeout_s: float = 30.0
    # Function Calling migration feature flags
    # Phase 4: FC path is now the default (2026-03-18)
    fc_runtime_snapshot_enabled: bool = True
    fc_skill_tool_calling_enabled: bool = True
    fc_compat_json_toolcalls_enabled: bool = False
    fc_compat_dynamic_tool_nodes_enabled: bool = False

    # Hybrid Multi-Agent feature flags (rollback-only)
    # Phase 1: Unified Agent Context
    rca_agent_context_enabled: bool = True
    # Phase 2: Surface & Middleware
    rca_hybrid_middleware_enabled: bool = True
    # Phase 4: Domain Agents
    rca_domain_agent_change_enabled: bool = True
    rca_domain_agent_knowledge_enabled: bool = True
    # Phase 5: Platform Special Agent
    rca_platform_special_agent_enabled: bool = True

    def safe_summary(self) -> dict[str, Any]:
        """Return a safe summary of settings for logging (excludes sensitive values)."""
        return {
            "base_url": self.base_url,
            "instance_id": self.instance_id,
            "concurrency": self.concurrency,
            "poll_interval_ms": self.poll_interval_ms,
            "lease_heartbeat_interval_seconds": self.lease_heartbeat_interval_seconds,
            "long_poll_wait_seconds": self.long_poll_wait_seconds,
            "pull_limit": self.pull_limit,
            "debug": self.debug,
            "ds_type": self.ds_type,
            "metrics_ds_type": self.metrics_ds_type,
            "logs_ds_type": self.logs_ds_type,
            "skills_execution_mode": self.skills_execution_mode,
            "skills_tool_calling_mode": self.skills_tool_calling_mode,
            "run_query": self.run_query,
            "run_verification": self.run_verification,
            "mcp_scopes_set": bool(self.mcp_scopes),
            "toolset_config_path_set": bool(self.toolset_config_path),
            "toolset_config_json_set": bool(self.toolset_config_json),
            "skills_local_paths_set": bool(self.skills_local_paths),
            "agent_model_set": bool(self.agent_model),
            "agent_base_url_set": bool(self.agent_base_url),
            "health_port": self.health_port,
            "health_host": self.health_host,
            "tool_execution_max_workers": self.tool_execution_max_workers,
            "tool_execution_group_timeout_s": self.tool_execution_group_timeout_s,
            # Function Calling feature flags
            "fc_runtime_snapshot_enabled": self.fc_runtime_snapshot_enabled,
            "fc_skill_tool_calling_enabled": self.fc_skill_tool_calling_enabled,
            "fc_compat_json_toolcalls_enabled": self.fc_compat_json_toolcalls_enabled,
            "fc_compat_dynamic_tool_nodes_enabled": self.fc_compat_dynamic_tool_nodes_enabled,
            # Hybrid Multi-Agent feature flags
            "rca_agent_context_enabled": self.rca_agent_context_enabled,
            "rca_hybrid_middleware_enabled": self.rca_hybrid_middleware_enabled,
            "rca_domain_agent_change_enabled": self.rca_domain_agent_change_enabled,
            "rca_domain_agent_knowledge_enabled": self.rca_domain_agent_knowledge_enabled,
            "rca_platform_special_agent_enabled": self.rca_platform_special_agent_enabled,
        }


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
    skills_execution_mode = os.getenv("SKILLS_EXECUTION_MODE", "prompt_first").strip().lower() or "prompt_first"
    if skills_execution_mode not in {"catalog", "prompt_first"}:
        skills_execution_mode = "prompt_first"
    skills_tool_calling_mode = os.getenv("SKILLS_TOOL_CALLING_MODE", "disabled").strip().lower() or "disabled"
    if skills_tool_calling_mode not in {"disabled", "evidence_plan_single_hop", "evidence_plan_dual_tool"}:
        skills_tool_calling_mode = "disabled"
    ds_type = os.getenv("DS_TYPE", "prometheus").strip().lower() or "prometheus"
    if ds_type not in {"prometheus", "loki", "elasticsearch"}:
        ds_type = "prometheus"
    metrics_ds_type = os.getenv("METRICS_DS_TYPE", ds_type).strip().lower() or ds_type
    if metrics_ds_type not in {"prometheus", "loki", "elasticsearch"}:
        metrics_ds_type = ds_type
    logs_ds_type = os.getenv("LOGS_DS_TYPE", ds_type).strip().lower() or ds_type
    if logs_ds_type not in {"prometheus", "loki", "elasticsearch"}:
        logs_ds_type = ds_type
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
        metrics_ds_type=metrics_ds_type,
        logs_ds_type=logs_ds_type,
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
        health_port=max(0, _env_int("HEALTH_PORT", 8080)),
        health_host=os.getenv("HEALTH_HOST", "0.0.0.0").strip() or "0.0.0.0",
        tool_execution_max_workers=max(1, min(20, _env_int("TOOL_EXECUTION_MAX_WORKERS", 5))),
        tool_execution_group_timeout_s=max(1.0, _env_float("TOOL_EXECUTION_GROUP_TIMEOUT_S", 30.0)),
        # Function Calling migration feature flags
        # Phase 4: FC path is now the default (2026-03-18)
        fc_runtime_snapshot_enabled=_env_bool("RCA_FC_RUNTIME_SNAPSHOT_ENABLED", True),
        fc_skill_tool_calling_enabled=_env_bool("RCA_FC_SKILL_TOOL_CALLING_ENABLED", True),
        fc_compat_json_toolcalls_enabled=_env_bool("RCA_FC_COMPAT_JSON_TOOLCALLS_ENABLED", False),
        fc_compat_dynamic_tool_nodes_enabled=_env_bool("RCA_FC_COMPAT_DYNAMIC_TOOL_NODES_ENABLED", False),
        # Hybrid Multi-Agent feature flags (rollback-only)
        rca_agent_context_enabled=_env_bool("RCA_AGENT_CONTEXT_ENABLED", True),
        rca_hybrid_middleware_enabled=_env_bool("RCA_HYBRID_MIDDLEWARE_ENABLED", True),
        rca_domain_agent_change_enabled=_env_bool("RCA_DOMAIN_AGENT_CHANGE_ENABLED", True),
        rca_domain_agent_knowledge_enabled=_env_bool("RCA_DOMAIN_AGENT_KNOWLEDGE_ENABLED", True),
        rca_platform_special_agent_enabled=_env_bool("RCA_PLATFORM_SPECIAL_AGENT_ENABLED", True),
    )


def validate_settings(settings: Settings) -> list[str]:
    """Validate settings and return a list of error messages.

    Returns:
        Empty list if all validations pass.
        List of error messages if any validations fail.
    """
    errors: list[str] = []

    # Layer 0: Required settings
    if not settings.scopes.strip():
        errors.append(
            "SCOPES is required but not set. "
            "Set the SCOPES environment variable."
        )

    # Layer 2: Conditional settings based on mode
    if settings.skills_execution_mode == "prompt_first":
        missing_agent_settings = []
        if not settings.agent_model.strip():
            missing_agent_settings.append("AGENT_MODEL")
        if not settings.agent_base_url.strip():
            missing_agent_settings.append("AGENT_BASE_URL")
        if not settings.agent_api_key.strip():
            missing_agent_settings.append("AGENT_API_KEY")

        if missing_agent_settings:
            missing_str = ", ".join(missing_agent_settings)
            errors.append(
                f"{missing_str} are required when SKILLS_EXECUTION_MODE=prompt_first. "
                f"Set these environment variables to enable LLM-driven skill execution."
            )

    return errors
