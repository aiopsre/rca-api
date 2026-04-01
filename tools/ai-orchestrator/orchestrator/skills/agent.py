"""Skill agent for prompt-based skill execution.

DEPRECATED (HM6): The capability-first pattern is no longer the primary execution model.
- For Route Agent + Domain Agents path, see nodes_router.py and nodes_agents.py
- For Platform Special Agent, see nodes_platform.py

This module is retained for backward compatibility with skill-based enrichment
via consume_prompt_skill() and consume_diagnosis_enrich_skill().
"""
from __future__ import annotations

import copy
import json
from typing import Any

from ..langgraph.llm_logging import log_llm_dialogue
from ..runtime.tool_registry import get_tool_metadata
from ..tooling.canonical_names import normalize_tool_name
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


def _build_tool_input_contract(tool_name: str) -> dict[str, str]:
    """Build input field descriptions for a tool based on its kind.

    Args:
        tool_name: The tool name to look up.

    Returns:
        Dictionary mapping field names to descriptions.
    """
    # Normalize tool name to canonical form for registry lookup
    normalized_name = normalize_tool_name(tool_name)
    meta = get_tool_metadata(normalized_name)
    kind = meta.kind if meta else "unknown"

    # Common fields for all query tools
    contract = {
        "datasource_id": "required string when tool is set",
        "start_ts": "required integer when tool is set",
        "end_ts": "required integer when tool is set",
    }

    # Kind-specific fields
    if kind == "logs":
        contract["query"] = f"required string for {tool_name}"
        contract["limit"] = f"required integer for {tool_name}"
    elif kind == "metrics":
        contract["promql"] = f"required string for {tool_name}"
        contract["step_seconds"] = f"required integer for {tool_name}"
    else:
        # Unknown kind - include both sets of fields
        contract["query"] = f"string for logs-like tools"
        contract["promql"] = f"string for metrics-like tools"
        contract["limit"] = f"integer for logs-like tools"
        contract["step_seconds"] = f"integer for metrics-like tools"

    return contract


def _augment_openai_tools_for_skill_document(
    openai_tools: list[dict[str, Any]],
    skill_document: str,
) -> list[dict[str, Any]]:
    """Augment tool descriptions/schemas from the skill document when needed.

    Prompt-first skills that wrap an MCP server can expose a richer tool
    description than the runtime registry currently carries. This helper
    patches the OpenAI tool surface for those prompt-first flows so the LLM
    sees tool intent and required arguments, even when platform metadata is
    sparse.
    """
    tools = copy.deepcopy([item for item in openai_tools if isinstance(item, dict)])
    doc = str(skill_document or "").lower()

    tempo_skill = "tempo_get_trace" in doc
    if not tempo_skill:
        return tools
    get_trace_only = "tempo_query" not in doc
    if get_trace_only:
        tools = [
            entry
            for entry in tools
            if not (
                isinstance(entry, dict)
                and isinstance(entry.get("function"), dict)
                and _trim(entry["function"].get("name")) == "tempo_query"
            )
        ]

    for entry in tools:
        if not isinstance(entry, dict):
            continue
        function = entry.get("function")
        if not isinstance(function, dict):
            continue
        tool_name = _trim(function.get("name"))
        if tool_name == "tempo_query" and "tempo_query" in doc:
            description = _trim(function.get("description"))
            tempo_description = (
                "Query Tempo traces when a trace ID is not yet known. "
                "Use this tool to find candidate traces for the incident window. "
                "Pass the time window via start/end arguments, and keep the query string "
                "focused on the Tempo search expression only."
            )
            function["description"] = (
                f"{description} {tempo_description}".strip()
                if description
                else tempo_description
            )
            parameters = function.get("parameters")
            if not isinstance(parameters, dict):
                parameters = {}
                function["parameters"] = parameters
            properties = parameters.get("properties")
            if not isinstance(properties, dict):
                properties = {}
                parameters["properties"] = properties
            properties.setdefault(
                "query",
                {
                    "type": "string",
                    "description": "Tempo search expression used to search candidate traces.",
                },
            )
            properties.setdefault(
                "start",
                {
                    "type": "string",
                    "description": "Optional start time for the Tempo search window.",
                },
            )
            properties.setdefault(
                "end",
                {
                    "type": "string",
                    "description": "Optional end time for the Tempo search window.",
                },
            )
            properties.setdefault(
                "limit",
                {
                    "type": "number",
                    "description": "Optional maximum number of traces to return.",
                },
            )
            required = parameters.get("required")
            if not isinstance(required, list):
                required = []
            if "query" not in required:
                required.append("query")
            parameters["required"] = required
            parameters.setdefault("additionalProperties", False)
        elif tool_name == "tempo_get_trace":
            description = _trim(function.get("description"))
            tempo_description = (
                "Fetch a Tempo trace by trace ID when the trace ID is already known. "
                "Use this tool to inspect spans and dependency timing."
            )
            function["description"] = (
                f"{description} {tempo_description}".strip()
                if description
                else tempo_description
            )
            parameters = function.get("parameters")
            if not isinstance(parameters, dict):
                parameters = {}
                function["parameters"] = parameters
            properties = parameters.get("properties")
            if not isinstance(properties, dict):
                properties = {}
                parameters["properties"] = properties
            properties.setdefault(
                "trace_id",
                {
                    "type": "string",
                    "description": "Tempo trace identifier.",
                },
            )
            required = parameters.get("required")
            if not isinstance(required, list):
                required = []
            if "trace_id" not in required:
                required.append("trace_id")
            parameters["required"] = required
            parameters.setdefault("additionalProperties", False)
    return tools


