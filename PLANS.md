# Refactor Plan：模块化 Daemon 与 LangGraph（不改行为）

---

## Phase R1：引入 langgraph/ 目录并迁移纯配置与纯 helper
**新增文件**
- `tools/ai-orchestrator/orchestrator/langgraph/__init__.py`
- `tools/ai-orchestrator/orchestrator/langgraph/config.py`
- `tools/ai-orchestrator/orchestrator/langgraph/helpers.py`

**迁移内容（从 graph.py）**
- `class OrchestratorConfig`
- `_extract_incident_id/_extract_input_hints/_resolve_force_switches/_resolve_a3_budget/...`
- `_query_result_* / _ordered_unique_strings / _append_evidence` 等不依赖 LangGraph builder 的 helper

**改动文件**
- `tools/ai-orchestrator/orchestrator/graph.py`（更新引用）
- `tools/ai-orchestrator/orchestrator/main.py`（如有直接引用 OrchestratorConfig）

**验收**
- import 无循环
- 运行 `python3 -m unittest ...` 全绿

---

## Phase R2：迁移 reporting + quality_gate + diagnosis
**新增文件**
- `orchestrator/langgraph/reporting.py`（迁移 `_report_node_action`）
- `orchestrator/langgraph/quality_gate.py`（迁移质量门槛相关函数）
- `orchestrator/langgraph/diagnosis.py`（迁移 diagnosis 构造相关函数）

**改动**
- `graph.py` 中节点代码改为从这些模块 import 对应函数

**验收**
- `test_runtime_sdk.py`、`test_graph_phasee.py` 全绿

---

## Phase R3：迁移 guard + nodes（保留 query entry 归一化）
**新增文件**
- `orchestrator/langgraph/guard.py`（迁移 `_guard`、`_is_finalize_succeeded` 等）
- `orchestrator/langgraph/nodes.py`（迁移所有节点函数）
  - 必须保留你们 patch 中的：
    - query 节点走 `_guard`
    - `_query_metrics_entry/_query_logs_entry` 将 guard 短路返回归一为 dict patch（避免 fan-out 冲突）

**改动**
- `graph.py` 变薄：只 re-export `OrchestratorConfig/build_graph`

**验收**
- 重点跑：`tests/test_graph_phasee.py::test_phasee_lease_lost_skips_query_nodes_and_no_toolcall_write`
- 全测试通过

---

## Phase R4：迁移 build_graph 到 builder.py
**新增文件**
- `orchestrator/langgraph/builder.py`（迁移 `build_graph(...)`）
  - 只负责 add_node/add_edge/compile
  - 节点函数从 `langgraph.nodes` 引用
  - guard 从 `langgraph.guard` 引用

**改动**
- `orchestrator/graph.py`：
  - `from orchestrator.langgraph.config import OrchestratorConfig`
  - `from orchestrator.langgraph.builder import build_graph`

**验收**
- 代码结构完成：graph.py 不再包含节点逻辑
- tests 全绿

---

## Phase R5：daemon 模块化（settings + runner）
**新增文件**
- `orchestrator/daemon/__init__.py`
- `orchestrator/daemon/settings.py`（迁移 Settings/load_settings/_env_*）
- `orchestrator/daemon/runner.py`（迁移 `_invoke_graph`）
- （可选）`orchestrator/daemon/health.py`（迁移 detect/parse）

**改动**
- `orchestrator/main.py` 变薄：只保留 `main()`，调用 `load_settings()` 与 `runner.run_once/run_forever`

**验收**
- `python3 -m orchestrator.main` 能启动（基本 smoke）
- tests 全绿


---

## Phase F：多模板 registry（Pipeline-only 选择）

### F1：新增 templates 目录与 basic_rca 模板 builder
**新增文件**
- `tools/ai-orchestrator/orchestrator/langgraph/templates/__init__.py`
- `tools/ai-orchestrator/orchestrator/langgraph/templates/basic_rca.py`

