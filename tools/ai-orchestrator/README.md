# ai-orchestrator (P0)

`tools/ai-orchestrator` 是 P0 的 LangGraph 编排器实现，负责轮询 `queued` AIJob 并执行固定 4 节点拓扑：

`load_job_and_start -> collect_evidence -> write_tool_calls -> finalize_job`

## Quick Start

1. 安装依赖（在本目录）：

```bash
python3 -m venv .venv
source .venv/bin/activate
pip install -e .
```

2. 启动 orchestrator：

```bash
SCOPES='*' RUN_QUERY=0 python -m orchestrator.main
```

## Environment Variables

- `BASE_URL`：默认 `http://127.0.0.1:5555`
- `SCOPES`：非空时所有请求附带 `X-Scopes`
- `INSTANCE_ID`：orchestrator 实例标识，默认 `{hostname}-{pid}`，用于 AIJob lease owner
- `RCA_API_SCOPES`：仅用于 MCP shim 调用（`/v1/mcp/tools/call`），注入到 `X-Scopes`；为空默认拒绝（fail-fast）
- `MCP_VERIFY_REMOTE_TOOLS`：默认 `0`；`1` 时启动拉取 `/v1/mcp/tools` 做 registry 一致性校验
- `POLL_INTERVAL_MS`：默认 `1000`（仅用于错误重试或禁用 long poll 时的 sleep/backoff）
- `LONG_POLL_WAIT_SECONDS`：默认 `20`（范围 `0~30`；上次拉取为空时用于 `wait_seconds`）
- `LEASE_HEARTBEAT_INTERVAL_SECONDS`：默认 `10`，运行中续租间隔
- `CONCURRENCY`：默认 `1`
- `RUN_QUERY`：默认 `0`（`0`=保存 mock evidence，`1`=query metrics + save）
- `FORCE_NO_EVIDENCE`：默认 `0`（`1`=强制走 L2 证据不足路径，保存占位 evidence + 低置信度 missing_evidence 结论）
- `FORCE_CONFLICT`：默认 `0`（`1`=强制走 L3 证据冲突路径，不依赖真实 datasource，保存至少 2 条占位 evidence + conflict_evidence 低置信度结论）
- `DS_BASE_URL`：`RUN_QUERY=1` 时需要
- `AUTO_CREATE_DATASOURCE`：默认 `1`
- `DEBUG`：默认 `0`
- `SKILLS_EXECUTION_MODE`：默认 `catalog`；`prompt_first` 时启用 Agent 驱动的 prompt-only Skill 链路
- `SKILLS_CACHE_DIR`：Skill bundle 本地缓存目录
- `SKILLS_LOCAL_PATHS`：开发态本地 Skill override 目录列表（逗号分隔）
- `AGENT_MODEL`：`prompt_first` 模式下使用的 OpenAI-compatible 模型名
- `AGENT_BASE_URL`：`prompt_first` 模式下的 OpenAI-compatible base URL
- `AGENT_API_KEY`：`prompt_first` 模式下的 API key
- `AGENT_TIMEOUT_SECONDS`：`prompt_first` 模式下模型请求超时
- `SKILLS_TOOL_CALLING_MODE`：默认 `disabled`；`evidence_plan_single_hop` 启用单次 `query_logs`，`evidence_plan_dual_tool` 启用受控的 `query_metrics + query_logs`

## Notes

- P0 保持串行执行（`CONCURRENCY=1` 推荐）。
- 任一节点异常会写入 `last_error`，并继续执行 `finalize_job` 走 failed 路径。
- 仅做只读取证与 diagnosis 写回，不包含高风险自动处置动作。
- 当前 prompt-first Skills 已打通 `diagnosis.enrich` 和 `evidence.plan`。
- `diagnosis.enrich` 当前支持两类单 executor 形态：
  - prompt executor
  - script executor（`executor_mode=script`）
- `diagnosis.enrich` 已支持 executor resources 的渐进式披露。
- `evidence.plan` 已支持“多个 knowledge skills + 单 executor skill”的运行模型。
- `evidence.plan` 的 executor 现在同时支持：
  - prompt executor
  - script executor（`executor_mode=script`）
- `evidence.plan` 还支持 Knowledge / Executor 两侧的资源渐进式披露：
  - worker 扫描 `references/`、`templates/`、`examples/`
  - Agent 先看摘要，再点名需要的资源 id
  - runtime 只加载被点名的文本资源正文
- checked-in prompt-only Skill 样板位于：
  - `tools/ai-orchestrator/skill-bundles/diagnosis-enrich/SKILL.md`
  - `tools/ai-orchestrator/skill-bundles/diagnosis-script-enrich/SKILL.md`
- `tools/ai-orchestrator/skill-bundles/evidence-plan/SKILL.md`
  - `evidence.plan` 的 executor 样板
- `tools/ai-orchestrator/skill-bundles/evidence-script-plan/SKILL.md`
  - `evidence.plan` 的 script executor 样板
- `tools/ai-orchestrator/skill-bundles/elasticsearch-evidence-plan/SKILL.md`
  - Elasticsearch / ECS 风格的 `evidence.plan` knowledge 样板
- `tools/ai-orchestrator/skill-bundles/prometheus-evidence-plan/SKILL.md`
  - Prometheus / metrics planning 的 `evidence.plan` knowledge 样板
- `tools/ai-orchestrator/skill-bundles/diagnosis-script-enrich/scripts/executor.py`
  - `diagnosis.enrich` 的 script executor 固定 entrypoint 样板
- 受控 tool-calling 仍只允许挂在 executor 上，当前 `evidence.plan` 的 prompt executor 和 script executor 都支持最多一次 `mcp.query_metrics` + 最多一次 `mcp.query_logs`，并让 `query_metrics` / `query_logs` 节点复用预热结果，但默认保持关闭
