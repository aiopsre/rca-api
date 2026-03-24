"""Tests for SkillCoordinator and execute_capability_skill().

Tests cover:
- Script executor selection and execution
- Prompt executor selection and execution
- Knowledge skill resource loading
- evidence.plan plan_tools/after_tools phases
- diagnosis.enrich script path (no tool-calling)
- Fallback behavior
"""
from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from orchestrator.runtime.skill_coordinator import (
    CAPABILITY_CONFIGS,
    CapabilityConfig,
    SkillCoordinator,
    SkillExecutionContext,
    SkillExecutionResult,
)
from orchestrator.runtime.runtime import OrchestratorRuntime


def _make_runtime(
    *,
    skill_catalog: MagicMock | None = None,
) -> OrchestratorRuntime:
    """Helper to create OrchestratorRuntime with minimal mocks."""
    mock_client = MagicMock()
    return OrchestratorRuntime(
        client=mock_client,
        job_id="test-job",
        instance_id="test-instance",
        heartbeat_interval_seconds=30,
        skill_catalog=skill_catalog,
    )


def _make_catalog_with_skill(
    *,
    binding_key: str,
    capability: str,
    role: str = "executor",
    executor_mode: str = "script",
    allowed_tools: tuple[str, ...] = (),
    priority: int = 100,
    root_dir: Path | None = None,
) -> MagicMock:
    """Helper to create a mock SkillCatalog with a skill."""
    mock_catalog = MagicMock()

    # Create mock skill candidate
    mock_candidate = MagicMock()
    mock_candidate.binding_key = binding_key
    mock_candidate.skill_id = binding_key.split("\x00")[0] if "\x00" in binding_key else binding_key
    mock_candidate.version = binding_key.split("\x00")[1] if "\x00" in binding_key and len(binding_key.split("\x00")) > 1 else "v1"
    mock_candidate.name = f"Test Skill for {capability}"
    mock_candidate.description = f"Test skill for {capability}"
    mock_candidate.compatibility = "test"
    mock_candidate.capability = capability
    mock_candidate.role = role
    mock_candidate.executor_mode = executor_mode
    mock_candidate.allowed_tools = allowed_tools
    mock_candidate.priority = priority

    # Create mock catalog skill
    mock_catalog_skill = MagicMock()
    mock_catalog_skill.root_dir = root_dir or Path("/tmp/test-skill")
    mock_catalog_skill.binding_key = binding_key
    mock_catalog_skill.summary.skill_id = mock_candidate.skill_id
    mock_catalog_skill.summary.version = mock_candidate.version

    # Set up catalog methods
    if role == "knowledge":
        mock_catalog.knowledge_candidates_for_capability.return_value = [mock_candidate]
        mock_catalog.executor_candidates_for_capability.return_value = []
    else:
        mock_catalog.knowledge_candidates_for_capability.return_value = []
        mock_catalog.executor_candidates_for_capability.return_value = [mock_candidate]

    mock_catalog.candidates_for_capability.return_value = [mock_candidate]
    mock_catalog.get_skill.return_value = mock_catalog_skill
    mock_catalog.list_skill_resources.return_value = []
    mock_catalog.load_skill_document.return_value = "# Test Skill\n\nTest skill document."
    mock_catalog.load_skill_resources.return_value = []

    return mock_catalog


def _make_agent_with_selection(
    *,
    selected_executor_binding_key: str = "",
    selected_knowledge_binding_keys: list[str] | None = None,
    selected_resource_ids: list[str] | None = None,
) -> MagicMock:
    """Helper to create a mock PromptSkillAgent with selection results."""
    mock_agent = MagicMock()
    mock_agent.configured = True

    # Executor selection result
    executor_result = MagicMock()
    executor_result.selected_binding_key = selected_executor_binding_key
    executor_result.reason = "test selection"

    # Knowledge selection result
    knowledge_result = MagicMock()
    knowledge_result.selected_binding_keys = selected_knowledge_binding_keys or []
    knowledge_result.reason = "test knowledge selection"

    # Resource selection result
    resource_result = MagicMock()
    resource_result.selected_resource_ids = selected_resource_ids or []
    resource_result.reason = "test resource selection"

    mock_agent.select_skill.return_value = executor_result
    mock_agent.select_knowledge_skills.return_value = knowledge_result
    mock_agent.select_skill_resources.return_value = resource_result

    return mock_agent


