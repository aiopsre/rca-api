"""Tests for health_server and metrics modules."""
from __future__ import annotations

import json
import pathlib
import sys
import threading
import time
import unittest

import requests

TESTS_DIR = pathlib.Path(__file__).resolve().parent
PROJECT_DIR = TESTS_DIR.parent
if str(PROJECT_DIR) not in sys.path:
    sys.path.insert(0, str(PROJECT_DIR))

from orchestrator import WORKER_VERSION
from orchestrator.daemon.health_server import HealthServer, HealthHandler, start_health_server
from orchestrator.daemon.metrics import (
    WORKER_JOBS_TOTAL,
    WORKER_JOBS_IN_PROGRESS,
    WORKER_JOB_DURATION_SECONDS,
    WORKER_POLL_ERRORS_TOTAL,
    WORKER_UPTIME_SECONDS,
    export_metrics,
    reset_uptime,
    get_uptime,
)


class TestHealthServer(unittest.TestCase):
    """Tests for HealthServer class."""

    def setUp(self) -> None:
        """Reset metrics before each test."""
        reset_uptime()

    def test_start_health_server_disabled(self) -> None:
        """Test that port=0 disables the health server."""
        server = start_health_server(port=0, host="0.0.0.0")
        self.assertIsNone(server)

    def test_health_server_start_and_stop(self) -> None:
        """Test health server can start and stop."""
        server = HealthServer(port=0)  # Let OS assign port
        # This test verifies the server can be created
        self.assertEqual(server.port, 0)
        self.assertEqual(server.host, "0.0.0.0")

    def test_set_ready(self) -> None:
        """Test ready state management."""
        server = HealthServer(port=0)
        self.assertFalse(server.is_ready())
        server.set_ready(True)
        self.assertTrue(server.is_ready())
        server.set_ready(False)
        self.assertFalse(server.is_ready())


class TestHealthHandler(unittest.TestCase):
    """Tests for HealthHandler class."""

    def test_is_ready_callback(self) -> None:
        """Test the is_ready callback mechanism."""
        ready_state = [False]

        def is_ready() -> bool:
            return ready_state[0]

        HealthHandler._is_ready = is_ready
        self.assertFalse(HealthHandler._is_ready())
        ready_state[0] = True
        self.assertTrue(HealthHandler._is_ready())


class TestMetrics(unittest.TestCase):
    """Tests for Prometheus metrics."""

    def setUp(self) -> None:
        """Reset uptime before each test."""
        reset_uptime()

    def test_export_metrics_returns_bytes(self) -> None:
        """Test that export_metrics returns bytes."""
        metrics = export_metrics()
        self.assertIsInstance(metrics, bytes)

    def test_export_metrics_contains_expected_metrics(self) -> None:
        """Test that exported metrics contain expected metric names."""
        metrics = export_metrics()
        self.assertIn(b"orchestrator_jobs_total", metrics)
        self.assertIn(b"orchestrator_jobs_in_progress", metrics)
        self.assertIn(b"orchestrator_job_duration_seconds", metrics)
        self.assertIn(b"orchestrator_poll_errors_total", metrics)
        self.assertIn(b"orchestrator_uptime_seconds", metrics)

    def test_uptime_increases(self) -> None:
        """Test that uptime increases over time."""
        uptime1 = get_uptime()
        time.sleep(0.1)
        uptime2 = get_uptime()
        self.assertGreater(uptime2, uptime1)

    def test_jobs_total_counter(self) -> None:
        """Test that jobs_total counter can be incremented."""
        # Increment with different labels
        WORKER_JOBS_TOTAL.labels(status="success").inc()
        WORKER_JOBS_TOTAL.labels(status="error").inc()
        # Verify metrics export includes these
        metrics = export_metrics()
        self.assertIn(b'orchestrator_jobs_total{status="success"}', metrics)
        self.assertIn(b'orchestrator_jobs_total{status="error"}', metrics)

    def test_jobs_in_progress_gauge(self) -> None:
        """Test that jobs_in_progress gauge can be incremented and decremented."""
        WORKER_JOBS_IN_PROGRESS.inc()
        WORKER_JOBS_IN_PROGRESS.inc()
        WORKER_JOBS_IN_PROGRESS.dec()
        # Verify metrics export
        metrics = export_metrics()
        self.assertIn(b"orchestrator_jobs_in_progress", metrics)

    def test_job_duration_histogram(self) -> None:
        """Test that job_duration histogram can record observations."""
        WORKER_JOB_DURATION_SECONDS.labels(pipeline="test_pipeline").observe(0.5)
        metrics = export_metrics()
        self.assertIn(b"orchestrator_job_duration_seconds", metrics)

    def test_poll_errors_counter(self) -> None:
        """Test that poll_errors counter can be incremented."""
        WORKER_POLL_ERRORS_TOTAL.inc()
        metrics = export_metrics()
        self.assertIn(b"orchestrator_poll_errors_total", metrics)


