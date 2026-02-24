from __future__ import annotations

from contextlib import contextmanager
from dataclasses import dataclass, field
import importlib.util
from pathlib import Path
import sys
from typing import Any, Iterator


_ENTRYPOINT_RELATIVE_PATH = Path("scripts/executor.py")
_ENTRYPOINT_CALLABLE = "run"


def _trim(value: Any) -> str:
    return str(value or "").strip()


class ScriptExecutorError(RuntimeError):
    pass


@dataclass(frozen=True)
class ScriptExecutorResult:
    payload: dict[str, Any] = field(default_factory=dict)
    session_patch: dict[str, Any] = field(default_factory=dict)
    observations: list[dict[str, Any]] = field(default_factory=list)
    tool_calls: list[dict[str, Any]] = field(default_factory=list)


@contextmanager
def _temporary_sys_path(paths: list[Path]) -> Iterator[None]:
    inserted: list[str] = []
    try:
        for path in reversed(paths):
            normalized = str(path.resolve())
            if normalized in sys.path:
                continue
            sys.path.insert(0, normalized)
            inserted.append(normalized)
        yield
    finally:
        for normalized in inserted:
            try:
                sys.path.remove(normalized)
            except ValueError:
                continue


class ScriptExecutorRunner:
    def __init__(self) -> None:
        self._entrypoint_relative_path = _ENTRYPOINT_RELATIVE_PATH
        self._callable_name = _ENTRYPOINT_CALLABLE

    def run(
        self,
        *,
        bundle_root: Path,
        input_payload: dict[str, Any],
        ctx: dict[str, Any],
        module_suffix: str,
    ) -> ScriptExecutorResult:
        script_path = bundle_root / self._entrypoint_relative_path
        if not script_path.exists() or not script_path.is_file():
            raise ScriptExecutorError(f"script executor missing entrypoint: {self._entrypoint_relative_path.as_posix()}")

        module_name = f"rca_skill_script_executor_{self._sanitize_module_suffix(module_suffix)}"
        with _temporary_sys_path([bundle_root, script_path.parent]):
            spec = importlib.util.spec_from_file_location(module_name, script_path)
            if spec is None or spec.loader is None:
                raise ScriptExecutorError("failed to load script executor module spec")
            module = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(module)

        run_fn = getattr(module, self._callable_name, None)
        if not callable(run_fn):
            raise ScriptExecutorError(
                f"script executor missing callable: {self._entrypoint_relative_path.as_posix()}:{self._callable_name}"
            )

        result = run_fn(dict(input_payload), dict(ctx))
        if not isinstance(result, dict):
            raise ScriptExecutorError("script executor must return a dict")

        payload = result.get("payload")
        if payload is None:
            payload = {}
        if not isinstance(payload, dict):
            raise ScriptExecutorError("script executor payload must be an object")

        session_patch = result.get("session_patch")
        if session_patch is None:
            session_patch = {}
        if not isinstance(session_patch, dict):
            raise ScriptExecutorError("script executor session_patch must be an object")

        observations = result.get("observations")
        if observations is None:
            observations = []
        if not isinstance(observations, list):
            raise ScriptExecutorError("script executor observations must be an array")

        tool_calls = result.get("tool_calls")
        if tool_calls is None:
            tool_calls = []
        if not isinstance(tool_calls, list):
            raise ScriptExecutorError("script executor tool_calls must be an array")

        normalized_tool_calls: list[dict[str, Any]] = []
        for item in tool_calls:
            if isinstance(item, dict):
                normalized_tool_calls.append(dict(item))

        return ScriptExecutorResult(
            payload=payload,
            session_patch=session_patch,
            observations=[item for item in observations if isinstance(item, dict)],
            tool_calls=normalized_tool_calls,
        )

    def _sanitize_module_suffix(self, value: str) -> str:
        cleaned = "".join(ch if ch.isalnum() else "_" for ch in _trim(value))
        return cleaned or "default"
