from __future__ import annotations

from dataclasses import dataclass
import time
from typing import Any, Callable, TypeVar

from ..sdk.errors import RCAApiError


T = TypeVar("T")


@dataclass(frozen=True)
class RetryPolicy:
    max_attempts: int = 3
    base_delay_seconds: float = 0.2
    max_delay_seconds: float = 2.0

    def normalized(self) -> "RetryPolicy":
        max_attempts = max(int(self.max_attempts), 1)
        base_delay_seconds = max(float(self.base_delay_seconds), 0.0)
        max_delay_seconds = max(float(self.max_delay_seconds), base_delay_seconds)
        return RetryPolicy(
            max_attempts=max_attempts,
            base_delay_seconds=base_delay_seconds,
            max_delay_seconds=max_delay_seconds,
        )


class RetryExecutor:
    def __init__(
        self,
        policy: RetryPolicy | None = None,
        log_func: Callable[[str], None] | None = None,
        sleep_func: Callable[[float], None] | None = None,
    ) -> None:
        self._policy = (policy or RetryPolicy()).normalized()
        self._log_func = log_func
        self._sleep_func = sleep_func or time.sleep

    def run(self, operation: str, fn: Callable[[], T]) -> T:
        op = str(operation).strip() or "operation"
        attempt = 1
        while True:
            try:
                return fn()
            except Exception as exc:  # noqa: BLE001
                if not self._should_retry(exc):
                    raise
                if attempt >= self._policy.max_attempts:
                    raise
                delay = self._compute_delay(attempt)
                if self._log_func is not None:
                    self._log_func(
                        "retrying operation "
                        f"op={op} attempt={attempt}/{self._policy.max_attempts} delay_s={delay:.3f} error={exc}"
                    )
                self._sleep_func(delay)
                attempt += 1

    def _should_retry(self, exc: Exception) -> bool:
        if isinstance(exc, RCAApiError):
            return exc.retryable
        return False

    def _compute_delay(self, attempt: int) -> float:
        if self._policy.base_delay_seconds <= 0:
            return 0.0
        exp_value = self._policy.base_delay_seconds * (2 ** max(attempt - 1, 0))
        return min(exp_value, self._policy.max_delay_seconds)

    @property
    def policy(self) -> RetryPolicy:
        return self._policy

