from __future__ import annotations

from dataclasses import dataclass
import hashlib
import json
from typing import Any, Callable

from ..tools_rca_api import RCAApiClient


@dataclass(frozen=True)
class EvidencePublishResult:
    evidence_id: str
    idempotency_key: str
    created_by: str


class EvidencePublisher:
    def __init__(
        self,
        *,
        client: RCAApiClient,
        job_id: str,
        execute_with_retry: Callable[[str, Callable[[], str]], str],
    ) -> None:
        self._client = client
        self._job_id = str(job_id).strip()
        if not self._job_id:
            raise RuntimeError("job_id is required")
        self._created_by = f"ai:{self._job_id}"
        self._execute_with_retry = execute_with_retry

    @staticmethod
    def _json_digest(payload: Any) -> str:
        try:
            canonical = json.dumps(payload, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
        except Exception:  # noqa: BLE001
            canonical = str(payload)
        return hashlib.sha256(canonical.encode("utf-8")).hexdigest()

    def build_idempotency_key(
        self,
        *,
        node_name: str,
        kind: str,
        query_hash_source: Any,
    ) -> str:
        node = str(node_name).strip() or "unknown_node"
        normalized_kind = str(kind).strip() or "unknown_kind"
        query_hash = self._json_digest(query_hash_source)
        base = f"{self._job_id}|{node}|{normalized_kind}|{query_hash}"
        digest = hashlib.sha256(base.encode("utf-8")).hexdigest()
        return f"orchestrator-evidence-{digest}"

    def save_mock_evidence(
        self,
        *,
        incident_id: str,
        node_name: str,
        kind: str,
        summary: str,
        raw: dict[str, Any],
        query_hash_source: Any = None,
    ) -> EvidencePublishResult:
        hash_source = query_hash_source if query_hash_source is not None else raw
        idempotency_key = self.build_idempotency_key(
            node_name=node_name,
            kind=kind,
            query_hash_source=hash_source,
        )

        def _save() -> str:
            return self._client.save_mock_evidence(
                incident_id=incident_id,
                summary=summary,
                raw=raw,
                job_id=self._job_id,
                idempotency_key=idempotency_key,
                created_by=self._created_by,
            )

        evidence_id = self._execute_with_retry("evidence.save_mock", _save)
        return EvidencePublishResult(
            evidence_id=evidence_id,
            idempotency_key=idempotency_key,
            created_by=self._created_by,
        )

    def save_evidence_from_query(
        self,
        *,
        incident_id: str,
        node_name: str,
        kind: str,
        query: dict[str, Any],
        result: dict[str, Any],
        query_hash_source: Any = None,
    ) -> EvidencePublishResult:
        hash_source = query_hash_source if query_hash_source is not None else query
        idempotency_key = self.build_idempotency_key(
            node_name=node_name,
            kind=kind,
            query_hash_source=hash_source,
        )

        def _save() -> str:
            return self._client.save_evidence_from_query(
                incident_id=incident_id,
                kind=kind,
                query=query,
                result=result,
                job_id=self._job_id,
                idempotency_key=idempotency_key,
                created_by=self._created_by,
            )

        evidence_id = self._execute_with_retry("evidence.save_query", _save)
        return EvidencePublishResult(
            evidence_id=evidence_id,
            idempotency_key=idempotency_key,
            created_by=self._created_by,
        )

