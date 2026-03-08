from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Callable

from .validation import (
    ValidationError,
    ValidationErrorKind,
    ValidationResult,
    validate_capability_output,
)


def _trim(value: Any) -> str:
    return str(value or "").strip()


@dataclass(frozen=True)
class PromptSkillConsumeResult:
    payload: dict[str, Any] = field(default_factory=dict)
    session_patch: dict[str, Any] = field(default_factory=dict)
    observations: list[dict[str, Any]] = field(default_factory=list)


@dataclass(frozen=True)
class CapabilityDefinition:
    capability: str
    stage: str
    output_contract: dict[str, Any]
    build_input: Callable[[Any], dict[str, Any]]
    build_stage_summary: Callable[[dict[str, Any]], dict[str, Any]]
    sanitize_output: Callable[[PromptSkillConsumeResult], tuple[PromptSkillConsumeResult, list[str]]]
    apply_result: Callable[[Any, PromptSkillConsumeResult, Callable[[Any, dict[str, Any] | None], None]], None]


def _merge_mapping(base: dict[str, Any], patch: dict[str, Any]) -> dict[str, Any]:
    merged = dict(base)
    for key, value in patch.items():
        if isinstance(value, dict) and isinstance(merged.get(key), dict):
            merged[key] = _merge_mapping(merged[key], value)
            continue
        merged[key] = value
    return merged


def _sanitize_logs_branch_meta(value: Any) -> tuple[dict[str, Any] | None, list[str]]:
    if not isinstance(value, dict):
        return None, ["logs_branch_meta"]

    dropped: list[str] = []
    normalized: dict[str, Any] = {}

    mode = _trim(value.get("mode"))
    if mode == "query":
        normalized["mode"] = "query"
    else:
        return None, ["logs_branch_meta.mode"]

    query_type = _trim(value.get("query_type"))
    if query_type == "logs":
        normalized["query_type"] = "logs"
    else:
        return None, ["logs_branch_meta.query_type"]

    request_payload = value.get("request_payload")
    if not isinstance(request_payload, dict):
        return None, ["logs_branch_meta.request_payload"]
    query = _trim(request_payload.get("query"))
    if not query:
        return None, ["logs_branch_meta.request_payload.query"]
    normalized["request_payload"] = {"query": query}
    for key in sorted(set(request_payload.keys()) - {"query"}):
        dropped.append(f"logs_branch_meta.request_payload.{key}")

    query_request = value.get("query_request")
    if not isinstance(query_request, dict):
        return None, ["logs_branch_meta.query_request"]
    query_text = _trim(query_request.get("queryText") or query_request.get("query_text"))
    if not query_text:
        return None, ["logs_branch_meta.query_request.queryText"]
    normalized["query_request"] = {"queryText": query_text}
    for key in sorted(set(query_request.keys()) - {"queryText", "query_text"}):
        dropped.append(f"logs_branch_meta.query_request.{key}")

    for key in sorted(set(value.keys()) - {"mode", "query_type", "request_payload", "query_request"}):
        dropped.append(f"logs_branch_meta.{key}")

    return normalized, dropped