class TestCapabilityConfig:
    """Tests for CapabilityConfig and CAPABILITY_CONFIGS."""

    def test_evidence_plan_config(self) -> None:
        """Test evidence.plan capability config."""
        config = CAPABILITY_CONFIGS.get("evidence.plan")
        assert config is not None
        assert config.allow_tool_calling is True
        assert config.max_tool_calls == 2
        assert "mcp.query_metrics" in (config.allowed_tools or [])
        assert "mcp.query_logs" in (config.allowed_tools or [])
        assert config.require_executor is False

    def test_diagnosis_enrich_config(self) -> None:
        """Test diagnosis.enrich capability config."""
        config = CAPABILITY_CONFIGS.get("diagnosis.enrich")
        assert config is not None
        assert config.allow_tool_calling is False
        assert config.require_executor is False

    def test_unknown_capability(self) -> None:
        """Test unknown capability returns None."""
        config = CAPABILITY_CONFIGS.get("unknown.capability")
        assert config is None


class TestSkillExecutionContext:
    """Tests for SkillExecutionContext dataclass."""

    def test_default_values(self) -> None:
        """Test default values are set correctly."""
        ctx = SkillExecutionContext(
            capability="test.capability",
            input_payload={"key": "value"},
            stage_summary={"stage": "test"},
        )
        assert ctx.capability == "test.capability"
        assert ctx.input_payload == {"key": "value"}
        assert ctx.stage_summary == {"stage": "test"}
        assert ctx.knowledge_context == []
        assert ctx.tool_results == []


class TestSkillExecutionResult:
    """Tests for SkillExecutionResult dataclass."""

    def test_default_values(self) -> None:
        """Test default values are set correctly."""
        result = SkillExecutionResult(success=True)
        assert result.success is True
        assert result.payload == {}
        assert result.session_patch == {}
        assert result.observations == []
        assert result.tool_calls == []
        assert result.fallback_used is False
        assert result.error_message == ""


