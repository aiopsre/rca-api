from __future__ import annotations

from hashlib import sha256
from pathlib import Path
import sys
import tempfile
import unittest
import zipfile


TESTS_DIR = Path(__file__).resolve().parent
PROJECT_DIR = TESTS_DIR.parent
REPO_ROOT = PROJECT_DIR.parent.parent
if str(PROJECT_DIR) not in sys.path:
    sys.path.insert(0, str(PROJECT_DIR))

from orchestrator.runtime.runtime import OrchestratorRuntime
from orchestrator.langgraph.nodes import query_logs_node
from orchestrator.skills.agent import SkillSelectionResult
from orchestrator.skills.capabilities import PromptSkillConsumeResult, get_capability_definition
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
            candidates = catalog.candidates_for_capability("diagnosis.enrich")
            self.assertEqual(len(candidates), 1)
            self.assertEqual(candidates[0].skill_id, "claude.analysis")
            self.assertIn("# test skill", catalog.load_skill_document(candidates[0].binding_key))

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

    def test_candidates_for_capability_sort_by_priority(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            base = Path(tmp_dir)
            skill_one = base / "s1"
            skill_two = base / "s2"
            skill_one.mkdir(parents=True)
            skill_two.mkdir(parents=True)
            _write_skill_dir(skill_one, name="Skill One", description="First")
            _write_skill_dir(skill_two, name="Skill Two", description="Second")
            bundle_one = base / "one.zip"
            bundle_two = base / "two.zip"
            digest_one = _zip_dir(skill_one, bundle_one)
            digest_two = _zip_dir(skill_two, bundle_two)
            payload = [
                {
                    "skillsetID": "skillset.default",
                    "skills": [
                        {
                            "skillID": "skill.one",
                            "version": "1.0.0",
                            "artifactURL": bundle_one.resolve().as_uri(),
                            "bundleDigest": digest_one,
                            "manifestJSON": '{"bundle_format":"claude_skill_v1","instruction_file":"SKILL.md","name":"Skill One","description":"First","compatibility":""}',
                            "capability": "diagnosis.enrich",
                            "priority": 80,
                            "enabled": True,
                        },
                        {
                            "skillID": "skill.two",
                            "version": "1.0.0",
                            "artifactURL": bundle_two.resolve().as_uri(),
                            "bundleDigest": digest_two,
                            "manifestJSON": '{"bundle_format":"claude_skill_v1","instruction_file":"SKILL.md","name":"Skill Two","description":"Second","compatibility":""}',
                            "capability": "diagnosis.enrich",
                            "priority": 120,
                            "enabled": True,
                        },
                    ],
                }
            ]
            catalog = SkillCatalog.from_resolved_skillsets(
                skillsets_payload=payload,
                cache_dir=str(base / "cache"),
            )
            candidates = catalog.candidates_for_capability("diagnosis.enrich")
            self.assertEqual([item.skill_id for item in candidates], ["skill.two", "skill.one"])

    def test_checked_in_prompt_only_bundle_has_valid_frontmatter(self) -> None:
        skill_path = REPO_ROOT / "tools" / "ai-orchestrator" / "skill-bundles" / "diagnosis-enrich" / "SKILL.md"
        frontmatter = parse_skill_frontmatter(skill_path.read_text(encoding="utf-8"))
        self.assertEqual(frontmatter["name"], "RCA Diagnosis Enricher")
        self.assertIn("Enrich the native RCA diagnosis", frontmatter["description"])

    def test_checked_in_prompt_only_evidence_bundle_has_valid_frontmatter(self) -> None:
        skill_path = REPO_ROOT / "tools" / "ai-orchestrator" / "skill-bundles" / "evidence-plan" / "SKILL.md"
        frontmatter = parse_skill_frontmatter(skill_path.read_text(encoding="utf-8"))
        self.assertEqual(frontmatter["name"], "RCA Evidence Planner")
        self.assertIn("Refine the native RCA evidence plan", frontmatter["description"])

    def test_checked_in_elasticsearch_evidence_bundle_has_valid_frontmatter(self) -> None:
        skill_path = REPO_ROOT / "tools" / "ai-orchestrator" / "skill-bundles" / "elasticsearch-evidence-plan" / "SKILL.md"
        frontmatter = parse_skill_frontmatter(skill_path.read_text(encoding="utf-8"))
        self.assertEqual(frontmatter["name"], "Elasticsearch Evidence Planner")
        self.assertIn("Elasticsearch-backed log queries", frontmatter["description"])

    def test_capability_registry_exposes_diagnosis_and_evidence_plan(self) -> None:
        diagnosis = get_capability_definition("diagnosis.enrich")
        evidence = get_capability_definition("evidence.plan")
        self.assertIsNotNone(diagnosis)
        self.assertIsNotNone(evidence)
        self.assertEqual(diagnosis.stage, "summarize_diagnosis")
        self.assertEqual(evidence.stage, "plan_evidence")

    def test_evidence_plan_sanitize_rejects_incomplete_logs_branch_meta(self) -> None:
        evidence = get_capability_definition("evidence.plan")
        self.assertIsNotNone(evidence)
        sanitized, dropped = evidence.sanitize_output(
            PromptSkillConsumeResult(
                payload={
                    "logs_branch_meta": {
                        "mode": "query",
                        "query_type": "logs",
                        "request_payload": {"query": 'service.name:"checkout"'},
                    }
                }
            )
        )
        self.assertEqual(sanitized.payload, {})
        self.assertIn("logs_branch_meta.query_request", dropped)

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

    def test_prompt_first_runtime_consumes_diagnosis_skill(self) -> None:
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

            class _FakeAgent:
                configured = True

                def select_skill(self, **_: object) -> SkillSelectionResult:
                    return SkillSelectionResult(selected_binding_key="claude.analysis\x001.0.0\x00diagnosis.enrich", reason="best match")

                def consume_skill(self, **_: object) -> PromptSkillConsumeResult:
                    return PromptSkillConsumeResult(
                        payload={
                            "diagnosis_patch": {
                                "summary": "Improved summary",
                                "root_cause": {
                                    "summary": "Improved root cause summary",
                                    "statement": "Improved statement",
                                    "confidence": 0.95,
                                },
                                "unknowns": ["missing traces"],
                                "next_steps": ["collect traces"],
                                "incident_id": "forbidden",
                            },
                        },
                        session_patch={
                            "latest_summary": {"summary": "Improved summary"},
                            "context_state_patch": {"skills": {"diagnosis_enrich": {"applied": True}}},
                        },
                        observations=[{"kind": "note", "message": "applied"}],
                    )

            runtime = OrchestratorRuntime(
                client=_FakeClient(),
                job_id="job-1",
                instance_id="orc-test",
                heartbeat_interval_seconds=10,
                skill_catalog=skill_catalog,
                skills_execution_mode="prompt_first",
                skill_agent=_FakeAgent(),
            )

            result = runtime.consume_diagnosis_enrich_skill(
                graph_state=GraphState(job_id="job-1", incident_id="inc-1"),
                input_payload={
                    "incident_id": "inc-1",
                    "incident_context": {"service": "svc-a"},
                    "input_hints": {},
                    "quality_gate_decision": "success",
                    "quality_gate_reasons": [],
                    "missing_evidence": [],
                    "evidence_ids": ["ev-1"],
                    "evidence_meta": [{"evidence_id": "ev-1"}],
                    "diagnosis_json": {
                        "summary": "Native summary",
                        "root_cause": {
                            "summary": "Native root summary",
                            "statement": "Native statement",
                            "confidence": 0.65,
                            "evidence_ids": ["ev-1"],
                        },
                    },
                },
            )

            self.assertIsInstance(result, dict)
            self.assertEqual(result["skill_id"], "claude.analysis")
            diagnosis_patch = result["payload"]["diagnosis_patch"]
            self.assertEqual(diagnosis_patch["summary"], "Improved summary")
            self.assertEqual(diagnosis_patch["root_cause"]["summary"], "Improved root cause summary")
            self.assertNotIn("incident_id", diagnosis_patch)
            self.assertNotIn("confidence", diagnosis_patch["root_cause"])
            self.assertEqual(result["session_patch"]["actor"], "skill:claude.analysis")
            self.assertEqual(result["session_patch"]["source"], "skill.prompt")

    def test_prompt_first_runtime_returns_none_when_selector_skips(self) -> None:
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

            class _FakeAgent:
                configured = True

                def select_skill(self, **_: object) -> SkillSelectionResult:
                    return SkillSelectionResult(selected_binding_key="", reason="skip")

            runtime = OrchestratorRuntime(
                client=_FakeClient(),
                job_id="job-1",
                instance_id="orc-test",
                heartbeat_interval_seconds=10,
                skill_catalog=skill_catalog,
                skills_execution_mode="prompt_first",
                skill_agent=_FakeAgent(),
            )

            result = runtime.consume_diagnosis_enrich_skill(
                graph_state=GraphState(job_id="job-1", incident_id="inc-1"),
                input_payload={"incident_id": "inc-1", "diagnosis_json": {}, "evidence_ids": []},
            )
            self.assertIsNone(result)

    def test_prompt_first_runtime_falls_back_when_agent_raises(self) -> None:
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

            class _BrokenAgent:
                configured = True

                def select_skill(self, **_: object) -> SkillSelectionResult:
                    raise RuntimeError("selector boom")

            runtime = OrchestratorRuntime(
                client=_FakeClient(),
                job_id="job-1",
                instance_id="orc-test",
                heartbeat_interval_seconds=10,
                skill_catalog=skill_catalog,
                skills_execution_mode="prompt_first",
                skill_agent=_BrokenAgent(),
            )

            result = runtime.consume_diagnosis_enrich_skill(
                graph_state=GraphState(job_id="job-1", incident_id="inc-1"),
                input_payload={"incident_id": "inc-1", "diagnosis_json": {}, "evidence_ids": []},
            )
            self.assertIsNone(result)

    def test_prompt_first_runtime_consumes_evidence_plan_skill(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            base = Path(tmp_dir)
            skill_dir = base / "remote"
            skill_dir.mkdir(parents=True)
            _write_skill_dir(skill_dir, name="Evidence Planner", description="Adjust evidence planning")
            bundle_path = base / "evidence.plan.zip"
            bundle_digest = _zip_dir(skill_dir, bundle_path)
            skill_catalog = SkillCatalog.from_resolved_skillsets(
                skillsets_payload=_resolved_skillsets_payload(
                    skillset_id="skillset.default",
                    skill_id="claude.evidence.plan",
                    version="1.0.0",
                    name="Evidence Planner",
                    description="Adjust evidence planning",
                    compatibility="",
                    bundle_path=bundle_path,
                    bundle_digest=bundle_digest,
                    capability="evidence.plan",
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

            class _FakeAgent:
                configured = True

                def select_skill(self, **_: object) -> SkillSelectionResult:
                    return SkillSelectionResult(
                        selected_binding_key="claude.evidence.plan\x001.0.0\x00evidence.plan",
                        reason="best match",
                    )

                def consume_skill(self, **_: object) -> PromptSkillConsumeResult:
                    return PromptSkillConsumeResult(
                        payload={
                            "evidence_plan_patch": {
                                "budget": {"max_calls": 2},
                                "metadata": {
                                    "prompt_skill": "elasticsearch.evidence.plan",
                                    "query_style": "ecs_query_string",
                                },
                            },
                            "evidence_candidates": [{"type": "logs", "name": "error_budget"}],
                            "metrics_branch_meta": {"mode": "mock", "query_type": "metrics"},
                            "logs_branch_meta": {
                                "mode": "query",
                                "query_type": "logs",
                                "request_payload": {
                                    "query": 'service.name:"svc-a" AND (kubernetes.namespace_name:"default" OR service.namespace:"default") AND (log.level:(error OR fatal) OR error.type:* OR error.message:* OR message:(*exception* OR *timeout* OR *panic*))',
                                    "datasource_id": "forbidden-ds",
                                    "limit": 10,
                                },
                                "query_request": {
                                    "queryText": 'service.name:"svc-a" AND (kubernetes.namespace_name:"default" OR service.namespace:"default") AND (log.level:(error OR fatal) OR error.type:* OR error.message:* OR message:(*exception* OR *timeout* OR *panic*))',
                                    "datasourceID": "forbidden-ds",
                                },
                            },
                            "session_patch": {"forbidden": True},
                            "diagnosis_json": {"forbidden": True},
                        },
                        observations=[{"kind": "note", "message": "planning updated"}],
                    )

            runtime = OrchestratorRuntime(
                client=_FakeClient(),
                job_id="job-1",
                instance_id="orc-test",
                heartbeat_interval_seconds=10,
                skill_catalog=skill_catalog,
                skills_execution_mode="prompt_first",
                skill_agent=_FakeAgent(),
            )

            state = GraphState(
                job_id="job-1",
                incident_id="inc-1",
                evidence_plan={"budget": {"max_calls": 1}, "candidates": [{"type": "metrics"}]},
                evidence_candidates=[{"type": "metrics"}],
                metrics_branch_meta={"mode": "query"},
                logs_branch_meta={
                    "mode": "query",
                    "query_type": "logs",
                    "request_payload": {
                        "datasource_id": "ds-logs",
                        "query": 'message:(*error* OR *exception*)',
                        "start_ts": 100,
                        "end_ts": 200,
                        "limit": 200,
                    },
                    "query_request": {
                        "datasourceID": "ds-logs",
                        "queryText": 'message:(*error* OR *exception*)',
                        "queryJSON": "{}",
                    },
                },
                evidence_mode="query",
                incident_context={"service": "svc-a", "namespace": "default"},
                input_hints={},
            )
            result = runtime.consume_prompt_skill(capability="evidence.plan", graph_state=state)

            self.assertIsInstance(result, dict)
            self.assertEqual(result["skill_id"], "claude.evidence.plan")
            self.assertEqual(state.evidence_plan["budget"]["max_calls"], 2)
            self.assertEqual(state.evidence_candidates, [{"type": "logs", "name": "error_budget"}])
            self.assertEqual(state.evidence_plan["candidates"], [{"type": "logs", "name": "error_budget"}])
            self.assertEqual(state.evidence_plan["metadata"]["prompt_skill"], "elasticsearch.evidence.plan")
            self.assertEqual(state.evidence_plan["metadata"]["query_style"], "ecs_query_string")
            self.assertEqual(state.metrics_branch_meta["mode"], "mock")
            self.assertEqual(state.logs_branch_meta["mode"], "query")
            self.assertEqual(state.logs_branch_meta["request_payload"]["datasource_id"], "ds-logs")
            self.assertEqual(state.logs_branch_meta["request_payload"]["start_ts"], 100)
            self.assertEqual(state.logs_branch_meta["request_payload"]["end_ts"], 200)
            self.assertEqual(state.logs_branch_meta["request_payload"]["limit"], 200)
            self.assertEqual(state.logs_branch_meta["query_request"]["datasourceID"], "ds-logs")
            self.assertEqual(state.logs_branch_meta["query_request"]["queryJSON"], "{}")
            self.assertIn('service.name:"svc-a"', state.logs_branch_meta["request_payload"]["query"])
            self.assertEqual(state.logs_branch_meta["request_payload"]["query"], state.logs_branch_meta["query_request"]["queryText"])
            self.assertEqual(result["payload"]["evidence_candidates"], [{"type": "logs", "name": "error_budget"}])
            self.assertEqual(result["session_patch"], {})

    def test_prompt_first_evidence_plan_single_hop_tool_call_warms_logs_state(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            base = Path(tmp_dir)
            skill_dir = base / "remote"
            skill_dir.mkdir(parents=True)
            _write_skill_dir(skill_dir, name="Elasticsearch Planner", description="Adjust evidence planning with one query")
            bundle_path = base / "evidence.plan.tool.zip"
            bundle_digest = _zip_dir(skill_dir, bundle_path)
            skill_catalog = SkillCatalog.from_resolved_skillsets(
                skillsets_payload=_resolved_skillsets_payload(
                    skillset_id="skillset.default",
                    skill_id="elasticsearch.evidence.plan",
                    version="1.0.0",
                    name="Elasticsearch Planner",
                    description="Adjust evidence planning with one query",
                    compatibility="",
                    bundle_path=bundle_path,
                    bundle_digest=bundle_digest,
                    capability="evidence.plan",
                    allowed_tools=["query_logs"],
                    priority=150,
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

            class _FakeAgent:
                configured = True

                def select_skill(self, **_: object) -> SkillSelectionResult:
                    return SkillSelectionResult(
                        selected_binding_key="elasticsearch.evidence.plan\x001.0.0\x00evidence.plan",
                        reason="best match",
                    )

                def plan_tool_call(self, **_: object):
                    class _Plan:
                        tool = "mcp.query_logs"
                        input_payload = {
                            "datasource_id": "ds-logs",
                            "query": 'service.name:"svc-a" AND (kubernetes.namespace_name:"default" OR service.namespace:"default") AND (log.level:(error OR fatal) OR error.type:* OR error.message:* OR message:(*exception* OR *timeout* OR *panic*))',
                            "start_ts": 100,
                            "end_ts": 200,
                            "limit": 200,
                        }
                        reason = "warm logs query first"

                    return _Plan()

                def consume_after_tool(self, **_: object) -> PromptSkillConsumeResult:
                    return PromptSkillConsumeResult(
                        payload={
                            "evidence_plan_patch": {
                                "metadata": {
                                    "prompt_skill": "elasticsearch.evidence.plan",
                                    "query_style": "ecs_query_string",
                                }
                            },
                            "logs_branch_meta": {
                                "mode": "query",
                                "query_type": "logs",
                                "request_payload": {
                                    "query": 'service.name:"svc-a" AND (kubernetes.namespace_name:"default" OR service.namespace:"default") AND (log.level:(error OR fatal) OR error.type:* OR error.message:* OR message:(*exception* OR *timeout* OR *panic*))'
                                },
                                "query_request": {
                                    "queryText": 'service.name:"svc-a" AND (kubernetes.namespace_name:"default" OR service.namespace:"default") AND (log.level:(error OR fatal) OR error.type:* OR error.message:* OR message:(*exception* OR *timeout* OR *panic*))'
                                },
                            },
                        },
                        observations=[{"kind": "note", "message": "applied after tool"}],
                    )

            runtime = OrchestratorRuntime(
                client=_FakeClient(),
                job_id="job-1",
                instance_id="orc-test",
                heartbeat_interval_seconds=10,
                skill_catalog=skill_catalog,
                skills_execution_mode="prompt_first",
                skills_tool_calling_mode="evidence_plan_single_hop",
                skill_agent=_FakeAgent(),
            )

            toolcalls: list[dict[str, object]] = []
            runtime.report_tool_call = lambda **kwargs: toolcalls.append(kwargs) or 1  # type: ignore[method-assign]
            runtime.query_logs = lambda **kwargs: {  # type: ignore[method-assign]
                "queryResultJSON": '{"data":{"result":[{"stream":{"service.name":"svc-a"},"values":[[1710000000,"boom"]]}]}}',
                "rowCount": 1,
                "resultSizeBytes": 88,
                "isTruncated": False,
            }

            state = GraphState(
                job_id="job-1",
                incident_id="inc-1",
                evidence_plan={"budget": {"max_calls": 1}, "candidates": [{"type": "logs"}]},
                evidence_candidates=[{"type": "logs"}],
                logs_branch_meta={
                    "mode": "query",
                    "query_type": "logs",
                    "request_payload": {
                        "datasource_id": "ds-logs",
                        "query": 'message:(*error* OR *exception*)',
                        "start_ts": 100,
                        "end_ts": 200,
                        "limit": 200,
                    },
                    "query_request": {
                        "datasourceID": "ds-logs",
                        "queryText": 'message:(*error* OR *exception*)',
                        "queryJSON": "{}",
                    },
                },
                evidence_mode="query",
                incident_context={"service": "svc-a", "namespace": "default"},
                input_hints={},
            )

            result = runtime.consume_prompt_skill(capability="evidence.plan", graph_state=state)

            self.assertIsInstance(result, dict)
            self.assertEqual(result["skill_id"], "elasticsearch.evidence.plan")
            self.assertEqual(state.evidence_plan["metadata"]["prompt_skill"], "elasticsearch.evidence.plan")
            self.assertEqual(state.logs_query_status, "ok")
            self.assertEqual(state.logs_query_error, None)
            self.assertEqual(state.logs_query_request["datasource_id"], "ds-logs")
            self.assertTrue(state.logs_branch_meta["tool_result_reusable"])
            self.assertEqual(state.logs_branch_meta["tool_result_source"], "skill_prompt_first")
            self.assertIn('service.name:"svc-a"', state.logs_branch_meta["request_payload"]["query"])
            self.assertEqual(state.logs_branch_meta["request_payload"]["datasource_id"], "ds-logs")
            self.assertEqual(state.logs_branch_meta["request_payload"]["limit"], 200)
            self.assertEqual(len(toolcalls), 1)
            self.assertEqual(toolcalls[0]["node_name"], "skill.evidence.plan")
            self.assertEqual(toolcalls[0]["tool_name"], "mcp.query_logs")

    def test_prompt_first_evidence_plan_invalid_tool_plan_falls_back(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            base = Path(tmp_dir)
            skill_dir = base / "remote"
            skill_dir.mkdir(parents=True)
            _write_skill_dir(skill_dir, name="Elasticsearch Planner", description="Adjust evidence planning with one query")
            bundle_path = base / "evidence.plan.tool.zip"
            bundle_digest = _zip_dir(skill_dir, bundle_path)
            skill_catalog = SkillCatalog.from_resolved_skillsets(
                skillsets_payload=_resolved_skillsets_payload(
                    skillset_id="skillset.default",
                    skill_id="elasticsearch.evidence.plan",
                    version="1.0.0",
                    name="Elasticsearch Planner",
                    description="Adjust evidence planning with one query",
                    compatibility="",
                    bundle_path=bundle_path,
                    bundle_digest=bundle_digest,
                    capability="evidence.plan",
                    allowed_tools=["query_logs"],
                    priority=150,
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

            class _BrokenToolAgent:
                configured = True

                def select_skill(self, **_: object) -> SkillSelectionResult:
                    return SkillSelectionResult(
                        selected_binding_key="elasticsearch.evidence.plan\x001.0.0\x00evidence.plan",
                        reason="best match",
                    )

                def plan_tool_call(self, **_: object):
                    class _Plan:
                        tool = "mcp.query_metrics"
                        input_payload = {"expr": "sum(up)"}
                        reason = "wrong tool"

                    return _Plan()

            runtime = OrchestratorRuntime(
                client=_FakeClient(),
                job_id="job-1",
                instance_id="orc-test",
                heartbeat_interval_seconds=10,
                skill_catalog=skill_catalog,
                skills_execution_mode="prompt_first",
                skills_tool_calling_mode="evidence_plan_single_hop",
                skill_agent=_BrokenToolAgent(),
            )
            runtime.report_tool_call = lambda **kwargs: 1  # type: ignore[method-assign]
            runtime.query_logs = lambda **kwargs: self.fail("query_logs must not run for invalid tool plan")  # type: ignore[method-assign]

            state = GraphState(
                job_id="job-1",
                incident_id="inc-1",
                logs_branch_meta={
                    "mode": "query",
                    "query_type": "logs",
                    "request_payload": {
                        "datasource_id": "ds-logs",
                        "query": 'message:(*error* OR *exception*)',
                        "start_ts": 100,
                        "end_ts": 200,
                        "limit": 200,
                    },
                    "query_request": {
                        "datasourceID": "ds-logs",
                        "queryText": 'message:(*error* OR *exception*)',
                        "queryJSON": "{}",
                    },
                },
                evidence_mode="query",
            )
            result = runtime.consume_prompt_skill(capability="evidence.plan", graph_state=state)
            self.assertIsNone(result)
            self.assertIsNone(state.logs_query_status)

    def test_prompt_first_evidence_plan_without_allowed_tool_uses_prompt_only_path(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            base = Path(tmp_dir)
            skill_dir = base / "remote"
            skill_dir.mkdir(parents=True)
            _write_skill_dir(skill_dir, name="Evidence Planner", description="Adjust evidence planning")
            bundle_path = base / "evidence.plan.zip"
            bundle_digest = _zip_dir(skill_dir, bundle_path)
            skill_catalog = SkillCatalog.from_resolved_skillsets(
                skillsets_payload=_resolved_skillsets_payload(
                    skillset_id="skillset.default",
                    skill_id="claude.evidence.plan",
                    version="1.0.0",
                    name="Evidence Planner",
                    description="Adjust evidence planning",
                    compatibility="",
                    bundle_path=bundle_path,
                    bundle_digest=bundle_digest,
                    capability="evidence.plan",
                    allowed_tools=[],
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

            class _PromptOnlyAgent:
                configured = True

                def select_skill(self, **_: object) -> SkillSelectionResult:
                    return SkillSelectionResult(
                        selected_binding_key="claude.evidence.plan\x001.0.0\x00evidence.plan",
                        reason="best match",
                    )

                def consume_skill(self, **_: object) -> PromptSkillConsumeResult:
                    return PromptSkillConsumeResult(
                        payload={
                            "evidence_plan_patch": {
                                "metadata": {
                                    "prompt_skill": "claude.evidence.plan",
                                }
                            }
                        }
                    )

            runtime = OrchestratorRuntime(
                client=_FakeClient(),
                job_id="job-1",
                instance_id="orc-test",
                heartbeat_interval_seconds=10,
                skill_catalog=skill_catalog,
                skills_execution_mode="prompt_first",
                skills_tool_calling_mode="evidence_plan_single_hop",
                skill_agent=_PromptOnlyAgent(),
            )
            runtime.query_logs = lambda **kwargs: self.fail("query_logs must not run when binding allow_tools is empty")  # type: ignore[method-assign]

            state = GraphState(job_id="job-1", incident_id="inc-1", evidence_plan={"candidates": []}, evidence_mode="query")
            result = runtime.consume_prompt_skill(capability="evidence.plan", graph_state=state)
            self.assertIsInstance(result, dict)
            self.assertEqual(state.evidence_plan["metadata"]["prompt_skill"], "claude.evidence.plan")
            self.assertIsNone(state.logs_query_status)

    def test_query_logs_node_reuses_prewarmed_skill_result(self) -> None:
        class _ReuseRuntime:
            def __init__(self) -> None:
                self.reported: list[dict[str, object]] = []
                self.observations: list[dict[str, object]] = []

            def report_tool_call(self, **kwargs):
                self.reported.append(kwargs)
                return len(self.reported)

            def report_observation(self, **kwargs):
                self.observations.append(kwargs)
                return len(self.observations)

            def query_logs(self, **kwargs):
                raise AssertionError("query_logs should not execute when prewarmed result is reusable")

        runtime = _ReuseRuntime()
        state = GraphState(
            job_id="job-1",
            incident_id="inc-1",
            logs_branch_meta={
                "mode": "query",
                "query_type": "logs",
                "tool_result_reusable": True,
                "tool_result_source": "skill_prompt_first",
                "request_payload": {
                    "datasource_id": "ds-logs",
                    "query": 'service.name:"svc-a"',
                    "start_ts": 100,
                    "end_ts": 200,
                    "limit": 200,
                },
                "query_request": {
                    "datasourceID": "ds-logs",
                    "queryText": 'service.name:"svc-a"',
                    "queryJSON": "{}",
                },
            },
            logs_query_status="ok",
            logs_query_output={
                "queryResultJSON": '{"data":{"result":[{"stream":{"service.name":"svc-a"},"values":[[1710000000,"boom"]]}]}}',
                "rowCount": 1,
                "resultSizeBytes": 88,
                "isTruncated": False,
            },
            logs_query_latency_ms=12,
            logs_query_result_size_bytes=88,
        )
        out = query_logs_node(state, runtime)  # type: ignore[arg-type]
        self.assertEqual(out["logs_query_status"], "ok")
        self.assertEqual(out["logs_query_latency_ms"], 12)
        self.assertEqual(len(runtime.reported), 1)
        self.assertEqual(runtime.reported[0]["tool_name"], "evidence.logs.reuse")
        self.assertEqual(len(runtime.observations), 1)
        self.assertEqual(runtime.observations[0]["tool"], "skill.tool_reuse")


if __name__ == "__main__":
    unittest.main()