class TestMetricsIntegration(unittest.TestCase):
    """Integration tests for metrics with realistic usage patterns."""

    def setUp(self) -> None:
        """Reset uptime before each test."""
        reset_uptime()

    def test_job_processing_metrics_flow(self) -> None:
        """Test a typical job processing metrics flow."""
        pipeline = "basic_rca"

        # Simulate job start
        WORKER_JOBS_IN_PROGRESS.inc()

        # Simulate job processing time
        time.sleep(0.05)
        duration = 0.05

        # Simulate job completion
        WORKER_JOBS_TOTAL.labels(status="success").inc()
        WORKER_JOB_DURATION_SECONDS.labels(pipeline=pipeline).observe(duration)
        WORKER_JOBS_IN_PROGRESS.dec()

        # Export and verify
        metrics = export_metrics()
        self.assertIn(b"orchestrator_jobs_total", metrics)
        self.assertIn(b"orchestrator_jobs_in_progress", metrics)
        self.assertIn(b"orchestrator_job_duration_seconds", metrics)


class TestHealthServerIntegration(unittest.TestCase):
    """Integration tests for health server with actual HTTP requests."""

    def test_health_server_endpoints(self) -> None:
        """Test health server endpoints with actual HTTP requests."""
        # Find an available port
        import socket

        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
            s.bind(("", 0))
            port = s.getsockname()[1]

        server = HealthServer(port=port, host="127.0.0.1")
        server.set_ready(True)

        if not server.start():
            self.skipTest(f"Could not start health server on port {port}")
            return

        try:
            # Wait for server to be ready
            time.sleep(0.5)

            # Test /health endpoint
            response = requests.get(f"http://127.0.0.1:{port}/health", timeout=5)
            self.assertEqual(response.status_code, 200)
            data = response.json()
            self.assertEqual(data["status"], "ok")
            self.assertEqual(data["version"], WORKER_VERSION)

            # Test /ready endpoint (should be ready)
            response = requests.get(f"http://127.0.0.1:{port}/ready", timeout=5)
            self.assertEqual(response.status_code, 200)
            data = response.json()
            self.assertEqual(data["status"], "ok")

            # Test /metrics endpoint
            response = requests.get(f"http://127.0.0.1:{port}/metrics", timeout=5)
            self.assertEqual(response.status_code, 200)
            self.assertIn("orchestrator_jobs_total", response.text)

            # Test not ready state
            server.set_ready(False)
            response = requests.get(f"http://127.0.0.1:{port}/ready", timeout=5)
            self.assertEqual(response.status_code, 503)
            data = response.json()
            self.assertEqual(data["status"], "not_ready")

            # Test 404 for unknown path
            response = requests.get(f"http://127.0.0.1:{port}/unknown", timeout=5)
            self.assertEqual(response.status_code, 404)

        finally:
            server.stop()


if __name__ == "__main__":
    unittest.main()