**实现内容**
- 在 `basic_rca.py` 中提供：
  - `build_basic_rca_graph(runtime: OrchestratorRuntime, cfg: OrchestratorConfig)`
- 其内部应复用现有 wiring（可直接从当前 `langgraph/builder.py:build_graph` 迁移或包装调用）
- 保持 query guard patch 语义不变（query entry 归一化仍生效）

**验收**
- basic_rca 模板运行行为不变（回归现有 tests）

---

### F2：新增 registry（pipeline -> builder）
**新增文件**
- `tools/ai-orchestrator/orchestrator/langgraph/registry.py`

**实现内容**
- 定义：
  - `class UnknownPipelineError(Exception)`
  - `get_template_builder(pipeline: str) -> Callable[[OrchestratorRuntime, OrchestratorConfig], Any]`
- registry 至少包含：
  - `"basic_rca" -> build_basic_rca_graph`
- pipeline 规范化：
  - `pipeline = (pipeline or "").strip().lower()`
  - 空值按 `"basic_rca"` 处理（匹配 server 默认语义）

**验收**
- unknown pipeline 抛 `UnknownPipelineError`

---

### F3：daemon 按 job.pipeline 选择模板（只看 pipeline）
**改动文件**
- `tools/ai-orchestrator/orchestrator/daemon/runner.py`

**实现内容**
- 在 `_invoke_graph(...)` 中，在 build graph 前读取 job：
  - `job = runtime.get_job(job_id)`
  - 从 job 中解析 `pipeline` 字段（需要兼容可能的 `pipeline`/`Pipeline`/`data.pipeline` 包裹；你们 SDK 已做过 envelope 兼容解析）
- 选择模板：
  - `builder = get_template_builder(pipeline)`
  - `compiled_graph = builder(runtime, graph_cfg)`
- unknown pipeline 处理（本期 fail-fast）：
  - 记录日志（包含 job_id + pipeline）
  - 将 `state.last_error` 设置为明确错误，并调用 `runtime.finalize(status="failed", ...)` 或按现有失败路径终止（需与现有 finalize 语义一致）

**验收**
- pipeline=basic_rca 正常执行
- pipeline=unknown 不执行 query/save 等节点

---

### F4：测试补充
**新增/改动文件**
- `tools/ai-orchestrator/tests/test_graph_template_registry.py`（推荐新增）
  或在已有 `tests/test_graph_phasee.py` 扩展

**测试用例**
1) `test_pipeline_basic_rca_selects_basic_builder`
- fake runtime.get_job 返回 pipeline=basic_rca
- 断言 runner 选择 basic builder（可通过 monkeypatch registry 的 builder spy）

2) `test_pipeline_unknown_fail_fast`
- fake runtime.get_job 返回 pipeline=unknown
- 断言：
  - 未调用 build_basic_rca_graph
  - 未调用 runtime.query_metrics/query_logs
  - 产生明确错误（state.last_error / 日志 / finalize failed）

**验收**
- `python3 -m unittest discover -s tests -p 'test_*.py' -v` 全绿

---

## Phase G：Toolset Registry（Multi MCP Servers + Skills，pipeline-only）

> 目标：将 “工具执行面” 从 `RCAApiClient.mcp_client` 解耦出来，允许 orchestrator 侧自由接入多个 MCP Server 与 Skills，并只按 `AIJob.pipeline` 选择 toolset。
>
> 实施状态（2026-03-05）：
> - 已完成 G1~G4 代码落地（`tools/ai-orchestrator`）
> - 验证 runner 已改为通过 runtime 注入的 `call_tool`
> - pipeline/template 与 pipeline/toolset 选择均保持 fail-fast 语义

### G1：新增 toolset 配置与解析（不引入新依赖）

**新增文件**
- `tools/ai-orchestrator/orchestrator/tooling/__init__.py`
- `tools/ai-orchestrator/orchestrator/tooling/toolset_config.py`
  - `ToolsetConfig` / `ProviderConfig` / `load_toolset_config_from_env(...)`
  - 支持 env：
    - `TOOLSET_CONFIG_PATH`
    - `TOOLSET_CONFIG_JSON`