def _sanitize_metrics_branch_meta(value: Any) -> tuple[dict[str, Any] | None, list[str]]:
    if not isinstance(value, dict):
        return None, ["metrics_branch_meta"]

    dropped: list[str] = []
    normalized: dict[str, Any] = {}

    mode = _trim(value.get("mode"))
    if mode == "query":
        normalized["mode"] = "query"
    else:
        return None, ["metrics_branch_meta.mode"]

    query_type = _trim(value.get("query_type"))
    if query_type == "metrics":
        normalized["query_type"] = "metrics"
    else:
        return None, ["metrics_branch_meta.query_type"]

    request_payload = value.get("request_payload")
    if not isinstance(request_payload, dict):
        return None, ["metrics_branch_meta.request_payload"]
    promql = _trim(request_payload.get("promql"))
    if not promql:
        return None, ["metrics_branch_meta.request_payload.promql"]
    normalized_request_payload: dict[str, Any] = {"promql": promql}
    if "step_seconds" in request_payload:
        try:
            step_seconds = int(request_payload.get("step_seconds"))
        except (TypeError, ValueError):
            return None, ["metrics_branch_meta.request_payload.step_seconds"]
        if step_seconds <= 0:
            return None, ["metrics_branch_meta.request_payload.step_seconds"]
        normalized_request_payload["step_seconds"] = step_seconds
    normalized["request_payload"] = normalized_request_payload
    for key in sorted(set(request_payload.keys()) - {"promql", "step_seconds"}):
        dropped.append(f"metrics_branch_meta.request_payload.{key}")

    query_request = value.get("query_request")
    if not isinstance(query_request, dict):
        return None, ["metrics_branch_meta.query_request"]
    query_text = _trim(query_request.get("queryText") or query_request.get("query_text"))
    if not query_text:
        return None, ["metrics_branch_meta.query_request.queryText"]
    normalized["query_request"] = {"queryText": query_text}
    for key in sorted(set(query_request.keys()) - {"queryText", "query_text"}):
        dropped.append(f"metrics_branch_meta.query_request.{key}")

    for key in sorted(set(value.keys()) - {"mode", "query_type", "request_payload", "query_request"}):
        dropped.append(f"metrics_branch_meta.{key}")

    return normalized, dropped


def _sanitize_session_patch(patch: Any) -> dict[str, Any]:
    if not isinstance(patch, dict):
        return {}
    sanitized: dict[str, Any] = {}
    latest_summary = patch.get("latest_summary")
    if isinstance(latest_summary, dict):
        sanitized["latest_summary"] = latest_summary
    pinned_append = patch.get("pinned_evidence_append")
    if isinstance(pinned_append, list):
        normalized_append = [item for item in pinned_append if isinstance(item, dict)]
        if normalized_append:
            sanitized["pinned_evidence_append"] = normalized_append
    pinned_remove = patch.get("pinned_evidence_remove")
    if isinstance(pinned_remove, list):
        normalized_remove = [str(item).strip() for item in pinned_remove if str(item).strip()]
        if normalized_remove:
            sanitized["pinned_evidence_remove"] = normalized_remove
    context_state_patch = patch.get("context_state_patch")
    if isinstance(context_state_patch, dict):
        sanitized["context_state_patch"] = context_state_patch
    for key in ("actor", "note", "source"):
        value = patch.get(key)
        if isinstance(value, str) and value.strip():
            sanitized[key] = value.strip()
    return sanitized


def _sanitize_diagnosis_patch(patch: Any) -> tuple[dict[str, Any], list[str]]:
    if not isinstance(patch, dict):
        return {}, []
    sanitized: dict[str, Any] = {}
    dropped: list[str] = []

    summary = patch.get("summary")
    if isinstance(summary, str) and summary.strip():
        sanitized["summary"] = summary.strip()
    elif "summary" in patch and summary is not None:
        dropped.append("summary")

    root_cause = patch.get("root_cause")
    if isinstance(root_cause, dict):
        allowed_root_cause: dict[str, Any] = {}
        summary_value = root_cause.get("summary")
        statement_value = root_cause.get("statement")
        if isinstance(summary_value, str) and summary_value.strip():
            allowed_root_cause["summary"] = summary_value.strip()
        elif "summary" in root_cause and summary_value is not None:
            dropped.append("root_cause.summary")
        if isinstance(statement_value, str):
            if statement_value.strip():
                allowed_root_cause["statement"] = statement_value.strip()
            elif "statement" in root_cause:
                # An explicit empty string means "clear the inherited statement".
                allowed_root_cause["statement"] = ""
        elif "statement" in root_cause and statement_value is not None:
            dropped.append("root_cause.statement")
        if allowed_root_cause:
            sanitized["root_cause"] = allowed_root_cause
        forbidden_root_keys = set(root_cause.keys()) - {"summary", "statement"}
        for key in sorted(forbidden_root_keys):
            dropped.append(f"root_cause.{key}")
    elif "root_cause" in patch and root_cause is not None:
        dropped.append("root_cause")

    recommendations = patch.get("recommendations")
    if isinstance(recommendations, list):
        normalized_recommendations = [item for item in recommendations if isinstance(item, dict)]
        if normalized_recommendations:
            sanitized["recommendations"] = normalized_recommendations
        elif recommendations:
            dropped.append("recommendations")
    elif "recommendations" in patch and recommendations is not None:
        dropped.append("recommendations")

    for key in ("unknowns", "next_steps"):
        value = patch.get(key)
        if isinstance(value, list):
            normalized_list = [str(item).strip() for item in value if str(item).strip()]
            if normalized_list:
                sanitized[key] = normalized_list
            elif value:
                dropped.append(key)
        elif key in patch and value is not None:
            dropped.append(key)

    forbidden_top_level = set(patch.keys()) - {"summary", "root_cause", "recommendations", "unknowns", "next_steps"}
    dropped.extend(sorted(forbidden_top_level))
    return sanitized, sorted(set(dropped))


