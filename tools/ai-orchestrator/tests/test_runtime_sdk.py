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
from orchestrator.runtime.retry import RetryExecutor, RetryPolicy
from orchestrator.runtime.runtime import OrchestratorRuntime
from orchestrator.runtime.toolcall_reporter import ToolCallReporter
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


if __name__ == "__main__":
    unittest.main()
