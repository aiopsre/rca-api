from __future__ import annotations

from typing import Any, Callable

from ..tools_rca_api import RCAApiClient
from .lease_manager import LeaseManager
from .toolcall_reporter import ToolCallReporter


class OrchestratorRuntime:
    def __init__(
        self,
        *,
        client: RCAApiClient,
        job_id: str,
        instance_id: str,
        heartbeat_interval_seconds: int,
        log_func: Callable[[str], None] | None = None,
    ) -> None:
        self._client = client
        self._job_id = str(job_id).strip()
        self._instance_id = str(instance_id).strip()
        if not self._job_id:
            raise RuntimeError("job_id is required")

        # Runtime owns lease identity propagation for all job lifecycle calls.
        if self._instance_id:
            self._client.session.headers.update({"X-Orchestrator-Instance-ID": self._instance_id})
            self._client.instance_id = self._instance_id

        self._lease_manager = LeaseManager(
            client=self._client,
            heartbeat_interval_seconds=heartbeat_interval_seconds,
            instance_id=self._instance_id,
            log_func=log_func,
        )
        self._toolcall_reporter = ToolCallReporter(client=self._client, job_id=self._job_id)

    def start(self) -> bool:
        return self._lease_manager.start(self._job_id)

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
    ) -> int:
        return self._toolcall_reporter.report(
            node_name=node_name,
            tool_name=tool_name,
            request_json=request_json,
            response_json=response_json,
            latency_ms=latency_ms,
            status=status,
            error=error,
        )

    def finalize(
        self,
        *,
        status: str,
        diagnosis_json: dict[str, Any] | None,
        error_message: str | None = None,
    ) -> None:
        self._client.finalize_job(
            self._job_id,
            status=status,
            diagnosis_json=diagnosis_json,
            error_message=error_message,
        )

    def is_lease_lost(self) -> bool:
        return self._lease_manager.is_lease_lost()

    def lease_lost_reason(self) -> str:
        return self._lease_manager.lease_lost_reason()

    def shutdown(self) -> None:
        self._lease_manager.shutdown()
