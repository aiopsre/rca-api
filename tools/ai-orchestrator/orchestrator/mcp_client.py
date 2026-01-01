from __future__ import annotations

import json
import random
import time
from dataclasses import dataclass
from typing import Any, Dict, Optional

import requests

from .tool_registry import get_tool, iter_tool_names


MCP_CODE_SCOPE_DENIED = "SCOPE_DENIED"
MCP_CODE_INVALID_ARGUMENT = "INVALID_ARGUMENT"
MCP_CODE_NOT_FOUND = "NOT_FOUND"
MCP_CODE_RATE_LIMITED = "RATE_LIMITED"

_MCP_NON_RETRYABLE_CODES = {
    MCP_CODE_SCOPE_DENIED,
    MCP_CODE_INVALID_ARGUMENT,
    MCP_CODE_NOT_FOUND,
}


@dataclass(frozen=True)
class MCPCallError(Exception):
    message: str
    status_code: int
    error_code: str
    body: str
    retryable: bool

    def __str__(self) -> str:  # pragma: no cover - dataclass repr is not useful for runtime logs.
        return self.message


class MCPClient:
    def __init__(
        self,
        base_url: str,
        scopes: str | None,
        timeout_s: float = 10.0,
        max_retries: int = 3,
        verify_remote_tools: bool = False,
    ) -> None:
        self.base_url = base_url.rstrip("/")
        self.scopes = (scopes or "").strip()
        self.timeout_s = max(float(timeout_s), 0.1)
        self.max_retries = max(int(max_retries), 1)
        self.session = requests.Session()
        self.session.headers.update({"Accept": "application/json"})
        if self.scopes:
            self.session.headers.update({"X-Scopes": self.scopes})

        if verify_remote_tools:
            self.verify_remote_tools()

    def call(
        self,
        tool: str,
        input_payload: Dict[str, Any] | None,
        idempotency_key: str | None = None,
    ) -> Dict[str, Any]:
        normalized_tool = str(tool).strip()
        if not normalized_tool:
            raise RuntimeError("mcp tool name is required")
        if get_tool(normalized_tool) is None:
            raise RuntimeError(f"unsupported mcp tool: {normalized_tool}")
        if not self.scopes:
            raise MCPCallError(
                message=(
                    f"mcp call denied by default because RCA_API_SCOPES is empty: "
                    f"tool={normalized_tool}, code={MCP_CODE_SCOPE_DENIED}"
                ),
                status_code=403,
                error_code=MCP_CODE_SCOPE_DENIED,
                body="",
                retryable=False,
            )

        payload: Dict[str, Any] = {
            "tool": normalized_tool,
            "input": input_payload or {},
        }
        idem = str(idempotency_key or "").strip()
        if idem:
            payload["idempotency_key"] = idem

        url = f"{self.base_url}/v1/mcp/tools/call"
        for attempt in range(1, self.max_retries + 1):
            try:
                response = self.session.post(url, json=payload, timeout=self.timeout_s)
            except requests.RequestException as exc:
                err = MCPCallError(
                    message=f"POST /v1/mcp/tools/call network error on attempt={attempt}: {exc}",
                    status_code=0,
                    error_code="NETWORK_ERROR",
                    body="",
                    retryable=True,
                )
                if attempt >= self.max_retries:
                    raise err from exc
                self._sleep_retry_backoff(attempt)
                continue

            body_text = response.text.strip()
            response_payload = self._decode_json_dict(body_text)

            if response.ok:
                if response_payload is None:
                    raise RuntimeError(
                        f"POST /v1/mcp/tools/call returned non-JSON success response for tool={normalized_tool}"
                    )
                return response_payload

            error_code = self._extract_error_code(response_payload)
            retryable = self._is_retryable(response.status_code, error_code)
            message = self._extract_error_message(
                response_status_code=response.status_code,
                response_payload=response_payload,
                body_text=body_text,
                tool=normalized_tool,
            )
            err = MCPCallError(
                message=message,
                status_code=response.status_code,
                error_code=error_code,
                body=body_text,
                retryable=retryable,
            )
            if not retryable or attempt >= self.max_retries:
                raise err
            self._sleep_retry_backoff(attempt)

        raise RuntimeError(f"mcp call exhausted retries unexpectedly: tool={normalized_tool}")

    def verify_remote_tools(self) -> None:
        url = f"{self.base_url}/v1/mcp/tools"
        response = self.session.get(url, timeout=self.timeout_s)
        body_text = response.text.strip()
        if not response.ok:
            raise RuntimeError(f"GET /v1/mcp/tools failed: http={response.status_code}, body={body_text}")

        payload = self._decode_json_dict(body_text)
        if payload is None:
            raise RuntimeError(f"GET /v1/mcp/tools returned non-JSON body: {body_text}")

        tools = payload.get("tools")
        if not isinstance(tools, list):
            raise RuntimeError(f"GET /v1/mcp/tools invalid payload: {payload}")

        remote_names: set[str] = set()
        for item in tools:
            if isinstance(item, dict):
                name = str(item.get("name") or "").strip()
                if name:
                    remote_names.add(name)

        expected = set(iter_tool_names())
        missing = sorted(expected - remote_names)
        if missing:
            raise RuntimeError(f"mcp remote registry missing expected tools: {missing}")

    @staticmethod
    def _decode_json_dict(raw: str) -> Optional[Dict[str, Any]]:
        if not raw:
            return {}
        try:
            payload = json.loads(raw)
        except json.JSONDecodeError:
            return None
        if isinstance(payload, dict):
            return payload
        return None

    @staticmethod
    def _extract_error_code(payload: Optional[Dict[str, Any]]) -> str:
        if not isinstance(payload, dict):
            return ""
        error_obj = payload.get("error")
        if not isinstance(error_obj, dict):
            return ""
        return str(error_obj.get("code") or "").strip().upper()

    @staticmethod
    def _extract_error_message(
        response_status_code: int,
        response_payload: Optional[Dict[str, Any]],
        body_text: str,
        tool: str,
    ) -> str:
        if isinstance(response_payload, dict):
            error_obj = response_payload.get("error")
            if isinstance(error_obj, dict):
                message = str(error_obj.get("message") or "").strip()
                code = str(error_obj.get("code") or "").strip()
                if message:
                    return f"mcp call failed: tool={tool}, http={response_status_code}, code={code}, message={message}"
        return f"mcp call failed: tool={tool}, http={response_status_code}, body={body_text}"

    @staticmethod
    def _is_retryable(status_code: int, error_code: str) -> bool:
        if error_code in _MCP_NON_RETRYABLE_CODES:
            return False
        if error_code == MCP_CODE_RATE_LIMITED:
            return True
        if status_code >= 500:
            return True
        return False

    @staticmethod
    def _sleep_retry_backoff(attempt: int) -> None:
        base = 0.25 * (2 ** max(attempt - 1, 0))
        jitter = random.uniform(0.0, 0.2)  # noqa: S311 - non-crypto jitter for retry backoff.
        time.sleep(min(base + jitter, 2.5))
