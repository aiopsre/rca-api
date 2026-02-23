from __future__ import annotations

import json
from typing import Any

from .capabilities import PromptSkillConsumeResult


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


class SkillSelectionResult:
    def __init__(self, *, selected_binding_key: str = "", reason: str = "") -> None:
        self.selected_binding_key = selected_binding_key
        self.reason = reason


class KnowledgeSelectionResult:
    def __init__(self, *, selected_binding_keys: list[str] | None = None, reason: str = "") -> None:
        normalized: list[str] = []
        seen: set[str] = set()
        for item in selected_binding_keys or []:
            binding_key = _trim(item)
            if not binding_key or binding_key in seen:
                continue
            seen.add(binding_key)
            normalized.append(binding_key)
        self.selected_binding_keys = normalized
        self.reason = reason


class ExecutorSelectionResult(SkillSelectionResult):
    pass


class DiagnosisEnrichOutput:
    def __init__(
        self,
        *,
        diagnosis_patch: dict[str, Any] | None = None,
        session_patch: dict[str, Any] | None = None,
        observations: list[dict[str, Any]] | None = None,
    ) -> None:
        self.diagnosis_patch = diagnosis_patch if isinstance(diagnosis_patch, dict) else {}
        self.session_patch = session_patch if isinstance(session_patch, dict) else {}
        self.observations = [item for item in (observations or []) if isinstance(item, dict)]


class ToolCallPlan:
    def __init__(
        self,
        *,
        tool: str = "",
        input_payload: dict[str, Any] | None = None,
        reason: str = "",
    ) -> None:
        self.tool = _trim(tool)
        self.input_payload = input_payload if isinstance(input_payload, dict) else {}
        self.reason = _trim(reason)