def _merge_diagnosis_patch(diagnosis_json: dict[str, Any], diagnosis_patch: dict[str, Any]) -> dict[str, Any]:
    if not diagnosis_patch:
        return diagnosis_json
    merged = dict(diagnosis_json)
    summary = diagnosis_patch.get("summary")
    if isinstance(summary, str) and summary.strip():
        merged["summary"] = summary.strip()
    root_cause_patch = diagnosis_patch.get("root_cause")
    if isinstance(root_cause_patch, dict):
        root_cause = merged.get("root_cause")
        if not isinstance(root_cause, dict):
            root_cause = {}
        root_cause = dict(root_cause)
        summary_value = root_cause_patch.get("summary")
        if isinstance(summary_value, str) and summary_value.strip():
            root_cause["summary"] = summary_value.strip()
        if "statement" in root_cause_patch:
            statement_value = root_cause_patch.get("statement")
            if isinstance(statement_value, str) and statement_value.strip():
                root_cause["statement"] = statement_value.strip()
            elif isinstance(statement_value, str):
                root_cause.pop("statement", None)
        merged["root_cause"] = root_cause
    recommendations = diagnosis_patch.get("recommendations")
    if isinstance(recommendations, list):
        merged["recommendations"] = [item for item in recommendations if isinstance(item, dict)]
    unknowns = diagnosis_patch.get("unknowns")
    if isinstance(unknowns, list):
        merged["unknowns"] = [str(item).strip() for item in unknowns if str(item).strip()]
    next_steps = diagnosis_patch.get("next_steps")
    if isinstance(next_steps, list):
        merged["next_steps"] = [str(item).strip() for item in next_steps if str(item).strip()]
    return merged


def _build_diagnosis_input(state: Any) -> dict[str, Any]:
    return {
        "incident_id": getattr(state, "incident_id", None),
        "incident_context": getattr(state, "incident_context", {}) if isinstance(getattr(state, "incident_context", {}), dict) else {},
        "input_hints": getattr(state, "input_hints", {}) if isinstance(getattr(state, "input_hints", {}), dict) else {},
        "quality_gate_decision": getattr(state, "quality_gate_decision", None),
        "quality_gate_reasons": getattr(state, "quality_gate_reasons", [])
        if isinstance(getattr(state, "quality_gate_reasons", []), list)
        else [],
        "missing_evidence": getattr(state, "missing_evidence", [])
        if isinstance(getattr(state, "missing_evidence", []), list)
        else [],
        "evidence_ids": getattr(state, "evidence_ids", []) if isinstance(getattr(state, "evidence_ids", []), list) else [],
        "evidence_meta": getattr(state, "evidence_meta", []) if isinstance(getattr(state, "evidence_meta", []), list) else [],
        "diagnosis_json": getattr(state, "diagnosis_json", {}) if isinstance(getattr(state, "diagnosis_json", {}), dict) else {},
    }


