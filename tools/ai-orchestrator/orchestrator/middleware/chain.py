"""Middleware chain for orchestrating middleware execution."""
from __future__ import annotations

from typing import TYPE_CHECKING, Any

from .base import AgentMiddleware, AgentRequest, AgentResponse

if TYPE_CHECKING:
    from ..state import GraphState
    from ..runtime.resolved_context import ResolvedAgentContext


class MiddlewareChain:
    """Chain of middlewares that process requests and responses."""

    def __init__(self, middlewares: list[AgentMiddleware] | None = None) -> None:
        self._middlewares = list(middlewares or [])

    def add(self, middleware: AgentMiddleware) -> "MiddlewareChain":
        """Add a middleware to the chain."""
        self._middlewares.append(middleware)
        return self

    def prepare(
        self,
        state: "GraphState",
        context: "ResolvedAgentContext",
        request: AgentRequest,
        config: dict[str, Any],
    ) -> AgentRequest:
        """Run all middlewares' prepare() in order."""
        current = request
        for mw in self._middlewares:
            current = mw.prepare(state, context, current, config)
        return current

    def after_llm(
        self,
        state: "GraphState",
        context: "ResolvedAgentContext",
        response: AgentResponse,
        config: dict[str, Any],
    ) -> AgentResponse:
        """Run all middlewares' after_llm() in order."""
        current = response
        for mw in self._middlewares:
            current = mw.after_llm(state, context, current, config)
        return current

    def is_empty(self) -> bool:
        """Check if the chain has no middlewares."""
        return len(self._middlewares) == 0