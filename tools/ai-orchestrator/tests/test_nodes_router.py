"""Tests for Route Agent nodes (Phase HM3)."""
from __future__ import annotations

import json
import os
import unittest
from dataclasses import dataclass, field
from typing import Any
from unittest import mock

from orchestrator.langgraph.nodes_router import (
    DOMAIN_CAPABILITY_MAP,
    DomainTask,
    _default_observability_task,
    _enrich_task_with_skill_scope,
    _parse_domain_tasks,
    _validate_domain_task,
    route_domains,
)
from orchestrator.langgraph.config import OrchestratorConfig
from orchestrator.state import GraphState


class TestDomainTask(unittest.TestCase):
    """Tests for DomainTask dataclass."""

    def test_default_values(self) -> None:
        """Test default values are set correctly."""
        task = DomainTask(task_id="task-1", domain="observability", goal="Test goal")
        self.assertEqual(task.task_id, "task-1")
        self.assertEqual(task.domain, "observability")
        self.assertEqual(task.goal, "Test goal")
        self.assertEqual(task.priority, 100)
        self.assertEqual(task.mode, "hybrid")
        self.assertEqual(task.tool_scope, [])
        self.assertEqual(task.skill_scope, [])

    def test_custom_values(self) -> None:
        """Test custom values are preserved."""
        task = DomainTask(
            task_id="task-2",
            domain="change",
            goal="Custom goal",
            priority=50,
            mode="custom_mode",
            tool_scope=["tool1", "tool2"],
            skill_scope=["skill-1"],
        )
        self.assertEqual(task.task_id, "task-2")
        self.assertEqual(task.domain, "change")
        self.assertEqual(task.goal, "Custom goal")
        self.assertEqual(task.priority, 50)
        self.assertEqual(task.mode, "custom_mode")
        self.assertEqual(task.tool_scope, ["tool1", "tool2"])
        self.assertEqual(task.skill_scope, ["skill-1"])

    def test_to_dict(self) -> None:
        """Test serialization to dict."""
        task = DomainTask(
            task_id="task-3",
            domain="knowledge",
            goal="Lookup docs",
            priority=75,
        )
        result = task.to_dict()
        self.assertIsInstance(result, dict)
        self.assertEqual(result["task_id"], "task-3")
        self.assertEqual(result["domain"], "knowledge")
        self.assertEqual(result["goal"], "Lookup docs")
        self.assertEqual(result["priority"], 75)
        self.assertEqual(result["mode"], "hybrid")
        self.assertEqual(result["tool_scope"], [])
        self.assertEqual(result["skill_scope"], [])

    def test_from_dict(self) -> None:
        """Test deserialization from dict."""
        data = {
            "task_id": "task-4",
            "domain": "observability",
            "goal": "From dict",
            "priority": 200,
            "mode": "tool_only",
            "tool_scope": ["t1"],
            "skill_scope": ["s1", "s2"],
        }
        task = DomainTask.from_dict(data)
        self.assertEqual(task.task_id, "task-4")
        self.assertEqual(task.domain, "observability")
        self.assertEqual(task.goal, "From dict")
        self.assertEqual(task.priority, 200)
        self.assertEqual(task.mode, "tool_only")
        self.assertEqual(task.tool_scope, ["t1"])
        self.assertEqual(task.skill_scope, ["s1", "s2"])

    def test_from_dict_missing_fields(self) -> None:
        """Test from_dict with missing fields uses defaults."""
        data = {"task_id": "task-5", "domain": "change"}
        task = DomainTask.from_dict(data)
        self.assertEqual(task.task_id, "task-5")
        self.assertEqual(task.domain, "change")
        self.assertEqual(task.goal, "")
        self.assertEqual(task.priority, 100)
        self.assertEqual(task.mode, "hybrid")