class ToolCallSequence:
    def __init__(
        self,
        *,
        tool_calls: list[ToolCallPlan] | list[dict[str, Any]] | None = None,
        reason: str = "",
    ) -> None:
        normalized: list[ToolCallPlan] = []
        for item in tool_calls or []:
            if isinstance(item, ToolCallPlan):
                normalized.append(item)
                continue
            if isinstance(item, dict):
                normalized.append(
                    ToolCallPlan(
                        tool=_trim(item.get("tool")),
                        input_payload=item.get("input") if isinstance(item.get("input"), dict) else {},
                        reason=_trim(item.get("reason")),
                    )
                )
        self.tool_calls = normalized
        self.reason = _trim(reason)


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
    ) -> ExecutorSelectionResult:
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
        return ExecutorSelectionResult(
            selected_binding_key=_trim(response.get("selected_binding_key")),
            reason=_trim(response.get("reason")),
        )

    def select_knowledge_skills(
        self,
        *,
        capability: str,
        stage: str,
        stage_summary: dict[str, Any],
        candidates: list[dict[str, Any]],
    ) -> KnowledgeSelectionResult:
        if not self.configured:
            raise RuntimeError("prompt skill agent is not configured")
        system_prompt = (
            "You are selecting zero or more optional RCA knowledge skills for the current stage.\n"
            "Knowledge skills only provide extra context and do not execute on their own.\n"
            "Return strict JSON with keys selected_binding_keys and reason.\n"
            "If none are useful, return an empty selected_binding_keys list."
        )
        user_payload = {
            "capability": capability,
            "stage": stage,
            "stage_summary": stage_summary,
            "candidates": candidates,
            "output_contract": {
                "selected_binding_keys": "array of binding keys",
                "reason": "short explanation",
            },
        }
        response = self._invoke_json(system_prompt=system_prompt, user_payload=user_payload)
        selected_binding_keys = response.get("selected_binding_keys")
        if not isinstance(selected_binding_keys, list):
            selected_binding_keys = []
        return KnowledgeSelectionResult(
            selected_binding_keys=[_trim(item) for item in selected_binding_keys],
            reason=_trim(response.get("reason")),
        )

    def consume_skill(
        self,
        *,
        capability: str,
        skill_id: str,
        skill_version: str,
        skill_document: str,
        input_payload: dict[str, Any],
        knowledge_context: list[dict[str, Any]] | None = None,
        output_contract: dict[str, Any],
    ) -> PromptSkillConsumeResult:
        if not self.configured:
            raise RuntimeError("prompt skill agent is not configured")
        system_prompt = (
            "You are applying a prompt-first RCA skill.\n"
            "Read the skill document and return strict JSON only.\n"
            "Do not call tools.\n"
            "Return only payload, session_patch, and observations.\n"
            "Respect the provided output_contract exactly."
        )
        user_payload = {
            "capability": capability,
            "skill_id": skill_id,
            "skill_version": skill_version,
            "skill_document": skill_document,
            "input": input_payload,
            "knowledge_context": [item for item in (knowledge_context or []) if isinstance(item, dict)],
            "output_contract": output_contract,
        }
        response = self._invoke_json(system_prompt=system_prompt, user_payload=user_payload)
        payload = response.get("payload")
        session_patch = response.get("session_patch")
        observations = response.get("observations")
        return PromptSkillConsumeResult(
            payload=payload if isinstance(payload, dict) else {},
            session_patch=session_patch if isinstance(session_patch, dict) else {},
            observations=[item for item in observations if isinstance(item, dict)] if isinstance(observations, list) else [],
        )

    def plan_tool_call(
        self,
        *,
        capability: str,
        skill_id: str,
        skill_version: str,
        skill_document: str,
        input_payload: dict[str, Any],
        knowledge_context: list[dict[str, Any]] | None = None,
        available_tools: list[str],
    ) -> ToolCallPlan:
        sequence = self.plan_tool_calls(
            capability=capability,
            skill_id=skill_id,
            skill_version=skill_version,
            skill_document=skill_document,
            input_payload=input_payload,
            knowledge_context=knowledge_context,
            available_tools=available_tools,
            max_tool_calls=1,
        )
        if sequence.tool_calls:
            return sequence.tool_calls[0]
        return ToolCallPlan(reason=sequence.reason)

    def plan_tool_calls(
        self,
        *,
        capability: str,
        skill_id: str,
        skill_version: str,
        skill_document: str,
        input_payload: dict[str, Any],
        knowledge_context: list[dict[str, Any]] | None = None,
        available_tools: list[str],
        max_tool_calls: int = 2,
    ) -> ToolCallSequence:
        if not self.configured:
            raise RuntimeError("prompt skill agent is not configured")
        system_prompt = (
            "You are planning a bounded sequence of tool calls for a prompt-first RCA skill.\n"
            "Return strict JSON only.\n"
            "If no tool call is needed, return an empty tool_calls list.\n"
            "Only choose from available_tools.\n"
            "Never return more than max_tool_calls items.\n"
            "Do not repeat the same tool in tool_calls.\n"
            "For this workflow, never emit raw Elasticsearch DSL; only emit the allowed query string input."
        )
        user_payload = {
            "capability": capability,
            "skill_id": skill_id,
            "skill_version": skill_version,
            "skill_document": skill_document,
            "input": input_payload,
            "knowledge_context": [item for item in (knowledge_context or []) if isinstance(item, dict)],
            "available_tools": [item for item in available_tools if _trim(item)],
            "max_tool_calls": max(int(max_tool_calls), 1),
            "output_contract": {
                "tool_calls": [
                    {
                        "tool": "string or empty",
                        "input": {
                            "datasource_id": "required string when tool is set",
                            "query": "required string for mcp.query_logs",
                            "promql": "required string for mcp.query_metrics",
                            "start_ts": "required integer when tool is set",
                            "end_ts": "required integer when tool is set",
                            "limit": "required integer for mcp.query_logs",
                            "step_seconds": "required integer for mcp.query_metrics",
                        },
                        "reason": "short explanation",
                    }
                ],
                "reason": "short explanation",
            },
        }
        response = self._invoke_json(system_prompt=system_prompt, user_payload=user_payload)
        tool_calls = response.get("tool_calls")
        if not isinstance(tool_calls, list):
            tool_calls = []
        return ToolCallSequence(
            tool_calls=[item for item in tool_calls if isinstance(item, dict)],
            reason=_trim(response.get("reason")),
        )

    def consume_after_tool(
        self,
        *,
        capability: str,
        skill_id: str,
        skill_version: str,
        skill_document: str,
        input_payload: dict[str, Any],
        knowledge_context: list[dict[str, Any]] | None = None,
        tool_request: dict[str, Any],
        tool_result: dict[str, Any],
        output_contract: dict[str, Any],
    ) -> PromptSkillConsumeResult:
        return self.consume_after_tools(
            capability=capability,
            skill_id=skill_id,
            skill_version=skill_version,
            skill_document=skill_document,
            input_payload=input_payload,
            knowledge_context=knowledge_context,
            tool_results=[
                {
                    "tool_request": tool_request if isinstance(tool_request, dict) else {},
                    "tool_result": tool_result if isinstance(tool_result, dict) else {},
                }
            ],
            output_contract=output_contract,
        )

    def consume_after_tools(
        self,
        *,
        capability: str,
        skill_id: str,
        skill_version: str,
        skill_document: str,
        input_payload: dict[str, Any],
        knowledge_context: list[dict[str, Any]] | None = None,
        tool_results: list[dict[str, Any]] | None = None,
        output_contract: dict[str, Any],
    ) -> PromptSkillConsumeResult:
        if not self.configured:
            raise RuntimeError("prompt skill agent is not configured")
        system_prompt = (
            "You are finishing a prompt-first RCA skill after zero or more controlled tool calls.\n"
            "Read the skill document, the original stage input, and the tool results.\n"
            "Return strict JSON only.\n"
            "Do not plan or call more tools.\n"
            "Return only payload, session_patch, and observations.\n"
            "Respect the provided output_contract exactly."
        )
        user_payload = {
            "capability": capability,
            "skill_id": skill_id,
            "skill_version": skill_version,
            "skill_document": skill_document,
            "input": input_payload,
            "knowledge_context": [item for item in (knowledge_context or []) if isinstance(item, dict)],
            "tool_results": [item for item in (tool_results or []) if isinstance(item, dict)],
            "output_contract": output_contract,
        }
        response = self._invoke_json(system_prompt=system_prompt, user_payload=user_payload)
        payload = response.get("payload")
        session_patch = response.get("session_patch")
        observations = response.get("observations")
        return PromptSkillConsumeResult(
            payload=payload if isinstance(payload, dict) else {},
            session_patch=session_patch if isinstance(session_patch, dict) else {},
            observations=[item for item in observations if isinstance(item, dict)] if isinstance(observations, list) else [],
        )

    def run_diagnosis_enrich(
        self,
        *,
        skill_id: str,
        skill_version: str,
        skill_document: str,
        input_payload: dict[str, Any],
    ) -> DiagnosisEnrichOutput:
        result = self.consume_skill(
            capability="diagnosis.enrich",
            skill_id=skill_id,
            skill_version=skill_version,
            skill_document=skill_document,
            input_payload=input_payload,
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
        )
        diagnosis_patch = result.payload.get("diagnosis_patch") if isinstance(result.payload, dict) else {}
        return DiagnosisEnrichOutput(
            diagnosis_patch=diagnosis_patch if isinstance(diagnosis_patch, dict) else {},
            session_patch=result.session_patch,
            observations=result.observations,
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