class TestSkillCoordinatorExecuteCapabilitySkill:
    """Tests for SkillCoordinator.execute_capability_skill()."""

    def test_unknown_capability(self) -> None:
        """Test error for unknown capability."""
        mock_catalog = MagicMock()
        mock_agent = MagicMock()
        mock_runtime = MagicMock()

        coordinator = SkillCoordinator(
            catalog=mock_catalog,
            agent=mock_agent,
            runtime=mock_runtime,
        )

        result = coordinator.execute_capability_skill(
            capability="unknown.capability",
            input_payload={},
            stage_summary={},
        )

        assert result.success is False
        assert "unknown capability" in result.error_message

    def test_no_executor_candidates_require_executor(self) -> None:
        """Test error when require_executor but no candidates."""
        mock_catalog = MagicMock()
        mock_catalog.knowledge_candidates_for_capability.return_value = []
        mock_catalog.executor_candidates_for_capability.return_value = []

        mock_agent = MagicMock()
        mock_runtime = MagicMock()

        # Create a config with require_executor=True
        test_config = CapabilityConfig(
            capability="test.require",
            require_executor=True,
        )

        coordinator = SkillCoordinator(
            catalog=mock_catalog,
            agent=mock_agent,
            runtime=mock_runtime,
        )

        with patch.dict(CAPABILITY_CONFIGS, {"test.require": test_config}):
            result = coordinator.execute_capability_skill(
                capability="test.require",
                input_payload={},
                stage_summary={},
            )

        assert result.success is False
        assert "no executor candidates" in result.error_message

    def test_no_skill_selected_fallback(self) -> None:
        """Test fallback when no skill is selected."""
        mock_catalog = _make_catalog_with_skill(
            binding_key="test-skill\x00v1\x00evidence.plan\x00executor",
            capability="evidence.plan",
            executor_mode="script",
        )
        mock_agent = _make_agent_with_selection(selected_executor_binding_key="")
        mock_runtime = MagicMock()

        coordinator = SkillCoordinator(
            catalog=mock_catalog,
            agent=mock_agent,
            runtime=mock_runtime,
        )

        result = coordinator.execute_capability_skill(
            capability="evidence.plan",
            input_payload={},
            stage_summary={},
        )

        assert result.success is True
        assert result.fallback_used is True
        assert "no skill selected" in result.error_message

    def test_script_executor_selected(self) -> None:
        """Test script executor is selected and executed."""
        binding_key = "test-script\x00v1\x00evidence.plan\x00executor"
        mock_catalog = _make_catalog_with_skill(
            binding_key=binding_key,
            capability="evidence.plan",
            executor_mode="script",
            allowed_tools=("mcp.query_logs",),
        )
        mock_agent = _make_agent_with_selection(
            selected_executor_binding_key=binding_key,
        )

        # Mock runtime with execute_skill_script
        mock_runtime = MagicMock()
        script_result = MagicMock()
        script_result.payload = {"evidence_plan_patch": {"test": "data"}}
        script_result.session_patch = {}
        script_result.observations = [{"kind": "note", "message": "test"}]
        script_result.tool_calls = []
        mock_runtime.execute_skill_script.return_value = script_result

        coordinator = SkillCoordinator(
            catalog=mock_catalog,
            agent=mock_agent,
            runtime=mock_runtime,
        )

        result = coordinator.execute_capability_skill(
            capability="evidence.plan",
            input_payload={"incident_id": "inc-123"},
            stage_summary={},
        )

        assert result.success is True
        assert result.fallback_used is False
        assert result.selected_executor_binding_key == binding_key
        assert result.payload == {"evidence_plan_patch": {"test": "data"}}
        mock_runtime.execute_skill_script.assert_called_once()

    def test_prompt_executor_selected(self) -> None:
        """Test prompt executor is selected and executed."""
        binding_key = "test-prompt\x00v1\x00evidence.plan\x00executor"
        mock_catalog = _make_catalog_with_skill(
            binding_key=binding_key,
            capability="evidence.plan",
            executor_mode="prompt",
        )
        mock_agent = _make_agent_with_selection(
            selected_executor_binding_key=binding_key,
        )

        # Mock consume_skill result
        consume_result = MagicMock()
        consume_result.payload = {"evidence_plan_patch": {"prompt": "data"}}
        consume_result.session_patch = {}
        consume_result.observations = []
        mock_agent.consume_skill.return_value = consume_result

        mock_runtime = MagicMock()

        coordinator = SkillCoordinator(
            catalog=mock_catalog,
            agent=mock_agent,
            runtime=mock_runtime,
        )

        result = coordinator.execute_capability_skill(
            capability="evidence.plan",
            input_payload={},
            stage_summary={},
        )

        assert result.success is True
        assert result.selected_executor_binding_key == binding_key
        mock_agent.consume_skill.assert_called_once()

    def test_knowledge_skills_loaded(self) -> None:
        """Test knowledge skills are loaded."""
        knowledge_binding = "knowledge-skill\x00v1\x00evidence.plan\x00knowledge"
        executor_binding = "executor-skill\x00v1\x00evidence.plan\x00executor"

        mock_catalog = MagicMock()

        # Knowledge candidate
        knowledge_candidate = MagicMock()
        knowledge_candidate.binding_key = knowledge_binding
        knowledge_candidate.skill_id = "knowledge-skill"
        knowledge_candidate.version = "v1"
        knowledge_candidate.name = "Knowledge Skill"
        knowledge_candidate.description = "Test knowledge"
        knowledge_candidate.compatibility = "test"
        knowledge_candidate.capability = "evidence.plan"
        knowledge_candidate.role = "knowledge"
        knowledge_candidate.executor_mode = ""
        knowledge_candidate.allowed_tools = ()
        knowledge_candidate.priority = 100
        knowledge_candidate.to_summary_dict.return_value = {
            "binding_key": knowledge_binding,
            "skill_id": "knowledge-skill",
        }

        # Executor candidate
        executor_candidate = MagicMock()
        executor_candidate.binding_key = executor_binding
        executor_candidate.skill_id = "executor-skill"
        executor_candidate.version = "v1"
        executor_candidate.name = "Executor Skill"
        executor_candidate.description = "Test executor"
        executor_candidate.compatibility = "test"
        executor_candidate.capability = "evidence.plan"
        executor_candidate.role = "executor"
        executor_candidate.executor_mode = "script"
        executor_candidate.allowed_tools = ()
        executor_candidate.priority = 100
        executor_candidate.to_summary_dict.return_value = {
            "binding_key": executor_binding,
        }

        mock_catalog.knowledge_candidates_for_capability.return_value = [knowledge_candidate]
        mock_catalog.executor_candidates_for_capability.return_value = [executor_candidate]
        mock_catalog.list_skill_resources.return_value = []
        mock_catalog.load_skill_document.return_value = "# Test"

        mock_agent = _make_agent_with_selection(
            selected_executor_binding_key=executor_binding,
            selected_knowledge_binding_keys=[knowledge_binding],
        )

        mock_runtime = MagicMock()
        script_result = MagicMock()
        script_result.payload = {}
        script_result.session_patch = {}
        script_result.observations = []
        script_result.tool_calls = []
        mock_runtime.execute_skill_script.return_value = script_result

        coordinator = SkillCoordinator(
            catalog=mock_catalog,
            agent=mock_agent,
            runtime=mock_runtime,
        )

        result = coordinator.execute_capability_skill(
            capability="evidence.plan",
            input_payload={},
            stage_summary={},
        )

        assert result.success is True
        assert knowledge_binding in result.selected_knowledge_binding_keys
        mock_agent.select_knowledge_skills.assert_called_once()

    def test_tool_calling_plan_tools_after_tools(self) -> None:
        """Test evidence.plan with tool calling (plan_tools → after_tools)."""
        binding_key = "evidence-script\x00v1\x00evidence.plan\x00executor"
        mock_catalog = _make_catalog_with_skill(
            binding_key=binding_key,
            capability="evidence.plan",
            executor_mode="script",
            allowed_tools=("mcp.query_metrics", "mcp.query_logs"),
        )
        mock_agent = _make_agent_with_selection(
            selected_executor_binding_key=binding_key,
        )

        # Mock runtime with two-phase execution
        mock_runtime = MagicMock()

        # Phase 1: plan_tools returns tool_calls
        plan_result = MagicMock()
        plan_result.payload = {}
        plan_result.session_patch = {}
        plan_result.observations = []
        plan_result.tool_calls = [
            {"tool": "mcp.query_logs", "input": {"query": "error"}}
        ]

        # Phase 2: after_tools returns final result
        after_result = MagicMock()
        after_result.payload = {"evidence_plan_patch": {"final": "result"}}
        after_result.session_patch = {}
        after_result.observations = []
        after_result.tool_calls = []

        mock_runtime.execute_skill_script.side_effect = [plan_result, after_result]
        mock_runtime.call_tool.return_value = {"results": []}

        coordinator = SkillCoordinator(
            catalog=mock_catalog,
            agent=mock_agent,
            runtime=mock_runtime,
        )

        result = coordinator.execute_capability_skill(
            capability="evidence.plan",
            input_payload={"incident_id": "inc-123"},
            stage_summary={},
        )

        assert result.success is True
        # Should have called execute_skill_script twice
        assert mock_runtime.execute_skill_script.call_count == 2
        # First call should be plan_tools
        first_call = mock_runtime.execute_skill_script.call_args_list[0]
        assert first_call.kwargs["phase"] == "plan_tools"
        # Second call should be after_tools with tool_results
        second_call = mock_runtime.execute_skill_script.call_args_list[1]
        assert second_call.kwargs["phase"] == "after_tools"

    def test_diagnosis_enrich_no_tool_calling(self) -> None:
        """Test diagnosis.enrich does not allow tool calling."""
        binding_key = "diagnosis-script\x00v1\x00diagnosis.enrich\x00executor"
        mock_catalog = _make_catalog_with_skill(
            binding_key=binding_key,
            capability="diagnosis.enrich",
            executor_mode="script",
        )
        mock_agent = _make_agent_with_selection(
            selected_executor_binding_key=binding_key,
        )

        mock_runtime = MagicMock()
        script_result = MagicMock()
        script_result.payload = {"diagnosis_patch": {"summary": "test"}}
        script_result.session_patch = {}
        script_result.observations = []
        script_result.tool_calls = []  # Should be empty
        mock_runtime.execute_skill_script.return_value = script_result

        coordinator = SkillCoordinator(
            catalog=mock_catalog,
            agent=mock_agent,
            runtime=mock_runtime,
        )

        result = coordinator.execute_capability_skill(
            capability="diagnosis.enrich",
            input_payload={"diagnosis_json": {}},
            stage_summary={},
        )

        assert result.success is True
        # Should only call once with phase="final"
        mock_runtime.execute_skill_script.assert_called_once()
        call_kwargs = mock_runtime.execute_skill_script.call_args.kwargs
        assert call_kwargs["phase"] == "final"

    def test_execution_error_returns_failure(self) -> None:
        """Test execution error returns failure result."""
        binding_key = "error-skill\x00v1\x00evidence.plan\x00executor"
        mock_catalog = _make_catalog_with_skill(
            binding_key=binding_key,
            capability="evidence.plan",
            executor_mode="script",
        )
        mock_agent = _make_agent_with_selection(
            selected_executor_binding_key=binding_key,
        )

        mock_runtime = MagicMock()
        mock_runtime.execute_skill_script.side_effect = RuntimeError("script failed")

        coordinator = SkillCoordinator(
            catalog=mock_catalog,
            agent=mock_agent,
            runtime=mock_runtime,
        )

        result = coordinator.execute_capability_skill(
            capability="evidence.plan",
            input_payload={},
            stage_summary={},
        )

        assert result.success is False
        assert "script failed" in result.error_message


