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


class StrategyResolvePhaseL1RunnerTest(unittest.TestCase):
    @staticmethod
    def _settings() -> Settings:
        return Settings(
            base_url="http://127.0.0.1:5555",
            scopes="*",
            mcp_scopes="*",
            mcp_verify_remote_tools=False,
            instance_id="orc-l1",
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

    @staticmethod
    def _graph_cfg() -> OrchestratorConfig:
        return OrchestratorConfig(
            run_query=True,
            force_no_evidence=False,
            force_conflict=False,
            ds_base_url="",
            auto_create_datasource=True,
            a3_max_calls=6,
            a3_max_total_bytes=2 * 1024 * 1024,
            a3_max_total_latency_ms=8000,
            post_finalize_observe=False,
            run_verification=False,
            verification_source="ai_job",
            post_finalize_wait_timeout_seconds=8,
            post_finalize_wait_interval_ms=500,
            post_finalize_wait_max_interval_ms=2000,
        )

    def test_strategy_template_id_drives_builder_selection(self) -> None:
        module_name = "_phasel1_builder_mod"
        fake_module = types.ModuleType(module_name)

        def _skill_call(tool: str, payload: dict[str, object], idempotency_key: str | None = None) -> dict[str, object]:
            return {"ok": True, "tool": tool, "payload": payload, "idempotency_key": idempotency_key}

        fake_module.call = _skill_call  # type: ignore[attr-defined]
        sys.modules[module_name] = fake_module

        class _FakeClient:
            @staticmethod
            def get_job(job_id: str) -> dict[str, str]:
                return {"jobID": job_id, "incidentID": "inc-l1", "pipeline": "custom_pipeline"}

            @staticmethod
            def resolve_strategy(pipeline: str) -> dict[str, object]:
                if pipeline != "custom_pipeline":
                    raise AssertionError(f"unexpected pipeline={pipeline}")
                return {
                    "pipeline": "custom_pipeline",
                    "templateID": "basic_rca",
                    "toolsets": [
                        {
                            "toolsetID": "default",
                            "providers": [
                                {
                                    "type": "skills",
                                    "module": module_name,
                                    "allowTools": ["query_logs"],
                                }
                            ],
                        }
                    ],
                }

        class _FakeRuntime:
            instances: list["_FakeRuntime"] = []

            def __init__(self, **kwargs: object) -> None:
                self.tool_invoker = kwargs.get("tool_invoker")
                self.start_calls = 0
                self.shutdown_calls = 0
                self.finalize_calls: list[dict[str, object]] = []
                _FakeRuntime.instances.append(self)

            def start(self) -> bool:
                self.start_calls += 1
                return True

            def finalize(
                self,
                status: str,
                diagnosis_json: dict[str, object] | None,
                error_message: str | None,
                evidence_ids: list[str] | None,
            ) -> None:
                self.finalize_calls.append(
                    {
                        "status": status,
                        "diagnosis_json": diagnosis_json,
                        "error_message": error_message,
                        "evidence_ids": list(evidence_ids or []),
                    }
                )

            def shutdown(self) -> None:
                self.shutdown_calls += 1

        class _FakeCompiledGraph:
            def invoke(self, _state: object) -> dict[str, object]:
                return {"job_id": "job-l1-template", "started": True, "finalized": True}

        selected_template_ids: list[str] = []

        def _fake_get_template_builder(template_id: str):
            selected_template_ids.append(template_id)

            def _builder(_runtime: _FakeRuntime, _cfg: OrchestratorConfig) -> _FakeCompiledGraph:
                return _FakeCompiledGraph()

            return _builder

        try:
            with mock.patch.object(runner_module, "_new_client", return_value=_FakeClient()), mock.patch.object(
                runner_module, "OrchestratorRuntime", _FakeRuntime
            ), mock.patch.object(runner_module, "get_template_builder", side_effect=_fake_get_template_builder):
                runner_module._invoke_graph(
                    self._settings(),
                    self._graph_cfg(),
                    "job-l1-template",
                    debug=False,
                )
        finally:
            sys.modules.pop(module_name, None)

        self.assertEqual(selected_template_ids, ["basic_rca"])
        runtime = _FakeRuntime.instances[0]
        self.assertEqual(runtime.start_calls, 1)
        self.assertEqual(runtime.shutdown_calls, 1)
        self.assertEqual(runtime.finalize_calls, [])

    def test_strategy_toolsets_chain_routes_to_second_toolset(self) -> None:
        module_one = "_phasel1_chain_mod_one"
        module_two = "_phasel1_chain_mod_two"
        called_tools: list[str] = []

        fake_mod_one = types.ModuleType(module_one)
        fake_mod_two = types.ModuleType(module_two)

        def _call_one(tool: str, payload: dict[str, object], idempotency_key: str | None = None) -> dict[str, object]:
            called_tools.append(f"one:{tool}")
            return {"source": "one", "tool": tool, "payload": payload, "idempotency_key": idempotency_key}

        def _call_two(tool: str, payload: dict[str, object], idempotency_key: str | None = None) -> dict[str, object]:
            called_tools.append(f"two:{tool}")
            return {"source": "two", "tool": tool, "payload": payload, "idempotency_key": idempotency_key}

        fake_mod_one.call = _call_one  # type: ignore[attr-defined]
        fake_mod_two.call = _call_two  # type: ignore[attr-defined]
        sys.modules[module_one] = fake_mod_one
        sys.modules[module_two] = fake_mod_two

        class _FakeClient:
            @staticmethod
            def get_job(job_id: str) -> dict[str, str]:
                return {"jobID": job_id, "incidentID": "inc-l1-chain", "pipeline": "basic_rca"}

            @staticmethod
            def resolve_strategy(pipeline: str) -> dict[str, object]:
                if pipeline != "basic_rca":
                    raise AssertionError(f"unexpected pipeline={pipeline}")
                return {
                    "pipeline": "basic_rca",
                    "templateID": "basic_rca",
                    "toolsets": [
                        {
                            "toolsetID": "logs",
                            "providers": [
                                {
                                    "type": "skills",
                                    "module": module_one,
                                    "allowTools": ["query_logs"],
                                }
                            ],
                        },
                        {
                            "toolsetID": "metrics",
                            "providers": [
                                {
                                    "type": "skills",
                                    "module": module_two,
                                    "allowTools": ["query_metrics"],
                                }
                            ],
                        },
                    ],
                }

        class _FakeRuntime:
            instances: list["_FakeRuntime"] = []

            def __init__(self, **kwargs: object) -> None:
                self.tool_invoker = kwargs.get("tool_invoker")
                self.start_calls = 0
                self.shutdown_calls = 0
                self.finalize_calls: list[dict[str, object]] = []
                self.last_tool_result: dict[str, object] | None = None
                _FakeRuntime.instances.append(self)

            def start(self) -> bool:
                self.start_calls += 1
                return True

            def call_tool(
                self,
                tool: str,
                params: dict[str, object],
                idempotency_key: str | None = None,
            ) -> dict[str, object]:
                if self.tool_invoker is None:
                    raise RuntimeError("missing tool invoker")
                return self.tool_invoker.call(
                    tool=tool,
                    input_payload=params,
                    idempotency_key=idempotency_key,
                )

            def finalize(
                self,
                status: str,
                diagnosis_json: dict[str, object] | None,
                error_message: str | None,
                evidence_ids: list[str] | None,
            ) -> None:
                self.finalize_calls.append(
                    {
                        "status": status,
                        "diagnosis_json": diagnosis_json,
                        "error_message": error_message,
                        "evidence_ids": list(evidence_ids or []),
                    }
                )

            def shutdown(self) -> None:
                self.shutdown_calls += 1

        class _FakeCompiledGraph:
            def __init__(self, runtime: _FakeRuntime) -> None:
                self._runtime = runtime

            def invoke(self, _state: object) -> dict[str, object]:
                self._runtime.last_tool_result = self._runtime.call_tool(
                    "query_metrics",
                    {"window": "5m"},
                    idempotency_key="idem-l1-chain",
                )
                return {"job_id": "job-l1-chain", "started": True, "finalized": True}

        def _fake_builder(runtime: _FakeRuntime, _cfg: OrchestratorConfig) -> _FakeCompiledGraph:
            return _FakeCompiledGraph(runtime)

        try:
            with mock.patch.object(runner_module, "_new_client", return_value=_FakeClient()), mock.patch.object(
                runner_module, "OrchestratorRuntime", _FakeRuntime
            ), mock.patch.object(runner_module, "get_template_builder", return_value=_fake_builder):
                runner_module._invoke_graph(
                    self._settings(),
                    self._graph_cfg(),
                    "job-l1-chain",
                    debug=False,
                )
        finally:
            sys.modules.pop(module_one, None)
            sys.modules.pop(module_two, None)

        runtime = _FakeRuntime.instances[0]
        self.assertEqual(runtime.start_calls, 1)
        self.assertEqual(runtime.shutdown_calls, 1)
        self.assertEqual(runtime.finalize_calls, [])
        self.assertEqual(called_tools, ["two:query_metrics"])
        self.assertIsNotNone(runtime.last_tool_result)
        meta = runtime.last_tool_result.get("_tooling_meta", {}) if runtime.last_tool_result else {}
        self.assertEqual(meta.get("resolved_from_toolset_id"), "metrics")

    def test_strategy_template_missing_or_unregistered_fail_fast(self) -> None:
        class _FakeClient:
            @staticmethod
            def get_job(job_id: str) -> dict[str, str]:
                return {"jobID": job_id, "incidentID": "inc-l1-fail", "pipeline": "basic_rca"}

            @staticmethod
            def resolve_strategy(_pipeline: str) -> dict[str, object]:
                return {
                    "pipeline": "basic_rca",
                    "templateID": "missing_template",
                    "toolsets": [
                        {
                            "toolsetID": "default",
                            "providers": [
                                {
                                    "type": "skills",
                                    "module": "pkg.skills.demo",
                                    "allowTools": ["query_logs"],
                                }
                            ],
                        }
                    ],
                }

        class _FakeRuntime:
            instances: list["_FakeRuntime"] = []

            def __init__(self, **kwargs: object) -> None:
                self.start_calls = 0
                self.shutdown_calls = 0
                self.finalize_calls: list[dict[str, object]] = []
                _FakeRuntime.instances.append(self)

            def start(self) -> bool:
                self.start_calls += 1
                return True

            def finalize(
                self,
                status: str,
                diagnosis_json: dict[str, object] | None,
                error_message: str | None,
                evidence_ids: list[str] | None,
            ) -> None:
                self.finalize_calls.append(
                    {
                        "status": status,
                        "diagnosis_json": diagnosis_json,
                        "error_message": error_message,
                        "evidence_ids": list(evidence_ids or []),
                    }
                )

            def shutdown(self) -> None:
                self.shutdown_calls += 1

        with mock.patch.object(runner_module, "_new_client", return_value=_FakeClient()), mock.patch.object(
            runner_module, "OrchestratorRuntime", _FakeRuntime
        ):
            runner_module._invoke_graph(
                self._settings(),
                self._graph_cfg(),
                "job-l1-fail",
                debug=False,
            )

        runtime = _FakeRuntime.instances[0]
        self.assertEqual(runtime.start_calls, 1)
        self.assertEqual(runtime.shutdown_calls, 1)
        self.assertEqual(len(runtime.finalize_calls), 1)
        self.assertEqual(runtime.finalize_calls[0]["status"], "failed")
        self.assertIn("template_selection_failed:", str(runtime.finalize_calls[0]["error_message"]))

    def test_toolset_select_observation_contains_template_id(self) -> None:
        module_name = "_phasel1_obs_mod"
        fake_module = types.ModuleType(module_name)

        def _skill_call(tool: str, payload: dict[str, object], idempotency_key: str | None = None) -> dict[str, object]:
            return {"tool": tool, "payload": payload, "idempotency_key": idempotency_key}

        fake_module.call = _skill_call  # type: ignore[attr-defined]
        sys.modules[module_name] = fake_module

        class _FakeClient:
            @staticmethod
            def get_job(job_id: str) -> dict[str, str]:
                return {"jobID": job_id, "incidentID": "inc-l1-obs", "pipeline": "basic_rca"}

            @staticmethod
            def resolve_strategy(_pipeline: str) -> dict[str, object]:
                return {
                    "pipeline": "basic_rca",
                    "templateID": "basic_rca",
                    "toolsets": [
                        {
                            "toolsetID": "default",
                            "providers": [
                                {
                                    "type": "skills",
                                    "module": module_name,
                                    "allowTools": ["query_logs"],
                                }
                            ],
                        }
                    ],
                }

        class _FakeRuntime:
            instances: list["_FakeRuntime"] = []

            def __init__(self, **kwargs: object) -> None:
                self.tool_invoker = kwargs.get("tool_invoker")
                self.start_calls = 0
                self.shutdown_calls = 0
                self.finalize_calls: list[dict[str, object]] = []
                self.observations: list[dict[str, object]] = []
                _FakeRuntime.instances.append(self)

            def start(self) -> bool:
                self.start_calls += 1
                return True

            def report_observation(
                self,
                *,
                tool: str,
                node_name: str,
                params: dict[str, object],
                response: dict[str, object],
                evidence_ids: list[str] | None,
            ) -> None:
                self.observations.append(
                    {
                        "tool": tool,
                        "node_name": node_name,
                        "params": params,
                        "response": response,
                        "evidence_ids": list(evidence_ids or []),
                    }
                )

            def finalize(
                self,
                status: str,
                diagnosis_json: dict[str, object] | None,
                error_message: str | None,
                evidence_ids: list[str] | None,
            ) -> None:
                self.finalize_calls.append(
                    {
                        "status": status,
                        "diagnosis_json": diagnosis_json,
                        "error_message": error_message,
                        "evidence_ids": list(evidence_ids or []),
                    }
                )

            def shutdown(self) -> None:
                self.shutdown_calls += 1

        class _FakeCompiledGraph:
            def invoke(self, _state: object) -> dict[str, object]:
                return {"job_id": "job-l1-obs", "started": True, "finalized": True}

        def _fake_builder(_runtime: _FakeRuntime, _cfg: OrchestratorConfig) -> _FakeCompiledGraph:
            return _FakeCompiledGraph()

        try:
            with mock.patch.object(runner_module, "_new_client", return_value=_FakeClient()), mock.patch.object(
                runner_module, "OrchestratorRuntime", _FakeRuntime
            ), mock.patch.object(runner_module, "get_template_builder", return_value=_fake_builder):
                runner_module._invoke_graph(
                    self._settings(),
                    self._graph_cfg(),
                    "job-l1-obs",
                    debug=False,
                )
        finally:
            sys.modules.pop(module_name, None)

        runtime = _FakeRuntime.instances[0]
        self.assertGreaterEqual(len(runtime.observations), 1)
        observed = runtime.observations[0]
        self.assertEqual(observed["tool"], "toolset.select")
        response = observed["response"]
        self.assertEqual(response.get("template_id"), "basic_rca")
        self.assertEqual(response.get("toolsets"), ["default"])

    def test_strategy_resolve_failure_is_fail_fast(self) -> None:
        class _FakeClient:
            @staticmethod
            def get_job(job_id: str) -> dict[str, str]:
                return {"jobID": job_id, "incidentID": "inc-l1-resolve-fail", "pipeline": "basic_rca"}

            @staticmethod
            def resolve_strategy(_pipeline: str) -> dict[str, object]:
                raise RuntimeError("template not registered")

        class _FakeRuntime:
            instances: list["_FakeRuntime"] = []

            def __init__(self, **kwargs: object) -> None:
                self.start_calls = 0
                self.shutdown_calls = 0
                self.finalize_calls: list[dict[str, object]] = []
                _FakeRuntime.instances.append(self)

            def start(self) -> bool:
                self.start_calls += 1
                return True

            def finalize(
                self,
                status: str,
                diagnosis_json: dict[str, object] | None,
                error_message: str | None,
                evidence_ids: list[str] | None,
            ) -> None:
                self.finalize_calls.append(
                    {
                        "status": status,
                        "diagnosis_json": diagnosis_json,
                        "error_message": error_message,
                        "evidence_ids": list(evidence_ids or []),
                    }
                )

            def shutdown(self) -> None:
                self.shutdown_calls += 1

        with mock.patch.object(runner_module, "_new_client", return_value=_FakeClient()), mock.patch.object(
            runner_module, "OrchestratorRuntime", _FakeRuntime
        ):
            runner_module._invoke_graph(
                self._settings(),
                self._graph_cfg(),
                "job-l1-resolve-fail",
                debug=False,
            )

        runtime = _FakeRuntime.instances[0]
        self.assertEqual(runtime.start_calls, 1)
        self.assertEqual(runtime.shutdown_calls, 1)
        self.assertEqual(len(runtime.finalize_calls), 1)
        self.assertEqual(runtime.finalize_calls[0]["status"], "failed")
        self.assertIn("strategy_selection_failed:", str(runtime.finalize_calls[0]["error_message"]))


if __name__ == "__main__":
    unittest.main()
