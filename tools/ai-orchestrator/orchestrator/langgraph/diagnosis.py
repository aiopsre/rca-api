from __future__ import annotations

from datetime import datetime, timezone
from typing import Any

from ..state import GraphState


def diagnosis_timestamp() -> str:
    return datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def build_success_diagnosis(state: GraphState) -> dict[str, Any]:
    evidence_ids = state.evidence_ids[:]
    primary_evidence = evidence_ids[0] if evidence_ids else ""
    return {
        "schema_version": "1.0",
        "generated_at": diagnosis_timestamp(),
        "incident_id": state.incident_id,
        "summary": "Suspected root cause based on consistent available evidence.",
        "root_cause": {
            "type": "unknown",
            "category": "app",
            "summary": "Suspected service-side issue based on consistent evidence.",
            "statement": "Metrics and logs indicate correlated service degradation in the same window.",
            "confidence": 0.65,
            "evidence_ids": evidence_ids,
        },
        "timeline": [
            {
                "t": diagnosis_timestamp(),
                "event": "evidence_collected",
                "ref": primary_evidence,
            }
        ],
        "observations": [
            {
                "title": "Evidence collected",
                "detail": "Metrics and logs are both available and consistent in the selected time window.",
            }
        ],
        "hypotheses": [
            {
                "statement": "Service-side regression likely contributed to elevated error rate.",
                "confidence": 0.55,
                "supporting_evidence_ids": evidence_ids,
                "missing_evidence": ["traces"],
            }
        ],
        "recommendations": [
            {
                "type": "readonly_check",
                "action": "Confirm hypothesis with traces or deployment diff for the same window.",
                "risk": "low",
            }
        ],
        "unknowns": ["Detailed upstream dependency impact requires traces."],
        "next_steps": ["Collect trace sample for top failing endpoint."],
    }


def build_missing_evidence_diagnosis(state: GraphState) -> dict[str, Any]:
    primary_evidence = state.evidence_ids[0]
    missing_evidence = (state.missing_evidence or ["logs", "traces"])[:20]
    return {
        "schema_version": "1.0",
        "generated_at": diagnosis_timestamp(),
        "incident_id": state.incident_id,
        "summary": "Insufficient evidence to determine root cause.",
        "root_cause": {
            "type": "missing_evidence",
            "category": "unknown",
            "summary": "Insufficient evidence to determine root cause.",
            "statement": "",
            "confidence": 0.15,
            "evidence_ids": [primary_evidence],
        },
        "missing_evidence": missing_evidence,
        "timeline": [
            {
                "t": diagnosis_timestamp(),
                "event": "evidence_gap_detected",
                "ref": primary_evidence,
            }
        ],
        "observations": [
            {
                "title": "Evidence gap",
                "detail": "Logs/traces were not available or not found in the query window.",
            }
        ],
        "hypotheses": [
            {
                "statement": "Evidence gap prevents confident root-cause attribution.",
                "confidence": 0.15,
                "supporting_evidence_ids": [primary_evidence],
                "missing_evidence": missing_evidence,
            }
        ],
        "recommendations": [
            {
                "type": "readonly_check",
                "action": "Expand time window and collect logs/traces before concluding.",
                "risk": "low",
            }
        ],
        "unknowns": ["Root cause remains unknown because critical evidence is missing."],
        "next_steps": ["Re-run RCA after logs and traces become available."],
    }


def build_conflict_evidence_diagnosis(state: GraphState) -> dict[str, Any]:
    evidence_ids = state.evidence_ids[:]
    if len(evidence_ids) > 2:
        evidence_ids = evidence_ids[:2]

    missing_evidence = (
        state.missing_evidence
        or [
            "align metrics/logs/traces time window and re-query within the same interval",
            "collect error logs (5xx/timeout/panic/OOM) during the metric spike",
            "collect upstream/downstream traces or confirm tracing sampling/drop",
        ]
    )[:20]
    return {
        "schema_version": "1.0",
        "generated_at": diagnosis_timestamp(),
        "incident_id": state.incident_id,
        "summary": "Evidence signals conflict: metrics indicate degradation while logs/traces do not corroborate within the same window.",
        "root_cause": {
            "type": "conflict_evidence",
            "category": "unknown",
            "summary": "metrics vs logs/traces conflict within the same window",
            "statement": "",
            "confidence": 0.25,
            "evidence_ids": evidence_ids,
        },
        "missing_evidence": missing_evidence,
        "timeline": [
            {
                "t": diagnosis_timestamp(),
                "event": "conflict_evidence_detected",
                "ref": evidence_ids[0] if evidence_ids else "",
            }
        ],
        "observations": [
            {
                "title": "Conflicting signals",
                "detail": "Metrics and logs/traces are inconsistent; avoid high-confidence conclusion.",
            }
        ],
        "hypotheses": [
            {
                "statement": "Current evidence is conflicting and insufficient for a decisive root cause.",
                "confidence": 0.25,
                "supporting_evidence_ids": evidence_ids,
                "missing_evidence": missing_evidence,
            }
        ],
        "recommendations": [
            {
                "type": "readonly_check",
                "action": "Re-run collection with aligned time windows and add trace/log datasource coverage.",
                "risk": "low",
            }
        ],
        "unknowns": ["Root cause remains uncertain due to evidence conflict."],
        "next_steps": ["Collect corroborating logs/traces in the same interval as metric anomalies."],
    }
