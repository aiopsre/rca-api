from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor
import json
import pathlib
import sys
import threading
import unittest
from typing import Any

import requests


TESTS_DIR = pathlib.Path(__file__).resolve().parent
PROJECT_DIR = TESTS_DIR.parent
if str(PROJECT_DIR) not in sys.path:
    sys.path.insert(0, str(PROJECT_DIR))

from orchestrator.runtime.evidence_publisher import EvidencePublisher
from orchestrator.runtime.post_finalize import PostFinalizeObserver
from orchestrator.runtime.retry import RetryExecutor, RetryPolicy
from orchestrator.runtime.runtime import OrchestratorRuntime
from orchestrator.runtime.toolcall_reporter import ToolCallReporter
from orchestrator.runtime.verification_runner import VerificationBudget, VerificationRunner
from orchestrator.sdk.errors import OrchestratorErrorCategory, RCAApiError
from orchestrator.tools_rca_api import RCAApiClient


class _FakeResponse:
    def __init__(self, status_code: int, payload: dict[str, Any] | None = None, body_text: str | None = None) -> None:
        self.status_code = int(status_code)
        self._payload = payload
        if body_text is not None:
            self.text = body_text
        elif payload is None:
            self.text = ""
        else:
            self.text = json.dumps(payload, ensure_ascii=False)

    @property
    def ok(self) -> bool:
        return 200 <= self.status_code < 300

    def json(self) -> Any:
        if self._payload is not None:
            return self._payload
        return json.loads(self.text)


