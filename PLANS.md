# Orchestrator 规范化底座（SDK + Runtime）实施计划（给 Codex）

唯一真实来源：本仓库 `tools/ai-orchestrator/` 与服务端 `internal/apiserver/`。

约束：
- 不修改 rca-api（Go 侧）。
- 尽量把新增/改动限制在 `tools/ai-orchestrator/` 内。
- LangGraph 节点不直接调用 HTTP（不直接触碰 `requests` / `RCAApiClient._request`）。
- 遵循写操作 header/lease/seq/幂等规则：
  - header：`X-Orchestrator-Instance-ID`（见 `internal/apiserver/handler/ai_job.go`）
  - lease：`/start` + `/heartbeat`（同上）
  - toolcall 幂等键：`(job_id, seq)`（见 `internal/apiserver/biz/v1/ai_job/ai_job.go:CreateToolCall`）

---

## Phase A（P0 最小闭环）：Start + Heartbeat + ToolCall 上报 + Finalize

### A1. 新增错误分类与 API envelope 解析
**新增文件**
- `tools/ai-orchestrator/orchestrator/sdk/errors.py`

**新增类型**
- `class RCAApiError(Exception)`：字段包含 `http_status`, `code`, `message`, `details`, `request_id`, `raw_body`
- `enum OrchestratorErrorCategory`（或字符串常量）：`missing_owner`, `owner_lost`, `retryable_transport`, `retryable_5xx`, `bad_request`, `permission_denied`, `unauthenticated`, `unknown`

**改动文件**
- `tools/ai-orchestrator/orchestrator/tools_rca_api.py`
  - 在 `RCAApiClient._request(...)` 内：
    1) 捕获 `requests.RequestException` -> raise `RCAApiError` 并标记 `retryable_transport`
    2) 对 HTTP 2xx：
       - 尝试解析 JSON envelope：若存在 `code` 且 `code != 0`，raise `RCAApiError(code=..., message=..., details=...)`
    3) 对 HTTP 非 2xx：
       - 尝试解析 JSON envelope（如有），否则保留 raw body
       - raise `RCAApiError(http_status=..., ...)`

**验收标准**
- 任意 API 调用失败时，不再只抛 `RuntimeError("... http=...")`，而是抛 `RCAApiError`，且可从异常对象读到：
  - transport vs api(code!=0) vs http 非 2xx 的区分信息

---

### A2. 新增 LeaseManager（统一 start/heartbeat/lease_lost 信号）
**新增文件**
- `tools/ai-orchestrator/orchestrator/runtime/lease_manager.py`

**新增类型/函数**
- `class LeaseManager`：
  - `start(job_id) -> bool`：内部调用 `RCAApiClient.start_job(job_id)`
  - `start_heartbeat(job_id, interval_s, on_lease_lost)`：后台线程循环 `RCAApiClient.renew_job_lease(job_id)`
    - 注意：现有 `RCAApiClient.renew_job_lease` 返回 `(ok, reason)`（见 `tools_rca_api.py:renew_job_lease`）
  - `stop()`：停止线程
  - `is_lost()` / `reason()`

**改动文件**
- `tools/ai-orchestrator/orchestrator/main.py`
  - 在 `_invoke_graph(...)` 内移除手写 heartbeat 线程与 `LeaseGuard` 的直接管理逻辑（当前在 `_invoke_graph` 内定义 `heartbeat_loop` 线程）。
  - 改为使用 `LeaseManager` 来管理 lease 生命周期。

**验收标准**
- 功能等价：依旧会在 heartbeat 失败时打印类似：
  - `lease heartbeat failed job=... instance_id=... reason=...`（当前 `main.py:_invoke_graph`）
- `LeaseManager.is_lost()` 可被 Runtime/graph 感知。

---

### A3. 新增 ToolCallReporter（统一 seq 分配 + 上报）
**新增文件**
- `tools/ai-orchestrator/orchestrator/runtime/toolcall_reporter.py`

**新增类型**
- `class ToolCallReporter`：
  - 构造参数：`client: RCAApiClient`, `job_id: str`, `lease_manager: LeaseManager`
  - 内部维护：`_seq: int`（Phase A 单线程递增即可）
  - `next_seq() -> int`
  - `report(node_name, tool_name, request_json, response_json, status, latency_ms, error_message=None)`

**关键逻辑（从服务端代码对齐）**
- ToolCall 写入 endpoint：`POST /v1/ai/jobs/{jobID}/tool-calls`
  - 结构来自 `pkg/api/apiserver/v1/ai_job.proto:CreateAIToolCallRequest`
