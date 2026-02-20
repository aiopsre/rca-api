from __future__ import annotations

from dataclasses import dataclass, replace
import json
from pathlib import Path
from typing import Any

from .cache import prepare_bundle


_DEFAULT_INSTRUCTION_FILE = "SKILL.md"
_DEFAULT_BUNDLE_FORMAT = "claude_skill_v1"


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


def parse_skill_frontmatter(raw: str) -> dict[str, str]:
    content = str(raw or "").replace("\r\n", "\n")
    if not content.startswith("---\n"):
        raise ValueError("SKILL.md missing frontmatter")
    rest = content[len("---\n") :]
    end = rest.find("\n---\n")
    if end < 0:
        raise ValueError("SKILL.md missing closing frontmatter delimiter")
    frontmatter = rest[:end]
    fields: dict[str, str] = {}
    for line in frontmatter.split("\n"):
        trimmed = line.strip()
        if not trimmed:
            continue
        if line.startswith(" ") or line.startswith("\t"):
            raise ValueError("SKILL.md frontmatter only supports flat scalar fields")
        if ":" not in line:
            raise ValueError(f"invalid SKILL.md frontmatter line: {trimmed}")
        key, value = line.split(":", 1)
        normalized_key = key.strip()
        normalized_value = value.strip()
        if not normalized_key:
            raise ValueError("invalid SKILL.md frontmatter key")
        if len(normalized_value) >= 2 and (
            (normalized_value.startswith('"') and normalized_value.endswith('"'))
            or (normalized_value.startswith("'") and normalized_value.endswith("'"))
        ):
            normalized_value = normalized_value[1:-1]
        if "\n" in normalized_value:
            raise ValueError("SKILL.md frontmatter multiline values are unsupported")
        fields[normalized_key] = normalized_value
    name = _trim(fields.get("name"))
    description = _trim(fields.get("description"))
    if not name or not description:
        raise ValueError("SKILL.md frontmatter requires name and description")
    return {
        "name": name,
        "description": description,
        "compatibility": _trim(fields.get("compatibility")),
    }


def read_skill_frontmatter(skill_path: Path) -> dict[str, str]:
    return parse_skill_frontmatter(skill_path.read_text(encoding="utf-8"))


@dataclass(frozen=True)
class SkillSummary:
    skill_id: str
    version: str
    name: str
    description: str
    compatibility: str = ""
    bundle_format: str = _DEFAULT_BUNDLE_FORMAT
    instruction_file: str = _DEFAULT_INSTRUCTION_FILE

    @classmethod
    def from_envelope(cls, raw: str, *, skill_id: str, version: str) -> "SkillSummary":
        try:
            payload = json.loads(str(raw or ""))
        except json.JSONDecodeError as exc:
            raise ValueError("manifestJSON must be valid JSON") from exc
        if not isinstance(payload, dict):
            raise ValueError("manifestJSON must be a JSON object")
        bundle_format = _trim(payload.get("bundle_format"))
        instruction_file = _trim(payload.get("instruction_file"))
        name = _trim(payload.get("name"))
        description = _trim(payload.get("description"))
        compatibility = _trim(payload.get("compatibility"))
        if bundle_format != _DEFAULT_BUNDLE_FORMAT:
            raise ValueError(f"unsupported bundle_format: {bundle_format or '<empty>'}")
        if instruction_file != _DEFAULT_INSTRUCTION_FILE:
            raise ValueError(f"unsupported instruction_file: {instruction_file or '<empty>'}")
        if not name or not description:
            raise ValueError("manifestJSON summary envelope requires name and description")
        normalized_skill_id = _trim(skill_id)
        normalized_version = _trim(version)
        if not normalized_skill_id or not normalized_version:
            raise ValueError("resolved skill requires skill_id and version")
        return cls(
            skill_id=normalized_skill_id,
            version=normalized_version,
            name=name,
            description=description,
            compatibility=compatibility,
            bundle_format=bundle_format,
            instruction_file=instruction_file,
        )


@dataclass(frozen=True)
class SkillBinding:
    capability: str
    allowed_tools: tuple[str, ...]
    priority: int = 100
    enabled: bool = True

    @classmethod
    def from_payload(cls, payload: dict[str, Any]) -> "SkillBinding":
        capability = _trim(payload.get("capability"))
        if not capability:
            raise ValueError("resolved skill binding requires capability")
        priority_raw = payload.get("priority")
        try:
            priority = int(priority_raw)
        except (TypeError, ValueError):
            priority = 100
        if priority <= 0:
            priority = 100
        enabled = payload.get("enabled")
        if enabled is None:
            enabled = True
        return cls(
            capability=capability,
            allowed_tools=tuple(_string_list(payload.get("allowed_tools") or payload.get("allowedTools"))),
            priority=priority,
            enabled=bool(enabled),
        )


