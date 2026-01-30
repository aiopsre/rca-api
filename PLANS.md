# RCA Orchestrator Branch Closeout Plan

本文件用于**收口当前分支**，不再继续扩展架构能力，而是将已经完成的控制面与执行面能力稳定下来，完成最终验证、文档整理与交接。

本分支以 **Phase L1** 为截止点。

---

# 一、分支目标

本分支已经完成从“单模板 + 单 toolset”向“平台声明策略、orchestrator 执行模板与 toolset chain”的演进。

当前最终目标不是继续增加功能，而是确保以下能力稳定可用：

1. `AIJob.pipeline` 作为唯一触发信号；
2. `rca-api` 作为控制面，负责：
   - template registry
   - strategy resolve
   - toolsets resolve（兼容旧路径）
3. `orchestrator` 作为执行面，负责：
   - template builder 选择
   - ToolInvoker / ToolInvokerChain
   - LangGraph 执行
   - runtime / lease / seq / toolcall / finalize
4. Redis 作为 template registry 的强依赖；
5. E2E 链路可运行，可观测，可 fail-fast。

---

# 二、最终执行架构

完整执行链路如下：

```text
AIJob.pipeline
        ↓
rca-api /v1/orchestrator/strategies/resolve
        ↓
template_id + toolsets[]
        ↓
orchestrator:
  get_template_builder(template_id)
  + ToolInvokerChain(toolsets[])
        ↓
LangGraph execution
        ↓
tool.invoke / finalize / notice / KB
```

关键边界：

- **pipeline**：仍为 string key，不资源化；
- **template 定义**：只存在于 orchestrator 代码；
- **template_id 注册**：由 orchestrator 上报到 rca-api；
- **strategy 声明**：由 rca-api 配置并 resolve 下发；
- **toolsets 执行**：始终由 orchestrator 执行，不在 rca-api 执行 tools。

---

# 三、已完成阶段概览

## Phase A-D：基础 orchestrator runtime 与 graph
已完成：
- Runtime
- LeaseManager
- ToolCallReporter
- EvidencePublisher
- Retry matrix
- 基本 graph 执行链路

## Phase E：LangGraph 稳定性与 guard
已完成：
- graph fan-out / fan-in
- query 节点 lease-lost guard
- finalize / observe / verification 等流程串接

## Phase F：模块化
已完成：
- `daemon/`
- `runtime/`
- `langgraph/`
- `tooling/`
- 薄入口保留兼容

## Phase G：Toolset runtime
已完成：
- `ToolInvoker`
- `MCP HTTP Provider`
- `Skills Provider`
- `allow_tools` 执行边界
- `toolset` 本地配置模型

## Phase H：平台 toolset resolve
已完成：
- `GET /v1/orchestrator/toolsets/resolve`
- pipeline-only 的 toolset 配置解析
- orchestrator 无本地 override 时从 server 获取 toolset

## Phase I：Observability
已完成：
- `toolset.select`
- `tool.invoke`
- provider / latency / error category / route info 观测

## Phase J / J+：Toolset Chain + Hardening
已完成：
- 本地 `pipelines` 支持 `str | list[str]`
- `ToolInvokerChain`
- `first_match` 规则
- 严格 fail-fast 配置校验
- `toolset_chain` / `resolved_from_toolset_id` / `route_policy`

## Phase K：server resolve 下发 `toolsets[]`
已完成：
- rca-api 支持 `toolsets[]`
- 保留 `toolset` 兼容旧客户端
- orchestrator server resolve 分支构造 `ToolInvokerChain`

## Phase L0：Template Registry（Redis 强依赖）
已完成：
- `POST /v1/orchestrator/templates/register`
- `GET /v1/orchestrator/templates`
- Redis 强依赖，不降级
- orchestrator 启动注册 + 60s 刷新

## Phase L1：Strategy Resolve
已完成：
- `GET /v1/orchestrator/strategies/resolve`
- `pipeline -> template_id + toolsets[]`
- resolve 时校验 `template_id` 必须已在 template registry 注册
- orchestrator 改为：
  - `pipeline -> resolve_strategy -> template_id -> builder`
  - 不再直接 `get_template_builder(pipeline)`

---

# 四、当前代码结构（最终形态）

## 1. rca-api 控制面

关键文件：

- `internal/apiserver/pkg/orchestratorcfg/toolset_config.go`
- `internal/apiserver/pkg/orchestratorcfg/strategy_config.go`
- `internal/apiserver/pkg/orchestratorregistry/template_registry.go`

关键 handler：

- `internal/apiserver/handler/orchestrator_toolset.go`
- `internal/apiserver/handler/orchestrator_strategy.go`
- `internal/apiserver/handler/orchestrator_template.go`

对应 API：

- `GET  /v1/orchestrator/toolsets/resolve`
- `GET  /v1/orchestrator/strategies/resolve`
- `POST /v1/orchestrator/templates/register`
- `GET  /v1/orchestrator/templates`

## 2. orchestrator 执行面

关键文件：

- `tools/ai-orchestrator/orchestrator/daemon/runner.py`
- `tools/ai-orchestrator/orchestrator/runtime/runtime.py`
- `tools/ai-orchestrator/orchestrator/tooling/invoker.py`
- `tools/ai-orchestrator/orchestrator/tooling/toolset_config.py`
- `tools/ai-orchestrator/orchestrator/langgraph/registry.py`
- `tools/ai-orchestrator/orchestrator/langgraph/templates/basic_rca.py`

关键行为：

- runner 启动时注册 template registry
- runner 每 60 秒刷新 template registry
- runner 先 resolve strategy，再选 template + toolsets
- runtime 统一负责 toolcall / observation / retry / lease semantics

---

# 五、最终收口原则

本分支从现在开始进入 **maintenance mode**。

