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
  - [x] Add `ai_job.proto` (includes ToolCall message/schema)（已完成）
  - [x] Run `make protoc`（已完成）
- Storage
  - [x] Models: `AIJob`, `AIToolCall`（已完成）
  - [x] Indexes: job status + created_at; tool_call by job_id+seq (Appendix H2)（已完成）
- API endpoints
  - [x] `POST /v1/incidents/{id}/ai:run`（已完成）
  - [x] `GET /v1/ai/jobs/{job_id}`（已完成）
  - [x] `GET /v1/incidents/{id}/ai`（已完成）
  - [x] `POST /v1/ai/jobs/{job_id}/start`（已完成）
  - [x] `POST /v1/ai/jobs/{job_id}/cancel`（已完成）
  - [x] `POST /v1/ai/jobs/{job_id}/finalize`（已完成）
  - [x] `POST /v1/ai/jobs/{job_id}/tool-calls`（已完成）
  - [x] `GET /v1/ai/jobs/{job_id}/tool-calls`（已完成）
- Writeback
  - [x] finalize 成功写回 incident 的 `diagnosis_json` / `root_cause_summary` / `evidence_refs_json`（已完成）
  - [x] incident `rca_status` 按 job 状态联动（running/done/failed）（已完成）
- Tests
  - [x] AIJob/ToolCall 关键状态流转与幂等测试（已完成）
  - [x] diagnosis_json（附录 J2/J3）校验测试（已完成）

Deliverable:
- AIJob + ToolCall 可审计、可回放、可写回 incident 的最小闭环能力（已完成）。

---

#### P0-3 实施细则（必须遵守）

##### 1) Current/History 存储策略（P0 简化版）
- history 必须落库（append-only，用于审计与回放）
- current 可采用 DB 同表方案：
  - 通过 `is_current` + `last_seen` 维护 current 视图
  - current 查询必须支持过滤（severity/service/cluster/namespace/fingerprint 等）与分页
- P1/P2 才考虑 Redis current cache（P0 不强制）

##### 2) Merge Policy（必须确定性）
- 以 `fingerprint` 为主键做去重/合并：
  - 同 fingerprint 新事件到来：更新 current 的 `last_seen`，并写入一条 history 记录
- incident 关联策略（必须选定一种并写入代码注释/文档）：
  - A（推荐）：fingerprint 绑定“一个未关闭 incident”，若已 closed 则新建 incident
  - 或 B：fingerprint + time_bucket（例如 24h）生成新的 incident
- 并发/重试下必须保证确定性：使用唯一索引/事务避免重复创建

##### 3) 幂等（附录G3）
- `POST /v1/alert-events:ingest` 必须支持 `Idempotency-Key`（header/body）
- 幂等命中时返回同一 event_id，并通过 `reused=true` 或 `details.existing_id` 明确标识

##### 4) Timeline（best effort）
- 如果存在 `incident_timeline` 表：
  - 在 ingest、incident 创建/关联、ack、状态变化时写入 timeline 事件
- 若表不存在，不阻塞主流程（仅日志提示）

##### 5) RBAC 与 P0 mock auth
- P0 允许使用 `X-Scopes` 注入 scopes（便于联调）
- P1 计划切换为 JWT/网关注入 scopes（文档需保留说明）

---

#### P0-4 E2E 增补（优先级：高）

E2E 最小目标：锁住“事件→证据→AI→回填”闭环，避免后续迭代引入回归。

##### 必做用例（对应附录 L）
- L1：K8s 5xx
  - ingest -> incident -> evidence（metrics/logs）-> ai_job -> finalize/writeback -> GET incident 校验
- L2：延迟无日志
  - 触发 missing_evidence/低置信度输出，不允许无证据高置信度结论（附录 J2/J3）
- L3：维护窗口/静默（P0 可 stub suppression 行为）
- L4：回归
  - ingest 幂等与风暴保护（高 QPS 下 current 不爆、history 不丢、外部依赖查询受限流保护）

##### 贯穿字段（日志/审计）
- request_id / incident_id / event_id / job_id / tool_call_id / datasource_id

---

## P0-5 LangGraph ai-orchestrator（必须：LangGraph）

### Spec
- docs/devel/zh-CN/附录M1_LangGraph_Orchestrator_接口规范.md

### Done Definition（验收口径）
1) rca-api 增加队列拉取接口：
   - GET /v1/ai/jobs?status=queued&limit&offset（created_at ASC，limit guardrails，RBAC ai.read）
2) 新增 tools/ai-orchestrator（必须基于 LangGraph）：
   - poll queued jobs -> graph.invoke 执行 job（start/toolcalls/evidence/finalize）
   - RUN_QUERY=0 必须可跑（无外部 datasource 依赖也能 evidence 落库 + finalize）
3) 升级 L1 E2E：
   - scripts/test_p0_L1.sh 不再直接调用 start/tool-calls/finalize
   - 改为轮询 GET /v1/ai/jobs/{job_id} 等待 succeeded/failed（含超时）
   - 成功后 GET incident 校验 diagnosis_json + evidence_refs_json 等
   - 严格 6 行 PASS 输出格式保持不变
4) 工程验证：
   - make protoc / make test / make lint-new 均通过
   - 给出一套可复现运行步骤（启动 rca-api、启动 orchestrator、跑 L1）

### Implementation Notes（简短）
- LangGraph 的节点签名、GraphState、RCAApiClient 接口、diagnosis_json 最小模板、异常路径与验收要求，全部以附录 M1 为准。
- P0 默认 CONCURRENCY=1（串行）；lease/heartbeat 延后到后续附录（不在 P0-5 做）。

