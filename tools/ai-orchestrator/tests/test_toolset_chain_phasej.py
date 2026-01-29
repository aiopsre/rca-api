from __future__ import annotations

import json
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
from orchestrator.tooling import build_tool_invoker_chain, load_toolset_config
from orchestrator.tooling.invoker import TOOLING_META_KEY


class ToolsetChainConfigTest(unittest.TestCase):
    def test_pipelines_list_mapping_parses_to_chain(self) -> None:
        config = load_toolset_config(
            {
                "pipelines": {
                    "basic_rca": ["ts_a", "ts_b"],
                },
                "toolsets": {
                    "ts_a": {
                        "providers": [
                            {"type": "skills", "module": "json", "allow_tools": ["query_logs"]},
                        ]
                    },
                    "ts_b": {
                        "providers": [
                            {"type": "skills", "module": "json", "allow_tools": ["query_metrics"]},
                        ]
                    },
                },
            }
        )
        self.assertEqual(config.get_toolset_chain("basic_rca"), ["ts_a", "ts_b"])
        self.assertEqual(config.resolve_toolset_id("basic_rca"), "ts_a")

    def test_missing_toolset_id_fail_fast(self) -> None:
        with self.assertRaisesRegex(ValueError, "pipeline=basic_rca references missing toolset_id=ts_missing"):
            load_toolset_config(
                {
                    "pipelines": {
                        "basic_rca": ["ts_a", "ts_missing"],
                    },
                    "toolsets": {
                        "ts_a": {
                            "providers": [
                                {"type": "skills", "module": "some.module", "allow_tools": ["query_logs"]},
                            ]
                        },
                    },
                }
            )

    def test_empty_chain_fail_fast(self) -> None:
        with self.assertRaisesRegex(ValueError, "pipeline=basic_rca has empty toolset chain"):
            load_toolset_config(
                {
                    "pipelines": {
                        "basic_rca": [],
                    },
                    "toolsets": {
                        "ts_a": {
                            "providers": [
                                {"type": "skills", "module": "some.module", "allow_tools": ["query_logs"]},
                            ]
                        },
                    },
                }
            )


