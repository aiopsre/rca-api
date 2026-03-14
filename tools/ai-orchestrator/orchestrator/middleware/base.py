"""Middleware base classes for hybrid multi-agent system."""
from __future__ import annotations

from dataclasses import dataclass, field
from typing import TYPE_CHECKING, Any

if TYPE_CHECKING:
    from ..state import GraphState
    from ..runtime.resolved_context import ResolvedAgentContext


@dataclass
class AgentRequest:
    """Request object passed through middleware chain.

    Attributes:
        system_prompt: System prompt for the LLM.
        user_prompt: User prompt for the LLM.
        visible_tools: List of tools visible to the agent.
        metadata: Additional metadata for the request.
    """

    system_prompt: str
    user_prompt: str
    visible_tools: list[dict[str, Any]] = field(default_factory=list)
    metadata: dict[str, Any] = field(default_factory=dict)


@dataclass
class AgentResponse:
    """Response object passed through middleware chain.

    Attributes:
        content: Raw response content from the LLM.
        parsed: Parsed response data.
        tool_calls: Tool calls extracted from the response.
        metadata: Additional metadata for the response.
    """

    content: str = ""
    parsed: dict[str, Any] = field(default_factory=dict)
    tool_calls: list[Any] = field(default_factory=list)
    metadata: dict[str, Any] = field(default_factory=dict)


class AgentMiddleware:
    """Base class for agent middleware.

    Middleware classes can override prepare() and after_llm() to
    modify requests and responses as they pass through the chain.
    """

    def prepare(
        self,
        state: "GraphState",
        context: "ResolvedAgentContext",
        request: AgentRequest,
        config: dict[str, Any],
    ) -> AgentRequest:
        """Prepare request before LLM call.

        Args:
            state: Current graph state.
            context: Resolved agent context.
            request: Request to prepare.
            config: Middleware configuration.

        Returns:
            Modified request.
        """
        return request

    def after_llm(
        self,
        state: "GraphState",
        context: "ResolvedAgentContext",
        response: AgentResponse,
        config: dict[str, Any],
    ) -> AgentResponse:
        """Process response after LLM call.

        Args:
            state: Current graph state.
            context: Resolved agent context.
            response: Response to process.
            config: Middleware configuration.

        Returns:
            Modified response.
        """
        return response