from __future__ import annotations

import pathlib
import sys
import types
import unittest
from unittest import mock


TESTS_DIR = pathlib.Path(__file__).resolve().parent
PROJECT_DIR = TESTS_DIR.parent
if str(PROJECT_DIR) not in sys.path:
    sys.path.insert(0, str(PROJECT_DIR))

from orchestrator.daemon import runner as runner_module
from orchestrator.daemon.settings import Settings
from orchestrator.graph import OrchestratorConfig
from orchestrator.runtime.runtime import OrchestratorRuntime


class ToolsetRegistryRunnerTest(unittest.TestCase):
    @staticmethod
    def _settings(toolset_config_json: str) -> Settings:
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
            toolset_config_json=toolset_config_json,
        )

    def test_pipeline_selects_toolset_and_builds_invoker(self) -> None:
        fake_module_name = "_phaseg_fake_skill_mod"
        called_tools: list[str] = []
        fake_module = types.ModuleType(fake_module_name)

        def _skill_call(tool: str, payload: dict[str, object], idempotency_key: str | None = None) -> dict[str, object]:
            called_tools.append(tool)
            return {"output": {"echo_tool": tool, "idempotency_key": idempotency_key, "payload": payload}}

        fake_module.call = _skill_call  # type: ignore[attr-defined]
        sys.modules[fake_module_name] = fake_module

        class _FakeClient:
            @staticmethod
            def get_job(job_id: str) -> dict[str, str]:
                return {"jobID": job_id, "incidentID": "inc-1", "pipeline": "basic_rca"}

        class _FakeRuntime:
            instances: list["_FakeRuntime"] = []

            def __init__(self, **kwargs: object) -> None:
                self.tool_invoker = kwargs.get("tool_invoker")
                self.start_calls = 0
                self.shutdown_calls = 0
                _FakeRuntime.instances.append(self)

            def start(self) -> bool:
                self.start_calls += 1
                return True

            def shutdown(self) -> None:
                self.shutdown_calls += 1

        class _FakeCompiledGraph:
            def invoke(self, state: object) -> dict[str, object]:
                self.state = state
                return {"job_id": "job-1", "started": True, "finalized": True}

        def _fake_builder(runtime: _FakeRuntime, _cfg: OrchestratorConfig) -> _FakeCompiledGraph:
            self.assertIsNotNone(runtime.tool_invoker)
            return _FakeCompiledGraph()

        settings = self._settings(
            toolset_config_json=(
                '{"pipelines":{"basic_rca":"skills_main"},'
                '"toolsets":{"skills_main":{"providers":[{"type":"skills","module":"_phaseg_fake_skill_mod",'
                '"allow_tools":["query_logs"]}]}}}'
            )
        )

        try:
            with mock.patch.object(runner_module, "_new_client", return_value=_FakeClient()), mock.patch.object(
                runner_module, "OrchestratorRuntime", _FakeRuntime
            ), mock.patch.object(runner_module, "get_template_builder", return_value=_fake_builder):
                runner_module._invoke_graph(
                    settings,
                    OrchestratorConfig(run_query=True, post_finalize_observe=False, run_verification=False),
                    "job-1",
                    debug=False,
                )
        finally:
            del sys.modules[fake_module_name]

        runtime = _FakeRuntime.instances[-1]
        self.assertEqual(runtime.start_calls, 1)
        self.assertEqual(runtime.shutdown_calls, 1)
        self.assertIsNotNone(runtime.tool_invoker)
        result = runtime.tool_invoker.call(tool="mcp.query_logs", input_payload={"query": "error"})
        self.assertEqual(result["output"]["echo_tool"], "query_logs")
        self.assertEqual(called_tools, ["query_logs"])

    def test_unknown_toolset_fail_fast_no_graph_or_toolcall(self) -> None:
        graph_invoked = {"count": 0}

        class _FakeClient:
            @staticmethod
            def get_job(job_id: str) -> dict[str, str]:
                return {"jobID": job_id, "incidentID": "inc-2", "pipeline": "basic_rca"}

        class _FakeRuntime:
            instances: list["_FakeRuntime"] = []

            def __init__(self, **kwargs: object) -> None:
                self.start_calls = 0
                self.query_metrics_calls = 0
                self.query_logs_calls = 0
                self.report_tool_call_calls = 0
                self.finalize_calls: list[dict[str, object]] = []
                self.shutdown_calls = 0
                _FakeRuntime.instances.append(self)

            def start(self) -> bool:
                self.start_calls += 1
                return True

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

        def _unexpected_builder(*_: object) -> object:
            class _Graph:
                def invoke(self, _state: object) -> dict[str, object]:
                    graph_invoked["count"] += 1
                    return {"job_id": "job-2", "started": True}

            return _Graph()

        settings = self._settings(
            toolset_config_json=(
                '{"pipelines":{"basic_rca":"missing_toolset"},'
                '"toolsets":{"default":{"providers":[{"type":"skills","module":"json","allow_tools":["query_logs"]}]}}}'
            )
        )
        with mock.patch.object(runner_module, "_new_client", return_value=_FakeClient()), mock.patch.object(
            runner_module, "OrchestratorRuntime", _FakeRuntime
        ), mock.patch.object(runner_module, "get_template_builder", return_value=_unexpected_builder):
            runner_module._invoke_graph(
                settings,
                OrchestratorConfig(run_query=True, post_finalize_observe=False, run_verification=False),
                "job-2",
                debug=False,
            )

        runtime = _FakeRuntime.instances[-1]
        self.assertEqual(runtime.start_calls, 1)
        self.assertEqual(runtime.query_metrics_calls, 0)
        self.assertEqual(runtime.query_logs_calls, 0)
        self.assertEqual(runtime.report_tool_call_calls, 0)
        self.assertEqual(runtime.shutdown_calls, 1)
        self.assertEqual(graph_invoked["count"], 0)
        self.assertEqual(len(runtime.finalize_calls), 1)
        self.assertEqual(runtime.finalize_calls[0]["status"], "failed")
        self.assertIn("toolset_selection_failed:", str(runtime.finalize_calls[0]["error_message"]))


