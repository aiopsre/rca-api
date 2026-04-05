# AI RCA 系列文章

这组文章用于介绍 `rca-api` 的 AI RCA 平台设计、运行时边界和阶段性演进思路。

公开说明：

- 文中的示例告警、时间线、ID、指纹、接口参数均为示意或脱敏样本
- 文中的性能、时延和准确率数据用于解释设计取舍，不应视为通用基准
- 以仓库内当前实现和架构文档为准；文章中的路线图表述仅代表当时阶段目标

文章目录：

1. [从告警到补充通知：AI RCA 平台在值班场景中的辅助决策实践](./01-oncall-value-and-boundaries.md)
2. [AI RCA 的主链路设计：从 alert event 到 diagnosis / notice 的平台闭环](./02-main-closed-loop.md)
3. [控制面与执行面的分层设计：Go rca-api 与 Python orchestrator 为什么必须拆开](./03-control-plane-vs-execution-plane.md)
4. [AIJob 的租约机制与运行时：为什么 claim / heartbeat / reclaim 是平台级能力](./04-ai-job-lease-and-runtime.md)
5. [从告警到 Incident：AI RCA 平台的告警治理与对象收敛](./05-alert-to-incident-governance.md)
6. [补充通知的设计：为什么 AI RCA 需要第二次触达](./06-supplemental-notice-design.md)
7. [Skills、MCP 与 LangGraph：AI RCA 的三层装配运行时深度剖析](./07-skills-mcp-langgraph-runtime.md)
8. [AI RCA 第一阶段复盘：哪些问题真的被解决了，哪些还只是开始](./08-phase-one-retrospective.md)
