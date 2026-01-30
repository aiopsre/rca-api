from __future__ import annotations

import pathlib
import sys
import unittest
from unittest import mock


TESTS_DIR = pathlib.Path(__file__).resolve().parent
PROJECT_DIR = TESTS_DIR.parent
if str(PROJECT_DIR) not in sys.path:
    sys.path.insert(0, str(PROJECT_DIR))

from orchestrator.daemon import runner as runner_module
from orchestrator.daemon.settings import Settings


class TemplateRegistryPhaseL0RunnerTest(unittest.TestCase):
    @staticmethod
    def _settings() -> Settings:
        return Settings(
            base_url="http://127.0.0.1:5555",
            scopes="*",
            mcp_scopes="*",
            mcp_verify_remote_tools=False,
            instance_id="orc-l0",
            poll_interval_ms=1000,
            lease_heartbeat_interval_seconds=10,
            concurrency=1,
            run_query=True,
            force_no_evidence=False,
            force_conflict=False,
            ds_base_url="http://prometheus:9090",
            auto_create_datasource=True,
            debug=False,
            pull_limit=10,
            long_poll_wait_seconds=20,
            a3_max_calls=6,
            a3_max_total_bytes=2 * 1024 * 1024,
            a3_max_total_latency_ms=8000,
            run_verification=False,
            post_finalize_observe=False,
            verification_source="ai_job",
            verification_max_steps=20,
            verification_max_total_latency_ms=8000,
            verification_max_total_bytes=2 * 1024 * 1024,
            verification_dedupe_enabled=True,
            post_finalize_wait_timeout_seconds=8,
            post_finalize_wait_interval_ms=500,
            post_finalize_wait_max_interval_ms=2000,
            toolset_config_path="",
            toolset_config_json="",
        )

    def setUp(self) -> None:
        runner_module._TEMPLATE_REGISTRY_LAST_ATTEMPT_TS = 0.0

    def test_register_templates_called_with_deduped_templates(self) -> None:
        calls: list[tuple[str, list[dict[str, str]]]] = []

        class _FakeClient:
            @staticmethod
            def register_templates(instance_id: str, templates: list[dict[str, str]]) -> dict[str, int]:
                calls.append((instance_id, templates))
                return {"count": len(templates)}

        with mock.patch.object(runner_module, "list_template_ids", return_value=["basic_rca", "", "basic_rca"]):
            runner_module._register_templates_if_due(
                settings=self._settings(),
                client=_FakeClient(),
                force=True,
            )

        self.assertEqual(len(calls), 1)
        self.assertEqual(calls[0][0], "orc-l0")
        self.assertEqual(calls[0][1], [{"templateID": "basic_rca", "version": ""}])

    def test_register_templates_refreshes_every_sixty_seconds(self) -> None:
        calls: list[int] = []

        class _FakeClient:
            @staticmethod
            def register_templates(instance_id: str, templates: list[dict[str, str]]) -> dict[str, int]:
                self.assertEqual(instance_id, "orc-l0")
                self.assertTrue(len(templates) > 0)
                calls.append(1)
                return {"count": len(templates)}

        with mock.patch.object(runner_module, "list_template_ids", return_value=["basic_rca"]), mock.patch.object(
            runner_module.time,
            "time",
            side_effect=[100.0, 120.0, 161.0],
        ):
            runner_module._register_templates_if_due(settings=self._settings(), client=_FakeClient(), force=False)
            runner_module._register_templates_if_due(settings=self._settings(), client=_FakeClient(), force=False)
            runner_module._register_templates_if_due(settings=self._settings(), client=_FakeClient(), force=False)

        self.assertEqual(len(calls), 2)

    def test_register_templates_is_best_effort(self) -> None:
        class _FakeClient:
            @staticmethod
            def register_templates(instance_id: str, templates: list[dict[str, str]]) -> dict[str, int]:
                raise RuntimeError(f"register failed instance={instance_id} count={len(templates)}")

        with mock.patch.object(runner_module, "list_template_ids", return_value=["basic_rca"]):
            runner_module._register_templates_if_due(settings=self._settings(), client=_FakeClient(), force=True)


if __name__ == "__main__":
    unittest.main()
