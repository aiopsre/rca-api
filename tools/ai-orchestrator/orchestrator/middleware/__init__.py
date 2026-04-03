"""Middleware package for hybrid multi-agent system.

Phase HM2: Surface and middleware infrastructure.

This package provides the middleware chain pattern for controlling
tool visibility, skill injection, and observation handling in the
hybrid multi-agent architecture.
"""

from .base import AgentMiddleware, AgentRequest, AgentResponse
from .chain import MiddlewareChain

__all__ = [
    "AgentMiddleware",
    "AgentRequest",
    "AgentResponse",
    "MiddlewareChain",
]