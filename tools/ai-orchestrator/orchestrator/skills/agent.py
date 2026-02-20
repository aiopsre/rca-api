from __future__ import annotations

from dataclasses import dataclass, field
import json
from typing import Any


def _trim(value: Any) -> str:
    return str(value or "").strip()


def _extract_message_text(content: Any) -> str:
    if isinstance(content, str):
        return content.strip()
    if isinstance(content, list):
        parts: list[str] = []
        for item in content:
            if isinstance(item, str):
                parts.append(item)
                continue
            if not isinstance(item, dict):
                continue
            item_type = _trim(item.get("type")).lower()
            if item_type in {"text", "output_text"}:
                text = _trim(item.get("text"))
                if text:
                    parts.append(text)
        return "\n".join(part for part in parts if part).strip()
    return _trim(content)


def _extract_json_payload(raw_text: str) -> dict[str, Any]:
    text = str(raw_text or "").strip()
    if not text:
        raise ValueError("agent returned empty response")
    if text.startswith("```"):
        lines = text.splitlines()
        if lines and lines[0].startswith("```"):
            lines = lines[1:]
        if lines and lines[-1].strip() == "```":
            lines = lines[:-1]
        text = "\n".join(lines).strip()
    try:
        payload = json.loads(text)
    except json.JSONDecodeError:
        start = text.find("{")
        end = text.rfind("}")
        if start < 0 or end <= start:
            raise
        payload = json.loads(text[start : end + 1])
    if not isinstance(payload, dict):
        raise ValueError("agent response must be a JSON object")
    return payload


@dataclass(frozen=True)
class SkillSelectionResult:
    selected_binding_key: str = ""
    reason: str = ""


@dataclass(frozen=True)
class DiagnosisEnrichOutput:
    diagnosis_patch: dict[str, Any] = field(default_factory=dict)
    session_patch: dict[str, Any] = field(default_factory=dict)
    observations: list[dict[str, Any]] = field(default_factory=list)


class PromptSkillAgent:
    def __init__(
        self,
        *,
        model: str,
        base_url: str,
        api_key: str,
        timeout_seconds: float,
    ) -> None:
        self._model_name = _trim(model)
        self._base_url = _trim(base_url).rstrip("/")
        self._api_key = _trim(api_key)
        self._timeout_seconds = max(float(timeout_seconds), 1.0)
        self._llm: Any | None = None

    @property
    def configured(self) -> bool:
        return bool(self._model_name and self._base_url and self._api_key)

    def select_skill(
        self,
        *,
        capability: str,
        stage: str,
        stage_summary: dict[str, Any],
        candidates: list[dict[str, Any]],
    ) -> SkillSelectionResult:
        if not self.configured:
            raise RuntimeError("prompt skill agent is not configured")
        system_prompt = (
            "You are selecting at most one optional RCA skill for the current stage.\n"
            "Choose the best candidate only when it is clearly useful.\n"
            "If no skill should be used, return an empty selected_binding_key.\n"
            "Return strict JSON with keys selected_binding_key and reason."
        )
        user_payload = {
            "capability": capability,
            "stage": stage,
            "stage_summary": stage_summary,
            "candidates": candidates,
            "output_contract": {
                "selected_binding_key": "string or empty",
                "reason": "short explanation",
            },
        }
        response = self._invoke_json(system_prompt=system_prompt, user_payload=user_payload)
        return SkillSelectionResult(
            selected_binding_key=_trim(response.get("selected_binding_key")),
            reason=_trim(response.get("reason")),
        )

    def run_diagnosis_enrich(
        self,
        *,
        skill_id: str,
        skill_version: str,
        skill_document: str,
        input_payload: dict[str, Any],
    ) -> DiagnosisEnrichOutput:
        if not self.configured:
            raise RuntimeError("prompt skill agent is not configured")
        system_prompt = (
            "You are applying a prompt-first RCA diagnosis skill.\n"
            "Read the skill document and return strict JSON only.\n"
            "Do not call tools. Only produce diagnosis_patch, session_patch, and observations.\n"
            "Do not modify schema_version, generated_at, incident_id, timeline, hypotheses, "
            "root_cause.confidence, or root_cause.evidence_ids."
        )
        user_payload = {
            "skill_id": skill_id,
            "skill_version": skill_version,
            "skill_document": skill_document,
            "input": input_payload,
            "output_contract": {
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
                "session_patch": {
                    "latest_summary": "optional object",
                    "pinned_evidence_append": "optional list",
                    "pinned_evidence_remove": "optional list",
                    "context_state_patch": "optional object",
                    "note": "optional string",
                },
                "observations": "optional list",
            },
        }
        response = self._invoke_json(system_prompt=system_prompt, user_payload=user_payload)
        diagnosis_patch = response.get("diagnosis_patch")
        session_patch = response.get("session_patch")
        observations = response.get("observations")
        return DiagnosisEnrichOutput(
            diagnosis_patch=diagnosis_patch if isinstance(diagnosis_patch, dict) else {},
            session_patch=session_patch if isinstance(session_patch, dict) else {},
            observations=[item for item in observations if isinstance(item, dict)] if isinstance(observations, list) else [],
        )

    def _invoke_json(self, *, system_prompt: str, user_payload: dict[str, Any]) -> dict[str, Any]:
        llm = self._get_llm()
        try:
            from langchain_core.messages import HumanMessage, SystemMessage
        except Exception as exc:  # noqa: BLE001
            raise RuntimeError("langchain-core is required for prompt-first skills") from exc
        response = llm.invoke(
            [
                SystemMessage(content=system_prompt),
                HumanMessage(content=json.dumps(user_payload, ensure_ascii=False, separators=(",", ":"))),
            ]
        )
        content = getattr(response, "content", response)
        text = _extract_message_text(content)
        return _extract_json_payload(text)

    def _get_llm(self) -> Any:
        if self._llm is not None:
            return self._llm
        try:
            from langchain_openai import ChatOpenAI
        except Exception as exc:  # noqa: BLE001
            raise RuntimeError("langchain-openai is required for prompt-first skills") from exc
        self._llm = ChatOpenAI(
            model=self._model_name,
            base_url=self._base_url,
            api_key=self._api_key,
            timeout=self._timeout_seconds,
            temperature=0,
        )
        return self._llm
