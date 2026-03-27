"""Tests for Domain Agent nodes (Phase HM3/HM4)."""
from __future__ import annotations

import json
import os
import unittest
from dataclasses import dataclass, field
from typing import Any
from unittest import mock

from orchestrator.langgraph.nodes_agents import (
    DomainFinding,
    _append_empty_finding,
    _find_task_for_domain,
    _is_domain_agent_enabled,
    merge_domain_findings,
    run_change_agent,
    run_knowledge_agent,
    run_observability_agent,
)
from orchestrator.langgraph.config import OrchestratorConfig
from orchestrator.state import GraphState


class TestDomainFinding(unittest.TestCase):
    """Tests for DomainFinding dataclass."""

    def test_default_values(self) -> None:
        """Test default values are set correctly."""
        finding = DomainFinding(domain="observability", summary="Test finding")
        self.assertEqual(finding.domain, "observability")
        self.assertEqual(finding.summary, "Test finding")
        self.assertEqual(finding.evidence_candidates, [])
        self.assertEqual(finding.diagnosis_patch, {})
        self.assertEqual(finding.session_patch_proposal, {})
        self.assertEqual(finding.status, "ok")

    def test_custom_values(self) -> None:
        """Test custom values are preserved."""
        finding = DomainFinding(
            domain="change",
            summary="Custom finding",
            evidence_candidates=[{"type": "metrics"}],
            diagnosis_patch={"summary": "Patch"},
            session_patch_proposal={"key": "value"},
            status="degraded",
        )
        self.assertEqual(finding.domain, "change")
        self.assertEqual(finding.summary, "Custom finding")
        self.assertEqual(finding.evidence_candidates, [{"type": "metrics"}])
        self.assertEqual(finding.diagnosis_patch, {"summary": "Patch"})
        self.assertEqual(finding.session_patch_proposal, {"key": "value"})
        self.assertEqual(finding.status, "degraded")

    def test_to_dict(self) -> None:
        """Test serialization to dict."""
        finding = DomainFinding(
            domain="knowledge",
            summary="Test",
            evidence_candidates=[{"id": "e1"}],
        )
        result = finding.to_dict()
        self.assertIsInstance(result, dict)
        self.assertEqual(result["domain"], "knowledge")
        self.assertEqual(result["summary"], "Test")
        self.assertEqual(result["evidence_candidates"], [{"id": "e1"}])
        self.assertEqual(result["status"], "ok")

    def test_from_dict(self) -> None:
        """Test deserialization from dict."""
        data = {
            "domain": "observability",
            "summary": "From dict",
            "evidence_candidates": [{"id": "e1"}],
            "diagnosis_patch": {"root_cause": "test"},
            "session_patch_proposal": {"update": "value"},
            "status": "error",
        }
        finding = DomainFinding.from_dict(data)
        self.assertEqual(finding.domain, "observability")
        self.assertEqual(finding.summary, "From dict")
        self.assertEqual(finding.evidence_candidates, [{"id": "e1"}])
        self.assertEqual(finding.diagnosis_patch, {"root_cause": "test"})
        self.assertEqual(finding.session_patch_proposal, {"update": "value"})
        self.assertEqual(finding.status, "error")

    def test_from_dict_missing_fields(self) -> None:
        """Test from_dict with missing fields uses defaults."""
        data = {"domain": "change"}
        finding = DomainFinding.from_dict(data)
        self.assertEqual(finding.domain, "change")
        self.assertEqual(finding.summary, "")
        self.assertEqual(finding.status, "ok")


