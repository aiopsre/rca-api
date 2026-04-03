"""End-to-end tests for Minimal Closed Loop against local compose environment.

These tests require a running compose environment with:
- docker compose -f deploy/compose/docker-compose.redis.yaml --profile mock up -d

Tests cover the 7 core MCL steps from the curl runbook:
1. Create notice channel
2. Ingest alert event
3. Trigger AI job
4. Poll job to terminal state
5. Read session context
6. View tool call audit
7. View diagnosis writeback
8. Poll notice delivery
"""
from __future__ import annotations

import json
import os
import time
from typing import Any

import pytest
import requests


# Configuration from environment
BASE_URL = os.environ.get("BASE_URL", "http://127.0.0.1:5555")
SCOPES = os.environ.get("SCOPES", "*")
ORCH_INSTANCE_ID = os.environ.get("ORCH_INSTANCE_ID", "mcl-e2e-test")

HEADERS = {
    "Accept": "application/json",
    "Content-Type": "application/json",
    "X-Scopes": SCOPES,
}


def _extract_field(response_json: dict[str, Any], *paths: str) -> str:
    """Extract field from response using multiple possible paths."""
    for path in paths:
        parts = path.split(".")
        value = response_json
        try:
            for part in parts:
                if part.isdigit():
                    value = value[int(part)]
                else:
                    value = value[part]
            if value is not None and str(value).strip():
                return str(value).strip()
        except (KeyError, TypeError, IndexError):
            continue
    return ""


def _wait_for_health(base_url: str, timeout: int = 60) -> bool:
    """Wait for API server to be healthy."""
    for _ in range(timeout):
        try:
            resp = requests.get(f"{base_url}/healthz", timeout=5)
            if resp.status_code == 200:
                return True
        except requests.RequestException:
            pass
        time.sleep(1)
    return False


@pytest.fixture(scope="module")
def ensure_environment():
    """Ensure the compose environment is healthy before running tests."""
    if not _wait_for_health(BASE_URL):
        pytest.skip(
            f"API server not healthy at {BASE_URL}. "
            "Start compose: docker compose -f deploy/compose/docker-compose.redis.yaml --profile mock up -d"
        )