- 幂等：服务端 `internal/apiserver/biz/v1/ai_job/ai_job.go:CreateToolCall` 会对 `(job_id, seq)` 幂等返回
  - 因此 reporter 不需要“先查再写”，只要保证 seq 稳定即可。

**验收标准**
- 替换 `graph.py:write_tool_calls` 内两次 `client.add_tool_call(seq=1/2, ...)`：
  - 改为 `reporter.report(...)`（seq 由 reporter 分配或显式传入）
- 行为不变：仍能成功写入两条 toolcall（collect_evidence + synthesize）

---

### A4. 新增 Runtime（对 LangGraph 层暴露稳定接口）
**新增文件**
- `tools/ai-orchestrator/orchestrator/runtime/runtime.py`

**新增类型**
- `class OrchestratorRuntime`：
  - 字段：`client`, `job_id`, `lease_manager`, `toolcall_reporter`
  - `start()`：执行 `lease_manager.start(job_id)` 并启动 heartbeat
  - `report_tool_call(...)`：委托给 reporter
  - `finalize(status, diagnosis_json=None, error_message=None)`：调用 `RCAApiClient.finalize_job(...)`
  - `is_lease_lost()/lease_reason()`：透出 lease 状态
  - `shutdown()`：停止 heartbeat

**改动文件**
- `tools/ai-orchestrator/orchestrator/graph.py`
  - `write_tool_calls(state, client)` -> `write_tool_calls(state, runtime)`
    - 禁止节点直接调用 `client.add_tool_call`
  - `finalize_job(state, client, lease_guard)` -> `finalize_job(state, runtime)`
    - 禁止节点直接调用 `client.finalize_job`
- `tools/ai-orchestrator/orchestrator/main.py`
  - `_invoke_graph`：在开始 graph 前创建并 `runtime.start()`；结束后 `runtime.shutdown()`

**验收标准**
- LangGraph 节点文件 `graph.py` 中不再出现对 `RCAApiClient.add_tool_call` / `RCAApiClient.finalize_job` 的直接调用（仅 Runtime 内允许 HTTP）。
- 运行 orchestrator（本地）仍能：
  - claim job（start）
  - 心跳续租（heartbeat）
  - 上报 toolcalls（2 条）
  - finalize 成功/失败（见 `graph.py:finalize_job` 的现有逻辑路径）

---

### A5. 集成测试 / 回归脚本（Orchestrator 侧）
**改动/新增文件**
- `tools/ai-orchestrator/tests/` 下新增或扩展测试：
  - 可新增 `tools/ai-orchestrator/tests/test_runtime_minimal.py`（若现有测试框架允许）
- 若无可用 mock infra：至少提供一个可运行脚本：
  - `tools/ai-orchestrator/scripts/run_one_job.sh`（或 python 脚本），说明如何设置环境变量与运行

**验收标准**
- 文档化“如何运行”（至少包括）：
  - `BASE_URL`, `SCOPES`, `INSTANCE_ID`, `LONG_POLL_WAIT_SECONDS` 等来自 `tools/ai-orchestrator/orchestrator/main.py:load_settings`
- 能在本地指向 rca-api 启动后的地址执行一次最小闭环。

---

# Phase B：EvidencePublisher + 并发 seq + 重试矩阵 + LangGraph 细粒度 ToolCall 上报

唯一真实来源：本仓库代码。

前置（从代码上看必须满足）：
- Orchestrator 必须能调用 rca-api：
  - Job：`POST /v1/ai/jobs/:jobID/start|heartbeat|tool-calls|finalize`（`internal/apiserver/handler/ai_job.go:init()`）
  - Evidence：`POST /v1/incidents/:incidentID/evidence`（`internal/apiserver/handler/evidence.go:init()`）
- ToolCall 结构与 finalize/evidence 字段以 proto 为准：
  - `pkg/api/apiserver/v1/ai_job.proto:CreateAIToolCallRequest`（含 `repeated evidenceIDs`）
  - `pkg/api/apiserver/v1/ai_job.proto:FinalizeAIJobRequest`（含 `repeated evidenceIDs`）
  - `pkg/api/apiserver/v1/evidence.proto:SaveEvidenceRequest`（含 `idempotencyKey/jobID/createdBy`）

---

## B0（若尚未有底座）：引入 Runtime/Reporter 抽象（一次性）
> 如果你本地已经完成 Phase A（SDK+Runtime），则跳过 B0。