class TestFindTaskForDomain(unittest.TestCase):
    """Tests for _find_task_for_domain helper."""

    def test_find_existing_task(self) -> None:
        """Test finding an existing task."""
        state = GraphState(
            job_id="job-1",
            domain_tasks=[
                {"task_id": "t1", "domain": "observability", "goal": "Goal 1"},
                {"task_id": "t2", "domain": "change", "goal": "Goal 2"},
            ],
        )
        result = _find_task_for_domain(state, "change")
        self.assertIsNotNone(result)
        self.assertEqual(result["task_id"], "t2")

    def test_find_missing_task_returns_none(self) -> None:
        """Test finding a missing task returns None."""
        state = GraphState(
            job_id="job-1",
            domain_tasks=[
                {"task_id": "t1", "domain": "observability", "goal": "Goal 1"},
            ],
        )
        result = _find_task_for_domain(state, "knowledge")
        self.assertIsNone(result)

    def test_find_in_empty_list_returns_none(self) -> None:
        """Test finding in empty list returns None."""
        state = GraphState(job_id="job-1")
        result = _find_task_for_domain(state, "observability")
        self.assertIsNone(result)


class TestAppendEmptyFinding(unittest.TestCase):
    """Tests for _append_empty_finding helper."""

    def test_append_empty_finding(self) -> None:
        """Test appending an empty finding."""
        state = GraphState(job_id="job-1")
        _append_empty_finding(state, "observability", "no_llm")

        self.assertEqual(len(state.domain_findings), 1)
        finding = state.domain_findings[0]
        self.assertEqual(finding["domain"], "observability")
        self.assertEqual(finding["status"], "degraded")
        self.assertIn("no_llm", finding["summary"])


class TestMergeDomainFindings(unittest.TestCase):
    """Tests for merge_domain_findings node function."""

    def test_merge_empty_findings(self) -> None:
        """Test merging empty findings."""
        state = GraphState(job_id="job-1")
        cfg = OrchestratorConfig()
        runtime = mock.MagicMock()

        result = merge_domain_findings(state, cfg, runtime)

        self.assertEqual(result.merged_findings["evidence_candidates"], [])
        self.assertEqual(result.merged_findings["diagnosis_patch"], {})
        self.assertEqual(result.merged_findings["domain_count"], 0)

    def test_merge_single_finding(self) -> None:
        """Test merging a single finding."""
        state = GraphState(
            job_id="job-1",
            domain_findings=[
                {
                    "domain": "observability",
                    "summary": "Found issues",
                    "evidence_candidates": [{"id": "e1"}, {"id": "e2"}],
                    "diagnosis_patch": {"summary": "Patch 1"},
                }
            ],
        )
        cfg = OrchestratorConfig()
        runtime = mock.MagicMock()

        result = merge_domain_findings(state, cfg, runtime)

        self.assertEqual(len(result.merged_findings["evidence_candidates"]), 2)
        self.assertEqual(result.merged_findings["diagnosis_patch"]["summary"], "Patch 1")
        self.assertEqual(result.merged_findings["domain_count"], 1)

    def test_merge_multiple_findings(self) -> None:
        """Test merging multiple findings."""
        state = GraphState(
            job_id="job-1",
            domain_findings=[
                {
                    "domain": "observability",
                    "summary": "Found issues",
                    "evidence_candidates": [{"id": "e1"}],
                    "diagnosis_patch": {"key1": "value1"},
                    "session_patch_proposal": {"s1": "v1"},
                },
                {
                    "domain": "change",
                    "summary": "Found changes",
                    "evidence_candidates": [{"id": "e2"}],
                    "diagnosis_patch": {"key2": "value2"},
                    "session_patch_proposal": {"s2": "v2"},
                },
            ],
        )
        cfg = OrchestratorConfig()
        runtime = mock.MagicMock()

        result = merge_domain_findings(state, cfg, runtime)

        self.assertEqual(len(result.merged_findings["evidence_candidates"]), 2)
        self.assertEqual(result.merged_findings["diagnosis_patch"]["key1"], "value1")
        self.assertEqual(result.merged_findings["diagnosis_patch"]["key2"], "value2")
        self.assertEqual(result.merged_findings["session_patch_proposal"]["s1"], "v1")
        self.assertEqual(result.merged_findings["session_patch_proposal"]["s2"], "v2")
        self.assertEqual(result.merged_findings["domain_count"], 2)
        self.assertIn("observability", result.merged_findings["domains"])
        self.assertIn("change", result.merged_findings["domains"])

    def test_merge_overrides_diagnosis_patch(self) -> None:
        """Test that later findings override earlier diagnosis patches."""
        state = GraphState(
            job_id="job-1",
            domain_findings=[
                {
                    "domain": "observability",
                    "summary": "First",
                    "diagnosis_patch": {"summary": "First summary", "confidence": 0.5},
                },
                {
                    "domain": "change",
                    "summary": "Second",
                    "diagnosis_patch": {"summary": "Second summary"},
                },
            ],
        )
        cfg = OrchestratorConfig()
        runtime = mock.MagicMock()

        result = merge_domain_findings(state, cfg, runtime)

        # Second overrides first
        self.assertEqual(result.merged_findings["diagnosis_patch"]["summary"], "Second summary")
        # But non-overlapping keys are preserved
        self.assertEqual(result.merged_findings["diagnosis_patch"]["confidence"], 0.5)

    def test_merge_copies_evidence_candidates_to_state(self) -> None:
        """Test that merged evidence_candidates are copied to state."""
        state = GraphState(
            job_id="job-1",
            domain_findings=[
                {
                    "domain": "observability",
                    "summary": "Found",
                    "evidence_candidates": [{"id": "e1"}, {"id": "e2"}],
                }
            ],
        )
        cfg = OrchestratorConfig()
        runtime = mock.MagicMock()

        result = merge_domain_findings(state, cfg, runtime)

        self.assertEqual(result.evidence_candidates, [{"id": "e1"}, {"id": "e2"}])


