from __future__ import annotations

from typing import Any, Dict, List, Optional

from pydantic import BaseModel, Field


class GraphState(BaseModel):
    job_id: str
    instance_id: Optional[str] = None
    incident_id: Optional[str] = None
    datasource_id: Optional[str] = None

    evidence_ids: List[str] = Field(default_factory=list)
    evidence_meta: List[Dict[str, Any]] = Field(default_factory=list)
    missing_evidence: List[str] = Field(default_factory=list)
    tool_calls_written: int = 0
    quality_gate_decision: Optional[str] = None
    quality_gate_reasons: List[str] = Field(default_factory=list)
    quality_gate_evidence_summary: Dict[str, Any] = Field(default_factory=dict)
    evidence_plan: Dict[str, Any] = Field(default_factory=dict)

    diagnosis_json: Optional[Dict[str, Any]] = None
    last_error: Optional[str] = None

    input_hints: Dict[str, Any] = Field(default_factory=dict)
    a3_max_calls: int = 6
    a3_max_total_bytes: int = 2 * 1024 * 1024
    a3_max_total_latency_ms: int = 8000

    force_no_evidence: bool = False
    force_conflict: bool = False
    started: bool = False
    finalized: bool = False
