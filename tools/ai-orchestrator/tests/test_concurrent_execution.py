"""Tests for concurrent tool execution.

This module tests the concurrent execution of tool calls within
parallel groups using ThreadPoolExecutor.
"""
from __future__ import annotations

import pathlib
import sys
import threading
import time
import unittest
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
from typing import Any
from unittest.mock import MagicMock, patch

TESTS_DIR = pathlib.Path(__file__).resolve().parent
PROJECT_DIR = TESTS_DIR.parent
if str(PROJECT_DIR) not in sys.path:
    sys.path.insert(0, str(PROJECT_DIR))

from orchestrator.langgraph.executor import (
    ToolExecutionResult,
    execute_group_concurrent,
    _execute_single,
)
from orchestrator.state.tool_call_plan import ToolCallItem


class MockRuntime:
    """Mock runtime for testing concurrent execution."""

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self.call_tool_calls: list[dict[str, Any]] = []
        self._call_delay_s: float = 0.0
        self._fail_on_tool: str | None = None

    def set_call_delay(self, delay_s: float) -> None:
        """Set artificial delay for call_tool to simulate I/O."""
        self._call_delay_s = delay_s

    def set_fail_on_tool(self, tool_name: str | None) -> None:
        """Set tool name that should fail when called."""
        self._fail_on_tool = tool_name

    def call_tool(self, *, tool: str, params: dict[str, Any]) -> dict[str, Any]:
        """Execute a tool call with optional delay and failure simulation."""
        with self._lock:
            self.call_tool_calls.append({"tool": tool, "params": params})

        if self._call_delay_s > 0:
            time.sleep(self._call_delay_s)

        if self._fail_on_tool and tool == self._fail_on_tool:
            raise RuntimeError(f"Simulated failure for tool: {tool}")

        return {
            "output": {
                "tool": tool,
                "params": params,
                "result": "success",
            }
        }


class TestToolExecutionResult(unittest.TestCase):
    """Tests for ToolExecutionResult dataclass."""

    def test_result_creation(self) -> None:
        """ToolExecutionResult should store all fields correctly."""
        result = ToolExecutionResult(
            tool="test_tool",
            params={"key": "value"},
            query_type="metrics",
            purpose="Test purpose",
            status="ok",
            result={"output": "data"},
            error=None,
            latency_ms=100,
            group_idx=0,
            item_idx=1,
        )
        self.assertEqual(result.tool, "test_tool")
        self.assertEqual(result.params, {"key": "value"})
        self.assertEqual(result.query_type, "metrics")
        self.assertEqual(result.status, "ok")
        self.assertIsNone(result.error)


class TestExecuteGroupConcurrent(unittest.TestCase):
    """Tests for execute_group_concurrent function."""

    def test_empty_items_returns_empty_list(self) -> None:
        """Empty items list should return empty results."""
        runtime = MockRuntime()
        results = execute_group_concurrent(
            items=[],
            runtime=runtime,
            group_idx=0,
        )
        self.assertEqual(results, [])

    def test_single_item_no_thread_pool(self) -> None:
        """Single item should execute without thread pool overhead."""
        runtime = MockRuntime()
        item = ToolCallItem(
            tool="single_tool",
            params={"test": "value"},
            query_type="logs",
            purpose="Single test",
        )

        results = execute_group_concurrent(
            items=[(0, item)],
            runtime=runtime,
            group_idx=0,
        )

        self.assertEqual(len(results), 1)
        self.assertEqual(results[0].tool, "single_tool")
        self.assertEqual(results[0].status, "ok")
        self.assertEqual(len(runtime.call_tool_calls), 1)

    def test_parallel_group_executes_concurrently(self) -> None:
        """Multiple items in same group should execute concurrently."""
        runtime = MockRuntime()
        runtime.set_call_delay(0.1)  # 100ms delay per call

        items = [
            (0, ToolCallItem(tool="tool_a", params={}, query_type="metrics", purpose="Tool A")),
            (1, ToolCallItem(tool="tool_b", params={}, query_type="logs", purpose="Tool B")),
            (2, ToolCallItem(tool="tool_c", params={}, query_type="traces", purpose="Tool C")),
        ]

        start_time = time.time()
        results = execute_group_concurrent(
            items=items,
            runtime=runtime,
            group_idx=0,
            max_workers=3,
        )
        elapsed_time = time.time() - start_time

        # All 3 tools should execute concurrently, not sequentially
        # Sequential would take ~300ms, concurrent should take ~100ms
        self.assertLess(elapsed_time, 0.25)  # Allow some overhead
        self.assertEqual(len(results), 3)
        self.assertEqual(len(runtime.call_tool_calls), 3)

    def test_partial_failure_does_not_block_group(self) -> None:
        """One failure should not block other items in same group."""
        runtime = MockRuntime()
        runtime.set_fail_on_tool("tool_b")
        runtime.set_call_delay(0.05)

        items = [
            (0, ToolCallItem(tool="tool_a", params={}, query_type="metrics", purpose="Tool A")),
            (1, ToolCallItem(tool="tool_b", params={}, query_type="logs", purpose="Tool B")),
            (2, ToolCallItem(tool="tool_c", params={}, query_type="traces", purpose="Tool C")),
        ]

        results = execute_group_concurrent(
            items=items,
            runtime=runtime,
            group_idx=0,
            max_workers=3,
        )

        # All 3 tools should have results
        self.assertEqual(len(results), 3)

        # Check statuses
        status_by_tool = {r.tool: r.status for r in results}
        self.assertEqual(status_by_tool["tool_a"], "ok")
        self.assertEqual(status_by_tool["tool_b"], "error")
        self.assertEqual(status_by_tool["tool_c"], "ok")

        # tool_b should have an error message
        tool_b_result = next(r for r in results if r.tool == "tool_b")
        self.assertIn("Simulated failure", tool_b_result.error or "")

    def test_result_order_preserved(self) -> None:
        """Results should be ordered by item_idx regardless of completion order."""
        runtime = MockRuntime()

        # Create items that will complete in different orders
        items = [
            (0, ToolCallItem(tool="tool_0", params={}, query_type="metrics", purpose="First")),
            (1, ToolCallItem(tool="tool_1", params={}, query_type="logs", purpose="Second")),
            (2, ToolCallItem(tool="tool_2", params={}, query_type="traces", purpose="Third")),
        ]

        results = execute_group_concurrent(
            items=items,
            runtime=runtime,
            group_idx=0,
            max_workers=3,
        )

        # Results should be sorted by item_idx
        self.assertEqual(results[0].item_idx, 0)
        self.assertEqual(results[1].item_idx, 1)
        self.assertEqual(results[2].item_idx, 2)

    def test_group_timeout_handling(self) -> None:
        """Group timeout should raise TimeoutError for pending items."""
        runtime = MockRuntime()
        runtime.set_call_delay(5.0)  # 5 seconds per call - will timeout

        # Use multiple items to trigger thread pool execution (single item has no timeout)
        items = [
            (0, ToolCallItem(tool="slow_tool_1", params={}, query_type="metrics", purpose="Slow tool 1")),
            (1, ToolCallItem(tool="slow_tool_2", params={}, query_type="metrics", purpose="Slow tool 2")),
        ]

        # Use a very short timeout - should raise TimeoutError
        with self.assertRaises(TimeoutError):
            execute_group_concurrent(
                items=items,
                runtime=runtime,
                group_idx=0,
                group_timeout_s=0.1,  # 100ms timeout
            )

    def test_max_workers_limits_concurrency(self) -> None:
        """max_workers should limit the number of concurrent executions."""
        runtime = MockRuntime()
        runtime.set_call_delay(0.1)

        items = [
            (i, ToolCallItem(tool=f"tool_{i}", params={}, query_type="metrics", purpose=f"Tool {i}"))
            for i in range(5)
        ]

        # With max_workers=2, 5 items with 100ms delay each
        # Should take at least 300ms (3 batches: 2 + 2 + 1)
        start_time = time.time()
        results = execute_group_concurrent(
            items=items,
            runtime=runtime,
            group_idx=0,
            max_workers=2,
        )
        elapsed_time = time.time() - start_time

        self.assertEqual(len(results), 5)
        # With 2 workers and 5 items of 100ms each:
        # Batch 1: items 0,1 (100ms)
        # Batch 2: items 2,3 (100ms)
        # Batch 3: item 4 (100ms)
        # Total: ~300ms
        self.assertGreaterEqual(elapsed_time, 0.25)


