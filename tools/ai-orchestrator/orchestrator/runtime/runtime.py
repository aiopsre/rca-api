from __future__ import annotations

from typing import TYPE_CHECKING, Any, Callable

from ..tools_rca_api import RCAApiClient
from .evidence_publisher import EvidencePublishResult, EvidencePublisher
from .lease_manager import LeaseManager
from .post_finalize import PostFinalizeObserver, PostFinalizeSnapshot
from .retry import RetryExecutor, RetryPolicy
from .toolcall_reporter import ToolCallReporter
from .verification_runner import VerificationBudget, VerificationRunner, VerificationStepResult

if TYPE_CHECKING:
    from ..tooling.invoker import ToolInvoker


class OrchestratorRuntime:
    def __init__(
        self,
        *,
        client: RCAApiClient,
        job_id: str,
        instance_id: str,
        heartbeat_interval_seconds: int,
        log_func: Callable[[str], None] | None = None,
        retry_policy: RetryPolicy | None = None,
        verification_max_steps: int = 20,
        verification_max_total_latency_ms: int = 0,
        verification_max_total_bytes: int = 0,
        verification_dedupe_enabled: bool = True,
        tool_invoker: ToolInvoker | None = None,
    ) -> None:
        self._client = client
        self._job_id = str(job_id).strip()
        self._instance_id = str(instance_id).strip()
        self._tool_invoker = tool_invoker
        if not self._job_id:
            raise RuntimeError("job_id is required")

        # Runtime owns lease identity propagation for all job lifecycle calls.
        if self._instance_id:
            self._client.session.headers.update({"X-Orchestrator-Instance-ID": self._instance_id})
            self._client.instance_id = self._instance_id

        self._retry_executor = RetryExecutor(
            policy=retry_policy,
            log_func=log_func,
        )
        self._lease_manager = LeaseManager(
            client=self._client,
            heartbeat_interval_seconds=heartbeat_interval_seconds,
            instance_id=self._instance_id,
            log_func=log_func,
            execute_with_retry=self._execute_with_retry,
        )
        self._toolcall_reporter = ToolCallReporter(client=self._client, job_id=self._job_id)
        self._evidence_publisher = EvidencePublisher(
            client=self._client,
            job_id=self._job_id,
            execute_with_retry=self._execute_with_retry,
        )
        self._post_finalize_observer = PostFinalizeObserver(
            client=self._client,
            execute_with_retry=self._execute_with_retry,
            log_func=log_func,
        )
        self._verification_runner = VerificationRunner(
            client=self._client,
            execute_with_retry=self._execute_with_retry,
            call_tool=self.call_tool,
            log_func=log_func,
            budget=VerificationBudget(
                max_steps=verification_max_steps,
                max_total_latency_ms=verification_max_total_latency_ms,
                max_total_bytes=verification_max_total_bytes,
            ),
            dedupe_enabled=verification_dedupe_enabled,
        )

    def start(self) -> bool:
        return self._execute_with_retry("job.start", lambda: self._lease_manager.start(self._job_id))

    def _execute_with_retry(self, operation: str, fn: Callable[[], Any]) -> Any:
        return self._retry_executor.run(operation, fn)

    def call_tool(
        self,
        tool: str,
        params: dict[str, Any] | None,
        idempotency_key: str | None = None,
    ) -> dict[str, Any]:
        normalized_tool = str(tool).strip()
        if not normalized_tool:
            raise RuntimeError("tool is required")

        def _call() -> dict[str, Any]:
            if self._tool_invoker is not None:
                return self._tool_invoker.call(
                    tool=normalized_tool,
                    input_payload=params,
                    idempotency_key=idempotency_key,
                )
            return self._client.mcp_client.call(
                tool=normalized_tool,
                input_payload=params,
                idempotency_key=idempotency_key,
            )

        return self._execute_with_retry(f"tool.call:{normalized_tool}", _call)

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
        seq = self._toolcall_reporter.allocate_seq()
        return self._execute_with_retry(
            f"tool_call.report:{node_name}:{tool_name}:seq={seq}",
            lambda: self._toolcall_reporter.report(
                node_name=node_name,
                tool_name=tool_name,
                request_json=request_json,
                response_json=response_json,
                latency_ms=latency_ms,
                status=status,
                error=error,
                evidence_ids=evidence_ids,
                seq=seq,
            ),
        )

    def finalize(
        self,
        *,
        status: str,
        diagnosis_json: dict[str, Any] | None,
        error_message: str | None = None,
        evidence_ids: list[str] | None = None,
    ) -> None:
        self._execute_with_retry(
            "job.finalize",
            lambda: self._client.finalize_job(
                self._job_id,
                status=status,
                diagnosis_json=diagnosis_json,
                error_message=error_message,
                evidence_ids=evidence_ids,
            ),
        )

    def get_job(self, job_id: str | None = None) -> dict[str, Any]:
        target_job_id = str(job_id or self._job_id).strip() or self._job_id
        return self._execute_with_retry("job.get", lambda: self._client.get_job(target_job_id))

    def get_incident(self, incident_id: str) -> dict[str, Any]:
        normalized = str(incident_id).strip()
        if not normalized:
            raise RuntimeError("incident_id is required")
        return self._execute_with_retry("incident.get", lambda: self._client.get_incident(normalized))

    def ensure_datasource(self, ds_base_url: str) -> str:
        return self._execute_with_retry("datasource.ensure", lambda: self._client.ensure_datasource(ds_base_url))

    def query_metrics(
        self,
        *,
        datasource_id: str,
        promql: str,
        start_ts: int,
        end_ts: int,
        step_s: int,
    ) -> dict[str, Any]:
        return self._execute_with_retry(
            "query.metrics",
            lambda: self._client.query_metrics(
                datasource_id=datasource_id,
                promql=promql,
                start_ts=start_ts,
                end_ts=end_ts,
                step_s=step_s,
            ),
        )

    def query_logs(
        self,
        *,
        datasource_id: str,
        query: str,
        start_ts: int,
        end_ts: int,
        limit: int,
    ) -> dict[str, Any]:
        return self._execute_with_retry(
            "query.logs",
            lambda: self._client.query_logs(
                datasource_id=datasource_id,
                query=query,
                start_ts=start_ts,
                end_ts=end_ts,
                limit=limit,
            ),
        )

    def save_mock_evidence(
        self,
        *,
        incident_id: str,
        node_name: str,
        kind: str,
        summary: str,
        raw: dict[str, Any],
        query_hash_source: Any = None,
    ) -> EvidencePublishResult:
        return self._evidence_publisher.save_mock_evidence(
            incident_id=incident_id,
            node_name=node_name,
            kind=kind,
            summary=summary,
            raw=raw,
            query_hash_source=query_hash_source,
        )

    def save_evidence_from_query(
        self,
        *,
        incident_id: str,
        node_name: str,
        kind: str,
        query: dict[str, Any],
        result: dict[str, Any],
        query_hash_source: Any = None,
    ) -> EvidencePublishResult:
        return self._evidence_publisher.save_evidence_from_query(
            incident_id=incident_id,
            node_name=node_name,
            kind=kind,
            query=query,
            result=result,
            query_hash_source=query_hash_source,
        )

    def is_lease_lost(self) -> bool:
        return self._lease_manager.is_lease_lost()

    def lease_lost_reason(self) -> str:
        return self._lease_manager.lease_lost_reason()

    def observe_post_finalize(
        self,
        *,
        incident_id: str,
        wait_timeout_s: float = 0.0,
        wait_interval_s: float = 0.5,
        wait_max_interval_s: float = 2.0,
    ) -> PostFinalizeSnapshot:
        if float(wait_timeout_s) > 0:
            return self._post_finalize_observer.observe_with_wait(
                incident_id=incident_id,
                job_id=self._job_id,
                timeout_s=wait_timeout_s,
                interval_s=wait_interval_s,
                max_interval_s=wait_max_interval_s,
            )
        return self._post_finalize_observer.observe(incident_id=incident_id, job_id=self._job_id)

    def run_verification(
        self,
        *,
        incident_id: str,
        verification_plan: dict[str, Any],
        source: str = "ai_job",
    ) -> list[VerificationStepResult]:
        return self._verification_runner.run(
            incident_id=incident_id,
            verification_plan=verification_plan,
            source=source,
            actor=f"ai:{self._job_id}",
        )

    def shutdown(self) -> None:
        self._lease_manager.shutdown()
