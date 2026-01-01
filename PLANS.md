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

---

## P1-4 Notice（Replay / Cancel / DLQ）可运营控制面

### Spec
- docs/devel/zh-CN/附录P1-4_Notice_重放与取消规范.md

### Done Definition（验收口径）
1) API 扩展（RBAC：notice.admin）：
   - POST /v1/notice-deliveries/{deliveryID}:replay
     - 将 failed（或 pending 可选）重置为 pending：attempts=0、next_retry_at=now、清 locked_by/locked_at
   - POST /v1/notice-deliveries/{deliveryID}:cancel
     - 将 pending/failed 置为 canceled，并清 locked_by/locked_at；worker 必须跳过 canceled
2) 并发一致性：
   - replay/cancel 与 worker claim 并发不出错；更新需事务化；发送前可二次检查 status（建议）
   - replay/cancel API 幂等（重复调用仍 200，返回当前 status）
3) DLQ 语义明确：
   - failed 视为 DLQ，可通过现有 list/filter 查询；修复后 replay 即可重投
4) 可观测性（最小）：
   - notice_delivery_replay_total / notice_delivery_cancel_total（或等价指标）
   - 日志包含 delivery_id/status/op（replay/cancel）与 request_id
5) 回归脚本：
   - 新增 scripts/test_p1_L5_notice_replay_cancel.sh
   - 覆盖：500→failed(DLQ)→mock变200→replay→worker→succeeded；以及 cancel 后不再发送
   - 成功输出 PASS L5-NOTICE-OPS + IDs；失败输出 FAIL step=<STEP> + HTTP code/body<=2KB + IDs
6) 工程验证：make protoc / make test / make lint-new 通过

### P1-3/4 回归脚本兼容性补丁：L3 Notice 适配 Outbox
- 升级 scripts/test_p1_L3_notice.sh：
  - 新增 RUN_WORKER=1 时自动拉起短生命周期 notice-worker，确保能验证 webhook 回调
  - RUN_WORKER=0 时退化为仅断言 deliveries（避免因 outbox 语义导致 0 回调误报）
  - 保持 PASS/FAIL step 诊断风格与 body 截断规范一致

---

## P1-5 Notice Selectors（订阅/路由）

### Spec
- docs/devel/zh-CN/附录P1-5_Notice_Selectors_订阅与路由规范.md

### Done Definition（验收口径）
1) NoticeChannel 增加 selectors（最小 allow-list）：
   - event_types / namespaces / services / severities（root_cause_types 可选）
   - selectors 为空 = 全量订阅（兼容旧行为）
2) 触发点改造（incident_created / diagnosis_written）：
   - 写 pending delivery 前执行 match(event_ctx, selectors)
   - 仅对匹配的 enabled channels 生成 NoticeDelivery(status=pending)
3) Guardrails：
   - event_types/severities 枚举校验；每列表长度/字符串长度上限
4) 回归脚本：新增 scripts/test_p1_L6_notice_selectors.sh
   - 创建 channel_all（无 selectors）与 channel_diag_only（event_types=[diagnosis_written]）
   - 验证 incident_created 只命中 channel_all；diagnosis_written 两者均命中
   - 含 worker 消费（脚本内启动或复用现有方式），PASS/FAIL step 诊断风格一致
5) make protoc / make test / make lint-new 通过

---

## P1-6 Notice（Channel 变更与投递一致性：Delivery Snapshot）

### Spec
- docs/devel/zh-CN/附录P1-6_Notice_Channel变更与投递一致性.md

