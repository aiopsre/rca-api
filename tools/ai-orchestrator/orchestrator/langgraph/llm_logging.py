from __future__ import annotations

import json
import logging
from typing import Any

_LOGGER = logging.getLogger("orchestrator.llm")
_MAX_TEXT_CHARS = 4096
_MAX_LIST_ITEMS = 8
_MAX_TOOL_ARGS_CHARS = 2000


def _truncate_text(value: Any, limit: int = _MAX_TEXT_CHARS) -> str:
    text = str(value or "").strip()
    if len(text) <= limit:
        return text
    return text[: max(0, limit - 3)] + "..."


def _extract_message_text(content: Any) -> str:
    if isinstance(content, str):
        return content.strip()
    if isinstance(content, list):
        parts: list[str] = []
        for item in content:
            if isinstance(item, str):
                parts.append(item.strip())
                continue
            if not isinstance(item, dict):
                continue
            item_type = str(item.get("type") or "").strip().lower()
            if item_type in {"text", "output_text"}:
                text = str(item.get("text") or "").strip()
                if text:
                    parts.append(text)
        return "\n".join(part for part in parts if part).strip()
    return str(content or "").strip()


def _json_safe(value: Any) -> Any:
    if value is None or isinstance(value, (str, int, float, bool)):
        return value
    if isinstance(value, dict):
        return {str(key): _json_safe(val) for key, val in list(value.items())[:_MAX_LIST_ITEMS]}
    if isinstance(value, (list, tuple, set)):
        return [_json_safe(item) for item in list(value)[:_MAX_LIST_ITEMS]]
    return _truncate_text(value, 256)


def _message_summary(message: Any) -> dict[str, Any]:
    if isinstance(message, dict):
        content = message.get("content")
        role = str(message.get("role") or message.get("type") or "dict").strip() or "dict"
    else:
        content = getattr(message, "content", message)
        role = type(message).__name__
    summary = {
        "role": role,
        "content": _truncate_text(_extract_message_text(content)),
    }
    name = ""
    if isinstance(message, dict):
        name = str(message.get("name") or "").strip()
    else:
        name = str(getattr(message, "name", "") or "").strip()
    if name:
        summary["name"] = _truncate_text(name, 128)
    return summary


def _tool_summary(tool: Any) -> dict[str, Any]:
    if not isinstance(tool, dict):
        return {"type": type(tool).__name__, "value": _truncate_text(tool, 256)}

    function = tool.get("function") if isinstance(tool.get("function"), dict) else {}
    name = str(
        tool.get("name")
        or function.get("name")
        or tool.get("tool_name")
        or tool.get("id")
        or ""
    ).strip()
    summary = {
        "name": name,
        "type": str(tool.get("type") or "unknown").strip() or "unknown",
    }
    description = str(function.get("description") or tool.get("description") or "").strip()
    if description:
        summary["description"] = _truncate_text(description, 256)
    return summary


def _tool_call_summary(tool_call: Any) -> dict[str, Any]:
    if isinstance(tool_call, dict):
        name = str(
            tool_call.get("name")
            or tool_call.get("tool_name")
            or tool_call.get("tool")
            or ""
        ).strip()
        arguments = tool_call.get("arguments")
        if arguments is None:
            arguments = tool_call.get("args")
        if arguments is None:
            arguments = tool_call.get("input")
    else:
        name = str(
            getattr(tool_call, "name", "")
            or getattr(tool_call, "tool_name", "")
            or getattr(tool_call, "tool", "")
            or ""
        ).strip()
        arguments = getattr(tool_call, "arguments", None)
        if arguments is None:
            arguments = getattr(tool_call, "args", None)
        if arguments is None:
            arguments = getattr(tool_call, "input", None)

    if isinstance(arguments, (dict, list, tuple, set)):
        arguments_text = json.dumps(_json_safe(arguments), ensure_ascii=False, separators=(",", ":"))
    else:
        arguments_text = _truncate_text(arguments, _MAX_TOOL_ARGS_CHARS)

    return {
        "name": name,
        "arguments": _truncate_text(arguments_text, _MAX_TOOL_ARGS_CHARS),
    }


def _response_summary(response: Any) -> dict[str, Any]:
    content = getattr(response, "content", response)
    summary: dict[str, Any] = {
        "type": type(response).__name__,
        "content": _truncate_text(_extract_message_text(content)),
    }

    tool_calls = getattr(response, "tool_calls", None) or []
    if tool_calls:
        summary["tool_calls"] = [_tool_call_summary(item) for item in tool_calls[:_MAX_LIST_ITEMS]]
        summary["tool_call_count"] = len(tool_calls)

    additional_kwargs = getattr(response, "additional_kwargs", None)
    if isinstance(additional_kwargs, dict) and additional_kwargs:
        summary["additional_kwargs"] = _json_safe(additional_kwargs)

    return summary


def log_llm_dialogue(
    *,
    event: str,
    node_name: str,
    messages: list[Any],
    tools: list[Any] | None = None,
    response: Any | None = None,
    error: Exception | str | None = None,
    extra: dict[str, Any] | None = None,
) -> None:
    if not _LOGGER.isEnabledFor(logging.DEBUG):
        return

    payload: dict[str, Any] = {
        "event": event,
        "node": node_name,
        "messages": [_message_summary(message) for message in messages[:_MAX_LIST_ITEMS]],
        "message_count": len(messages),
    }
    if tools is not None:
        payload["tools"] = [_tool_summary(tool) for tool in tools[:_MAX_LIST_ITEMS]]
        payload["tool_count"] = len(tools)
    if response is not None:
        payload["response"] = _response_summary(response)
    if error is not None:
        payload["error"] = _truncate_text(error, 1024)
    if extra:
        payload["extra"] = _json_safe(extra)

    _LOGGER.debug("llm_dialogue=%s", json.dumps(payload, ensure_ascii=False, separators=(",", ":")))
