from __future__ import annotations

import unittest
from orchestrator.daemon.settings import Settings, validate_settings


def _make_settings(
    *,
    scopes: str = "tenant:default",
    skills_execution_mode: str = "prompt_first",
    agent_model: str = "gpt-4o",
    agent_base_url: str = "https://api.openai.com/v1",
    agent_api_key: str = "sk-test",
) -> Settings:
    """Create a Settings instance with sensible defaults for testing."""
    return Settings(
        base_url="http://127.0.0.1:5555",
        scopes=scopes,
        mcp_scopes="",
        mcp_verify_remote_tools=False,
        instance_id="test-instance",
        poll_interval_ms=1000,
        lease_heartbeat_interval_seconds=10,
        concurrency=1,
        run_query=False,
        force_no_evidence=False,
        force_conflict=False,
        ds_base_url="",
        ds_type="prometheus",
        metrics_ds_type="prometheus",
        logs_ds_type="prometheus",
        auto_create_datasource=True,
        debug=False,
        pull_limit=10,
        long_poll_wait_seconds=20,
        a3_max_calls=6,
        a3_max_total_bytes=2097152,
        a3_max_total_latency_ms=8000,
        run_verification=False,
        post_finalize_observe=True,
        verification_source="ai_job",
        verification_max_steps=20,
        verification_max_total_latency_ms=8000,
        verification_max_total_bytes=2097152,
        verification_dedupe_enabled=True,
        post_finalize_wait_timeout_seconds=8,
        post_finalize_wait_interval_ms=500,
        post_finalize_wait_max_interval_ms=2000,
        toolset_config_path="",
        toolset_config_json="",
        skills_execution_mode=skills_execution_mode,
        skills_tool_calling_mode="disabled",
        skills_cache_dir="/tmp/rca-ai-orchestrator/skills-cache",
        skills_local_paths="",
        agent_model=agent_model,
        agent_base_url=agent_base_url,
        agent_api_key=agent_api_key,
        agent_timeout_seconds=20.0,
        health_port=8080,
        health_host="0.0.0.0",
        tool_execution_max_workers=5,
        tool_execution_group_timeout_s=30.0,
    )


class TestSettingsValidation(unittest.TestCase):
    def test_scopes_required(self):
        """SCOPES is always required."""
        settings = _make_settings(scopes="")
        errors = validate_settings(settings)
        self.assertEqual(len(errors), 1)
        self.assertIn("SCOPES", errors[0])

    def test_scopes_whitespace_only_fails(self):
        """SCOPES with only whitespace should fail."""
        settings = _make_settings(scopes="   ")
        errors = validate_settings(settings)
        self.assertEqual(len(errors), 1)
        self.assertIn("SCOPES", errors[0])

    def test_prompt_first_requires_all_agent_settings(self):
        """When prompt_first mode, all AGENT_* settings are required."""
        settings = _make_settings(
            scopes="tenant:default",
            skills_execution_mode="prompt_first",
            agent_model="",
            agent_base_url="",
            agent_api_key="",
        )
        errors = validate_settings(settings)
        self.assertEqual(len(errors), 1)
        self.assertIn("AGENT_MODEL", errors[0])
        self.assertIn("AGENT_BASE_URL", errors[0])
        self.assertIn("AGENT_API_KEY", errors[0])

    def test_prompt_first_missing_model_only(self):
        """When prompt_first mode, missing only AGENT_MODEL should fail."""
        settings = _make_settings(
            scopes="tenant:default",
            skills_execution_mode="prompt_first",
            agent_model="",
            agent_base_url="https://api.openai.com/v1",
            agent_api_key="sk-test",
        )
        errors = validate_settings(settings)
        self.assertEqual(len(errors), 1)
        self.assertIn("AGENT_MODEL", errors[0])
        self.assertNotIn("AGENT_BASE_URL", errors[0])
        self.assertNotIn("AGENT_API_KEY", errors[0])

    def test_prompt_first_missing_base_url_only(self):
        """When prompt_first mode, missing only AGENT_BASE_URL should fail."""
        settings = _make_settings(
            scopes="tenant:default",
            skills_execution_mode="prompt_first",
            agent_model="gpt-4o",
            agent_base_url="",
            agent_api_key="sk-test",
        )
        errors = validate_settings(settings)
        self.assertEqual(len(errors), 1)
        self.assertIn("AGENT_BASE_URL", errors[0])
        self.assertNotIn("AGENT_MODEL", errors[0])
        self.assertNotIn("AGENT_API_KEY", errors[0])

    def test_prompt_first_missing_api_key_only(self):
        """When prompt_first mode, missing only AGENT_API_KEY should fail."""
        settings = _make_settings(
            scopes="tenant:default",
            skills_execution_mode="prompt_first",
            agent_model="gpt-4o",
            agent_base_url="https://api.openai.com/v1",
            agent_api_key="",
        )
        errors = validate_settings(settings)
        self.assertEqual(len(errors), 1)
        self.assertIn("AGENT_API_KEY", errors[0])
        self.assertNotIn("AGENT_MODEL", errors[0])
        self.assertNotIn("AGENT_BASE_URL", errors[0])

    def test_catalog_mode_no_agent_required(self):
        """When catalog mode, AGENT_* settings are not required."""
        settings = _make_settings(
            scopes="tenant:default",
            skills_execution_mode="catalog",
            agent_model="",
            agent_base_url="",
            agent_api_key="",
        )
        errors = validate_settings(settings)
        self.assertEqual(len(errors), 0)

    def test_valid_prompt_first_config(self):
        """Valid prompt_first configuration should pass validation."""
        settings = _make_settings(
            scopes="tenant:default",
            skills_execution_mode="prompt_first",
            agent_model="gpt-4o",
            agent_base_url="https://api.openai.com/v1",
            agent_api_key="sk-test-key",
        )
        errors = validate_settings(settings)
        self.assertEqual(len(errors), 0)

    def test_both_scopes_and_agent_missing(self):
        """Both missing SCOPES and AGENT_* should report both errors."""
        settings = _make_settings(
            scopes="",
            skills_execution_mode="prompt_first",
            agent_model="",
            agent_base_url="",
            agent_api_key="",
        )
        errors = validate_settings(settings)
        self.assertEqual(len(errors), 2)
        # Check that both errors are present
        error_text = " ".join(errors)
        self.assertIn("SCOPES", error_text)
        self.assertIn("AGENT_MODEL", error_text)


if __name__ == "__main__":
    unittest.main()