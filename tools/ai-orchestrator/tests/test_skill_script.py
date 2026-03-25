"""Tests for skill script execution via OrchestratorRuntime."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import MagicMock

import pytest

from orchestrator.runtime.runtime import OrchestratorRuntime
from orchestrator.skills.script_runner import ScriptExecutorError, ScriptExecutorResult


def _make_runtime(*, skill_catalog: MagicMock | None = None) -> OrchestratorRuntime:
    """Helper to create OrchestratorRuntime with minimal mocks."""
    mock_client = MagicMock()
    return OrchestratorRuntime(
        client=mock_client,
        job_id="test-job",
        instance_id="test-instance",
        heartbeat_interval_seconds=30,
        skill_catalog=skill_catalog,
    )


class TestExecuteSkillScript:
    """Test cases for OrchestratorRuntime.execute_skill_script()."""

    def test_execute_skill_script_skill_not_found(self) -> None:
        """Test error when skill catalog is not configured."""
        runtime = _make_runtime(skill_catalog=None)

        with pytest.raises(ScriptExecutorError, match="skill catalog is not configured"):
            runtime.execute_skill_script(
                skill_binding_key="nonexistent/v1",
                input_payload={},
            )

    def test_execute_skill_script_skill_missing(self) -> None:
        """Test error when skill is not found in catalog."""
        mock_catalog = MagicMock()
        mock_catalog.get_skill.return_value = None
        runtime = _make_runtime(skill_catalog=mock_catalog)

        with pytest.raises(ScriptExecutorError, match="skill not found"):
            runtime.execute_skill_script(
                skill_binding_key="missing-skill/v1",
                input_payload={},
            )

    def test_execute_skill_script_missing_executor(self) -> None:
        """Test error when skill has no scripts/executor.py."""
        mock_catalog = MagicMock()
        mock_skill = MagicMock()
        mock_skill.root_dir = Path("/nonexistent/path")
        mock_catalog.get_skill.return_value = mock_skill
        runtime = _make_runtime(skill_catalog=mock_catalog)

        with pytest.raises(ScriptExecutorError, match="script executor missing entrypoint"):
            runtime.execute_skill_script(
                skill_binding_key="no-script/v1",
                input_payload={},
            )

    def test_execute_skill_script_success(self, tmp_path: Path) -> None:
        """Test successful script execution."""
        # Create a mock skill bundle with executor.py
        skill_dir = tmp_path / "test-skill"
        scripts_dir = skill_dir / "scripts"
        scripts_dir.mkdir(parents=True)

        executor_py = scripts_dir / "executor.py"
        executor_py.write_text("""
from typing import Any

def run(input_payload: dict[str, Any], ctx: dict[str, Any]) -> dict[str, Any]:
    return {
        "payload": {
            "result": "success",
            "input_value": input_payload.get("value", 0) * 2,
        },
        "observations": [
            {"kind": "note", "message": "script executed successfully"}
        ],
    }
""")

        mock_catalog = MagicMock()
        mock_skill = MagicMock()
        mock_skill.root_dir = skill_dir
        mock_catalog.get_skill.return_value = mock_skill
        runtime = _make_runtime(skill_catalog=mock_catalog)

        result = runtime.execute_skill_script(
            skill_binding_key="test-skill/v1",
            input_payload={"value": 21},
            phase="final",
        )

        assert isinstance(result, ScriptExecutorResult)
        assert result.payload["result"] == "success"
        assert result.payload["input_value"] == 42
        assert len(result.observations) == 1
        assert result.observations[0]["kind"] == "note"

    def test_execute_skill_script_with_phases(self, tmp_path: Path) -> None:
        """Test script execution with plan_tools and after_tools phases."""
        skill_dir = tmp_path / "phase-skill"
        scripts_dir = skill_dir / "scripts"
        scripts_dir.mkdir(parents=True)

        executor_py = scripts_dir / "executor.py"
        executor_py.write_text("""
from typing import Any

def run(input_payload: dict[str, Any], ctx: dict[str, Any]) -> dict[str, Any]:
    phase = ctx.get("phase", "final")

    if phase == "plan_tools":
        return {
            "tool_calls": [
                {"tool": "test.tool", "input": {"query": "test"}}
            ],
            "observations": [
                {"kind": "note", "message": "planning tools"}
            ],
        }

    tool_results = ctx.get("tool_results", [])
    return {
        "payload": {
            "phase": phase,
            "tool_results_count": len(tool_results),
        },
        "observations": [
            {"kind": "note", "message": f"completed in phase={phase}"}
        ],
    }
""")

        mock_catalog = MagicMock()
        mock_skill = MagicMock()
        mock_skill.root_dir = skill_dir
        mock_catalog.get_skill.return_value = mock_skill
        runtime = _make_runtime(skill_catalog=mock_catalog)

        # Phase 1: plan_tools
        result = runtime.execute_skill_script(
            skill_binding_key="phase-skill/v1",
            input_payload={},
            phase="plan_tools",
        )
        assert len(result.tool_calls) == 1
        assert result.tool_calls[0]["tool"] == "test.tool"

        # Phase 2: after_tools with tool results
        result = runtime.execute_skill_script(
            skill_binding_key="phase-skill/v1",
            input_payload={},
            phase="after_tools",
            tool_results=[{"tool": "test.tool", "output": "data"}],
        )
        assert result.payload["phase"] == "after_tools"
        assert result.payload["tool_results_count"] == 1

    def test_execute_skill_script_error_handling(self, tmp_path: Path) -> None:
        """Test error handling when script raises exception."""
        skill_dir = tmp_path / "error-skill"
        scripts_dir = skill_dir / "scripts"
        scripts_dir.mkdir(parents=True)

        executor_py = scripts_dir / "executor.py"
        executor_py.write_text("""
from typing import Any

def run(input_payload: dict[str, Any], ctx: dict[str, Any]) -> dict[str, Any]:
    raise ValueError("intentional test error")
""")

        mock_catalog = MagicMock()
        mock_skill = MagicMock()
        mock_skill.root_dir = skill_dir
        mock_catalog.get_skill.return_value = mock_skill
        runtime = _make_runtime(skill_catalog=mock_catalog)

        with pytest.raises(ValueError, match="intentional test error"):
            runtime.execute_skill_script(
                skill_binding_key="error-skill/v1",
                input_payload={},
            )

    def test_execute_skill_script_invalid_return_type(self, tmp_path: Path) -> None:
        """Test error when script returns invalid type."""
        skill_dir = tmp_path / "invalid-return-skill"
        scripts_dir = skill_dir / "scripts"
        scripts_dir.mkdir(parents=True)

        executor_py = scripts_dir / "executor.py"
        executor_py.write_text("""
from typing import Any

def run(input_payload: dict[str, Any], ctx: dict[str, Any]) -> dict[str, Any]:
    return "not a dict"
""")

        mock_catalog = MagicMock()
        mock_skill = MagicMock()
        mock_skill.root_dir = skill_dir
        mock_catalog.get_skill.return_value = mock_skill
        runtime = _make_runtime(skill_catalog=mock_catalog)

        with pytest.raises(ScriptExecutorError, match="must return a dict"):
            runtime.execute_skill_script(
                skill_binding_key="invalid-return-skill/v1",
                input_payload={},
            )