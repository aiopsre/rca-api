from __future__ import annotations

from dataclasses import dataclass
import pathlib
import sys
import threading
import types
import unittest


TESTS_DIR = pathlib.Path(__file__).resolve().parent
PROJECT_DIR = TESTS_DIR.parent
if str(PROJECT_DIR) not in sys.path:
    sys.path.insert(0, str(PROJECT_DIR))

from orchestrator.graph import OrchestratorConfig, build_graph
from orchestrator.runtime.tool_discovery import ToolDescriptor, ToolDiscoveryResult
from orchestrator.runtime.tool_catalog import ToolSpec, ToolCatalogSnapshot, ExecutedToolCall
from orchestrator.runtime.fc_adapter import FunctionCallingToolAdapter
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
        self._call_lock = threading.Lock()  # Thread safety for concurrent execution
        self.tool_calls: list[dict[str, object]] = []
        self.query_metrics_calls = 0
        self.query_logs_calls = 0
        self.call_tool_calls: list[dict[str, object]] = []
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

    def ensure_datasource(self, ds_base_url: str, ds_type: str = "prometheus") -> str:
        del ds_type
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

    def consume_prompt_skill(
        self,
        *,
        capability: str,
        graph_state: object,
    ) -> dict[str, object] | None:
        del capability, graph_state
        return None

    def consume_diagnosis_enrich_skill(
        self,
        *,
        graph_state: object,
        input_payload: dict[str, object],
    ) -> dict[str, object] | None:
        return None

    def merge_session_patch(self, graph_state: GraphState, patch: dict[str, object] | None) -> None:
        if not isinstance(patch, dict):
            return
        current = graph_state.session_patch if isinstance(graph_state.session_patch, dict) else {}
        current = dict(current)
        current.update(patch)
        graph_state.session_patch = current

    def discover_tools(self) -> ToolDiscoveryResult:
        """Discover available tools for dynamic tool execution."""
        # Return tools based on the runtime's capabilities
        tools: list[ToolDescriptor] = []

        # Add metrics tool if query_metrics is available
        if hasattr(self, 'query_metrics'):
            tools.append(ToolDescriptor(
                tool_name="prometheus_query",
                description="Query Prometheus metrics",
                tags=("metrics", "query"),
            ))

        # Add logs tool if query_logs is available
        if hasattr(self, 'query_logs'):
            tools.append(ToolDescriptor(
                tool_name="loki_search",
                description="Search Loki logs",
                tags=("logs", "search"),
            ))

        by_tag: dict[str, list[ToolDescriptor]] = {}
        for tool in tools:
            for tag in tool.tags:
                by_tag.setdefault(tag, []).append(tool)

        return ToolDiscoveryResult(tools=tuple(tools), by_tag=by_tag)

    def call_tool(self, *, tool: str, params: dict[str, object]) -> dict[str, object]:
        """Execute a tool call (thread-safe)."""
        with self._call_lock:
            self.call_tool_calls.append({"tool": tool, "params": params})
            if tool == "prometheus_query":
                self.query_metrics_calls += 1
                return {
                    "output": {
                        "data": {
                            "result": [{"value": [1, "2"]}],
                        },
                    },
                }
            elif tool == "loki_search":
                self.query_logs_calls += 1
                return {
                    "output": {
                        "rows": [{"line": "error timeout"}],
                    },
                }
        return {"output": {}}

    def report_observation(
        self,
        *,
        tool: str,
        node_name: str,
        params: dict[str, object],
        response: dict[str, object],
        evidence_ids: list[str] | None = None,
    ) -> None:
        """Report an observation (best effort for tests)."""
        self.report_tool_call(
            node_name=node_name,
            tool_name=tool,
            request_json=params,
            response_json=response,
            latency_ms=0,
            status=response.get("status", "ok") if isinstance(response, dict) else "ok",
            evidence_ids=evidence_ids,
        )

    def get_tool_catalog_snapshot(self) -> ToolCatalogSnapshot | None:
        """Get tool catalog snapshot for FC path."""
        # Build snapshot from discover_tools
        discovery = self.discover_tools()
        if not discovery.tools:
            return None

        tools = []
        by_name = {}
        for tool in discovery.tools:
            spec = ToolSpec(
                name=tool.tool_name,
                description=tool.description or f"Tool {tool.tool_name}",
                input_schema=tool.input_schema or {"type": "object"},
                tags=tool.tags,
            )
            tools.append(spec)
            by_name[tool.tool_name] = spec

        return ToolCatalogSnapshot(
            toolset_ids=("fake_toolset",),
            tools=tuple(tools),
            by_name=by_name,
        )

    def get_fc_adapter(self) -> FunctionCallingToolAdapter | None:
        """Get FC adapter for FC graph path."""
        snapshot = self.get_tool_catalog_snapshot()
        if snapshot is None:
            return None
        return FunctionCallingToolAdapter(snapshot)

    def execute_tool(self, tool_name: str, args: dict, *, source: str = "graph") -> ExecutedToolCall:
        """Execute a tool call for FC path."""
        result = self.call_tool(tool=tool_name, params=args)
        return ExecutedToolCall(
            tool_name=tool_name,
            request_json=args,
            response_json=result,
            latency_ms=10,
            source=source,
            status="ok",
        )


if __name__ == "__main__":
    unittest.main()
