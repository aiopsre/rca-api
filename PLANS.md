# PLANS.md — Execution plan (P0-first), aligned with docs/devel/zh-CN

> This file translates the guide into an executable plan.
> Always keep this in sync with `docs/devel/zh-CN/00_总体说明与里程碑.md`.

---

## P0 Plan: Minimal closed-loop (MVP)

### P0-0 Repo baseline (one-time)
- [ ] Ensure docs live under `docs/devel/zh-CN` and are referenced in README (optional).
- [ ] Ensure `.gitignore` includes `.DS_Store` and `**/._*` (macOS artifacts).
- [ ] Confirm Makefile targets work locally:
  - [ ] `make deps`
  - [ ] `make protoc`
  - [ ] `make test`
  - [ ] `make lint`

Deliverable:
- Clean repo baseline + toolchain working.

---

### P0-1 Datasource + Evidence (MUST DO FIRST)
Docs:
- `02_P0_证据域_Datasource&Evidence.md`
- Appendix G (API envelope/分页/幂等/状态机), H (storage), I (RBAC), K (observability)

Scope:
1) Datasource CRUD
2) Evidence query + save + list
3) Guardrails (time window / size / timeout / limit / allowlist where needed)

Checklist:
- Contract (proto)
  - [ ] Add/extend `pkg/api/.../datasource.proto`
  - [ ] Add/extend `pkg/api/.../evidence.proto`
  - [ ] Run `make protoc`
- Storage
  - [ ] Add models: `Datasource`, `Evidence`
  - [ ] Add store repos with required indexes (Appendix H2)
- Biz logic
  - [ ] `queryMetrics` / `queryLogs`
  - [ ] `saveEvidence` (bind to incident_id, persist query + window + result metadata)
  - [ ] `listEvidenceByIncident`
- Guardrails (hard requirements)
  - [ ] max time range (configurable)
  - [ ] max rows/bytes (store size + overflow handling plan; Appendix H3)
  - [ ] per-datasource timeout & error normalization
  - [ ] rate limit protection for evidence queries (Appendix K2)
- API endpoints (examples; match docs)
  - [ ] `POST /v1/datasources` + GET/LIST/PATCH/DELETE
  - [ ] `POST /v1/evidence:queryMetrics`
  - [ ] `POST /v1/evidence:queryLogs`
  - [ ] `POST /v1/incidents/{id}/evidence` (save)
  - [ ] `GET /v1/incidents/{id}/evidence` (list)
- Tests
  - [ ] Unit tests for validation/guardrails
  - [ ] Minimal integration test for datasource->query->save->list (mock datasource client)

Deliverable:
- Evidence pipeline is usable by LangGraph tools (even before AIJob exists).

---

### P0-2 AIJob + ToolCall (auditable + replayable)
Docs:
- `03_P0_AI域_AIJob&ToolCall&LangGraph.md`
- Appendix J (diagnosis_json), G3 (idempotency), H (indexes/retention)

Scope:
- A durable job record that an orchestrator can execute (poll/push).
- Full ToolCall audit trail.
- Writeback to incident: diagnosis_json per Appendix J2.

Checklist:
- Contract (proto)
  - [ ] Add `ai_job.proto` (includes ToolCall message/schema)
  - [ ] Run `make protoc`
- Storage
  - [ ] Models: `AIJob`, `AIToolCall`
  - [ ] Indexes: job status + created_at; tool_call by job_id+seq (Appendix H2)
- API endpoints
  - [ ] `POST /v1/incidents/{id}/ai:r

