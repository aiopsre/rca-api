"""Tests for audit utilities."""
from __future__ import annotations

import pathlib
import sys
import unittest

TESTS_DIR = pathlib.Path(__file__).resolve().parent
PROJECT_DIR = TESTS_DIR.parent
if str(PROJECT_DIR) not in sys.path:
    sys.path.insert(0, str(PROJECT_DIR))

from orchestrator.runtime.audit import (
    AuditKind,
    AuditRecord,
    redact_sensitive,
    summarize_request,
    summarize_response,
    SENSITIVE_FIELD_PATTERNS,
    REDACT_VALUE,
)


class TestRedactSensitive(unittest.TestCase):
    """Tests for sensitive field redaction."""

    def test_redact_api_key(self):
        """API key is redacted."""
        data = {"api_key": "secret123", "name": "test"}
        result = redact_sensitive(data)
        self.assertEqual(result["api_key"], REDACT_VALUE)
        self.assertEqual(result["name"], "test")

    def test_redact_nested(self):
        """Nested sensitive fields are redacted."""
        data = {
            "config": {
                "token": "abc123",
                "other": "value",
            }
        }
        result = redact_sensitive(data)
        self.assertEqual(result["config"]["token"], REDACT_VALUE)
        self.assertEqual(result["config"]["other"], "value")

    def test_redact_various_patterns(self):
        """Various sensitive patterns are redacted."""
        data = {
            "password": "pwd123",
            "Authorization": "Bearer token",
            "SECRET_KEY": "key123",
            "access_token": "tok123",
        }
        result = redact_sensitive(data)
        for key in data:
            self.assertEqual(result[key], REDACT_VALUE)

    def test_non_sensitive_preserved(self):
        """Non-sensitive fields are preserved."""
        data = {
            "query": "up",
            "datasource_id": "ds-1",
            "start_ts": 1234567890,
        }
        result = redact_sensitive(data)
        self.assertEqual(result, data)

    def test_redact_in_list(self):
        """Sensitive fields in list items are redacted."""
        data = {
            "items": [
                {"api_key": "secret1", "name": "item1"},
                {"token": "secret2", "name": "item2"},
            ]
        }
        result = redact_sensitive(data)
        self.assertEqual(result["items"][0]["api_key"], REDACT_VALUE)
        self.assertEqual(result["items"][1]["token"], REDACT_VALUE)
        self.assertEqual(result["items"][0]["name"], "item1")

    def test_empty_dict(self):
        """Empty dict returns empty dict."""
        result = redact_sensitive({})
        self.assertEqual(result, {})

    def test_non_dict_input(self):
        """Non-dict input returns empty dict."""
        result = redact_sensitive("not a dict")  # type: ignore
        self.assertEqual(result, {})

    def test_max_depth_limit(self):
        """Max depth limit is respected - beyond limit returns data as-is."""
        data = {"level1": {"level2": {"level3": {"level4": {"level5": {"password": "secret"}}}}}}
        # With max_depth=5, level6 (password) should NOT be redacted
        # because the recursion stops and returns data as-is
        result = redact_sensitive(data, max_depth=5)
        # At max_depth=0, the function returns the data dict as-is (without redaction)
        self.assertEqual(result["level1"]["level2"]["level3"]["level4"]["level5"]["password"], "secret")

    def test_redaction_at_shallow_depth(self):
        """Redaction works at shallow depth."""
        data = {"config": {"password": "secret"}}
        result = redact_sensitive(data, max_depth=3)
        self.assertEqual(result["config"]["password"], REDACT_VALUE)

    def test_hyphenated_key_redaction(self):
        """Hyphenated keys are redacted."""
        data = {
            "api-key": "secret",
            "auth-token": "secret",
        }
        result = redact_sensitive(data)
        self.assertEqual(result["api-key"], REDACT_VALUE)
        self.assertEqual(result["auth-token"], REDACT_VALUE)


class TestSummarizeRequest(unittest.TestCase):
    """Tests for request summarization."""

    def test_summarize_query_tool(self):
        """Query tool request is summarized."""
        params = {
            "query": "up{job='prometheus'}",
            "datasource_id": "prom-1",
            "start_ts": 1000,
            "end_ts": 2000,
        }
        result = summarize_request("mcp.query_logs", params)
        self.assertIn("tool", result)
        self.assertIn("query", result)
        self.assertIn("datasource_id", result)

    def test_summarize_with_sensitive(self):
        """Sensitive fields are redacted in summary."""
        params = {
            "query": "up",
            "api_key": "secret",
        }
        result = summarize_request("tool", params)
        self.assertEqual(result["api_key"], REDACT_VALUE)

    def test_summarize_truncates_long_query(self):
        """Long queries are truncated."""
        long_query = "x" * 300
        params = {"query": long_query}
        result = summarize_request("mcp.query", params)
        self.assertEqual(len(str(result.get("query", ""))), 200)

    def test_summarize_promql(self):
        """PromQL queries are summarized."""
        params = {
            "promql": 'up{job="prometheus"}',
            "datasource_id": "prom-1",
        }
        result = summarize_request("mcp.promql_query", params)
        self.assertIn("promql", result)

    def test_summarize_time_range(self):
        """Time range is extracted."""
        params = {
            "start_ts": 1000,
            "end_ts": 2000,
        }
        result = summarize_request("mcp.query_range", params)
        self.assertIn("time_range", result)
        self.assertEqual(result["time_range"]["start_ts"], 1000)
        self.assertEqual(result["time_range"]["end_ts"], 2000)

    def test_summarize_non_query_tool(self):
        """Non-query tools get truncated params."""
        params = {f"key_{i}": f"value_{i}" for i in range(15)}
        result = summarize_request("some_tool", params)
        self.assertIn("_truncated", result)


