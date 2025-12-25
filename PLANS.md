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

---

## P0-6 AIJob 队列拉取优化（A1 Long Polling）

### Spec
- docs/devel/zh-CN/附录M2_LongPolling_AIJob队列拉取规范.md

### Done Definition（验收口径）
1) rca-api：在现有队列接口基础上支持 Long Polling
   - GET /v1/ai/jobs?status=queued&limit&offset&wait_seconds=20
   - 行为：
     - 若队列非空：立即返回
     - 若队列为空且 wait_seconds>0：最多阻塞 wait_seconds 秒；期间有新 queued job 则提前返回
     - 超时无 job：返回空列表（HTTP 200）
   - guardrails：
     - wait_seconds 合法范围（例如 0~30，具体见附录 M2）
2) rca-api：in-process notify（P0 单实例）
   - 在创建 queued job 成功后（ai:run 落库成功）触发 notify 唤醒等待中的 long poll 请求
   - handler 采用“两阶段 list + wait + relist”模式（细节见附录 M2 的 Go 伪代码）
3) 可观测性（最小）
   - 保留现有 ai_job_queue_pull 指标
   - 可选：新增 longpoll wait/wakeup/timeout 指标（若实现则写入 metrics 清单）
4) orchestrator：改用自适应 long polling
   - 当上一次拉取为空：下一次请求带 wait_seconds=20
   - 当拉取非空：处理完后立即继续拉取（wait_seconds=0）
5) 回归验证
   - make test / make lint-new 通过
   - 在队列为空时观测到 QPS 显著降低（约每 20 秒一次请求）
   - 在 wait 期间创建新 job：orchestrator 能在超时前拿到 job 并执行

### Notes（简短）
- P0 仅保证单实例 rca-api 下 long poll 唤醒有效；多实例跨节点通知属于后续版本（不在 P0-6）。
- 并发/重复执行问题由 start_job 的状态迁移（CAS）保证，不由 long polling 解决（见附录 M2）。

---

## 验证步骤（P0-6 Long Polling）

### 0) 前置：启动 rca-api

```bash
cd /opt/workspace/study/rca-api
go run ./cmd/rca-apiserver -c configs/rca-apiserver.yaml
```

### 1) 队列为空时：验证请求会阻塞到超时并返回空列表

开一个新终端（T1）：

```bash
BASE_URL="http://127.0.0.1:5555"
SCOPES="*"

time curl -sS "${BASE_URL}/v1/ai/jobs?status=queued&limit=10&offset=0&wait_seconds=5" \
  -H "X-Scopes: ${SCOPES}"
```

期望：

* `time` 显示大约 **5 秒**（接近 wait_seconds）
* 输出是一个**空列表/空 jobs**（HTTP 200）

> 如果你们响应 envelope 是 `{code,data}`，只要 data.jobs 为空即可。

### 2) wait 期间创建 job：验证 long poll 会被提前唤醒

准备三个终端：

#### 终端 T1：先发起 long poll（wait_seconds=20）

```bash
BASE_URL="http://127.0.0.1:5555"
SCOPES="*"

time curl -sS "${BASE_URL}/v1/ai/jobs?status=queued&limit=10&offset=0&wait_seconds=20" \
  -H "X-Scopes: ${SCOPES}"
```

#### 终端 T2：在 T1 阻塞期间触发创建 job（ai:run）

你需要一个 incident_id。最简单是用你现有脚本创建（如果脚本里会 ingest 并创建 incident）：

* 如果你已有 `scripts/test_p0_L1.sh` 会先 ingest 并创建 incident，可以直接运行到 ai:run 前半段（你也可以手工调用你们的 incident 创建/ingest）。

这里给一个“推荐做法”：直接跑 L1（它会触发 ai:run），并让 orchestrator 先不启动：

```bash
cd /opt/workspace/study/rca-api
BASE_URL="http://127.0.0.1:5555" SCOPES="*" RUN_QUERY=0 ./scripts/test_p0_L1.sh
```

如果你希望只触发 ai:run 而不跑全流程，就用你们已有 API 方式对某个 incident 执行：

```bash
# 伪示例：用你们现有 endpoint 触发 ai:run（incident_id 替换成实际值）
curl -sS -X POST "${BASE_URL}/v1/incidents/${INCIDENT_ID}/ai:run" \
  -H "X-Scopes: ${SCOPES}" \
  -H "Content-Type: application/json" \
  -d '{"idempotencyKey":"lp-wakeup-test-1"}'
```

期望：

* T1 的 curl **在 20 秒内提前返回**（time 明显小于 20s）
* 返回体里包含至少 1 条 queued job（能看到 job_id）

### 3) QPS 降低验证（手工观测版）

队列为空时，用循环对比“无 long poll”与“long poll”的请求次数（粗略即可）。

