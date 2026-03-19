from __future__ import annotations

from dataclasses import dataclass
import json
import time
from typing import Any


def _trim_text(value: Any) -> str:
    return str(value or "").strip()


def normalize_lower_text(value: Any) -> str:
    return _trim_text(value).lower()


def normalize_string_list(values: list[Any] | None) -> list[str]:
    if not values:
        return []
    out: list[str] = []
    seen: set[str] = set()
    for item in values:
        normalized = _trim_text(item)
        if not normalized or normalized in seen:
            continue
        seen.add(normalized)
        out.append(normalized)
    return out


def compact_json(payload: Any) -> str:
    return json.dumps(payload, ensure_ascii=False, separators=(",", ":"), sort_keys=True)


def ts(seconds: int) -> dict[str, int]:
    return {"seconds": int(seconds), "nanos": 0}


@dataclass(frozen=True)
class ClaimStartRequest:
    """Canonical request model for worker claim/start."""

    job_id: str

    def path(self) -> str:
        return f"/v1/ai/jobs/{_trim_text(self.job_id)}/start"


@dataclass(frozen=True)
class ClaimStartResponse:
    """Canonical response model for worker claim/start.

    Contains resolved skillsets and resolved tool providers for the job's pipeline.
    """

    skillsets_json: str | None = None
    resolved_tool_providers: list[dict[str, Any]] | None = None
    agent_context_json: str | None = None
    playbook_config_json: str | None = None
    verification_template_json: str | None = None

    @classmethod
    def from_api_response(cls, payload: dict[str, Any]) -> "ClaimStartResponse":
        data = payload.get("data", payload)
        if not isinstance(data, dict):
            return cls()
        skillsets_json = None
        raw_skillsets = data.get("skillsetsJSON")
        if isinstance(raw_skillsets, str) and raw_skillsets.strip():
            skillsets_json = raw_skillsets.strip()
        # Parse resolved_tool_providers (canonical field)
        resolved_tool_providers = None
        raw_providers = data.get("resolvedToolProviders")
        if isinstance(raw_providers, list) and raw_providers:
            resolved_tool_providers = [
                p for p in raw_providers if isinstance(p, dict)
            ]
        # Parse agent_context_json (hybrid multi-agent)
        agent_context_json = None
        raw_agent_context = data.get("agentContextJSON")
        if isinstance(raw_agent_context, str) and raw_agent_context.strip():
            agent_context_json = raw_agent_context.strip()
        # Parse playbook_config_json (Phase 8A)
        playbook_config_json = None
        raw_playbook_config = data.get("playbookConfigJSON")
        if isinstance(raw_playbook_config, str) and raw_playbook_config.strip():
            playbook_config_json = raw_playbook_config.strip()
        # Parse verification_template_json (Phase 8B)
        verification_template_json = None
        raw_verification_template = data.get("verificationTemplateJSON")
        if isinstance(raw_verification_template, str) and raw_verification_template.strip():
            verification_template_json = raw_verification_template.strip()
        return cls(
            skillsets_json=skillsets_json,
            resolved_tool_providers=resolved_tool_providers,
            agent_context_json=agent_context_json,
            playbook_config_json=playbook_config_json,
            verification_template_json=verification_template_json,
        )

    def has_skillsets(self) -> bool:
        return isinstance(self.skillsets_json, str) and self.skillsets_json != ""

    def has_resolved_tool_providers(self) -> bool:
        return isinstance(self.resolved_tool_providers, list) and len(self.resolved_tool_providers) > 0

    def has_agent_context(self) -> bool:
        return isinstance(self.agent_context_json, str) and self.agent_context_json != ""

    def has_playbook_config(self) -> bool:
        return isinstance(self.playbook_config_json, str) and self.playbook_config_json != ""

    def has_verification_template(self) -> bool:
        return isinstance(self.verification_template_json, str) and self.verification_template_json != ""

    def parse_playbook_config(self) -> dict[str, Any] | None:
        """Parse playbook_config_json into a dictionary."""
        if not self.has_playbook_config():
            return None
        try:
            parsed = json.loads(self.playbook_config_json)
            return parsed if isinstance(parsed, dict) else None
        except json.JSONDecodeError:
            return None

    def parse_verification_template(self) -> list[dict[str, Any]] | None:
        """Parse verification_template_json into a list of template dictionaries."""
        if not self.has_verification_template():
            return None
        try:
            parsed = json.loads(self.verification_template_json)
            return parsed if isinstance(parsed, list) else None
        except json.JSONDecodeError:
            return None

    def parse_skillsets(self) -> dict[str, Any] | None:
        if not self.has_skillsets():
            return None
        try:
            parsed = json.loads(self.skillsets_json)
            return parsed if isinstance(parsed, dict) else None
        except json.JSONDecodeError:
            return None