**改动文件**
- `tools/ai-orchestrator/orchestrator/daemon/settings.py`
  - 新增 settings 字段：
    - `toolset_config_path: str`
    - `toolset_config_json: str`
  - `load_settings()` 读取上述 env（默认空）

**验收**
- 无新增第三方依赖（仅 stdlib）
- `python3 -m unittest ...` 全绿

---

### G2：新增 ToolInvoker + Providers（MCP HTTP / Skills）

**新增文件**
- `tools/ai-orchestrator/orchestrator/tooling/invoker.py`
  - `class ToolInvokeError(Exception)`
  - `class ToolInvoker`
    - `call(tool: str, input: dict, idempotency_key: str | None) -> dict`
    - tool allowlist 校验
    - provider 路由（按 toolset providers 顺序或显式映射）
- `tools/ai-orchestrator/orchestrator/tooling/providers/__init__.py`
- `tools/ai-orchestrator/orchestrator/tooling/providers/mcp_http.py`
  - `class MCPHttpProvider`
    - POST `{base_url}/v1/mcp/tools/call`
    - 复用 `mcp_client.py` 的 retryable 语义（可复制 `_is_retryable`，避免再依赖 `tool_registry.py:get_tool`）
- `tools/ai-orchestrator/orchestrator/tooling/providers/skills.py`
  - `class SkillsProvider`
    - 从 `module` 动态 import，要求暴露 `call(tool, input, idempotency_key) -> dict`
    - 默认 fail-fast（除非返回中显式标记 retryable）

**验收**
- invoker 对未知 tool / tool 不在 allowlist：fail-fast（抛稳定异常）
- provider 对调用失败：返回可判定 retryable 的错误（供 runtime 重试执行器判断）

---

### G3：Runtime 注入 invoker，并改造 verification runner 走 runtime.call_tool

**改动文件**
- `tools/ai-orchestrator/orchestrator/runtime/runtime.py`
  - `OrchestratorRuntime.__init__(...)` 新增参数：`tool_invoker: ToolInvoker | None`
  - 新增方法：
    - `call_tool(tool: str, params: dict, idempotency_key: str | None = None) -> dict`
      - 内部必须走 `_execute_with_retry(...)` 以保持与写操作一致的重试矩阵
- `tools/ai-orchestrator/orchestrator/runtime/verification_runner.py`
  - `VerificationRunner.__init__` 新增参数：
    - `call_tool: Callable[[str, dict, str | None], dict]`
  - `run(...)` 内将 `self._client.mcp_client.call(...)` 替换为 `call_tool(...)`
  - 仍保留 `_normalize_tool_name` 对 `mcp.` 前缀兼容

**验收**
- verification runner 不再引用 `RCAApiClient.mcp_client`（可通过 grep/测试断言）
- 原有 dedupe/budget/observed<=512 语义不变

---

### G4：runner 按 pipeline 选择 toolset，并 fail-fast 缺失配置

**改动文件**
- `tools/ai-orchestrator/orchestrator/daemon/runner.py`
  - 在 pipeline 选模板成功后、invoke graph 前：
    - 加载 toolset config（一次加载可缓存到进程级，避免每 job 读文件）
    - `toolset_id = pipelines[pipeline_normalized]`（pipeline-only）
    - 构造 invoker 并注入 `OrchestratorRuntime(...)`
  - 缺失 pipeline->toolset 映射 / toolset 不存在：fail-fast
    - 记录日志：`job_id/pipeline/toolset_id`
    - `runtime.finalize(status="failed", error_message="toolset_selection_failed: ...")`
    - 不进入 graph，不执行 query/save，不写 toolcall

**新增测试**
- `tools/ai-orchestrator/tests/test_toolset_registry_phaseg.py`
  1) `test_pipeline_selects_toolset_and_builds_invoker`
  2) `test_unknown_toolset_fail_fast_no_graph_invoke`
  3) `test_verification_runner_uses_runtime_call_tool_not_client_mcp`