#### 3.1 无 long poll（对照）

```bash
BASE_URL="http://127.0.0.1:5555"
SCOPES="*"
for i in {1..10}; do
  curl -sS "${BASE_URL}/v1/ai/jobs?status=queued&limit=10&offset=0" -H "X-Scopes: ${SCOPES}" >/dev/null
  sleep 1
done
echo "done"
```

期望：10 秒约 10 次请求。

#### 3.2 long poll（同样 10 秒窗口）

```bash
BASE_URL="http://127.0.0.1:5555"
SCOPES="*"
# 这里 wait_seconds=10，循环 2 次即可覆盖 ~20s；你也可以 wait_seconds=20 循环 1 次
for i in {1..2}; do
  curl -sS "${BASE_URL}/v1/ai/jobs?status=queued&limit=10&offset=0&wait_seconds=10" -H "X-Scopes: ${SCOPES}" >/dev/null
done
echo "done"
```

期望：同样时间范围内，请求数显著减少（接近 1~2 次/20 秒）。

### 4) orchestrator 自适应 long poll 验证（功能验证）

启动 orchestrator（新终端 T3）：

```bash
cd /opt/workspace/study/rca-api/tools/ai-orchestrator
source .venv/bin/activate 2>/dev/null || true

BASE_URL="http://127.0.0.1:5555" SCOPES="*" RUN_QUERY=0 CONCURRENCY=1 POLL_INTERVAL_MS=1000 \
python -m orchestrator.main
```

验证点：

* 当队列为空时，orchestrator 的拉取频率应明显下降（如果你在 orchestrator 日志里打了“pull start/end”，应看到间隔接近 wait_seconds）。
* 当你触发一次 `ai:run` 创建 queued job 后，orchestrator 应能在超时前拿到 job 并执行（随后 L1 能 PASS）。

---

## P0-7 L2 回归：证据不足（Missing Evidence）路径

### Spec
- docs/devel/zh-CN/附录Lx_L2_证据不足回归用例.md

### Done Definition（验收口径）
1) orchestrator 支持可控触发 L2（证据不足）路径（推荐 `FORCE_NO_EVIDENCE=1` 或等价开关），并仍完整执行：
   start -> toolcalls -> finalize（LangGraph 执行链路不变）
2) diagnosis_json 写回满足附录 J2/J3 + 附录 Lx 要求：
   - root_cause.type=missing_evidence（或等价）
   - confidence <= 0.3
   - missing_evidence 列表非空
   - evidence_ids 规则按 J2/J3（若要求非空则必须保存占位 evidence 并引用）
3) 新增回归脚本 `scripts/test_p0_L2.sh`（或 L1 脚本 MODE=L2）：
   - 触发 ai:run -> 等待 job 终态 -> GET incident 断言上述字段
   - 输出格式可沿用 L1 严格风格（PASS L2 + 关键 IDs），失败非 0 退出并打印诊断
4) 工程验证：make test / make lint-new 通过

---

## P0-8 L4-2 回归：幂等与风暴保护（N=200）

### Spec
- docs/devel/zh-CN/附录L4-2_回归_幂等与风暴保护.md

### Done Definition（验收口径）
1) 新增风暴回归脚本 `scripts/test_p0_L4_2_storm.sh`（或同等命名）：
   - 默认 N=200、CONCURRENCY=10、IDEM_REUSE_RATIO=0.5
   - 并发触发 ingest + ai:run（覆盖重复 fingerprint 与幂等键复用）
   - 可选等待 job 终态（默认 WAIT_JOB=1），含超时与失败诊断
2) 脚本必须输出统计摘要并做断言（按附录 L4-2 的不变量与阈值）：
   - unique_incidents / unique_current_events / unique_jobs / unique_toolcalls / unique_evidences 等
   - 验证幂等不变量、merge 策略 A、AIJob/ToolCall 去重不被打穿，不出现线性爆炸
3) 工程验证：make test / make lint-new 通过
4) 复现步骤：提供 rca-api + orchestrator 启动方式与一条命令跑风暴脚本的示例

---

## P1-1 Silence（抑制/维护窗口）最小闭环

### Spec
- docs/devel/zh-CN/附录P1-1_Silence_接口与行为规范.md

### Done Definition（验收口径）
1) 新增 Silence 资源（CRUD + 查询）：
   - POST /v1/silences
   - GET /v1/silences/{silenceID}
   - GET /v1/silences?namespace=&enabled=&active=
   - PATCH /v1/silences/{silenceID}（enabled / endsAt / reason）
   - DELETE /v1/silences/{silenceID}（软删/禁用）
   - 校验：matchers 非空、endsAt>startsAt、matcher key/op/value 合法（P1-1 仅支持 op='='）
2) RBAC scopes：
   - silence.read / silence.admin