### Done Definition（验收口径）
1) NoticeDelivery 增加 snapshot（最小：endpoint_url/timeout_ms/headers/secret_fingerprint/channel_version 可选），并有大小/数量 guardrails。
2) delivery 创建时填充 snapshot；P1-5 selectors 匹配逻辑不变。
3) notice-worker 发送时必须优先使用 snapshot（endpoint/timeout/headers），确保 channel 后续变更不影响已入队 delivery。
4) replay 不修改 snapshot，保证可复现；cancel 不影响 snapshot。
5) 新增回归脚本 scripts/test_p1_L7_notice_snapshot.sh：
   - 生成 pending 后修改 channel endpoint，再启动 worker
   - 断言投递仍到旧 endpoint（snapshot），新 endpoint 0 次
6) make protoc / make test / make lint-new 通过

---

## P1-7 Notice（Replay 使用最新 Channel：刷新 Snapshot）

### Spec
- docs/devel/zh-CN/附录P1-7_Notice_Replay_使用最新Channel规范.md

### Done Definition（验收口径）
1) 扩展 replay API：
   - POST /v1/notice-deliveries/{deliveryID}:replay?use_latest_channel=0|1（默认 0）
2) 语义：
   - use_latest_channel=0：重置 pending（attempts=0、next_retry_at=now、清 lock），不修改 snapshot（P1-6 语义不变）
   - use_latest_channel=1：除重置 pending 外，刷新 snapshot 为当前 channel 最新配置（endpoint/timeout/headers/secret_fingerprint/channel_version）
   - replay 默认不修改 idempotency_key
3) 错误处理：
   - channel 不存在时：use_latest_channel=1 返回 409；use_latest_channel=0 仍可 replay
4) 指标/日志：
   - notice_delivery_replay_total 按 mode=snapshot/latest 计数（或等价）
   - 日志包含 replay_mode 与 snapshot endpoint before/after（可选）
5) 回归脚本：新增 scripts/test_p1_L8_notice_replay_latest.sh
   - A=500 让 delivery 快速 failed；改 channel endpoint 到 B=200；replay(use_latest_channel=1)；worker 后断言打到 B 且 snapshot 更新
   - PASS/FAIL step 诊断风格一致
6) make protoc / make test / make lint-new 通过

---

## P1-8 Notice（SecretFingerprint 失配策略：Fail-Fast + L9 回归）

### Spec
- docs/devel/zh-CN/附录P1-8_Notice_SecretFingerprint一致性与失配策略.md

### Done Definition（验收口径）
1) notice-worker 发送前做 secret fingerprint 一致性校验：
   - 若 delivery.snapshot.secretFingerprint 非空且与当前 channel.secretFingerprint 不一致：
     - delivery 直接置 failed（DLQ），error 必含 `secret_fingerprint_mismatch` 与 `replay?useLatestChannel=1`
     - 不再进入重试回退
2) 新增指标：notice_delivery_snapshot_mismatch_total（至少带 event_type 维度）
3) 结构化日志补齐：mismatch 标记、snap_fp/channel_fp 前缀、delivery_id/channel_id/event_type/incident_id
4) 新增回归脚本：scripts/test_p1_L9_notice_secret_mismatch.sh
   - S1 成功；S1->S2 后 fail-fast；replay(useLatestChannel=1) 后成功
   - PASS/FAIL 输出格式与 L 系列一致
5) make protoc / make test / make lint-new 通过

---

## P2-1 Notice（通知模板：COMPACT/FULL + 字段裁剪）

### Spec
- docs/devel/zh-CN/附录P2-1_Notice_通知模板与字段裁剪规范.md

### Done Definition（验收口径）
1) NoticeChannel 增加模板配置：
   - payload_mode=COMPACT|FULL（默认 COMPACT）
   - include_diagnosis/include_evidence_ids/include_root_cause/include_links（可选开关）
2) Outbox 生成 delivery.request 时按模板构造 payload：
   - COMPACT 仅允许摘要与 diagnosis_min（不得包含完整 diagnosis_json）
   - FULL 允许裁剪版 diagnosis + evidence_ids（均有上限与截断）
