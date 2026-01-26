from __future__ import annotations

import requests


def parse_prometheus_metric_value(line: str) -> float:
    if not line:
        return 0.0
    parts = line.strip().split()
    if not parts:
        return 0.0
    try:
        return float(parts[-1])
    except ValueError:
        return 0.0


def detect_pubsub_ready(base_url: str, scopes: str, timeout_s: float = 2.0) -> tuple[bool, bool]:
    url = f"{base_url.rstrip('/')}/metrics"
    headers = {"Accept": "text/plain"}
    if scopes:
        headers["X-Scopes"] = scopes
    try:
        response = requests.get(url, headers=headers, timeout=max(timeout_s, 0.5))
    except Exception:  # noqa: BLE001
        return False, False
    if not response.ok:
        return False, False

    found = False
    ready = False
    for raw in response.text.splitlines():
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        if line.startswith("redis_pubsub_subscribe_ready"):
            found = True
            if parse_prometheus_metric_value(line) > 0:
                ready = True
            continue
        if not found and line.startswith("redis_pubsub_subscribe_state"):
            found = True
            if parse_prometheus_metric_value(line) > 0:
                ready = True
    return found, ready
