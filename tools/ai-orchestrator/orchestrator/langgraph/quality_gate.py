from __future__ import annotations

from typing import Any

from ..state import GraphState
from .helpers import coerce_bool, ordered_unique_strings


QUALITY_GATE_PASS = "pass"
QUALITY_GATE_MISSING = "missing"
QUALITY_GATE_CONFLICT = "conflict"


def build_quality_gate_evidence_summary(state: GraphState) -> dict[str, Any]:
    sources: list[str] = []
    no_data = 0
    for item in state.evidence_meta:
        source = str(item.get("source") or "").strip()
        if source:
            sources.append(source)
        if coerce_bool(item.get("no_data")):
            no_data += 1

    if not sources and state.evidence_ids:
        sources = ["unknown"]

    return {
        "total": len(state.evidence_ids),
        "no_data": no_data,
        "sources": ordered_unique_strings(sources),
    }


def has_conflict_signal(state: GraphState, evidence_summary: dict[str, Any]) -> bool:
    for item in state.evidence_meta:
        if coerce_bool(item.get("conflict_hint")):
            return True

    total = int(evidence_summary.get("total") or 0)
    no_data = int(evidence_summary.get("no_data") or 0)
    sources = evidence_summary.get("sources")
    source_count = len(sources) if isinstance(sources, list) else 0
    return total >= 2 and no_data > 0 and no_data < total and source_count >= 2


def evaluate_quality_gate(state: GraphState) -> tuple[str, list[str], dict[str, Any]]:
    evidence_summary = build_quality_gate_evidence_summary(state)

    if state.force_conflict:
        reasons = ["FORCE_CONFLICT=1"]
        if state.force_no_evidence:
            reasons.append("FORCE_CONFLICT takes precedence over FORCE_NO_EVIDENCE when both are enabled")
        return QUALITY_GATE_CONFLICT, reasons, evidence_summary

    if state.force_no_evidence:
        return QUALITY_GATE_MISSING, ["FORCE_NO_EVIDENCE=1"], evidence_summary

    if int(evidence_summary.get("total") or 0) < 2:
        return QUALITY_GATE_MISSING, ["insufficient evidence: total evidence records < 2"], evidence_summary

    if int(evidence_summary.get("no_data") or 0) >= int(evidence_summary.get("total") or 0):
        return QUALITY_GATE_MISSING, ["all collected evidence are marked as no_data"], evidence_summary

    if has_conflict_signal(state, evidence_summary):
        return QUALITY_GATE_CONFLICT, ["conflicting evidence signals detected across collected sources"], evidence_summary

    return QUALITY_GATE_PASS, ["evidence is sufficient and consistent"], evidence_summary


def ensure_quality_gate(state: GraphState) -> dict[str, Any]:
    if state.quality_gate_decision and state.quality_gate_reasons:
        if not state.quality_gate_evidence_summary:
            state.quality_gate_evidence_summary = build_quality_gate_evidence_summary(state)
        return {
            "decision": state.quality_gate_decision,
            "reasons": state.quality_gate_reasons,
            "evidence_summary": state.quality_gate_evidence_summary,
        }

    decision, reasons, evidence_summary = evaluate_quality_gate(state)
    state.quality_gate_decision = decision
    state.quality_gate_reasons = reasons
    state.quality_gate_evidence_summary = evidence_summary
    return {
        "decision": decision,
        "reasons": reasons,
        "evidence_summary": evidence_summary,
    }
