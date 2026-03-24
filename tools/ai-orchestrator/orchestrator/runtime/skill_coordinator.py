"""Skill coordinator for capability-first skill execution.

This module provides the SkillCoordinator class that orchestrates the full
skill execution pipeline: selection → resource loading → execution.

Phase HM10: Main chain integration for execute_skill_script().
"""
from __future__ import annotations

from dataclasses import dataclass, field
from typing import TYPE_CHECKING, Any

if TYPE_CHECKING:
    from ..skills.runtime import SkillCatalog, SkillCandidate
    from ..skills.agent import PromptSkillAgent
    from .runtime import OrchestratorRuntime


@dataclass
class SkillAgentConfig:
    """Configuration for the skill selection agent.

    Required for LLM-driven skill and resource selection.
    """

    model: str
    base_url: str
    api_key: str
    timeout_seconds: float = 30.0


@dataclass
class SkillExecutionContext:
    """Context for skill execution.

    Carries state through the progressive disclosure pipeline.
    """

    capability: str
    input_payload: dict[str, Any]
    stage_summary: dict[str, Any]
    knowledge_context: list[dict[str, Any]] = field(default_factory=list)
    tool_results: list[dict[str, Any]] = field(default_factory=list)


@dataclass
class SkillExecutionResult:
    """Result from skill execution.

    Contains the output payload, session updates, and execution metadata.
    """

    success: bool
    payload: dict[str, Any] = field(default_factory=dict)
    session_patch: dict[str, Any] = field(default_factory=dict)
    observations: list[dict[str, Any]] = field(default_factory=list)
    tool_calls: list[dict[str, Any]] = field(default_factory=list)
    selected_executor_binding_key: str = ""
    selected_knowledge_binding_keys: list[str] = field(default_factory=list)
    fallback_used: bool = False
    error_message: str = ""


@dataclass
class CapabilityConfig:
    """Configuration for capability execution.

    Defines the behavior and constraints for each capability.
    """

    capability: str
    allow_tool_calling: bool = False
    max_tool_calls: int = 2
    allowed_tools: list[str] | None = None
    require_executor: bool = False


# Capability registry
CAPABILITY_CONFIGS: dict[str, CapabilityConfig] = {
    "evidence.plan": CapabilityConfig(
        capability="evidence.plan",
        allow_tool_calling=True,
        max_tool_calls=2,
        allowed_tools=["mcp.query_metrics", "mcp.query_logs"],
        require_executor=False,  # Can fallback to native
    ),
    "diagnosis.enrich": CapabilityConfig(
        capability="diagnosis.enrich",
        allow_tool_calling=False,
        require_executor=False,
    ),
}