class RCAApiClientRequestTest(unittest.TestCase):
    def test_request_raises_rca_api_error_for_business_error_envelope(self) -> None:
        client = RCAApiClient("http://example.com", scopes="*")
        client.session.request = lambda **_: _FakeResponse(
            200,
            payload={"code": 4001001, "message": "invalid argument", "details": {"field": "job_id"}},
        )

        with self.assertRaises(RCAApiError) as ctx:
            client._request("GET", "/v1/ai/jobs")
        self.assertEqual(ctx.exception.category, OrchestratorErrorCategory.BAD_REQUEST)
        self.assertEqual(ctx.exception.http_status, 200)

    def test_request_raises_retryable_transport_for_requests_exception(self) -> None:
        client = RCAApiClient("http://example.com", scopes="*")

        def _raise_request_exception(**_: Any) -> _FakeResponse:
            raise requests.RequestException("network down")

        client.session.request = _raise_request_exception
        with self.assertRaises(RCAApiError) as ctx:
            client._request("POST", "/v1/ai/jobs/job-1/heartbeat")
        self.assertEqual(ctx.exception.category, OrchestratorErrorCategory.RETRYABLE_TRANSPORT)

    def test_request_classifies_ai_job_conflict_as_owner_lost(self) -> None:
        client = RCAApiClient("http://example.com", scopes="*")
        client.session.request = lambda **_: _FakeResponse(
            409,
            payload={
                "code": "Conflict.AIJobInvalidTransition",
                "message": "AI job status transition is not allowed.",
            },
        )
        with self.assertRaises(RCAApiError) as ctx:
            client._request("POST", "/v1/ai/jobs/job-1/heartbeat")
        self.assertEqual(ctx.exception.category, OrchestratorErrorCategory.OWNER_LOST)

    def test_request_classifies_missing_owner_from_invalid_argument_on_lease_endpoint(self) -> None:
        client = RCAApiClient("http://example.com", scopes="*")
        client.session.request = lambda **_: _FakeResponse(
            400,
            payload={"reason": "BadRequest.InvalidArgument", "message": "invalid argument"},
        )
        with self.assertRaises(RCAApiError) as ctx:
            client._request("POST", "/v1/ai/jobs/job-1/start")
        self.assertEqual(ctx.exception.category, OrchestratorErrorCategory.MISSING_OWNER)

    def test_list_tool_calls_uses_expected_endpoint_and_params(self) -> None:
        client = RCAApiClient("http://example.com", scopes="*")
        captured: dict[str, Any] = {}

        def _fake_request(method: str, path: str, **kwargs: Any) -> dict[str, Any]:
            captured["method"] = method
            captured["path"] = path
            captured["kwargs"] = kwargs
            return {"data": {"totalCount": 1, "toolCalls": [{"seq": 2}]}}

        client._request = _fake_request  # type: ignore[method-assign]
        response = client.list_tool_calls("job-abc", limit=20, offset=3, seq=2)
        self.assertEqual(captured["method"], "GET")
        self.assertEqual(captured["path"], "/v1/ai/jobs/job-abc/tool-calls")
        self.assertEqual(captured["kwargs"]["params"], {"limit": 20, "offset": 3, "seq": 2})
        self.assertEqual(response["totalCount"], 1)

    def test_create_incident_verification_run_uses_expected_payload(self) -> None:
        client = RCAApiClient("http://example.com", scopes="*")
        captured: dict[str, Any] = {}

        def _fake_request(method: str, path: str, **kwargs: Any) -> dict[str, Any]:
            captured["method"] = method
            captured["path"] = path
            captured["kwargs"] = kwargs
            return {"data": {"run": {"runID": "run-1"}}}

        client._request = _fake_request  # type: ignore[method-assign]
        response = client.create_incident_verification_run(
            incident_id="inc-9",
            source="ai_job",
            step_index=1,
            tool="mcp.query_logs",
            params_json={"query": "error"},
            observed="keyword matched",
            meets_expectation=True,
            actor="ai:job-9",
        )
        self.assertEqual(captured["method"], "POST")
        self.assertEqual(captured["path"], "/v1/incidents/inc-9/verification-runs")
        body = captured["kwargs"]["json_body"]
        self.assertEqual(body["incidentID"], "inc-9")
        self.assertEqual(body["source"], "ai_job")
        self.assertEqual(body["stepIndex"], 1)
        self.assertEqual(body["tool"], "mcp.query_logs")
        self.assertEqual(body["observed"], "keyword matched")
        self.assertTrue(body["meetsExpectation"])
        self.assertEqual(body["actor"], "ai:job-9")
        self.assertEqual(body["paramsJSON"], '{"query":"error"}')
        self.assertEqual(response["run"]["runID"], "run-1")

    def test_list_incident_verification_runs_uses_expected_endpoint_and_params(self) -> None:
        client = RCAApiClient("http://example.com", scopes="*")
        captured: dict[str, Any] = {}

        def _fake_request(method: str, path: str, **kwargs: Any) -> dict[str, Any]:
            captured["method"] = method
            captured["path"] = path
            captured["kwargs"] = kwargs
            return {"data": {"totalCount": 1, "runs": [{"stepIndex": 2}]}}

        client._request = _fake_request  # type: ignore[method-assign]
        response = client.list_incident_verification_runs("inc-9", page=3, limit=50)
        self.assertEqual(captured["method"], "GET")
        self.assertEqual(captured["path"], "/v1/incidents/inc-9/verification-runs")
        self.assertEqual(captured["kwargs"]["params"], {"page": 3, "limit": 50})
        self.assertEqual(response["totalCount"], 1)


class ToolCallReporterTest(unittest.TestCase):
    def test_reporter_assigns_monotonic_seq_per_job(self) -> None:
        calls: list[dict[str, Any]] = []

        class _FakeClient:
            def add_tool_call(self, **kwargs: Any) -> None:
                calls.append(kwargs)

        reporter = ToolCallReporter(_FakeClient(), "job-123")

        for _ in range(3):
            reporter.report(
                node_name="collect_evidence",
                tool_name="mcp.query_metrics",
                request_json={"incident_id": "inc-1"},
                response_json={"status": "ok"},
                latency_ms=5,
                status="ok",
                error=None,
            )

        self.assertEqual([item["seq"] for item in calls], [1, 2, 3])
        self.assertEqual({item["job_id"] for item in calls}, {"job-123"})

    def test_reporter_concurrency_has_unique_monotonic_seq(self) -> None:
        calls: list[dict[str, Any]] = []
        calls_lock = threading.Lock()

        class _FakeClient:
            def add_tool_call(self, **kwargs: Any) -> None:
                with calls_lock:
                    calls.append(kwargs)

        reporter = ToolCallReporter(_FakeClient(), "job-456")
        total = 50

        def _task() -> int:
            return reporter.report(
                node_name="collect_evidence",
                tool_name="mcp.query_logs",
                request_json={"incident_id": "inc-1"},
                response_json={"status": "ok"},
                latency_ms=7,
                status="ok",
                error=None,
                evidence_ids=["evidence-1"],
            )

        with ThreadPoolExecutor(max_workers=8) as pool:
            seqs = list(pool.map(lambda _: _task(), range(total)))

        self.assertEqual(len(seqs), total)
        self.assertEqual(sorted(seqs), list(range(1, total + 1)))
        self.assertEqual(sorted(item["seq"] for item in calls), list(range(1, total + 1)))
        self.assertTrue(all(item["evidence_ids"] == ["evidence-1"] for item in calls))