3) Guardrails：payload 总大小上限（建议 16KB）+ missing_evidence/evidence_ids/字符串长度上限；超限时 truncated=true
4) 回归脚本：新增 scripts/test_p2_L10_notice_template.sh
   - 两个 channel（compact/full）对同一事件产生不同 payload，并断言关键字段/不包含项
5) make protoc / make test / make lint-new 通过

---

## P2-2 Notice（模板变量与链接规范：Safe Summary + Versioned Links v1）

### Spec
- docs/devel/zh-CN/附录P2-2_Notice_模板变量与链接规范.md

### Done Definition（验收口径）
1) NoticeChannel 增加 summary_template（可选），支持 ${var} 安全替换（allow-list 变量，无表达式/正则）。
2) include_links=true 时输出 links.version="v1" 的稳定结构，并按 base_url 规则生成 incident_url/delivery_url 等。
3) Guardrails：
   - summary_template<=512；替换后 summary<=512；替换次数<=50；仅处理 ${[a-zA-Z0-9_]+}
   - links 不包含 token，不外发 secret/headers
4) 新增回归脚本 scripts/test_p2_L11_notice_links_and_summary_template.sh：
   - 验证 links.v1 结构与 URL
   - 验证 summary_template 变量替换生效
   - PASS/FAIL step 诊断风格一致
5) make protoc / make test / make lint-new 通过

---

## P2-3 Notice（Webhook 签名与重放防护：HMAC + Timestamp + Nonce）

### Spec
- docs/devel/zh-CN/附录P2-3_Notice_Webhook签名与重放防护规范.md

### Done Definition（验收口径）
1) notice-worker 发送 webhook 时增加头：
   - X-Rca-Signature / X-Rca-Timestamp / X-Rca-Nonce / X-Rca-Delivery-Id / X-Rca-Event-Type
2) 签名算法固定为 v1 signing_string（body_sha256 + ts + nonce + method + path），HMAC-SHA256(secret)
3) nonce 每次 HTTP attempt 必须变化；replay 后再次发送也必须是新 nonce/签名
4) Guardrails：nonce<=128；timestamp 为整数；secret 为空时行为需明确（兼容允许但在文档标注不安全）
5) 新增回归脚本 scripts/test_p2_L12_notice_webhook_signature.sh：
   - mock 收到签名头
   - 脚本本地计算 expected_sig 并断言一致
   - replay/重试后 nonce/签名变化
6) make protoc / make test / make lint-new 通过

---

## P0-9 回归：L3 证据冲突/不一致（conflict_evidence）

### Spec
- docs/devel/zh-CN/附录L3-1_回归_证据冲突与不一致.md
- docs/devel/zh-CN/附录M1_LangGraph_Orchestrator_接口规范.md（6.2.4：新增 conflict_evidence 最小模板；硬性校验要点补充）

### Done Definition
1) orchestrator 支持 FORCE_CONFLICT=1（不依赖真实 datasource），仍保存 evidence/toolcalls，并在 finalize 输出 conflict_evidence 诊断。
2) 写回 diagnosis_json 必须满足：
   - root_cause.type="conflict_evidence"
   - root_cause.confidence<=0.30
   - missing_evidence 非空（>=1，<=20）
   - evidence_ids（若非空）必须引用已存在 evidence_id
3) 后端增加最小校验/归一化：conflict_evidence 强制低置信度 + 缺失项非空（与文档硬性校验要点一致）。
4) 新增回归脚本 scripts/test_p0_L3_conflict_evidence.sh（PASS/FAIL step 诊断风格一致）。
5) make test / make lint-new 通过。

---

## P0-10 证据质量门控（A2 Evidence Quality Gate）

### Spec
- docs/devel/zh-CN/附录A2_证据质量门控_EvidenceQualityGate.md
- docs/devel/zh-CN/附录M1_LangGraph_Orchestrator_接口规范.md（6.2.4：missing/conflict 模板与硬性校验要点）