def _build_diagnosis_stage_summary(input_payload: dict[str, Any]) -> dict[str, Any]:
    return {
        "incident_id": _trim(input_payload.get("incident_id")),
        "quality_gate_decision": _trim(input_payload.get("quality_gate_decision")),
        "quality_gate_reasons": input_payload.get("quality_gate_reasons")
        if isinstance(input_payload.get("quality_gate_reasons"), list)
        else [],
        "missing_evidence": input_payload.get("missing_evidence") if isinstance(input_payload.get("missing_evidence"), list) else [],
        "evidence_ids": input_payload.get("evidence_ids") if isinstance(input_payload.get("evidence_ids"), list) else [],
        "has_incident_context": isinstance(input_payload.get("incident_context"), dict) and bool(input_payload.get("incident_context")),
        "has_input_hints": isinstance(input_payload.get("input_hints"), dict) and bool(input_payload.get("input_hints")),
        "diagnosis_summary": _trim(
            ((input_payload.get("diagnosis_json") or {}) if isinstance(input_payload.get("diagnosis_json"), dict) else {}).get("summary")
        ),
    }


def _sanitize_diagnosis_result(result: PromptSkillConsumeResult) -> tuple[PromptSkillConsumeResult, list[str]]:
    """Sanitize diagnosis.enrich output with structured validation."""
    raw_payload = result.payload if isinstance(result.payload, dict) else {}

    # Perform schema validation
    validation = validate_capability_output("diagnosis.enrich", raw_payload)
    # For diagnosis, we allow extra fields but still validate structure
    # So we don't fail on validation errors, but log them as dropped

    diagnosis_patch, dropped = _sanitize_diagnosis_patch(raw_payload.get("diagnosis_patch"))
    payload = {"diagnosis_patch": diagnosis_patch} if diagnosis_patch else {}
    session_patch = _sanitize_session_patch(result.session_patch)
    observations = [item for item in result.observations if isinstance(item, dict)]

    # Add any schema validation errors to dropped
    if not validation.is_valid:
        for error in validation.errors:
            dropped.append(f"{error.path}: {error.message}")

    return PromptSkillConsumeResult(payload=payload, session_patch=session_patch, observations=observations), dropped


def _apply_diagnosis_result(
    state: Any,
    result: PromptSkillConsumeResult,
    merge_session_patch: Callable[[Any, dict[str, Any] | None], None],
) -> None:
    payload = result.payload if isinstance(result.payload, dict) else {}
    diagnosis_patch = payload.get("diagnosis_patch")
    current = getattr(state, "diagnosis_json", None)
    if isinstance(diagnosis_patch, dict):
        if not isinstance(current, dict):
            current = {}
        setattr(state, "diagnosis_json", _merge_diagnosis_patch(current, diagnosis_patch))
    if result.session_patch:
        merge_session_patch(state, result.session_patch)


def _build_evidence_plan_input(state: Any) -> dict[str, Any]:
    return {
        "incident_id": getattr(state, "incident_id", None),
        "incident_context": getattr(state, "incident_context", {}) if isinstance(getattr(state, "incident_context", {}), dict) else {},
        "input_hints": getattr(state, "input_hints", {}) if isinstance(getattr(state, "input_hints", {}), dict) else {},
        "evidence_plan": getattr(state, "evidence_plan", {}) if isinstance(getattr(state, "evidence_plan", {}), dict) else {},
        "evidence_candidates": getattr(state, "evidence_candidates", [])
        if isinstance(getattr(state, "evidence_candidates", []), list)
        else [],
        "metrics_branch_meta": getattr(state, "metrics_branch_meta", {})
        if isinstance(getattr(state, "metrics_branch_meta", {}), dict)
        else {},
        "logs_branch_meta": getattr(state, "logs_branch_meta", {})
        if isinstance(getattr(state, "logs_branch_meta", {}), dict)
        else {},
        "evidence_mode": getattr(state, "evidence_mode", None),
    }


def _build_evidence_plan_stage_summary(input_payload: dict[str, Any]) -> dict[str, Any]:
    evidence_plan = input_payload.get("evidence_plan") if isinstance(input_payload.get("evidence_plan"), dict) else {}
    return {
        "incident_id": _trim(input_payload.get("incident_id")),
        "evidence_mode": _trim(input_payload.get("evidence_mode")),
        "candidate_count": len(input_payload.get("evidence_candidates") or []) if isinstance(input_payload.get("evidence_candidates"), list) else 0,
        "metrics_branch_mode": _trim(
            ((input_payload.get("metrics_branch_meta") or {}) if isinstance(input_payload.get("metrics_branch_meta"), dict) else {}).get("mode")
        ),
        "logs_branch_mode": _trim(
            ((input_payload.get("logs_branch_meta") or {}) if isinstance(input_payload.get("logs_branch_meta"), dict) else {}).get("mode")
        ),
        "has_incident_context": isinstance(input_payload.get("incident_context"), dict) and bool(input_payload.get("incident_context")),
        "has_input_hints": isinstance(input_payload.get("input_hints"), dict) and bool(input_payload.get("input_hints")),
        "planned_candidate_count": len(evidence_plan.get("candidates") or []) if isinstance(evidence_plan.get("candidates"), list) else 0,
    }