允许的改动：

1. bug fix
2. 测试修复
3. 文档补充
4. E2E 验证脚本
5. 观测字段小修（不改变 schema 核心语义）

不允许的改动：

1. 新 API
2. 新 provider 类型
3. 新 graph 抽象模型
4. pipeline 资源化
5. strategy CRUD
6. remediation 自动修复
7. 灰度 / AB / 流量路由
8. 新模板能力扩展（本分支不做）

---

# 六、必须完成的最终收尾任务

## 任务 1：冻结范围
- 明确 Phase L1 为本分支截止点；
- 不再增加新的 Phase；
- 仅接受稳定性修复。

## 任务 2：跑通 E2E 黄金路径
至少完成一次真实链路验证：

1. 启动 Redis
2. 启动 rca-apiserver
3. 注册 template（L0）
4. 配置 strategy（L1）
5. 启动 orchestrator
6. 拉起 job
7. 验证：
   - strategy resolve 成功
   - template builder 选择成功
   - toolsets chain 构造成功
   - `toolset.select` 观测存在
   - `tool.invoke` 观测存在
   - finalize 成功

## 任务 3：保留文档
本分支至少保留以下文档：

- `48_D12_Branch_Closeout_Orchestrator_Strategy_And_Registry.md`
- `49_D13_Orchestrator_Strategy_E2E_Checklist.md`
- `50_D14_Orchestrator_Config_Matrix.md`
- `51_D15_Code_Reading_Map_After_L1.md`

---

# 七、必须保持的核心语义

以下语义不得被后续收尾改动破坏：

## 1. pipeline-only
- 模板与 strategy resolve 的输入都只看 `AIJob.pipeline`
- 不引入额外 job 字段作为选择信号

## 2. ToolInvokerChain
- `first_match`
- 仅 `allow_tools_denied` 才 fallback
- provider 运行异常不 fallback

## 3. fail-fast
以下场景必须在 graph build/invoke 前失败：

- unknown pipeline
- strategy resolve failed
- template_id 未注册
- template_id 本地无 builder
- 空 toolset chain
- toolset config 非法

## 4. lease / ownership
- lease-lost 后不得继续写 toolcall
- runtime 未 start 不得写 toolcall

## 5. Redis 强依赖
- `redis.enabled=false` → rca-apiserver 启动失败
- Redis connect/ping 失败 → 启动失败
- template registry 不允许降级为内存

---

# 八、E2E 必测场景

## 场景 A：最小成功链路
配置：

- pipeline: `basic_rca`
- template_id: `basic_rca`
- toolsets: `["default"]`

验证：

- `GET /v1/orchestrator/strategies/resolve` 返回成功
- orchestrator 正确选中 `basic_rca` builder
- `toolset.select` 包含 `template_id=basic_rca`
- `tool.invoke` 记录 provider / toolset_chain / resolved_from_toolset_id
- job finalize 成功

## 场景 B：toolset chain 路由
配置：

- toolsets: `["logs_only","metrics_only"]`

验证：

- `query_logs` 命中 `logs_only`
- `query_metrics` 命中 `metrics_only`
- `tool.invoke.route_policy == "first_match"`

## 场景 C：template registry 缺失
不注册 template 时：

- `GET /v1/orchestrator/strategies/resolve?pipeline=...`
- 返回 `template not registered`
- orchestrator fail-fast，不进入 graph

## 场景 D：template builder 缺失
平台下发合法已注册 template_id，但 orchestrator 本地无 builder：

- runner fail-fast finalize failed
- 不进入 graph build/invoke

---

# 九、最终回归命令

## Go
```bash
make protoc
go test ./...
```

## Python
```bash
cd tools/ai-orchestrator
python3 -m unittest discover -s tests -p 'test_*.py' -v
```

## 手工 smoke
按：
- `49_D13_Orchestrator_Strategy_E2E_Checklist.md`

---

# 十、当前已知限制（保留在 closeout 中）

1. `strategy.toolsets` 必须为 list，不能是 string
2. template registry 只同步 `template_id/version/instance`，不同步 graph 内容
3. `/v1/orchestrator/toolsets/resolve` 仍保留，仅用于兼容
4. 当前真实模板可能仍主要是 `basic_rca`
5. verification 可以关闭，不作为当前主路径强依赖
6. template registry 是“活跃实例视角”，依赖 TTL 刷新

---

# 十一、下一阶段（不在本分支实现）

本分支结束后，后续能力应在新分支开展，例如：

- 第二个真实模板（验证多模板不是 only-one）
- strategy registry 管理增强
- remediation / playbook / SOP integration
- 灰度与流量路由
- pipeline 资源化（若未来确有必要）

这些都**不在本分支继续做**。

---

# 十二、代码阅读入口

推荐阅读顺序：

1. `tools/ai-orchestrator/orchestrator/daemon/runner.py`
2. `tools/ai-orchestrator/orchestrator/tools_rca_api.py`
3. `internal/apiserver/pkg/orchestratorcfg/strategy_config.go`
4. `internal/apiserver/pkg/orchestratorregistry/template_registry.go`
5. `tools/ai-orchestrator/orchestrator/langgraph/registry.py`
6. `tools/ai-orchestrator/orchestrator/langgraph/templates/basic_rca.py`
7. `tools/ai-orchestrator/orchestrator/tooling/invoker.py`
8. `tools/ai-orchestrator/orchestrator/runtime/runtime.py`

辅助文档：
- `50_D14_Orchestrator_Config_Matrix.md`
- `51_D15_Code_Reading_Map_After_L1.md`

---

# 十三、状态结论

本分支到此为止：

```text
Phase L1 COMPLETE
```

状态：

```text
maintenance mode
```

可合并，可交接，可停止新增架构能力。
