"""HTTP health server for the orchestrator worker."""
from __future__ import annotations

import json
import logging
import threading
from http.server import HTTPServer, BaseHTTPRequestHandler
from typing import Callable

from .. import WORKER_VERSION
from .metrics import export_metrics, CONTENT_TYPE_LATEST

_logger = logging.getLogger("orchestrator.health_server")


class HealthHandler(BaseHTTPRequestHandler):
    """HTTP request handler for health endpoints."""

    # Class-level callbacks set by HealthServer
    _is_ready: Callable[[], bool] = lambda: False

    def log_message(self, format: str, *args) -> None:  # type: ignore[override]
        """Override to use our logger instead of stderr."""
        _logger.debug("%s - %s", self.address_string(), format % args)

    def _send_json_response(self, status_code: int, data: dict) -> None:
        """Send a JSON response."""
        body = json.dumps(data).encode("utf-8")
        self.send_response(status_code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self) -> None:  # noqa: N802
        """Handle GET requests."""
        if self.path == "/health":
            self._handle_health()
        elif self.path == "/ready":
            self._handle_ready()
        elif self.path == "/metrics":
            self._handle_metrics()
        else:
            self.send_error(404, "Not Found")

    def _handle_health(self) -> None:
        """Handle /health endpoint - always returns ok if server is running."""
        self._send_json_response(200, {"status": "ok", "version": WORKER_VERSION})

    def _handle_ready(self) -> None:
        """Handle /ready endpoint - checks if worker is ready to process jobs."""
        if self._is_ready():
            self._send_json_response(200, {"status": "ok"})
        else:
            self._send_json_response(503, {"status": "not_ready"})

    def _handle_metrics(self) -> None:
        """Handle /metrics endpoint - Prometheus metrics."""
        body = export_metrics()
        self.send_response(200)
        self.send_header("Content-Type", CONTENT_TYPE_LATEST)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


class HealthServer:
    """HTTP server providing health and metrics endpoints."""

    def __init__(self, port: int, host: str = "0.0.0.0"):
        """Initialize the health server.

        Args:
            port: Port to listen on (0 to disable).
            host: Host to bind to.
        """
        self.port = port
        self.host = host
        self._ready = False
        self._server: HTTPServer | None = None
        self._thread: threading.Thread | None = None

    def set_ready(self, ready: bool) -> None:
        """Set the ready state for the /ready endpoint."""
        self._ready = ready

    def is_ready(self) -> bool:
        """Check if the server is ready."""
        return self._ready

    def start(self) -> bool:
        """Start the health server in a background thread.

        Returns:
            True if server started successfully, False if disabled or failed.
        """
        if self.port <= 0:
            _logger.info("health server disabled (HEALTH_PORT=0)")
            return False

        try:
            # Set the ready callback on the handler class
            HealthHandler._is_ready = self.is_ready

            self._server = HTTPServer((self.host, self.port), HealthHandler)
            self._thread = threading.Thread(
                target=self._server.serve_forever,
                daemon=True,
                name="health-server",
            )
            self._thread.start()
            _logger.info(f"health server listening on {self.host}:{self.port}")
            return True
        except OSError as e:
            _logger.error(f"failed to start health server on port {self.port}: {e}")
            return False

    def stop(self) -> None:
        """Stop the health server."""
        if self._server:
            _logger.info("stopping health server")
            self._server.shutdown()
            self._server = None
        if self._thread and self._thread.is_alive():
            self._thread.join(timeout=5.0)
            self._thread = None


def start_health_server(port: int, host: str = "0.0.0.0") -> HealthServer | None:
    """Create and start a health server.

    Args:
        port: Port to listen on (0 to disable).
        host: Host to bind to.

    Returns:
        HealthServer instance if started, None if disabled.
    """
    if port <= 0:
        return None

    server = HealthServer(port=port, host=host)
    if server.start():
        return server
    return None


__all__ = [
    "HealthServer",
    "HealthHandler",
    "start_health_server",
]