def _sanitize_evidence_plan_result(result: PromptSkillConsumeResult) -> tuple[PromptSkillConsumeResult, list[str]]:
    """Sanitize evidence.plan output with structured validation."""
    raw_payload = result.payload if isinstance(result.payload, dict) else {}

    # Perform schema validation
    validation = validate_capability_output("evidence.plan", raw_payload)
    # For evidence.plan, we allow extra fields but still validate structure

    payload: dict[str, Any] = {}
    dropped: list[str] = []

    evidence_plan_patch = raw_payload.get("evidence_plan_patch")
    if isinstance(evidence_plan_patch, dict):
        payload["evidence_plan_patch"] = evidence_plan_patch
    elif "evidence_plan_patch" in raw_payload and evidence_plan_patch is not None:
        dropped.append("evidence_plan_patch")

    evidence_candidates = raw_payload.get("evidence_candidates")
    if isinstance(evidence_candidates, list):
        if all(isinstance(item, dict) for item in evidence_candidates):
            payload["evidence_candidates"] = list(evidence_candidates)
        else:
            dropped.append("evidence_candidates")
    elif "evidence_candidates" in raw_payload and evidence_candidates is not None:
        dropped.append("evidence_candidates")

    for key in ("metrics_branch_meta", "logs_branch_meta"):
        value = raw_payload.get(key)
        if key == "metrics_branch_meta":
            normalized_metrics_branch_meta, dropped_metrics_branch_meta = _sanitize_metrics_branch_meta(value)
            dropped.extend(dropped_metrics_branch_meta)
            if isinstance(normalized_metrics_branch_meta, dict):
                payload[key] = normalized_metrics_branch_meta
            elif key in raw_payload and value is not None:
                dropped.append(key)
            continue
        if key == "logs_branch_meta":
            normalized_logs_branch_meta, dropped_logs_branch_meta = _sanitize_logs_branch_meta(value)
            dropped.extend(dropped_logs_branch_meta)
            if isinstance(normalized_logs_branch_meta, dict):
                payload[key] = normalized_logs_branch_meta
            elif key in raw_payload and value is not None:
                dropped.append(key)
            continue
        if isinstance(value, dict):
            payload[key] = value
        elif key in raw_payload and value is not None:
            dropped.append(key)

    if result.session_patch:
        dropped.append("session_patch")

    observations = [item for item in result.observations if isinstance(item, dict)]
    forbidden_top_level = set(raw_payload.keys()) - {
        "evidence_plan_patch",
        "evidence_candidates",
        "metrics_branch_meta",
        "logs_branch_meta",
    }
    dropped.extend(sorted(forbidden_top_level))

    # Add any schema validation errors to dropped
    if not validation.is_valid:
        for error in validation.errors:
            dropped.append(f"{error.path}: {error.message}")

    return PromptSkillConsumeResult(payload=payload, session_patch={}, observations=observations), sorted(set(dropped))