class ToolInvokerChainRoutingTest(unittest.TestCase):
    def test_chain_routes_to_second_toolset(self) -> None:
        module_one = "_phasej_skill_mod_one"
        module_two = "_phasej_skill_mod_two"
        calls: list[str] = []
        mod_one = types.ModuleType(module_one)
        mod_two = types.ModuleType(module_two)

        def _call_one(tool: str, payload: dict[str, object], idempotency_key: str | None = None) -> dict[str, object]:
            calls.append(f"one:{tool}")
            return {"output": {"from": "one", "tool": tool, "payload": payload, "idempotency_key": idempotency_key}}

        def _call_two(tool: str, payload: dict[str, object], idempotency_key: str | None = None) -> dict[str, object]:
            calls.append(f"two:{tool}")
            return {"output": {"from": "two", "tool": tool, "payload": payload, "idempotency_key": idempotency_key}}

        mod_one.call = _call_one  # type: ignore[attr-defined]
        mod_two.call = _call_two  # type: ignore[attr-defined]
        sys.modules[module_one] = mod_one
        sys.modules[module_two] = mod_two

        try:
            config = load_toolset_config(
                {
                    "pipelines": {"basic_rca": ["ts_1", "ts_2"]},
                    "toolsets": {
                        "ts_1": {
                            "providers": [
                                {"type": "skills", "module": module_one, "allow_tools": ["query_metrics"]},
                            ]
                        },
                        "ts_2": {
                            "providers": [
                                {"type": "skills", "module": module_two, "allow_tools": ["query_logs"]},
                            ]
                        },
                    },
                }
            )
            invoker = build_tool_invoker_chain(config, ["ts_1", "ts_2"])
            result = invoker.call(tool="mcp.query_logs", input_payload={"query": "error"})
        finally:
            del sys.modules[module_one]
            del sys.modules[module_two]

        self.assertEqual(result["output"]["from"], "two")
        self.assertEqual(calls, ["two:query_logs"])
        self.assertEqual(result[TOOLING_META_KEY]["resolved_from_toolset_id"], "ts_2")

    def test_chain_conflict_prefers_first_toolset(self) -> None:
        module_one = "_phasej_skill_conflict_one"
        module_two = "_phasej_skill_conflict_two"
        calls: list[str] = []
        mod_one = types.ModuleType(module_one)
        mod_two = types.ModuleType(module_two)

        def _call_one(tool: str, payload: dict[str, object], idempotency_key: str | None = None) -> dict[str, object]:
            calls.append(f"one:{tool}")
            return {"output": {"from": "one", "tool": tool, "payload": payload, "idempotency_key": idempotency_key}}

        def _call_two(tool: str, payload: dict[str, object], idempotency_key: str | None = None) -> dict[str, object]:
            calls.append(f"two:{tool}")
            return {"output": {"from": "two", "tool": tool, "payload": payload, "idempotency_key": idempotency_key}}

        mod_one.call = _call_one  # type: ignore[attr-defined]
        mod_two.call = _call_two  # type: ignore[attr-defined]
        sys.modules[module_one] = mod_one
        sys.modules[module_two] = mod_two

        try:
            config = load_toolset_config(
                {
                    "pipelines": {"basic_rca": ["ts_1", "ts_2"]},
                    "toolsets": {
                        "ts_1": {
                            "providers": [
                                {"type": "skills", "module": module_one, "allow_tools": ["query_logs"]},
                            ]
                        },
                        "ts_2": {
                            "providers": [
                                {"type": "skills", "module": module_two, "allow_tools": ["query_logs"]},
                            ]
                        },
                    },
                }
            )
            invoker = build_tool_invoker_chain(config, ["ts_1", "ts_2"])
            result = invoker.call(tool="mcp.query_logs", input_payload={"query": "error"})
        finally:
            del sys.modules[module_one]
            del sys.modules[module_two]

        self.assertEqual(result["output"]["from"], "one")
        self.assertEqual(calls, ["one:query_logs"])
        self.assertEqual(result[TOOLING_META_KEY]["resolved_from_toolset_id"], "ts_1")


