from __future__ import annotations

import importlib
from types import ModuleType
from typing import Any, Callable


class SkillsProvider:
    def __init__(self, *, module: str, function: str = "call") -> None:
        normalized_module = str(module).strip()
        normalized_function = str(function).strip() or "call"
        if not normalized_module:
            raise ValueError("skills module is required")

        loaded_module = importlib.import_module(normalized_module)
        call_func = getattr(loaded_module, normalized_function, None)
        if not callable(call_func):
            raise RuntimeError(
                f"skills provider function is not callable: module={normalized_module} function={normalized_function}"
            )
        self._module: ModuleType = loaded_module
        self._module_name = normalized_module
        self._function_name = normalized_function
        self._call_func: Callable[[str, dict[str, Any], str | None], Any] = call_func

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
        try:
            result = self._call_func(normalized_tool, input_payload or {}, idempotency_key)
        except Exception as exc:  # noqa: BLE001
            raise RuntimeError(
                f"skills tool call failed: module={self._module_name} function={self._function_name} "
                f"tool={normalized_tool}: {exc}"
            ) from exc
        if not isinstance(result, dict):
            raise RuntimeError(
                f"skills tool call must return dict: module={self._module_name} "
                f"function={self._function_name} tool={normalized_tool}"
            )
        return result
