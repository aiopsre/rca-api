from __future__ import annotations

from typing import Any, Dict, List, Optional

from pydantic import BaseModel, Field


class GraphState(BaseModel):
    job_id: str
    incident_id: Optional[str] = None
    datasource_id: Optional[str] = None

    evidence_ids: List[str] = Field(default_factory=list)
    evidence_meta: List[Dict[str, Any]] = Field(default_factory=list)
    missing_evidence: List[str] = Field(default_factory=list)
    tool_calls_written: int = 0
    quality_gate_decision: Optional[str] = None
    quality_gate_reasons: List[str] = Field(default_factory=list)
    quality_gate_evidence_summary: Dict[str, Any] = Field(default_factory=dict)

    diagnosis_json: Optional[Dict[str, Any]] = None
    last_error: Optional[str] = None

    force_no_evidence: bool = False
    force_conflict: bool = False
    started: bool = False
    finalized: bool = False