class RunnerChainObservationTest(unittest.TestCase):
    @staticmethod
    def _settings(toolset_config_json: str) -> Settings:
        return Settings(
            base_url="http://127.0.0.1:5555",
            scopes="*",
            mcp_scopes="*",
            mcp_verify_remote_tools=False,
            instance_id="orc-phasej",
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

    def test_runner_toolset_select_observation_contains_toolsets(self) -> None:
        module_one = "_phasej_runner_mod_one"
        module_two = "_phasej_runner_mod_two"
        mod_one = types.ModuleType(module_one)
        mod_two = types.ModuleType(module_two)
        mod_one.call = lambda *_args, **_kwargs: {"output": {"from": "one"}}  # type: ignore[attr-defined]
        mod_two.call = lambda *_args, **_kwargs: {"output": {"from": "two"}}  # type: ignore[attr-defined]
        sys.modules[module_one] = mod_one
        sys.modules[module_two] = mod_two

        graph_invoked = {"count": 0}

        class _FakeClient:
            @staticmethod
            def get_job(job_id: str) -> dict[str, str]:
                return {"jobID": job_id, "incidentID": "inc-j-runner", "pipeline": "basic_rca"}

        class _FakeRuntime:
            instances: list["_FakeRuntime"] = []

            def __init__(self, **kwargs: object) -> None:
                self.tool_invoker = kwargs.get("tool_invoker")
                self.report_observation_calls: list[dict[str, object]] = []
                _FakeRuntime.instances.append(self)

            @staticmethod
            def start() -> bool:
                return True

            def report_observation(self, **kwargs: object) -> int:
                self.report_observation_calls.append(kwargs)
                return len(self.report_observation_calls)

            @staticmethod
            def shutdown() -> None:
                return None

        class _FakeGraph:
            def invoke(self, _state: object) -> dict[str, object]:
                graph_invoked["count"] += 1
                return {"job_id": "job-j-runner", "started": True, "finalized": True}

        def _fake_builder(_runtime: _FakeRuntime, _cfg: OrchestratorConfig) -> _FakeGraph:
            return _FakeGraph()

        settings = self._settings(
            toolset_config_json=json.dumps(
                {
                    "pipelines": {"basic_rca": ["ts_a", "ts_b"]},
                    "toolsets": {
                        "ts_a": {
                            "providers": [
                                {"type": "skills", "module": module_one, "allow_tools": ["query_metrics"]},
                            ]
                        },
                        "ts_b": {
                            "providers": [
                                {"type": "skills", "module": module_two, "allow_tools": ["query_logs"]},
                            ]
                        },
                    },
                }
            )
        )

        try:
            with mock.patch.object(runner_module, "_new_client", return_value=_FakeClient()), mock.patch.object(
                runner_module, "OrchestratorRuntime", _FakeRuntime
            ), mock.patch.object(runner_module, "get_template_builder", return_value=_fake_builder):
                runner_module._invoke_graph(
                    settings,
                    OrchestratorConfig(run_query=True, post_finalize_observe=False, run_verification=False),
                    "job-j-runner",
                    debug=False,
                )
        finally:
            del sys.modules[module_one]
            del sys.modules[module_two]

        runtime = _FakeRuntime.instances[-1]
        self.assertEqual(graph_invoked["count"], 1)
        self.assertEqual(len(runtime.report_observation_calls), 1)
        observed = runtime.report_observation_calls[0]
        self.assertEqual(observed["tool"], "toolset.select")
        self.assertEqual(observed["response"]["toolsets"], ["ts_a", "ts_b"])
        self.assertEqual(observed["response"]["source"], "local_override")

    def test_runner_missing_toolset_id_fail_fast_before_graph(self) -> None:
        self._assert_runner_fail_fast(
            toolset_config_json=json.dumps(
                {
                    "pipelines": {"basic_rca": ["ts_a", "ts_missing"]},
                    "toolsets": {
                        "ts_a": {
                            "providers": [
                                {"type": "skills", "module": "some.module", "allow_tools": ["query_logs"]},
                            ]
                        },
                    },
                }
            ),
            expected_error="pipeline=basic_rca references missing toolset_id=ts_missing",
        )

    def test_runner_empty_chain_fail_fast_before_graph(self) -> None:
        self._assert_runner_fail_fast(
            toolset_config_json=json.dumps(
                {
                    "pipelines": {"basic_rca": []},
                    "toolsets": {
                        "ts_a": {
                            "providers": [
                                {"type": "skills", "module": "some.module", "allow_tools": ["query_logs"]},
                            ]
                        },
                    },
                }
            ),
            expected_error="pipeline=basic_rca has empty toolset chain",
        )

    def _assert_runner_fail_fast(self, *, toolset_config_json: str, expected_error: str) -> None:
        graph_invoked = {"count": 0}

        class _FakeClient:
            @staticmethod
            def get_job(job_id: str) -> dict[str, str]:
                return {"jobID": job_id, "incidentID": "inc-j-runner-fail", "pipeline": "basic_rca"}

        class _FakeRuntime:
            instances: list["_FakeRuntime"] = []

            def __init__(self, **kwargs: object) -> None:
                self.finalize_calls: list[dict[str, object]] = []
                self.shutdown_calls = 0
                _FakeRuntime.instances.append(self)

            @staticmethod
            def start() -> bool:
                return True

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

        class _UnexpectedGraph:
            def invoke(self, _state: object) -> dict[str, object]:
                graph_invoked["count"] += 1
                return {"job_id": "job-unexpected", "started": True}

        def _unexpected_builder(*_: object) -> _UnexpectedGraph:
            return _UnexpectedGraph()

        with mock.patch.object(runner_module, "_new_client", return_value=_FakeClient()), mock.patch.object(
            runner_module, "OrchestratorRuntime", _FakeRuntime
        ), mock.patch.object(runner_module, "get_template_builder", return_value=_unexpected_builder):
            runner_module._invoke_graph(
                self._settings(toolset_config_json),
                OrchestratorConfig(run_query=True, post_finalize_observe=False, run_verification=False),
                "job-j-runner-fail",
                debug=False,
            )

        runtime = _FakeRuntime.instances[-1]
        self.assertEqual(graph_invoked["count"], 0)
        self.assertEqual(runtime.shutdown_calls, 1)
        self.assertEqual(len(runtime.finalize_calls), 1)
        self.assertEqual(runtime.finalize_calls[0]["status"], "failed")
        self.assertIn("toolset_selection_failed:", str(runtime.finalize_calls[0]["error_message"]))
        self.assertIn(expected_error, str(runtime.finalize_calls[0]["error_message"]))


class RuntimeChainObservationTest(unittest.TestCase):
    def test_runtime_tool_invoke_observation_contains_resolved_from_toolset_id(self) -> None:
        module_one = "_phasej_runtime_mod_one"
        module_two = "_phasej_runtime_mod_two"
        mod_one = types.ModuleType(module_one)
        mod_two = types.ModuleType(module_two)
        mod_one.call = lambda *_args, **_kwargs: {"output": {"from": "one"}}  # type: ignore[attr-defined]
        mod_two.call = (
            lambda tool, payload, idempotency_key=None: {  # type: ignore[assignment]
                "output": {"from": "two", "tool": tool, "payload": payload, "idempotency_key": idempotency_key}
            }
        )
        sys.modules[module_one] = mod_one
        sys.modules[module_two] = mod_two

        class _FakeSession:
            def __init__(self) -> None:
                self.headers: dict[str, str] = {}

        class _FakeMCPClient:
            @staticmethod
            def call(**_: object) -> dict[str, object]:
                raise AssertionError("mcp client should not be called when chain invoker is set")

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

        try:
            config = load_toolset_config(
                {
                    "pipelines": {"basic_rca": ["ts_1", "ts_2"]},
                    "toolsets": {
                        "ts_1": {
                            "providers": [
                                {"type": "skills", "module": module_one, "allow_tools": ["query_metrics"]},
                            ]
                        },
                        "ts_2": {
                            "providers": [
                                {"type": "skills", "module": module_two, "allow_tools": ["query_logs"]},
                            ]
                        },
                    },
                }
            )
            invoker = build_tool_invoker_chain(config, ["ts_1", "ts_2"])

            client = _FakeClient()
            runtime = OrchestratorRuntime(
                client=client,
                job_id="job-j-runtime",
                instance_id="orc-j-runtime",
                heartbeat_interval_seconds=60,
                retry_policy=RetryPolicy(max_attempts=1, base_delay_seconds=0.0, max_delay_seconds=0.0),
                tool_invoker=invoker,
            )
            self.assertTrue(runtime.start())
            result = runtime.call_tool("mcp.query_logs", {"query": "error"}, idempotency_key="idem-j")
            runtime.shutdown()
        finally:
            del sys.modules[module_one]
            del sys.modules[module_two]

        self.assertEqual(result["output"]["from"], "two")
        self.assertEqual(len(client.tool_calls), 1)
        call = client.tool_calls[0]
        self.assertEqual(call["tool_name"], "tool.invoke")
        self.assertEqual(call["response_json"]["resolved_from_toolset_id"], "ts_2")
        self.assertEqual(call["response_json"]["toolset_chain"], ["ts_1", "ts_2"])
        self.assertEqual(call["response_json"]["route_policy"], "first_match")


if __name__ == "__main__":
    unittest.main()
