from __future__ import annotations

from hashlib import sha256
import json
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
from orchestrator.skills.runtime import SkillRuntime
from orchestrator.state import GraphState


def _write_skill_dir(
    root_dir: Path,
    *,
    skill_id: str,
    version: str,
    module_name: str,
    module_source: str,
    allowed_tools: list[str],
) -> dict[str, object]:
    manifest = {
        "skill_id": skill_id,
        "version": version,
        "runtime": "python",
        "entrypoint": {"module": module_name, "callable": "run"},
        "instruction_file": "SKILL.md",
        "resource_files": ["templates/guide.txt"],
        "allowed_tools": allowed_tools,
    }
    (root_dir / "SKILL.md").write_text("# test skill\n", encoding="utf-8")
    resource_path = root_dir / "templates" / "guide.txt"
    resource_path.parent.mkdir(parents=True, exist_ok=True)
    resource_path.write_text("guide\n", encoding="utf-8")
    module_path = root_dir / f"{module_name}.py"
    module_path.write_text(module_source, encoding="utf-8")
    (root_dir / "manifest.json").write_text(json.dumps(manifest, ensure_ascii=False), encoding="utf-8")
    return manifest


def _zip_dir(root_dir: Path, zip_path: Path) -> str:
    with zipfile.ZipFile(zip_path, "w", zipfile.ZIP_DEFLATED) as archive:
        for path in root_dir.rglob("*"):
            if path.is_file():
                archive.write(path, path.relative_to(root_dir).as_posix())
    return sha256(zip_path.read_bytes()).hexdigest()


def _resolved_skillsets_payload(
    *,
    skillset_id: str,
    manifest: dict[str, object],
    bundle_path: Path,
    bundle_digest: str,
) -> list[dict[str, object]]:
    return [
        {
            "skillsetID": skillset_id,
            "skills": [
                {
                    "skillID": manifest["skill_id"],
                    "version": manifest["version"],
                    "artifactURL": bundle_path.resolve().as_uri(),
                    "bundleDigest": bundle_digest,
                    "manifestJSON": json.dumps(manifest, ensure_ascii=False),
                }
            ],
        }
    ]


