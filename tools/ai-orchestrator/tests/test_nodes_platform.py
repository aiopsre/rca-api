"""Tests for Platform Special Agent nodes (Phase HM5)."""
from __future__ import annotations

import json
import os
import unittest
from typing import Any
from unittest import mock

from orchestrator.langgraph.nodes_platform import (
    _is_platform_special_agent_enabled,
    _parse_diagnosis_patch,
    sanitize_diagnosis_patch,
)
from orchestrator.langgraph.config import OrchestratorConfig
from orchestrator.state import GraphState


class TestIsPlatformSpecialAgentEnabled(unittest.TestCase):
    """Tests for _is_platform_special_agent_enabled helper."""

    def test_default_enabled(self) -> None:
        """Test default is enabled."""
        with mock.patch.dict(os.environ, {}, clear=True):
            result = _is_platform_special_agent_enabled()
            self.assertTrue(result)

    def test_disabled_via_env(self) -> None:
        """Test can be disabled via env var."""
        with mock.patch.dict(os.environ, {"RCA_PLATFORM_SPECIAL_AGENT_ENABLED": "false"}):
            result = _is_platform_special_agent_enabled()
            self.assertFalse(result)

    def test_enabled_via_env(self) -> None:
        """Test can be explicitly enabled via env var."""
        with mock.patch.dict(os.environ, {"RCA_PLATFORM_SPECIAL_AGENT_ENABLED": "true"}):
            result = _is_platform_special_agent_enabled()
            self.assertTrue(result)


class TestParseDiagnosisPatch(unittest.TestCase):
    """Tests for _parse_diagnosis_patch helper."""

    def test_parse_empty_content(self) -> None:
        """Test parsing empty content returns empty dict."""
        result = _parse_diagnosis_patch("")
        self.assertEqual(result, {})

    def test_parse_none_content(self) -> None:
        """Test parsing None returns empty dict."""
        result = _parse_diagnosis_patch(None)  # type: ignore
        self.assertEqual(result, {})

    def test_parse_direct_json(self) -> None:
        """Test parsing direct JSON."""
        content = json.dumps({
            "summary": "Test summary",
            "root_cause": {"summary": "Root cause", "confidence": 0.75},
        })
        result = _parse_diagnosis_patch(content)
        self.assertEqual(result["summary"], "Test summary")
        self.assertEqual(result["root_cause"]["confidence"], 0.75)

    def test_parse_wrapped_json(self) -> None:
        """Test parsing JSON with diagnosis_patch wrapper."""
        content = json.dumps({
            "diagnosis_patch": {
                "summary": "Wrapped summary",
                "next_steps": ["Step 1"],
            },
        })
        result = _parse_diagnosis_patch(content)
        self.assertEqual(result["summary"], "Wrapped summary")
        self.assertEqual(result["next_steps"], ["Step 1"])

    def test_parse_json_code_block(self) -> None:
        """Test parsing JSON from code block."""
        content = '''Here is the diagnosis:
```json
{
  "summary": "Code block summary",
  "unknowns": ["Unknown 1"]
}
```
'''
        result = _parse_diagnosis_patch(content)
        self.assertEqual(result["summary"], "Code block summary")
        self.assertEqual(result["unknowns"], ["Unknown 1"])

    def test_parse_invalid_json_returns_empty(self) -> None:
        """Test parsing invalid JSON returns empty dict."""
        result = _parse_diagnosis_patch("not valid json")
        self.assertEqual(result, {})