class TestRunObservabilityAgent(unittest.TestCase):
    """Tests for run_observability_agent node function."""

    def test_agent_no_task_adds_empty_finding(self) -> None:
        """Test when no observability task, adds empty finding."""
        state = GraphState(
            job_id="job-1",
            incident_id="inc-1",
            domain_tasks=[
                {"task_id": "t1", "domain": "change", "goal": "Test"},
            ],
        )
        cfg = OrchestratorConfig()
        runtime = mock.MagicMock()
        runtime.get_fc_adapter.return_value = mock.MagicMock()

        result = run_observability_agent(state, cfg, runtime)

        # Should add a degraded finding
        self.assertEqual(len(result.domain_findings), 1)
        self.assertEqual(result.domain_findings[0]["status"], "degraded")

    def test_agent_with_tool_scope_filters_tools(self) -> None:
        """Test that tool_scope from task is used to filter tools."""
        state = GraphState(
            job_id="job-1",
            incident_id="inc-1",
            domain_tasks=[
                {
                    "task_id": "t1",
                    "domain": "observability",
                    "goal": "Test",
                    "tool_scope": ["allowed_tool"],
                },
            ],
        )
        cfg = OrchestratorConfig()
        runtime = mock.MagicMock()

        # Mock FC adapter with multiple tools
        mock_adapter = mock.MagicMock()
        mock_adapter.to_openai_tools_for_graph.return_value = [
            {"type": "function", "function": {"name": "allowed_tool", "description": "desc"}},
            {"type": "function", "function": {"name": "other_tool", "description": "desc"}},
        ]
        runtime.get_fc_adapter.return_value = mock_adapter

        # Mock LLM
        mock_llm = mock.MagicMock()
        mock_llm.bind_tools.return_value.invoke.return_value = mock.MagicMock(
            content="Test finding", tool_calls=[]
        )

        with mock.patch(
            "orchestrator.langgraph.nodes_agents._get_llm", return_value=mock_llm
        ):
            result = run_observability_agent(state, cfg, runtime)

        # Should have filtered tools - only allowed_tool should be bound
        bind_call_args = mock_llm.bind_tools.call_args
        bound_tools = bind_call_args[0][0] if bind_call_args else []
        tool_names = [t.get("function", {}).get("name") for t in bound_tools]
        self.assertEqual(tool_names, ["allowed_tool"])
        # Should have degradation reason for filtering
        self.assertTrue(
            any("tools_filtered_by_scope" in r for r in result.degrade_reasons)
        )

    def test_agent_without_tools_invokes_plain_llm(self) -> None:
        """Test that the agent skips bind_tools when no tools are available."""
        state = GraphState(
            job_id="job-1",
            incident_id="inc-1",
            domain_tasks=[
                {"task_id": "t1", "domain": "observability", "goal": "Test"},
            ],
        )
        cfg = OrchestratorConfig()
        runtime = mock.MagicMock()

        # Mock FC adapter
        mock_adapter = mock.MagicMock()
        mock_adapter.to_openai_tools_for_graph.return_value = []
        runtime.get_fc_adapter.return_value = mock_adapter

        # Mock LLM
        mock_llm = mock.MagicMock()
        mock_llm.invoke.return_value = mock.MagicMock(content="Observed finding", tool_calls=[])

        with mock.patch(
            "orchestrator.langgraph.nodes_agents._get_llm", return_value=mock_llm
        ):
            result = run_observability_agent(state, cfg, runtime)

        # Should have finding and use plain invoke path
        self.assertEqual(len(result.domain_findings), 1)
        self.assertEqual(result.domain_findings[0]["status"], "ok")
        self.assertEqual(result.domain_findings[0]["summary"], "Observed finding")
        mock_llm.bind_tools.assert_not_called()
        mock_llm.invoke.assert_called_once()