class RetryExecutorTest(unittest.TestCase):
    def test_retry_executor_retries_retryable_errors_with_backoff(self) -> None:
        attempts = {"count": 0}
        sleeps: list[float] = []

        def _fn() -> str:
            attempts["count"] += 1
            if attempts["count"] < 3:
                raise RCAApiError(
                    category=OrchestratorErrorCategory.RETRYABLE_TRANSPORT,
                    message="transport",
                    method="POST",
                    path="/v1/ai/jobs/job-1/tool-calls",
                )
            return "ok"

        executor = RetryExecutor(
            policy=RetryPolicy(max_attempts=4, base_delay_seconds=0.01, max_delay_seconds=0.05),
            sleep_func=lambda delay: sleeps.append(delay),
        )
        result = executor.run("toolcall.report", _fn)
        self.assertEqual(result, "ok")
        self.assertEqual(attempts["count"], 3)
        self.assertEqual(len(sleeps), 2)
        self.assertGreaterEqual(sleeps[1], sleeps[0])

    def test_retry_executor_fail_fast_for_missing_owner(self) -> None:
        attempts = {"count": 0}
        sleeps: list[float] = []

        def _fn() -> str:
            attempts["count"] += 1
            raise RCAApiError(
                category=OrchestratorErrorCategory.MISSING_OWNER,
                message="missing owner",
                method="POST",
                path="/v1/ai/jobs/job-1/start",
                http_status=400,
            )

        executor = RetryExecutor(
            policy=RetryPolicy(max_attempts=5, base_delay_seconds=0.01, max_delay_seconds=0.05),
            sleep_func=lambda delay: sleeps.append(delay),
        )
        with self.assertRaises(RCAApiError):
            executor.run("job.start", _fn)
        self.assertEqual(attempts["count"], 1)
        self.assertEqual(sleeps, [])


class RuntimeToolCallRetryTest(unittest.TestCase):
    def test_runtime_retries_toolcall_with_same_seq(self) -> None:
        client = RCAApiClient("http://example.com", scopes="*", instance_id="orc-1")
        attempts: list[int] = []

        def _flaky_add_tool_call(**kwargs: Any) -> None:
            attempts.append(int(kwargs["seq"]))
            if len(attempts) == 1:
                raise RCAApiError(
                    category=OrchestratorErrorCategory.RETRYABLE_TRANSPORT,
                    message="transport",
                    method="POST",
                    path="/v1/ai/jobs/job-rt/tool-calls",
                )

        client.add_tool_call = _flaky_add_tool_call
        runtime = OrchestratorRuntime(
            client=client,
            job_id="job-rt",
            instance_id="orc-1",
            heartbeat_interval_seconds=10,
            retry_policy=RetryPolicy(max_attempts=2, base_delay_seconds=0.0, max_delay_seconds=0.0),
        )
        seq = runtime.report_tool_call(
            node_name="collect_evidence",
            tool_name="mcp.query_metrics",
            request_json={"incident_id": "inc-1"},
            response_json={"status": "ok"},
            latency_ms=5,
            status="ok",
        )

        self.assertEqual(seq, 1)
        self.assertEqual(attempts, [1, 1])