class SkillCoordinator:
    """Coordinates skill selection, resource loading, and execution.

    This class implements the progressive disclosure pattern:
    Summary → Selection → Resource Loading → Execution

    The coordinator:
    1. Gets skill candidates from the catalog
    2. Uses LLM to select knowledge skills and executor skill
    3. Loads selected resources (progressive disclosure)
    4. Executes the skill (script or prompt mode)
    5. Handles tool calling for capabilities that support it
    6. Returns unified SkillExecutionResult
    """

    def __init__(
        self,
        *,
        catalog: "SkillCatalog",
        agent: "PromptSkillAgent",
        runtime: "OrchestratorRuntime",
    ) -> None:
        """Initialize the skill coordinator.

        Args:
            catalog: SkillCatalog for skill discovery and resource loading.
            agent: PromptSkillAgent for LLM-driven selection.
            runtime: OrchestratorRuntime for tool execution.
        """
        self._catalog = catalog
        self._agent = agent
        self._runtime = runtime

    def execute_capability_skill(
        self,
        capability: str,
        input_payload: dict[str, Any],
        stage_summary: dict[str, Any],
    ) -> SkillExecutionResult:
        """Execute a capability skill with full coordination.

        This is the main entry point for capability-first skill execution.
        Implements the progressive disclosure pattern:
        Summary → Selection → Resource Loading → Execution

        Args:
            capability: The capability to execute (e.g., "evidence.plan").
            input_payload: Input payload for the skill.
            stage_summary: Summary of current stage for selection context.

        Returns:
            SkillExecutionResult with payload, session_patch, observations.
        """
        config = CAPABILITY_CONFIGS.get(capability)
        if config is None:
            return SkillExecutionResult(
                success=False,
                error_message=f"unknown capability: {capability}",
            )

        ctx = SkillExecutionContext(
            capability=capability,
            input_payload=input_payload,
            stage_summary=stage_summary,
        )

        # Step 1: Get candidates
        knowledge_candidates = self._catalog.knowledge_candidates_for_capability(capability)
        executor_candidates = self._catalog.executor_candidates_for_capability(capability)

        if not executor_candidates and config.require_executor:
            return SkillExecutionResult(
                success=False,
                error_message=f"no executor candidates for {capability}",
            )

        # Step 2: Select knowledge skills (LLM)
        if knowledge_candidates and self._agent.configured:
            knowledge_result = self._agent.select_knowledge_skills(
                capability=capability,
                stage=capability,
                stage_summary=stage_summary,
                candidates=[c.to_summary_dict() for c in knowledge_candidates],
            )
            ctx.knowledge_context = [
                {"binding_key": bk, "role": "knowledge"}
                for bk in knowledge_result.selected_binding_keys
            ]

        # Step 3: Load knowledge skill resources
        for kc in ctx.knowledge_context:
            binding_key = kc["binding_key"]
            try:
                resources = self._load_skill_resources_with_selection(binding_key, ctx)
                kc["resources"] = resources
            except Exception:  # noqa: BLE001
                kc["resources"] = []

        # Step 4: Select executor skill (LLM)
        selected_binding_key = ""
        if executor_candidates and self._agent.configured:
            executor_result = self._agent.select_skill(
                capability=capability,
                stage=capability,
                stage_summary=stage_summary,
                candidates=[c.to_summary_dict() for c in executor_candidates],
            )
            selected_binding_key = executor_result.selected_binding_key

        if not selected_binding_key:
            # No skill selected, use native fallback
            return SkillExecutionResult(
                success=True,
                fallback_used=True,
                error_message="no skill selected, using native implementation",
                selected_knowledge_binding_keys=[
                    kc["binding_key"] for kc in ctx.knowledge_context
                ],
            )

        # Step 5: Load executor skill resources
        try:
            executor_resources = self._load_skill_resources_with_selection(
                selected_binding_key, ctx
            )
        except Exception:  # noqa: BLE001
            executor_resources = []

        # Step 6: Get executor candidate info
        executor_candidate = next(
            (c for c in executor_candidates if c.binding_key == selected_binding_key),
            None
        )
        if executor_candidate is None:
            return SkillExecutionResult(
                success=False,
                error_message=f"executor candidate not found: {selected_binding_key}",
            )

        # Step 7: Execute based on executor_mode
        try:
            if executor_candidate.executor_mode == "script":
                result = self._execute_script(
                    binding_key=selected_binding_key,
                    executor_candidate=executor_candidate,
                    input_payload=input_payload,
                    ctx=ctx,
                    config=config,
                    executor_resources=executor_resources,
                )
            else:  # prompt
                result = self._execute_prompt(
                    binding_key=selected_binding_key,
                    executor_candidate=executor_candidate,
                    input_payload=input_payload,
                    ctx=ctx,
                    executor_resources=executor_resources,
                )

            result.selected_executor_binding_key = selected_binding_key
            result.selected_knowledge_binding_keys = [
                kc["binding_key"] for kc in ctx.knowledge_context
            ]
            return result

        except Exception as e:
            return SkillExecutionResult(
                success=False,
                error_message=str(e),
                selected_executor_binding_key=selected_binding_key,
                selected_knowledge_binding_keys=[
                    kc["binding_key"] for kc in ctx.knowledge_context
                ],
            )

    def _load_skill_resources_with_selection(
        self,
        binding_key: str,
        ctx: SkillExecutionContext,
    ) -> list[dict[str, Any]]:
        """Select and load resources for a skill binding.

        Implements progressive disclosure for resource loading.

        Args:
            binding_key: The skill binding key.
            ctx: Execution context.

        Returns:
            List of loaded resource documents.
        """
        # List available resources
        summaries = self._catalog.list_skill_resources(binding_key)
        if not summaries:
            return []

        # Agent selects resources (if configured)
        if not self._agent.configured:
            return []

        skill_document = self._catalog.load_skill_document(binding_key)
        parts = binding_key.split("\x00")
        skill_id = parts[0] if len(parts) > 0 else ""
        skill_version = parts[1] if len(parts) > 1 else ""
        role = parts[3] if len(parts) > 3 else "executor"

        selection = self._agent.select_skill_resources(
            capability=ctx.capability,
            skill_id=skill_id,
            skill_version=skill_version,
            role=role,
            skill_document=skill_document,
            stage_summary=ctx.stage_summary,
            available_resources=[s.to_dict() for s in summaries],
            knowledge_context=ctx.knowledge_context,
        )

        # Load selected resources
        loaded = self._catalog.load_skill_resources(
            binding_key,
            selection.selected_resource_ids,
        )
        return [r.to_dict() for r in loaded]

    def _execute_script(
        self,
        binding_key: str,
        executor_candidate: "SkillCandidate",
        input_payload: dict[str, Any],
        ctx: SkillExecutionContext,
        config: CapabilityConfig,
        executor_resources: list[dict[str, Any]],
    ) -> SkillExecutionResult:
        """Execute script executor with optional tool calling.

        Args:
            binding_key: The skill binding key.
            executor_candidate: The selected executor candidate.
            input_payload: Input payload for the skill.
            ctx: Execution context.
            config: Capability configuration.
            executor_resources: Loaded executor resources.

        Returns:
            SkillExecutionResult from script execution.
        """
        # Build ctx for script
        initial_phase = "plan_tools" if config.allow_tool_calling else "final"
        tool_calling_mode = self._infer_tool_calling_mode(executor_candidate, config)

        # Execute initial phase
        result = self._runtime.execute_skill_script(
            skill_binding_key=binding_key,
            input_payload=input_payload,
            phase=initial_phase,
            knowledge_context=ctx.knowledge_context,
            skill_resources=executor_resources,
            allowed_tools=list(executor_candidate.allowed_tools),
            tool_calling_mode=tool_calling_mode,
        )

        # Handle tool calling for capabilities that support it (e.g., evidence.plan)
        if result.tool_calls and config.allow_tool_calling:
            tool_results = self._execute_tool_calls(result.tool_calls, config, executor_candidate)

            # Execute after_tools phase
            result = self._runtime.execute_skill_script(
                skill_binding_key=binding_key,
                input_payload=input_payload,
                phase="after_tools",
                tool_results=tool_results,
                knowledge_context=ctx.knowledge_context,
                skill_resources=executor_resources,
                allowed_tools=list(executor_candidate.allowed_tools),
                tool_calling_mode=tool_calling_mode,
            )

        return SkillExecutionResult(
            success=True,
            payload=result.payload,
            session_patch=result.session_patch,
            observations=result.observations,
            tool_calls=result.tool_calls,
        )

    def _execute_prompt(
        self,
        binding_key: str,
        executor_candidate: "SkillCandidate",
        input_payload: dict[str, Any],
        ctx: SkillExecutionContext,
        executor_resources: list[dict[str, Any]] | None = None,
    ) -> SkillExecutionResult:
        """Execute prompt executor.

        Args:
            binding_key: The skill binding key.
            executor_candidate: The selected executor candidate.
            input_payload: Input payload for the skill.
            ctx: Execution context.
            executor_resources: Loaded executor resources.

        Returns:
            SkillExecutionResult from prompt execution.
        """
        skill_document = self._catalog.load_skill_document(binding_key)
        parts = binding_key.split("\x00")
        skill_id = parts[0] if len(parts) > 0 else ""
        skill_version = parts[1] if len(parts) > 1 else ""

        result = self._agent.consume_skill(
            capability=ctx.capability,
            skill_id=skill_id,
            skill_version=skill_version,
            skill_document=skill_document,
            input_payload=input_payload,
            knowledge_context=ctx.knowledge_context,
            skill_resources=executor_resources or [],
            output_contract={},
        )

        return SkillExecutionResult(
            success=True,
            payload=result.payload,
            session_patch=result.session_patch,
            observations=result.observations,
        )

    def _infer_tool_calling_mode(
        self,
        candidate: "SkillCandidate",
        config: CapabilityConfig,
    ) -> str:
        """Infer tool_calling_mode from candidate and config.

        Args:
            candidate: The selected executor candidate.
            config: Capability configuration.

        Returns:
            Tool calling mode string.
        """
        if not config.allow_tool_calling:
            return "disabled"

        allowed = set(candidate.allowed_tools)
        has_metrics = "mcp.query_metrics" in allowed
        has_logs = "mcp.query_logs" in allowed

        if has_metrics and has_logs:
            return "evidence_plan_dual_tool"
        elif has_logs:
            return "evidence_plan_single_hop"
        else:
            return "disabled"

    def _execute_tool_calls(
        self,
        tool_calls: list[dict[str, Any]],
        config: CapabilityConfig,
        candidate: "SkillCandidate",
    ) -> list[dict[str, Any]]:
        """Execute tool calls with enforcement of allowed_tools and max_tool_calls.

        Args:
            tool_calls: List of tool call requests.
            config: Capability configuration.
            candidate: The selected executor candidate.

        Returns:
            List of tool call results.
        """
        results: list[dict[str, Any]] = []
        allowed_tools = set(config.allowed_tools or []) | set(candidate.allowed_tools)
        max_calls = config.max_tool_calls

        executed_count = 0
        for tc in tool_calls:
            tool_name = tc.get("tool", "")
            tool_input = tc.get("input", {})

            # Enforce max_tool_calls limit
            if executed_count >= max_calls:
                results.append({
                    "tool": tool_name,
                    "input": tool_input,
                    "status": "rejected",
                    "error": f"max_tool_calls ({max_calls}) exceeded",
                })
                continue

            # Enforce allowed_tools limit
            if tool_name not in allowed_tools:
                results.append({
                    "tool": tool_name,
                    "input": tool_input,
                    "status": "rejected",
                    "error": f"tool '{tool_name}' not in allowed_tools: {sorted(allowed_tools)}",
                })
                continue

            try:
                output = self._runtime.call_tool(tool_name, tool_input)
                results.append({
                    "tool": tool_name,
                    "input": tool_input,
                    "output": output,
                    "status": "ok",
                })
                executed_count += 1
            except Exception as e:
                results.append({
                    "tool": tool_name,
                    "input": tool_input,
                    "error": str(e),
                    "status": "error",
                })
                # Count errors toward the limit too
                executed_count += 1

        return results