from __future__ import annotations

from dataclasses import dataclass
import importlib
import importlib.util
import json
from pathlib import Path
import sys
from typing import Any, Callable

from .cache import prepare_bundle
from .manifest import SkillManifest


def _trim(value: Any) -> str:
    return str(value or "").strip()


@dataclass(frozen=True)
class PreparedSkill:
    manifest: SkillManifest
    root_dir: Path
    source: str
    call: Callable[[dict[str, Any], dict[str, Any]], dict[str, Any]]


class SkillRuntime:
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
        self._skills: dict[str, PreparedSkill] = {}
        self._skillset_ids: list[str] = []

    @classmethod
    def from_resolved_skillsets(
        cls,
        *,
        skillsets_payload: list[dict[str, Any]] | None,
        cache_dir: str,
        local_override_paths: list[str] | None = None,
        bundle_timeout_s: float = 15.0,
    ) -> "SkillRuntime":
        runtime = cls(
            cache_dir=cache_dir,
            local_override_paths=local_override_paths,
            bundle_timeout_s=bundle_timeout_s,
        )
        runtime._load_remote_skillsets(skillsets_payload or [])
        runtime._apply_local_overrides()
        return runtime

    def skill_ids(self) -> list[str]:
        return sorted(self._skills.keys())

    def skillset_ids(self) -> list[str]:
        return list(self._skillset_ids)

    def has_skills(self) -> bool:
        return bool(self._skills)

    def get(self, skill_id: str) -> PreparedSkill:
        normalized = _trim(skill_id)
        if not normalized or normalized not in self._skills:
            raise RuntimeError(f"skill is not available: {normalized or '<empty>'}")
        return self._skills[normalized]

    def allowed_tools(self, skill_id: str) -> list[str]:
        prepared = self.get(skill_id)
        return list(prepared.manifest.allowed_tools)

    def execute(
        self,
        *,
        skill_id: str,
        input_payload: dict[str, Any] | None,
        graph_state: Any,
        session_snapshot: dict[str, Any] | None,
        tool_executor: Callable[[str, dict[str, Any] | None], dict[str, Any]],
    ) -> dict[str, Any]:
        prepared = self.get(skill_id)
        allowed_tools = set(prepared.manifest.allowed_tools)

        def _restricted_tool_executor(tool: str, payload: dict[str, Any] | None = None) -> dict[str, Any]:
            normalized_tool = _trim(tool)
            if normalized_tool not in allowed_tools:
                raise RuntimeError(f"skill tool denied: skill={prepared.manifest.skill_id} tool={normalized_tool}")
            return tool_executor(normalized_tool, payload or {})

        ctx = {
            "skill_id": prepared.manifest.skill_id,
            "version": prepared.manifest.version,
            "graph_state": graph_state,
            "session_snapshot": dict(session_snapshot or {}),
            "tool_executor": _restricted_tool_executor,
            "resource_root": str(prepared.root_dir),
            "instruction_file": str(prepared.root_dir / prepared.manifest.instruction_file),
            "resource_files": [str(prepared.root_dir / item) for item in prepared.manifest.resource_files],
            "allowed_tools": sorted(allowed_tools),
        }
        result = prepared.call(input_payload or {}, ctx)
        if not isinstance(result, dict):
            raise RuntimeError(f"skill must return dict: skill={prepared.manifest.skill_id}")
        return dict(result)

    def describe(self) -> list[dict[str, Any]]:
        out: list[dict[str, Any]] = []
        for skill_id in self.skill_ids():
            prepared = self._skills[skill_id]
            out.append(
                {
                    "skill_id": prepared.manifest.skill_id,
                    "version": prepared.manifest.version,
                    "source": prepared.source,
                    "allowed_tools": list(prepared.manifest.allowed_tools),
                }
            )
        return out

    def _load_remote_skillsets(self, skillsets_payload: list[dict[str, Any]]) -> None:
        seen_skill_ids: set[str] = set()
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
                manifest_json = skill_payload.get("manifestJSON") or skill_payload.get("manifest_json")
                if not isinstance(manifest_json, str) or not manifest_json.strip():
                    raise RuntimeError("resolved skill is missing manifestJSON")
                expected_manifest = SkillManifest.from_dict(json.loads(manifest_json))
                if expected_manifest.skill_id in seen_skill_ids:
                    raise RuntimeError(f"duplicate skill_id across resolved skillsets: {expected_manifest.skill_id}")
                seen_skill_ids.add(expected_manifest.skill_id)
                artifact_url = _trim(skill_payload.get("artifactURL") or skill_payload.get("artifact_url"))
                bundle_digest = _trim(skill_payload.get("bundleDigest") or skill_payload.get("bundle_digest"))
                root_dir = prepare_bundle(
                    self._cache_dir,
                    artifact_url=artifact_url,
                    bundle_digest=bundle_digest,
                    timeout_s=self._bundle_timeout_s,
                )
                manifest = self._load_bundle_manifest(root_dir, expected_manifest=expected_manifest)
                self._skills[manifest.skill_id] = PreparedSkill(
                    manifest=manifest,
                    root_dir=root_dir,
                    source="registry",
                    call=self._load_entrypoint(root_dir, manifest),
                )

    def _apply_local_overrides(self) -> None:
        for raw_path in self._local_override_paths:
            base = Path(raw_path).expanduser()
            if not base.exists():
                continue
            candidates: list[Path]
            if (base / "manifest.json").exists():
                candidates = [base]
            else:
                candidates = [item for item in base.iterdir() if item.is_dir() and (item / "manifest.json").exists()]
            for skill_dir in candidates:
                manifest = SkillManifest.from_dict(json.loads((skill_dir / "manifest.json").read_text(encoding="utf-8")))
                self._skills[manifest.skill_id] = PreparedSkill(
                    manifest=manifest,
                    root_dir=skill_dir,
                    source="local_override",
                    call=self._load_entrypoint(skill_dir, manifest),
                )

    def _load_bundle_manifest(self, root_dir: Path, *, expected_manifest: SkillManifest | None = None) -> SkillManifest:
        manifest_path = root_dir / "manifest.json"
        manifest = SkillManifest.from_dict(json.loads(manifest_path.read_text(encoding="utf-8")))
        if expected_manifest is not None and manifest != expected_manifest:
            raise RuntimeError(
                "skill bundle manifest mismatch: "
                f"expected={expected_manifest.skill_id}@{expected_manifest.version} "
                f"actual={manifest.skill_id}@{manifest.version}"
            )
        return manifest

    def _load_entrypoint(self, root_dir: Path, manifest: SkillManifest) -> Callable[[dict[str, Any], dict[str, Any]], dict[str, Any]]:
        sys.path.insert(0, str(root_dir))
        try:
            importlib.invalidate_caches()
            module = importlib.import_module(manifest.entrypoint.module)
        except ModuleNotFoundError:
            entrypoint_file = root_dir / (manifest.entrypoint.module.replace(".", "/") + ".py")
            if not entrypoint_file.exists():
                raise
            spec = importlib.util.spec_from_file_location(
                f"skill_{manifest.skill_id}_{manifest.version}",
                entrypoint_file,
            )
            if spec is None or spec.loader is None:
                raise RuntimeError(f"failed to load skill entrypoint file: {entrypoint_file}")
            module = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(module)
        finally:
            try:
                sys.path.remove(str(root_dir))
            except ValueError:
                pass
        call = getattr(module, manifest.entrypoint.callable_name, None)
        if not callable(call):
            raise RuntimeError(
                f"skill entrypoint is not callable: skill={manifest.skill_id} "
                f"module={manifest.entrypoint.module} callable={manifest.entrypoint.callable_name}"
            )
        return call
