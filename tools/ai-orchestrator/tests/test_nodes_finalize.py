from __future__ import annotations

import pathlib
import sys
import unittest
from typing import Any


TESTS_DIR = pathlib.Path(__file__).resolve().parent
PROJECT_DIR = TESTS_DIR.parent
if str(PROJECT_DIR) not in sys.path:
    sys.path.insert(0, str(PROJECT_DIR))

from orchestrator.langgraph.nodes import finalize_job
from orchestrator.state import GraphState


class _FakeRuntime:
    def __init__(self, *, report_error: Exception | None = None) -> None:
        self.report_error = report_error
        self.events: list[tuple[str, str]] = []
        self.tool_calls: list[dict[str, Any]] = []
        self.finalize_calls: list[dict[str, Any]] = []

    def is_lease_lost(self) -> bool:
        return False

    def lease_lost_reason(self) -> str:
        return ""

    def report_tool_call(
        self,
        *,
        node_name: str,
        tool_name: str,
        request_json: dict[str, Any],
        response_json: dict[str, Any] | None,
        latency_ms: int,
        status: str,
        error: str | None = None,
        evidence_ids: list[str] | None = None,
    ) -> int:
        del latency_ms
        self.events.append(("report_tool_call", tool_name))
        if self.report_error is not None:
            raise self.report_error
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

    def finalize(
        self,
        *,
        status: str,
        diagnosis_json: dict[str, Any] | None,
        error_message: str | None = None,
        evidence_ids: list[str] | None = None,
    ) -> None:
        self.events.append(("finalize", status))
        self.finalize_calls.append(
            {
                "status": status,
                "diagnosis_json": diagnosis_json,
                "error_message": error_message,
                "evidence_ids": evidence_ids or [],
            }
        )


class FinalizeJobNodeTest(unittest.TestCase):
    def test_finalize_reports_audit_before_terminal_transition(self) -> None:
        runtime = _FakeRuntime()
        diagnosis = {"summary": "done"}
        state = GraphState(job_id="job-1", evidence_ids=["ev-1"], diagnosis_json=diagnosis)

        result = finalize_job(state, runtime)

        self.assertTrue(result.finalized)
        self.assertEqual(runtime.events, [("report_tool_call", "ai_job.finalize"), ("finalize", "succeeded")])
        self.assertEqual(len(runtime.tool_calls), 1)
        self.assertEqual(len(runtime.finalize_calls), 1)
        self.assertEqual(runtime.finalize_calls[0]["status"], "succeeded")
        self.assertEqual(runtime.finalize_calls[0]["diagnosis_json"], diagnosis)
        self.assertEqual(runtime.tool_calls[0]["response_json"]["status"], "succeeded")
        self.assertEqual(runtime.tool_calls[0]["response_json"]["phase"], "pre_finalize")
        self.assertFalse(runtime.tool_calls[0]["response_json"]["finalized"])
        self.assertEqual(result.tool_calls_written, 1)

    def test_finalize_failed_path_reports_before_terminal_transition(self) -> None:
        runtime = _FakeRuntime()
        state = GraphState(job_id="job-1", evidence_ids=["ev-1"], last_error="boom")

        result = finalize_job(state, runtime)

        self.assertTrue(result.finalized)
        self.assertEqual(runtime.events, [("report_tool_call", "ai_job.finalize"), ("finalize", "failed")])
        self.assertEqual(runtime.finalize_calls[0]["status"], "failed")
        self.assertEqual(runtime.finalize_calls[0]["error_message"], "boom")
        self.assertEqual(runtime.tool_calls[0]["request_json"]["error_message"], "boom")
        self.assertEqual(runtime.tool_calls[0]["response_json"]["status"], "failed")
        self.assertEqual(runtime.tool_calls[0]["response_json"]["phase"], "pre_finalize")
        self.assertFalse(runtime.tool_calls[0]["response_json"]["finalized"])

    def test_finalize_still_transitions_when_audit_write_fails(self) -> None:
        runtime = _FakeRuntime(report_error=RuntimeError("audit unavailable"))
        diagnosis = {"summary": "done"}
        state = GraphState(job_id="job-1", evidence_ids=["ev-1"], diagnosis_json=diagnosis)

        result = finalize_job(state, runtime)

        self.assertTrue(result.finalized)
        self.assertEqual(runtime.events, [("report_tool_call", "ai_job.finalize"), ("finalize", "succeeded")])
        self.assertEqual(len(runtime.tool_calls), 0)
        self.assertEqual(len(runtime.finalize_calls), 1)
        self.assertEqual(runtime.finalize_calls[0]["status"], "succeeded")
        self.assertEqual(result.tool_calls_written, 0)


if __name__ == "__main__":
    unittest.main()
