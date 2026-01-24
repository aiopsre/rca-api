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
    evidence_mode: Optional[str] = None
    evidence_candidates: List[Dict[str, Any]] = Field(default_factory=list)
    incident_context: Dict[str, Any] = Field(default_factory=dict)

    metrics_query_request: Dict[str, Any] = Field(default_factory=dict)
    metrics_query_output: Dict[str, Any] = Field(default_factory=dict)
    metrics_query_status: Optional[str] = None
    metrics_query_error: Optional[str] = None
    metrics_query_latency_ms: int = 0
    metrics_query_result_size_bytes: int = 0
    metrics_branch_meta: Dict[str, Any] = Field(default_factory=dict)

    logs_query_request: Dict[str, Any] = Field(default_factory=dict)
    logs_query_output: Dict[str, Any] = Field(default_factory=dict)
    logs_query_status: Optional[str] = None
    logs_query_error: Optional[str] = None
    logs_query_latency_ms: int = 0
    logs_query_result_size_bytes: int = 0
    logs_branch_meta: Dict[str, Any] = Field(default_factory=dict)

    post_finalize_snapshot: Dict[str, Any] = Field(default_factory=dict)
    post_finalize_verification_plan: Dict[str, Any] = Field(default_factory=dict)
    post_finalize_kb_refs: List[Dict[str, Any]] = Field(default_factory=list)
    post_finalize_target_seq: Optional[int] = None
    verification_results: List[Dict[str, Any]] = Field(default_factory=list)
    verification_done: bool = False

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
