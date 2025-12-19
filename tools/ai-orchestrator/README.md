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
- `POLL_INTERVAL_MS`：默认 `1000`
- `CONCURRENCY`：默认 `1`
- `RUN_QUERY`：默认 `0`（`0`=保存 mock evidence，`1`=query metrics + save）
- `DS_BASE_URL`：`RUN_QUERY=1` 时需要
- `AUTO_CREATE_DATASOURCE`：默认 `1`
- `DEBUG`：默认 `0`

## Notes

- P0 保持串行执行（`CONCURRENCY=1` 推荐）。
- 任一节点异常会写入 `last_error`，并继续执行 `finalize_job` 走 failed 路径。
- 仅做只读取证与 diagnosis 写回，不包含高风险自动处置动作。