class TestRunChangeAgent(unittest.TestCase):
    """Tests for run_change_agent node function."""

    def test_change_agent_disabled_skips(self) -> None:
        """Test when change agent disabled, skips execution."""
        state = GraphState(
            job_id="job-1",
            incident_id="inc-1",
            domain_tasks=[
                {"task_id": "t1", "domain": "change", "goal": "Test"},
            ],
        )
        cfg = OrchestratorConfig()
        runtime = mock.MagicMock()

        with mock.patch.dict(os.environ, {
            "RCA_DOMAIN_AGENT_CHANGE_ENABLED": "false",
        }):
            result = run_change_agent(state, cfg, runtime)

        # Should not add any findings when disabled
        self.assertEqual(len(result.domain_findings), 0)

    def test_change_agent_no_task_skips(self) -> None:
        """Test when no change task, skips gracefully."""
        state = GraphState(
            job_id="job-1",
            incident_id="inc-1",
            domain_tasks=[
                {"task_id": "t1", "domain": "observability", "goal": "Test"},
            ],
        )
        cfg = OrchestratorConfig()
        runtime = mock.MagicMock()

        result = run_change_agent(state, cfg, runtime)

        # Should not add any findings when no task
        self.assertEqual(len(result.domain_findings), 0)

    def test_change_agent_with_task_executes(self) -> None:
        """Test change agent executes when task is present."""
        state = GraphState(
            job_id="job-1",
            incident_id="inc-1",
            domain_tasks=[
                {
                    "task_id": "t1",
                    "domain": "change",
                    "goal": "Find changes",
                    "tool_scope": ["deployment_history"],
                },
            ],
        )
        cfg = OrchestratorConfig()
        runtime = mock.MagicMock()

        # Mock FC adapter
        mock_adapter = mock.MagicMock()
        mock_adapter.to_openai_tools_for_graph.return_value = [
            {"type": "function", "function": {"name": "deployment_history", "description": "desc"}},
        ]
        runtime.get_fc_adapter.return_value = mock_adapter

        # Mock LLM
        mock_llm = mock.MagicMock()
        mock_llm.bind_tools.return_value.invoke.return_value = mock.MagicMock(
            content="Found deployment", tool_calls=[]
        )

        with mock.patch(
            "orchestrator.langgraph.nodes_agents._get_llm", return_value=mock_llm
        ):
            result = run_change_agent(state, cfg, runtime)

        # Should have finding
        self.assertEqual(len(result.domain_findings), 1)
        self.assertEqual(result.domain_findings[0]["domain"], "change")

    def test_change_agent_without_tools_invokes_plain_llm(self) -> None:
        """Test change agent skips bind_tools when no tools are available."""
        state = GraphState(
            job_id="job-1",
            incident_id="inc-1",
            domain_tasks=[
                {
                    "task_id": "t1",
                    "domain": "change",
                    "goal": "Find changes",
                },
            ],
        )
        cfg = OrchestratorConfig()
        runtime = mock.MagicMock()

        mock_adapter = mock.MagicMock()
        mock_adapter.to_openai_tools_for_graph.return_value = []
        runtime.get_fc_adapter.return_value = mock_adapter

        mock_llm = mock.MagicMock()
        mock_llm.invoke.return_value = mock.MagicMock(content="Found changes", tool_calls=[])

        with mock.patch(
            "orchestrator.langgraph.nodes_agents._get_llm", return_value=mock_llm
        ):
            result = run_change_agent(state, cfg, runtime)

        self.assertEqual(len(result.domain_findings), 1)
        self.assertEqual(result.domain_findings[0]["status"], "ok")
        self.assertEqual(result.domain_findings[0]["summary"], "Found changes")
        mock_llm.bind_tools.assert_not_called()
        mock_llm.invoke.assert_called_once()