class TestParseDomainTasks(unittest.TestCase):
    """Tests for _parse_domain_tasks helper."""

    def test_parse_empty_content(self) -> None:
        """Test parsing empty content returns empty list."""
        result = _parse_domain_tasks("")
        self.assertEqual(result, [])

    def test_parse_none_content(self) -> None:
        """Test parsing None returns empty list."""
        result = _parse_domain_tasks(None)  # type: ignore
        self.assertEqual(result, [])

    def test_parse_direct_json_list(self) -> None:
        """Test parsing direct JSON list."""
        content = json.dumps([
            {"task_id": "t1", "domain": "observability", "goal": "Goal 1"},
            {"task_id": "t2", "domain": "change", "goal": "Goal 2"},
        ])
        result = _parse_domain_tasks(content)
        self.assertEqual(len(result), 2)
        self.assertEqual(result[0]["task_id"], "t1")
        self.assertEqual(result[1]["domain"], "change")

    def test_parse_json_with_domain_tasks_key(self) -> None:
        """Test parsing JSON with domain_tasks key."""
        content = json.dumps({
            "domain_tasks": [
                {"task_id": "t1", "domain": "observability", "goal": "Goal 1"},
            ]
        })
        result = _parse_domain_tasks(content)
        self.assertEqual(len(result), 1)
        self.assertEqual(result[0]["task_id"], "t1")

    def test_parse_json_code_block(self) -> None:
        """Test parsing JSON from code block."""
        content = '''Here are the tasks:
```json
[
    {"task_id": "t1", "domain": "observability", "goal": "Goal 1"}
]
```
'''
        result = _parse_domain_tasks(content)
        self.assertEqual(len(result), 1)
        self.assertEqual(result[0]["task_id"], "t1")

    def test_parse_invalid_json_returns_empty(self) -> None:
        """Test parsing invalid JSON returns empty list."""
        result = _parse_domain_tasks("not valid json")
        self.assertEqual(result, [])

    def test_parse_non_list_returns_empty(self) -> None:
        """Test parsing non-list JSON returns empty list."""
        result = _parse_domain_tasks('{"key": "value"}')
        self.assertEqual(result, [])


class TestValidateDomainTask(unittest.TestCase):
    """Tests for _validate_domain_task helper."""

    def test_validate_valid_task(self) -> None:
        """Test valid task passes through."""
        task = {
            "task_id": "t1",
            "domain": "observability",
            "goal": "Goal",
            "priority": 100,
            "mode": "hybrid",
        }
        result = _validate_domain_task(task)
        self.assertEqual(result["task_id"], "t1")
        self.assertEqual(result["domain"], "observability")

    def test_validate_invalid_domain_defaults_to_observability(self) -> None:
        """Test invalid domain defaults to observability."""
        task = {"task_id": "t1", "domain": "invalid_domain"}
        result = _validate_domain_task(task)
        self.assertEqual(result["domain"], "observability")

    def test_validate_supported_domains_preserved(self) -> None:
        """Test HM4: observability, change, and knowledge domains are preserved."""
        state = GraphState(job_id="job-1")

        # Test change domain is preserved
        task = {"task_id": "t1", "domain": "change", "goal": "Test"}
        result = _validate_domain_task(task, state)
        self.assertEqual(result["domain"], "change")
        self.assertFalse(any("domain_not_supported" in r for r in state.degrade_reasons))

        # Test knowledge domain is preserved
        task = {"task_id": "t2", "domain": "knowledge", "goal": "Test"}
        result = _validate_domain_task(task, state)
        self.assertEqual(result["domain"], "knowledge")
        self.assertFalse(any("domain_not_supported" in r for r in state.degrade_reasons))

    def test_validate_unsupported_domain_fallback_with_state(self) -> None:
        """Test truly unsupported domain falls back to observability with degradation."""
        state = GraphState(job_id="job-1")
        task = {"task_id": "t1", "domain": "unsupported_domain", "goal": "Test"}
        result = _validate_domain_task(task, state)
        self.assertEqual(result["domain"], "observability")
        self.assertTrue(any("domain_not_supported" in r for r in state.degrade_reasons))

    def test_validate_observability_domain_preserved(self) -> None:
        """Test observability domain is preserved in HM4."""
        state = GraphState(job_id="job-1")
        task = {"task_id": "t1", "domain": "observability", "goal": "Test"}
        result = _validate_domain_task(task, state)
        self.assertEqual(result["domain"], "observability")
        # No degradation for supported domain
        self.assertFalse(any("domain_not_supported" in r for r in state.degrade_reasons))

    def test_validate_generates_task_id_if_missing(self) -> None:
        """Test generates task_id if missing."""
        task = {"domain": "observability", "goal": "Test"}
        result = _validate_domain_task(task)
        self.assertIn("observability-", result["task_id"])

    def test_validate_clamps_priority(self) -> None:
        """Test priority is clamped to valid range."""
        task = {"task_id": "t1", "domain": "observability", "priority": 5000}
        result = _validate_domain_task(task)
        self.assertEqual(result["priority"], 1000)  # max

        task = {"task_id": "t2", "domain": "observability", "priority": -100}
        result = _validate_domain_task(task)
        self.assertEqual(result["priority"], 1)  # min

    def test_validate_normalizes_mode(self) -> None:
        """Test mode is normalized."""
        task = {"task_id": "t1", "domain": "observability", "mode": "SKILL_ONLY"}
        result = _validate_domain_task(task)
        self.assertEqual(result["mode"], "SKILL_ONLY")  # preserved

    def test_validate_preserves_tool_scope(self) -> None:
        """Test tool_scope is preserved."""
        task = {
            "task_id": "t1",
            "domain": "observability",
            "tool_scope": ["tool1", "tool2"],
        }
        result = _validate_domain_task(task)
        self.assertEqual(result["tool_scope"], ["tool1", "tool2"])


