from __future__ import annotations

import pathlib
import sys
import unittest
from unittest import mock

import requests


TESTS_DIR = pathlib.Path(__file__).resolve().parent
PROJECT_DIR = TESTS_DIR.parent
if str(PROJECT_DIR) not in sys.path:
    sys.path.insert(0, str(PROJECT_DIR))

from orchestrator.tooling.providers.mcp_http import MCPHttpProvider


class _FakeResponse:
    def __init__(self, *, ok: bool, lines: list[str], text: str = "") -> None:
        self.ok = ok
        self._lines = lines
        self.text = text or "\n".join(lines)
        self.closed = False

    def iter_lines(self, decode_unicode: bool = False):  # noqa: D401 - test helper.
        yield from self._lines

    def close(self) -> None:
        self.closed = True


class MCPHttpProviderTransportTest(unittest.TestCase):
    def test_probe_sse_endpoint_detects_endpoint_event(self) -> None:
        provider = MCPHttpProvider(base_url="http://example.com")
        fake_response = _FakeResponse(
            ok=True,
            lines=[
                "event: endpoint",
                "data: /mcp?sessionId=abc123",
                "",
            ],
        )
        provider._session.get = mock.MagicMock(return_value=fake_response)  # type: ignore[method-assign]

        self.assertTrue(provider._probe_sse_endpoint())
        self.assertTrue(fake_response.closed)

    def test_call_prefers_sse_when_probe_succeeds(self) -> None:
        provider = MCPHttpProvider(base_url="http://example.com")
        with mock.patch.object(provider, "_probe_sse_endpoint", return_value=True), mock.patch.object(
            provider, "_call_via_sse", return_value={"transport": "sse"}
        ) as call_sse, mock.patch.object(provider, "_call_via_http", return_value={"transport": "http"}) as call_http:
            result = provider.call(tool="tempo_query", input_payload={"query": "duration>1s"})

        self.assertEqual(result, {"transport": "sse"})
        call_sse.assert_called_once()
        call_http.assert_not_called()

    def test_call_falls_back_to_http_when_sse_probe_fails(self) -> None:
        provider = MCPHttpProvider(base_url="http://example.com")
        with mock.patch.object(provider, "_probe_sse_endpoint", return_value=False), mock.patch.object(
            provider, "_call_via_sse", return_value={"transport": "sse"}
        ) as call_sse, mock.patch.object(provider, "_call_via_http", return_value={"transport": "http"}) as call_http:
            result = provider.call(tool="tempo_get_trace", input_payload={"trace_id": "abc"})

        self.assertEqual(result, {"transport": "http"})
        call_http.assert_called_once()
        call_sse.assert_not_called()

    def test_wrap_sse_error_unwraps_exception_group(self) -> None:
        provider = MCPHttpProvider(base_url="http://example.com")
        inner = requests.Timeout("timeout while waiting for SSE response")
        group = ExceptionGroup("wrapped", [inner])

        error = provider._wrap_sse_error("tempo_query", group)

        self.assertEqual(error.category.value, "retryable_transport")
        self.assertIn("tempo_query", str(error))
        self.assertIn("timeout while waiting for SSE response", str(error))


if __name__ == "__main__":
    unittest.main()
