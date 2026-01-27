from __future__ import annotations

import json
from typing import Any

import requests

from ...sdk.errors import OrchestratorErrorCategory, RCAApiError


class MCPHttpProvider:
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
        self._timeout_s = max(float(timeout_s), 0.1)
        self._session = requests.Session()
        self._session.headers.update({"Accept": "application/json"})
        normalized_scopes = str(scopes).strip()
        if normalized_scopes:
            self._session.headers.update({"X-Scopes": normalized_scopes})

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

        body: dict[str, Any] = {
            "tool": normalized_tool,
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
