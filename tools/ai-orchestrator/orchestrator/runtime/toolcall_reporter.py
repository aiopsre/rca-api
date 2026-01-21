from __future__ import annotations

import threading
from typing import Any

from ..tools_rca_api import RCAApiClient


class ToolCallReporter:
    def __init__(self, client: RCAApiClient, job_id: str) -> None:
        self._client = client
        self._job_id = str(job_id).strip()
        if not self._job_id:
            raise RuntimeError("job_id is required")
        self._next_seq = 1
        self._lock = threading.Lock()

    def report(
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
        with self._lock:
            seq = self._next_seq
            self._next_seq += 1

        self._client.add_tool_call(
            job_id=self._job_id,
            seq=seq,
            node_name=node_name,
            tool_name=tool_name,
            request_json=request_json,
            response_json=response_json,
            latency_ms=latency_ms,
            status=status,
            error=error,
        )
        return seq
