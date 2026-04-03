from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Dict


def _normalized_reasons(raw_reasons: tuple[str, ...]) -> list[str]:
    reasons: list[str] = []
    for item in raw_reasons:
        reason = str(item).strip()
        if not reason:
            continue
        reasons.append(reason)
    if not reasons:
        reasons.append("default_rank")
    return reasons


@dataclass(frozen=True)
class Candidate:
    name: str
    query_type: str
    score: float
    reasons: tuple[str, ...]
    params: Dict[str, Any] = field(default_factory=dict, compare=False)

    def to_plan_dict(self) -> dict[str, Any]:
        return {
            "name": self.name,
            "type": self.query_type,
            "score": round(float(self.score), 6),
            "reasons": _normalized_reasons(self.reasons),
            "params": dict(self.params),
        }


class BudgetTracker:
    def __init__(self, max_calls: int, max_total_bytes: int, max_total_latency_ms: int) -> None:
        self.max_calls = max(int(max_calls), 0)
        self.max_total_bytes = max(int(max_total_bytes), 0)
        self.max_total_latency_ms = max(int(max_total_latency_ms), 0)
        self.calls = 0
        self.total_bytes = 0
        self.total_latency_ms = 0

    def budget_snapshot(self) -> dict[str, int]:
        return {
            "max_calls": self.max_calls,
            "max_total_bytes": self.max_total_bytes,
            "max_total_latency_ms": self.max_total_latency_ms,
        }

    def used_snapshot(self) -> dict[str, int]:
        return {
            "calls": self.calls,
            "total_bytes": self.total_bytes,
            "total_latency_ms": self.total_latency_ms,
        }

    def can_execute_query(self) -> bool:
        if self.calls >= self.max_calls:
            return False
        if self.total_bytes >= self.max_total_bytes:
            return False
        if self.total_latency_ms >= self.max_total_latency_ms:
            return False
        return True

    def record_query(self, result_bytes: int, latency_ms: int) -> None:
        self.calls += 1
        self.total_bytes += max(int(result_bytes), 0)
        self.total_latency_ms += max(int(latency_ms), 0)


def build_candidates(context: dict[str, Any]) -> list[Candidate]:
    service = str(context.get("service") or "").strip()
    namespace = str(context.get("namespace") or "").strip()
    severity = str(context.get("severity") or "").strip().upper()

    scope_reason = "service_scoped" if service else ("namespace_scoped" if namespace else "broad_scope")
    severity_boost = 0.06 if severity in {"P0", "P1"} else (0.03 if severity == "P2" else 0.0)

    logs_selector = '{app="%s"}' % service if service else '{namespace="%s"}' % namespace if namespace else '{job=~".+"}'
    logs_query = f"{logs_selector} |= \"error\""

    return [
        Candidate(
            name="query_metrics:apiserver_5xx_rate",
            query_type="metrics",
            score=0.90 + severity_boost,
            reasons=("high_signal", "low_cost", scope_reason),
            params={
                "expr": "sum(up)",
                "window_seconds": 600,
                "step_seconds": 30,
            },
        ),
        Candidate(
            name="query_metrics:apiserver_latency",
            query_type="metrics",
            score=0.78 + severity_boost,
            reasons=("high_signal", "low_cost", scope_reason),
            params={
                "expr": "avg(up)",
                "window_seconds": 600,
                "step_seconds": 30,
            },
        ),
        Candidate(
            name="query_logs:error_stacktrace",
            query_type="logs",
            score=0.58 + severity_boost,
            reasons=("discriminative", "expensive", scope_reason),
            params={
                "query": logs_query,
                "window_seconds": 600,
                "limit": 200,
            },
        ),
    ]


def rank_candidates(candidates: list[Candidate], context: dict[str, Any]) -> list[Candidate]:
    del context
    normalized: list[Candidate] = []
    for item in candidates:
        reasons = _normalized_reasons(tuple(item.reasons))
        normalized.append(
            Candidate(
                name=item.name,
                query_type=item.query_type,
                score=float(item.score),
                reasons=tuple(reasons),
                params=dict(item.params),
            )
        )
    return sorted(normalized, key=lambda item: (-item.score, item.name))
