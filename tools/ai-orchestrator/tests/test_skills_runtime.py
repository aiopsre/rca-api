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
from orchestrator.langgraph.nodes import query_logs_node, query_metrics_node
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
    resources: list[tuple[str, str]] | None = None,
    script_body: str | None = None,
) -> None:
    compatibility_line = f"compatibility: {compatibility}\n" if compatibility else ""
    (root_dir / "SKILL.md").write_text(
        f"---\nname: {name}\ndescription: {description}\n{compatibility_line}---\n\n# test skill\n",
        encoding="utf-8",
    )
    resource_items = resources or [("templates/guide.txt", "guide\n")]
    for relative_path, content in resource_items:
        resource_path = root_dir / relative_path
        resource_path.parent.mkdir(parents=True, exist_ok=True)
        resource_path.write_text(content, encoding="utf-8")
    if script_body is not None:
        executor_path = root_dir / "scripts" / "executor.py"
        executor_path.parent.mkdir(parents=True, exist_ok=True)
        executor_path.write_text(script_body, encoding="utf-8")


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
    role: str = "executor",
    executor_mode: str | None = None,
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
                    "role": role,
                    "executorMode": executor_mode,
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

    def test_skill_catalog_lists_and_loads_selected_resources_only(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            base = Path(tmp_dir)
            skill_dir = base / "remote"
            skill_dir.mkdir(parents=True)
            _write_skill_dir(
                skill_dir,
                name="Claude Analysis",
                description="Analyze incident evidence",
                resources=[
                    ("references/ecs-fields.md", "# ECS Fields\n\nUse service.name and namespace fields.\n"),
                    ("examples/query.txt", 'service.name:"checkout"\n'),
                    ("templates/output.md", "# Output Rules\n\nKeep payload minimal.\n"),
                ],
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
                    compatibility="",
                    bundle_path=bundle_path,
                    bundle_digest=bundle_digest,
                ),
                cache_dir=str(base / "cache"),
            )

            candidate = catalog.candidates_for_capability("diagnosis.enrich")[0]
            resources = catalog.list_skill_resources(candidate.binding_key)
            self.assertEqual(
                [item.resource_id for item in resources],
                [
                    "examples/query.txt",
                    "references/ecs-fields.md",
                    "templates/output.md",
                ],
            )
            self.assertEqual(resources[1].resource_kind, "reference")
            self.assertEqual(resources[1].title, "ECS Fields")
            self.assertIn("service.name", resources[1].preview)

            loaded = catalog.load_skill_resources(
                candidate.binding_key,
                ["references/ecs-fields.md", "templates/output.md"],
            )
            self.assertEqual([item.resource_id for item in loaded], ["references/ecs-fields.md", "templates/output.md"])
            self.assertEqual(loaded[0].to_agent_payload()["resource_kind"], "reference")
            self.assertIn("service.name", loaded[0].content)
            self.assertIn("Keep payload minimal", loaded[1].content)

    def test_skill_catalog_ignores_unsupported_and_oversized_resources(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            base = Path(tmp_dir)
            skill_dir = base / "remote"
            skill_dir.mkdir(parents=True)
            _write_skill_dir(
                skill_dir,
                name="Claude Analysis",
                description="Analyze incident evidence",
                resources=[
                    ("references/valid.md", "# Valid\n\nShort preview.\n"),
                    ("references/binary.bin", "not-supported\n"),
                    ("examples/too-large.txt", "a" * (33 * 1024)),
                ],
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
                    compatibility="",
                    bundle_path=bundle_path,
                    bundle_digest=bundle_digest,
                ),
                cache_dir=str(base / "cache"),
            )

            candidate = catalog.candidates_for_capability("diagnosis.enrich")[0]
            resources = catalog.list_skill_resources(candidate.binding_key)
            self.assertEqual([item.resource_id for item in resources], ["references/valid.md"])

    def test_candidates_for_capability_split_by_role(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            base = Path(tmp_dir)
            knowledge_dir = base / "knowledge"
            executor_dir = base / "executor"
            knowledge_dir.mkdir(parents=True)
            executor_dir.mkdir(parents=True)
            _write_skill_dir(knowledge_dir, name="Elastic Knowledge", description="Elastic guidance")
            _write_skill_dir(executor_dir, name="Prompt Planner", description="Executor planner")
            knowledge_bundle = base / "knowledge.zip"
            executor_bundle = base / "executor.zip"
            knowledge_digest = _zip_dir(knowledge_dir, knowledge_bundle)
            executor_digest = _zip_dir(executor_dir, executor_bundle)
            payload = [
                {
                    "skillsetID": "skillset.default",
                    "skills": [
                        {
                            "skillID": "elasticsearch.evidence.plan",
                            "version": "1.0.0",
                            "artifactURL": knowledge_bundle.resolve().as_uri(),
                            "bundleDigest": knowledge_digest,
                            "manifestJSON": '{"bundle_format":"claude_skill_v1","instruction_file":"SKILL.md","name":"Elastic Knowledge","description":"Elastic guidance","compatibility":""}',
                            "capability": "evidence.plan",
                            "role": "knowledge",
                            "priority": 150,
                            "enabled": True,
                        },
                        {
                            "skillID": "claude.evidence.prompt_planner",
                            "version": "1.0.0",
                            "artifactURL": executor_bundle.resolve().as_uri(),
                            "bundleDigest": executor_digest,
                            "manifestJSON": '{"bundle_format":"claude_skill_v1","instruction_file":"SKILL.md","name":"Prompt Planner","description":"Executor planner","compatibility":""}',
                            "capability": "evidence.plan",
                            "role": "executor",
                            "priority": 100,
                            "enabled": True,
                        },
                    ],
                }
            ]
            catalog = SkillCatalog.from_resolved_skillsets(
                skillsets_payload=payload,
                cache_dir=str(base / "cache"),
            )
            knowledge = catalog.knowledge_candidates_for_capability("evidence.plan")
            executors = catalog.executor_candidates_for_capability("evidence.plan")
            self.assertEqual([item.skill_id for item in knowledge], ["elasticsearch.evidence.plan"])
            self.assertEqual([item.skill_id for item in executors], ["claude.evidence.prompt_planner"])
            self.assertEqual(knowledge[0].role, "knowledge")
            self.assertEqual(executors[0].role, "executor")
            self.assertEqual(knowledge[0].executor_mode, "")
            self.assertEqual(executors[0].executor_mode, "prompt")

    def test_executor_candidates_include_script_mode(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            base = Path(tmp_dir)
            skill_dir = base / "script-executor"
            skill_dir.mkdir(parents=True)
            _write_skill_dir(
                skill_dir,
                name="Script Enricher",
                description="Executor planner",
                script_body=(
                    "def run(input_payload, ctx):\n"
                    "    return {\"payload\": {}, \"session_patch\": {}, \"observations\": []}\n"
                ),
            )
            bundle_path = base / "script-executor.zip"
            bundle_digest = _zip_dir(skill_dir, bundle_path)
            catalog = SkillCatalog.from_resolved_skillsets(
                skillsets_payload=_resolved_skillsets_payload(
                    skillset_id="skillset.default",
                    skill_id="claude.diagnosis.script_enricher",
                    version="1.0.0",
                    name="Script Enricher",
                    description="Executor planner",
                    compatibility="",
                    bundle_path=bundle_path,
                    bundle_digest=bundle_digest,
                    role="executor",
                    executor_mode="script",
                ),
                cache_dir=str(base / "cache"),
            )

            candidate = catalog.executor_candidates_for_capability("diagnosis.enrich")[0]
            self.assertEqual(candidate.executor_mode, "script")

    def test_checked_in_prompt_only_bundle_has_valid_frontmatter(self) -> None:
        skill_path = REPO_ROOT / "tools" / "ai-orchestrator" / "skill-bundles" / "diagnosis-enrich" / "SKILL.md"
        frontmatter = parse_skill_frontmatter(skill_path.read_text(encoding="utf-8"))
        self.assertEqual(frontmatter["name"], "RCA Diagnosis Enricher")
        self.assertIn("Enrich the native RCA diagnosis", frontmatter["description"])

    def test_checked_in_prompt_only_evidence_bundle_has_valid_frontmatter(self) -> None:
        skill_path = REPO_ROOT / "tools" / "ai-orchestrator" / "skill-bundles" / "evidence-plan" / "SKILL.md"
        frontmatter = parse_skill_frontmatter(skill_path.read_text(encoding="utf-8"))
        self.assertEqual(frontmatter["name"], "RCA Evidence Planner")
        self.assertIn("single executor for evidence.plan", frontmatter["description"])

    def test_checked_in_elasticsearch_evidence_bundle_has_valid_frontmatter(self) -> None:
        skill_path = REPO_ROOT / "tools" / "ai-orchestrator" / "skill-bundles" / "elasticsearch-evidence-plan" / "SKILL.md"
        frontmatter = parse_skill_frontmatter(skill_path.read_text(encoding="utf-8"))
        self.assertEqual(frontmatter["name"], "Elasticsearch Evidence Planner")
        self.assertIn("Elasticsearch-backed logs knowledge", frontmatter["description"])

    def test_checked_in_prometheus_evidence_bundle_has_valid_frontmatter(self) -> None:
        skill_path = REPO_ROOT / "tools" / "ai-orchestrator" / "skill-bundles" / "prometheus-evidence-plan" / "SKILL.md"
        frontmatter = parse_skill_frontmatter(skill_path.read_text(encoding="utf-8"))
        self.assertEqual(frontmatter["name"], "Prometheus Evidence Planner Knowledge")
        self.assertIn("Prometheus-backed metrics", frontmatter["description"])

    def test_checked_in_diagnosis_script_bundle_has_valid_frontmatter(self) -> None:
        skill_path = REPO_ROOT / "tools" / "ai-orchestrator" / "skill-bundles" / "diagnosis-script-enrich" / "SKILL.md"
        frontmatter = parse_skill_frontmatter(skill_path.read_text(encoding="utf-8"))
        self.assertEqual(frontmatter["name"], "RCA Diagnosis Script Enricher")
        self.assertIn("script executor", frontmatter["description"].lower())

    def test_checked_in_evidence_script_bundle_has_valid_frontmatter(self) -> None:
        skill_path = REPO_ROOT / "tools" / "ai-orchestrator" / "skill-bundles" / "evidence-script-plan" / "SKILL.md"
        frontmatter = parse_skill_frontmatter(skill_path.read_text(encoding="utf-8"))
        self.assertEqual(frontmatter["name"], "RCA Evidence Script Planner")
        self.assertIn("script executor", frontmatter["description"].lower())

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
            _write_skill_dir(
                skill_dir,
                name="Claude Analysis",
                description="Analyze incident evidence",
                resources=[
                    ("references/quality-gate-guidance.md", "# Quality Gate Guidance\n\nCalibrate certainty by quality gate.\n"),
                    ("templates/diagnosis-output-rules.md", "# Output Rules\n\nRewrite native wording.\n"),
                ],
            )
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
            _write_skill_dir(
                skill_dir,
                name="Claude Analysis",
                description="Analyze incident evidence",
                resources=[
                    ("references/quality-gate-guidance.md", "# Quality Gate Guidance\n\nCalibrate certainty by quality gate.\n"),
                    ("templates/diagnosis-output-rules.md", "# Output Rules\n\nRewrite native wording.\n"),
                ],
            )
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

                def select_skill_resources(self, **kwargs: object):
                    assert kwargs.get("role") == "executor"

                    class _Selection:
                        reason = "load diagnosis guidance only"
                        selected_resource_ids = [
                            "references/quality-gate-guidance.md",
                            "templates/diagnosis-output-rules.md",
                        ]

                    return _Selection()

                def consume_skill(self, **kwargs: object) -> PromptSkillConsumeResult:
                    knowledge_context = kwargs.get("knowledge_context")
                    skill_resources = kwargs.get("skill_resources")
                    assert knowledge_context == []
                    assert isinstance(skill_resources, list)
                    assert [item.get("resource_id") for item in skill_resources] == [
                        "references/quality-gate-guidance.md",
                        "templates/diagnosis-output-rules.md",
                    ]
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

    def test_prompt_first_runtime_executes_script_diagnosis_skill(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            base = Path(tmp_dir)
            skill_dir = base / "remote"
            skill_dir.mkdir(parents=True)
            _write_skill_dir(
                skill_dir,
                name="Script Enricher",
                description="Analyze incident evidence",
                resources=[
                    ("references/quality-gate-guidance.md", "# Quality Gate Guidance\n\nUse lower confidence wording when signals are partial.\n"),
                    ("templates/diagnosis-output-rules.md", "# Output Rules\n\nReturn concise operator wording.\n"),
                ],
                script_body=(
                    "def run(input_payload, ctx):\n"
                    "    resources = [item.get('resource_id') for item in ctx.get('skill_resources', []) if isinstance(item, dict)]\n"
                    "    return {\n"
                    "        'payload': {\n"
                    "            'diagnosis_patch': {\n"
                    "                'summary': 'Script improved summary',\n"
                    "                'root_cause': {\n"
                    "                    'summary': 'Script improved root cause summary',\n"
                    "                    'statement': 'Script improved statement',\n"
                    "                    'confidence': 0.99,\n"
                    "                },\n"
                    "                'incident_id': 'forbidden',\n"
                    "                'next_steps': ['collect traces'],\n"
                    "            }\n"
                    "        },\n"
                    "        'session_patch': {\n"
                    "            'latest_summary': {'summary': 'Script improved summary'},\n"
                    "            'context_state_patch': {'skills': {'diagnosis_script_enrich': {'applied': True, 'resources': resources}}},\n"
                    "        },\n"
                    "        'observations': [{'kind': 'note', 'message': 'script applied'}],\n"
                    "    }\n"
                ),
            )
            bundle_path = base / "script-enricher.zip"
            bundle_digest = _zip_dir(skill_dir, bundle_path)
            skill_catalog = SkillCatalog.from_resolved_skillsets(
                skillsets_payload=_resolved_skillsets_payload(
                    skillset_id="skillset.default",
                    skill_id="claude.diagnosis.script_enricher",
                    version="1.0.0",
                    name="Script Enricher",
                    description="Analyze incident evidence",
                    compatibility="",
                    bundle_path=bundle_path,
                    bundle_digest=bundle_digest,
                    executor_mode="script",
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

            class _SelectingAgent:
                configured = True

                def select_skill(self, **_: object) -> SkillSelectionResult:
                    return SkillSelectionResult(
                        selected_binding_key="claude.diagnosis.script_enricher\x001.0.0\x00diagnosis.enrich",
                        reason="use script executor",
                    )

                def select_skill_resources(self, **kwargs: object):
                    assert kwargs.get("role") == "executor"

                    class _Selection:
                        reason = "load script diagnosis resources"
                        selected_resource_ids = [
                            "references/quality-gate-guidance.md",
                            "templates/diagnosis-output-rules.md",
                        ]

                    return _Selection()

                def consume_skill(self, **_: object) -> PromptSkillConsumeResult:
                    raise AssertionError("script executor should not call consume_skill")

            runtime = OrchestratorRuntime(
                client=_FakeClient(),
                job_id="job-1",
                instance_id="orc-test",
                heartbeat_interval_seconds=10,
                skill_catalog=skill_catalog,
                skills_execution_mode="prompt_first",
                skill_agent=_SelectingAgent(),
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
            self.assertEqual(result["skill_id"], "claude.diagnosis.script_enricher")
            diagnosis_patch = result["payload"]["diagnosis_patch"]
            self.assertEqual(diagnosis_patch["summary"], "Script improved summary")
            self.assertEqual(diagnosis_patch["root_cause"]["summary"], "Script improved root cause summary")
            self.assertNotIn("incident_id", diagnosis_patch)
            self.assertNotIn("confidence", diagnosis_patch["root_cause"])
            self.assertEqual(result["session_patch"]["actor"], "skill:claude.diagnosis.script_enricher")
            self.assertEqual(result["session_patch"]["source"], "skill.script")

    def test_prompt_first_runtime_falls_back_for_invalid_script_executor(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            base = Path(tmp_dir)
            skill_dir = base / "remote"
            skill_dir.mkdir(parents=True)
            _write_skill_dir(
                skill_dir,
                name="Broken Script Enricher",
                description="Analyze incident evidence",
            )
            bundle_path = base / "broken-script-enricher.zip"
            bundle_digest = _zip_dir(skill_dir, bundle_path)
            skill_catalog = SkillCatalog.from_resolved_skillsets(
                skillsets_payload=_resolved_skillsets_payload(
                    skillset_id="skillset.default",
                    skill_id="claude.diagnosis.script_enricher",
                    version="1.0.0",
                    name="Broken Script Enricher",
                    description="Analyze incident evidence",
                    compatibility="",
                    bundle_path=bundle_path,
                    bundle_digest=bundle_digest,
                    executor_mode="script",
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

            class _SelectingAgent:
                configured = True

                def select_skill(self, **_: object) -> SkillSelectionResult:
                    return SkillSelectionResult(
                        selected_binding_key="claude.diagnosis.script_enricher\x001.0.0\x00diagnosis.enrich",
                        reason="use script executor",
                    )

            runtime = OrchestratorRuntime(
                client=_FakeClient(),
                job_id="job-1",
                instance_id="orc-test",
                heartbeat_interval_seconds=10,
                skill_catalog=skill_catalog,
                skills_execution_mode="prompt_first",
                skill_agent=_SelectingAgent(),
            )

            result = runtime.consume_diagnosis_enrich_skill(
                graph_state=GraphState(job_id="job-1", incident_id="inc-1"),
                input_payload={"incident_id": "inc-1", "diagnosis_json": {}, "evidence_ids": []},
            )
            self.assertIsNone(result)

    def test_prompt_first_runtime_rejects_tool_calls_from_diagnosis_script_executor(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            base = Path(tmp_dir)
            skill_dir = base / "remote"
            skill_dir.mkdir(parents=True)
            _write_skill_dir(
                skill_dir,
                name="Broken Script Enricher",
                description="Analyze incident evidence",
                script_body=(
                    "def run(input_payload, ctx):\n"
                    "    return {\n"
                    "        'tool_calls': [\n"
                    "            {\n"
                    "                'tool': 'mcp.query_logs',\n"
                    "                'input': {\n"
                    "                    'datasource_id': 'ds-logs',\n"
                    "                    'query': 'message:error',\n"
                    "                    'start_ts': 100,\n"
                    "                    'end_ts': 200,\n"
                    "                    'limit': 10,\n"
                    "                },\n"
                    "            }\n"
                    "        ]\n"
                    "    }\n"
                ),
            )
            bundle_path = base / "broken-script-enricher.zip"
            bundle_digest = _zip_dir(skill_dir, bundle_path)
            skill_catalog = SkillCatalog.from_resolved_skillsets(
                skillsets_payload=_resolved_skillsets_payload(
                    skillset_id="skillset.default",
                    skill_id="claude.diagnosis.script_enricher",
                    version="1.0.0",
                    name="Broken Script Enricher",
                    description="Analyze incident evidence",
                    compatibility="",
                    bundle_path=bundle_path,
                    bundle_digest=bundle_digest,
                    executor_mode="script",
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

            class _SelectingAgent:
                configured = True

                def select_skill(self, **_: object) -> SkillSelectionResult:
                    return SkillSelectionResult(
                        selected_binding_key="claude.diagnosis.script_enricher\x001.0.0\x00diagnosis.enrich",
                        reason="use script executor",
                    )

            runtime = OrchestratorRuntime(
                client=_FakeClient(),
                job_id="job-1",
                instance_id="orc-test",
                heartbeat_interval_seconds=10,
                skill_catalog=skill_catalog,
                skills_execution_mode="prompt_first",
                skill_agent=_SelectingAgent(),
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
                            "metrics_branch_meta": {
                                "mode": "query",
                                "query_type": "metrics",
                                "request_payload": {
                                    "promql": 'sum(rate(http_requests_total{service="svc-a"}[5m]))',
                                    "step_seconds": 60,
                                    "datasource_id": "forbidden-ds",
                                },
                                "query_request": {
                                    "queryText": 'sum(rate(http_requests_total{service="svc-a"}[5m]))',
                                    "datasourceID": "forbidden-ds",
                                },
                            },
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
                metrics_branch_meta={
                    "mode": "query",
                    "query_type": "metrics",
                    "request_payload": {
                        "datasource_id": "ds-metrics",
                        "promql": "sum(up)",
                        "start_ts": 100,
                        "end_ts": 200,
                        "step_seconds": 30,
                    },
                    "query_request": {
                        "datasourceID": "ds-metrics",
                        "queryText": "sum(up)",
                        "queryJSON": "{}",
                    },
                },
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
            self.assertEqual(state.metrics_branch_meta["mode"], "query")
            self.assertEqual(state.metrics_branch_meta["request_payload"]["datasource_id"], "ds-metrics")
            self.assertEqual(state.metrics_branch_meta["request_payload"]["start_ts"], 100)
            self.assertEqual(state.metrics_branch_meta["request_payload"]["end_ts"], 200)
            self.assertEqual(state.metrics_branch_meta["request_payload"]["step_seconds"], 60)
            self.assertEqual(
                state.metrics_branch_meta["request_payload"]["promql"],
                'sum(rate(http_requests_total{service="svc-a"}[5m]))',
            )
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

    def test_prompt_first_runtime_consumes_multiple_knowledge_skills_and_single_executor(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            base = Path(tmp_dir)
            elastic_dir = base / "elastic"
            metrics_dir = base / "metrics"
            executor_dir = base / "executor"
            elastic_dir.mkdir(parents=True)
            metrics_dir.mkdir(parents=True)
            executor_dir.mkdir(parents=True)
            _write_skill_dir(elastic_dir, name="Elastic Knowledge", description="Elastic logs guidance")
            _write_skill_dir(metrics_dir, name="Prometheus Knowledge", description="Prometheus metrics guidance")
            _write_skill_dir(executor_dir, name="Prompt Planner", description="Planner executor")
            elastic_bundle = base / "elastic.zip"
            metrics_bundle = base / "metrics.zip"
            executor_bundle = base / "executor.zip"
            elastic_digest = _zip_dir(elastic_dir, elastic_bundle)
            metrics_digest = _zip_dir(metrics_dir, metrics_bundle)
            executor_digest = _zip_dir(executor_dir, executor_bundle)
            skill_catalog = SkillCatalog.from_resolved_skillsets(
                skillsets_payload=[
                    {
                        "skillsetID": "skillset.default",
                        "skills": [
                            {
                                "skillID": "elasticsearch.evidence.plan",
                                "version": "1.0.0",
                                "artifactURL": elastic_bundle.resolve().as_uri(),
                                "bundleDigest": elastic_digest,
                                "manifestJSON": '{"bundle_format":"claude_skill_v1","instruction_file":"SKILL.md","name":"Elastic Knowledge","description":"Elastic logs guidance","compatibility":""}',
                                "capability": "evidence.plan",
                                "role": "knowledge",
                                "priority": 150,
                                "enabled": True,
                            },
                            {
                                "skillID": "prometheus.evidence.plan",
                                "version": "1.0.0",
                                "artifactURL": metrics_bundle.resolve().as_uri(),
                                "bundleDigest": metrics_digest,
                                "manifestJSON": '{"bundle_format":"claude_skill_v1","instruction_file":"SKILL.md","name":"Prometheus Knowledge","description":"Prometheus metrics guidance","compatibility":""}',
                                "capability": "evidence.plan",
                                "role": "knowledge",
                                "priority": 140,
                                "enabled": True,
                            },
                            {
                                "skillID": "claude.evidence.prompt_planner",
                                "version": "1.0.0",
                                "artifactURL": executor_bundle.resolve().as_uri(),
                                "bundleDigest": executor_digest,
                                "manifestJSON": '{"bundle_format":"claude_skill_v1","instruction_file":"SKILL.md","name":"Prompt Planner","description":"Planner executor","compatibility":""}',
                                "capability": "evidence.plan",
                                "role": "executor",
                                "priority": 100,
                                "enabled": True,
                            },
                        ],
                    }
                ],
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

                def select_knowledge_skills(self, **_: object):
                    class _Selection:
                        selected_binding_keys = [
                            "elasticsearch.evidence.plan\x001.0.0\x00evidence.plan\x00knowledge",
                            "prometheus.evidence.plan\x001.0.0\x00evidence.plan\x00knowledge",
                        ]
                        reason = "need logs and metrics context"

                    return _Selection()

                def select_skill(self, **_: object) -> SkillSelectionResult:
                    return SkillSelectionResult(
                        selected_binding_key="claude.evidence.prompt_planner\x001.0.0\x00evidence.plan\x00executor",
                        reason="single planner",
                    )

                def consume_skill(self, **kwargs: object) -> PromptSkillConsumeResult:
                    knowledge_context = kwargs.get("knowledge_context")
                    assert isinstance(knowledge_context, list)
                    assert len(knowledge_context) == 2
                    skill_ids = [str(item.get("skill_id")) for item in knowledge_context if isinstance(item, dict)]
                    assert skill_ids == ["elasticsearch.evidence.plan", "prometheus.evidence.plan"]
                    return PromptSkillConsumeResult(
                        payload={
                            "evidence_plan_patch": {
                                "metadata": {
                                    "prompt_skill": "claude.evidence.prompt_planner",
                                    "knowledge_skills": skill_ids,
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
                skill_agent=_FakeAgent(),
            )

            state = GraphState(
                job_id="job-1",
                incident_id="inc-1",
                evidence_plan={"budget": {"max_calls": 1}},
                evidence_candidates=[],
                metrics_branch_meta={"mode": "query"},
                logs_branch_meta={
                    "mode": "query",
                    "query_type": "logs",
                    "request_payload": {
                        "datasource_id": "ds-logs",
                        "query": "message:error",
                        "start_ts": 100,
                        "end_ts": 200,
                        "limit": 200,
                    },
                    "query_request": {"datasourceID": "ds-logs", "queryText": "message:error"},
                },
                evidence_mode="query",
                incident_context={"service": "svc-a", "namespace": "default"},
                input_hints={},
            )

            result = runtime.consume_prompt_skill(capability="evidence.plan", graph_state=state)

            self.assertIsInstance(result, dict)
            self.assertEqual(result["skill_id"], "claude.evidence.prompt_planner")
            self.assertEqual(
                result["knowledge_skill_ids"],
                ["elasticsearch.evidence.plan", "prometheus.evidence.plan"],
            )
            self.assertEqual(
                state.evidence_plan["metadata"]["knowledge_skills"],
                ["elasticsearch.evidence.plan", "prometheus.evidence.plan"],
            )

    def test_prompt_first_runtime_loads_selected_knowledge_and_executor_resources(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            base = Path(tmp_dir)
            elastic_dir = base / "elastic"
            metrics_dir = base / "metrics"
            executor_dir = base / "executor"
            elastic_dir.mkdir(parents=True)
            metrics_dir.mkdir(parents=True)
            executor_dir.mkdir(parents=True)
            _write_skill_dir(
                elastic_dir,
                name="Elastic Knowledge",
                description="Elastic logs guidance",
                resources=[
                    ("references/ecs-fields.md", "# ECS Fields\n\nPrefer service.name and namespace fields.\n"),
                    ("examples/log-querytext-examples.md", "# Logs Query Examples\n\nservice.name:\"checkout\"\n"),
                ],
            )
            _write_skill_dir(
                metrics_dir,
                name="Prometheus Knowledge",
                description="Prometheus metrics guidance",
                resources=[
                    ("references/metric-families.md", "# Metric Families\n\nUse request rate and error rate.\n"),
                    ("examples/promql-scope-examples.md", "# PromQL Scope Examples\n\nsum(rate(http_requests_total[5m]))\n"),
                ],
            )
            _write_skill_dir(
                executor_dir,
                name="Prompt Planner",
                description="Planner executor",
                resources=[
                    ("templates/evidence-plan-output-rules.md", "# Output Rules\n\nKeep payload conservative.\n"),
                ],
            )
            elastic_bundle = base / "elastic.zip"
            metrics_bundle = base / "metrics.zip"
            executor_bundle = base / "executor.zip"
            elastic_digest = _zip_dir(elastic_dir, elastic_bundle)
            metrics_digest = _zip_dir(metrics_dir, metrics_bundle)
            executor_digest = _zip_dir(executor_dir, executor_bundle)
            skill_catalog = SkillCatalog.from_resolved_skillsets(
                skillsets_payload=[
                    {
                        "skillsetID": "skillset.default",
                        "skills": [
                            {
                                "skillID": "elasticsearch.evidence.plan",
                                "version": "1.0.0",
                                "artifactURL": elastic_bundle.resolve().as_uri(),
                                "bundleDigest": elastic_digest,
                                "manifestJSON": '{"bundle_format":"claude_skill_v1","instruction_file":"SKILL.md","name":"Elastic Knowledge","description":"Elastic logs guidance","compatibility":""}',
                                "capability": "evidence.plan",
                                "role": "knowledge",
                                "priority": 150,
                                "enabled": True,
                            },
                            {
                                "skillID": "prometheus.evidence.plan",
                                "version": "1.0.0",
                                "artifactURL": metrics_bundle.resolve().as_uri(),
                                "bundleDigest": metrics_digest,
                                "manifestJSON": '{"bundle_format":"claude_skill_v1","instruction_file":"SKILL.md","name":"Prometheus Knowledge","description":"Prometheus metrics guidance","compatibility":""}',
                                "capability": "evidence.plan",
                                "role": "knowledge",
                                "priority": 140,
                                "enabled": True,
                            },
                            {
                                "skillID": "claude.evidence.prompt_planner",
                                "version": "1.0.0",
                                "artifactURL": executor_bundle.resolve().as_uri(),
                                "bundleDigest": executor_digest,
                                "manifestJSON": '{"bundle_format":"claude_skill_v1","instruction_file":"SKILL.md","name":"Prompt Planner","description":"Planner executor","compatibility":""}',
                                "capability": "evidence.plan",
                                "role": "executor",
                                "priority": 100,
                                "enabled": True,
                            },
                        ],
                    }
                ],
                cache_dir=str(base / "cache"),
            )

            class _FakeSession:
                def __init__(self) -> None:
                    self.headers: dict[str, str] = {}

            class _FakeClient:
                def __init__(self) -> None:
                    self.session = _FakeSession()
                    self.instance_id = ""

            class _ResourceAwareAgent:
                configured = True

                def select_knowledge_skills(self, **_: object):
                    class _Selection:
                        selected_binding_keys = [
                            "elasticsearch.evidence.plan\x001.0.0\x00evidence.plan\x00knowledge",
                            "prometheus.evidence.plan\x001.0.0\x00evidence.plan\x00knowledge",
                        ]
                        reason = "use both knowledge skills"

                    return _Selection()

                def select_skill_resources(self, **kwargs: object):
                    skill_id = str(kwargs.get("skill_id") or "")

                    class _Selection:
                        reason = "load only needed resources"
                        selected_resource_ids: list[str]

                    selection = _Selection()
                    if skill_id == "elasticsearch.evidence.plan":
                        selection.selected_resource_ids = ["references/ecs-fields.md"]
                    elif skill_id == "prometheus.evidence.plan":
                        selection.selected_resource_ids = ["examples/promql-scope-examples.md"]
                    else:
                        selection.selected_resource_ids = ["templates/evidence-plan-output-rules.md"]
                    return selection

                def select_skill(self, **_: object) -> SkillSelectionResult:
                    return SkillSelectionResult(
                        selected_binding_key="claude.evidence.prompt_planner\x001.0.0\x00evidence.plan\x00executor",
                        reason="single planner",
                    )

                def consume_skill(self, **kwargs: object) -> PromptSkillConsumeResult:
                    knowledge_context = kwargs.get("knowledge_context")
                    skill_resources = kwargs.get("skill_resources")
                    assert isinstance(knowledge_context, list)
                    assert isinstance(skill_resources, list)
                    assert len(knowledge_context) == 2
                    assert len(skill_resources) == 1
                    resource_index = {
                        str(item.get("skill_id")): item.get("resources")
                        for item in knowledge_context
                        if isinstance(item, dict)
                    }
                    assert isinstance(resource_index.get("elasticsearch.evidence.plan"), list)
                    assert isinstance(resource_index.get("prometheus.evidence.plan"), list)
                    assert resource_index["elasticsearch.evidence.plan"][0]["resource_id"] == "references/ecs-fields.md"
                    assert resource_index["prometheus.evidence.plan"][0]["resource_id"] == "examples/promql-scope-examples.md"
                    assert skill_resources[0]["resource_id"] == "templates/evidence-plan-output-rules.md"
                    return PromptSkillConsumeResult(
                        payload={
                            "evidence_plan_patch": {
                                "metadata": {
                                    "prompt_skill": "claude.evidence.prompt_planner",
                                    "resource_mode": "selected",
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
                skill_agent=_ResourceAwareAgent(),
            )

            state = GraphState(
                job_id="job-1",
                incident_id="inc-1",
                evidence_plan={"budget": {"max_calls": 1}},
                evidence_mode="query",
                incident_context={"service": "svc-a", "namespace": "default"},
                input_hints={},
                logs_branch_meta={
                    "mode": "query",
                    "query_type": "logs",
                    "request_payload": {
                        "datasource_id": "ds-logs",
                        "query": "message:error",
                        "start_ts": 100,
                        "end_ts": 200,
                        "limit": 200,
                    },
                    "query_request": {"datasourceID": "ds-logs", "queryText": "message:error"},
                },
            )

            result = runtime.consume_prompt_skill(capability="evidence.plan", graph_state=state)
            self.assertIsInstance(result, dict)
            self.assertEqual(state.evidence_plan["metadata"]["resource_mode"], "selected")

    def test_prompt_first_runtime_filters_invalid_and_overflow_skill_resources(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            base = Path(tmp_dir)
            executor_dir = base / "executor"
            executor_dir.mkdir(parents=True)
            _write_skill_dir(
                executor_dir,
                name="Prompt Planner",
                description="Planner executor",
                resources=[
                    ("templates/one.md", "# One\n\nfirst\n"),
                    ("templates/two.md", "# Two\n\nsecond\n"),
                    ("templates/three.md", "# Three\n\nthird\n"),
                    ("templates/four.md", "# Four\n\nfourth\n"),
                ],
            )
            executor_bundle = base / "executor.zip"
            executor_digest = _zip_dir(executor_dir, executor_bundle)
            skill_catalog = SkillCatalog.from_resolved_skillsets(
                skillsets_payload=[
                    {
                        "skillsetID": "skillset.default",
                        "skills": [
                            {
                                "skillID": "claude.evidence.prompt_planner",
                                "version": "1.0.0",
                                "artifactURL": executor_bundle.resolve().as_uri(),
                                "bundleDigest": executor_digest,
                                "manifestJSON": '{"bundle_format":"claude_skill_v1","instruction_file":"SKILL.md","name":"Prompt Planner","description":"Planner executor","compatibility":""}',
                                "capability": "evidence.plan",
                                "role": "executor",
                                "priority": 100,
                                "enabled": True,
                            }
                        ],
                    }
                ],
                cache_dir=str(base / "cache"),
            )

            class _FakeSession:
                def __init__(self) -> None:
                    self.headers: dict[str, str] = {}

            class _FakeClient:
                def __init__(self) -> None:
                    self.session = _FakeSession()
                    self.instance_id = ""

            class _ResourceFilterAgent:
                configured = True

                def select_skill(self, **_: object) -> SkillSelectionResult:
                    return SkillSelectionResult(
                        selected_binding_key="claude.evidence.prompt_planner\x001.0.0\x00evidence.plan\x00executor",
                        reason="single executor",
                    )

                def select_skill_resources(self, **_: object):
                    class _Selection:
                        reason = "test filtering"
                        selected_resource_ids = [
                            "templates/one.md",
                            "missing.md",
                            "templates/two.md",
                            "templates/three.md",
                            "templates/four.md",
                        ]

                    return _Selection()

                def consume_skill(self, **kwargs: object) -> PromptSkillConsumeResult:
                    skill_resources = kwargs.get("skill_resources")
                    assert isinstance(skill_resources, list)
                    assert [item.get("resource_id") for item in skill_resources] == [
                        "templates/one.md",
                        "templates/two.md",
                        "templates/three.md",
                    ]
                    return PromptSkillConsumeResult(
                        payload={"evidence_plan_patch": {"metadata": {"resource_filtering": "ok"}}}
                    )

            runtime = OrchestratorRuntime(
                client=_FakeClient(),
                job_id="job-1",
                instance_id="orc-test",
                heartbeat_interval_seconds=10,
                skill_catalog=skill_catalog,
                skills_execution_mode="prompt_first",
                skill_agent=_ResourceFilterAgent(),
            )

            state = GraphState(job_id="job-1", incident_id="inc-1", evidence_plan={"budget": {"max_calls": 1}}, evidence_mode="query")
            result = runtime.consume_prompt_skill(capability="evidence.plan", graph_state=state)
            self.assertIsInstance(result, dict)
            self.assertEqual(state.evidence_plan["metadata"]["resource_filtering"], "ok")

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

    def test_prompt_first_evidence_plan_dual_tool_warms_logs_and_metrics_state(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            base = Path(tmp_dir)
            elastic_dir = base / "elastic"
            prom_dir = base / "prom"
            executor_dir = base / "executor"
            elastic_dir.mkdir(parents=True)
            prom_dir.mkdir(parents=True)
            executor_dir.mkdir(parents=True)
            _write_skill_dir(elastic_dir, name="Elastic Knowledge", description="Elastic guidance")
            _write_skill_dir(prom_dir, name="Prometheus Knowledge", description="Prometheus guidance")
            _write_skill_dir(executor_dir, name="Evidence Planner", description="Executor planner")
            elastic_bundle = base / "elastic.zip"
            prom_bundle = base / "prom.zip"
            executor_bundle = base / "executor.zip"
            elastic_digest = _zip_dir(elastic_dir, elastic_bundle)
            prom_digest = _zip_dir(prom_dir, prom_bundle)
            executor_digest = _zip_dir(executor_dir, executor_bundle)

            skill_catalog = SkillCatalog.from_resolved_skillsets(
                skillsets_payload=[
                    {
                        "skillsetID": "skillset.default",
                        "skills": [
                            {
                                "skillID": "elasticsearch.evidence.plan",
                                "version": "1.0.0",
                                "artifactURL": elastic_bundle.resolve().as_uri(),
                                "bundleDigest": elastic_digest,
                                "manifestJSON": '{"bundle_format":"claude_skill_v1","instruction_file":"SKILL.md","name":"Elastic Knowledge","description":"Elastic guidance","compatibility":""}',
                                "capability": "evidence.plan",
                                "role": "knowledge",
                                "allowedTools": [],
                                "priority": 150,
                                "enabled": True,
                            },
                            {
                                "skillID": "prometheus.evidence.plan",
                                "version": "1.0.0",
                                "artifactURL": prom_bundle.resolve().as_uri(),
                                "bundleDigest": prom_digest,
                                "manifestJSON": '{"bundle_format":"claude_skill_v1","instruction_file":"SKILL.md","name":"Prometheus Knowledge","description":"Prometheus guidance","compatibility":""}',
                                "capability": "evidence.plan",
                                "role": "knowledge",
                                "allowedTools": [],
                                "priority": 140,
                                "enabled": True,
                            },
                            {
                                "skillID": "claude.evidence.prompt_planner",
                                "version": "1.0.0",
                                "artifactURL": executor_bundle.resolve().as_uri(),
                                "bundleDigest": executor_digest,
                                "manifestJSON": '{"bundle_format":"claude_skill_v1","instruction_file":"SKILL.md","name":"Evidence Planner","description":"Executor planner","compatibility":""}',
                                "capability": "evidence.plan",
                                "role": "executor",
                                "allowedTools": ["query_logs", "query_metrics"],
                                "priority": 100,
                                "enabled": True,
                            },
                        ],
                    }
                ],
                cache_dir=str(base / "cache"),
            )

            class _FakeSession:
                def __init__(self) -> None:
                    self.headers: dict[str, str] = {}

            class _FakeClient:
                def __init__(self) -> None:
                    self.session = _FakeSession()
                    self.instance_id = ""

            class _DualToolAgent:
                configured = True

                def select_knowledge_skills(self, **_: object):
                    class _Selection:
                        selected_binding_keys = [
                            "elasticsearch.evidence.plan\x001.0.0\x00evidence.plan\x00knowledge",
                            "prometheus.evidence.plan\x001.0.0\x00evidence.plan\x00knowledge",
                        ]
                        reason = "use both domain guides"

                    return _Selection()

                def select_skill(self, **_: object) -> SkillSelectionResult:
                    return SkillSelectionResult(
                        selected_binding_key="claude.evidence.prompt_planner\x001.0.0\x00evidence.plan\x00executor",
                        reason="single executor",
                    )

                def plan_tool_calls(self, **kwargs: object):
                    knowledge_context = kwargs.get("knowledge_context")
                    assert isinstance(knowledge_context, list)
                    assert len(knowledge_context) == 2

                    class _Sequence:
                        reason = "check metrics first then correlate logs"
                        tool_calls = [
                            {
                                "tool": "mcp.query_metrics",
                                "input": {
                                    "datasource_id": "ds-metrics",
                                    "promql": 'sum(rate(http_requests_total{service="svc-a",status=~"5.."}[5m]))',
                                    "start_ts": 100,
                                    "end_ts": 200,
                                    "step_seconds": 60,
                                },
                                "reason": "confirm 5xx spike",
                            },
                            {
                                "tool": "mcp.query_logs",
                                "input": {
                                    "datasource_id": "ds-logs",
                                    "query": 'service.name:"svc-a" AND (log.level:(error OR fatal) OR error.type:* OR error.message:* OR message:(*exception* OR *timeout* OR *panic*))',
                                    "start_ts": 100,
                                    "end_ts": 200,
                                    "limit": 200,
                                },
                                "reason": "correlate error logs",
                            },
                        ]

                    return _Sequence()

                def consume_after_tools(self, **kwargs: object) -> PromptSkillConsumeResult:
                    tool_results = kwargs.get("tool_results")
                    assert isinstance(tool_results, list)
                    assert [item.get("tool") for item in tool_results if isinstance(item, dict)] == [
                        "mcp.query_metrics",
                        "mcp.query_logs",
                    ]
                    return PromptSkillConsumeResult(
                        payload={
                            "evidence_plan_patch": {
                                "metadata": {
                                    "prompt_skill": "claude.evidence.prompt_planner",
                                    "knowledge_skills": ["elasticsearch.evidence.plan", "prometheus.evidence.plan"],
                                }
                            },
                            "metrics_branch_meta": {
                                "mode": "query",
                                "query_type": "metrics",
                                "request_payload": {
                                    "promql": 'sum(rate(http_requests_total{service="svc-a",status=~"5.."}[5m]))',
                                    "step_seconds": 60,
                                },
                                "query_request": {
                                    "queryText": 'sum(rate(http_requests_total{service="svc-a",status=~"5.."}[5m]))',
                                },
                            },
                            "logs_branch_meta": {
                                "mode": "query",
                                "query_type": "logs",
                                "request_payload": {
                                    "query": 'service.name:"svc-a" AND (log.level:(error OR fatal) OR error.type:* OR error.message:* OR message:(*exception* OR *timeout* OR *panic*))'
                                },
                                "query_request": {
                                    "queryText": 'service.name:"svc-a" AND (log.level:(error OR fatal) OR error.type:* OR error.message:* OR message:(*exception* OR *timeout* OR *panic*))'
                                },
                            },
                        }
                    )

            runtime = OrchestratorRuntime(
                client=_FakeClient(),
                job_id="job-1",
                instance_id="orc-test",
                heartbeat_interval_seconds=10,
                skill_catalog=skill_catalog,
                skills_execution_mode="prompt_first",
                skills_tool_calling_mode="evidence_plan_dual_tool",
                skill_agent=_DualToolAgent(),
            )

            toolcalls: list[dict[str, object]] = []
            runtime.report_tool_call = lambda **kwargs: toolcalls.append(kwargs) or 1  # type: ignore[method-assign]
            runtime.query_metrics = lambda **kwargs: {  # type: ignore[method-assign]
                "queryResultJSON": '{"data":{"result":[{"metric":{"service":"svc-a"},"values":[[1710000000,"0.12"]]}]}}',
                "rowCount": 1,
                "resultSizeBytes": 96,
                "isTruncated": False,
            }
            runtime.query_logs = lambda **kwargs: {  # type: ignore[method-assign]
                "queryResultJSON": '{"data":{"result":[{"stream":{"service.name":"svc-a"},"values":[[1710000000,"boom"]]}]}}',
                "rowCount": 1,
                "resultSizeBytes": 88,
                "isTruncated": False,
            }

            state = GraphState(
                job_id="job-1",
                incident_id="inc-1",
                evidence_plan={"budget": {"max_calls": 2}, "candidates": [{"type": "metrics"}, {"type": "logs"}]},
                evidence_candidates=[{"type": "metrics"}, {"type": "logs"}],
                metrics_branch_meta={
                    "mode": "query",
                    "query_type": "metrics",
                    "request_payload": {
                        "datasource_id": "ds-metrics",
                        "promql": "sum(up)",
                        "start_ts": 100,
                        "end_ts": 200,
                        "step_seconds": 30,
                    },
                    "query_request": {
                        "datasourceID": "ds-metrics",
                        "queryText": "sum(up)",
                        "queryJSON": "{}",
                    },
                },
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
            self.assertEqual(result["skill_id"], "claude.evidence.prompt_planner")
            self.assertEqual(
                result["knowledge_skill_ids"],
                ["elasticsearch.evidence.plan", "prometheus.evidence.plan"],
            )
            self.assertEqual(state.metrics_query_status, "ok")
            self.assertEqual(state.logs_query_status, "ok")
            self.assertTrue(state.metrics_branch_meta["tool_result_reusable"])
            self.assertTrue(state.logs_branch_meta["tool_result_reusable"])
            self.assertEqual(state.metrics_branch_meta["tool_result_source"], "skill_prompt_first")
            self.assertEqual(state.logs_branch_meta["tool_result_source"], "skill_prompt_first")
            self.assertEqual(state.metrics_query_request["promql"], 'sum(rate(http_requests_total{service="svc-a",status=~"5.."}[5m]))')
            self.assertIn('service.name:"svc-a"', state.logs_query_request["query"])
            self.assertEqual([item["tool_name"] for item in toolcalls], ["mcp.query_metrics", "mcp.query_logs"])
            self.assertEqual(state.evidence_plan["metadata"]["prompt_skill"], "claude.evidence.prompt_planner")

    def test_prompt_first_evidence_plan_metrics_branch_meta_preserves_guardrails(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            base = Path(tmp_dir)
            skill_dir = base / "executor"
            skill_dir.mkdir(parents=True)
            _write_skill_dir(skill_dir, name="Evidence Planner", description="Executor planner")
            bundle_path = base / "executor.zip"
            bundle_digest = _zip_dir(skill_dir, bundle_path)
            skill_catalog = SkillCatalog.from_resolved_skillsets(
                skillsets_payload=_resolved_skillsets_payload(
                    skillset_id="skillset.default",
                    skill_id="claude.evidence.prompt_planner",
                    version="1.0.0",
                    name="Evidence Planner",
                    description="Executor planner",
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
                        selected_binding_key="claude.evidence.prompt_planner\x001.0.0\x00evidence.plan",
                        reason="best match",
                    )

                def consume_skill(self, **_: object) -> PromptSkillConsumeResult:
                    return PromptSkillConsumeResult(
                        payload={
                            "metrics_branch_meta": {
                                "mode": "query",
                                "query_type": "metrics",
                                "request_payload": {
                                    "promql": 'sum(rate(http_requests_total{service="svc-a"}[5m]))',
                                    "step_seconds": 60,
                                    "datasource_id": "override-me",
                                    "start_ts": 1,
                                    "end_ts": 2,
                                },
                                "query_request": {
                                    "queryText": 'sum(rate(http_requests_total{service="svc-a"}[5m]))',
                                    "queryJSON": "{}",
                                },
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
                skills_tool_calling_mode="disabled",
                skill_agent=_PromptOnlyAgent(),
            )

            state = GraphState(
                job_id="job-1",
                incident_id="inc-1",
                metrics_branch_meta={
                    "mode": "query",
                    "query_type": "metrics",
                    "request_payload": {
                        "datasource_id": "ds-metrics",
                        "promql": "sum(up)",
                        "start_ts": 100,
                        "end_ts": 200,
                        "step_seconds": 30,
                    },
                    "query_request": {
                        "datasourceID": "ds-metrics",
                        "queryText": "sum(up)",
                        "queryJSON": "{}",
                    },
                },
            )

            result = runtime.consume_prompt_skill(capability="evidence.plan", graph_state=state)
            self.assertIsInstance(result, dict)
            self.assertEqual(state.metrics_branch_meta["request_payload"]["datasource_id"], "ds-metrics")
            self.assertEqual(state.metrics_branch_meta["request_payload"]["start_ts"], 100)
            self.assertEqual(state.metrics_branch_meta["request_payload"]["end_ts"], 200)
            self.assertEqual(state.metrics_branch_meta["request_payload"]["step_seconds"], 60)
            self.assertEqual(
                state.metrics_branch_meta["request_payload"]["promql"],
                'sum(rate(http_requests_total{service="svc-a"}[5m]))',
            )

    def test_prompt_first_evidence_plan_dual_tool_rejects_duplicate_tool_sequence(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            base = Path(tmp_dir)
            skill_dir = base / "executor"
            skill_dir.mkdir(parents=True)
            _write_skill_dir(skill_dir, name="Evidence Planner", description="Executor planner")
            bundle_path = base / "executor.zip"
            bundle_digest = _zip_dir(skill_dir, bundle_path)
            skill_catalog = SkillCatalog.from_resolved_skillsets(
                skillsets_payload=_resolved_skillsets_payload(
                    skillset_id="skillset.default",
                    skill_id="claude.evidence.prompt_planner",
                    version="1.0.0",
                    name="Evidence Planner",
                    description="Executor planner",
                    compatibility="",
                    bundle_path=bundle_path,
                    bundle_digest=bundle_digest,
                    capability="evidence.plan",
                    allowed_tools=["query_logs", "query_metrics"],
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
                        selected_binding_key="claude.evidence.prompt_planner\x001.0.0\x00evidence.plan",
                        reason="best match",
                    )

                def plan_tool_calls(self, **_: object):
                    class _Sequence:
                        tool_calls = [
                            {
                                "tool": "mcp.query_metrics",
                                "input": {
                                    "datasource_id": "ds-metrics",
                                    "promql": "sum(up)",
                                    "start_ts": 100,
                                    "end_ts": 200,
                                    "step_seconds": 30,
                                },
                            },
                            {
                                "tool": "mcp.query_metrics",
                                "input": {
                                    "datasource_id": "ds-metrics",
                                    "promql": "sum(rate(up[5m]))",
                                    "start_ts": 100,
                                    "end_ts": 200,
                                    "step_seconds": 30,
                                },
                            },
                        ]

                    return _Sequence()

            runtime = OrchestratorRuntime(
                client=_FakeClient(),
                job_id="job-1",
                instance_id="orc-test",
                heartbeat_interval_seconds=10,
                skill_catalog=skill_catalog,
                skills_execution_mode="prompt_first",
                skills_tool_calling_mode="evidence_plan_dual_tool",
                skill_agent=_BrokenToolAgent(),
            )
            runtime.query_metrics = lambda **kwargs: self.fail("query_metrics must not run for duplicate tool sequence")  # type: ignore[method-assign]
            state = GraphState(job_id="job-1", incident_id="inc-1", evidence_mode="query")
            result = runtime.consume_prompt_skill(capability="evidence.plan", graph_state=state)
            self.assertIsNone(result)
            self.assertIsNone(state.metrics_query_status)

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

    def test_script_executor_evidence_plan_dual_tool_warms_logs_and_metrics_state(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            base = Path(tmp_dir)
            elastic_dir = base / "elastic"
            prom_dir = base / "prom"
            executor_dir = base / "executor"
            elastic_dir.mkdir(parents=True)
            prom_dir.mkdir(parents=True)
            executor_dir.mkdir(parents=True)
            _write_skill_dir(
                elastic_dir,
                name="Elastic Knowledge",
                description="Elastic guidance",
                resources=[("references/ecs-fields.md", "# ECS Fields\n\nUse service.name.\n")],
            )
            _write_skill_dir(
                prom_dir,
                name="Prometheus Knowledge",
                description="Prometheus guidance",
                resources=[("references/metric-families.md", "# Metric Families\n\nUse error rate.\n")],
            )
            _write_skill_dir(
                executor_dir,
                name="Evidence Script Planner",
                description="Script executor planner",
                resources=[("templates/evidence-script-output-rules.md", "# Output Rules\n\nKeep payload conservative.\n")],
                script_body=(
                    "def _resource_ids(ctx):\n"
                    "    out = []\n"
                    "    for item in ctx.get('skill_resources', []):\n"
                    "        if isinstance(item, dict) and item.get('resource_id'):\n"
                    "            out.append(item['resource_id'])\n"
                    "    return out\n"
                    "def _knowledge_skill_ids(ctx):\n"
                    "    out = []\n"
                    "    for item in ctx.get('knowledge_context', []):\n"
                    "        if isinstance(item, dict) and item.get('skill_id'):\n"
                    "            out.append(item['skill_id'])\n"
                    "    return out\n"
                    "def run(input_payload, ctx):\n"
                    "    if ctx.get('phase') == 'plan_tools':\n"
                    "        return {\n"
                    "            'tool_calls': [\n"
                    "                {\n"
                    "                    'tool': 'mcp.query_metrics',\n"
                    "                    'input': {\n"
                    "                        'datasource_id': 'ds-metrics',\n"
                    "                        'promql': 'sum(rate(http_requests_total{service=\"svc-a\",status=~\"5..\"}[5m]))',\n"
                    "                        'start_ts': 100,\n"
                    "                        'end_ts': 200,\n"
                    "                        'step_seconds': 60,\n"
                    "                    },\n"
                    "                },\n"
                    "                {\n"
                    "                    'tool': 'mcp.query_logs',\n"
                    "                    'input': {\n"
                    "                        'datasource_id': 'ds-logs',\n"
                    "                        'query': 'service.name:\"svc-a\" AND log.level:(error OR fatal)',\n"
                    "                        'start_ts': 100,\n"
                    "                        'end_ts': 200,\n"
                    "                        'limit': 200,\n"
                    "                    },\n"
                    "                },\n"
                    "            ],\n"
                    "            'observations': [{'kind': 'note', 'message': 'planning tools'}],\n"
                    "        }\n"
                    "    tool_results = ctx.get('tool_results', [])\n"
                    "    return {\n"
                    "        'payload': {\n"
                    "            'evidence_plan_patch': {\n"
                    "                'metadata': {\n"
                    "                    'prompt_skill': 'claude.evidence.script_planner',\n"
                    "                    'knowledge_skills': _knowledge_skill_ids(ctx),\n"
                    "                    'resource_ids': _resource_ids(ctx),\n"
                    "                    'tool_result_count': len(tool_results),\n"
                    "                }\n"
                    "            },\n"
                    "            'metrics_branch_meta': {\n"
                    "                'mode': 'query',\n"
                    "                'query_type': 'metrics',\n"
                    "                'request_payload': {\n"
                    "                    'promql': 'sum(rate(http_requests_total{service=\"svc-a\",status=~\"5..\"}[5m]))',\n"
                    "                    'step_seconds': 60,\n"
                    "                },\n"
                    "                'query_request': {\n"
                    "                    'queryText': 'sum(rate(http_requests_total{service=\"svc-a\",status=~\"5..\"}[5m]))',\n"
                    "                },\n"
                    "            },\n"
                    "            'logs_branch_meta': {\n"
                    "                'mode': 'query',\n"
                    "                'query_type': 'logs',\n"
                    "                'request_payload': {\n"
                    "                    'query': 'service.name:\"svc-a\" AND log.level:(error OR fatal)',\n"
                    "                },\n"
                    "                'query_request': {\n"
                    "                    'queryText': 'service.name:\"svc-a\" AND log.level:(error OR fatal)',\n"
                    "                },\n"
                    "            },\n"
                    "        },\n"
                    "        'observations': [{'kind': 'note', 'message': 'after tools'}],\n"
                    "    }\n"
                ),
            )
            elastic_bundle = base / "elastic.zip"
            prom_bundle = base / "prom.zip"
            executor_bundle = base / "executor.zip"
            elastic_digest = _zip_dir(elastic_dir, elastic_bundle)
            prom_digest = _zip_dir(prom_dir, prom_bundle)
            executor_digest = _zip_dir(executor_dir, executor_bundle)

            skill_catalog = SkillCatalog.from_resolved_skillsets(
                skillsets_payload=[
                    {
                        "skillsetID": "skillset.default",
                        "skills": [
                            {
                                "skillID": "elasticsearch.evidence.plan",
                                "version": "1.0.0",
                                "artifactURL": elastic_bundle.resolve().as_uri(),
                                "bundleDigest": elastic_digest,
                                "manifestJSON": '{"bundle_format":"claude_skill_v1","instruction_file":"SKILL.md","name":"Elastic Knowledge","description":"Elastic guidance","compatibility":""}',
                                "capability": "evidence.plan",
                                "role": "knowledge",
                                "allowedTools": [],
                                "priority": 150,
                                "enabled": True,
                            },
                            {
                                "skillID": "prometheus.evidence.plan",
                                "version": "1.0.0",
                                "artifactURL": prom_bundle.resolve().as_uri(),
                                "bundleDigest": prom_digest,
                                "manifestJSON": '{"bundle_format":"claude_skill_v1","instruction_file":"SKILL.md","name":"Prometheus Knowledge","description":"Prometheus guidance","compatibility":""}',
                                "capability": "evidence.plan",
                                "role": "knowledge",
                                "allowedTools": [],
                                "priority": 140,
                                "enabled": True,
                            },
                            {
                                "skillID": "claude.evidence.script_planner",
                                "version": "1.0.0",
                                "artifactURL": executor_bundle.resolve().as_uri(),
                                "bundleDigest": executor_digest,
                                "manifestJSON": '{"bundle_format":"claude_skill_v1","instruction_file":"SKILL.md","name":"Evidence Script Planner","description":"Script executor planner","compatibility":""}',
                                "capability": "evidence.plan",
                                "role": "executor",
                                "executorMode": "script",
                                "allowedTools": ["query_logs", "query_metrics"],
                                "priority": 100,
                                "enabled": True,
                            },
                        ],
                    }
                ],
                cache_dir=str(base / "cache"),
            )

            class _FakeSession:
                def __init__(self) -> None:
                    self.headers: dict[str, str] = {}

            class _FakeClient:
                def __init__(self) -> None:
                    self.session = _FakeSession()
                    self.instance_id = ""

            class _SelectingAgent:
                configured = True

                def select_knowledge_skills(self, **_: object):
                    class _Selection:
                        selected_binding_keys = [
                            "elasticsearch.evidence.plan\x001.0.0\x00evidence.plan\x00knowledge",
                            "prometheus.evidence.plan\x001.0.0\x00evidence.plan\x00knowledge",
                        ]
                        reason = "need logs and metrics knowledge"

                    return _Selection()

                def select_skill(self, **_: object) -> SkillSelectionResult:
                    return SkillSelectionResult(
                        selected_binding_key="claude.evidence.script_planner\x001.0.0\x00evidence.plan\x00executor",
                        reason="use script planner",
                    )

                def select_skill_resources(self, **kwargs: object):
                    skill_id = str(kwargs.get("skill_id") or "")

                    class _Selection:
                        reason = "load selected resource"
                        selected_resource_ids: list[str]

                    selection = _Selection()
                    if skill_id == "elasticsearch.evidence.plan":
                        selection.selected_resource_ids = ["references/ecs-fields.md"]
                    elif skill_id == "prometheus.evidence.plan":
                        selection.selected_resource_ids = ["references/metric-families.md"]
                    else:
                        selection.selected_resource_ids = ["templates/evidence-script-output-rules.md"]
                    return selection

            runtime = OrchestratorRuntime(
                client=_FakeClient(),
                job_id="job-1",
                instance_id="orc-test",
                heartbeat_interval_seconds=10,
                skill_catalog=skill_catalog,
                skills_execution_mode="prompt_first",
                skills_tool_calling_mode="evidence_plan_dual_tool",
                skill_agent=_SelectingAgent(),
            )

            toolcalls: list[dict[str, object]] = []
            runtime.report_tool_call = lambda **kwargs: toolcalls.append(kwargs) or 1  # type: ignore[method-assign]
            runtime.query_metrics = lambda **kwargs: {  # type: ignore[method-assign]
                "queryResultJSON": '{"data":{"result":[{"metric":{"service":"svc-a"},"values":[[1710000000,"0.12"]]}]}}',
                "rowCount": 1,
                "resultSizeBytes": 96,
                "isTruncated": False,
            }
            runtime.query_logs = lambda **kwargs: {  # type: ignore[method-assign]
                "queryResultJSON": '{"data":{"result":[{"stream":{"service.name":"svc-a"},"values":[[1710000000,"boom"]]}]}}',
                "rowCount": 1,
                "resultSizeBytes": 88,
                "isTruncated": False,
            }

            state = GraphState(
                job_id="job-1",
                incident_id="inc-1",
                evidence_plan={"budget": {"max_calls": 2}, "candidates": [{"type": "metrics"}, {"type": "logs"}]},
                evidence_candidates=[{"type": "metrics"}, {"type": "logs"}],
                metrics_branch_meta={
                    "mode": "query",
                    "query_type": "metrics",
                    "request_payload": {
                        "datasource_id": "ds-metrics",
                        "promql": "sum(up)",
                        "start_ts": 100,
                        "end_ts": 200,
                        "step_seconds": 30,
                    },
                    "query_request": {"datasourceID": "ds-metrics", "queryText": "sum(up)", "queryJSON": "{}"},
                },
                logs_branch_meta={
                    "mode": "query",
                    "query_type": "logs",
                    "request_payload": {
                        "datasource_id": "ds-logs",
                        "query": "message:error",
                        "start_ts": 100,
                        "end_ts": 200,
                        "limit": 200,
                    },
                    "query_request": {"datasourceID": "ds-logs", "queryText": "message:error", "queryJSON": "{}"},
                },
                evidence_mode="query",
                incident_context={"service": "svc-a", "namespace": "default"},
                input_hints={},
            )

            result = runtime.consume_prompt_skill(capability="evidence.plan", graph_state=state)

            self.assertIsInstance(result, dict)
            self.assertEqual(result["skill_id"], "claude.evidence.script_planner")
            self.assertEqual(
                result["knowledge_skill_ids"],
                ["elasticsearch.evidence.plan", "prometheus.evidence.plan"],
            )
            self.assertEqual(state.evidence_plan["metadata"]["prompt_skill"], "claude.evidence.script_planner")
            self.assertEqual(state.evidence_plan["metadata"]["resource_ids"], ["templates/evidence-script-output-rules.md"])
            self.assertEqual(state.evidence_plan["metadata"]["tool_result_count"], 2)
            self.assertEqual(state.metrics_query_status, "ok")
            self.assertEqual(state.logs_query_status, "ok")
            self.assertTrue(state.metrics_branch_meta["tool_result_reusable"])
            self.assertTrue(state.logs_branch_meta["tool_result_reusable"])
            self.assertEqual([item["tool_name"] for item in toolcalls], ["mcp.query_metrics", "mcp.query_logs"])

    def test_script_executor_evidence_plan_after_tools_rejects_tool_calls(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            base = Path(tmp_dir)
            executor_dir = base / "executor"
            executor_dir.mkdir(parents=True)
            _write_skill_dir(
                executor_dir,
                name="Evidence Script Planner",
                description="Script executor planner",
                script_body=(
                    "def run(input_payload, ctx):\n"
                    "    if ctx.get('phase') == 'plan_tools':\n"
                    "        return {\n"
                    "            'tool_calls': [\n"
                    "                {\n"
                    "                    'tool': 'mcp.query_metrics',\n"
                    "                    'input': {\n"
                    "                        'datasource_id': 'ds-metrics',\n"
                    "                        'promql': 'sum(up)',\n"
                    "                        'start_ts': 100,\n"
                    "                        'end_ts': 200,\n"
                    "                        'step_seconds': 30,\n"
                    "                    },\n"
                    "                }\n"
                    "            ]\n"
                    "        }\n"
                    "    return {\n"
                    "        'tool_calls': [\n"
                    "            {\n"
                    "                'tool': 'mcp.query_logs',\n"
                    "                'input': {\n"
                    "                    'datasource_id': 'ds-logs',\n"
                    "                    'query': 'message:error',\n"
                    "                    'start_ts': 100,\n"
                    "                    'end_ts': 200,\n"
                    "                    'limit': 10,\n"
                    "                },\n"
                    "            }\n"
                    "        ]\n"
                    "    }\n"
                ),
            )
            executor_bundle = base / "executor.zip"
            executor_digest = _zip_dir(executor_dir, executor_bundle)
            skill_catalog = SkillCatalog.from_resolved_skillsets(
                skillsets_payload=[
                    {
                        "skillsetID": "skillset.default",
                        "skills": [
                            {
                                "skillID": "claude.evidence.script_planner",
                                "version": "1.0.0",
                                "artifactURL": executor_bundle.resolve().as_uri(),
                                "bundleDigest": executor_digest,
                                "manifestJSON": '{"bundle_format":"claude_skill_v1","instruction_file":"SKILL.md","name":"Evidence Script Planner","description":"Script executor planner","compatibility":""}',
                                "capability": "evidence.plan",
                                "role": "executor",
                                "executorMode": "script",
                                "allowedTools": ["query_metrics", "query_logs"],
                                "priority": 100,
                                "enabled": True,
                            }
                        ],
                    }
                ],
                cache_dir=str(base / "cache"),
            )

            class _FakeSession:
                def __init__(self) -> None:
                    self.headers: dict[str, str] = {}

            class _FakeClient:
                def __init__(self) -> None:
                    self.session = _FakeSession()
                    self.instance_id = ""

            class _SelectingAgent:
                configured = True

                def select_skill(self, **_: object) -> SkillSelectionResult:
                    return SkillSelectionResult(
                        selected_binding_key="claude.evidence.script_planner\x001.0.0\x00evidence.plan\x00executor",
                        reason="use script planner",
                    )

            runtime = OrchestratorRuntime(
                client=_FakeClient(),
                job_id="job-1",
                instance_id="orc-test",
                heartbeat_interval_seconds=10,
                skill_catalog=skill_catalog,
                skills_execution_mode="prompt_first",
                skills_tool_calling_mode="evidence_plan_dual_tool",
                skill_agent=_SelectingAgent(),
            )

            runtime.query_metrics = lambda **kwargs: {  # type: ignore[method-assign]
                "queryResultJSON": '{"data":{"result":[{"metric":{"service":"svc-a"},"values":[[1710000000,"0.12"]]}]}}',
                "rowCount": 1,
                "resultSizeBytes": 96,
                "isTruncated": False,
            }

            state = GraphState(
                job_id="job-1",
                incident_id="inc-1",
                evidence_plan={"budget": {"max_calls": 2}},
                metrics_branch_meta={
                    "mode": "query",
                    "query_type": "metrics",
                    "request_payload": {
                        "datasource_id": "ds-metrics",
                        "promql": "sum(up)",
                        "start_ts": 100,
                        "end_ts": 200,
                        "step_seconds": 30,
                    },
                    "query_request": {"datasourceID": "ds-metrics", "queryText": "sum(up)", "queryJSON": "{}"},
                },
                evidence_mode="query",
            )

            result = runtime.consume_prompt_skill(capability="evidence.plan", graph_state=state)
            self.assertIsNone(result)
            self.assertIsNone(state.metrics_query_status)
            self.assertIsNone(state.logs_query_status)

    def test_script_executor_evidence_plan_without_tool_mode_returns_final_payload(self) -> None:
        with tempfile.TemporaryDirectory() as tmp_dir:
            base = Path(tmp_dir)
            executor_dir = base / "executor"
            executor_dir.mkdir(parents=True)
            _write_skill_dir(
                executor_dir,
                name="Evidence Script Planner",
                description="Script executor planner",
                script_body=(
                    "def run(input_payload, ctx):\n"
                    "    return {\n"
                    "        'payload': {\n"
                    "            'evidence_plan_patch': {\n"
                    "                'metadata': {\n"
                    "                    'prompt_skill': 'claude.evidence.script_planner',\n"
                    "                    'executor_mode': 'script',\n"
                    "                }\n"
                    "            }\n"
                    "        }\n"
                    "    }\n"
                ),
            )
            executor_bundle = base / "executor.zip"
            executor_digest = _zip_dir(executor_dir, executor_bundle)
            skill_catalog = SkillCatalog.from_resolved_skillsets(
                skillsets_payload=[
                    {
                        "skillsetID": "skillset.default",
                        "skills": [
                            {
                                "skillID": "claude.evidence.script_planner",
                                "version": "1.0.0",
                                "artifactURL": executor_bundle.resolve().as_uri(),
                                "bundleDigest": executor_digest,
                                "manifestJSON": '{"bundle_format":"claude_skill_v1","instruction_file":"SKILL.md","name":"Evidence Script Planner","description":"Script executor planner","compatibility":""}',
                                "capability": "evidence.plan",
                                "role": "executor",
                                "executorMode": "script",
                                "allowedTools": ["query_metrics", "query_logs"],
                                "priority": 100,
                                "enabled": True,
                            }
                        ],
                    }
                ],
                cache_dir=str(base / "cache"),
            )

            class _FakeSession:
                def __init__(self) -> None:
                    self.headers: dict[str, str] = {}

            class _FakeClient:
                def __init__(self) -> None:
                    self.session = _FakeSession()
                    self.instance_id = ""

            class _SelectingAgent:
                configured = True

                def select_skill(self, **_: object) -> SkillSelectionResult:
                    return SkillSelectionResult(
                        selected_binding_key="claude.evidence.script_planner\x001.0.0\x00evidence.plan\x00executor",
                        reason="use script planner",
                    )

            runtime = OrchestratorRuntime(
                client=_FakeClient(),
                job_id="job-1",
                instance_id="orc-test",
                heartbeat_interval_seconds=10,
                skill_catalog=skill_catalog,
                skills_execution_mode="prompt_first",
                skills_tool_calling_mode="disabled",
                skill_agent=_SelectingAgent(),
            )

            state = GraphState(job_id="job-1", incident_id="inc-1", evidence_plan={"budget": {"max_calls": 1}}, evidence_mode="query")
            result = runtime.consume_prompt_skill(capability="evidence.plan", graph_state=state)
            self.assertIsInstance(result, dict)
            self.assertEqual(result["skill_id"], "claude.evidence.script_planner")
            self.assertEqual(state.evidence_plan["metadata"]["executor_mode"], "script")
            self.assertIsNone(state.metrics_query_status)
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

    def test_query_metrics_node_reuses_prewarmed_skill_result(self) -> None:
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

            def query_metrics(self, **kwargs):
                raise AssertionError("query_metrics should not execute when prewarmed result is reusable")

        runtime = _ReuseRuntime()
        state = GraphState(
            job_id="job-1",
            incident_id="inc-1",
            metrics_branch_meta={
                "mode": "query",
                "query_type": "metrics",
                "tool_result_reusable": True,
                "tool_result_source": "skill_prompt_first",
                "request_payload": {
                    "datasource_id": "ds-metrics",
                    "promql": 'sum(rate(http_requests_total{service="svc-a"}[5m]))',
                    "start_ts": 100,
                    "end_ts": 200,
                    "step_seconds": 60,
                },
                "query_request": {
                    "datasourceID": "ds-metrics",
                    "queryText": 'sum(rate(http_requests_total{service="svc-a"}[5m]))',
                    "queryJSON": "{}",
                },
            },
            metrics_query_status="ok",
            metrics_query_output={
                "queryResultJSON": '{"data":{"result":[{"metric":{"service":"svc-a"},"values":[[1710000000,"0.12"]]}]}}',
                "rowCount": 1,
                "resultSizeBytes": 96,
                "isTruncated": False,
            },
            metrics_query_latency_ms=23,
            metrics_query_result_size_bytes=96,
        )
        out = query_metrics_node(state, runtime)  # type: ignore[arg-type]
        self.assertEqual(out["metrics_query_status"], "ok")
        self.assertEqual(out["metrics_query_latency_ms"], 23)
        self.assertEqual(len(runtime.reported), 1)
        self.assertEqual(runtime.reported[0]["tool_name"], "evidence.metrics.reuse")
        self.assertEqual(len(runtime.observations), 1)
        self.assertEqual(runtime.observations[0]["tool"], "skill.tool_reuse")


if __name__ == "__main__":
    unittest.main()