新增目录（建议）：
- `tools/ai-orchestrator/orchestrator/runtime/`
- `tools/ai-orchestrator/orchestrator/sdk/`

目标：
- LangGraph 节点不直接调用 HTTP client（当前 `graph.py:write_tool_calls` 直接 `client.add_tool_call`）
- lease/start/toolcall/finalize 收敛到 runtime 层

验收：
- `tools/ai-orchestrator/orchestrator/graph.py` 内不再出现对 `RCAApiClient.add_tool_call/finalize_job` 的直接调用。

---

## B1：EvidencePublisher（稳定幂等 + job 绑定 + createdBy）
改动文件：
- `tools/ai-orchestrator/orchestrator/tools_rca_api.py`
  - 修改：
    - `save_mock_evidence(self, incident_id, summary, raw, job_id=None, idempotency_key=None, created_by=None) -> str`
    - `save_evidence_from_query(self, incident_id, kind, query, result, job_id=None, idempotency_key=None, created_by=None) -> str`
  - 行为：
    - 若传入 `idempotency_key` 则直接使用；否则维持旧 uuid 行为（兼容）

新增文件（建议）：
- `tools/ai-orchestrator/orchestrator/runtime/evidence_publisher.py`
  - `class EvidencePublisher`：
    - `publish_mock(incident_id, summary, raw, job_id, node_name, kind) -> evidence_id`
    - `publish_from_query(incident_id, kind, query, result, job_id, node_name) -> evidence_id`
    - 内部生成稳定 idempotencyKey，例如：
      - `f"ai:{job_id}:{node_name}:{kind}:{sha256(query_json)[:16]}"`

验收标准：
- 同一 job+node+kind+query 重试两次，服务端只生成 1 条 evidence（依赖 `SaveEvidenceRequest.idempotencyKey`）。
- evidence 存储带 `jobID=job_id`，`createdBy="ai:{job_id}"`（见 `pkg/api/apiserver/v1/evidence.proto:SaveEvidenceRequest`）。

---

## B2：并发 seq（线程安全 + 单调递增）
改动文件（若已有 reporter）：
- `tools/ai-orchestrator/orchestrator/runtime/toolcall_reporter.py`
  - `next_seq()` 增加 `threading.Lock` 保护
  - 允许 `report(..., evidence_ids: list[str] | None)`，写入 `CreateAIToolCallRequest.evidenceIDs` 字段

若无 reporter（当前包状态）：
- 在 `tools/ai-orchestrator/orchestrator/tools_rca_api.py:add_tool_call` 的调用方（graph 层）引入 seq allocator（不推荐长期方案；仅过渡）

验收标准：
- 并发 10 个线程同时 report 100 次 toolcall：seq 不重复、不回退。
- 服务端仍以 `(job_id, seq)` 幂等（`internal/apiserver/biz/v1/ai_job/ai_job.go:CreateToolCall`）。

---

## B3：错误自动重试矩阵（仅 retryable）
新增文件（建议）：
- `tools/ai-orchestrator/orchestrator/runtime/retry.py`
  - `class RetryPolicy`：max_attempts, base_backoff_s, max_backoff_s, jitter
  - `def run_with_retry(fn, classify_error)`

改动点：
- 将重试应用在：
  - `toolcall` 上报（POST tool-calls）
  - evidence 保存（POST incidents/:id/evidence）
  - finalize（POST finalize）
  - heartbeat（按需；注意 owner/lease 丢失不重试）

分类依据（从代码上看）：
- owner/lease 冲突是 HTTP 409（`internal/pkg/errno/ai_job.go:ErrAIJobInvalidTransition`, `ErrAIToolCallStatusConflict`）
- missing owner header：`internal/apiserver/handler/ai_job.go:requireOrchestratorInstanceID` 会写 `errorsx.ErrInvalidArgument`

验收标准：
- 模拟 transport timeout：会重试 N 次后失败。
- 模拟 409：不重试，直接 fail-fast，并触发“停止后续写入”（如终止本 job 运行）。

---

## B4：LangGraph 更细粒度 ToolCall 上报（按节点/动作拆分）
改动文件：
- `tools/ai-orchestrator/orchestrator/graph.py`

现状（从代码上看）：
- `write_tool_calls` 节点集中写 2 条 toolcall（`seq=1/2`）
- `collect_evidence` 内部会调用 `client.save_mock_evidence/save_evidence_from_query` 落库 evidence