class TestOrchestratorRuntimeExecuteCapabilitySkill:
    """Tests for OrchestratorRuntime.execute_capability_skill()."""

    def test_no_skill_catalog_returns_fallback(self) -> None:
        """Test returns fallback when no skill catalog configured."""
        runtime = _make_runtime(skill_catalog=None)

        result = runtime.execute_capability_skill(
            capability="evidence.plan",
            input_payload={},
        )

        assert result.success is False
        assert result.fallback_used is True
        assert "skill coordinator not available" in result.error_message

    def test_coordinator_is_lazy_initialized(self) -> None:
        """Test coordinator is initialized on first use."""
        mock_catalog = MagicMock()
        mock_catalog.knowledge_candidates_for_capability.return_value = []
        mock_catalog.executor_candidates_for_capability.return_value = []

        runtime = _make_runtime(skill_catalog=mock_catalog)

        # Coordinator should be None initially
        assert runtime._skill_coordinator is None

        # Call should initialize coordinator
        result = runtime.execute_capability_skill(
            capability="evidence.plan",
            input_payload={},
        )

        # Coordinator should now be initialized
        assert runtime._skill_coordinator is not None

    def test_coordinator_reused(self) -> None:
        """Test coordinator is reused across calls."""
        mock_catalog = MagicMock()
        mock_catalog.knowledge_candidates_for_capability.return_value = []
        mock_catalog.executor_candidates_for_capability.return_value = []

        runtime = _make_runtime(skill_catalog=mock_catalog)

        result1 = runtime.execute_capability_skill(
            capability="evidence.plan",
            input_payload={},
        )
        coordinator1 = runtime._skill_coordinator

        result2 = runtime.execute_capability_skill(
            capability="evidence.plan",
            input_payload={},
        )
        coordinator2 = runtime._skill_coordinator

        # Same coordinator instance should be reused
        assert coordinator1 is coordinator2