def _apply_evidence_plan_result(
    state: Any,
    result: PromptSkillConsumeResult,
    merge_session_patch: Callable[[Any, dict[str, Any] | None], None],
) -> None:
    del merge_session_patch
    payload = result.payload if isinstance(result.payload, dict) else {}
    current_plan = getattr(state, "evidence_plan", None)
    if not isinstance(current_plan, dict):
        current_plan = {}
    evidence_plan_patch = payload.get("evidence_plan_patch")
    if isinstance(evidence_plan_patch, dict):
        current_plan = _merge_mapping(current_plan, evidence_plan_patch)
        setattr(state, "evidence_plan", current_plan)
    evidence_candidates = payload.get("evidence_candidates")
    if isinstance(evidence_candidates, list):
        setattr(state, "evidence_candidates", list(evidence_candidates))
    metrics_branch_meta = payload.get("metrics_branch_meta")
    if isinstance(metrics_branch_meta, dict):
        current_metrics_branch_meta = getattr(state, "metrics_branch_meta", None)
        if not isinstance(current_metrics_branch_meta, dict):
            current_metrics_branch_meta = {}
        merged_metrics_branch_meta = dict(current_metrics_branch_meta)
        merged_metrics_branch_meta["mode"] = str(metrics_branch_meta.get("mode") or merged_metrics_branch_meta.get("mode") or "query")
        merged_metrics_branch_meta["query_type"] = "metrics"
        request_payload = merged_metrics_branch_meta.get("request_payload")
        if not isinstance(request_payload, dict):
            request_payload = {}
        incoming_request_payload = metrics_branch_meta.get("request_payload")
        if isinstance(incoming_request_payload, dict):
            promql = _trim(incoming_request_payload.get("promql"))
            if promql:
                request_payload["promql"] = promql
            if "step_seconds" in incoming_request_payload:
                try:
                    step_seconds = int(incoming_request_payload.get("step_seconds"))
                except (TypeError, ValueError):
                    step_seconds = 0
                if step_seconds > 0:
                    request_payload["step_seconds"] = step_seconds
        merged_metrics_branch_meta["request_payload"] = request_payload
        query_request = merged_metrics_branch_meta.get("query_request")
        if not isinstance(query_request, dict):
            query_request = {}
        incoming_query_request = metrics_branch_meta.get("query_request")
        if isinstance(incoming_query_request, dict):
            query_text = _trim(incoming_query_request.get("queryText") or incoming_query_request.get("query_text"))
            if query_text:
                query_request["queryText"] = query_text
        merged_metrics_branch_meta["query_request"] = query_request
        setattr(state, "metrics_branch_meta", merged_metrics_branch_meta)
    logs_branch_meta = payload.get("logs_branch_meta")
    if isinstance(logs_branch_meta, dict):
        current_logs_branch_meta = getattr(state, "logs_branch_meta", None)
        if not isinstance(current_logs_branch_meta, dict):
            current_logs_branch_meta = {}
        merged_logs_branch_meta = dict(current_logs_branch_meta)
        merged_logs_branch_meta["mode"] = str(logs_branch_meta.get("mode") or merged_logs_branch_meta.get("mode") or "query")
        merged_logs_branch_meta["query_type"] = "logs"
        request_payload = merged_logs_branch_meta.get("request_payload")
        if not isinstance(request_payload, dict):
            request_payload = {}
        incoming_request_payload = logs_branch_meta.get("request_payload")
        if isinstance(incoming_request_payload, dict):
            query = _trim(incoming_request_payload.get("query"))
            if query:
                request_payload["query"] = query
        merged_logs_branch_meta["request_payload"] = request_payload
        query_request = merged_logs_branch_meta.get("query_request")
        if not isinstance(query_request, dict):
            query_request = {}
        incoming_query_request = logs_branch_meta.get("query_request")
        if isinstance(incoming_query_request, dict):
            query_text = _trim(incoming_query_request.get("queryText") or incoming_query_request.get("query_text"))
            if query_text:
                query_request["queryText"] = query_text
        merged_logs_branch_meta["query_request"] = query_request
        setattr(state, "logs_branch_meta", merged_logs_branch_meta)
    current_candidates = getattr(state, "evidence_candidates", None)
    current_plan = getattr(state, "evidence_plan", None)
    if isinstance(current_plan, dict) and isinstance(current_candidates, list):
        current_plan["candidates"] = current_candidates


def _build_tool_plan_input(state: Any) -> dict[str, Any]:
    return {
        "incident_id": getattr(state, "incident_id", None),
        "incident_context": getattr(state, "incident_context", {}) if isinstance(getattr(state, "incident_context", {}), dict) else {},
        "input_hints": getattr(state, "input_hints", {}) if isinstance(getattr(state, "input_hints", {}), dict) else {},
        "existing_evidence_ids": getattr(state, "evidence_ids", []) if isinstance(getattr(state, "evidence_ids", []), list) else [],
        "evidence_mode": getattr(state, "evidence_mode", None),
    }