Phase B 目标：
- 将 toolcall 上报下沉到：
  - `load_job_and_start`：上报 start claim 结果（ok/owner_lost）
  - `collect_evidence`：分别上报：
    - query_metrics/query_logs（request/response/latency）
    - save_evidence（写 evidenceIDs）
  - `finalize_job`：上报 finalize，并在 finalize request 中写 `evidenceIDs=state.evidence_ids`

实现方式（建议）：
- 删除或弱化 `write_tool_calls` 节点，改为每个节点自身在关键点调用 `runtime.report_tool_call(...)`

验收标准：
- toolcalls 列表能看到每个节点/动作的记录（不再只有 2 条汇总）。
- 每条 toolcall 如果产生了 evidence，则 `evidenceIDs` 非空并包含新增 evidenceID。
- Finalize 请求携带 evidenceIDs（见 `pkg/api/apiserver/v1/ai_job.proto:FinalizeAIJobRequest`）。

---

## B5：集成测试/脚本
新增测试（建议）：
- `tools/ai-orchestrator/tests/test_evidence_idempotency.py`
  - 验证 EvidencePublisher 稳定 idempotencyKey 生成
- `tools/ai-orchestrator/tests/test_seq_concurrency.py`
  - 多线程 seq 不冲突

脚本（建议）：
- `tools/ai-orchestrator/scripts/run_one_job_phaseB.sh`
  - 启用并发与 query，观察 toolcall/evidence/finalize 写入效果


---