3) 事件域集成（关键行为）：
   - 在 POST /v1/alert-events:ingest 入口增加 “active silence match”
   - 命中 silence 时：
     - 返回/记录 silenced=true（并携带 silence_id）
     - 默认不创建/不推进 incident（止血）
     - timeline best effort 记录 alert_silenced（含 event_id/fingerprint/silence_id）
   - 未命中则保持现有 ingest+merge 策略 A 行为不变
4) 回归脚本（L3）：
   - 新增 scripts/test_p1_L3_silence.sh：
     - 创建 silence -> ingest 命中断言不创建/不推进 incident
     - disable/过期 -> 再 ingest 断言恢复正常 merge
     - 输出 PASS L3 + IDs；失败输出诊断并非 0 退出
5) 工程验证：make protoc / make test / make lint-new 通过

---

## P1-2B Notice（Webhook 通知）最小闭环

### Spec
- docs/devel/zh-CN/附录P1-2B_Notice_接口与行为规范.md

### Done Definition（验收口径）
1) NoticeChannel + NoticeDelivery 落地（proto -> make protoc -> model/store/biz/handler -> validation -> tests）：
   - NoticeChannel CRUD：
     - POST /v1/notice-channels
     - GET /v1/notice-channels/{channelID}
     - GET /v1/notice-channels?enabled=
     - PATCH /v1/notice-channels/{channelID}
     - DELETE /v1/notice-channels/{channelID}（软删/禁用）
   - Delivery 只读查询：
     - GET /v1/notice-deliveries?incident_id=&channel_id=&event_type=&status=&limit=&offset=
     - GET /v1/notice-deliveries/{deliveryID}
2) RBAC scopes：notice.read / notice.admin，并在 handler 强制校验。
3) 触发点（best effort，不阻塞主流程）：
   - incident_created：merge 新建 incident 成功后触发通知
   - diagnosis_written：AIJob finalize 成功写回 incident.diagnosis_json 后触发通知
   - 每次投递必须落库 NoticeDelivery（含 request/response/latency/error，截断保护）。
4) Guardrails：
   - request/response body 截断（默认 8KB）、error 截断（默认 2KB）
   - endpointURL 仅允许 http/https；timeoutMs 钳制 500~10000
5) 回归脚本：新增 scripts/test_p1_L3_notice.sh
   - 启动本地 mock webhook -> 创建 NoticeChannel -> 触发 incident_created + diagnosis_written
   - 断言 mock 收到 2 次回调 + deliveries 可按 incident_id 查询到 >=2 且 succeeded
6) 工程验证：make protoc / make test / make lint-new 通过

---

## P1-3 Notice（Delivery Queue + Retry Worker）最小可靠性闭环

### Spec
- docs/devel/zh-CN/附录P1-3_Notice_投递队列与重试规范.md

### Done Definition（验收口径）
1) 采用 DB Outbox：业务路径不再直接网络投递，仅创建 `NoticeDelivery(status=pending)`（best effort，不阻塞主流程）：
   - `incident_created` / `diagnosis_written` 触发点改为：写入 pending delivery（含 request_body 截断）
2) `NoticeDelivery` 扩展调度字段（最小集）：
   - status: pending/succeeded/failed（canceled 可选）
   - attempts、max_attempts、next_retry_at
   - locked_by/locked_at（支持 claim；单实例也要保证并发不重复）
   - idempotency_key（HTTP Header: Idempotency-Key）
3) 新增 notice-worker（独立进程/子命令均可）：
   - 拉取并 claim `pending && next_retry_at<=now` 的 deliveries（按 created_at 或 id）
   - 发送 webhook（timeout=channel.timeoutMs 钳制 500~10000）
   - 成功：置 succeeded；失败（可重试）：attempts++，计算 backoff+ jitter，更新 next_retry_at；达到上限置 failed
   - 可重试：网络错误/timeout/429/5xx；不可重试：4xx（除 429）
4) 可观测性（最小指标 + 日志字段）：
   - notice_delivery_dispatch_total / notice_delivery_send_total{status} / notice_delivery_send_latency_ms
   - pending gauge（可选）与 failed total（建议）
   - 日志贯穿 delivery_id/channel_id/event_type/incident_id/job_id/attempts/http_code/error
5) 回归脚本（L4）：新增 `scripts/test_p1_L4_notice_retry.sh`
   - mock webhook 前 2 次返回 500，之后返回 200
   - 触发产生 delivery -> 启动 worker -> 断言最终 succeeded、attempts>=3、收到 Idempotency-Key
   - 成功输出 PASS L4-NOTICE-RETRY + IDs；失败输出 FAIL step=<STEP> + HTTP code/body<=2KB + IDs
6) 工程验证：make protoc / make test / make lint-new 通过