def _tempo_tool_usage_hints(skill_document: str) -> list[str]:
    doc = str(skill_document or "").lower()
    if "tempo_get_trace" not in doc:
        return []
    if "tempo_query" not in doc:
        return [
            "Use tempo_get_trace only when a trace_id is already available.",
            "Do not search for traces with tempo_query in this skill.",
        ]
    return [
        "For tempo_query, keep query to the Tempo search expression only.",
        "Pass the time window via start and end tool arguments; do not embed @timestamp filters in query.",
        "Use tempo_get_trace only after you already know a trace_id.",
    ]


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


class SkillResourceSelectionResult:
    def __init__(self, *, selected_resource_ids: list[str] | None = None, reason: str = "") -> None:
        normalized: list[str] = []
        seen: set[str] = set()
        for item in selected_resource_ids or []:
            resource_id = _trim(item)
            if not resource_id or resource_id in seen:
                continue
            seen.add(resource_id)
            normalized.append(resource_id)
        self.selected_resource_ids = normalized
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
    """Skill agent for prompt-based skill execution.

    DEPRECATED (HM6): This class is retained for backward compatibility.
    For the new agent-based approach, see nodes_router.py and nodes_agents.py.
    """

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
        skill_resources: list[dict[str, Any]] | None = None,
        output_contract: dict[str, Any],
    ) -> PromptSkillConsumeResult:
        if not self.configured:
            raise RuntimeError("prompt skill agent is not configured")
        system_prompt = (
            "You are applying a prompt-first RCA skill.\n"
            "Read the skill document and return strict JSON only.\n"
            "If the runtime exposes tools for this skill, use them when they are needed to investigate evidence.\n"
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
            "skill_resources": [item for item in (skill_resources or []) if isinstance(item, dict)],
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
        skill_resources: list[dict[str, Any]] | None = None,
        available_tools: list[str],
    ) -> ToolCallPlan:
        sequence = self.plan_tool_calls(
            capability=capability,
            skill_id=skill_id,
            skill_version=skill_version,
            skill_document=skill_document,
            input_payload=input_payload,
            knowledge_context=knowledge_context,
            skill_resources=skill_resources,
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
        skill_resources: list[dict[str, Any]] | None = None,
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
            "skill_resources": [item for item in (skill_resources or []) if isinstance(item, dict)],
            "available_tools": [item for item in available_tools if _trim(item)],
            "max_tool_calls": max(int(max_tool_calls), 1),
            "output_contract": {
                "tool_calls": [
                    {
                        "tool": "string or empty",
                        "input": _build_tool_input_contract(available_tools[0]) if available_tools else {},
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

    def plan_tool_calls_fc(
        self,
        *,
        capability: str,
        skill_id: str,
        skill_version: str,
        skill_document: str,
        input_payload: dict[str, Any],
        knowledge_context: list[dict[str, Any]] | None = None,
        skill_resources: list[dict[str, Any]] | None = None,
        adapter: Any,  # FunctionCallingToolAdapter - avoid circular import
        max_tool_calls: int = 2,
    ) -> list[Any]:  # list[NormalizedToolCall]
        """Plan tool calls using function calling instead of JSON parsing.

        FC3A: This method now receives the FunctionCallingToolAdapter directly
        and uses its normalize_tool_calls() method, removing duplicate logic.

        Args:
            capability: The capability being executed (e.g., "evidence.plan").
            skill_id: The skill identifier.
            skill_version: The skill version.
            skill_document: The skill document content.
            input_payload: The input payload for the skill.
            knowledge_context: Optional knowledge context from knowledge skills.
            skill_resources: Optional skill resources.
            adapter: FunctionCallingToolAdapter instance (FC3A unified adapter).
            max_tool_calls: Maximum number of tool calls to allow.

        Returns:
            List of NormalizedToolCall instances from adapter.normalize_tool_calls().
        """
        if not self.configured:
            raise RuntimeError("prompt skill agent is not configured")

        # FC3A: Get OpenAI tools from the adapter (single source of truth)
        # Use per-surface filtering for Skills visibility
        openai_tools = _augment_openai_tools_for_skill_document(
            adapter.to_openai_tools_for_skills(),
            skill_document,
        )
        if not openai_tools:
            return []

        llm = self._get_llm()
        # Bind tools to LLM for function calling
        llm_with_tools = llm.bind_tools(openai_tools)

        system_prompt = (
            "You are planning a bounded sequence of tool calls for a prompt-first RCA skill.\n"
            "Use the provided tools to gather evidence whenever the skill is about external systems, traces, logs, metrics, or similar RCA evidence.\n"
            "Prefer at least one tool call when relevant tools are available.\n"
            "If no tool call is needed, return without calling any tools.\n"
            "Never call more than max_tool_calls tools.\n"
            "Do not call the same tool twice.\n"
        )
        user_payload = {
            "capability": capability,
            "skill_id": skill_id,
            "skill_version": skill_version,
            "skill_document": skill_document,
            "tool_usage_hints": _tempo_tool_usage_hints(skill_document),
            "input": input_payload,
            "knowledge_context": [item for item in (knowledge_context or []) if isinstance(item, dict)],
            "skill_resources": [item for item in (skill_resources or []) if isinstance(item, dict)],
            "max_tool_calls": max(int(max_tool_calls), 1),
        }

        try:
            from langchain_core.messages import HumanMessage, SystemMessage
        except Exception as exc:
            raise RuntimeError("langchain-core is required for prompt-first skills") from exc

        messages = [
            SystemMessage(content=system_prompt),
            HumanMessage(content=json.dumps(user_payload, ensure_ascii=False, separators=(",", ":"))),
        ]
        log_llm_dialogue(
            event="request",
            node_name="prompt_skill_agent",
            messages=messages,
            tools=openai_tools,
            extra={
                "capability": capability,
                "skill_id": skill_id,
                "mode": "plan_tool_calls_fc",
            },
        )
        try:
            response = llm_with_tools.invoke(messages)
        except Exception as exc:  # noqa: BLE001
            log_llm_dialogue(
                event="error",
                node_name="prompt_skill_agent",
                messages=messages,
                tools=openai_tools,
                error=exc,
                extra={
                    "capability": capability,
                    "skill_id": skill_id,
                    "mode": "plan_tool_calls_fc",
                },
            )
            raise
        log_llm_dialogue(
            event="response",
            node_name="prompt_skill_agent",
            messages=messages,
            tools=openai_tools,
            response=response,
            extra={
                "capability": capability,
                "skill_id": skill_id,
                "mode": "plan_tool_calls_fc",
            },
        )

        # FC3A: Use adapter's normalize_tool_calls() - single source of truth
        tool_calls = getattr(response, "tool_calls", []) or []

        # Early validation: reject overlong sequences
        if len(tool_calls) > max_tool_calls:
            raise RuntimeError(f"FC tool_calls exceeds max_tool_calls: {len(tool_calls)} > {max_tool_calls}")

        # Normalize using the adapter (FC3A unified normalization)
        normalized = adapter.normalize_tool_calls(tool_calls)

        # Check for duplicates (validation not in adapter.normalize_tool_calls)
        seen: set[str] = set()
        for call in normalized:
            if call.tool_name in seen:
                raise RuntimeError(f"FC tool_calls contains duplicate tool: {call.tool_name}")
            seen.add(call.tool_name)

        return normalized

    def consume_after_tool(
        self,
        *,
        capability: str,
        skill_id: str,
        skill_version: str,
        skill_document: str,
        input_payload: dict[str, Any],
        knowledge_context: list[dict[str, Any]] | None = None,
        skill_resources: list[dict[str, Any]] | None = None,
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
            skill_resources=skill_resources,
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
        skill_resources: list[dict[str, Any]] | None = None,
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
            "skill_resources": [item for item in (skill_resources or []) if isinstance(item, dict)],
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

    def select_skill_resources(
        self,
        *,
        capability: str,
        skill_id: str,
        skill_version: str,
        role: str,
        skill_document: str,
        stage_summary: dict[str, Any],
        available_resources: list[dict[str, Any]],
        knowledge_context: list[dict[str, Any]] | None = None,
    ) -> SkillResourceSelectionResult:
        if not self.configured:
            raise RuntimeError("prompt skill agent is not configured")
        system_prompt = (
            "You are selecting zero or more supporting resources for a prompt-first RCA skill.\n"
            "Read the skill document and the resource summaries, then choose only the files that are clearly useful.\n"
            "Do not ask for every resource by default.\n"
            "Return strict JSON with keys selected_resource_ids and reason."
        )
        user_payload = {
            "capability": capability,
            "skill_id": skill_id,
            "skill_version": skill_version,
            "role": role,
            "skill_document": skill_document,
            "stage_summary": stage_summary,
            "available_resources": [item for item in available_resources if isinstance(item, dict)],
            "knowledge_context": [item for item in (knowledge_context or []) if isinstance(item, dict)],
            "output_contract": {
                "selected_resource_ids": "array of resource ids",
                "reason": "short explanation",
            },
        }
        response = self._invoke_json(system_prompt=system_prompt, user_payload=user_payload)
        selected_resource_ids = response.get("selected_resource_ids")
        if not isinstance(selected_resource_ids, list):
            selected_resource_ids = []
        return SkillResourceSelectionResult(
            selected_resource_ids=[_trim(item) for item in selected_resource_ids],
            reason=_trim(response.get("reason")),
        )

    def _invoke_json(self, *, system_prompt: str, user_payload: dict[str, Any]) -> dict[str, Any]:
        llm = self._get_llm()
        try:
            from langchain_core.messages import HumanMessage, SystemMessage
        except Exception as exc:  # noqa: BLE001
            raise RuntimeError("langchain-core is required for prompt-first skills") from exc
        messages = [
            SystemMessage(content=system_prompt),
            HumanMessage(content=json.dumps(user_payload, ensure_ascii=False, separators=(",", ":"))),
        ]
        log_llm_dialogue(
            event="request",
            node_name="prompt_skill_agent",
            messages=messages,
            extra={
                "capability": user_payload.get("capability", ""),
                "skill_id": user_payload.get("skill_id", ""),
            },
        )
        try:
            response = llm.invoke(messages)
        except Exception as exc:  # noqa: BLE001
            log_llm_dialogue(
                event="error",
                node_name="prompt_skill_agent",
                messages=messages,
                error=exc,
                extra={
                    "capability": user_payload.get("capability", ""),
                    "skill_id": user_payload.get("skill_id", ""),
                },
            )
            raise
        log_llm_dialogue(
            event="response",
            node_name="prompt_skill_agent",
            messages=messages,
            response=response,
            extra={
                "capability": user_payload.get("capability", ""),
                "skill_id": user_payload.get("skill_id", ""),
            },
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