@dataclass(frozen=True)
class RenewHeartbeatRequest:
    """Canonical request model for worker renew/heartbeat."""

    job_id: str

    def path(self) -> str:
        return f"/v1/ai/jobs/{_trim_text(self.job_id)}/heartbeat"


@dataclass(frozen=True)
class GetJobSessionContextRequest:
    """Canonical request model for job-scoped session context reads."""

    job_id: str

    def path(self) -> str:
        return f"/v1/ai/jobs/{_trim_text(self.job_id)}/session-context"


@dataclass(frozen=True)
class PatchJobSessionContextRequest:
    """Canonical request model for job-scoped session context patching."""

    job_id: str
    session_revision: str | None = None
    latest_summary: dict[str, Any] | None = None
    pinned_evidence_append: list[dict[str, Any]] | None = None
    pinned_evidence_remove: list[str] | None = None
    context_state_patch: dict[str, Any] | None = None
    actor: str | None = None
    note: str | None = None
    source: str | None = None

    def path(self) -> str:
        return f"/v1/ai/jobs/{_trim_text(self.job_id)}/session-context"

    def to_api_body(self) -> dict[str, Any]:
        body: dict[str, Any] = {}
        if self.session_revision is not None:
            body["session_revision"] = _trim_text(self.session_revision)
        if isinstance(self.latest_summary, dict):
            body["latest_summary"] = self.latest_summary
        append_items = [item for item in (self.pinned_evidence_append or []) if isinstance(item, dict)]
        if append_items:
            body["pinned_evidence_append"] = append_items
        remove_items = normalize_string_list(self.pinned_evidence_remove)
        if remove_items:
            body["pinned_evidence_remove"] = remove_items
        if isinstance(self.context_state_patch, dict):
            body["context_state_patch"] = self.context_state_patch
        actor = _trim_text(self.actor)
        if actor:
            body["actor"] = actor
        note = _trim_text(self.note)
        if note:
            body["note"] = note
        source = _trim_text(self.source)
        if source:
            body["source"] = source
        return body


