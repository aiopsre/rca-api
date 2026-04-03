"""Prometheus metrics for the orchestrator worker."""
from __future__ import annotations

import time

from prometheus_client import Counter, Gauge, Histogram, generate_latest, CONTENT_TYPE_LATEST

# Worker metrics
WORKER_JOBS_TOTAL = Counter(
    "orchestrator_jobs_total",
    "Total jobs processed",
    ["status"],
)

WORKER_JOBS_IN_PROGRESS = Gauge(
    "orchestrator_jobs_in_progress",
    "Jobs currently being processed",
)

WORKER_JOB_DURATION_SECONDS = Histogram(
    "orchestrator_job_duration_seconds",
    "Job processing duration in seconds",
    ["pipeline"],
)

WORKER_POLL_ERRORS_TOTAL = Counter(
    "orchestrator_poll_errors_total",
    "Total poll loop errors",
)

WORKER_UPTIME_SECONDS = Gauge(
    "orchestrator_uptime_seconds",
    "Worker uptime in seconds",
)

# Track start time for uptime calculation
_start_time: float = time.time()


def get_uptime() -> float:
    """Get the worker uptime in seconds."""
    return time.time() - _start_time


def reset_uptime() -> None:
    """Reset the uptime start time (for testing)."""
    global _start_time
    _start_time = time.time()


def export_metrics() -> bytes:
    """Export metrics in Prometheus text format."""
    # Update uptime gauge before export
    WORKER_UPTIME_SECONDS.set(get_uptime())
    return generate_latest()


__all__ = [
    "WORKER_JOBS_TOTAL",
    "WORKER_JOBS_IN_PROGRESS",
    "WORKER_JOB_DURATION_SECONDS",
    "WORKER_POLL_ERRORS_TOTAL",
    "WORKER_UPTIME_SECONDS",
    "CONTENT_TYPE_LATEST",
    "export_metrics",
    "get_uptime",
    "reset_uptime",
]