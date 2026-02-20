from __future__ import annotations

from hashlib import sha256
from pathlib import Path
import sys
import tempfile
import unittest
import zipfile


TESTS_DIR = Path(__file__).resolve().parent
PROJECT_DIR = TESTS_DIR.parent
if str(PROJECT_DIR) not in sys.path:
    sys.path.insert(0, str(PROJECT_DIR))

from orchestrator.runtime.runtime import OrchestratorRuntime
from orchestrator.skills.runtime import SkillCatalog, parse_skill_frontmatter
from orchestrator.state import GraphState


def _write_skill_dir(
    root_dir: Path,
    *,
    name: str,
    description: str,
    compatibility: str = "",
) -> None:
    compatibility_line = f"compatibility: {compatibility}\n" if compatibility else ""
    (root_dir / "SKILL.md").write_text(
        f"---\nname: {name}\ndescription: {description}\n{compatibility_line}---\n\n# test skill\n",
        encoding="utf-8",
    )
    resource_path = root_dir / "templates" / "guide.txt"
    resource_path.parent.mkdir(parents=True, exist_ok=True)
    resource_path.write_text("guide\n", encoding="utf-8")


def _zip_dir(root_dir: Path, zip_path: Path) -> str:
    with zipfile.ZipFile(zip_path, "w", zipfile.ZIP_DEFLATED) as archive:
        for path in root_dir.rglob("*"):
            if path.is_file():
                archive.write(path, path.relative_to(root_dir).as_posix())
    return sha256(zip_path.read_bytes()).hexdigest()


def _resolved_skillsets_payload(
    *,
    skillset_id: str,
    skill_id: str,
    version: str,
    name: str,
    description: str,
    compatibility: str,
    bundle_path: Path,
    bundle_digest: str,
    capability: str = "diagnosis.enrich",
    allowed_tools: list[str] | None = None,
    priority: int = 100,
    enabled: bool = True,
) -> list[dict[str, object]]:
    envelope = (
        '{"bundle_format":"claude_skill_v1","instruction_file":"SKILL.md",'
        f'"name":"{name}","description":"{description}","compatibility":"{compatibility}"'
        "}"
    )
    return [
        {
            "skillsetID": skillset_id,
            "skills": [
                {
                    "skillID": skill_id,
                    "version": version,
                    "artifactURL": bundle_path.resolve().as_uri(),
                    "bundleDigest": bundle_digest,
                    "manifestJSON": envelope,
                    "capability": capability,
                    "allowedTools": allowed_tools or [],
                    "priority": priority,
                    "enabled": enabled,
                }
            ],
        }
    ]


