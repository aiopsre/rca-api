"""Session middleware for injecting session context into agent requests."""
from __future__ import annotations

from typing import TYPE_CHECKING, Any

from .base import AgentMiddleware, AgentRequest, AgentResponse

if TYPE_CHECKING:
    from ..state import GraphState
    from ..runtime.resolved_context import ResolvedAgentContext


class SessionMiddleware(AgentMiddleware):
    """Middleware that injects session context into prompts.

    This middleware reads session_snapshot from the context and
    adds relevant session information to the agent request.
    """

    def prepare(
        self,
        state: "GraphState",
        context: "ResolvedAgentContext",
        request: AgentRequest,
        config: dict[str, Any],
    ) -> AgentRequest:
        """Inject session context into the request.

        Config options:
        - include_session: Whether to include session snapshot (default: True)
        - include_incident: Whether to include incident context (default: True)
        """
        include_session = config.get("include_session", True)
        include_incident = config.get("include_incident", True)

        session_parts: list[str] = []

        if include_session and context.session_snapshot:
            session_parts.append(f"Session Context: {context.session_snapshot}")

        if include_incident and state.incident_context:
            session_parts.append(f"Incident Context: {state.incident_context}")

        if session_parts:
            existing_prompt = request.user_prompt
            session_block = "\n\n".join(session_parts)
            request.user_prompt = f"{session_block}\n\n{existing_prompt}"

        return request