class TestSummarizeResponse(unittest.TestCase):
    """Tests for response summarization."""

    def test_summarize_success(self):
        """Success response is summarized."""
        response = {
            "status": "ok",
            "rowCount": 100,
            "resultSizeBytes": 1024,
        }
        result = summarize_response(response)
        self.assertEqual(result["status"], "ok")
        self.assertEqual(result["row_count"], 100)
        self.assertEqual(result["result_size_bytes"], 1024)

    def test_summarize_error(self):
        """Error response includes error message."""
        response = {
            "status": "error",
            "error": "connection timeout",
        }
        result = summarize_response(response)
        self.assertEqual(result["status"], "error")
        self.assertEqual(result["error"], "connection timeout")

    def test_empty_response(self):
        """Empty response returns status empty."""
        result = summarize_response(None)
        self.assertEqual(result["status"], "empty")

    def test_non_dict_response(self):
        """Non-dict response returns status empty."""
        result = summarize_response("not a dict")  # type: ignore
        self.assertEqual(result["status"], "empty")

    def test_truncated_flag(self):
        """Truncated flag is captured."""
        response = {
            "status": "ok",
            "isTruncated": True,
        }
        result = summarize_response(response)
        self.assertTrue(result["is_truncated"])

    def test_snake_case_fields(self):
        """Snake case fields are captured."""
        response = {
            "status": "ok",
            "row_count": 50,
            "result_size_bytes": 2048,
            "is_truncated": False,
        }
        result = summarize_response(response)
        self.assertEqual(result["row_count"], 50)
        self.assertEqual(result["result_size_bytes"], 2048)
        self.assertFalse(result["is_truncated"])

    def test_long_error_truncated(self):
        """Long error messages are truncated."""
        long_error = "x" * 600
        response = {
            "status": "error",
            "error": long_error,
        }
        result = summarize_response(response)
        self.assertEqual(len(result["error"]), 500)

    def test_sensitive_in_response_redacted(self):
        """Sensitive fields in response are redacted."""
        response = {
            "status": "ok",
            "api_key": "secret123",
        }
        result = summarize_response(response)
        self.assertNotIn("api_key", result)


class TestAuditRecord(unittest.TestCase):
    """Tests for AuditRecord."""

    def test_to_observation_params(self):
        """AuditRecord converts to observation params."""
        record = AuditRecord(
            incident_id="inc-1",
            ai_job_id="job-1",
            skill_id="skill-1",
            skill_version="v1.0",
            capability="tool.plan",
            tool_name="prometheus_query",
        )
        params = record.to_observation_params()
        self.assertEqual(params["skill_id"], "skill-1")
        self.assertEqual(params["tool_name"], "prometheus_query")

    def test_to_observation_response(self):
        """AuditRecord converts to observation response."""
        record = AuditRecord(
            status="ok",
            latency_ms=100,
        )
        response = record.to_observation_response()
        self.assertEqual(response["status"], "ok")
        self.assertEqual(response["latency_ms"], 100)

    def test_to_observation_response_with_error(self):
        """AuditRecord includes error in response."""
        record = AuditRecord(
            status="error",
            latency_ms=50,
            error="connection failed",
            degrade_reason="timeout",
        )
        response = record.to_observation_response()
        self.assertEqual(response["status"], "error")
        self.assertEqual(response["error"], "connection failed")
        self.assertEqual(response["degrade_reason"], "timeout")

    def test_default_values(self):
        """AuditRecord has sensible defaults."""
        record = AuditRecord()
        self.assertEqual(record.status, "ok")
        self.assertEqual(record.kind, AuditKind.TOOL_INVOKE)
        self.assertEqual(record.latency_ms, 0)
        self.assertIsNotNone(record.timestamp_ms)

    def test_evidence_ids_default(self):
        """Evidence IDs default to empty list."""
        record = AuditRecord()
        self.assertEqual(record.evidence_ids, [])


class TestAuditKind(unittest.TestCase):
    """Tests for AuditKind enum."""

    def test_skill_select_value(self):
        """AuditKind.SKILL_SELECT has correct value."""
        self.assertEqual(AuditKind.SKILL_SELECT.value, "skill.select")

    def test_tool_invoke_value(self):
        """AuditKind.TOOL_INVOKE has correct value."""
        self.assertEqual(AuditKind.TOOL_INVOKE.value, "tool.invoke")

    def test_all_kinds_are_strings(self):
        """All AuditKind values are strings."""
        for kind in AuditKind:
            self.assertIsInstance(kind.value, str)


if __name__ == "__main__":
    unittest.main()