class TestExecuteSingle(unittest.TestCase):
    """Tests for _execute_single function."""

    def test_successful_execution(self) -> None:
        """Successful tool call should return ok status."""
        runtime = MockRuntime()
        item = ToolCallItem(
            tool="test_tool",
            params={"key": "value"},
            query_type="metrics",
            purpose="Test execution",
        )

        result = _execute_single(item, 0, runtime, 0)

        self.assertEqual(result.status, "ok")
        self.assertIsNone(result.error)
        self.assertIn("output", result.result)

    def test_failed_execution(self) -> None:
        """Failed tool call should return error status."""
        runtime = MockRuntime()
        runtime.set_fail_on_tool("failing_tool")

        item = ToolCallItem(
            tool="failing_tool",
            params={},
            query_type="metrics",
            purpose="Will fail",
        )

        result = _execute_single(item, 0, runtime, 0)

        self.assertEqual(result.status, "error")
        self.assertIsNotNone(result.error)
        self.assertIn("Simulated failure", result.error or "")

    def test_latency_measured(self) -> None:
        """Latency should be measured for each execution."""
        runtime = MockRuntime()
        runtime.set_call_delay(0.05)  # 50ms delay

        item = ToolCallItem(
            tool="slow_tool",
            params={},
            query_type="metrics",
            purpose="Slow execution",
        )

        result = _execute_single(item, 0, runtime, 0)

        # Latency should be at least 50ms
        self.assertGreaterEqual(result.latency_ms, 50)


class TestThreadSafety(unittest.TestCase):
    """Tests for thread safety of concurrent execution."""

    def test_runtime_calls_are_thread_safe(self) -> None:
        """Runtime should handle concurrent calls safely."""
        runtime = MockRuntime()
        call_count = 100

        items = [
            (i, ToolCallItem(tool=f"tool_{i}", params={"index": i}, query_type="metrics", purpose=f"Tool {i}"))
            for i in range(call_count)
        ]

        results = execute_group_concurrent(
            items=items,
            runtime=runtime,
            group_idx=0,
            max_workers=10,
        )

        # All calls should be recorded
        self.assertEqual(len(runtime.call_tool_calls), call_count)
        self.assertEqual(len(results), call_count)

        # All results should be successful
        success_count = sum(1 for r in results if r.status == "ok")
        self.assertEqual(success_count, call_count)

    def test_no_duplicate_calls(self) -> None:
        """Each item should be called exactly once."""
        runtime = MockRuntime()

        items = [
            (i, ToolCallItem(tool=f"tool_{i}", params={"id": i}, query_type="metrics", purpose=f"Tool {i}"))
            for i in range(20)
        ]

        execute_group_concurrent(
            items=items,
            runtime=runtime,
            group_idx=0,
            max_workers=5,
        )

        # Check for duplicates
        called_tools = [c["tool"] for c in runtime.call_tool_calls]
        self.assertEqual(len(called_tools), len(set(called_tools)))


if __name__ == "__main__":
    unittest.main()