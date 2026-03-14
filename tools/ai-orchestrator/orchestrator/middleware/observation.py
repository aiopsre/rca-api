"""Observation middleware for recording agent interactions."""
from __future__ import annotations

from typing import TYPE_CHECKING, Any

from .base import AgentMiddleware, AgentRequest, AgentResponse

if TYPE_CHECKING:
    from ..state import GraphState
    from ..runtime.resolved_context import ResolvedAgentContext


class ObservationMiddleware(AgentMiddleware):
    """Middleware that records observations for audit and debugging.

    This middleware captures request/response pairs for later analysis.
    """

    def prepare(
        self,
        state: "GraphState",
        context: "ResolvedAgentContext",
        request: AgentRequest,
        config: dict[str, Any],
    ) -> AgentRequest:
        """Record request in metadata.

        Config options:
        - observation_type: Type of observation to record
        - domain: Domain for the observation (e.g., "router", "observability")
        """
        observation_type = config.get("observation_type", "agent.request")
        domain = config.get("domain", "unknown")

        request.metadata["observation_type"] = observation_type
        request.metadata["domain"] = domain
        request.metadata["job_id"] = context.job_id
        request.metadata["template_id"] = context.template_id

        return request

    def after_llm(
        self,
        state: "GraphState",
        context: "ResolvedAgentContext",
        response: AgentResponse,
        config: dict[str, Any],
    ) -> AgentResponse:
        """Record response in metadata.

        Config options:
        - observation_type: Type of observation to record
        - domain: Domain for the observation
        """
        observation_type = config.get("observation_type", "agent.response")
        domain = config.get("domain", "unknown")

        response.metadata["observation_type"] = observation_type
        response.metadata["domain"] = domain
        response.metadata["job_id"] = context.job_id
        response.metadata["template_id"] = context.template_id

        return response