class TestSanitizeDiagnosisPatch(unittest.TestCase):
    """Tests for sanitize_diagnosis_patch function."""

    def test_sanitize_empty_payload(self) -> None:
        """Test sanitizing empty payload."""
        result = sanitize_diagnosis_patch({})
        self.assertEqual(result, {})

    def test_sanitize_none_payload(self) -> None:
        """Test sanitizing None payload."""
        result = sanitize_diagnosis_patch(None)  # type: ignore
        self.assertEqual(result, {})

    def test_sanitize_full_patch(self) -> None:
        """Test sanitizing a full diagnosis patch."""
        payload = {
            "summary": "Test summary",
            "root_cause": {
                "summary": "Root cause summary",
                "statement": "Root cause statement",
                "confidence": 0.85,
            },
            "recommendations": [
                {"type": "action", "action": "Do something", "risk": "low"},
            ],
            "unknowns": ["Unknown item"],
            "next_steps": ["Next step"],
        }
        result = sanitize_diagnosis_patch(payload)

        self.assertEqual(result["summary"], "Test summary")
        self.assertEqual(result["root_cause"]["summary"], "Root cause summary")
        self.assertEqual(result["root_cause"]["statement"], "Root cause statement")
        self.assertEqual(result["root_cause"]["confidence"], 0.85)
        self.assertEqual(len(result["recommendations"]), 1)
        self.assertEqual(result["unknowns"], ["Unknown item"])
        self.assertEqual(result["next_steps"], ["Next step"])

    def test_sanitize_caps_summary_length(self) -> None:
        """Test that summary length is capped."""
        long_summary = "x" * 2000
        result = sanitize_diagnosis_patch({"summary": long_summary})
        self.assertEqual(len(result["summary"]), 1000)

    def test_sanitize_clamps_confidence(self) -> None:
        """Test that confidence is clamped to [0, 1]."""
        result = sanitize_diagnosis_patch({
            "root_cause": {"confidence": 1.5},
        })
        self.assertEqual(result["root_cause"]["confidence"], 1.0)

        result = sanitize_diagnosis_patch({
            "root_cause": {"confidence": -0.5},
        })
        self.assertEqual(result["root_cause"]["confidence"], 0.0)

    def test_sanitize_caps_recommendations_count(self) -> None:
        """Test that recommendations count is capped."""
        recommendations = [
            {"action": f"Action {i}"} for i in range(15)
        ]
        result = sanitize_diagnosis_patch({"recommendations": recommendations})
        self.assertEqual(len(result["recommendations"]), 10)

    def test_sanitize_filters_invalid_recommendations(self) -> None:
        """Test that invalid recommendations are filtered."""
        recommendations = [
            {"action": "Valid action"},
            {"type": "missing_action"},
            "not a dict",
        ]
        result = sanitize_diagnosis_patch({"recommendations": recommendations})
        self.assertEqual(len(result["recommendations"]), 1)
        self.assertEqual(result["recommendations"][0]["action"], "Valid action")

    def test_sanitize_handles_missing_root_cause_gracefully(self) -> None:
        """Test handling of missing root_cause fields."""
        result = sanitize_diagnosis_patch({
            "root_cause": {},  # Empty but present
        })
        self.assertIn("root_cause", result)
        self.assertEqual(result["root_cause"]["summary"], "")
        self.assertEqual(result["root_cause"]["confidence"], 0.5)  # Default


class TestRunPlatformSpecialAgent(unittest.TestCase):
    """Tests for run_platform_special_agent node function."""

    def test_platform_agent_disabled_skips(self) -> None:
        """Test when platform agent disabled, skips execution."""
        from orchestrator.langgraph.nodes_platform import run_platform_special_agent

        state = GraphState(
            job_id="job-1",
            incident_id="inc-1",
            merged_findings={"domain_count": 1, "domains": ["observability"]},
        )
        cfg = OrchestratorConfig()
        runtime = mock.MagicMock()

        with mock.patch.dict(os.environ, {
            "RCA_ROUTE_AGENT_ENABLED": "true",
            "RCA_PLATFORM_SPECIAL_AGENT_ENABLED": "false",
        }):
            result = run_platform_special_agent(state, cfg, runtime)

        # Should not set platform_special_patch when disabled
        self.assertEqual(result.platform_special_patch, {})

    def test_platform_agent_route_disabled_skips(self) -> None:
        """Test when route agent disabled, skips platform agent."""
        from orchestrator.langgraph.nodes_platform import run_platform_special_agent

        state = GraphState(
            job_id="job-1",
            incident_id="inc-1",
        )
        cfg = OrchestratorConfig()
        runtime = mock.MagicMock()

        with mock.patch.dict(os.environ, {
            "RCA_ROUTE_AGENT_ENABLED": "false",
            "RCA_PLATFORM_SPECIAL_AGENT_ENABLED": "true",
        }):
            result = run_platform_special_agent(state, cfg, runtime)

        # Should not set platform_special_patch when route disabled
        self.assertEqual(result.platform_special_patch, {})

    def test_platform_agent_no_llm_adds_degrade_reason(self) -> None:
        """Test when no LLM configured, adds degrade reason."""
        from orchestrator.langgraph.nodes_platform import run_platform_special_agent

        state = GraphState(
            job_id="job-1",
            incident_id="inc-1",
            merged_findings={"domain_count": 1, "domains": ["observability"]},
        )
        cfg = OrchestratorConfig()
        runtime = mock.MagicMock()

        with mock.patch.dict(os.environ, {
            "RCA_ROUTE_AGENT_ENABLED": "true",
            "RCA_PLATFORM_SPECIAL_AGENT_ENABLED": "true",
        }):
            with mock.patch(
                "orchestrator.langgraph.nodes_platform._get_llm", return_value=None
            ):
                result = run_platform_special_agent(state, cfg, runtime)

        self.assertTrue(any("llm_not_configured" in r for r in result.degrade_reasons))