**验收**
- `python3 -m unittest discover -s tests -p 'test_*.py' -v` 全绿
- 行为回归：pipeline registry（unknown pipeline）fail-fast 语义保持不变

---

## Phase H：rca-api Toolset Registry（pipeline 绑定 + 只读下发）& orchestrator 对接

> 目标：rca-api 提供 toolset 配置下发（不执行 tools）；orchestrator 在未设置本地 TOOLSET_CONFIG_* 时，从 server resolve toolset。

### H1：新增 protobuf（server API message）

**新增文件**
- `pkg/api/apiserver/v1/orchestrator_toolset.proto`
  - `message OrchestratorToolsetProvider`
  - `message OrchestratorToolset`
  - `message ResolveToolsetRequest`（query: pipeline）
  - `message ResolveToolsetResponse`（toolset）

**改动文件**
- 运行 `make protoc`（依据 `Makefile:protoc` 会生成 `*.pb.go` 与 defaults/tag 注入）

**验收**
- `pkg/api/apiserver/v1/orchestrator_toolset.pb.go` 生成并可编译

---

### H2：server 侧配置读取（JSON env/path）

**新增文件**
- `internal/apiserver/pkg/orchestratorcfg/toolset_config.go`
  - `type ToolsetConfig`（与 orchestrator Phase G schema 对齐）
  - `LoadFromEnv() (*ToolsetConfig, error)`
  - `Resolve(pipeline string) (*OrchestratorToolset, error)`
  - 支持 env：
    - `RCA_TOOLSET_CONFIG_JSON`
    - `RCA_TOOLSET_CONFIG_PATH`

**验收**
- `LoadFromEnv` 支持 JSON 直传与 file path
- pipeline normalize 规则与 orchestrator `tooling/toolset_config.py:normalize_pipeline_key` 对齐（空值→basic_rca）

---

### H3：新增 biz + handler：/v1/orchestrator/toolsets/resolve

**新增文件**
- `internal/apiserver/biz/v1/orchestrator_toolset/toolset.go`
  - `type OrchestratorToolsetBiz interface{ Resolve(ctx, req) (*resp, error) }`
  - `func New(store store.IStore) OrchestratorToolsetBiz`
  - 内部调用 `orchestratorcfg.LoadFromEnv().Resolve(req.pipeline)`
- `internal/apiserver/handler/orchestrator_toolset.go`
  - `func (h *Handler) ResolveOrchestratorToolset(c *gin.Context)`
  - `func init()` 注册路由：
    - `rg := v1.Group("/orchestrator", mws...)`
    - `rg.GET("/toolsets/resolve", handler.ResolveOrchestratorToolset)`
  - auth：`authz.RequireAnyScope(c, authz.ScopeAIRead)`

**改动文件**
- `internal/apiserver/biz/biz.go`
  - `IBiz` 增加 `OrchestratorToolsetV1()`
  - `biz` struct 增加 once+field，并在方法里 `orchestrator_toolset.New(b.store)`
- `internal/apiserver/pkg/validation/` 新增 `orchestrator_toolset.go`
  - `ValidateResolveToolsetRequest(...)`（pipeline 必填或允许空但 normalize）
- `internal/apiserver/handler/handler.go` 无需改（沿用 registrar）

**验收**
- `GET /v1/orchestrator/toolsets/resolve?pipeline=basic_rca` 返回 toolset
- 缺失 mapping 返回 404（或业务定义的 errno）
- 无权限返回 403（permission denied）

---

### H4：orchestrator 对接 server resolve（无本地 config 时）

**改动文件**
- `tools/ai-orchestrator/orchestrator/tools_rca_api.py`
  - `RCAApiClient.resolve_toolset(pipeline: str) -> dict`
    - GET `/v1/orchestrator/toolsets/resolve`，query pipeline
    - 兼容 envelope：payload.get("data", payload)