class TestInferToolCallingMode:
    """Tests for _infer_tool_calling_mode()."""

    def test_disabled_when_not_allowed(self) -> None:
        """Test disabled when allow_tool_calling is False."""
        mock_catalog = MagicMock()
        mock_agent = MagicMock()
        mock_runtime = MagicMock()

        coordinator = SkillCoordinator(
            catalog=mock_catalog,
            agent=mock_agent,
            runtime=mock_runtime,
        )

        candidate = MagicMock()
        candidate.allowed_tools = ("mcp.query_metrics", "mcp.query_logs")
        config = CapabilityConfig(
            capability="test",
            allow_tool_calling=False,
        )

        mode = coordinator._infer_tool_calling_mode(candidate, config)
        assert mode == "disabled"

    def test_dual_tool_mode(self) -> None:
        """Test dual tool mode when both metrics and logs are allowed."""
        mock_catalog = MagicMock()
        mock_agent = MagicMock()
        mock_runtime = MagicMock()

        coordinator = SkillCoordinator(
            catalog=mock_catalog,
            agent=mock_agent,
            runtime=mock_runtime,
        )

        candidate = MagicMock()
        candidate.allowed_tools = ("mcp.query_metrics", "mcp.query_logs")
        config = CapabilityConfig(
            capability="test",
            allow_tool_calling=True,
        )

        mode = coordinator._infer_tool_calling_mode(candidate, config)
        assert mode == "evidence_plan_dual_tool"

    def test_single_hop_mode(self) -> None:
        """Test single hop mode when only logs is allowed."""
        mock_catalog = MagicMock()
        mock_agent = MagicMock()
        mock_runtime = MagicMock()

        coordinator = SkillCoordinator(
            catalog=mock_catalog,
            agent=mock_agent,
            runtime=mock_runtime,
        )

        candidate = MagicMock()
        candidate.allowed_tools = ("mcp.query_logs",)
        config = CapabilityConfig(
            capability="test",
            allow_tool_calling=True,
        )

        mode = coordinator._infer_tool_calling_mode(candidate, config)
        assert mode == "evidence_plan_single_hop"

    def test_disabled_no_matching_tools(self) -> None:
        """Test disabled when no matching tools."""
        mock_catalog = MagicMock()
        mock_agent = MagicMock()
        mock_runtime = MagicMock()

        coordinator = SkillCoordinator(
            catalog=mock_catalog,
            agent=mock_agent,
            runtime=mock_runtime,
        )

        candidate = MagicMock()
        candidate.allowed_tools = ("mcp.other_tool",)
        config = CapabilityConfig(
            capability="test",
            allow_tool_calling=True,
        )

        mode = coordinator._infer_tool_calling_mode(candidate, config)
        assert mode == "disabled"