@pytest.fixture(scope="module")
def mcl_context(ensure_environment) -> dict[str, str]:
    """Shared test context with IDs from MCL flow.

    Executes the full MCL flow and returns the context for individual test verification.
    """
    context: dict[str, str] = {}
    rand = str(int(time.time()) % 100000)

    # Step 1: Create notice channel
    channel_body = {
        "name": f"mcl-e2e-channel-{rand}",
        "type": "webhook",
        "enabled": True,
        "endpointURL": "http://mock-webhook:8080/mcl",
        "timeoutMs": 1000,
        "maxRetries": 2,
    }
    resp = requests.post(
        f"{BASE_URL}/v1/notice-channels",
        headers=HEADERS,
        json=channel_body,
    )
    assert resp.status_code == 200, f"Failed to create notice channel: {resp.text}"
    channel_id = _extract_field(resp.json(), "noticeChannel.channelID", "channelID", "data.noticeChannel.channelID", "data.channelID")
    assert channel_id, f"No channelID in response: {resp.json()}"
    context["channel_id"] = channel_id

    # Step 2: Ingest alert event
    now_epoch = int(time.time())
    ingest_body = {
        "idempotencyKey": f"idem-mcl-e2e-{rand}",
        "fingerprint": f"mcl-e2e-fp-{rand}",
        "status": "firing",
        "severity": "P1",
        "service": "mcl-e2e-svc",
        "cluster": "mcl-e2e-cluster",
        "namespace": "default",
        "workload": "mcl-e2e-workload",
        "labelsJSON": json.dumps({"alertname": "MCLE2ETest", "service": "mcl-e2e-svc"}),
        "lastSeenAt": {"seconds": now_epoch, "nanos": 0},
    }
    resp = requests.post(
        f"{BASE_URL}/v1/alert-events:ingest",
        headers=HEADERS,
        json=ingest_body,
    )
    assert resp.status_code == 200, f"Failed to ingest alert event: {resp.text}"
    incident_id = _extract_field(resp.json(), "incidentID", "incident_id", "data.incidentID", "data.incident_id")
    event_id = _extract_field(resp.json(), "eventID", "event_id", "data.eventID", "data.event_id")
    assert incident_id, f"No incidentID in response: {resp.json()}"
    context["incident_id"] = incident_id
    context["event_id"] = event_id

    # Step 3: Trigger AI job
    start_epoch = now_epoch - 1800
    run_body = {
        "incidentID": incident_id,
        "idempotencyKey": f"idem-mcl-e2e-run-{rand}",
        "pipeline": "basic_rca",
        "trigger": "manual",
        "timeRangeStart": {"seconds": start_epoch, "nanos": 0},
        "timeRangeEnd": {"seconds": now_epoch, "nanos": 0},
        "inputHintsJSON": json.dumps({"scenario": "MCL_E2E", "event_id": event_id}),
        "createdBy": "system",
    }
    resp = requests.post(
        f"{BASE_URL}/v1/incidents/{incident_id}/ai:run",
        headers=HEADERS,
        json=run_body,
    )
    assert resp.status_code == 200, f"Failed to trigger AI job: {resp.text}"
    job_id = _extract_field(resp.json(), "jobID", "job_id", "data.jobID", "data.job_id")
    assert job_id, f"No jobID in response: {resp.json()}"
    context["job_id"] = job_id

    # Step 4: Poll job to terminal state
    job_status = ""
    for _ in range(120):
        resp = requests.get(
            f"{BASE_URL}/v1/ai/jobs/{job_id}",
            headers=HEADERS,
        )
        if resp.status_code == 200:
            job_status = _extract_field(resp.json(), "job.status", "status", "data.job.status", "data.status")
            if job_status in ("succeeded", "failed", "canceled"):
                break
        time.sleep(1)
    context["job_status"] = job_status

    # Step 5: Read session context
    session_headers = {**HEADERS, "X-Orchestrator-Instance-ID": ORCH_INSTANCE_ID}
    resp = requests.get(
        f"{BASE_URL}/v1/ai/jobs/{job_id}/session-context",
        headers=session_headers,
    )
    if resp.status_code == 200:
        session_id = _extract_field(resp.json(), "session_id", "sessionID", "data.session_id", "data.sessionID")
        context["session_id"] = session_id

    # Step 6: View tool call audit
    resp = requests.get(
        f"{BASE_URL}/v1/ai/jobs/{job_id}/tool-calls?offset=0&limit=50",
        headers=HEADERS,
    )
    if resp.status_code == 200:
        tool_calls = _extract_field(resp.json(), "toolCalls", "data.toolCalls")
        if isinstance(tool_calls, list):
            context["tool_call_count"] = str(len(tool_calls))
        else:
            data = resp.json().get("data", {})
            tc_list = data.get("toolCalls", [])
            context["tool_call_count"] = str(len(tc_list))

    # Step 7: View diagnosis writeback
    resp = requests.get(
        f"{BASE_URL}/v1/incidents/{incident_id}",
        headers=HEADERS,
    )
    if resp.status_code == 200:
        incident = resp.json().get("incident", resp.json())
        context["rca_status"] = incident.get("rcaStatus", incident.get("rca_status", ""))
        context["root_cause_summary"] = incident.get("rootCauseSummary", incident.get("root_cause_summary", ""))

    # Step 8: Poll notice delivery
    for _ in range(60):
        resp = requests.get(
            f"{BASE_URL}/v1/notice-deliveries?incident_id={incident_id}&channel_id={channel_id}&event_type=diagnosis_written",
            headers=HEADERS,
        )
        if resp.status_code == 200:
            deliveries = resp.json().get("noticeDeliveries", [])
            if deliveries and deliveries[0].get("status") == "succeeded":
                context["notice_status"] = "succeeded"
                break
        time.sleep(1)
    else:
        context["notice_status"] = "timeout"

    return context


