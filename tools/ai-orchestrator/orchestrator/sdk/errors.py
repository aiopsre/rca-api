from __future__ import annotations

from enum import Enum
import json
from typing import Any


class OrchestratorErrorCategory(str, Enum):
    MISSING_OWNER = "missing_owner"
    OWNER_LOST = "owner_lost"
    RETRYABLE_TRANSPORT = "retryable_transport"
    RETRYABLE_5XX = "retryable_5xx"
    BAD_REQUEST = "bad_request"
    PERMISSION = "permission"
    UNAUTHENTICATED = "unauthenticated"
    NOT_FOUND = "not_found"
    RATE_LIMITED = "rate_limited"
    CONFLICT = "conflict"
    UNKNOWN = "unknown"


def _coerce_error_code(value: Any) -> int | str | None:
    if value is None:
        return None
    if isinstance(value, bool):
        return int(value)
    if isinstance(value, int):
        return value
    if isinstance(value, float):
        return int(value)
    if isinstance(value, str):
        trimmed = value.strip()
        if not trimmed:
            return None
        try:
            return int(trimmed)
        except ValueError:
            return trimmed
    return str(value)


def _normalize_text(value: Any) -> str:
    if isinstance(value, str):
        return value.strip().lower()
    if value is None:
        return ""
    try:
        return json.dumps(value, ensure_ascii=False, sort_keys=True).lower()
    except Exception:  # noqa: BLE001
        return str(value).strip().lower()


def classify_api_error(
    *,
    http_status: int | None,
    envelope_code: int | str | None,
    message: str,
    details: Any = None,
    path: str = "",
) -> OrchestratorErrorCategory:
    code_num: int | None = envelope_code if isinstance(envelope_code, int) else None
    code_text = _normalize_text(envelope_code)
    message_text = _normalize_text(message)
    details_text = _normalize_text(details)
    all_text = " ".join(part for part in [code_text, message_text, details_text] if part)
    path_text = str(path).strip().lower()

    missing_owner_keywords = [
        "x-orchestrator-instance-id",
        "orchestrator instance",
        "lease owner",
        "lease_owner",
        "owner required",
        "missing header",
    ]
    if any(keyword in all_text for keyword in missing_owner_keywords):
        return OrchestratorErrorCategory.MISSING_OWNER

    lease_endpoint_suffixes = ("/start", "/heartbeat", "/finalize", "/tool-calls")
    if (
        http_status == 400
        and "/v1/ai/jobs/" in path_text
        and path_text.endswith(lease_endpoint_suffixes)
        and ("invalidargument" in all_text or "invalid argument" in all_text)
    ):
        return OrchestratorErrorCategory.MISSING_OWNER

    owner_lost_keywords = [
        "invalidtransition",
        "status transition",
        "status conflict",
        "lease",
        "owner mismatch",
    ]
    if (http_status == 409 or "conflict" in code_text) and any(keyword in all_text for keyword in owner_lost_keywords):
        return OrchestratorErrorCategory.OWNER_LOST

    if code_num is not None:
        code_prefix = str(abs(code_num))
        if code_prefix.startswith("4011"):
            return OrchestratorErrorCategory.UNAUTHENTICATED
        if code_prefix.startswith("4031"):
            return OrchestratorErrorCategory.PERMISSION
        if code_prefix.startswith("4041"):
            return OrchestratorErrorCategory.NOT_FOUND
        if code_prefix.startswith("4091"):
            if "/v1/ai/jobs/" in path_text:
                return OrchestratorErrorCategory.OWNER_LOST
            return OrchestratorErrorCategory.CONFLICT
        if code_prefix.startswith("4291"):
            return OrchestratorErrorCategory.RATE_LIMITED
        if code_prefix.startswith("5001") or code_prefix.startswith("5021") or code_prefix.startswith("5041"):
            return OrchestratorErrorCategory.RETRYABLE_5XX
        if code_prefix.startswith("4001"):
            return OrchestratorErrorCategory.BAD_REQUEST

    if code_text:
        if "unauthenticated" in code_text:
            return OrchestratorErrorCategory.UNAUTHENTICATED
        if "permission" in code_text or "forbidden" in code_text:
            return OrchestratorErrorCategory.PERMISSION
        if "notfound" in code_text or "not_found" in code_text:
            return OrchestratorErrorCategory.NOT_FOUND
        if "ratelimit" in code_text or "rate_limit" in code_text:
            return OrchestratorErrorCategory.RATE_LIMITED
        if "badrequest" in code_text or "invalidargument" in code_text:
            return OrchestratorErrorCategory.BAD_REQUEST
        if "conflict" in code_text:
            if "/v1/ai/jobs/" in path_text:
                return OrchestratorErrorCategory.OWNER_LOST
            return OrchestratorErrorCategory.CONFLICT
        if "internalerror" in code_text or "dependency" in code_text:
            return OrchestratorErrorCategory.RETRYABLE_5XX

    if http_status is not None:
        if http_status == 401:
            return OrchestratorErrorCategory.UNAUTHENTICATED
        if http_status == 403:
            return OrchestratorErrorCategory.PERMISSION
        if http_status == 404:
            return OrchestratorErrorCategory.NOT_FOUND
        if http_status == 409:
            if "/v1/ai/jobs/" in path_text:
                return OrchestratorErrorCategory.OWNER_LOST
            return OrchestratorErrorCategory.CONFLICT
        if http_status == 429:
            return OrchestratorErrorCategory.RATE_LIMITED
        if http_status >= 500:
            return OrchestratorErrorCategory.RETRYABLE_5XX
        if http_status == 400:
            return OrchestratorErrorCategory.BAD_REQUEST

    return OrchestratorErrorCategory.UNKNOWN