class TestExecuteToolCallsEnforcement:
    """Tests for _execute_tool_calls() enforcement."""

    def test_reject_tool_not_in_allowed_tools(self) -> None:
        """Test rejection when tool is not in allowed_tools."""
        mock_catalog = MagicMock()
        mock_agent = MagicMock()
        mock_runtime = MagicMock()

        coordinator = SkillCoordinator(
            catalog=mock_catalog,
            agent=mock_agent,
            runtime=mock_runtime,
        )

        tool_calls = [
            {"tool": "mcp.unauthorized_tool", "input": {}},
        ]

        config = CapabilityConfig(
            capability="test",
            allow_tool_calling=True,
            allowed_tools=["mcp.query_logs"],
        )
        candidate = MagicMock()
        candidate.allowed_tools = ()

        results = coordinator._execute_tool_calls(tool_calls, config, candidate)

        assert len(results) == 1
        assert results[0]["status"] == "rejected"
        assert "not in allowed_tools" in results[0]["error"]

    def test_reject_exceeds_max_tool_calls(self) -> None:
        """Test rejection when exceeding max_tool_calls."""
        mock_catalog = MagicMock()
        mock_agent = MagicMock()
        mock_runtime = MagicMock()
        mock_runtime.call_tool.return_value = {"result": "ok"}

        coordinator = SkillCoordinator(
            catalog=mock_catalog,
            agent=mock_agent,
            runtime=mock_runtime,
        )

        tool_calls = [
            {"tool": "mcp.query_logs", "input": {}},
            {"tool": "mcp.query_logs", "input": {}},
            {"tool": "mcp.query_logs", "input": {}},  # Third call should be rejected
        ]

        config = CapabilityConfig(
            capability="test",
            allow_tool_calling=True,
            allowed_tools=["mcp.query_logs"],
            max_tool_calls=2,
        )
        candidate = MagicMock()
        candidate.allowed_tools = ()

        results = coordinator._execute_tool_calls(tool_calls, config, candidate)

        assert len(results) == 3
        # First two should be ok
        assert results[0]["status"] == "ok"
        assert results[1]["status"] == "ok"
        # Third should be rejected
        assert results[2]["status"] == "rejected"
        assert "max_tool_calls" in results[2]["error"]

    def test_combine_config_and_candidate_allowed_tools(self) -> None:
        """Test that allowed_tools from config and candidate are combined."""
        mock_catalog = MagicMock()
        mock_agent = MagicMock()
        mock_runtime = MagicMock()
        mock_runtime.call_tool.return_value = {"result": "ok"}

        coordinator = SkillCoordinator(
            catalog=mock_catalog,
            agent=mock_agent,
            runtime=mock_runtime,
        )

        tool_calls = [
            {"tool": "mcp.query_logs", "input": {}},  # From config
            {"tool": "mcp.query_metrics", "input": {}},  # From candidate
        ]

        config = CapabilityConfig(
            capability="test",
            allow_tool_calling=True,
            allowed_tools=["mcp.query_logs"],
            max_tool_calls=2,
        )
        candidate = MagicMock()
        candidate.allowed_tools = ("mcp.query_metrics",)

        results = coordinator._execute_tool_calls(tool_calls, config, candidate)

        assert len(results) == 2
        assert results[0]["status"] == "ok"
        assert results[1]["status"] == "ok"

    def test_successful_execution_within_limits(self) -> None:
        """Test successful execution when within limits."""
        mock_catalog = MagicMock()
        mock_agent = MagicMock()
        mock_runtime = MagicMock()
        mock_runtime.call_tool.return_value = {"results": []}

        coordinator = SkillCoordinator(
            catalog=mock_catalog,
            agent=mock_agent,
            runtime=mock_runtime,
        )

        tool_calls = [
            {"tool": "mcp.query_logs", "input": {"query": "error"}},
        ]

        config = CapabilityConfig(
            capability="test",
            allow_tool_calling=True,
            allowed_tools=["mcp.query_logs"],
            max_tool_calls=2,
        )
        candidate = MagicMock()
        candidate.allowed_tools = ()

        results = coordinator._execute_tool_calls(tool_calls, config, candidate)

        assert len(results) == 1
        assert results[0]["status"] == "ok"
        mock_runtime.call_tool.assert_called_once_with("mcp.query_logs", {"query": "error"})