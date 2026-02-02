# RCA Platform Refactor Plan (v3)

## 核心判断

基于最新未重构代码，这轮工作不应再定义为“从 0 到 1 的平台化重构”。

更准确的定义是：

> **对已经完成一部分平台化演进的代码进行收口整合、契约显式化和边界稳定化。**

---

## 当前已完成到什么程度

最新代码已经具备以下事实：

1. `rca-apiserver` 已是明显的控制面
2. `ai-orchestrator` 已经部分产品化，具备：
   - `daemon/`
   - `runtime/`
   - `langgraph/`
   - `tooling/`
   - `sdk/`
3. server 侧已实现：
   - `toolset resolve`
   - `strategy resolve`
   - `template registry`
4. Redis 对 template registry 已成为强依赖
5. `pipeline -> template_id + toolsets[] -> ToolInvokerChain` 已经成为事实执行链

因此，重构重点必须调整。

---

## 这轮重构的主要目标

1. **显式化已有 Runtime Contract**
2. **收口 Strategy / Template / Toolset / Tooling 真源**
3. **抽出 Integrations 边界**
4. **引入 Session Context**
5. **后续再统一 Trigger Router**
6. **补齐 Run Trace / Decision Trace**
7. **将 deterministic 模板层配置化**

---

## 非目标

当前明确不作为第一优先级：

- 将 LangGraph 并入 API 进程
- 从头发明全新的 runtime worker
- 立即把 `tools/ai-orchestrator` 大规模重命名为 `runtime-worker`
- 从零重做一整套脱离现有实现的 ToolRegistry 框架
- 一次性重写所有对外 API

---

## 推荐阶段顺序

## Phase 0 — 文档与基线校正

目标：
- 校正现有文档与最新代码一致
- 明确“收口整合而非从头建设”的原则
- 固化关键 ADR

交付物：
- `docs/architecture/current-state.md`
- `docs/architecture/target-architecture.md`
- `docs/architecture/refactor-principles.md`
- 关键 ADR

---

## Phase 1 — Runtime Contract 显式化

目标：
- 将现有 Go/Python 之间已经存在的事实契约显式化
- 统一 request/response models
- 补 contract tests

重点：
- claim/start
- renew/heartbeat
- toolcall report
- evidence publish
- finalize
- verification
- strategy resolve / template register 相关执行前契约

---

## Phase 2 — Strategy / Template / Toolset / Tooling 收口

目标：
- 明确以下边界：
  - strategy resolve
  - template registry
  - toolsets[]
  - Python local tool registry
  - MCP tools view
- 减少 metadata 重复定义

这一步是当前最关键的“真源收口”工作。

---

## Phase 3 — Integrations 边界抽离

目标：
- 从 EvidenceBiz 中抽出 datasource/query adapter interface
- 为 Prom/Loki/ES 之外的系统接入留出清晰位置
- 减少业务逻辑与外部系统适配耦合

---

## Phase 4 — Session Context

目标：
- 引入业务 session 模型
- 为 follow-up / replay / watch 提供共享上下文
- 让 AIJob 与 session 绑定

说明：
这是当前最新代码中仍未真正解决的重要缺口。

---

## Phase 5 — Runtime Worker 收口稳定化

目标：
- 固化现有 `daemon / runtime / langgraph / tooling / sdk` 分层
- 明确 config / version / health / logging 约定
- 让 worker 更正式，但不依赖大规模目录改名

---

## Phase 6 — Trigger Router

目标：
- 将 alert / manual / replay / cron / change 统一归一为 TriggerRequest

说明：
由于 strategy/tooling/session 边界更优先，因此 Trigger Router 排在其后。

---

## Phase 7 — Run Trace / Decision Trace

目标：
- 统一执行过程和结论的 trace 模型
- 串联 tool calls / evidence / diagnosis / notice

---

## Phase 8 — Deterministic 模板层

目标：
- playbook 模板化
- verification 模板化
- KB writeback policy 模板化

---

## 当前最值得坚持的四件事

1. Runtime Contract
2. Tooling 真源收口
3. Integrations 边界
4. Session Context

这四件事是当前版本继续往平台方向走的最关键基础。

---

## 成功标准

这轮重构成功的标志不是“目录名更好看”，而是：

- 已有平台骨架被显式化和稳定化
- strategy / template / toolsets / tooling 不再长期漂移
- Go/Python 执行契约更清晰
- Session 与 Trigger 的扩展基础建立
- 后续扩 Tempo/K8s/Ticket/Replay 时不再需要继续挤压现有边界
