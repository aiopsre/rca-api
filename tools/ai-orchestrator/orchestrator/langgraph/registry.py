from __future__ import annotations

from typing import Any, Callable

from ..runtime.runtime import OrchestratorRuntime
from .config import OrchestratorConfig
from .templates.basic_rca import build_basic_rca_graph

TemplateBuilder = Callable[[OrchestratorRuntime, OrchestratorConfig], Any]


class UnknownPipelineError(RuntimeError):
    def __init__(self, pipeline: str) -> None:
        normalized = str(pipeline or "").strip()
        super().__init__(f"unknown pipeline template: {normalized or '<empty>'}")
        self.pipeline = normalized


def normalize_pipeline(pipeline: str | None) -> str:
    normalized = str(pipeline or "").strip().lower()
    if not normalized:
        return "basic_rca"
    return normalized


def get_template_builder(pipeline: str | None) -> TemplateBuilder:
    normalized = normalize_pipeline(pipeline)
    if normalized == "basic_rca":
        return build_basic_rca_graph
    raise UnknownPipelineError(normalized)


def list_template_ids() -> list[str]:
    return ["basic_rca"]
