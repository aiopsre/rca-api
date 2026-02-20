from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any


def _trim(value: Any) -> str:
    return str(value or "").strip()


def _string_list(value: Any) -> list[str]:
    if not isinstance(value, list):
        return []
    out: list[str] = []
    seen: set[str] = set()
    for item in value:
        normalized = _trim(item)
        if not normalized or normalized in seen:
            continue
        seen.add(normalized)
        out.append(normalized)
    return out


@dataclass(frozen=True)
class SkillEntrypoint:
    module: str
    callable_name: str


@dataclass(frozen=True)
class SkillManifest:
    skill_id: str
    version: str
    runtime: str
    entrypoint: SkillEntrypoint
    instruction_file: str = "SKILL.md"
    resource_files: tuple[str, ...] = field(default_factory=tuple)
    allowed_tools: tuple[str, ...] = field(default_factory=tuple)
    compat: dict[str, Any] = field(default_factory=dict)

    @classmethod
    def from_dict(cls, payload: dict[str, Any]) -> "SkillManifest":
        if not isinstance(payload, dict):
            raise ValueError("skill manifest must be an object")
        skill_id = _trim(payload.get("skill_id"))
        version = _trim(payload.get("version"))
        runtime = _trim(payload.get("runtime")) or "python"
        if runtime != "python":
            raise ValueError(f"unsupported skill runtime: {runtime}")
        entrypoint_raw = payload.get("entrypoint")
        if not isinstance(entrypoint_raw, dict):
            raise ValueError("skill manifest requires object field: entrypoint")
        module = _trim(entrypoint_raw.get("module"))
        callable_name = _trim(entrypoint_raw.get("callable")) or "run"
        if not skill_id or not version or not module or not callable_name:
            raise ValueError("skill manifest requires skill_id, version, entrypoint.module, entrypoint.callable")
        instruction_file = _trim(payload.get("instruction_file")) or "SKILL.md"
        resource_files = tuple(_string_list(payload.get("resource_files")))
        if "allowed_tools" not in payload or not isinstance(payload.get("allowed_tools"), list):
            raise ValueError("skill manifest requires list field: allowed_tools")
        allowed_tools = tuple(_string_list(payload.get("allowed_tools")))
        compat = payload.get("compat")
        if not isinstance(compat, dict):
            compat = {}
        return cls(
            skill_id=skill_id,
            version=version,
            runtime=runtime,
            entrypoint=SkillEntrypoint(module=module, callable_name=callable_name),
            instruction_file=instruction_file,
            resource_files=resource_files,
            allowed_tools=allowed_tools,
            compat=dict(compat),
        )