class TestMCLEndToEnd:
    """E2E tests covering the 7 core MCL steps."""

    def test_01_notice_channel_created(self, mcl_context: dict[str, str]) -> None:
        """Verify notice channel was created."""
        assert "channel_id" in mcl_context
        assert mcl_context["channel_id"]

    def test_02_incident_created(self, mcl_context: dict[str, str]) -> None:
        """Verify incident was created from alert ingest."""
        assert "incident_id" in mcl_context
        assert mcl_context["incident_id"]

    def test_03_ai_job_triggered(self, mcl_context: dict[str, str]) -> None:
        """Verify AI job was triggered."""
        assert "job_id" in mcl_context
        assert mcl_context["job_id"]

    def test_04_job_succeeded(self, mcl_context: dict[str, str]) -> None:
        """Verify job reached succeeded state."""
        assert mcl_context.get("job_status") == "succeeded", f"Job status: {mcl_context.get('job_status')}"

    def test_05_session_context_available(self, mcl_context: dict[str, str]) -> None:
        """Verify session context is available."""
        assert "session_id" in mcl_context
        assert mcl_context["session_id"]

    def test_06_tool_calls_recorded(self, mcl_context: dict[str, str]) -> None:
        """Verify tool calls were recorded for audit."""
        tool_call_count = int(mcl_context.get("tool_call_count", "0"))
        assert tool_call_count >= 1, f"Tool call count: {tool_call_count}"

    def test_07_diagnosis_writeback(self, mcl_context: dict[str, str]) -> None:
        """Verify diagnosis was written back to incident."""
        assert mcl_context.get("rca_status") == "done", f"RCA status: {mcl_context.get('rca_status')}"
        assert mcl_context.get("root_cause_summary"), "No root cause summary"

    def test_08_notice_delivered(self, mcl_context: dict[str, str]) -> None:
        """Verify notice was delivered."""
        assert mcl_context.get("notice_status") == "succeeded", f"Notice status: {mcl_context.get('notice_status')}"


class TestMCLJobDetails:
    """Additional verification of job details."""

    def test_job_has_incident_binding(self, mcl_context: dict[str, str]) -> None:
        """Verify job is bound to the correct incident."""
        job_id = mcl_context["job_id"]
        incident_id = mcl_context["incident_id"]

        resp = requests.get(f"{BASE_URL}/v1/ai/jobs/{job_id}", headers=HEADERS)
        assert resp.status_code == 200

        job = resp.json().get("job", resp.json())
        job_incident_id = job.get("incidentID", job.get("incident_id", ""))
        assert job_incident_id == incident_id

    def test_tool_calls_contain_key_nodes(self, mcl_context: dict[str, str]) -> None:
        """Verify tool calls contain expected node names."""
        job_id = mcl_context["job_id"]

        resp = requests.get(
            f"{BASE_URL}/v1/ai/jobs/{job_id}/tool-calls?offset=0&limit=50",
            headers=HEADERS,
        )
        assert resp.status_code == 200

        data = resp.json()
        tool_calls = data.get("toolCalls", data.get("data", {}).get("toolCalls", []))

        node_names = {tc.get("nodeName", "") for tc in tool_calls if isinstance(tc, dict)}

        # Runbook 5.1.3 check #3: should contain route_domains
        assert "route_domains" in node_names, f"Missing route_domains. Found: {node_names}"

        # Should have at least one of observability or platform_special
        has_analysis = "run_observability_agent" in node_names or "run_platform_special_agent" in node_names
        assert has_analysis, f"Missing analysis nodes. Found: {node_names}"


class TestMCLIncidentDetails:
    """Additional verification of incident diagnosis details."""

    def test_diagnosis_json_valid(self, mcl_context: dict[str, str]) -> None:
        """Verify diagnosis JSON has expected structure."""
        incident_id = mcl_context["incident_id"]

        resp = requests.get(f"{BASE_URL}/v1/incidents/{incident_id}", headers=HEADERS)
        assert resp.status_code == 200

        incident = resp.json().get("incident", resp.json())
        diagnosis_json_str = incident.get("diagnosisJSON", incident.get("diagnosis_json", ""))

        if diagnosis_json_str:
            diagnosis = json.loads(diagnosis_json_str)

            # Should have root_cause with confidence
            root_cause = diagnosis.get("root_cause", {})
            assert "confidence" in root_cause, f"No confidence in root_cause: {root_cause}"

    def test_evidence_refs_present(self, mcl_context: dict[str, str]) -> None:
        """Verify evidence references are present."""
        incident_id = mcl_context["incident_id"]

        resp = requests.get(f"{BASE_URL}/v1/incidents/{incident_id}", headers=HEADERS)
        assert resp.status_code == 200

        incident = resp.json().get("incident", resp.json())
        evidence_refs_str = incident.get("evidenceRefsJSON", incident.get("evidence_refs_json", ""))

        if evidence_refs_str:
            evidence_refs = json.loads(evidence_refs_str)
            evidence_ids = evidence_refs.get("evidence_ids", [])
            assert len(evidence_ids) >= 1, f"No evidence_ids: {evidence_refs}"