### Done Definition
1) orchestrator 在 finalize_job 前实现 Evidence Quality Gate：
   - FORCE_NO_EVIDENCE=1 -> missing_evidence
   - FORCE_CONFLICT=1 -> conflict_evidence（优先级固定：conflict 优先）
   - 仅在证据充分且一致时允许 confidence>0.60
2) toolcall 审计必须包含 gate_decision（写入 toolcall.output JSON）：
   - quality_gate.decision=pass|missing|conflict
   - reasons 非空，含 evidence_summary（数量/来源/no_data 统计）
3) 后端 finalize 强制约束：
   - missing/conflict -> confidence<=0.30 且 missing_evidence 非空且<=20
   - 禁止 missing/conflict 与 confidence>0.60 同时出现
4) 新增回归脚本 scripts/test_p0_L5_quality_gate.sh 覆盖 missing/conflict（必选）与 pass（可选），并断言 toolcalls 中 gate_decision 存在
5) make test / make lint-new 通过


---

## C1 MCP Readonly Tools（Done Definition）

* 提供 `/v1/mcp/tools` 与 `/v1/mcp/tools/call` 两个接口；
* 支持 6 个 tool：get_incident、list_alert_events_current、get_evidence、list_incident_evidence、query_metrics、query_logs；
* tool 权限：基于 `X-Scopes` 映射，默认拒绝，403 返回固定错误结构；
* query_metrics/query_logs 复用 evidence guardrails（时间窗/limit/timeout/max_result_bytes/限流）；
* 每次调用写入 ToolCall 审计（tool_name=mcp.*，input/output/error/latency，截断一致，严禁泄露 secret/headers/token）；
* 新增回归脚本 `scripts/test_c1_L1_mcp_tools.sh` 通过；并保证 `make test`、`make lint-new` 通过。

---

## C2 Orchestrator MCP Alignment（Done Definition）

* `tools/ai-orchestrator` 内的只读/低风险工具调用统一通过 rca-api MCP shim：

  * `POST /v1/mcp/tools/call` 调用 `get_incident/list_alert_events_current/get_evidence/list_incident_evidence/query_metrics/query_logs`
  * 不再直接调用分散 REST；
* orchestrator 内新增 `MCPClient` 与 `ToolRegistry`（含 tool metadata 与 required_scopes），并支持错误映射；
* 重试策略：仅对网络错误/超时/5xx/RATE_LIMITED 重试（指数退避+jitter，最大 3 次）；对 SCOPE_DENIED/INVALID_ARGUMENT/NOT_FOUND 不重试；
* ToolCall 审计贯穿一致：平台侧落库 `tool_name=mcp.*`，回归可查询验证；
* 新增回归脚本 `scripts/test_c2_L1_orchestrator_mcp.sh` 并通过；
* `make test` 与 `make lint-new` 均通过。

---

## C3 MCP Readonly Tools Expansion（Done Definition）

* 在 rca-api MCP shim（`/v1/mcp/tools`、`/v1/mcp/tools/call`）上新增只读/低风险工具：

  * `list_incidents`、`list_alert_events_history`
  * `list_datasources`、`get_datasource`（仅元信息，严格脱敏）
  * `get_ai_job`、`list_ai_jobs`、`list_tool_calls`（ToolCall 查询仍遵循截断/脱敏）
  * `list_silences`、`list_notice_deliveries`
* 所有新增工具：

  * 复用 X-Scopes 默认拒绝与 required_scopes 映射
  * 输出遵循字段白名单 + 脱敏 + 截断（审计 8KB/8KB/2KB；响应整体 16KB；超限 TRUNCATED_OUTPUT）
  * `query_metrics/query_logs` 仍严格复用 evidence guardrails（不可绕过）
* 新增回归脚本 `scripts/test_c3_L1_mcp_more_tools.sh` 覆盖 allow/deny/截断/脱敏与审计查询，并通过；
* `make test` 与 `make lint-new` 均通过。