1/verification/plan.go: BuildPlan`

**验收标准**

* 无代码改动也可完成；产出为“确认记录”（在 PR 描述即可）。

---

## Phase C1 — SDK：补齐 PostFinalize 所需 API（RCAApiClient）

**改动文件**

* `tools/ai-orchestrator/orchestrator/tools_rca_api.py`

**新增/修改函数**

1. `def list_tool_calls(self, job_id: str, limit: int=200, offset: int=0, seq: int|None=None) -> dict`

   * 调用：`self._request("GET", f"/v1/ai/jobs/{job_id}/tool-calls", params=...)`
   * 与服务端路由对齐：`internal/apiserver/handler/ai_job.go: jobGroup.GET("/:jobID/tool-calls", handler.ListAIToolCalls)`
2. `def create_incident_verification_run(self, incident_id: str, source: str, step_index: int, tool: str, observed: str, meets_expectation: bool, params_json: dict|None=None, actor: str|None=None) -> dict`

   * 调用：`self._request("POST", f"/v1/incid# Phase C Plan — PostFinalize KBRefs/VerificationPlan Observer + VerificationRuns Executor

## Phase C0 — 代码定位与约束确认（只读）

**目的**：确保新增能力仍尽量限制在 `tools/ai-orchestrator` 内，复用 Phase B runtime 与 retry。

* 参考实现与依赖点（只读确认）：

  * `tools/ai-orchestrator/orchestrator/runtime/runtime.py: OrchestratorRuntime._execute_with_retry`（统一重试入口）
  * `tools/ai-orchestrator/orchestrator/tools_rca_api.py: RCAApiClient._request`（统一 header/envelope/error 分类）
  * 服务端路由：

    * `internal/apiserver/handler/ai_job.go`：`GET /v1/ai/jobs/:jobID/tool-calls`
    * `internal/apiserver/handler/incident.go`：`POST /v1/incidents/:incidentID/verification-runs`
  * 服务端生成逻辑：

    * `internal/apiserver/biz/v1/ai_job/ai_job.go: buildVerificationPlan / runKBBestEffort`
    * `internal/apiserver/biz/vents/{incident_id}/verification-runs", json_body=...)`
   * 与 proto 对齐：`pkg/api/apiserver/v1/incident.proto: CreateIncidentVerificationRunRequest`

**验收标准**

* 单元测试可 mock session.request，验证 path / method / payload 字段正确
* `RCAApiError` 分类保持不变（仍由 `_request` 抛）

---

## Phase C2 — Runtime：PostFinalizeObserver（拉取 + 解析）

**新增文件**

* `tools/ai-orchestrator/orchestrator/runtime/post_finalize.py`
* `tools/ai-orchestrator/orchestrator/runtime/__init__.py`（如需导出）

**新增类型/函数**

* `@dataclass class PostFinalizeSnapshot:`

  * `verification_plan: dict|None`
  * `kb_refs: list[dict]`（从 toolcall response_json 解析）
  * `target_tool_call_seq: int|None`
* `def fetch_post_finalize_snapshot(client: RCAApiClient, job_id: str, incident_id: str, retry: RetryExecutor, timeout_s: float) -> PostFinalizeSnapshot`

  * `client.get_incident(incident_id)` 读取 `diagnosisJSON`
  * `client.list_tool_calls(job_id)` 读取 toolcalls，并在每条 toolcall 的 `responseJSON` 中查找 `kb_refs`
  * 读取行为可走 `retryable_transport / retryable_5xx / 429` 的 retry（复用 `RetryExecutor`）

**与服务端字段对齐**

* `kb_refs` 注入位置：`internal/apiserver/biz/v1/kb/kb.go: InjectRefsToToolCall` 会写入 toolcall response payload 的 `payload["kb_refs"]`
* verification plan 注入位置：`internal/apiserver/biz/v1/ai_job/ai_job.go: injectVerificationPlanIntoDiagnosis` 写入 `payload["verification_plan"]`

**验收标准**

* 单元测试：给定 toolcalls 响应样例（包含/不包含 kb_refs）、incident diagnosisJSON（包含/不包含 verification_plan），解析结果正确

---

## Phase C3 — Runtime：VerificationRunner（执行 MCP + 写 verification-runs）

**新增文件**

* `tools/ai-orchestrator/orchestrator/runtime/verification_runner.py`

**新增类型/函数**

* `@dataclass class VerificationStepResult:`

  * `step_index: int`
  * `tool: str`
  * `params: dict`
  * `observed: str`
  * `meets_expectation: bool`
  * `raw_output: dict|None`
  * `error: str|None`
* `def run_verification_plan(client: RCAApiClient, incident_id: str, plan: dict, source: str, retry: RetryExecutor) -> list[VerificationStepResult]`

  * 解析 `plan["steps"]`（schema 来源：`internal/apiserver/biz/v1/verification/plan.go: Plan/Step`）
  * 调用 `client._mcp_call(step["tool"], step["params"], idempotency_key=...)`（MCP client：`tools/ai-orchestrator/orchestrator/mcp_client.py: MCPClient.call`）
  * 将每步结果写入：`client.create_incident_verification_run(...)`
  * observed 生成规则（最小实现）：

    * success：截取 output 的 compact json 前 N 字符（例如 512）
    * error：写 error message 前 N 字符
  * meetsExpectation（最小实现）：

    * 先实现 `expected.type == "exists"`：只要 output 非空即 true
    * `contains_keyword`：检查 output json 字符串是否包含 keyword
    * 其他类型先 conservative：false，并把原因写入 observed
    * expected 类型集合来源：`internal/apiserver/biz/v1/verification/plan.go: allowedExpectedTypes`

**验收标准**

* 单元测试：mock MCP 调用返回 payload，验证每一步都触发 `create_incident_verification_run`
* 覆盖至少 `exists` 与 `contains_keyword` 两类 expected

---

## Phase C4 — main：挂接 Phase C（Finalize 后执行）

**改动文件**

* `tools/ai-orchestrator/orchestrator/main.py`

**改动点**

* 在 `_invoke_graph` 完成 `runtime.finalize(...)` 后（且 succeeded）：

  1. 触发 `PostFinalizeObserver.fetch_post_finalize_snapshot`
  2. 若 snapshot.verification_plan 存在且 steps 非空：调用 `run_verification_plan(...)`
* 增加 env 开关（建议）：

  * `RUN_VERIFICATION=1/0`（默认 0）
  * `POST_FINALIZE_OBSERVE=1/0`（默认 1）
  * `VERIFICATION_SOURCE`（默认 "playbook" 或 "ai_job"；字段来源：`incident.proto: source`）

**验收标准**

* `RUN_VERIFICATION=0` 时行为不变
* `RUN_VERIFICATION=1` 且 verification_plan 存在时，会产生写入 verification-runs 的 HTTP 调用（可通过 mock / 端到端脚本验证）

---

## Phase C5 — 测试与脚本

**改动文件**

* `tools/ai-orchestrator/tests/test_runtime_sdk.py`（新增测试类/用例）

**新增测试建议**

* `PostFinalizeObserverTest`：kb_refs / verification_plan 解析
* `VerificationRunnerTest`：expected.exists / contains_keyword 判断 + 写入 payload
* `RCAApiClientVerificationRunTest`：verify endpoint path/payload

**验收标准**

* `python3 -m unittest discover -s tests -p 'test_*.py' -v` 全绿
