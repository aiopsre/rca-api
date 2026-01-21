from __future__ import annotations

import json
import pathlib
import sys
import unittest
from typing import Any

import requests


TESTS_DIR = pathlib.Path(__file__).resolve().parent
PROJECT_DIR = TESTS_DIR.parent
if str(PROJECT_DIR) not in sys.path:
    sys.path.insert(0, str(PROJECT_DIR))

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


if __name__ == "__main__":
    unittest.main()