- `tools/ai-orchestrator/orchestrator/daemon/runner.py`
  - 在 `_select_tool_invoker` 内：
    - 若本地 `TOOLSET_CONFIG_*` 存在：沿用 Phase G（local）
    - 否则：调用 `client.resolve_toolset(pipeline)`，并用返回内容构造 `ToolsetConfig` / ToolsetDefinition，再 build invoker
  - resolve 失败：沿用 fail-fast finalize（graph build/invoke 前）

**新增测试**
- server：`internal/apiserver/handler/orchestrator_toolset_test.go`
  - 覆盖 resolve success / missing mapping / invalid config
- orchestrator：`tools/ai-orchestrator/tests/test_toolset_registry_phaseh.py`
  - 覆盖 “无 TOOLSET_CONFIG_* → 调 client.resolve_toolset → build invoker”
  - 覆盖 resolve 404 → fail-fast finalize & no graph invoke

**验收**
- `go test ./...` 通过
- `python3 -m unittest discover -s tools/ai-orchestrator/tests -p 'test_*.py' -v` 通过

---

## Phase I：运维可观测（Toolset/Provider/Tool Invoke）

> 目标：不改业务语义，仅补齐“job级 toolset 选择 + tool invoke”的可观测输出（日志 + toolcalls）。

### I1：runtime 增加通用观测上报（report_observation）

**改动文件**
- `tools/ai-orchestrator/orchestrator/runtime/runtime.py`
  - 新增方法：
    - `report_observation(tool: str, node_name: str, params: dict, response: dict, evidence_ids: list[str] | None = None)`
      - 内部直接调用现有 `report_tool_call(...)`
      - 必须尊重 lease-lost：若 `is_lease_lost()` 或 runtime 未 start，则仅记录日志不写 toolcall
  - 修改 `call_tool(...)`：
    - 在调用前/后统计 latency_ms
    - 捕获 `ToolInvokeError` / `RCAApiError` / `Exception` 并映射 `error_category`
    - 调用 `report_observation(tool="tool.invoke" 或 "tool.invoke_rejected", ...)`

**验收**
- call_tool 不改变返回/异常语义，仅新增观测 side-effect
- unit tests 覆盖 latency_ms 与 error_category 字段存在

---

### I2：runner 上报 toolset.select（pre-graph）

**改动文件**
- `tools/ai-orchestrator/orchestrator/daemon/runner.py`
  - toolset 选择成功后、build graph 前：
    - 调用 `runtime.report_observation(tool="toolset.select", node_name="runner.pre_graph", ...)`
    - 包含 pipeline/template/toolset_id/source/providers 摘要

**验收**
- toolset.select 在 graph build 前写入 toolcall（best-effort）
- toolset selection failed 时保留原 fail-fast finalize，额外记录日志（可选尽力上报 toolcall）

---

### I3：ToolInvoker 暴露路由决策（provider_id/type）

**改动文件**
- `tools/ai-orchestrator/orchestrator/tooling/invoker.py`
  - `ToolInvoker.call(...)` 返回时附带 meta：
    - 方式 A：返回 `(result, meta)`（需要最小改动链路）
    - 方式 B：result 内加入 `_meta` 字段（不污染业务字段时更好）
  - meta 至少包含：
    - provider_id
    - provider_type

> 选择其中一种，但要保持对 provider 返回 dict 的兼容。

**验收**
- runtime.call_tool 能拿到 provider_id/provider_type 并上报

---

### I4：新增测试

**新增/改动文件**
- `tools/ai-orchestrator/tests/test_observability_phasei.py`（新增）
  - `test_runner_emits_toolset_select_toolcall_pre_graph`
  - `test_runtime_call_tool_emits_tool_invoke_with_provider_meta`
  - `test_tool_not_allowed_emits_tool_invoke_rejected_and_raises`

**验收**
- `cd tools/ai-orchestrator && python3 -m unittest discover -s tests -p 'test_*.py' -v` 全绿