class SkillRuntimeTest(unittest.TestCase):
    def test_resolved_skill_bundle_downloads_and_executes(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            base = Path(tmp_dir)
            skill_dir = base / "remote"
            skill_dir.mkdir(parents=True)
            module_name = "_skill_remote_exec"
            self.addCleanup(lambda: sys.modules.pop(module_name, None))
            manifest = _write_skill_dir(
                skill_dir,
                skill_id="claude.analysis",
                version="1.0.0",
                module_name=module_name,
                module_source="""
def run(input_payload, ctx):
    tool_result = ctx["tool_executor"]("query_logs", {"query": input_payload.get("query")})
    return {
        "tool_result": tool_result,
        "session_patch": {
            "latest_summary": {"source": "skill"},
            "pinned_evidence_append": [{"evidence_id": "ev-1"}],
            "context_state_patch": {"skills": {"last": ctx["skill_id"]}},
        },
    }
""".strip(),
                allowed_tools=["query_logs"],
            )
            bundle_path = base / "claude.analysis.zip"
            bundle_digest = _zip_dir(skill_dir, bundle_path)
            runtime = SkillRuntime.from_resolved_skillsets(
                skillsets_payload=_resolved_skillsets_payload(
                    skillset_id="skillset.default",
                    manifest=manifest,
                    bundle_path=bundle_path,
                    bundle_digest=bundle_digest,
                ),
                cache_dir=str(base / "cache"),
            )

            tool_calls: list[tuple[str, dict[str, object]]] = []

            def _tool_executor(tool: str, payload: dict[str, object] | None = None) -> dict[str, object]:
                normalized = payload or {}
                tool_calls.append((tool, normalized))
                return {"output": {"tool": tool, "payload": normalized}}

            result = runtime.execute(
                skill_id="claude.analysis",
                input_payload={"query": "error"},
                graph_state={},
                session_snapshot={"session_id": "sess-1"},
                tool_executor=_tool_executor,
            )

            self.assertEqual(runtime.skillset_ids(), ["skillset.default"])
            self.assertEqual(runtime.skill_ids(), ["claude.analysis"])
            self.assertEqual(runtime.describe()[0]["source"], "registry")
            self.assertEqual(tool_calls, [("query_logs", {"query": "error"})])
            self.assertEqual(result["tool_result"]["output"]["tool"], "query_logs")
            self.assertEqual(result["session_patch"]["latest_summary"], {"source": "skill"})

    def test_resolved_skill_bundle_manifest_mismatch_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            base = Path(tmp_dir)
            skill_dir = base / "remote"
            skill_dir.mkdir(parents=True)
            module_name = "_skill_manifest_mismatch"
            self.addCleanup(lambda: sys.modules.pop(module_name, None))
            manifest = _write_skill_dir(
                skill_dir,
                skill_id="claude.analysis",
                version="1.0.1",
                module_name=module_name,
                module_source="def run(input_payload, ctx):\n    return {'ok': True}\n",
                allowed_tools=["query_logs"],
            )
            bundle_path = base / "claude.analysis.zip"
            bundle_digest = _zip_dir(skill_dir, bundle_path)
            resolved_manifest = dict(manifest)
            resolved_manifest["version"] = "1.0.0"

            with self.assertRaisesRegex(RuntimeError, "skill bundle manifest mismatch"):
                SkillRuntime.from_resolved_skillsets(
                    skillsets_payload=_resolved_skillsets_payload(
                        skillset_id="skillset.default",
                        manifest=resolved_manifest,
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
            remote_module = "_skill_override_remote"
            local_module = "_skill_override_local"
            self.addCleanup(lambda: sys.modules.pop(remote_module, None))
            self.addCleanup(lambda: sys.modules.pop(local_module, None))
            remote_manifest = _write_skill_dir(
                remote_dir,
                skill_id="claude.analysis",
                version="1.0.0",
                module_name=remote_module,
                module_source="def run(input_payload, ctx):\n    return {'source': 'registry'}\n",
                allowed_tools=[],
            )
            bundle_path = base / "claude.analysis.zip"
            bundle_digest = _zip_dir(remote_dir, bundle_path)

            local_root = base / "local-overrides"
            local_skill_dir = local_root / "claude.analysis"
            local_skill_dir.mkdir(parents=True)
            _write_skill_dir(
                local_skill_dir,
                skill_id="claude.analysis",
                version="2.0.0",
                module_name=local_module,
                module_source="def run(input_payload, ctx):\n    return {'source': 'local_override'}\n",
                allowed_tools=[],
            )

            runtime = SkillRuntime.from_resolved_skillsets(
                skillsets_payload=_resolved_skillsets_payload(
                    skillset_id="skillset.default",
                    manifest=remote_manifest,
                    bundle_path=bundle_path,
                    bundle_digest=bundle_digest,
                ),
                cache_dir=str(base / "cache"),
                local_override_paths=[str(local_root)],
            )

            result = runtime.execute(
                skill_id="claude.analysis",
                input_payload={},
                graph_state={},
                session_snapshot={},
                tool_executor=lambda _tool, _payload=None: {"output": {}},
            )

            self.assertEqual(runtime.describe()[0]["source"], "local_override")
            self.assertEqual(runtime.describe()[0]["version"], "2.0.0")
            self.assertEqual(result["source"], "local_override")

    def test_prepare_bundle_rejects_zip_slip_entries(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            base = Path(tmp_dir)
            bundle_path = base / "unsafe.zip"
            module_name = "_skill_unsafe_zip"
            with zipfile.ZipFile(bundle_path, "w", zipfile.ZIP_DEFLATED) as archive:
                archive.writestr(
                    "manifest.json",
                    json.dumps(
                        {
                            "skill_id": "claude.analysis",
                            "version": "1.0.0",
                            "runtime": "python",
                            "entrypoint": {"module": module_name, "callable": "run"},
                            "instruction_file": "SKILL.md",
                            "resource_files": [],
                            "allowed_tools": [],
                        }
                    ),
                )
                archive.writestr("SKILL.md", "# unsafe\n")
                archive.writestr(f"{module_name}.py", "def run(input_payload, ctx):\n    return {'ok': True}\n")
                archive.writestr("../escape.py", "boom = True\n")
            bundle_digest = sha256(bundle_path.read_bytes()).hexdigest()
            manifest = {
                "skill_id": "claude.analysis",
                "version": "1.0.0",
                "runtime": "python",
                "entrypoint": {"module": module_name, "callable": "run"},
                "instruction_file": "SKILL.md",
                "resource_files": [],
                "allowed_tools": [],
            }

            with self.assertRaisesRegex(RuntimeError, "unsafe path"):
                SkillRuntime.from_resolved_skillsets(
                    skillsets_payload=_resolved_skillsets_payload(
                        skillset_id="skillset.default",
                        manifest=manifest,
                        bundle_path=bundle_path,
                        bundle_digest=bundle_digest,
                    ),
                    cache_dir=str(base / "cache"),
                )

    def test_orchestrator_runtime_execute_skill_merges_session_patch(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            base = Path(tmp_dir)
            skill_dir = base / "remote"
            skill_dir.mkdir(parents=True)
            module_name = "_skill_runtime_execute"
            self.addCleanup(lambda: sys.modules.pop(module_name, None))
            manifest = _write_skill_dir(
                skill_dir,
                skill_id="claude.analysis",
                version="1.0.0",
                module_name=module_name,
                module_source="""
def run(input_payload, ctx):
    tool_result = ctx["tool_executor"]("query_logs", {"query": input_payload.get("query")})
    return {
        "tool_result": tool_result,
        "session_patch": {
            "latest_summary": {"title": "updated"},
            "pinned_evidence_append": [{"evidence_id": "ev-2"}],
            "context_state_patch": {"skill_runs": 1},
        },
    }
""".strip(),
                allowed_tools=["query_logs"],
            )
            bundle_path = base / "claude.analysis.zip"
            bundle_digest = _zip_dir(skill_dir, bundle_path)
            skill_runtime = SkillRuntime.from_resolved_skillsets(
                skillsets_payload=_resolved_skillsets_payload(
                    skillset_id="skillset.default",
                    manifest=manifest,
                    bundle_path=bundle_path,
                    bundle_digest=bundle_digest,
                ),
                cache_dir=str(base / "cache"),
            )

            class _FakeMCPClient:
                @staticmethod
                def call(*_args: object, **_kwargs: object) -> dict[str, object]:
                    raise AssertionError("direct mcp client should not be used when tool_invoker is configured")

            class _FakeClient:
                def __init__(self) -> None:
                    self.session = type("_Session", (), {"headers": {}})()
                    self.instance_id = ""
                    self.mcp_client = _FakeMCPClient()

            class _FakeToolInvoker:
                def __init__(self) -> None:
                    self.toolset_ids = ["toolset.default"]
                    self.calls: list[tuple[str, dict[str, object]]] = []

                def call(
                    self,
                    *,
                    tool: str,
                    input_payload: dict[str, object] | None,
                    idempotency_key: str | None = None,
                ) -> dict[str, object]:
                    del idempotency_key
                    normalized = input_payload or {}
                    self.calls.append((tool, normalized))
                    return {
                        "output": {
                            "tool": tool,
                            "payload": normalized,
                        }
                    }

            tool_invoker = _FakeToolInvoker()
            runtime = OrchestratorRuntime(
                client=_FakeClient(),
                job_id="job-1",
                instance_id="",
                heartbeat_interval_seconds=10,
                tool_invoker=tool_invoker,
                skill_runtime=skill_runtime,
            )
            state = GraphState(job_id="job-1", session_snapshot={"session_id": "sess-1", "session_revision": "rev-1"})

            result = runtime.execute_skill("claude.analysis", input_payload={"query": "error"}, graph_state=state)

            self.assertEqual(tool_invoker.calls, [("query_logs", {"query": "error"})])
            self.assertEqual(result["tool_result"]["output"]["tool"], "query_logs")
            self.assertEqual(
                state.session_patch,
                {
                    "latest_summary": {"title": "updated"},
                    "pinned_evidence_append": [{"evidence_id": "ev-2"}],
                    "context_state_patch": {"skill_runs": 1},
                },
            )


if __name__ == "__main__":
    unittest.main()
