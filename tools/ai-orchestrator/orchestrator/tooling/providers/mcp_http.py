from __future__ import annotations

import json
from typing import Any

import requests

from ...sdk.errors import OrchestratorErrorCategory, RCAApiError


class MCPHttpProvider:
    """MCP provider that supports both HTTP and SSE session transports.

    The historical runtime only knew how to POST to /v1/mcp/tools/call.
    Some MCP servers expose the legacy SSE session flow instead:

    - GET /sse returns an `endpoint` event with a session-specific POST URL
    - POSTing JSON-RPC requests to that endpoint drives initialize/call_tool

    This provider auto-detects SSE support by probing `/sse` once and then
    prefers SSE when available. If no SSE endpoint is detected, it falls back
    to the original HTTP transport.
    """

    _PATH = "/v1/mcp/tools/call"

    def __init__(
        self,
        *,
        base_url: str,
        scopes: str = "",
        timeout_s: float = 10.0,
    ) -> None:
        normalized_base_url = str(base_url).strip().rstrip("/")
        if not normalized_base_url:
            raise ValueError("mcp_http base_url is required")

        self._base_url = normalized_base_url
        self._sse_url = self._normalize_sse_url(normalized_base_url)
        self._timeout_s = max(float(timeout_s), 0.1)
        self._session = requests.Session()
        self._session.headers.update({"Accept": "application/json"})
        normalized_scopes = str(scopes).strip()
        if normalized_scopes:
            self._session.headers.update({"X-Scopes": normalized_scopes})

        self._transport_mode: str | None = None

    def call(
        self,
        *,
        tool: str,
        input_payload: dict[str, Any] | None,
        idempotency_key: str | None = None,
    ) -> dict[str, Any]:
        normalized_tool = str(tool).strip()
        if not normalized_tool:
            raise RuntimeError("tool name is required")

        if self._should_use_sse():
            return self._call_via_sse(
                tool=normalized_tool,
                input_payload=input_payload,
                idempotency_key=idempotency_key,
            )
        return self._call_via_http(
            tool=normalized_tool,
            input_payload=input_payload,
            idempotency_key=idempotency_key,
        )

    def _call_via_http(
        self,
        *,
        tool: str,
        input_payload: dict[str, Any] | None,
        idempotency_key: str | None = None,
    ) -> dict[str, Any]:
        body: dict[str, Any] = {
            "tool": tool,
            "input": input_payload or {},
        }
        normalized_idempotency_key = str(idempotency_key or "").strip()
        if normalized_idempotency_key:
            body["idempotency_key"] = normalized_idempotency_key

        try:
            response = self._session.post(
                f"{self._base_url}{self._PATH}",
                json=body,
                timeout=self._timeout_s,
            )
        except requests.RequestException as exc:
            raise RCAApiError.from_transport_error(
                method="POST",
                path=self._PATH,
                cause=exc,
            ) from exc

        body_text = response.text.strip()
        payload = self._decode_json_dict(body_text)

        if response.ok:
            if payload is None:
                raise RCAApiError(
                    category=OrchestratorErrorCategory.UNKNOWN,
                    message=f"POST {self._PATH} returned non-JSON body",
                    method="POST",
                    path=self._PATH,
                    http_status=response.status_code,
                    body_text=body_text,
                )
            return payload

        envelope_code, envelope_message, details = self._extract_error_parts(payload)
        raise RCAApiError.from_response(
            method="POST",
            path=self._PATH,
            http_status=response.status_code,
            envelope_code=envelope_code,
            envelope_message=envelope_message or body_text or f"http={response.status_code}",
            details=details,
            payload=payload if isinstance(payload, dict) else None,
            body_text=body_text,
        )

    def _call_via_sse(
        self,
        *,
        tool: str,
        input_payload: dict[str, Any] | None,
        idempotency_key: str | None = None,
    ) -> dict[str, Any]:
        try:
            import anyio
            from mcp.client.session import ClientSession
            from mcp.client.sse import sse_client
        except Exception as exc:  # pragma: no cover - runtime dependency issue.
            raise RuntimeError("MCP SSE transport requires mcp and anyio packages") from exc

        normalized_input = input_payload if isinstance(input_payload, dict) else {}
        normalized_idempotency_key = str(idempotency_key or "").strip()

        async def _run() -> dict[str, Any]:
            headers = {"Accept": "application/json, text/event-stream"}
            async with sse_client(
                self._sse_url,
                headers=headers,
                timeout=self._timeout_s,
                sse_read_timeout=max(self._timeout_s * 6.0, 60.0),
            ) as streams:
                async with ClientSession(*streams) as session:
                    await session.initialize()
                    request_payload = dict(normalized_input)
                    if normalized_idempotency_key:
                        request_payload["idempotency_key"] = normalized_idempotency_key
                    result = await session.call_tool(tool, request_payload)
                    if hasattr(result, "model_dump"):
                        return result.model_dump(by_alias=True, exclude_none=True)
                    if isinstance(result, dict):
                        return dict(result)
                    raise RuntimeError(
                        f"unexpected MCP SSE tool result type: tool={tool} type={type(result).__name__}"
                    )

        try:
            return anyio.run(_run)
        except Exception as exc:
            raise self._wrap_sse_error(tool, exc) from exc

    def verify_remote_tools(self) -> None:
        if self._should_use_sse():
            self._verify_remote_tools_sse()
            return

        url = f"{self._base_url}/v1/mcp/tools"
        response = self._session.get(url, timeout=self._timeout_s)
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

    def _verify_remote_tools_sse(self) -> None:
        try:
            import anyio
            from mcp.client.session import ClientSession
            from mcp.client.sse import sse_client
        except Exception as exc:  # pragma: no cover - runtime dependency issue.
            raise RuntimeError("MCP SSE transport requires mcp and anyio packages") from exc

        async def _run() -> None:
            async with sse_client(
                self._sse_url,
                headers={"Accept": "application/json, text/event-stream"},
                timeout=self._timeout_s,
                sse_read_timeout=max(self._timeout_s * 6.0, 60.0),
            ) as streams:
                async with ClientSession(*streams) as session:
                    await session.initialize()
                    result = await session.list_tools()
                    remote_names: set[str] = set()
                    for tool in getattr(result, "tools", []) or []:
                        if isinstance(tool, dict):
                            name = str(tool.get("name") or "").strip()
                        else:
                            name = str(getattr(tool, "name", "") or "").strip()
                        if name:
                            remote_names.add(name)

                    expected = set(iter_tool_names())
                    missing = sorted(expected - remote_names)
                    if missing:
                        raise RuntimeError(f"mcp remote registry missing expected tools: {missing}")

        anyio.run(_run)

    def _should_use_sse(self) -> bool:
        if self._transport_mode == "sse":
            return True
        if self._transport_mode == "http":
            return False
        if self._base_url.endswith("/sse"):
            self._transport_mode = "sse"
            return True
        if self._probe_sse_endpoint():
            self._transport_mode = "sse"
            return True
        self._transport_mode = "http"
        return False

    def _probe_sse_endpoint(self) -> bool:
        try:
            response = self._session.get(
                self._sse_url,
                headers={"Accept": "text/event-stream"},
                stream=True,
                timeout=min(self._timeout_s, 3.0),
            )
        except requests.RequestException:
            return False

        try:
            if not response.ok:
                return False
            saw_endpoint = False
            for line in response.iter_lines(decode_unicode=True):
                if not isinstance(line, str):
                    continue
                if line.startswith("event: endpoint"):
                    continue
                if line.startswith("data: "):
                    data = line[6:].strip()
                    if data:
                        saw_endpoint = True
                        break
            return saw_endpoint
        finally:
            response.close()

    @staticmethod
    def _normalize_sse_url(base_url: str) -> str:
        normalized = str(base_url or "").strip().rstrip("/")
        if not normalized:
            return ""
        if normalized.endswith("/sse"):
            return normalized
        return f"{normalized}/sse"

    def _wrap_sse_error(self, tool: str, exc: Exception) -> RCAApiError:
        exc = self._unwrap_exception(exc)
        message = f"MCP SSE call failed: tool={tool}, error={exc}"
        body = str(exc)
        category = OrchestratorErrorCategory.UNKNOWN
        http_status = 500
        envelope_code: int | str | None = "MCP_SSE_ERROR"
        envelope_message = message
        details: Any = None

        try:
            from mcp.shared.exceptions import McpError
        except Exception:  # pragma: no cover - runtime dependency issue.
            McpError = None  # type: ignore[assignment]

        if McpError is not None and isinstance(exc, McpError):
            error = getattr(exc, "error", None)
            if error is not None:
                envelope_code = getattr(error, "code", envelope_code)
                envelope_message = str(getattr(error, "message", "") or message).strip() or message
                details = getattr(error, "data", None)
                try:
                    body = error.model_dump_json(by_alias=True, exclude_none=True)
                except Exception:  # noqa: BLE001 - fallback to string below.
                    body = str(error)
            normalized_code = str(envelope_code or "").strip().lower()
            normalized_message = f"{envelope_message} {body}".strip().lower()
            if any(token in normalized_code or token in normalized_message for token in ("invalid", "badrequest", "bad_request")):
                category = OrchestratorErrorCategory.BAD_REQUEST
                http_status = 400
            elif any(token in normalized_code or token in normalized_message for token in ("permission", "forbidden", "unauthenticated")):
                category = OrchestratorErrorCategory.PERMISSION
                http_status = 403
            elif any(token in normalized_code or token in normalized_message for token in ("notfound", "not_found", "missing")):
                category = OrchestratorErrorCategory.NOT_FOUND
                http_status = 404
            elif any(token in normalized_code or token in normalized_message for token in ("rate", "limit")):
                category = OrchestratorErrorCategory.RATE_LIMITED
                http_status = 429
            else:
                category = OrchestratorErrorCategory.RETRYABLE_5XX
                http_status = 500
        elif isinstance(exc, requests.RequestException) or isinstance(exc, (TimeoutError, OSError)):
            category = OrchestratorErrorCategory.RETRYABLE_TRANSPORT
            http_status = 0
            envelope_code = "NETWORK_ERROR"
        else:
            category = OrchestratorErrorCategory.UNKNOWN
            http_status = 500

        return RCAApiError(
            category=category,
            message=message,
            method="POST",
            path="/sse",
            http_status=http_status,
            envelope_code=envelope_code,
            envelope_message=envelope_message,
            details=details,
            body_text=body,
        )

    @staticmethod
    def _unwrap_exception(exc: Exception) -> Exception:
        current: Exception = exc
        while isinstance(current, BaseExceptionGroup) and getattr(current, "exceptions", None):
            nested = list(getattr(current, "exceptions", []))
            if not nested:
                break
            next_exc = nested[0]
            if not isinstance(next_exc, Exception):
                break
            current = next_exc
        return current

    @staticmethod
    def _decode_json_dict(raw: str) -> dict[str, Any] | None:
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
    def _extract_error_parts(payload: Any) -> tuple[Any, str, Any]:
        if not isinstance(payload, dict):
            return None, "", None
        error_obj = payload.get("error")
        if isinstance(error_obj, dict):
            code = error_obj.get("code")
            message = str(error_obj.get("message") or "").strip()
            details = error_obj.get("details")
            return code, message, details

        code = payload.get("code")
        if code is None:
            code = payload.get("reason")
        message = str(payload.get("message") or "").strip()
        details = payload.get("details")
        if details is None:
            details = payload.get("metadata")
        return code, message, details

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
        import random
        import time

        base = 0.25 * (2 ** max(attempt - 1, 0))
        jitter = random.uniform(0.0, 0.2)  # noqa: S311 - non-crypto jitter for retry backoff.
        time.sleep(min(base + jitter, 2.5))


MCP_CODE_SCOPE_DENIED = "SCOPE_DENIED"
MCP_CODE_INVALID_ARGUMENT = "INVALID_ARGUMENT"
MCP_CODE_NOT_FOUND = "NOT_FOUND"
MCP_CODE_RATE_LIMITED = "RATE_LIMITED"

_MCP_NON_RETRYABLE_CODES = {
    MCP_CODE_SCOPE_DENIED,
    MCP_CODE_INVALID_ARGUMENT,
    MCP_CODE_NOT_FOUND,
}


def iter_tool_names() -> list[str]:
    from ...runtime.tool_registry import iter_tool_names as _iter_tool_names

    return list(_iter_tool_names())
