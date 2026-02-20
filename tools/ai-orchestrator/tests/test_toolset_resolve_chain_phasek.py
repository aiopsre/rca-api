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
from orchestrator.graph import OrchestratorConfig


class ToolsetResolveChainPhaseKTest(unittest.TestCase):
    @staticmethod
    def _settings() -> Settings:
        return Settings(
            base_url="http://127.0.0.1:5555",
            scopes="*",
            mcp_scopes="*",
            mcp_verify_remote_tools=False,
            instance_id="orc-phasek",
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

    def test_server_toolsets_chain_routes_to_second_toolset(self) -> None:
        called_tools: list[str] = []

        def _mcp_call(self, *, tool: str, input_payload: dict[str, object] | None, idempotency_key: str | None = None) -> dict[str, object]:
            if self._base_url.endswith("provider-one"):
                called_tools.append(f"one:{tool}")
                return {"output": {"from": "one", "tool": tool, "payload": input_payload or {}, "idempotency_key": idempotency_key}}
            called_tools.append(f"two:{tool}")
            return {"output": {"from": "two", "tool": tool, "payload": input_payload or {}, "idempotency_key": idempotency_key}}

        class _FakeClient:
            @staticmethod
            def resolve_toolset(_pipeline: str) -> dict[str, object]:
                return {
                    "pipeline": "basic_rca",
                    "toolsets": [
                        {
                            "toolsetID": "ts_one",
                            "providers": [
                                {
                                    "type": "mcp_http",
                                    "baseURL": "http://provider-one",
                                    "allowTools": ["query_metrics"],
                                }
                            ],
                        },
                        {
                            "toolsetID": "ts_two",
                            "providers": [
                                {
                                    "type": "mcp_http",
                                    "baseURL": "http://provider-two",
                                    "allowTools": ["query_logs"],
                                }
                            ],
                        },
                    ],
                }

        with mock.patch("orchestrator.tooling.invoker.MCPHttpProvider.call", new=_mcp_call):
            invoker, toolsets, source = runner_module._select_tool_invoker_via_server(_FakeClient(), "basic_rca")
            result = invoker.call(tool="mcp.query_logs", input_payload={"query": "error"})

        self.assertEqual(source, "server_resolve")
        self.assertEqual(toolsets, ["ts_one", "ts_two"])
        self.assertEqual(result["output"]["from"], "two")
        self.assertEqual(called_tools, ["two:query_logs"])

    def test_runner_toolset_select_observation_keeps_toolsets_list_for_server_source(self) -> None:
        class _FakeClient:
            @staticmethod
            def get_job(job_id: str) -> dict[str, str]:
                return {"jobID": job_id, "incidentID": "inc-k", "pipeline": "basic_rca"}

            @staticmethod
            def resolve_toolset(_pipeline: str) -> dict[str, object]:
                return {
                    "pipeline": "basic_rca",
                    "toolsets": [
                        {
                            "toolsetID": "ts_one",
                            "providers": [
                                {
                                    "type": "mcp_http",
                                    "baseURL": "http://provider-one",
                                    "allowTools": ["query_metrics"],
                                }
                            ],
                        },
                        {
                            "toolsetID": "ts_two",
                            "providers": [
                                {
                                    "type": "mcp_http",
                                    "baseURL": "http://provider-two",
                                    "allowTools": ["query_logs"],
                                }
                            ],
                        },
                    ],
                }

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
                return {"job_id": "job-k", "started": True, "finalized": True}

        def _fake_builder(_runtime: _FakeRuntime, _cfg: OrchestratorConfig) -> _FakeGraph:
            return _FakeGraph()

        with mock.patch.object(runner_module, "_new_client", return_value=_FakeClient()), mock.patch.object(
            runner_module, "OrchestratorRuntime", _FakeRuntime
        ), mock.patch.object(runner_module, "get_template_builder", return_value=_fake_builder):
            runner_module._invoke_graph(
                self._settings(),
                OrchestratorConfig(run_query=True, post_finalize_observe=False, run_verification=False),
                "job-k",
                debug=False,
            )

        runtime = _FakeRuntime.instances[-1]
        self.assertEqual(runtime.start_calls, 1)
        self.assertEqual(runtime.shutdown_calls, 1)
        self.assertEqual(len(runtime.report_observation_calls), 1)
        observed = runtime.report_observation_calls[0]
        self.assertEqual(observed["tool"], "toolset.select")
        self.assertEqual(observed["response"]["source"], "server_resolve")
        self.assertEqual(observed["response"]["toolsets"], ["ts_one", "ts_two"])
        self.assertEqual(observed["response"]["available_tools"], ["query_logs", "query_metrics"])


if __name__ == "__main__":
    unittest.main()
