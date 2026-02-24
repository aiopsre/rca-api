from __future__ import annotations

from typing import Any


def _resource_ids(ctx: dict[str, Any]) -> list[str]:
    raw = ctx.get("skill_resources")
    if not isinstance(raw, list):
        return []
    out: list[str] = []
    for item in raw:
        if not isinstance(item, dict):
            continue
        resource_id = str(item.get("resource_id") or "").strip()
        if resource_id:
            out.append(resource_id)
    return out


def run(input_payload: dict[str, Any], ctx: dict[str, Any]) -> dict[str, Any]:
    diagnosis_json = input_payload.get("diagnosis_json")
    if not isinstance(diagnosis_json, dict):
        diagnosis_json = {}
    root_cause = diagnosis_json.get("root_cause")
    if not isinstance(root_cause, dict):
        root_cause = {}

    incident_context = input_payload.get("incident_context")
    if not isinstance(incident_context, dict):
        incident_context = {}
    service = str(incident_context.get("service") or "the affected service").strip()
    quality_gate = str(input_payload.get("quality_gate_decision") or "unknown").strip().lower()

    if quality_gate == "success":
        summary = f"Script executor tightened the diagnosis for {service} using the current quality-gated evidence."
        statement = f"Available metrics and logs indicate a service-side degradation window for {service} that needs operator confirmation."
    elif quality_gate == "missing":
        summary = f"Script executor reframed the diagnosis for {service} around missing evidence."
        statement = f"Current signals suggest {service} degradation, but the missing evidence list is still too large for a strong causal claim."
    else:
        summary = f"Script executor rewrote the diagnosis for {service} with conservative operator wording."
        statement = f"Correlated signals around {service} point to degradation, but the evidence remains mixed and should be verified further."

    return {
        "payload": {
            "diagnosis_patch": {
                "summary": summary,
                "root_cause": {
                    "summary": f"Script executor summary for {service}",
                    "statement": statement,
                },
                "recommendations": [
                    f"Review recent changes and service-side errors for {service} in the affected window."
                ],
                "unknowns": [
                    "Trace-level correlation is still missing for the most affected requests."
                ],
                "next_steps": [
                    "Collect request traces and compare them with deployment and config change events."
                ],
            }
        },
        "session_patch": {
            "latest_summary": {
                "summary": summary,
            },
            "context_state_patch": {
                "skills": {
                    "diagnosis_script_enrich": {
                        "applied": True,
                        "mode": "script",
                        "resource_ids": _resource_ids(ctx),
                    }
                }
            },
        },
        "observations": [
            {
                "kind": "note",
                "message": f"diagnosis script executor applied for quality_gate={quality_gate or 'unknown'}",
            }
        ],
    }