def _build_tool_plan_stage_summary(input_payload: dict[str, Any]) -> dict[str, Any]:
    return {
        "incident_id": _trim(input_payload.get("incident_id")),
        "evidence_mode": _trim(input_payload.get("evidence_mode")),
        "has_incident_context": isinstance(input_payload.get("incident_context"), dict) and bool(input_payload.get("incident_context")),
        "has_input_hints": isinstance(input_payload.get("input_hints"), dict) and bool(input_payload.get("input_hints")),
        "existing_evidence_count": len(input_payload.get("existing_evidence_ids") or []) if isinstance(input_payload.get("existing_evidence_ids"), list) else 0,
    }


def _sanitize_tool_plan_item(item: Any) -> tuple[dict[str, Any] | None, list[str]]:
    """Sanitize a single tool call item in the plan."""
    if not isinstance(item, dict):
        return None, ["tool_call_plan.items: invalid item type"]

    dropped: list[str] = []
    result: dict[str, Any] = {}

    tool = _trim(item.get("tool"))
    if not tool:
        return None, ["tool_call_plan.items[].tool: required"]
    result["tool"] = tool

    params = item.get("params")
    if isinstance(params, dict):
        result["params"] = dict(params)
    elif params is not None:
        dropped.append("tool_call_plan.items[].params: invalid type, using empty dict")
        result["params"] = {}
    else:
        result["params"] = {}

    # Optional fields
    query_type = _trim(item.get("query_type"))
    if query_type:
        result["query_type"] = query_type

    purpose = _trim(item.get("purpose"))
    if purpose:
        result["purpose"] = purpose

    evidence_kind = _trim(item.get("evidence_kind"))
    if evidence_kind:
        result["evidence_kind"] = evidence_kind

    if item.get("optional") is not None:
        result["optional"] = bool(item.get("optional"))

    depends_on = item.get("depends_on")
    if isinstance(depends_on, list):
        result["depends_on"] = [str(d) for d in depends_on if d is not None]
    elif depends_on is not None:
        dropped.append("tool_call_plan.items[].depends_on: invalid type")

    call_id = _trim(item.get("call_id"))
    if call_id:
        result["call_id"] = call_id

    return result, dropped


def _sanitize_tool_plan_result(result: PromptSkillConsumeResult) -> tuple[PromptSkillConsumeResult, list[str]]:
    """Sanitize tool.plan output with structured validation."""
    raw_payload = result.payload if isinstance(result.payload, dict) else {}

    # First, perform schema validation
    validation = validate_capability_output("tool.plan", raw_payload)
    if not validation.is_valid:
        # Return structured error info
        dropped = [f"{e.path}: {e.message}" for e in validation.errors]
        return PromptSkillConsumeResult(payload={}, session_patch={}, observations=[]), dropped

    # Then apply additional sanitization logic for business rules
    payload: dict[str, Any] = {}
    dropped: list[str] = []

    tool_call_plan = raw_payload.get("tool_call_plan")
    if isinstance(tool_call_plan, dict):
        sanitized_plan: dict[str, Any] = {}

        items = tool_call_plan.get("items")
        if isinstance(items, list):
            sanitized_items: list[dict[str, Any]] = []
            for idx, item in enumerate(items):
                sanitized_item, item_dropped = _sanitize_tool_plan_item(item)
                for drop in item_dropped:
                    dropped.append(f"tool_call_plan.items[{idx}].{drop}" if not drop.startswith("tool_call_plan") else drop)
                if sanitized_item:
                    sanitized_items.append(sanitized_item)
            sanitized_plan["items"] = sanitized_items
        elif items is not None:
            dropped.append("tool_call_plan.items: invalid type")

        parallel_groups = tool_call_plan.get("parallel_groups")
        if isinstance(parallel_groups, list):
            sanitized_groups: list[list[int]] = []
            for gidx, group in enumerate(parallel_groups):
                if isinstance(group, list):
                    sanitized_group = [int(i) for i in group if isinstance(i, int)]
                    sanitized_groups.append(sanitized_group)
            sanitized_plan["parallel_groups"] = sanitized_groups
        elif parallel_groups is not None:
            dropped.append("tool_call_plan.parallel_groups: invalid type")

        if sanitized_plan:
            payload["tool_call_plan"] = sanitized_plan
    elif tool_call_plan is not None:
        dropped.append("tool_call_plan: invalid type")

    if result.session_patch:
        dropped.append("session_patch: must be omitted")

    observations = [item for item in result.observations if isinstance(item, dict)]
    forbidden_top_level = set(raw_payload.keys()) - {"tool_call_plan"}
    dropped.extend(sorted(forbidden_top_level))

    return PromptSkillConsumeResult(payload=payload, session_patch={}, observations=observations), sorted(set(dropped))


