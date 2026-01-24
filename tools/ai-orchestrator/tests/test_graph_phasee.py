from __future__ import annotations

from dataclasses import dataclass
import pathlib
import sys
import types
import unittest


TESTS_DIR = pathlib.Path(__file__).resolve().parent
PROJECT_DIR = TESTS_DIR.parent
if str(PROJECT_DIR) not in sys.path:
    sys.path.insert(0, str(PROJECT_DIR))

from orchestrator.graph import OrchestratorConfig, build_graph
from orchestrator.state import GraphState


@dataclass(frozen=True)
class _PublishResult:
    evidence_id: str
    idempotency_key: str
    created_by: str


@dataclass(frozen=True)
class _VerificationResult:
    step_index: int
    tool: str
    meets_expectation: bool
    observed: str


class _FakeRuntime:
    def __init__(self) -> None:
        self.tool_calls: list[dict[str, object]] = []
        self.query_metrics_calls = 0
        self.query_logs_calls = 0
        self.finalize_calls: list[dict[str, object]] = []
        self.observe_calls = 0
        self.verification_calls = 0
        self._evidence_counter = 0

    def is_lease_lost(self) -> bool:
        return False

    def lease_lost_reason(self) -> str:
        return ""

    def get_job(self, job_id: str | None = None) -> dict[str, object]:
        return {
            "jobID": job_id or "job-1",
            "incidentID": "inc-1",
            "inputHintsJSON": "{}",
        }

    def get_incident(self, incident_id: str) -> dict[str, object]:
        return {
            "incidentID": incident_id,
            "service": "svc-a",
            "namespace": "default",
            "severity": "critical",
        }

    def ensure_datasource(self, ds_base_url: str) -> str:
        return "ds-1"

    def query_metrics(self, *, datasource_id: str, promql: str, start_ts: int, end_ts: int, step_s: int) -> dict[str, object]:
        self.query_metrics_calls += 1
        return {
            "queryResultJSON": '{"data":{"result":[{"value":[1,"2"]}]}}',
            "resultSizeBytes": 64,
            "rowCount": 1,
            "isTruncated": False,
        }

    def query_logs(self, *, datasource_id: str, query: str, start_ts: int, end_ts: int, limit: int) -> dict[str, object]:
        self.query_logs_calls += 1
        return {
            "queryResultJSON": '{"rows":[{"line":"error timeout"}]}',
            "resultSizeBytes": 72,
            "rowCount": 1,
            "isTruncated": False,
        }

    def report_tool_call(
        self,
        *,
        node_name: str,
        tool_name: str,
        request_json: dict[str, object],
        response_json: dict[str, object] | None,
        latency_ms: int,
        status: str,
        error: str | None = None,
        evidence_ids: list[str] | None = None,
    ) -> int:
        self.tool_calls.append(
            {
                "node_name": node_name,
                "tool_name": tool_name,
                "request_json": request_json,
                "response_json": response_json,
                "status": status,
                "error": error,
                "evidence_ids": evidence_ids or [],
            }
        )
        return len(self.tool_calls)

    def _next_publish_result(self, prefix: str) -> _PublishResult:
        self._evidence_counter += 1
        idx = self._evidence_counter
        return _PublishResult(
            evidence_id=f"{prefix}-{idx}",
            idempotency_key=f"idem-{prefix}-{idx}",
            created_by="ai:job-1",
        )

    def save_mock_evidence(
        self,
        *,
        incident_id: str,
        node_name: str,
        kind: str,
        summary: str,
        raw: dict[str, object],
        query_hash_source: object = None,
    ) -> _PublishResult:
        return self._next_publish_result(f"evidence-{kind}")

    def save_evidence_from_query(
        self,
        *,
        incident_id: str,
        node_name: str,
        kind: str,
        query: dict[str, object],
        result: dict[str, object],
        query_hash_source: object = None,
    ) -> _PublishResult:
        return self._next_publish_result(f"evidence-{kind}")

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

    def observe_post_finalize(
        self,
        *,
        incident_id: str,
        wait_timeout_s: float = 0.0,
        wait_interval_s: float = 0.5,
        wait_max_interval_s: float = 2.0,
    ) -> object:
        self.observe_calls += 1
        return types.SimpleNamespace(
            incident_id=incident_id,
            job_id="job-1",
            verification_plan={
                "version": "a5",
                "steps": [
                    {
                        "tool": "mcp.query_logs",
                        "params": {"datasource_id": "ds-1", "query": "error"},
                        "expected": {"type": "contains_keyword", "keyword": "error"},
                    }
                ],
            },
            kb_refs=[{"doc_id": "kb-1", "patterns": [{"type": "keyword", "value": "error"}]}],
            target_toolcall_seq=12,
        )

    def run_verification(
        self,
        *,
        incident_id: str,
        verification_plan: dict[str, object],
        source: str = "ai_job",
    ) -> list[_VerificationResult]:
        self.verification_calls += 1
        return [
            _VerificationResult(
                step_index=1,
                tool="mcp.query_logs",
                meets_expectation=True,
                observed='{"status":"ok","reason":"contains_keyword_check"}',
            )
        ]


class GraphPhaseETest(unittest.TestCase):
    def _invoke(self, cfg: OrchestratorConfig) -> tuple[GraphState, _FakeRuntime]:
        runtime = _FakeRuntime()
        graph = build_graph(None, cfg, runtime)
        out = graph.invoke(GraphState(job_id="job-1", instance_id="orc-1", started=True))
        if isinstance(out, dict):
            out = GraphState.model_validate(out)
        return out, runtime

    def test_phasee_mock_mode_runs_post_finalize_and_verification_in_graph(self) -> None:
        final_state, runtime = self._invoke(
            OrchestratorConfig(
                run_query=False,
                run_verification=True,
                post_finalize_observe=True,
                verification_source="ai_job",
            )
        )

        self.assertTrue(final_state.finalized)
        self.assertTrue(final_state.verification_done)
        self.assertEqual(runtime.observe_calls, 1)
        self.assertEqual(runtime.verification_calls, 1)

        node_names = {item["node_name"] for item in runtime.tool_calls}
        self.assertIn("query_metrics", node_names)
        self.assertIn("query_logs", node_names)
        self.assertIn("post_finalize_observe", node_names)
        self.assertIn("run_verification", node_names)

        diag_call = next(item for item in runtime.tool_calls if item["tool_name"] == "diagnosis.generate")
        response = diag_call["response_json"]
        self.assertIsInstance(response, dict)
        self.assertIn("quality_gate", response)
        self.assertIn("evidence_plan", response)
        self.assertIn("executed", response["evidence_plan"])

    def test_phasee_query_mode_fanout_merges_two_evidence_and_keeps_executed(self) -> None:
        final_state, runtime = self._invoke(
            OrchestratorConfig(
                run_query=True,
                ds_base_url="http://prometheus:9090",
                auto_create_datasource=True,
                run_verification=False,
                post_finalize_observe=False,
            )
        )

        self.assertTrue(final_state.finalized)
        self.assertGreaterEqual(runtime.query_metrics_calls, 1)
        self.assertGreaterEqual(runtime.query_logs_calls, 1)
        self.assertGreaterEqual(len(final_state.evidence_ids), 2)

        diag_call = next(item for item in runtime.tool_calls if item["tool_name"] == "diagnosis.generate")
        response = diag_call["response_json"]
        self.assertIsInstance(response, dict)
        self.assertIn("evidence_plan", response)
        executed = response["evidence_plan"].get("executed")
        self.assertIsInstance(executed, list)
        self.assertGreaterEqual(len(executed), 1)


if __name__ == "__main__":
    unittest.main()
