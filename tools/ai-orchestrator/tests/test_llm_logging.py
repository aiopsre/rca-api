from __future__ import annotations

import unittest

from orchestrator.langgraph.helpers import invoke_llm_with_optional_tools
from orchestrator.langgraph.llm_logging import log_llm_dialogue


class _DummyMessage:
    def __init__(self, content: object) -> None:
        self.content = content


class _DummyResponse:
    def __init__(self, content: object, tool_calls: list[object] | None = None) -> None:
        self.content = content
        self.tool_calls = tool_calls or []


class _DummyLLM:
    def __init__(self, response: object) -> None:
        self._response = response
        self.bound_tools: list[list[dict[str, object]]] = []
        self.invocations: list[list[object]] = []

    def bind_tools(self, tools: list[dict[str, object]]) -> "_DummyLLM":
        self.bound_tools.append(list(tools))
        return self

    def invoke(self, messages: list[object]) -> object:
        self.invocations.append(list(messages))
        return self._response


class TestLLMDebugLogging(unittest.TestCase):
    def test_log_llm_dialogue_emits_prompt_and_response(self) -> None:
        messages = [
            _DummyMessage("system prompt"),
            _DummyMessage("user prompt " + ("x" * 5000)),
        ]
        response = _DummyResponse(
            "assistant reply",
            tool_calls=[{"name": "search", "arguments": {"query": "foo"}}],
        )

        with self.assertLogs("orchestrator.llm", level="DEBUG") as captured:
            log_llm_dialogue(
                event="response",
                node_name="unit.test",
                messages=messages,
                response=response,
                extra={"job_id": "job-1"},
            )

        joined = "\n".join(captured.output)
        self.assertIn('"event":"response"', joined)
        self.assertIn("system prompt", joined)
        self.assertIn("assistant reply", joined)
        self.assertIn('"tool_calls"', joined)
        self.assertIn("job-1", joined)
        self.assertIn("...", joined)

    def test_invoke_llm_with_optional_tools_logs_and_binds_conditionally(self) -> None:
        messages = [_DummyMessage("system"), _DummyMessage("user")]
        response = _DummyResponse("assistant reply")

        llm = _DummyLLM(response)
        with self.assertLogs("orchestrator.llm", level="DEBUG") as captured:
            result = invoke_llm_with_optional_tools(
                llm,
                messages,
                [],
                node_name="unit.empty_tools",
                extra={"case": "empty"},
            )

        self.assertIs(result, response)
        self.assertEqual(llm.bound_tools, [])
        self.assertEqual(len(llm.invocations), 1)
        joined = "\n".join(captured.output)
        self.assertIn('"event":"request"', joined)
        self.assertIn('"event":"response"', joined)
        self.assertIn('"tool_count":0', joined)

    def test_invoke_llm_with_optional_tools_binds_when_tools_present(self) -> None:
        messages = [_DummyMessage("system"), _DummyMessage("user")]
        response = _DummyResponse("assistant reply")

        llm = _DummyLLM(response)
        tools = [{"type": "function", "function": {"name": "search"}}]
        with self.assertLogs("orchestrator.llm", level="DEBUG") as captured:
            result = invoke_llm_with_optional_tools(
                llm,
                messages,
                tools,
                node_name="unit.tools",
                extra={"case": "with_tools"},
            )

        self.assertIs(result, response)
        self.assertEqual(len(llm.bound_tools), 1)
        self.assertEqual(llm.bound_tools[0], tools)
        self.assertEqual(len(llm.invocations), 1)
        joined = "\n".join(captured.output)
        self.assertIn('"tool_count":1', joined)


if __name__ == "__main__":
    unittest.main()