class TestSummarizeDiagnosisAgentized(unittest.TestCase):
    """Tests for summarize_diagnosis_agentized node function."""

    def test_agentized_merges_domain_patch(self) -> None:
        """Test that domain diagnosis patch is merged."""
        from orchestrator.langgraph.nodes import summarize_diagnosis_agentized

        state = GraphState(
            job_id="job-1",
            incident_id="inc-1",
            evidence_ids=["ev-1"],
            merged_findings={
                "domain_count": 1,
                "domains": ["observability"],
                "diagnosis_patch": {
                    "summary": "Domain summary",
                    "root_cause": {"statement": "Domain root cause"},
                },
            },
        )
        cfg = OrchestratorConfig()
        runtime = mock.MagicMock()

        result = summarize_diagnosis_agentized(state, cfg, runtime)

        self.assertIsInstance(result.diagnosis_json, dict)
        self.assertIn("summary", result.diagnosis_json)

    def test_agentized_merges_platform_patch(self) -> None:
        """Test that platform special patch is merged."""
        from orchestrator.langgraph.nodes import summarize_diagnosis_agentized

        state = GraphState(
            job_id="job-1",
            incident_id="inc-1",
            evidence_ids=["ev-1"],
            platform_special_patch={
                "summary": "Platform summary",
                "root_cause": {"confidence": 0.9},
            },
        )
        cfg = OrchestratorConfig()
        runtime = mock.MagicMock()

        result = summarize_diagnosis_agentized(state, cfg, runtime)

        self.assertIsInstance(result.diagnosis_json, dict)
        # Platform patch should have been merged
        self.assertEqual(result.diagnosis_json.get("summary"), "Platform summary")

    def test_agentized_merges_platform_confidence(self) -> None:
        """Test that platform confidence is properly merged into diagnosis."""
        from orchestrator.langgraph.nodes import summarize_diagnosis_agentized

        state = GraphState(
            job_id="job-1",
            incident_id="inc-1",
            evidence_ids=["ev-1"],
            platform_special_patch={
                "root_cause": {"confidence": 0.92},
            },
        )
        cfg = OrchestratorConfig()
        runtime = mock.MagicMock()

        result = summarize_diagnosis_agentized(state, cfg, runtime)

        self.assertIsInstance(result.diagnosis_json, dict)
        # Confidence from platform patch should be merged
        root_cause = result.diagnosis_json.get("root_cause", {})
        self.assertEqual(root_cause.get("confidence"), 0.92)

    def test_agentized_clamps_invalid_confidence(self) -> None:
        """Test that out-of-range confidence is clamped during merge."""
        from orchestrator.langgraph.nodes import summarize_diagnosis_agentized

        state = GraphState(
            job_id="job-1",
            incident_id="inc-1",
            evidence_ids=["ev-1"],
            platform_special_patch={
                "root_cause": {"confidence": 1.5},  # Over 1.0
            },
        )
        cfg = OrchestratorConfig()
        runtime = mock.MagicMock()

        result = summarize_diagnosis_agentized(state, cfg, runtime)

        root_cause = result.diagnosis_json.get("root_cause", {})
        # Should be clamped to 1.0
        self.assertEqual(root_cause.get("confidence"), 1.0)

    def test_agentized_raises_on_missing_incident_id(self) -> None:
        """Test that missing incident_id raises error."""
        from orchestrator.langgraph.nodes import summarize_diagnosis_agentized

        state = GraphState(job_id="job-1", evidence_ids=["ev-1"])
        cfg = OrchestratorConfig()
        runtime = mock.MagicMock()

        with self.assertRaises(RuntimeError) as ctx:
            summarize_diagnosis_agentized(state, cfg, runtime)

        self.assertIn("incident_id", str(ctx.exception))

    def test_agentized_raises_on_missing_evidence_ids(self) -> None:
        """Test that missing evidence_ids raises error."""
        from orchestrator.langgraph.nodes import summarize_diagnosis_agentized

        state = GraphState(job_id="job-1", incident_id="inc-1")
        cfg = OrchestratorConfig()
        runtime = mock.MagicMock()

        with self.assertRaises(RuntimeError) as ctx:
            summarize_diagnosis_agentized(state, cfg, runtime)

        self.assertIn("evidence_ids", str(ctx.exception))


if __name__ == "__main__":
    unittest.main()