from __future__ import annotations

import threading
from typing import Any

from ..tools_rca_api import RCAApiClient


class SeqAllocator:
    def __init__(self, start: int = 1) -> None:
        self._next_seq = max(int(start), 1)
        self._lock = threading.Lock()

    def allocate(self) -> int:
        with self._lock:
            seq = self._next_seq
            self._next_seq += 1
            return seq


class ToolCallReporter:
    def __init__(self, client: RCAApiClient, job_id: str, seq_allocator: SeqAllocator | None = None) -> None:
        self._client = client
        self._job_id = str(job_id).strip()
        if not self._job_id:
            raise RuntimeError("job_id is required")
        self._seq_allocator = seq_allocator or SeqAllocator(start=1)

    def allocate_seq(self) -> int:
        return self._seq_allocator.allocate()

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
        evidence_ids: list[str] | None = None,
        seq: int | None = None,
    ) -> int:
        report_seq = int(seq) if seq is not None else self._seq_allocator.allocate()
        if report_seq <= 0:
            raise RuntimeError("seq must be positive")

        self._client.add_tool_call(
            job_id=self._job_id,
            seq=report_seq,
            node_name=node_name,
            tool_name=tool_name,
            request_json=request_json,
            response_json=response_json,
            latency_ms=latency_ms,
            status=status,
            error=error,
            evidence_ids=evidence_ids,
        )
        return report_seq