class SkillCatalogTest(unittest.TestCase):
    def test_resolved_skill_bundle_downloads_and_catalogs(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            base = Path(tmp_dir)
            skill_dir = base / "remote"
            skill_dir.mkdir(parents=True)
            _write_skill_dir(
                skill_dir,
                name="Claude Analysis",
                description="Analyze incident evidence",
                compatibility="Requires query_logs access",
            )
            bundle_path = base / "claude.analysis.zip"
            bundle_digest = _zip_dir(skill_dir, bundle_path)
            catalog = SkillCatalog.from_resolved_skillsets(
                skillsets_payload=_resolved_skillsets_payload(
                    skillset_id="skillset.default",
                    skill_id="claude.analysis",
                    version="1.0.0",
                    name="Claude Analysis",
                    description="Analyze incident evidence",
                    compatibility="Requires query_logs access",
                    bundle_path=bundle_path,
                    bundle_digest=bundle_digest,
                    allowed_tools=["query_logs"],
                ),
                cache_dir=str(base / "cache"),
            )

            self.assertEqual(catalog.skillset_ids(), ["skillset.default"])
            self.assertEqual(catalog.skill_ids(), ["claude.analysis"])
            described = catalog.describe()
            self.assertEqual(len(described), 1)
            self.assertEqual(described[0]["source"], "registry")
            self.assertEqual(described[0]["capability"], "diagnosis.enrich")
            self.assertEqual(described[0]["allowed_tools"], ["query_logs"])
            self.assertEqual(described[0]["priority"], 100)
            self.assertEqual(described[0]["name"], "Claude Analysis")
            self.assertEqual(described[0]["description"], "Analyze incident evidence")

    def test_resolved_skill_bundle_summary_mismatch_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            base = Path(tmp_dir)
            skill_dir = base / "remote"
            skill_dir.mkdir(parents=True)
            _write_skill_dir(skill_dir, name="Claude Analysis", description="Actual description")
            bundle_path = base / "claude.analysis.zip"
            bundle_digest = _zip_dir(skill_dir, bundle_path)

            with self.assertRaisesRegex(RuntimeError, "skill bundle summary mismatch"):
                SkillCatalog.from_resolved_skillsets(
                    skillsets_payload=_resolved_skillsets_payload(
                        skillset_id="skillset.default",
                        skill_id="claude.analysis",
                        version="1.0.0",
                        name="Claude Analysis",
                        description="Expected description",
                        compatibility="",
                        bundle_path=bundle_path,
                        bundle_digest=bundle_digest,
                    ),
                    cache_dir=str(base / "cache"),
                )

    def test_local_override_wins_over_registry_bundle(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            base = Path(tmp_dir)
            remote_dir = base / "remote"
            remote_dir.mkdir(parents=True)
            _write_skill_dir(
                remote_dir,
                name="Claude Analysis",
                description="Analyze incident evidence",
                compatibility="Requires query_logs access",
            )
            bundle_path = base / "claude.analysis.zip"
            bundle_digest = _zip_dir(remote_dir, bundle_path)

            local_root = base / "local-overrides"
            local_skill_dir = local_root / "claude.analysis"
            local_skill_dir.mkdir(parents=True)
            _write_skill_dir(
                local_skill_dir,
                name="Claude Analysis",
                description="Analyze incident evidence",
                compatibility="Requires query_logs access",
            )

            catalog = SkillCatalog.from_resolved_skillsets(
                skillsets_payload=_resolved_skillsets_payload(
                    skillset_id="skillset.default",
                    skill_id="claude.analysis",
                    version="1.0.0",
                    name="Claude Analysis",
                    description="Analyze incident evidence",
                    compatibility="Requires query_logs access",
                    bundle_path=bundle_path,
                    bundle_digest=bundle_digest,
                ),
                cache_dir=str(base / "cache"),
                local_override_paths=[str(local_root)],
            )

            self.assertEqual(catalog.describe()[0]["source"], "local_override")

    def test_prepare_bundle_rejects_zip_slip_entries(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            base = Path(tmp_dir)
            bundle_path = base / "unsafe.zip"
            with zipfile.ZipFile(bundle_path, "w", zipfile.ZIP_DEFLATED) as archive:
                archive.writestr("SKILL.md", "---\nname: Unsafe\ndescription: Unsafe\n---\n")
                archive.writestr("../escape.py", "boom = True\n")
            bundle_digest = sha256(bundle_path.read_bytes()).hexdigest()

            with self.assertRaisesRegex(RuntimeError, "unsafe path"):
                SkillCatalog.from_resolved_skillsets(
                    skillsets_payload=_resolved_skillsets_payload(
                        skillset_id="skillset.default",
                        skill_id="claude.analysis",
                        version="1.0.0",
                        name="Unsafe",
                        description="Unsafe",
                        compatibility="",
                        bundle_path=bundle_path,
                        bundle_digest=bundle_digest,
                    ),
                    cache_dir=str(base / "cache"),
                )

    def test_parse_skill_frontmatter_rejects_missing_required_fields(self) -> None:
        with self.assertRaisesRegex(ValueError, "name and description"):
            parse_skill_frontmatter("---\nname: Only Name\n---\n")
        with self.assertRaisesRegex(ValueError, "missing frontmatter"):
            parse_skill_frontmatter("# no frontmatter\n")
        with self.assertRaisesRegex(ValueError, "flat scalar fields"):
            parse_skill_frontmatter("---\nname: Demo\n nested: nope\ndescription: Sample\n---\n")

    def test_orchestrator_runtime_execute_skill_is_disabled_for_catalog(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            base = Path(tmp_dir)
            skill_dir = base / "remote"
            skill_dir.mkdir(parents=True)
            _write_skill_dir(skill_dir, name="Claude Analysis", description="Analyze incident evidence")
            bundle_path = base / "claude.analysis.zip"
            bundle_digest = _zip_dir(skill_dir, bundle_path)
            skill_catalog = SkillCatalog.from_resolved_skillsets(
                skillsets_payload=_resolved_skillsets_payload(
                    skillset_id="skillset.default",
                    skill_id="claude.analysis",
                    version="1.0.0",
                    name="Claude Analysis",
                    description="Analyze incident evidence",
                    compatibility="",
                    bundle_path=bundle_path,
                    bundle_digest=bundle_digest,
                ),
                cache_dir=str(base / "cache"),
            )

            class _FakeSession:
                def __init__(self) -> None:
                    self.headers: dict[str, str] = {}

            class _FakeClient:
                def __init__(self) -> None:
                    self.session = _FakeSession()
                    self.instance_id = ""

            runtime = OrchestratorRuntime(
                client=_FakeClient(),
                job_id="job-1",
                instance_id="orc-test",
                heartbeat_interval_seconds=10,
                skill_catalog=skill_catalog,
            )

            state = GraphState(job_id="job-1", incident_id="inc-1")
            with self.assertRaisesRegex(RuntimeError, "skill execution is disabled"):
                runtime.execute_skill("claude.analysis", input_payload={"query": "error"}, graph_state=state)


if __name__ == "__main__":
    unittest.main()