class TestRunKnowledgeAgent(unittest.TestCase):
    """Tests for run_knowledge_agent node function."""

    def test_knowledge_agent_disabled_skips(self) -> None:
        """Test when knowledge agent disabled, skips execution."""
        state = GraphState(
            job_id="job-1",
            incident_id="inc-1",
            domain_tasks=[
                {"task_id": "t1", "domain": "knowledge", "goal": "Test"},
            ],
        )
        cfg = OrchestratorConfig()
        runtime = mock.MagicMock()

        with mock.patch.dict(os.environ, {
            "RCA_DOMAIN_AGENT_KNOWLEDGE_ENABLED": "false",
        }):
            result = run_knowledge_agent(state, cfg, runtime)

        # Should not add any findings when disabled
        self.assertEqual(len(result.domain_findings), 0)

    def test_knowledge_agent_no_task_skips(self) -> None:
        """Test when no knowledge task, skips gracefully."""
        state = GraphState(
            job_id="job-1",
            incident_id="inc-1",
            domain_tasks=[
                {"task_id": "t1", "domain": "observability", "goal": "Test"},
            ],
        )
        cfg = OrchestratorConfig()
        runtime = mock.MagicMock()

        result = run_knowledge_agent(state, cfg, runtime)

        # Should not add any findings when no task
        self.assertEqual(len(result.domain_findings), 0)

    def test_knowledge_agent_with_task_executes(self) -> None:
        """Test knowledge agent executes when task is present."""
        state = GraphState(
            job_id="job-1",
            incident_id="inc-1",
            incident_context={"alert_name": "HighErrorRate"},
            domain_tasks=[
                {
                    "task_id": "t1",
                    "domain": "knowledge",
                    "goal": "Find similar incidents",
                    "tool_scope": ["search_kb"],
                },
            ],
        )
        cfg = OrchestratorConfig()
        runtime = mock.MagicMock()

        # Mock FC adapter
        mock_adapter = mock.MagicMock()
        mock_adapter.to_openai_tools_for_graph.return_value = [
            {"type": "function", "function": {"name": "search_kb", "description": "desc"}},
        ]
        runtime.get_fc_adapter.return_value = mock_adapter

        # Mock LLM
        mock_llm = mock.MagicMock()
        mock_llm.bind_tools.return_value.invoke.return_value = mock.MagicMock(
            content="Found similar incidents", tool_calls=[]
        )

        with mock.patch(
            "orchestrator.langgraph.nodes_agents._get_llm", return_value=mock_llm
        ):
            result = run_knowledge_agent(state, cfg, runtime)

        # Should have finding
        self.assertEqual(len(result.domain_findings), 1)
        self.assertEqual(result.domain_findings[0]["domain"], "knowledge")

    def test_knowledge_agent_without_tools_invokes_plain_llm(self) -> None:
        """Test knowledge agent skips bind_tools when no tools are available."""
        state = GraphState(
            job_id="job-1",
            incident_id="inc-1",
            incident_context={"alert_name": "HighErrorRate"},
            domain_tasks=[
                {
                    "task_id": "t1",
                    "domain": "knowledge",
                    "goal": "Find similar incidents",
                },
            ],
        )
        cfg = OrchestratorConfig()
        runtime = mock.MagicMock()

        mock_adapter = mock.MagicMock()
        mock_adapter.to_openai_tools_for_graph.return_value = []
        runtime.get_fc_adapter.return_value = mock_adapter

        mock_llm = mock.MagicMock()
        mock_llm.invoke.return_value = mock.MagicMock(content="Found similar incidents", tool_calls=[])

        with mock.patch(
            "orchestrator.langgraph.nodes_agents._get_llm", return_value=mock_llm
        ):
            result = run_knowledge_agent(state, cfg, runtime)

        self.assertEqual(len(result.domain_findings), 1)
        self.assertEqual(result.domain_findings[0]["status"], "ok")
        self.assertEqual(result.domain_findings[0]["summary"], "Found similar incidents")
        mock_llm.bind_tools.assert_not_called()
        mock_llm.invoke.assert_called_once()