class TestMCLNginxSlowRequest:
    """MCL tests with Nginx slow request sample from runbook 4.10."""

    @pytest.fixture
    def nginx_context(self, ensure_environment) -> dict[str, str]:
        """Execute MCL with Nginx slow request sample."""
        context: dict[str, str] = {}
        rand = str(int(time.time()) % 100000)
        now_epoch = int(time.time())

        # Ingest Nginx slow request alert
        ingest_body = {
            "idempotencyKey": f"idem-mcl-nginx-{rand}",
            "fingerprint": f"es:ingress_slow:seller.tour.qlcd.com:/api/v1/seller/buyer_vendor/get_list_pc:172.17.53.107:8080",
            "status": "firing",
            "severity": "warning",
            "service": "seller.tour.qlcd.com",
            "cluster": "prod",
            "namespace": "ingress-apisix",
            "workload": "apisix",
            "labelsJSON": json.dumps({
                "alertname": "NginxIngressSlowSpike",
                "service": "seller.tour.qlcd.com",
            }),
            "annotationsJSON": json.dumps({
                "summary": "Nginx ingress slow request",
                "request_time_seconds": "4.364",
                "upstream_response_time_seconds": "4.363",
                "trace_id": "6711023160f3e6095965bc48687c8548",
            }),
            "rawEventJSON": json.dumps({
                "message": (
                    '- 10.1.6.44 "-" - [25/Mar/2026:22:07:35 +0800] seller.tour.qlcd.com '
                    '"POST /api/v1/seller/buyer_vendor/get_list_pc HTTP/2.0" 200 8850'
                ),
            }),
        }

        resp = requests.post(
            f"{BASE_URL}/v1/alert-events:ingest",
            headers=HEADERS,
            json=ingest_body,
        )
        assert resp.status_code == 200, f"Failed to ingest: {resp.text}"

        incident_id = _extract_field(resp.json(), "incidentID", "incident_id", "data.incidentID")
        assert incident_id
        context["incident_id"] = incident_id

        # Trigger AI job
        run_body = {
            "incidentID": incident_id,
            "idempotencyKey": f"idem-mcl-nginx-run-{rand}",
            "pipeline": "basic_rca",
            "trigger": "manual",
            "timeRangeStart": {"seconds": now_epoch - 1800, "nanos": 0},
            "timeRangeEnd": {"seconds": now_epoch, "nanos": 0},
            "inputHintsJSON": json.dumps({"scenario": "NginxSlowRequest"}),
            "createdBy": "system",
        }

        resp = requests.post(
            f"{BASE_URL}/v1/incidents/{incident_id}/ai:run",
            headers=HEADERS,
            json=run_body,
        )
        assert resp.status_code == 200

        job_id = _extract_field(resp.json(), "jobID", "job_id", "data.jobID")
        context["job_id"] = job_id

        # Poll to terminal
        for _ in range(120):
            resp = requests.get(f"{BASE_URL}/v1/ai/jobs/{job_id}", headers=HEADERS)
            if resp.status_code == 200:
                status = _extract_field(resp.json(), "job.status", "status", "data.job.status")
                if status in ("succeeded", "failed", "canceled"):
                    context["job_status"] = status
                    break
            time.sleep(1)

        return context

    def test_nginx_slow_request_job_succeeded(self, nginx_context: dict[str, str]) -> None:
        """Verify Nginx slow request MCL job succeeded."""
        assert nginx_context.get("job_status") == "succeeded"

    def test_nginx_slow_request_has_diagnosis(self, nginx_context: dict[str, str]) -> None:
        """Verify Nginx slow request has diagnosis."""
        incident_id = nginx_context["incident_id"]

        resp = requests.get(f"{BASE_URL}/v1/incidents/{incident_id}", headers=HEADERS)
        assert resp.status_code == 200

        incident = resp.json().get("incident", resp.json())
        assert incident.get("rcaStatus") == "done"
        assert incident.get("rootCauseSummary")


if __name__ == "__main__":
    pytest.main([__file__, "-v"])