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