@dataclass(frozen=True)
class ToolCallReportRequest:
    """Canonical request model for tool call reporting."""

    job_id: str
    seq: int
    node_name: str
    tool_name: str
    request_json: dict[str, Any]
    response_json: dict[str, Any] | None
    latency_ms: int
    status: str
    error_message: str | None = None
    evidence_ids: list[str] | None = None

    # Extended audit metadata
    incident_id: str | None = None
    skill_id: str | None = None
    skill_version: str | None = None
    capability: str | None = None
    provider_id: str | None = None
    toolset_id: str | None = None

    def path(self) -> str:
        return f"/v1/ai/jobs/{_trim_text(self.job_id)}/tool-calls"

    def to_api_body(self) -> dict[str, Any]:
        body: dict[str, Any] = {
            "jobID": _trim_text(self.job_id),
            "seq": int(self.seq),
            "nodeName": _trim_text(self.node_name),
            "toolName": _trim_text(self.tool_name),
            "requestJSON": compact_json(self.request_json if isinstance(self.request_json, dict) else {}),
            "status": normalize_lower_text(self.status),
            "latencyMs": int(max(self.latency_ms, 0)),
        }
        if self.response_json is not None:
            body["responseJSON"] = compact_json(self.response_json)
        error_message = _trim_text(self.error_message)
        if error_message:
            body["errorMessage"] = error_message
        evidence_ids = normalize_string_list(self.evidence_ids)
        if evidence_ids:
            body["evidenceIDs"] = evidence_ids
        # Extended audit metadata
        if self.incident_id:
            body["incidentID"] = _trim_text(self.incident_id)
        if self.skill_id:
            body["skillID"] = _trim_text(self.skill_id)
        if self.skill_version:
            body["skillVersion"] = _trim_text(self.skill_version)
        if self.capability:
            body["capability"] = _trim_text(self.capability)
        if self.provider_id:
            body["providerID"] = _trim_text(self.provider_id)
        if self.toolset_id:
            body["toolsetID"] = _trim_text(self.toolset_id)
        return body


@dataclass(frozen=True)
class ListToolCallsRequest:
    """Canonical query model for tool call listing."""

    job_id: str
    limit: int = 200
    offset: int = 0
    seq: int | None = None

    def path(self) -> str:
        return f"/v1/ai/jobs/{_trim_text(self.job_id)}/tool-calls"

    def to_api_params(self) -> dict[str, int]:
        params: dict[str, int] = {
            "limit": max(int(self.limit), 1),
            "offset": max(int(self.offset), 0),
        }
        if self.seq is not None:
            params["seq"] = int(self.seq)
        return params


@dataclass(frozen=True)
class FinalizeRequest:
    """Canonical request model for job finalize."""

    job_id: str
    status: str
    diagnosis_json: dict[str, Any] | None = None
    error_message: str | None = None
    evidence_ids: list[str] | None = None

    def path(self) -> str:
        return f"/v1/ai/jobs/{_trim_text(self.job_id)}/finalize"

    def to_api_body(self) -> dict[str, Any]:
        body: dict[str, Any] = {
            "jobID": _trim_text(self.job_id),
            "status": normalize_lower_text(self.status),
        }
        if self.diagnosis_json is not None:
            body["diagnosisJSON"] = compact_json(self.diagnosis_json)
        error_message = _trim_text(self.error_message)
        if error_message:
            body["errorMessage"] = error_message
        evidence_ids = normalize_string_list(self.evidence_ids)
        if evidence_ids:
            body["evidenceIDs"] = evidence_ids
        return body


