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

from orchestrator.langgraph.helpers import build_incident_context, select_current_alert_event
from orchestrator.langgraph.nodes import load_job_and_start
from orchestrator.langgraph.nodes_agents import _build_observability_user_prompt
from orchestrator.langgraph.nodes_platform import _build_platform_special_user_prompt
from orchestrator.langgraph.nodes_router import _build_router_user_prompt
from orchestrator.langgraph.config import OrchestratorConfig
from orchestrator.state import GraphState


class IncidentContextTests(unittest.TestCase):
    def test_build_incident_context_merges_incident_alert_and_raw_event_summary(self) -> None:
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
        self.assertIn("raw_event_summary", context)
        self.assertIn("path=/api/v1/seller/buyer_vendor/get_list_pc", context["raw_event_summary"])
        self.assertNotIn("raw_event_http_request_uri_path", context)
        self.assertNotIn("raw_event_trace_id", context)

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
        self.assertIn("raw_event_summary", result.incident_context)
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
                "raw_event_summary": "method=POST; path=/api/v1/seller/buyer_vendor/get_list_pc; status=200",
            },
            merged_findings={"domain_count": 1, "domains": ["observability"]},
            evidence_ids=["evidence-1"],
        )

        router_prompt = _build_router_user_prompt(state)
        observability_prompt = _build_observability_user_prompt(state, {"goal": "Inspect slow request"})
        platform_prompt = _build_platform_special_user_prompt(state)

        self.assertIn("Fingerprint: fp-1", router_prompt)
        self.assertIn("Raw Event: method=POST", router_prompt)
        self.assertIn("Raw Event: method=POST", observability_prompt)
        self.assertIn("Alert: NginxIngressSlowSpike", platform_prompt)
        self.assertIn("Quality gate:", platform_prompt)


if __name__ == "__main__":
    unittest.main()