class VerificationRuntimeToolInvokerTest(unittest.TestCase):
    def test_verification_runner_uses_runtime_call_tool(self) -> None:
        called_tools: list[str] = []
        published: list[dict[str, object]] = []

        class _FakeSession:
            def __init__(self) -> None:
                self.headers: dict[str, str] = {}

        class _FakeMCPClient:
            def call(self, **_: object) -> dict[str, object]:
                raise AssertionError("verification must not call RCAApiClient.mcp_client.call directly")

        class _FakeClient:
            def __init__(self) -> None:
                self.session = _FakeSession()
                self.instance_id = ""
                self.mcp_client = _FakeMCPClient()

            def create_incident_verification_run(self, **kwargs: object) -> dict[str, object]:
                published.append(kwargs)
                return {"run": {"runID": f"run-{len(published)}"}}

        class _FakeInvoker:
            def call(
                self,
                *,
                tool: str,
                input_payload: dict[str, object] | None,
                idempotency_key: str | None = None,
            ) -> dict[str, object]:
                called_tools.append(tool)
                return {"output": {"queryResultJSON": '{"rows":[{"line":"error timeout"}]}'}}

        runtime = OrchestratorRuntime(
            client=_FakeClient(),
            job_id="job-verify",
            instance_id="orc-verify",
            heartbeat_interval_seconds=10,
            verification_dedupe_enabled=False,
            tool_invoker=_FakeInvoker(),
        )
        plan = {
            "steps": [
                {
                    "tool": "mcp.query_logs",
                    "params": {"query": "error"},
                    "expected": {"type": "contains_keyword", "keyword": "timeout"},
                }
            ]
        }
        results = runtime.run_verification(
            incident_id="inc-verify",
            verification_plan=plan,
            source="ai_job",
        )
        self.assertEqual(len(results), 1)
        self.assertTrue(results[0].meets_expectation)
        self.assertEqual(called_tools, ["query_logs"])
        self.assertEqual(len(published), 1)


if __name__ == "__main__":
    unittest.main()
