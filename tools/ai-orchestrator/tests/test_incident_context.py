from __future__ import annotations

import json
import pathlib
import sys
import unittest
from unittest import mock


TESTS_DIR = pathlib.Path(__file__).resolve().parent
PROJECT_DIR = TESTS_DIR.parent
if str(PROJECT_DIR) not in sys.path:
    sys.path.insert(0, str(PROJECT_DIR))

from orchestrator.langgraph.helpers import (
    build_incident_context,
    render_alert_event_excerpt,
    select_current_alert_event,
)
from orchestrator.langgraph.nodes import load_job_and_start
from orchestrator.langgraph.nodes_agents import _build_observability_user_prompt
from orchestrator.langgraph.nodes_platform import _build_platform_special_user_prompt
from orchestrator.langgraph.nodes_router import _build_router_user_prompt
from orchestrator.langgraph.config import OrchestratorConfig
from orchestrator.state import GraphState


class IncidentContextTests(unittest.TestCase):
    def test_build_incident_context_merges_incident_alert(self) -> None:
        incident = {
            "incidentID": "incident-1",
            "service": "seller.tour.qlcd.com",
            "namespace": "ingress-apisix",
            "severity": "P1",
            "cluster": "prod",
            "workloadName": "apisix",
            "alertName": "NginxIngressSlowSpike",
            "fingerprint": "fp-1",
            "status": "investigating",
            "rcaStatus": "running",
            "rootCauseSummary": "pending",
        }
        alert_event = {
            "eventID": "event-1",
            "incidentID": "incident-1",
            "fingerprint": "fp-1",
            "alertName": "NginxIngressSlowSpike",
            "service": "seller.tour.qlcd.com",
            "namespace": "ingress-apisix",
            "cluster": "prod",
            "workload": "apisix-99b644497-xwbcj",
            "status": "firing",
            "severity": "warning",
            "isCurrent": True,
            "lastSeenAt": "2026-03-26T02:17:35Z",
            "rawEventJSON": json.dumps(
                {
                    "message": "slow nginx request",
                    "http.request.method": "POST",
                    "http.request.uri_path": "/api/v1/seller/buyer_vendor/get_list_pc",
                    "http.response.status_code": "200",
                    "nginx.request.time": 4.364,
                    "nginx.upstream.response.time": 4.363,
                    "nginx.upstream.address": "172.17.53.107:8080",
                    "Trace.Id": "6711023160f3e6095965bc48687c8548",
                    "Trace.SpanId": "a4d0dfc0f1ad78ae",
                    "user_agent.request_id": "Yms_q7ZGDQ6fmxaqzz4Wx",
                }
            ),
        }

        context = build_incident_context(incident, alert_event)

        self.assertEqual(context["service"], "seller.tour.qlcd.com")
        self.assertEqual(context["namespace"], "ingress-apisix")
        self.assertEqual(context["severity"], "P1")
        self.assertEqual(context["alert_name"], "NginxIngressSlowSpike")
        self.assertEqual(context["alert_event_id"], "event-1")
        self.assertNotIn("raw_event_summary", context)

    def test_render_alert_event_excerpt_uses_raw_payload_without_parsing(self) -> None:
        alert_event = {
            "eventID": "event-1",
            "rawEventJSON": json.dumps(
                {
                    "message": "slow nginx request",
                    "http.request.method": "POST",
                    "http.request.uri_path": "/api/v1/seller/buyer_vendor/get_list_pc",
                    "http.response.status_code": "200",
                }
            ),
        }

        excerpt = render_alert_event_excerpt(alert_event, max_len=256)

        self.assertIn("http.request.uri_path", excerpt)
        self.assertIn("/api/v1/seller/buyer_vendor/get_list_pc", excerpt)

    def test_select_current_alert_event_prefers_matching_fingerprint(self) -> None:
        incident = {"incidentID": "incident-1", "fingerprint": "fp-1"}
        selected = select_current_alert_event(
            {
                "events": [
                    {"eventID": "event-2", "fingerprint": "fp-2", "isCurrent": True},
                    {"eventID": "event-1", "fingerprint": "fp-1", "incidentID": "incident-1", "isCurrent": True},
                ]
            },
            incident,
        )

        self.assertEqual(selected["eventID"], "event-1")

    def test_load_job_and_start_populates_full_records(self) -> None:
        state = GraphState(job_id="job-1")
        cfg = OrchestratorConfig()
        runtime = mock.MagicMock()
        runtime.get_job.return_value = {"jobID": "job-1", "incidentID": "incident-1"}
        runtime.get_incident.return_value = {
            "incidentID": "incident-1",
            "service": "seller.tour.qlcd.com",
            "namespace": "ingress-apisix",
            "severity": "P1",
            "cluster": "prod",
            "workloadName": "apisix",
            "alertName": "NginxIngressSlowSpike",
            "fingerprint": "fp-1",
            "status": "investigating",
            "rcaStatus": "running",
        }
        runtime.list_alert_events_current.return_value = {
            "events": [
                {
                    "eventID": "event-1",
                    "incidentID": "incident-1",
                    "fingerprint": "fp-1",
                    "alertName": "NginxIngressSlowSpike",
                    "service": "seller.tour.qlcd.com",
                    "namespace": "ingress-apisix",
                    "cluster": "prod",
                    "workload": "apisix-99b644497-xwbcj",
                    "status": "firing",
                    "severity": "warning",
                    "isCurrent": True,
                    "rawEventJSON": json.dumps(
                        {
                            "message": "slow nginx request",
                            "http.request.method": "POST",
                            "http.request.uri_path": "/api/v1/seller/buyer_vendor/get_list_pc",
                            "http.response.status_code": "200",
                            "nginx.request.time": 4.364,
                            "nginx.upstream.response.time": 4.363,
                            "Trace.Id": "6711023160f3e6095965bc48687c8548",
                        }
                    ),
                }
            ]
        }

        result = load_job_and_start(state, cfg, runtime)

        self.assertEqual(result.incident_record["incidentID"], "incident-1")
        self.assertEqual(result.alert_event_record["eventID"], "event-1")
        self.assertEqual(result.incident_context["service"], "seller.tour.qlcd.com")
        runtime.get_job.assert_called_once_with("job-1")
        runtime.get_incident.assert_called_once_with("incident-1")
        runtime.list_alert_events_current.assert_called_once()

    def test_prompt_builders_use_context_summary(self) -> None:
        state = GraphState(
            job_id="job-1",
            incident_id="incident-1",
            incident_context={
                "service": "seller.tour.qlcd.com",
                "namespace": "ingress-apisix",
                "severity": "P1",
                "alert_name": "NginxIngressSlowSpike",
                "fingerprint": "fp-1",
                "cluster": "prod",
            },
            alert_event_record={
                "eventID": "event-1",
                "rawEventJSON": json.dumps(
                    {
                        "message": "slow nginx request",
                        "http.request.method": "POST",
                        "http.request.uri_path": "/api/v1/seller/buyer_vendor/get_list_pc",
                        "http.response.status_code": "200",
                    }
                ),
            },
            merged_findings={"domain_count": 1, "domains": ["observability"]},
            evidence_ids=["evidence-1"],
        )

        router_prompt = _build_router_user_prompt(state)
        observability_prompt = _build_observability_user_prompt(state, {"goal": "Inspect slow request"})
        platform_prompt = _build_platform_special_user_prompt(state)

        self.assertIn("Fingerprint: fp-1", router_prompt)
        self.assertNotIn("Alert Event Payload:", router_prompt)
        self.assertIn("Alert Event Payload:", observability_prompt)
        self.assertIn("Alert Event Payload:", platform_prompt)
        self.assertIn("Alert: NginxIngressSlowSpike", platform_prompt)
        self.assertIn("Quality gate:", platform_prompt)

    def test_incident_context_has_flags(self) -> None:
        """Test has_* flags are correctly set."""
        incident = {
            "incidentID": "incident-1",
            "labelsJSON": '{"foo":"bar"}',
            "traceID": "abc123",
            "changeID": "change-123",
        }
        alert_event = {
            "eventID": "event-1",
            "rawEventJSON": json.dumps({"message": "test event"}),
        }

        context = build_incident_context(incident, alert_event)

        self.assertTrue(context["has_labels_json"])
        self.assertFalse(context["has_annotations_json"])
        self.assertTrue(context["has_raw_event"])
        self.assertTrue(context["has_trace_id"])
        self.assertTrue(context["has_change_id"])

    def test_incident_context_missing_fields(self) -> None:
        """Test flags are False when fields are missing."""
        incident = {"incidentID": "incident-1"}
        alert_event = {"eventID": "event-1"}

        context = build_incident_context(incident, alert_event)

        self.assertFalse(context.get("has_labels_json", False))
        self.assertFalse(context.get("has_annotations_json", False))
        self.assertFalse(context.get("has_raw_event", False))
        self.assertFalse(context.get("has_trace_id", False))
        self.assertFalse(context.get("has_change_id", False))

    def test_router_prompt_excludes_raw_payload(self) -> None:
        """Router should not receive raw alert payload."""
        from orchestrator.langgraph.prompt_context import build_router_prompt_context

        state = GraphState(
            job_id="job-1",
            incident_context={"service": "api"},
            alert_event_record={"rawEventJSON": '{"sensitive":"data"}'},
        )

        ctx = build_router_prompt_context(state)
        self.assertNotIn("raw_alert_excerpt", ctx)
        self.assertNotIn("alert_event_record", ctx)

    def test_observability_prompt_includes_raw_payload(self) -> None:
        """Observability should have access to raw alert payload."""
        from orchestrator.langgraph.prompt_context import build_observability_prompt_context

        state = GraphState(
            job_id="job-1",
            incident_context={"service": "api"},
            alert_event_record={"rawEventJSON": '{"http.request.uri_path":"/api/test"}'},
        )

        ctx = build_observability_prompt_context(state)
        self.assertIn("raw_alert_excerpt", ctx)
        self.assertIn("/api/test", ctx["raw_alert_excerpt"])

    def test_change_prompt_excludes_raw_payload(self) -> None:
        """Change agent should not receive raw alert payload."""
        from orchestrator.langgraph.prompt_context import build_change_prompt_context

        state = GraphState(
            job_id="job-1",
            incident_context={"service": "api", "start_at": "2026-03-26T00:00:00Z"},
            alert_event_record={"rawEventJSON": '{"sensitive":"data"}'},
        )

        ctx = build_change_prompt_context(state)
        self.assertNotIn("raw_alert_excerpt", ctx)
        self.assertIn("time_context", ctx)

    def test_knowledge_prompt_uses_search_hints(self) -> None:
        """Knowledge agent should use search_hints, not raw payload."""
        from orchestrator.langgraph.prompt_context import build_knowledge_prompt_context

        state = GraphState(
            job_id="job-1",
            incident_context={
                "service": "api",
                "alert_name": "HighErrorRate",
                "fingerprint": "fp-123",
                "root_cause_summary": "Database timeout",
            },
            alert_event_record={"rawEventJSON": '{"sensitive":"data"}'},
        )

        ctx = build_knowledge_prompt_context(state)
        self.assertNotIn("raw_alert_excerpt", ctx)
        self.assertIn("search_hints", ctx)
        self.assertEqual(ctx["search_hints"]["alert_name"], "HighErrorRate")
        self.assertEqual(ctx["search_hints"]["fingerprint"], "fp-123")

    def test_has_labels_checks_both_incident_and_alert(self) -> None:
        """has_labels_json should be True if either incident or alert has labels."""
        # Alert has labels, incident doesn't
        incident = {"incidentID": "incident-1"}
        alert_event = {
            "eventID": "event-1",
            "labelsJSON": '{"alert_label":"value"}',
        }
        context = build_incident_context(incident, alert_event)
        self.assertTrue(context["has_labels_json"])

        # Incident has labels, alert doesn't
        incident = {"incidentID": "incident-1", "labelsJSON": '{"incident_label":"value"}'}
        alert_event = {"eventID": "event-1"}
        context = build_incident_context(incident, alert_event)
        self.assertTrue(context["has_labels_json"])

    def test_has_trace_id_from_structured_fields_only(self) -> None:
        """has_trace_id should only check structured fields, not raw JSON."""
        # trace_id from incident structured field
        incident = {"incidentID": "incident-1", "traceID": "abc123"}
        alert_event = {"eventID": "event-1"}
        context = build_incident_context(incident, alert_event)
        self.assertTrue(context["has_trace_id"])

        # trace_id from alert event structured field
        incident = {"incidentID": "incident-1"}
        alert_event = {"eventID": "event-1", "traceID": "xyz789"}
        context = build_incident_context(incident, alert_event)
        self.assertTrue(context["has_trace_id"])

        # Trace in raw JSON should NOT set has_trace_id (no heuristic matching)
        incident = {"incidentID": "incident-1"}
        alert_event = {"eventID": "event-1", "rawEventJSON": '{"Trace.Id":"in-raw-only"}'}
        context = build_incident_context(incident, alert_event)
        self.assertFalse(context["has_trace_id"])

    def test_render_alert_event_excerpt_returns_empty_without_raw_event(self) -> None:
        """render_alert_event_excerpt should return empty string if rawEventJSON is missing."""
        alert_event = {
            "eventID": "event-1",
            "service": "api",
            "namespace": "default",
        }
        excerpt = render_alert_event_excerpt(alert_event)
        self.assertEqual(excerpt, "")

    def test_last_seen_at_fallback_to_alert(self) -> None:
        """last_seen_at should fallback to alert_last_seen_at if incident doesn't have it."""
        incident = {"incidentID": "incident-1"}
        alert_event = {
            "eventID": "event-1",
            "lastSeenAt": "2026-03-26T10:00:00Z",
        }
        context = build_incident_context(incident, alert_event)
        self.assertEqual(context["last_seen_at"], "2026-03-26T10:00:00Z")

        # Incident has its own last_seen_at
        incident = {"incidentID": "incident-1", "lastSeenAt": "2026-03-26T09:00:00Z"}
        alert_event = {"eventID": "event-1", "lastSeenAt": "2026-03-26T10:00:00Z"}
        context = build_incident_context(incident, alert_event)
        self.assertEqual(context["last_seen_at"], "2026-03-26T09:00:00Z")


if __name__ == "__main__":
    unittest.main()