class TestDefaultObservabilityTask(unittest.TestCase):
    """Tests for _default_observability_task helper."""

    def test_default_task_structure(self) -> None:
        """Test default task has correct structure."""
        state = GraphState(job_id="job-1")
        result = _default_observability_task(state)

        self.assertEqual(result["task_id"], "obs-default")
        self.assertEqual(result["domain"], "observability")
        self.assertEqual(result["priority"], 100)
        self.assertEqual(result["mode"], "hybrid")

    def test_default_task_includes_service(self) -> None:
        """Test default task includes service in goal."""
        state = GraphState(
            job_id="job-1",
            incident_context={"service": "my-service"},
        )
        result = _default_observability_task(state)
        self.assertIn("my-service", result["goal"])

    def test_default_task_includes_namespace(self) -> None:
        """Test default task includes namespace in goal."""
        state = GraphState(
            job_id="job-1",
            incident_context={"service": "svc", "namespace": "prod"},
        )
        result = _default_observability_task(state)
        self.assertIn("prod", result["goal"])


class TestRouteDomainsNode(unittest.TestCase):
    """Tests for route_domains node function."""

    def test_route_sets_route_context(self) -> None:
        """Test route_domains sets route_context."""
        state = GraphState(job_id="job-1", incident_id="inc-1")
        cfg = OrchestratorConfig()
        runtime = mock.MagicMock()

        result = route_domains(state, cfg, runtime)

        self.assertIn("routed_at", result.route_context)
        self.assertIn("domain_count", result.route_context)
        self.assertIn("domains", result.route_context)


class TestEnrichTaskWithSkillScope(unittest.TestCase):
    """Tests for _enrich_task_with_skill_scope helper (HM11)."""

    def test_observability_with_available_skill(self) -> None:
        """Test observability task gets skill_scope when skill available."""
        task = {"task_id": "t1", "domain": "observability", "goal": "Test"}
        skill_surface = {
            "capability_map": {
                "evidence.plan": ["skill-1@v1:executor"],
            }
        }
        result = _enrich_task_with_skill_scope(task, skill_surface)

        self.assertEqual(result["skill_scope"], ["evidence.plan"])
        self.assertEqual(result["mode"], "hybrid")

    def test_observability_without_available_skill(self) -> None:
        """Test observability task without skill uses native tools."""
        task = {"task_id": "t1", "domain": "observability", "goal": "Test"}
        skill_surface = {"capability_map": {}}  # No evidence.plan skill

        result = _enrich_task_with_skill_scope(task, skill_surface)

        self.assertEqual(result["skill_scope"], [])
        self.assertEqual(result["mode"], "hybrid")

    def test_domain_without_capability_mapping(self) -> None:
        """Test domain without capability mapping uses native tools."""
        task = {"task_id": "t1", "domain": "change", "goal": "Test"}
        skill_surface = {"capability_map": {"evidence.plan": ["skill-1@v1:executor"]}}

        result = _enrich_task_with_skill_scope(task, skill_surface)

        self.assertEqual(result["skill_scope"], [])
        self.assertEqual(result["mode"], "hybrid")

    def test_null_skill_surface(self) -> None:
        """Test null skill_surface uses native tools."""
        task = {"task_id": "t1", "domain": "observability", "goal": "Test"}

        result = _enrich_task_with_skill_scope(task, None)

        self.assertEqual(result["skill_scope"], [])
        self.assertEqual(result["mode"], "hybrid")

    def test_preserves_existing_task_fields(self) -> None:
        """Test existing task fields are preserved."""
        task = {
            "task_id": "t1",
            "domain": "observability",
            "goal": "Custom goal",
            "priority": 50,
            "tool_scope": ["tool1"],
        }
        skill_surface = {"capability_map": {"evidence.plan": ["skill-1@v1:executor"]}}

        result = _enrich_task_with_skill_scope(task, skill_surface)

        self.assertEqual(result["task_id"], "t1")
        self.assertEqual(result["domain"], "observability")
        self.assertEqual(result["goal"], "Custom goal")
        self.assertEqual(result["priority"], 50)
        self.assertEqual(result["tool_scope"], ["tool1"])
        self.assertEqual(result["skill_scope"], ["evidence.plan"])

    def test_domain_capability_map_constant(self) -> None:
        """Test DOMAIN_CAPABILITY_MAP has expected entries."""
        self.assertIn("observability", DOMAIN_CAPABILITY_MAP)
        self.assertEqual(DOMAIN_CAPABILITY_MAP["observability"], "evidence.plan")


if __name__ == "__main__":
    unittest.main()