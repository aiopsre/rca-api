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
from orchestrator.langgraph.config import OrchestratorConfig
from orchestrator.langgraph.registry import UnknownPipelineError, get_template_builder
from orchestrator.langgraph.templates.basic_rca import build_basic_rca_graph


class TemplateRegistryTest(unittest.TestCase):
    def test_registry_selects_basic_rca_with_pipeline_normalization(self) -> None:
        self.assertIs(get_template_builder("basic_rca"), build_basic_rca_graph)
        self.assertIs(get_template_builder("  BASIC_RCA "), build_basic_rca_graph)
        self.assertIs(get_template_builder(""), build_basic_rca_graph)
        self.assertIs(get_template_builder(None), build_basic_rca_graph)

    def test_registry_raises_for_unknown_pipeline(self) -> None:
        with self.assertRaises(UnknownPipelineError):
            get_template_builder("custom_pipeline")


class InvokeGraphTemplateSelectionTest(unittest.TestCase):
    @staticmethod
    def _settings() -> Settings:
        return Settings(
            base_url="http://127.0.0.1:5555",
            scopes="*",
            mcp_scopes="*",
            mcp_verify_remote_tools=False,
            instance_id="orc-test",
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
            toolset_config_json=(
                '{"pipelines":{"basic_rca":"default"},'
                '"toolsets":{"default":{"providers":[{"type":"skills","module":"json","allow_tools":["query_logs"]}]}}}'
            ),
        )

    def test_unknown_pipeline_fail_fast_without_query_or_toolcall_write(self) -> None:
        class _FakeRuntime:
            instances: list["_FakeRuntime"] = []

            def __init__(self, **kwargs: object) -> None:
                self.job_id = str(kwargs.get("job_id") or "")
                self.start_calls = 0
                self.get_job_calls = 0
                self.query_metrics_calls = 0
                self.query_logs_calls = 0
                self.report_tool_call_calls = 0
                self.finalize_calls: list[dict[str, object]] = []
                self.shutdown_calls = 0
                _FakeRuntime.instances.append(self)

            def start(self) -> bool:
                self.start_calls += 1
                return True

            def get_job(self, job_id: str | None = None) -> dict[str, object]:
                self.get_job_calls += 1
                return {
                    "jobID": job_id or self.job_id,
                    "incidentID": "inc-unknown",
                    "pipeline": "unknown_pipeline",
                }

            def query_metrics(self, **_: object) -> dict[str, object]:
                self.query_metrics_calls += 1
                return {}

            def query_logs(self, **_: object) -> dict[str, object]:
                self.query_logs_calls += 1
                return {}

            def report_tool_call(self, **_: object) -> int:
                self.report_tool_call_calls += 1
                return self.report_tool_call_calls

            def finalize(
                self,
                *,
                status: str,
                diagnosis_json: dict[str, object] | None,
                error_message: str | None = None,
                evidence_ids: list[str] | None = None,
            ) -> None:
                self.finalize_calls.append(
                    {
                        "status": status,
                        "diagnosis_json": diagnosis_json,
                        "error_message": error_message,
                        "evidence_ids": evidence_ids or [],
                    }
                )

            def shutdown(self) -> None:
                self.shutdown_calls += 1

        class _FakeClient:
            @staticmethod
            def get_job(job_id: str) -> dict[str, object]:
                return {
                    "jobID": job_id,
                    "incidentID": "inc-unknown",
                    "pipeline": "unknown_pipeline",
                }

        with mock.patch.object(runner_module, "_new_client", return_value=_FakeClient()), mock.patch.object(
            runner_module, "OrchestratorRuntime", _FakeRuntime
        ):
            runner_module._invoke_graph(
                self._settings(),
                OrchestratorConfig(run_query=True, post_finalize_observe=False, run_verification=False),
                "job-unknown",
                debug=False,
            )

        runtime = _FakeRuntime.instances[-1]
        self.assertEqual(runtime.start_calls, 1)
        self.assertEqual(runtime.get_job_calls, 0)
        self.assertEqual(runtime.query_metrics_calls, 0)
        self.assertEqual(runtime.query_logs_calls, 0)
        self.assertEqual(runtime.report_tool_call_calls, 0)
        self.assertEqual(runtime.shutdown_calls, 1)
        self.assertEqual(len(runtime.finalize_calls), 1)
        self.assertEqual(runtime.finalize_calls[0]["status"], "failed")
        self.assertIn("unknown pipeline template", str(runtime.finalize_calls[0]["error_message"]))


if __name__ == "__main__":
    unittest.main()