class RCAApiError(RuntimeError):
    def __init__(
        self,
        *,
        category: OrchestratorErrorCategory,
        message: str,
        method: str,
        path: str,
        http_status: int | None = None,
        envelope_code: int | str | None = None,
        envelope_message: str | None = None,
        details: Any = None,
        payload: dict[str, Any] | None = None,
        body_text: str | None = None,
        cause: Exception | None = None,
    ) -> None:
        super().__init__(message)
        self.category = category
        self.method = method.upper()
        self.path = path
        self.http_status = http_status
        self.envelope_code = _coerce_error_code(envelope_code)
        self.envelope_message = envelope_message
        self.details = details
        self.payload = payload
        self.body_text = body_text
        self.cause = cause

    @property
    def retryable(self) -> bool:
        return self.category in {
            OrchestratorErrorCategory.RETRYABLE_TRANSPORT,
            OrchestratorErrorCategory.RETRYABLE_5XX,
            OrchestratorErrorCategory.RATE_LIMITED,
        }

    @classmethod
    def from_transport_error(
        cls,
        *,
        method: str,
        path: str,
        cause: Exception,
    ) -> "RCAApiError":
        message = f"{method.upper()} {path} transport error: {cause}"
        return cls(
            category=OrchestratorErrorCategory.RETRYABLE_TRANSPORT,
            message=message,
            method=method,
            path=path,
            cause=cause,
        )

    @classmethod
    def from_response(
        cls,
        *,
        method: str,
        path: str,
        http_status: int,
        envelope_code: Any = None,
        envelope_message: Any = None,
        details: Any = None,
        payload: dict[str, Any] | None = None,
        body_text: str | None = None,
    ) -> "RCAApiError":
        normalized_code = _coerce_error_code(envelope_code)
        message_text = str(envelope_message or "").strip()
        if not message_text:
            message_text = f"http={http_status}"
        category = classify_api_error(
            http_status=http_status,
            envelope_code=normalized_code,
            message=message_text,
            details=details,
            path=path,
        )
        message = (
            f"{method.upper()} {path} failed: category={category.value} "
            f"http={http_status} code={normalized_code!r} message={message_text}"
        )
        return cls(
            category=category,
            message=message,
            method=method,
            path=path,
            http_status=http_status,
            envelope_code=normalized_code,
            envelope_message=message_text,
            details=details,
            payload=payload,
            body_text=body_text,
        )
