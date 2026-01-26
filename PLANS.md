# Patch Plan：Lease lost 时 query 节点停止且不写 toolcall（将 query 节点纳入 _guard）

---

## P1：graph.py 将 query 节点纳入 _guard

**改动文件**
- `tools/ai-orchestrator/orchestrator/graph.py`

**改动点**
- 在 `build_graph(...)` 中，将两个节点的注册方式改为 `_guard(...)` 包装：
  - `query_metrics`
  - `query_logs`

**验收标准**
- 代码静态检查：两节点与其他节点一致地经过 `_guard`
- lease_lost 时不会调用 `runtime.report_tool_call`（写路径）

---

## P2：补充/更新图级单测

**改动文件**
- `tools/ai-orchestrator/tests/test_graph_phasee.py`

**新增/修改用例**
- 新增 `test_phasee_lease_lost_skips_query_nodes_and_no_toolcall_write`（或在现有用例中覆盖）
  - mock runtime：`is_lease_lost()` 在进入 query 节点前返回 True
  - 执行 graph
  - 断言：
    - query 节点不触发 `_report_node_action` / `runtime.report_tool_call`
    - 图能按既定 guard 行为收敛（不要求成功，只要求不写 toolcall）

**验收标准**
- `python3 -m unittest discover -s tests -p 'test_*.py' -v` 全绿