class EvidencePublisherTest(unittest.TestCase):
    def test_publisher_builds_stable_idempotency_and_sets_job_metadata(self) -> None:
        saves: list[dict[str, Any]] = []

        class _FakeClient:
            def save_mock_evidence(self, **kwargs: Any) -> str:
                saves.append({"kind": "mock", **kwargs})
                return "evidence-mock-1"

            def save_evidence_from_query(self, **kwargs: Any) -> str:
                saves.append({"kind": "query", **kwargs})
                return "evidence-query-1"

        operations: list[str] = []

        def _execute_with_retry(operation: str, fn: Any) -> str:
            operations.append(operation)
            return fn()

        publisher = EvidencePublisher(
            client=_FakeClient(),
            job_id="job-789",
            execute_with_retry=_execute_with_retry,
        )

        mock_1 = publisher.save_mock_evidence(
            incident_id="inc-1",
            node_name="collect_evidence",
            kind="metrics",
            summary="mock",
            raw={"source": "orchestrator"},
            query_hash_source={"queryText": "mock://orchestrator"},
        )
        mock_2 = publisher.save_mock_evidence(
            incident_id="inc-1",
            node_name="collect_evidence",
            kind="metrics",
            summary="mock",
            raw={"source": "orchestrator"},
            query_hash_source={"queryText": "mock://orchestrator"},
        )
        query = publisher.save_evidence_from_query(
            incident_id="inc-1",
            node_name="collect_evidence",
            kind="logs",
            query={"queryText": "{app=\"demo\"}"},
            result={"queryResultJSON": "[]"},
            query_hash_source={"queryText": "{app=\"demo\"}"},
        )

        self.assertEqual(mock_1.idempotency_key, mock_2.idempotency_key)
        self.assertEqual(mock_1.created_by, "ai:job-789")
        self.assertEqual(query.created_by, "ai:job-789")
        self.assertEqual(operations, ["evidence.save_mock", "evidence.save_mock", "evidence.save_query"])
        self.assertEqual(saves[0]["job_id"], "job-789")
        self.assertEqual(saves[0]["created_by"], "ai:job-789")
        self.assertEqual(saves[2]["job_id"], "job-789")
        self.assertEqual(saves[2]["created_by"], "ai:job-789")


class PostFinalizeObserverTest(unittest.TestCase):
    def test_observer_extracts_verification_plan_and_kb_refs(self) -> None:
        class _FakeClient:
            def get_incident(self, incident_id: str) -> dict[str, Any]:
                self.incident_id = incident_id
                return {
                    "incidentID": incident_id,
                    "diagnosisJSON": json.dumps(
                        {"verification_plan": {"version": "a5", "steps": [{"tool": "mcp.query_metrics"}]}},
                        ensure_ascii=False,
                    ),
                }

            def list_tool_calls(self, job_id: str, *, limit: int = 200, offset: int = 0) -> dict[str, Any]:
                self.job_id = job_id
                self.calls = getattr(self, "calls", [])
                self.calls.append({"limit": limit, "offset": offset})
                if offset == 0:
                    return {
                        "totalCount": 2,
                        "toolCalls": [
                            {
                                "seq": 2,
                                "responseJSON": '{"kb_refs":[{"doc_id":"kb-1","patterns":[{"type":"keyword","value":"error"}]}]}',
                            },
                            {"seq": 1, "responseJSON": '{"status":"ok"}'},
                        ],
                    }
                return {"totalCount": 2, "toolCalls": []}

        operations: list[str] = []

        def _execute_with_retry(operation: str, fn: Any) -> Any:
            operations.append(operation)
            return fn()

        observer = PostFinalizeObserver(
            client=_FakeClient(),
            execute_with_retry=_execute_with_retry,
            log_func=None,
        )
        snapshot = observer.observe(incident_id="inc-77", job_id="job-77")
        self.assertEqual(operations, ["post_finalize.get_incident", "post_finalize.list_tool_calls"])
        self.assertIsInstance(snapshot.verification_plan, dict)
        self.assertEqual(snapshot.target_toolcall_seq, 2)
        self.assertEqual(len(snapshot.kb_refs), 1)
        self.assertEqual(snapshot.kb_refs[0]["doc_id"], "kb-1")

    def test_observer_observe_with_wait_retries_until_plan_available(self) -> None:
        class _FakeClient:
            def __init__(self) -> None:
                self.get_count = 0

            def get_incident(self, incident_id: str) -> dict[str, Any]:
                self.get_count += 1
                if self.get_count < 2:
                    return {"incidentID": incident_id, "diagnosisJSON": "{}"}
                return {
                    "incidentID": incident_id,
                    "diagnosisJSON": '{"verification_plan":{"version":"a5","steps":[{"tool":"mcp.query_logs"}]}}',
                }

            def list_tool_calls(self, job_id: str, *, limit: int = 200, offset: int = 0) -> dict[str, Any]:
                return {"totalCount": 0, "toolCalls": []}

        observer = PostFinalizeObserver(
            client=_FakeClient(),
            execute_with_retry=lambda _op, fn: fn(),
            log_func=None,
        )
        snapshot = observer.observe_with_wait(
            incident_id="inc-10",
            job_id="job-10",
            timeout_s=0.1,
            interval_s=0.01,
            max_interval_s=0.02,
        )
        self.assertIsNotNone(snapshot.verification_plan)


