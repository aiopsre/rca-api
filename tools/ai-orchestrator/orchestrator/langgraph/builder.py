from __future__ import annotations

from typing import Any

from .config import OrchestratorConfig
from .templates.basic_rca import build_basic_rca_graph


def build_graph(
    _client: Any,
    cfg: OrchestratorConfig,
    runtime: Any,
):
    # Keep the public build_graph entrypoint stable while template internals move to registry/templates.
    return build_basic_rca_graph(runtime, cfg)
