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
from orchestrator.runtime.retry import RetryPolicy
from orchestrator.runtime.runtime import OrchestratorRuntime
from orchestrator.tooling import ToolInvokeError
from orchestrator.tooling.invoker import TOOLING_META_KEY


class RunnerToolsetSelectObservationTest(unittest.TestCase):
    @staticmethod
    def _settings(toolset_config_json: str) -> Settings:
        return Settings(
            base_url="http://127.0.0.1:5555",
            scopes="*",
            mcp_scopes="*",
            mcp_verify_remote_tools=False,
            instance_id="orc-phasei",
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

    def test_runner_reports_toolset_select_before_graph(self) -> None:
        fake_module_name = "_phasei_fake_skill_mod"
        fake_module = types.ModuleType(fake_module_name)
        fake_module.call = lambda *_args, **_kwargs: {"output": {"ok": True}}  # type: ignore[attr-defined]
        sys.modules[fake_module_name] = fake_module

        graph_invoked = {"count": 0}

        class _FakeClient:
            @staticmethod
            def get_job(job_id: str) -> dict[str, str]:
                return {"jobID": job_id, "incidentID": "inc-phasei-1", "pipeline": "basic_rca"}

        class _FakeRuntime:
            instances: list["_FakeRuntime"] = []

            def __init__(self, **kwargs: object) -> None:
                self.tool_invoker = kwargs.get("tool_invoker")
                self.report_observation_calls: list[dict[str, object]] = []
                self.start_calls = 0
                self.shutdown_calls = 0
                _FakeRuntime.instances.append(self)

            def start(self) -> bool:
                self.start_calls += 1
                return True

            def report_observation(self, **kwargs: object) -> int:
                self.report_observation_calls.append(kwargs)
                return len(self.report_observation_calls)

            def shutdown(self) -> None:
                self.shutdown_calls += 1

        class _FakeGraph:
            def invoke(self, _state: object) -> dict[str, object]:
                graph_invoked["count"] += 1
                return {"job_id": "job-phasei-1", "started": True, "finalized": True}

        def _fake_builder(_runtime: _FakeRuntime, _cfg: OrchestratorConfig) -> _FakeGraph:
            return _FakeGraph()

        settings = self._settings(
            toolset_config_json=(
                '{"pipelines":{"basic_rca":"skills_obs"},'
                '"toolsets":{"skills_obs":{"providers":[{"type":"skills","module":"_phasei_fake_skill_mod",'
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
                    "job-phasei-1",
                    debug=False,
                )
        finally:
            del sys.modules[fake_module_name]

        runtime = _FakeRuntime.instances[-1]
        self.assertEqual(runtime.start_calls, 1)
        self.assertEqual(runtime.shutdown_calls, 1)
        self.assertEqual(graph_invoked["count"], 1)
        self.assertEqual(len(runtime.report_observation_calls), 1)
        observed = runtime.report_observation_calls[0]
        self.assertEqual(observed["tool"], "toolset.select")
        self.assertEqual(observed["node_name"], "runner.pre_graph")
        params = observed["params"]
        self.assertEqual(params["pipeline"], "basic_rca")
        self.assertEqual(params["template"], "_fake_builder")
        response = observed["response"]
        self.assertEqual(response["toolsets"], ["skills_obs"])
        self.assertEqual(response["source"], "local_override")
        self.assertEqual(len(response["providers"]), 1)
        self.assertEqual(response["providers"][0]["provider_type"], "skills")


class RuntimeToolInvokeObservationTest(unittest.TestCase):
    def test_call_tool_reports_invoke_with_provider_meta_and_latency(self) -> None:
        class _FakeSession:
            def __init__(self) -> None:
                self.headers: dict[str, str] = {}

        class _FakeMCPClient:
            def call(self, **_: object) -> dict[str, object]:
                raise AssertionError("mcp_client.call should not be used when tool_invoker is injected")

        class _FakeClient:
            def __init__(self) -> None:
                self.session = _FakeSession()
                self.instance_id = ""
                self.mcp_client = _FakeMCPClient()
                self.tool_calls: list[dict[str, object]] = []

            @staticmethod
            def start_job(_job_id: str) -> bool:
                return True

            @staticmethod
            def renew_job_lease(_job_id: str) -> None:
                return None

            def add_tool_call(self, **kwargs: object) -> None:
                self.tool_calls.append(kwargs)

        class _FakeInvoker:
            @staticmethod
            def call(
                *,
                tool: str,
                input_payload: dict[str, object] | None,
                idempotency_key: str | None = None,
            ) -> dict[str, object]:
                return {
                    "output": {"tool": tool, "query": input_payload or {}, "idempotency_key": idempotency_key},
                    TOOLING_META_KEY: {"provider_id": "skills.main", "provider_type": "skills"},
                }

        client = _FakeClient()
        runtime = OrchestratorRuntime(
            client=client,
            job_id="job-phasei-2",
            instance_id="orc-phasei",
            heartbeat_interval_seconds=60,
            retry_policy=RetryPolicy(max_attempts=1, base_delay_seconds=0.0, max_delay_seconds=0.0),
            tool_invoker=_FakeInvoker(),
        )
        self.assertTrue(runtime.start())
        result = runtime.call_tool("mcp.query_logs", {"query": "error"}, idempotency_key="idem-1")
        runtime.shutdown()

        self.assertEqual(result["output"]["tool"], "mcp.query_logs")
        self.assertNotIn(TOOLING_META_KEY, result)
        self.assertEqual(len(client.tool_calls), 1)
        tool_call = client.tool_calls[0]
        self.assertEqual(tool_call["tool_name"], "tool.invoke")
        self.assertEqual(tool_call["status"], "ok")
        response_json = tool_call["response_json"]
        self.assertEqual(response_json["provider_id"], "skills.main")
        self.assertEqual(response_json["provider_type"], "skills")
        self.assertGreaterEqual(int(response_json["latency_ms"]), 1)

    def test_call_tool_allowlist_rejected_reports_and_raises(self) -> None:
        class _FakeSession:
            def __init__(self) -> None:
                self.headers: dict[str, str] = {}

        class _FakeClient:
            def __init__(self) -> None:
                self.session = _FakeSession()
                self.instance_id = ""
                self.tool_calls: list[dict[str, object]] = []

            @staticmethod
            def start_job(_job_id: str) -> bool:
                return True

            @staticmethod
            def renew_job_lease(_job_id: str) -> None:
                return None

            def add_tool_call(self, **kwargs: object) -> None:
                self.tool_calls.append(kwargs)

        class _RejectingInvoker:
            @staticmethod
            def call(**_: object) -> dict[str, object]:
                raise ToolInvokeError("allowlist denied", retryable=False, reason="allow_tools_denied")

        client = _FakeClient()
        runtime = OrchestratorRuntime(
            client=client,
            job_id="job-phasei-3",
            instance_id="orc-phasei",
            heartbeat_interval_seconds=60,
            retry_policy=RetryPolicy(max_attempts=1, base_delay_seconds=0.0, max_delay_seconds=0.0),
            tool_invoker=_RejectingInvoker(),
        )
        self.assertTrue(runtime.start())
        with self.assertRaises(ToolInvokeError):
            runtime.call_tool("mcp.query_logs", {"query": "error"})
        runtime.shutdown()

        self.assertEqual(len(client.tool_calls), 1)
        tool_call = client.tool_calls[0]
        self.assertEqual(tool_call["tool_name"], "tool.invoke_rejected")
        self.assertEqual(tool_call["status"], "error")
        response_json = tool_call["response_json"]
        self.assertEqual(response_json["error_category"], "allow_tools_denied")


if __name__ == "__main__":
    unittest.main()