def _apply_tool_plan_result(
    state: Any,
    result: PromptSkillConsumeResult,
    merge_session_patch: Callable[[Any, dict[str, Any] | None], None],
) -> None:
    del merge_session_patch
    payload = result.payload if isinstance(result.payload, dict) else {}
    tool_call_plan = payload.get("tool_call_plan")
    if isinstance(tool_call_plan, dict):
        setattr(state, "tool_call_plan", tool_call_plan)


_CAPABILITY_DEFINITIONS: dict[str, CapabilityDefinition] = {
    "diagnosis.enrich": CapabilityDefinition(
        capability="diagnosis.enrich",
        stage="summarize_diagnosis",
        output_contract={
            "payload": {
                "diagnosis_patch": {
                    "summary": "optional string",
                    "root_cause": {
                        "summary": "optional string",
                        "statement": "optional string",
                    },
                    "recommendations": "optional list",
                    "unknowns": "optional list",
                    "next_steps": "optional list",
                },
            },
            "session_patch": {
                "latest_summary": "optional object",
                "pinned_evidence_append": "optional list",
                "pinned_evidence_remove": "optional list",
                "context_state_patch": "optional object",
                "note": "optional string",
            },
            "observations": "optional list",
        },
        build_input=_build_diagnosis_input,
        build_stage_summary=_build_diagnosis_stage_summary,
        sanitize_output=_sanitize_diagnosis_result,
        apply_result=_apply_diagnosis_result,
    ),
    "evidence.plan": CapabilityDefinition(
        capability="evidence.plan",
        stage="plan_evidence",
        output_contract={
            "payload": {
                "evidence_plan_patch": "optional object",
                "evidence_candidates": "optional list of objects",
                "metrics_branch_meta": "optional object",
                "logs_branch_meta": "optional object",
            },
            "session_patch": "must be omitted",
            "observations": "optional list",
        },
        build_input=_build_evidence_plan_input,
        build_stage_summary=_build_evidence_plan_stage_summary,
        sanitize_output=_sanitize_evidence_plan_result,
        apply_result=_apply_evidence_plan_result,
    ),
    "tool.plan": CapabilityDefinition(
        capability="tool.plan",
        stage="plan_tool_calls",
        output_contract={
            "payload": {
                "tool_call_plan": {
                    "items": [
                        {
                            "tool": "string - tool name (required)",
                            "params": "object - tool parameters",
                            "query_type": "string - metrics/logs/traces (optional)",
                            "purpose": "string - why this tool is called (optional)",
                            "evidence_kind": "string - evidence type (optional, default: query)",
                            "optional": "boolean - whether failure is acceptable (optional)",
                            "depends_on": "list of call_ids this depends on (optional)",
                            "call_id": "string - unique identifier (optional)",
                        }
                    ],
                    "parallel_groups": "list of index lists for parallel execution (optional)",
                },
            },
            "session_patch": "must be omitted",
            "observations": "optional list",
        },
        build_input=_build_tool_plan_input,
        build_stage_summary=_build_tool_plan_stage_summary,
        sanitize_output=_sanitize_tool_plan_result,
        apply_result=_apply_tool_plan_result,
    ),
}


def get_capability_definition(capability: str) -> CapabilityDefinition | None:
    return _CAPABILITY_DEFINITIONS.get(_trim(capability))


def list_capabilities() -> list[str]:
    return sorted(_CAPABILITY_DEFINITIONS.keys())
