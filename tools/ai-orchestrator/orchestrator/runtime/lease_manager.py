from __future__ import annotations

import threading
from typing import Any, Callable

from ..tools_rca_api import RCAApiClient


class LeaseManager:
    def __init__(
        self,
        client: RCAApiClient,
        heartbeat_interval_seconds: int,
        instance_id: str,
        log_func: Callable[[str], None] | None = None,
        execute_with_retry: Callable[[str, Callable[[], Any]], Any] | None = None,
    ) -> None:
        self._client = client
        self._heartbeat_interval_seconds = max(1, int(heartbeat_interval_seconds))
        self._instance_id = str(instance_id).strip()
        self._log_func = log_func
        self._execute_with_retry = execute_with_retry

        self._job_id = ""
        self._stop_event = threading.Event()
        self._lease_lost_event = threading.Event()
        self._lease_lost_reason = ""
        self._lock = threading.Lock()
        self._heartbeat_thread: threading.Thread | None = None

    def start(self, job_id: str) -> bool:
        normalized_job_id = str(job_id).strip()
        if not normalized_job_id:
            raise RuntimeError("job_id is required")

        claimed = self._client.start_job(normalized_job_id)
        if not claimed:
            return False

        with self._lock:
            self._job_id = normalized_job_id
            self._lease_lost_reason = ""
            self._lease_lost_event.clear()
            self._stop_event.clear()

        self._heartbeat_thread = threading.Thread(target=self._heartbeat_loop, daemon=True)
        self._heartbeat_thread.start()
        return True

    def _heartbeat_loop(self) -> None:
        while not self._stop_event.wait(self._heartbeat_interval_seconds):
            with self._lock:
                job_id = self._job_id
            if not job_id:
                return
            try:
                if self._execute_with_retry is not None:
                    self._execute_with_retry(
                        "job.heartbeat",
                        lambda: self._client.renew_job_lease(job_id),
                    )
                else:
                    self._client.renew_job_lease(job_id)
            except Exception as exc:  # noqa: BLE001
                self._mark_lease_lost(str(exc))
                self._stop_event.set()
                return

    def _mark_lease_lost(self, reason: str) -> None:
        with self._lock:
            if self._lease_lost_event.is_set():
                return
            normalized_reason = str(reason).strip() or "lease_renew_failed"
            self._lease_lost_reason = normalized_reason
            self._lease_lost_event.set()
            if self._log_func is not None:
                self._log_func(
                    "lease heartbeat failed "
                    f"job={self._job_id} instance_id={self._instance_id} reason={normalized_reason}"
                )

    def is_lease_lost(self) -> bool:
        return self._lease_lost_event.is_set()

    def lease_lost_reason(self) -> str:
        with self._lock:
            return self._lease_lost_reason

    def shutdown(self, join_timeout_seconds: float = 2.0) -> None:
        self._stop_event.set()
        thread = self._heartbeat_thread
        if thread is None:
            return
        if thread.is_alive() and thread is not threading.current_thread():
            thread.join(timeout=max(float(join_timeout_seconds), 0.0))