class VerificationRunnerTest(unittest.TestCase):
    def test_runner_executes_steps_and_publishes_verification_runs(self) -> None:
        published: list[dict[str, Any]] = []
        called_tools: list[str] = []

        class _FakeMCPClient:
            def call(self, *, tool: str, input_payload: dict[str, Any], idempotency_key: str | None = None) -> dict[str, Any]:
                called_tools.append(tool)
                if tool == "query_metrics":
                    return {"output": {"queryResultJSON": '{"data":{"result":[{"value":[1,"2"]}]}}'}}
                if tool == "query_logs":
                    return {"output": {"queryResultJSON": '{"rows":[{"line":"error timeout"}]}'}}
                return {"output": {"queryResultJSON": '{"data":{"result":[]}}'}}

        class _FakeClient:
            def __init__(self) -> None:
                self.mcp_client = _FakeMCPClient()

            def create_incident_verification_run(self, **kwargs: Any) -> dict[str, Any]:
                published.append(kwargs)
                return {"run": {"runID": f"run-{len(published)}"}}

        operations: list[str] = []

        def _execute_with_retry(operation: str, fn: Any) -> Any:
            operations.append(operation)
            return fn()

        runner = VerificationRunner(
            client=_FakeClient(),
            execute_with_retry=_execute_with_retry,
            log_func=None,
            dedupe_enabled=False,
        )
        plan = {
            "steps": [
                {
                    "tool": "mcp.query_metrics",
                    "params": {"datasource_id": "ds-1", "expr": "sum(up)"},
                    "expected": {"type": "exists"},
                },
                {
                    "tool": "mcp.query_logs",
                    "params": {"datasource_id": "ds-1", "query": "error"},
                    "expected": {"type": "contains_keyword", "keyword": "timeout"},
                },
                {
                    "tool": "mcp.query_metrics",
                    "params": {"datasource_id": "ds-1", "expr": "sum(up)"},
                    "expected": {"type": "threshold_below", "value": 0.1},
                },
            ]
        }

        results = runner.run(
            incident_id="inc-55",
            verification_plan=plan,
            source="ai_job",
            actor="ai:job-55",
        )
        self.assertEqual(called_tools, ["query_metrics", "query_logs", "query_metrics"])
        self.assertEqual([item.meets_expectation for item in results], [True, True, False])
        third_observed = json.loads(results[2].observed)
        self.assertEqual(third_observed["reason"], "threshold_check")
        self.assertFalse(third_observed["matched"])
        self.assertEqual(len(published), 3)
        self.assertEqual([item["step_index"] for item in published], [1, 2, 3])
        self.assertTrue(all(item["source"] == "ai_job" for item in published))
        self.assertTrue(any(op.startswith("verification.publish") for op in operations))
        self.assertTrue(all(len(item["observed"]) <= 512 for item in published))

    def test_runner_dedup_skips_existing_source_step_tool(self) -> None:
        published: list[dict[str, Any]] = []
        called_tools: list[str] = []

        class _FakeMCPClient:
            def call(self, *, tool: str, input_payload: dict[str, Any], idempotency_key: str | None = None) -> dict[str, Any]:
                called_tools.append(tool)
                return {"output": {"queryResultJSON": '{"rows":[{"line":"error"}]}'}}

        class _FakeClient:
            def __init__(self) -> None:
                self.mcp_client = _FakeMCPClient()

            def list_incident_verification_runs(self, incident_id: str, *, page: int = 1, limit: int = 200) -> dict[str, Any]:
                if page == 1:
                    return {
                        "totalCount": 1,
                        "runs": [
                            {"source": "ai_job", "stepIndex": 1, "tool": "mcp.query_logs"},
                        ],
                    }
                return {"totalCount": 1, "runs": []}

            def create_incident_verification_run(self, **kwargs: Any) -> dict[str, Any]:
                published.append(kwargs)
                return {"run": {"runID": f"run-{len(published)}"}}

        runner = VerificationRunner(
            client=_FakeClient(),
            execute_with_retry=lambda _op, fn: fn(),
            log_func=None,
            dedupe_enabled=True,
        )
        plan = {
            "steps": [
                {
                    "tool": "mcp.query_logs",
                    "params": {"query": "error"},
                    "expected": {"type": "contains_keyword", "keyword": "error"},
                },
                {
                    "tool": "mcp.query_logs",
                    "params": {"query": "error"},
                    "expected": {"type": "contains_keyword", "keyword": "error"},
                },
            ]
        }
        results = runner.run(incident_id="inc-66", verification_plan=plan, source="ai_job", actor="ai:job-66")
        self.assertEqual(len(results), 2)
        self.assertEqual(called_tools, ["query_logs"])
        self.assertEqual(len(published), 1)
        self.assertEqual(published[0]["step_index"], 2)
        first_observed = json.loads(results[0].observed)
        self.assertEqual(first_observed["reason"], "deduped_existing_verification_run")

    def test_runner_budget_stops_remaining_steps_and_writes_budget_run(self) -> None:
        published: list[dict[str, Any]] = []

        class _FakeMCPClient:
            def call(self, *, tool: str, input_payload: dict[str, Any], idempotency_key: str | None = None) -> dict[str, Any]:
                return {"output": {"queryResultJSON": '{"data":{"result":[{"value":[1,"2"]}]}}'}}

        class _FakeClient:
            def __init__(self) -> None:
                self.mcp_client = _FakeMCPClient()

            def create_incident_verification_run(self, **kwargs: Any) -> dict[str, Any]:
                published.append(kwargs)
                return {"run": {"runID": f"run-{len(published)}"}, "warnings": ["TRUNCATED_TEXT"]}

        logs: list[str] = []
        runner = VerificationRunner(
            client=_FakeClient(),
            execute_with_retry=lambda _op, fn: fn(),
            log_func=lambda msg: logs.append(msg),
            budget=VerificationBudget(max_steps=1, max_total_latency_ms=0, max_total_bytes=0),
            dedupe_enabled=False,
        )
        plan = {
            "steps": [
                {"tool": "mcp.query_metrics", "params": {"expr": "a"}, "expected": {"type": "exists"}},
                {"tool": "mcp.query_metrics", "params": {"expr": "b"}, "expected": {"type": "exists"}},
                {"tool": "mcp.query_metrics", "params": {"expr": "c"}, "expected": {"type": "exists"}},
            ]
        }
        results = runner.run(incident_id="inc-70", verification_plan=plan, source="ai_job", actor="ai:job-70")
        self.assertEqual(len(results), 1)
        self.assertEqual(len(published), 2)
        self.assertEqual(published[1]["tool"], "verification.budget")
        budget_observed = json.loads(published[1]["observed"])
        self.assertEqual(budget_observed["reason"], "max_steps_reached")
        self.assertTrue(any("verification publish warnings" in line for line in logs))


if __name__ == "__main__":
    unittest.main()
