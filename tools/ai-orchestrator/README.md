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

## Notes

- P0 保持串行执行（`CONCURRENCY=1` 推荐）。
- 任一节点异常会写入 `last_error`，并继续执行 `finalize_job` 走 failed 路径。
- 仅做只读取证与 diagnosis 写回，不包含高风险自动处置动作。