class TestMergeEvidenceHybridPath(unittest.TestCase):
    """Tests for merge_evidence in hybrid multi-agent path."""

    def test_preserves_evidence_from_domain_agents(self) -> None:
        """Test that merge_evidence preserves evidence saved by domain agents."""
        from orchestrator.langgraph.nodes import merge_evidence

        state = GraphState(
            job_id="job-1",
            incident_id="inc-1",
            # Simulate evidence saved by domain agents
            evidence_ids=["ev-1", "ev-2"],
            evidence_meta=[
                {"source": "metrics", "no_data": False},
                {"source": "logs", "no_data": False},
            ],
            # Simulate domain findings (indicates hybrid path)
            domain_findings=[
                {"domain": "observability", "summary": "Found issues"},
            ],
        )
        cfg = OrchestratorConfig()
        runtime = mock.MagicMock()

        result = merge_evidence(state, cfg, runtime)

        # Should preserve evidence from domain agents
        self.assertEqual(result.evidence_ids, ["ev-1", "ev-2"])
        self.assertEqual(len(result.evidence_meta), 2)

    def test_creates_mock_evidence_when_no_domain_evidence(self) -> None:
        """Test that merge_evidence creates mock evidence when no domain evidence exists."""
        from orchestrator.langgraph.nodes import merge_evidence

        state = GraphState(
            job_id="job-1",
            incident_id="inc-1",
            evidence_ids=[],
            evidence_meta=[],
            # No domain findings - should use legacy path which falls back to mock
        )
        cfg = OrchestratorConfig()
        runtime = mock.MagicMock()
        runtime.save_mock_evidence.return_value = mock.MagicMock(
            evidence_id="fallback-ev-1",
            idempotency_key="key-1",
            created_by="ai:job-1",
        )

        result = merge_evidence(state, cfg, runtime)

        # Should have created mock evidence
        self.assertTrue(len(result.evidence_ids) >= 1)
        runtime.save_mock_evidence.assert_called_once()

    def test_creates_mock_evidence_when_domain_findings_but_no_evidence(self) -> None:
        """Test that merge_evidence creates mock when domain findings exist but no evidence was saved.

        This covers the case where LLM/adapter was missing and domain agents only
        produced degraded/empty findings without saving any evidence.
        """
        from orchestrator.langgraph.nodes import merge_evidence

        state = GraphState(
            job_id="job-1",
            incident_id="inc-1",
            evidence_ids=[],  # No evidence saved
            evidence_meta=[],
            # Domain findings exist (degraded) but no evidence
            domain_findings=[
                {"domain": "observability", "summary": "No finding: no_llm", "status": "degraded"},
            ],
        )
        cfg = OrchestratorConfig()
        runtime = mock.MagicMock()
        runtime.save_mock_evidence.return_value = mock.MagicMock(
            evidence_id="fallback-ev-1",
            idempotency_key="key-1",
            created_by="ai:job-1",
        )

        result = merge_evidence(state, cfg, runtime)

        # Should have created mock evidence (not skipped due to domain_findings existing)
        self.assertTrue(len(result.evidence_ids) >= 1)
        runtime.save_mock_evidence.assert_called_once()


if __name__ == "__main__":
    unittest.main()
