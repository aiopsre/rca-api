#!/usr/bin/env python3
from __future__ import annotations

import json
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer


def _write(handler: BaseHTTPRequestHandler, status: int, payload: dict[str, object]) -> None:
    raw = json.dumps(payload).encode("utf-8")
    handler.send_response(status)
    handler.send_header("Content-Type", "application/json")
    handler.send_header("Content-Length", str(len(raw)))
    handler.end_headers()
    handler.wfile.write(raw)


class Handler(BaseHTTPRequestHandler):
    def log_message(self, format: str, *args: object) -> None:  # noqa: A003
        return

    def do_GET(self) -> None:  # noqa: N802
        if self.path == "/healthz":
            _write(self, 200, {"ok": True})
            return
        if self.path.startswith("/api/v1/query_range"):
            _write(
                self,
                200,
                {
                    "status": "success",
                    "data": {
                        "resultType": "matrix",
                        "result": [
                            {
                                "metric": {"__name__": "http_requests_total", "job": "mock"},
                                "values": [[1710000000, "0.42"]],
                            }
                        ],
                    },
                },
            )
            return
        _write(self, 404, {"error": "not_found"})

    def do_POST(self) -> None:  # noqa: N802
        if self.path.startswith("/_search"):
            _write(
                self,
                200,
                {
                    "hits": {
                        "hits": [
                            {
                                "_source": {
                                    "@timestamp": "2026-03-15T00:00:00Z",
                                    "service.name": "mock-service",
                                    "message": "mock elasticsearch result",
                                }
                            }
                        ]
                    }
                },
            )
            return
        _write(self, 404, {"error": "not_found"})


def main() -> None:
    host = sys.argv[1]
    port = int(sys.argv[2])
    HTTPServer((host, port), Handler).serve_forever()


if __name__ == "__main__":
    main()