@dataclass(frozen=True)
class CatalogSkill:
    summary: SkillSummary
    binding: SkillBinding
    root_dir: Path
    source: str
    artifact_url: str
    bundle_digest: str

    @property
    def binding_key(self) -> str:
        return _binding_key(self.summary.skill_id, self.summary.version, self.binding.capability)


@dataclass(frozen=True)
class SkillCandidate:
    binding_key: str
    skill_id: str
    version: str
    name: str
    description: str
    compatibility: str
    capability: str
    priority: int
    source: str

    def to_summary_dict(self) -> dict[str, Any]:
        return {
            "binding_key": self.binding_key,
            "skill_id": self.skill_id,
            "version": self.version,
            "name": self.name,
            "description": self.description,
            "compatibility": self.compatibility,
            "capability": self.capability,
            "priority": self.priority,
            "source": self.source,
        }


class SkillCatalog:
    def __init__(
        self,
        *,
        cache_dir: str,
        local_override_paths: list[str] | None = None,
        bundle_timeout_s: float = 15.0,
    ) -> None:
        self._cache_dir = str(cache_dir or "").strip() or "/tmp/rca-ai-orchestrator/skills-cache"
        self._bundle_timeout_s = max(float(bundle_timeout_s), 1.0)
        self._local_override_paths = [str(item).strip() for item in (local_override_paths or []) if str(item).strip()]
        self._skills: dict[str, CatalogSkill] = {}
        self._skill_ids: dict[str, list[str]] = {}
        self._skillset_ids: list[str] = []

    @classmethod
    def from_resolved_skillsets(
        cls,
        *,
        skillsets_payload: list[dict[str, Any]] | None,
        cache_dir: str,
        local_override_paths: list[str] | None = None,
        bundle_timeout_s: float = 15.0,
    ) -> "SkillCatalog":
        catalog = cls(
            cache_dir=cache_dir,
            local_override_paths=local_override_paths,
            bundle_timeout_s=bundle_timeout_s,
        )
        catalog._load_remote_skillsets(skillsets_payload or [])
        catalog._apply_local_overrides()
        return catalog

    def skill_ids(self) -> list[str]:
        return sorted(self._skill_ids.keys())

    def skillset_ids(self) -> list[str]:
        return list(self._skillset_ids)

    def has_skills(self) -> bool:
        return bool(self._skills)

    def describe(self) -> list[dict[str, Any]]:
        out: list[dict[str, Any]] = []
        for binding_key in sorted(self._skills.keys()):
            item = self._skills[binding_key]
            out.append(
                {
                    "skill_id": item.summary.skill_id,
                    "version": item.summary.version,
                    "name": item.summary.name,
                    "description": item.summary.description,
                    "compatibility": item.summary.compatibility,
                    "capability": item.binding.capability,
                    "allowed_tools": list(item.binding.allowed_tools),
                    "priority": item.binding.priority,
                    "enabled": item.binding.enabled,
                    "source": item.source,
                }
            )
        return out

    def candidates_for_capability(self, capability: str) -> list[SkillCandidate]:
        normalized_capability = _trim(capability)
        if not normalized_capability:
            return []
        out: list[SkillCandidate] = []
        for binding_key, item in self._skills.items():
            if item.binding.capability != normalized_capability or not item.binding.enabled:
                continue
            out.append(
                SkillCandidate(
                    binding_key=binding_key,
                    skill_id=item.summary.skill_id,
                    version=item.summary.version,
                    name=item.summary.name,
                    description=item.summary.description,
                    compatibility=item.summary.compatibility,
                    capability=item.binding.capability,
                    priority=item.binding.priority,
                    source=item.source,
                )
            )
        out.sort(key=lambda item: (-item.priority, item.skill_id, item.version, item.binding_key))
        return out

    def load_skill_document(self, binding_key: str) -> str:
        item = self._skills.get(_trim(binding_key))
        if item is None:
            raise RuntimeError(f"unknown skill binding: {binding_key}")
        skill_path = item.root_dir / item.summary.instruction_file
        if not skill_path.exists():
            raise RuntimeError(f"skill bundle missing {item.summary.instruction_file}")
        return skill_path.read_text(encoding="utf-8")

    def get_skill(self, binding_key: str) -> CatalogSkill:
        item = self._skills.get(_trim(binding_key))
        if item is None:
            raise RuntimeError(f"unknown skill binding: {binding_key}")
        return item

    def _load_remote_skillsets(self, skillsets_payload: list[dict[str, Any]]) -> None:
        for skillset_item in skillsets_payload:
            if not isinstance(skillset_item, dict):
                continue
            skillset_id = _trim(
                skillset_item.get("skillsetID") or skillset_item.get("skillsetId") or skillset_item.get("skillset_id")
            )
            if skillset_id:
                self._skillset_ids.append(skillset_id)
            raw_skills = skillset_item.get("skills")
            if not isinstance(raw_skills, list):
                continue
            for skill_payload in raw_skills:
                if not isinstance(skill_payload, dict):
                    continue
                skill_id = _trim(skill_payload.get("skillID") or skill_payload.get("skillId") or skill_payload.get("skill_id"))
                version = _trim(skill_payload.get("version"))
                manifest_json = skill_payload.get("manifestJSON") or skill_payload.get("manifest_json")
                if not isinstance(manifest_json, str) or not manifest_json.strip():
                    raise RuntimeError("resolved skill is missing manifestJSON")
                summary = SkillSummary.from_envelope(manifest_json, skill_id=skill_id, version=version)
                binding = SkillBinding.from_payload(skill_payload)
                if not binding.enabled:
                    continue
                binding_key = _binding_key(summary.skill_id, summary.version, binding.capability)
                if binding_key in self._skills:
                    raise RuntimeError(f"duplicate resolved skill binding: {binding_key}")
                artifact_url = _trim(skill_payload.get("artifactURL") or skill_payload.get("artifact_url"))
                bundle_digest = _trim(skill_payload.get("bundleDigest") or skill_payload.get("bundle_digest"))
                root_dir = prepare_bundle(
                    self._cache_dir,
                    artifact_url=artifact_url,
                    bundle_digest=bundle_digest,
                    timeout_s=self._bundle_timeout_s,
                )
                self._validate_bundle_summary(root_dir, expected_summary=summary)
                item = CatalogSkill(
                    summary=summary,
                    binding=binding,
                    root_dir=root_dir,
                    source="registry",
                    artifact_url=artifact_url,
                    bundle_digest=bundle_digest,
                )
                self._skills[binding_key] = item
                self._skill_ids.setdefault(summary.skill_id, []).append(binding_key)

    def _apply_local_overrides(self) -> None:
        if not self._skills:
            return
        for raw_path in self._local_override_paths:
            base = Path(raw_path).expanduser()
            if not base.exists():
                continue
            candidates: list[Path]
            if (base / _DEFAULT_INSTRUCTION_FILE).exists():
                candidates = [base]
            else:
                candidates = [item for item in base.iterdir() if item.is_dir() and (item / _DEFAULT_INSTRUCTION_FILE).exists()]
            for skill_dir in candidates:
                skill_id = _trim(skill_dir.name)
                if not skill_id:
                    continue
                binding_keys = self._skill_ids.get(skill_id) or []
                for binding_key in binding_keys:
                    existing = self._skills[binding_key]
                    self._validate_bundle_summary(skill_dir, expected_summary=existing.summary)
                    self._skills[binding_key] = replace(existing, root_dir=skill_dir, source="local_override")

    def _validate_bundle_summary(self, root_dir: Path, *, expected_summary: SkillSummary) -> None:
        skill_path = root_dir / expected_summary.instruction_file
        if not skill_path.exists():
            raise RuntimeError(f"skill bundle missing {expected_summary.instruction_file}")
        frontmatter = read_skill_frontmatter(skill_path)
        if frontmatter["name"] != expected_summary.name or frontmatter["description"] != expected_summary.description:
            raise RuntimeError(
                "skill bundle summary mismatch: "
                f"expected={expected_summary.skill_id}@{expected_summary.version} "
                f"name={expected_summary.name!r} description={expected_summary.description!r}"
            )


def _binding_key(skill_id: str, version: str, capability: str) -> str:
    return f"{_trim(skill_id)}\x00{_trim(version)}\x00{_trim(capability)}"