@dataclass(frozen=True)
class EvidencePublishRequest:
    """Canonical request model for evidence publish/save."""

    incident_id: str
    idempotency_key: str
    evidence_type: str
    query_text: str
    query_json: str
    time_range_start_s: int
    time_range_end_s: int
    result_json: str
    summary: str
    created_by: str
    job_id: str | None = None
    datasource_id: str | None = None

    @classmethod
    def for_mock(
        cls,
        *,
        incident_id: str,
        summary: str,
        raw: dict[str, Any],
        job_id: str | None = None,
        idempotency_key: str | None = None,
        created_by: str | None = None,
        now_seconds: int | None = None,
    ) -> "EvidencePublishRequest":
        now_s = int(now_seconds if now_seconds is not None else time.time())
        return cls(
            incident_id=_trim_text(incident_id),
            idempotency_key=_trim_text(idempotency_key) or "",
            evidence_type="metrics",
            query_text="mock://orchestrator",
            query_json="{}",
            time_range_start_s=now_s - 600,
            time_range_end_s=now_s,
            result_json=compact_json(raw),
            summary=_trim_text(summary),
            created_by=_trim_text(created_by) or "system",
            job_id=_trim_text(job_id) or None,
        )

    @classmethod
    def for_query(
        cls,
        *,
        incident_id: str,
        kind: str,
        query: dict[str, Any],
        result: dict[str, Any],
        job_id: str | None = None,
        idempotency_key: str | None = None,
        created_by: str | None = None,
        now_seconds: int | None = None,
    ) -> "EvidencePublishRequest":
        now_s = int(now_seconds if now_seconds is not None else time.time())
        query_text = _trim_text(query.get("queryText") or query.get("query_text") or "orchestrator_query")
        raw_result = result.get("queryResultJSON")
        if isinstance(raw_result, str):
            result_json = raw_result
        else:
            result_json = compact_json(result)
        datasource_id = _trim_text(query.get("datasourceID") or query.get("datasource_id")) or None
        return cls(
            incident_id=_trim_text(incident_id),
            idempotency_key=_trim_text(idempotency_key) or "",
            evidence_type=normalize_lower_text(kind),
            query_text=query_text,
            query_json=compact_json(query),
            time_range_start_s=now_s - 600,
            time_range_end_s=now_s,
            result_json=result_json,
            summary=f"orchestrator collected {_trim_text(kind)} evidence",
            created_by=_trim_text(created_by) or "system",
            job_id=_trim_text(job_id) or None,
            datasource_id=datasource_id,
        )

    def path(self) -> str:
        return f"/v1/incidents/{_trim_text(self.incident_id)}/evidence"

    def to_api_body(self, *, fallback_idempotency_key: str) -> dict[str, Any]:
        body: dict[str, Any] = {
            "incidentID": _trim_text(self.incident_id),
            "idempotencyKey": _trim_text(self.idempotency_key) or _trim_text(fallback_idempotency_key),
            "type": normalize_lower_text(self.evidence_type),
            "queryText": _trim_text(self.query_text),
            "queryJSON": _trim_text(self.query_json),
            "timeRangeStart": ts(self.time_range_start_s),
            "timeRangeEnd": ts(self.time_range_end_s),
            "resultJSON": _trim_text(self.result_json),
            "summary": _trim_text(self.summary),
            "createdBy": _trim_text(self.created_by) or "system",
        }
        if self.job_id:
            body["jobID"] = _trim_text(self.job_id)
        if self.datasource_id:
            body["datasourceID"] = _trim_text(self.datasource_id)
        return body


@dataclass(frozen=True)
class VerificationReportRequest:
    """Canonical request model for verification run reporting."""

    incident_id: str
    source: str
    step_index: int
    tool: str
    observed: str
    meets_expectation: bool
    params_json: dict[str, Any] | str | None = None
    actor: str | None = None

    def path(self) -> str:
        return f"/v1/incidents/{_trim_text(self.incident_id)}/verification-runs"

    def to_api_body(self) -> dict[str, Any]:
        body: dict[str, Any] = {
            "incidentID": _trim_text(self.incident_id),
            "source": normalize_lower_text(self.source),
            "stepIndex": int(self.step_index),
            "tool": _trim_text(self.tool),
            "observed": _trim_text(self.observed),
            "meetsExpectation": bool(self.meets_expectation),
        }
        normalized_actor = _trim_text(self.actor)
        if normalized_actor:
            body["actor"] = normalized_actor
        if self.params_json is not None:
            if isinstance(self.params_json, str):
                params = _trim_text(self.params_json)
                if params:
                    body["paramsJSON"] = params
            else:
                body["paramsJSON"] = compact_json(self.params_json)
        return body


@dataclass(frozen=True)
class ListVerificationRunsRequest:
    """Canonical query model for verification run listing."""

    incident_id: str
    page: int = 1
    limit: int = 200

    def path(self) -> str:
        return f"/v1/incidents/{_trim_text(self.incident_id)}/verification-runs"

    def to_api_params(self) -> dict[str, int]:
        return {
            "page": max(int(self.page), 1),
            "limit": max(int(self.limit), 1),
        }
