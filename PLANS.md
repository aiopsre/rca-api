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

## Phase B（后续扩展，非本次必须）
> 先占位，给 Codex 后续迭代用。

B1. EvidencePublisher 接入（Evidence 独立资源显式落库）
- 复用 `tools_rca_api.py:save_mock_evidence/save_evidence_from_query`
- Runtime 提供 `publish_evidence(...)` 并返回 evidence_id 列表

B2. ToolCall 并发 seq（线程安全 + 单调递增）
- ToolCallReporter seq 改成原子自增 / lock
- 对齐服务端幂等键 `(job_id, seq)` 不变

B3. 细粒度错误分类 -> 自动重试矩阵
- 对 `RCAApiError` 分类后：仅对 retryable 类做指数退避
- owner_lost 直接 fail-fast，停止 job

B4. 更深 LangGraph 集成（更多节点上报、更严格“节点不碰 HTTP”）
- 所有节点对外仅调用 runtime 接口
