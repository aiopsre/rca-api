# 补充通知不是"再发一条消息"：可信度、引用回复与 Incident 可回看设计

> **系列导读**：这是 AI RCA 八篇系列的第 6 篇。前五篇分别从值班场景价值、主链路设计、控制面与执行面分层、运行时租约、告警治理等角度，讲解了平台如何将"辅助决策"落地为"可运行、可运维、可扩展的平台级系统"。本篇要回答一个更本质的问题：**为什么补充通知不是"再发一条消息"，而是值班体验设计和平台闭环的关键环节？**

补充通知这个术语，乍一听可能让人误以为"只是在初版告警之后，再发一条诊断结果的消息而已"。如果这么理解，那就大错特错了。补充通知的设计哲学是：

> **初版告警负责"第一时间触达"，补充通知负责"把上下文补到正确的对话位置"。**

这个设计背后的工程判断，涉及值班体验、消息渠道、平台页面、可信度表达等多个维度。让我们一步步拆解。

---

## 一、为什么必须分两次发：时间窗口与上下文

### 1.1 值班员的真实困境：一个凌晨三点的决策

凌晨 3 点 15 分 22 秒，手机震动。你从睡梦中惊醒，大脑还没有完全清醒。飞书推送：

```
🔴 [P0] Pod 反复重启 - demo 服务 - prod-cn1 集群
告警名称：K8sPodCrashLoopBackOff
触发时间：2026-04-02 03:15:22
实例：demo-deployment-7b8c9d6e5f-x4y2z
```

**你的第一反应**：
1. **心跳加速**：P0！核心服务！
2. **快速判断**：这个服务影响什么？是否需要立即介入？
3. **动作决策**：是继续睡觉（如果是边缘服务），还是立刻起床（如果是核心服务）？

**这个 8 秒的窗口，决定了整个事件的走向**：

```
T0: 03:15:22 → 告警触发
T1: 03:15:23 → 初版告警触达（1 秒）
T2: 03:15:30 → 你决定：立即介入（8 秒）
```

### 1.2 如果等 22 秒再发：会失去什么？

假设系统设计成"等 AI 分析完再发一条完整消息"：

```
T0: 03:15:22 → 告警触发
T1: 03:15:23 → AIJob 启动（1 秒）
T2: 03:15:45 → AI 完成分析（22 秒）
T3: 03:15:46 → 完整通知触达（1 秒）

完整通知内容：
├─ 告警：Pod 反复重启
├─ 根因：数据库连接池耗尽（置信度 85%）
├─ 证据：4 条
└─ 建议：扩容连接池
```

**失去了什么**？

#### 1.2.1 失去了"立即响应"的窗口

```
场景对比：

设计 A（分两次发）：
  T0: 03:15:22 → 告警
  T1: 03:15:23 → 初版告警 → 你立即决定介入
  T2: 03:15:30 → 你打开电脑
  T3: 03:15:46 → 补充通知 → 你直接基于诊断做决策
  总耗时：24 秒

设计 B（等 22 秒再发）：
  T0: 03:15:22 → 告警
  T1: 03:15:46 → 完整通知 → 你才决定介入
  T2: 03:16:00 → 你打开电脑
  总耗时：38 秒
```

**延迟 14 秒**：对于 P0 故障，14 秒可能意味着：
- 更多用户受影响
- 更多错误日志产生
- 更难定位问题根因

#### 1.2.2 失去了"分级响应"的能力

值班场景中，不是所有问题都需要"立即介入"：

| 告警类型 | 是否需要立即介入 | 期望响应时间 |
|---------|----------------|------------|
| P0 核心服务宕机 | 是 | 0-30 秒 |
| P1 重要服务异常 | 可能 | 1-5 分钟 |
| P2 边缘服务偶发错误 | 否 | 5-30 分钟 |

**如果只发一次消息**：
- 你需要等待 22 秒，才能判断"这个问题是否需要立即介入"
- 对于不需要立即介入的问题（如 P2），22 秒的等待是浪费
- 对于需要立即介入的问题（如 P0），22 秒的等待是致命

**分两次发的优势**：
- 初版告警（1 秒）：告诉你"是否需要立即介入"
- 补充通知（22 秒）：告诉你"如何介入"

#### 1.2.3 失去了"上下文切换"的缓冲

人类的认知模式是：
1. **紧急判断**（0-3 秒）：是否需要立即介入？（快速、直觉）
2. **详细分析**（22 秒+）：问题是什么？如何解决？（慢速、理性）

**如果只发一次消息**：
- 你收到消息时，需要同时做两件事：
  - 紧急判断：是否需要立即介入？
  - 详细分析：问题是什么？如何解决？
- 这两个认知模式会互相干扰，导致决策质量下降

**分两次发的优势**：
- 第一次消息（紧急判断）：你基于直觉快速决策
- 缓冲期（22 秒）：你切换到详细分析模式
- 第二次消息（详细分析）：你基于理性做深度决策

### 1.3 22 秒的分析成本：为什么值得等待？

补充通知的 22 秒延迟，不是"技术慢"，而是"分析需要时间"。让我们拆解这 22 秒都在做什么：

```
T0: 03:15:22 → 告警触发
T1: 03:15:23 → AIJob 启动（1 秒）
   ↓
并行查询阶段（15 秒）：
├─ 查询 K8s API（2 秒）：Pod Events、容器日志
├─ 查询 Prometheus（3 秒）：连接池指标、CPU/内存
├─ 查询 Loki（4 秒）：应用错误日志
├─ 查询 Tempo（6 秒）：链路追踪、错误率
   ↓
证据整理阶段（3 秒）：
├─ 格式化证据（1 秒）
├─ 去重和排序（1 秒）
├─ 计算证据覆盖率（1 秒）
   ↓
诊断生成阶段（3 秒）：
├─ 调用 LLM（2 秒）：生成结构化结论
├─ 计算置信度（1 秒）：评估证据质量
   ↓
T2: 03:15:45 → AI 完成分析（22 秒）
T3: 03:15:46 → 补充通知触达（1 秒）
```

**为什么不能更快**？

#### 1.3.1 查询并行度有限

虽然查询是并行的，但受限于：
- **外部 API 速率限制**：Prometheus、Tempo 等系统有 QPS 限制
- **网络延迟**：跨集群查询需要网络传输时间
- **数据量**：查询 5 分钟内的指标、日志、链路，数据量不小

**示例数据**（一组脱敏演练样本）：
- 最快查询：K8s API，2 秒（数据量小，本地集群）
- 最慢查询：Tempo 链路追踪，6 秒（数据量大，跨集群）
- 平均查询：3-4 秒

#### 1.3.2 证据整理需要时间

查询到原始数据后，需要：
1. **格式化**：把不同系统的数据格式统一（K8s Events vs Prometheus Metrics vs Loki Logs）
2. **去重**：同一问题在不同系统中可能有多条记录（如连接池耗尽在 K8s、Prometheus、Loki 都有体现）
3. **排序**：按时间顺序排列证据，方便值班员理解

**为什么不能省略**？
- 如果不格式化，值班员需要自己理解不同系统的数据格式
- 如果不去重，值班员会看到重复信息，增加认知负担
- 如果不排序，值班员需要自己拼凑时间线

#### 1.3.3 诊断生成需要调用 LLM

调用 LLM 不是"瞬间完成"，需要：
1. **上下文构建**（0.5 秒）：把证据整理成 LLM 可理解的格式
2. **网络传输**（0.5 秒）：发送到 LLM 服务
3. **LLM 推理**（0.8 秒）：生成诊断结论
4. **结果解析**（0.2 秒）：解析 LLM 输出，验证结构化

**为什么不能用规则引擎**？
- 规则引擎可以处理已知问题（如"连接池耗尽"）
- 但无法处理未知问题（如"新出现的错误码"）
- LLM 的优势是：可以基于证据生成合理的假设，即使这个问题之前没遇到过

### 1.4 分两次发的工程判断

**核心判断**：值班场景中的决策，分为两个阶段：

| 阶段 | 时间窗口 | 认知模式 | 核心问题 | 信息需求 |
|-----|---------|---------|---------|---------|
| 紧急判断 | 0-3 秒 | 直觉、快速 | 是否需要立即介入？ | 严重程度、服务重要性 |
| 详细分析 | 22 秒+ | 理性、深度 | 问题是什么？如何解决？ | 根因、证据、建议 |

**分两次发，是为了尊重这两个不同的决策阶段**：

#### 1.4.1 信息密度的设计

**初版告警**（信息密度低）：
```
🔴 [P0] Pod 反复重启
服务：demo
集群：prod-cn1
时间：03:15:22
```
- 只包含最关键的 4 个字段
- 1 秒内可以读完
- 目的：快速判断是否需要介入

**补充通知**（信息密度高）：
```
【AI 补充通知】🔴 [P0] Pod 反复重启

诊断时间：03:15:46
根因：数据库连接池耗尽（置信度 85%）

证据：
1. K8s Pod Events：CrashLoopBackOff
2. Prometheus：连接池使用率 100%
3. Loki：Cannot get connection from pool
4. Tempo：数据库查询 p99 延迟 5s

建议：
├─ 紧急：扩容连接池
├─ 优化：检查慢查询
└─ 监控：观察连接池使用率

查看更多：https://rca.internal/incidents/xxx
```
- 包含诊断、证据、建议、链接等多个字段
- 需要 5-10 秒阅读
- 目的：基于诊断做详细决策

#### 1.4.2 时机设计的权衡

**为什么不 10 秒发补充通知**？
- 10 秒太短：证据可能不完整（如 Loki 日志还没采集到）
- 10 秒太早：值班员可能还没准备好接收详细信息

**为什么不 30 秒发补充通知**？
- 30 秒太长：值班员已经自己开始排查了
- 30 秒太晚：失去了"辅助决策"的价值

**22 秒的依据**：
- 22 秒是查询 + 整理 + 诊断的平均耗时（示例样本）
- 22 秒是值班员从"紧急判断"切换到"详细分析"的合理缓冲期
- 22 秒是"等待"和"及时"的平衡点

#### 1.4.3 失败场景的设计

**如果 AI 分析失败**（如查询超时、LLM 调用失败）：
- 不发送补充通知
- 在 Incident 页面记录失败原因
- 值班员依然可以基于初版告警做决策

**为什么这样设计**？
- 补充通知是"增值服务"，不是"必需品"
- 如果补充通知不可靠（经常失败），反而会降低值班员信任度
- 初版告警始终可用，保证基本的告警触达能力

---

## 二、补充通知的核心设计：可信度与证据

### 2.1 一个完整的补充通知示例

让我们看一个脱敏后的补充通知示例：

```
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
【AI 补充通知】🔴 [P0] Pod 反复重启
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

📌 基础信息
────────────────────────────────────────────
诊断时间：2026-04-02 03:15:46 UTC
AIJob ID：job-abc123def456-20260402-031523
Incident ID：incident-xyz789-20260402-031522
诊断耗时：22.4 秒（03:15:23 → 03:15:46）

🎯 诊断结论
────────────────────────────────────────────
根因分析：数据库连接池耗尽
置信度：85% （★★★★☆）

证据列表（引用回复可用）：
────────────────────────────────────────────
1. K8s Pod Events（03:14-03:15）
   ────────────────────────────────────────
   03:14:30  Created container demo
   03:14:32  Started container demo
   03:14:45  Back-off restarting failed container
   03:14:50  CrashLoopBackOff: back-off 5m0s restarting failed container
   03:15:00  Last State: Terminated (Exit Code: 1)
   ────────────────────────────────────────
   【引用此证据回复】💬

2. Prometheus 指标（03:14-03:15）
   ────────────────────────────────────────
   连接池使用率：100%（200/200）
   活跃连接数：200
   等待连接数：50
   等待时间：30.2 秒（p99）
   ────────────────────────────────────────
   【引用此证据回复】💬

3. Loki 应用日志（03:14-03:15）
   ────────────────────────────────────────
   [ERROR] 03:14:45 Cannot get connection from pool after 30s
   [ERROR] 03:14:46 Cannot get connection from pool after 30s
   [ERROR] 03:14:47 Timeout waiting for connection
   ...（共 10 条）
   ────────────────────────────────────────
   【引用此证据回复】💬

4. Tempo 链路追踪（03:14-03:15）
   ────────────────────────────────────────
   数据库查询延迟：5.2 秒（p99）
   错误率：95%
   失败请求：475/500
   ────────────────────────────────────────
   【引用此证据回复】💬

💡 建议操作
────────────────────────────────────────────
紧急操作：
├─ 扩容数据库连接池（从 200 到 400）
│  命令：kubectl patch deployment demo -p '{"spec":{"template":{"spec":{"containers":[{"name":"demo","env":[{"name":"DB_POOL_SIZE","value":"400"}]}]}}}}'
├─ 重启 Pod（可选，如果扩容后仍然异常）
│  命令：kubectl rollout restart deployment demo
└─ 观察连接池使用率变化（确认是否缓解）

优化操作：
├─ 检查慢查询（可能连接池耗尽的根源）
│  SQL：SELECT * FROM pg_stat_statements ORDER BY mean_time DESC LIMIT 10
├─ 优化连接泄漏（检查代码中是否有未关闭的连接）
└─ 考虑连接池自动扩缩容（基于使用率动态调整）

🔍 更多信息
────────────────────────────────────────────
- 查看完整诊断：https://rca.internal/incidents/incident-xyz789
- 查看所有证据：https://rca.internal/incidents/incident-xyz789/evidence
- 查看 AIJob 详情：https://rca.internal/ai/jobs/job-abc123def456
- 反馈诊断质量：点击链接中的"反馈"按钮

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
```

### 2.2 置信度：不是魔术数字，而是证据覆盖率

#### 2.2.1 置信度 85% 的含义

看到"置信度 85%"，你会怎么理解？

**错误理解**：
- "85% 的概率是这个根因"
- "有 15% 的可能是其他原因"

**正确理解**：
- **证据覆盖率**：4 个维度的证据中，4 个都支持"连接池耗尽"这个结论
- **证据质量**：所有证据的时间范围一致（03:14-03:15），数据完整
- **矛盾证据**：没有发现与这个结论矛盾的证据

#### 2.2.2 置信度的计算逻辑

```python
# Python worker 端（简化版）
def compute_confidence(evidence_list, root_cause_hypothesis):
    """
    计算诊断置信度
    
    参数:
        evidence_list: List[Evidence]  证据列表
        root_cause_hypothesis: str    根因假设（如 "database_connection_pool_exhausted"）
    
    返回:
        float  置信度（0-1）
    """
    
    # 1. 证据覆盖率：支持结论的证据数量 / 总证据数量
    supporting_count = sum(
        1 for e in evidence_list 
        if e.supports_hypothesis(root_cause_hypothesis)
    )
    total_count = len(evidence_list)
    coverage_score = supporting_count / total_count if total_count > 0 else 0
    
    # 2. 矛盾证据惩罚：每发现一个矛盾证据，扣分
    contradicting_count = sum(
        1 for e in evidence_list 
        if e.contradicts_hypothesis(root_cause_hypothesis)
    )
    contradiction_penalty = 0.2 * contradicting_count
    
    # 3. 证据质量加分：时间范围一致、数据完整、查询成功
    quality_score = compute_evidence_quality(evidence_list)
    # quality_score 范围：0-0.1
    
    # 4. 综合置信度
    confidence = min(1.0, max(0.0, 
        coverage_score - contradiction_penalty + quality_score
    ))
    
    return round(confidence, 2)
```

**证据质量的计算**：

```python
def compute_evidence_quality(evidence_list):
    """
    计算证据质量（0-0.1）
    """
    score = 0.0
    
    # 时间范围一致：所有证据的时间范围重叠度 > 90%
    if is_time_range_consistent(evidence_list):
        score += 0.04
    
    # 数据完整性：所有查询都成功，没有超时或失败
    if all(e.query_success for e in evidence_list):
        score += 0.03
    
    # 证据多样性：覆盖多个维度（K8s + Prometheus + Loki + Tempo）
    if len(set(e.type for e in evidence_list)) >= 3:
        score += 0.03
    
    return min(0.1, score)
```

#### 2.2.3 置信度的决策阈值

**我们的实践**：

| 置信度 | 含义 | 值班员建议 |
|-------|------|-----------|
| ≥ 90% | 高可信度 | 可以直接按照建议操作，无需人工复核 |
| 70-89% | 中等可信度 | 建议快速复核证据，确认后再操作 |
| 50-69% | 低可信度 | 必须人工复核，不能直接按照建议操作 |
| < 50% | 不可信 | 诊断仅供参考，建议人工排查 |

**为什么设置 70% 作为中/低分界线**？

- **样本观察**：在一组脱敏演练样本中，置信度 ≥ 70% 的诊断，准确率约为 85%
- **误判成本**：如果置信度 70% 但诊断错误，可能导致错误操作（如错误扩容）
- **复核成本**：复核一次诊断平均需要 30 秒

**权衡**：
- 如果阈值设为 80%：减少误判，但增加复核次数（更多诊断需要复核）
- 如果阈值设为 60%：减少复核，但增加误判风险（更多诊断直接采纳）

**70% 是平衡点**：
- 85% 的准确率足够高（100 次诊断，约 85 次正确）
- 复核成本可控（约 30% 的诊断需要复核）

#### 2.2.4 置信度的局限性

**置信度不是 100% 可信**：

1. **证据缺失**：如果某些关键证据缺失（如查询超时），置信度可能偏高
2. **证据偏差**：如果所有证据都来自同一系统（如只有 Prometheus），置信度可能偏高
3. **新问题**：对于之前没遇到过的问题，LLM 可能生成"合理但错误"的诊断

**值班员如何判断**：

- **看证据数量**：证据越多，置信度越可靠（4 条证据比 2 条可靠）
- **看证据来源**：证据来源越多，置信度越可靠（4 个系统比 1 个可靠）
- **看证据时间范围**：时间范围越一致，置信度越可靠（都是 03:14-03:15 比分散的时间可靠）

### 2.3 证据列表：可引用、可回复的上下文锚点

#### 2.3.1 为什么证据要可引用？

想象这个场景：

```
值班员 A：收到补充通知，觉得 "Loki 日志"这条证据可能有问题
值班员 A：@机器人 回复 Loki 日志：这条日志的时间范围只有 1 分钟，可能不完整
机器人：记录回复，更新 Incident 的评论列表
值班员 B：看到回复，决定补充查询更长时间范围的日志
值班员 B：在 Incident 页面手动添加新的证据
```

**如果没有"引用回复"**：
- 值班员 A 需要"复制证据内容，再发一条消息"，说"我觉得这个证据有问题"
- 值班员 B 需要"找到值班员 A 说的证据"，很麻烦
- 讨论容易丢失上下文（"你说的是哪条证据？"）

**有了"引用回复"**：
- 直接在证据下方回复，上下文一目了然
- 所有讨论都锚定在证据上，不会丢失
- 值班员可以看到"谁质疑了这条证据，为什么质疑"

#### 2.3.2 证据的引用格式

**每个证据都有唯一的 Evidence ID**：

```
Evidence ID: evidence-12345-20260402-031430
类型: k8s_pod_events
查询: kubectl describe pod demo-deployment-7b8c9d6e5f-x4y2z --namespace demo
时间范围: 03:14-03:15
结果: CrashLoopBackOff: back-off 5m0s restarting failed container
```

**引用回复的格式**：

```
@机器人 回复 evidence-12345-20260402-031430: 这条证据的时间范围太短，建议查询 03:10-03:15
```

**为什么用 Evidence ID 而不是序号**？
- 序号会变：如果新增证据，序号会重新排列
- ID 不变：Evidence ID 是永久的，即使新增证据也不会变

#### 2.3.3 证据的协作价值

**协作场景 1：质疑证据**

```
值班员 A: @机器人 回复 evidence-12345: 这条 K8s Events 可能不准确，因为 Pod 刚重启过
值班员 B: 看到回复，补充查询更早的 Events
值班员 B: 在 Incident 页面添加新的证据：evidence-67890（03:10-03:14 的 Events）
```

**协作场景 2：确认证据**

```
值班员 A: @机器人 回复 evidence-23456: 这条 Prometheus 指标确认了，连接池确实 100%
值班员 B: 看到确认，决定直接按照建议操作
```

**协作场景 3：补充证据**

```
值班员 A: @机器人 回复 evidence-34567: 这条 Loki 日志只显示了错误，建议补充查询正常日志
机器人: 自动查询正常日志，添加新证据：evidence-78901
```

### 2.4 Incident 深度链接：消息触达，页面沉淀

#### 2.4.1 为什么需要深度链接？

**消息渠道的局限性**：

1. **消息可能被刷屏**：几分钟后，补充通知就被其他消息淹没了
2. **消息没有结构化展示**：所有信息都在一条消息里，难以查看完整诊断
3. **消息不支持多轮交互**：无法添加新证据、更新根因、标记解决状态

**平台页面的优势**：

1. **永久存储**：Incident 页面永远可访问，不会被刷屏
2. **结构化展示**：
   ```
   Incident 页面
   ├─ 基本信息（服务、集群、严重程度）
   ├─ 诊断结论（根因、置信度）
   ├─ 证据列表（可展开、可排序）
   ├─ 时间线（告警、诊断、操作）
   ├─ 评论（值班员的讨论）
   └─ 操作历史（谁做了什么）
   ```
3. **多轮交互**：
   - 添加新证据
   - 更新根因
   - 标记解决状态
   - 导出报告

#### 2.4.2 深度链接的设计

**补充通知末尾的链接**：

```
查看更多：https://rca.internal/incidents/incident-xyz789
```

**点击链接后的页面**：

```
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
Incident #incident-xyz789
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

📌 基本信息
────────────────────────────────────────────
服务：demo
集群：prod-cn1
严重程度：P0
状态：处理中
创建时间：2026-04-02 03:15:22
诊断时间：2026-04-02 03:15:46

🎯 诊断结论
────────────────────────────────────────────
根因：数据库连接池耗尽
置信度：85%

📋 证据列表
────────────────────────────────────────────
[1] K8s Pod Events（03:14-03:15）
    Click to expand →
[2] Prometheus 指标（03:14-03:15）
    Click to expand →
[3] Loki 应用日志（03:14-03:15）
    Click to expand →
[4] Tempo 链路追踪（03:14-03:15）
    Click to expand →

💬 评论
────────────────────────────────────────────
值班员 A (03:16:00): Loki 日志的时间范围可能不完整
值班员 B (03:16:30): 已补充查询 03:10-03:15 的日志，确认连接池耗尽
值班员 A (03:17:00): 同意，按照建议扩容连接池

⏱️ 时间线
────────────────────────────────────────────
03:15:22  告警触发
03:15:23  AIJob 启动
03:15:46  诊断完成
03:16:00  值班员 A 查看诊断
03:16:30  值班员 B 补充证据
03:17:00  值班员 A 扩容连接池

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
```

#### 2.4.3 "消息触达，页面沉淀"的设计哲学

**设计原则**：

> **消息只负责触达，平台页面负责沉淀和复看。**

**为什么这样设计**？

1. **触达速度**：消息渠道（飞书/Slack）的触达速度比打开网页快
   - 消息：1 秒内触达
   - 网页：需要点击链接，加载页面，约 3-5 秒

2. **上下文保留**：消息渠道适合快速查看，网页适合深度查看
   - 消息：值班员在手机上快速查看
   - 网页：值班员在电脑上深度查看

3. **协作效率**：消息渠道适合即时沟通，网页适合持久记录
   - 消息：值班员之间的即时讨论
   - 网页：所有讨论的持久记录

**实际场景**：

```
场景：值班员在睡觉，手机收到补充通知

1. 手机查看补充通知（1 秒）
   → 判断：需要立即介入
   
2. 起床，打开电脑（30 秒）
   → 点击深度链接，打开 Incident 页面
   
3. 深度查看诊断（1 分钟）
   → 查看证据、评论、时间线
   
4. 操作和协作（5 分钟）
   → 扩容连接池、添加评论、标记解决
```

---

## 三、`diagnosis_written` 事件：异步 outbox 模式

### 3.1 为什么需要异步 outbox？

#### 3.1.1 同步发送的问题

假设设计成"同步发送"：

```go
// 同步发送（不推荐）
func Finalize(ctx context.Context, rq *v1.FinalizeAIJobRequest) error {
    // 1. 验证状态转移
    // 2. 验证并规范化 diagnosis JSON
    // 3. 回写 diagnosis_json 到 Incident
    // 4. 清租约
    // 5. 同步发送补充通知 ← 问题在这里
    err := sendNoticeSync(ctx, noticeReq)
    if err != nil {
        return err // 如果发送失败，整个 Finalize 失败
    }
    return nil
}
```

**问题 1：依赖外部系统可用性**

```
场景：webhook endpoint 不可用（如网络故障、服务宕机）

T0: 03:15:45 → Finalize 执行
T1: 03:15:46 → 发送 webhook（超时 30 秒）
T2: 03:16:16 → webhook 超时，返回错误
T3: 03:16:16 → Finalize 失败，事务回滚
结果：诊断没有回写，Incident 状态异常
```

**问题 2：阻塞控制面**

```
场景：同时有 100 个 AIJob 完成，需要发送 100 条补充通知

如果同步发送：
- apiserver 需要等待 100 个 webhook 响应
- apiserver 线程被阻塞，无法处理其他请求
- apiserver 可能 OOM（内存溢出）

如果异步 outbox：
- apiserver 写入 100 条 outbox 记录（毫秒级）
- apiserver 立即返回，继续处理其他请求
- notice-worker 异步投递 100 条 webhook
```

**问题 3：无法重试**

```
场景：webhook endpoint 返回 500 错误

如果同步发送：
- Finalize 失败
- 无法重试（因为事务已经回滚）
- 诊断丢失

如果异步 outbox：
- outbox 记录写入成功
- notice-worker 重试投递（指数退避）
- 最终成功
```

#### 3.1.2 异步 outbox 的优势

**设计**：

```go
// 异步 outbox（推荐）
func Finalize(ctx context.Context, rq *v1.FinalizeAIJobRequest) error {
    // 1. 验证状态转移
    // 2. 验证并规范化 diagnosis JSON
    // 3. 回写 diagnosis_json 到 Incident
    // 4. 清租约
    // 5. 写入 outbox（不等待外部系统）
    noticepkg.DispatchBestEffort(ctx, st, noticeReq)
    return nil
}
```

**优势 1：解耦**

```
控制面（apiserver）：
- 只负责写入 outbox
- 不依赖外部系统可用性
- 不阻塞在 webhook 上

执行面（notice-worker）：
- 负责投递 outbox
- 可以重试、限流、监控
- 独立扩缩容
```

**优势 2：可靠性**

```
场景：webhook endpoint 不可用

T0: 03:15:45 → Finalize 写入 outbox（成功）
T1: 03:15:46 → Finalize 返回成功
T2: 03:15:47 → notice-worker 尝试投递（失败）
T3: 03:15:48 → notice-worker 重试（1 秒后）
T4: 03:15:50 → notice-worker 重试（2 秒后）
T5: 03:15:54 → notice-worker 重试（4 秒后）
T6: 03:16:02 → webhook 恢复，投递成功
结果：诊断回写成功，补充通知最终送达
```

**优势 3：可观测性**

```
所有投递记录都保存在 notice_deliveries 表：

delivery_id | channel_id | event_type | status | attempts | error_text
------------|------------|------------|--------|----------|-----------
del-123     | ch-456     | diag_writ  | pending| 0        | NULL
del-456     | ch-789     | diag_writ  | failed | 3        | timeout
del-789     | ch-012     | diag_writ  | succ   | 1        | NULL

可以查询：
- 哪些补充通知失败了？
- 失败的原因是什么？
- 重试了多少次？
```

### 3.2 `diagnosis_written` 事件触发

#### 3.2.1 触发时机

```go
// internal/apiserver/biz/v1/ai_job/ai_job.go:705-711
noticeReq = &noticepkg.DispatchRequest{
    EventType:           noticepkg.EventTypeDiagnosisWritten,
    JobID:               jobID,
    DiagnosisConfidence: diagnosisConfidence,
    DiagnosisEvidenceID: append([]string(nil), evidenceIDs...),
    OccurredAt:          now,
}
```

**触发时机**：`Finalize` 事务成功提交后

**为什么在这个时机**？

1. **诊断已经回写**：确保补充通知发送时，诊断已经持久化
2. **租约已清理**：确保没有租约冲突
3. **状态已更新**：确保 Incident 状态是最新的

#### 3.2.2 事件携带的信息

| 字段 | 含义 | 示例 |
|-----|------|------|
| `EventType` | 事件类型 | `diagnosis_written` |
| `JobID` | AIJob ID | `job-abc123def456` |
| `DiagnosisConfidence` | 诊断置信度 | `0.85` |
| `DiagnosisEvidenceID` | 证据 ID 列表 | `["evidence-1", "evidence-2"]` |
| `OccurredAt` | 事件发生时间 | `2026-04-02 03:15:46` |

**为什么携带这些信息**？

1. **`EventType`**：区分不同类型的事件（如 `incident_created`、`diagnosis_written`）
2. **`JobID`**：关联到具体的 AIJob，方便追溯
3. **`DiagnosisConfidence`**：用于筛选（如只发送置信度 ≥ 70% 的通知）
4. **`DiagnosisEvidenceID`**：用于构建证据列表
5. **`OccurredAt`**：用于生成 idempotency key，避免重复投递

### 3.3 DispatchBestEffort：写入 outbox

#### 3.3.1 函数实现

```go
// internal/apiserver/pkg/notice/dispatch.go:49-65
func DispatchBestEffort(ctx context.Context, st store.IStore, rq DispatchRequest) {
    // 1. 准备投递计划（选择 channels）
    plan, ok := prepareDispatchPlan(ctx, st, rq)
    if !ok {
        return
    }
    // 2. 为每个 channel 写入 outbox（status=pending）
    for _, channel := range plan.channels {
        enqueueDeliveryForChannel(ctx, st, plan, rq, channel)
    }
}
```

**关键点**：

1. **BestEffort**：不返回错误，即使失败也不影响调用方
2. **异步**：写入数据库，不等待外部系统响应
3. **批量**：为每个匹配的 channel 写入一条 outbox 记录

#### 3.3.2 准备投递计划

```go
// internal/apiserver/pkg/notice/dispatch.go:167-198
func prepareDispatchPlan(ctx context.Context, st store.IStore, rq DispatchRequest) (*dispatchPlan, bool) {
    // 1. 查询所有启用的 webhook channel
    channels, err := st.NoticeChannel().ListEnabledWebhook(ctx)
    if err != nil {
        slog.ErrorContext(ctx, "notice list channels failed", "error", err)
        return nil, false
    }
    if len(channels) == 0 {
        return nil, false
    }
    
    // 2. 根据 selector 匹配 channel
    eventCtx := buildEventContext(eventType, rq.Incident)
    matchedChannels := selectMatchedChannels(channels, eventCtx)
    if len(matchedChannels) == 0 {
        return nil, false
    }
    
    return &dispatchPlan{
        channels:   matchedChannels,
        eventType:  eventType,
        occurredAt: occurredAt,
    }, true
}
```

**Selector 匹配**：

```go
func selectMatchedChannels(channels []*model.NoticeChannelM, ctx eventContext) []*model.NoticeChannelM {
    out := make([]*model.NoticeChannelM, 0, len(channels))
    for _, ch := range channels {
        if !matchChannelSelectors(ch.SelectorsJSON, ctx) {
            continue
        }
        out = append(out, ch)
    }
    return out
}

func matchChannelSelectors(raw *string, ctx eventContext) bool {
    selectors := decodeSelectors(raw)
    if selectors == nil {
        return true // 无 selector，匹配所有
    }
    
    // 逐个维度匹配
    if !matchSelectorDimension(selectors.EventTypes, ctx.EventType) {
        return false
    }
    if !matchSelectorDimension(selectors.Namespaces, ctx.Namespace) {
        return false
    }
    if !matchSelectorDimension(selectors.Services, ctx.Service) {
        return false
    }
    if !matchSelectorDimension(selectors.Severities, ctx.Severity) {
        return false
    }
    if !matchSelectorDimension(selectors.RootCauseTypes, ctx.RootCauseType) {
        return false
    }
    return true
}
```

**示例**：

```json
// Channel 配置
{
  "selectors": {
    "event_types": ["diagnosis_written"],
    "namespaces": ["prod"],
    "services": ["demo", "api"],
    "severities": ["critical", "warning"],
    "root_cause_types": ["database_connection_pool_exhausted"]
  }
}

// 匹配场景
Incident: namespace=prod, service=demo, severity=critical, root_cause=database_connection_pool_exhausted
Result: 匹配（所有维度都符合）

Incident: namespace=staging, service=demo, severity=critical, root_cause=database_connection_pool_exhausted
Result: 不匹配（namespace 不符合）
```

#### 3.3.3 写入 outbox

```go
func enqueueDeliveryForChannel(ctx context.Context, st store.IStore, plan *dispatchPlan, rq DispatchRequest, channel *model.NoticeChannelM) {
    // 1. 构建 payload
    payloadRaw, err := buildPayloadForChannel(rq, channel)
    if err != nil {
        slog.ErrorContext(ctx, "notice payload build failed", "error", err)
        return
    }
    
    // 2. 构建 snapshot
    snapshot, err := BuildDeliverySnapshotFromChannel(channel)
    if err != nil {
        slog.WarnContext(ctx, "notice delivery snapshot build failed", "error", err)
        return
    }
    
    // 3. 写入 notice_deliveries 表
    delivery := &model.NoticeDeliveryM{
        ChannelID:                 channel.ChannelID,
        EventType:                 plan.eventType,
        IncidentID:                strPtrOrNil(rq.Incident.IncidentID),
        JobID:                     strPtrOrNil(rq.JobID),
        RequestBody:               truncateString(string(payloadRaw), RequestBodyMaxBytes),
        Status:                    DeliveryStatusPending,
        Attempts:                  0,
        MaxAttempts:               deriveMaxAttempts(channel.MaxRetries),
        NextRetryAt:               time.Now().UTC(),
        IdempotencyKey:            newDeliveryIdempotencyKey(channel.ChannelID, plan.eventType, rq.Incident.IncidentID, rq.JobID, plan.occurredAt),
        SnapshotEndpointURL:       snapshot.EndpointURL,
        SnapshotTimeoutMs:         snapshot.TimeoutMs,
        SnapshotHeadersJSON:       encodeSnapshotHeaders(snapshot.Headers),
        SnapshotSecretFingerprint: snapshot.SecretFingerprint,
        SnapshotChannelVersion:    snapshot.ChannelVersion,
    }
    if err := st.NoticeDelivery().Create(ctx, delivery); err != nil {
        slog.ErrorContext(ctx, "notice delivery enqueue failed", "error", err)
        return
    }
    
    // 4. 触发 Redis Stream 信号
    PublishNoticeDeliverySignalBestEffort(ctx, delivery.DeliveryID)
    
    // 5. 重建 payload（包含 delivery_id）
    rebuildDeliveryPayloadWithID(ctx, st, rq, channel, delivery)
}
```

**Snapshot 的作用**：

```
场景：channel 配置变更（如 endpoint URL 变更）

如果没有 snapshot：
- notice-worker 投递时，读取最新的 channel 配置
- 如果 endpoint 已经变更，投递到错误的地址
- 原本应该投递到旧地址的消息，投递失败

如果有 snapshot：
- 写入 outbox 时，保存当时的 channel 配置快照
- notice-worker 投递时，使用 snapshot，不读取最新配置
- 即使 endpoint 变更，也能投递到正确的地址
```

**Idempotency Key 的作用**：

```go
func newDeliveryIdempotencyKey(channelID string, eventType string, incidentID string, jobID string, occurredAt time.Time) string {
    base := strings.Join([]string{
        channelID,
        strings.ToLower(eventType),
        incidentID,
        jobID,
        occurredAt.UTC().Format(time.RFC3339Nano),
    }, "|")
    sum := sha256.Sum256([]byte(base))
    return "notice-" + hex.EncodeToString(sum[:16])
}
```

```
场景：重复投递

假设由于网络抖动，DispatchBestEffort 被调用两次

第一次：
  idempotency_key = notice-abc123...

第二次（相同参数）：
  idempotency_key = notice-abc123...（相同）

notice-worker 投递时：
  检查 idempotency_key 是否已经处理过
  如果已经处理过，跳过（避免重复投递）
```

### 3.4 Worker 投递：带重试的可靠投递

#### 3.4.1 Worker 架构

```
Notice Worker
├─ Run()：主循环，每隔 1 秒执行一次
│  ├─ runOnceFromStream()：从 Redis Stream 消费（优先）
│  │  └─ TryClaimByDeliveryID()：尝试认领 delivery
│  └─ runOnceFromDB()：从 DB 回退消费（Stream 不可用时）
│     └─ ClaimPending()：批量认领 pending 的 delivery
│
├─ processDelivery()：处理单个 delivery
│  ├─ resolveSendConfig()：解析发送配置
│  ├─ acquireRateLimitPermit()：获取速率限制许可
│  ├─ sendWebhook()：发送 webhook
│  └─ 根据结果更新状态
│     ├─ MarkSucceeded()：成功
│     ├─ MarkRetry()：重试
│     └─ MarkFailed()：失败
│
└─ computeRetryDelay()：计算重试延迟
   └─ 指数退避：1 秒、2 秒、4 秒、8 秒...最大 60 秒
```

#### 3.4.2 Stream 优先，DB 回退

**为什么需要两种消费方式**？

```
Redis Stream（优先）：
- 优点：实时性强，延迟低（毫秒级）
- 缺点：依赖 Redis 可用性

DB 轮询（回退）：
- 优点：不依赖 Redis，可靠性高
- 缺点：延迟高（1 秒轮询间隔）
```

**实际场景**：

```
场景 1：Redis 可用
  → DispatchBestEffort 写入 outbox + 触发 Stream 信号
  → Worker 从 Stream 消费，延迟 < 100 毫秒
  → 补充通知几乎实时送达

场景 2：Redis 不可用
  → DispatchBestEffort 写入 outbox（Stream 信号失败）
  → Worker 从 DB 轮询，延迟 1 秒
  → 补充通知延迟 1 秒送达

场景 3：Worker 重启
  → Stream 中的消息丢失
  → Worker 从 DB 轮询，消费 pending 的 delivery
  → 补充通知最终送达
```

#### 3.4.3 速率限制

**为什么需要速率限制**？

```
场景：100 个 AIJob 同时完成，需要发送 100 条补充通知

如果没有速率限制：
- Worker 同时发送 100 个 webhook
- webhook endpoint 可能被打爆（如 QPS 限制 10）
- webhook endpoint 返回 429，投递失败

如果有速率限制：
- Worker 限制 QPS（如 10）
- 每 100 毫秒发送 1 个 webhook
- webhook endpoint 能够处理，投递成功
```

**实现**：

```go
func (w *Worker) acquireRateLimitPermit(ctx context.Context, delivery *model.NoticeDeliveryM) (RateLimitPermit, error) {
    if w.rateLimiter == nil {
        return RateLimitPermit{}, nil
    }
    
    deadline := time.Now().UTC().Add(w.opts.LockTimeout)
    for {
        decision, err := w.rateLimiter.Acquire(ctx, delivery.ChannelID)
        if err != nil {
            return RateLimitPermit{}, err
        }
        if decision.Allowed {
            return decision.Permit, nil
        }
        
        // 等待 retry_after
        retryAfter, reason := normalizeRateLimitRetry(decision)
        if time.Now().UTC().Add(retryAfter).After(deadline) {
            return RateLimitPermit{}, fmt.Errorf("rate limit denied: %s", reason)
        }
        
        time.Sleep(retryAfter)
    }
}
```

**Channel 级别限流**：

```
每个 channel 有自己的速率限制：

channel-1（值班群）：10 QPS
channel-2（服务群）：5 QPS
channel-3（测试群）：1 QPS

好处：
- 值班群优先（高 QPS）
- 服务群次之（中等 QPS）
- 测试群最低（低 QPS）
```

#### 3.4.4 重试策略

**重试条件**：

```go
func isRetryable(sendErr error, code *int32) bool {
    if sendErr != nil {
        return true // 网络错误、超时，重试
    }
    if code == nil {
        return true // 无响应码，重试
    }
    httpCode := *code
    if httpCode == 429 {
        return true // 限流，重试
    }
    if httpCode >= 500 {
        return true // 服务器错误，重试
    }
    if httpCode >= 400 {
        return false // 客户端错误，不重试
    }
    return false
}
```

**重试延迟计算**：

```go
func computeRetryDelay(attempt int64, base time.Duration, capDelay time.Duration, jitterMax time.Duration) time.Duration {
    if attempt <= 0 {
        attempt = 1
    }
    
    // 指数退避
    delay := base
    for i := int64(1); i < attempt; i++ {
        delay *= 2
        if delay >= capDelay {
            delay = capDelay
            break
        }
    }
    
    // 随机抖动（避免多个 delivery 同时重试）
    jitter := time.Duration(randomNanos(jitterMax.Nanoseconds()))
    return delay + jitter
}
```

**示例**：

```
假设：base=1 秒，cap=60 秒，jitter=200 毫秒

第 1 次失败：
  delay = 1 秒 + 随机抖动（0-200 毫秒）
  重试时间：1.0-1.2 秒后

第 2 次失败：
  delay = 2 秒 + 随机抖动（0-200 毫秒）
  重试时间：2.0-2.2 秒后

第 3 次失败：
  delay = 4 秒 + 随机抖动（0-200 毫秒）
  重试时间：4.0-4.2 秒后

第 4 次失败：
  delay = 8 秒 + 随机抖动（0-200 毫秒）
  重试时间：8.0-8.2 秒后

...
  
第 6 次失败：
  delay = 60 秒（达到上限）
  重试时间：60.0-60.2 秒后
```

**为什么指数退避**？

1. **减少对 webhook endpoint 的压力**：失败越多次，重试间隔越长
2. **给 webhook endpoint 恢复时间**：如果是临时故障，间隔越长，恢复概率越高
3. **避免雪崩效应**：如果所有失败的 delivery 同时重试，可能导致 webhook endpoint 再次崩溃

**为什么随机抖动**？

```
场景：100 个 delivery 同时失败，第 1 次重试

如果没有抖动：
  100 个 delivery 同时重试（1 秒后）
  webhook endpoint 可能被打爆

如果有抖动：
  100 个 delivery 分散重试（1.0-1.2 秒）
  webhook endpoint 负载均衡
```

#### 3.4.5 投递结果处理

**成功**：

```go
if sendErr == nil && code != nil && *code >= 200 && *code < 300 {
    if err := w.store.NoticeDelivery().MarkSucceeded(ctx, delivery.DeliveryID, w.opts.WorkerID, code, responseBodyPtr, latencyMs); err != nil {
        return w.handleMarkClaimLost(ctx, delivery, err)
    }
    outcome = DeliveryStatusSucceeded
}
```

**重试**：

```go
case retryable && attemptNow < maxAttempts:
    nextRetryAt := time.Now().UTC().Add(computeRetryDelay(attemptNow, w.opts.BaseBackoff, w.opts.CapBackoff, w.opts.JitterMax))
    if err := w.store.NoticeDelivery().MarkRetry(ctx, delivery.DeliveryID, w.opts.WorkerID, code, responseBodyPtr, errText, latencyMs, nextRetryAt); err != nil {
        return w.handleMarkClaimLost(ctx, delivery, err)
    }
    outcome = "retry"
```

**失败**：

```go
default:
    if err := w.store.NoticeDelivery().MarkFailed(ctx, delivery.DeliveryID, w.opts.WorkerID, code, responseBodyPtr, errText, latencyMs); err != nil {
        return w.handleMarkClaimLost(ctx, delivery, err)
    }
    outcome = DeliveryStatusFailed
```

**Claim Lost 处理**：

```go
func (w *Worker) handleMarkClaimLost(ctx context.Context, delivery *model.NoticeDeliveryM, err error) error {
    if !errors.Is(err, gorm.ErrRecordNotFound) {
        return err
    }
    
    // Claim Lost：其他 Worker 已经认领了这个 delivery
    slog.WarnContext(ctx, "notice delivery claim lost before status update",
        "worker_id", w.opts.WorkerID,
        "delivery_id", delivery.DeliveryID,
    )
    return nil
}
```

```
场景：多实例 Worker

Worker A：认领 delivery-123
Worker A：发送 webhook（耗时 10 秒）
Worker B：认领 delivery-123（因为 Worker A 的锁超时）
Worker B：发送 webhook（成功）
Worker B：更新 status 为 succeeded
Worker A：尝试更新 status（失败，记录不存在）
Worker A：记录 "claim lost"，不报错
```

### 3.5 Payload 组装：结构化数据

#### 3.5.1 Payload 模板配置

```go
func buildPayloadTemplateConfig(channel *model.NoticeChannelM) payloadTemplateConfig {
    if channel == nil {
        return payloadTemplateConfig{mode: NoticePayloadModeCompact}
    }
    return payloadTemplateConfig{
        mode:               normalizePayloadMode(channel.PayloadMode),
        includeDiagnosis:   channel.IncludeDiagnosis,
        includeEvidenceIDs: channel.IncludeEvidenceIDs,
        includeRootCause:   channel.IncludeRootCause,
        includeLinks:       channel.IncludeLinks,
    }
}
```

**两种模式**：

| 字段 | Compact 模式 | Full 模式 |
|-----|-------------|----------|
| `root_cause_summary` | ✅ | ✅ |
| `diagnosis_min.confidence` | ✅ | ❌ |
| `diagnosis_min.root_cause` | ✅ | ❌ |
| `diagnosis_min.missing_evidence` | ✅ | ❌ |
| `root_cause` | ❌ | ✅ |
| `evidence_ids` | ❌ | ✅ |
| `diagnosis.confidence` | ❌ | ✅ |
| `diagnosis.root_cause` | ❌ | ✅ |
| `diagnosis.evidence_ids` | ❌ | ✅ |
| `diagnosis.missing_evidence` | ❌ | ✅ |
| `links` | ❌ | ✅ |

**为什么有两种模式**？

```
场景 1：值班群（Compact 模式）
  - 消息简洁，只包含核心信息（根因、置信度）
  - 避免消息过长，影响阅读
  - 适合手机查看

场景 2：服务群（Full 模式）
  - 消息详细，包含所有信息（根因、置信度、证据、链接）
  - 适合深度查看
  - 适合电脑查看
```

#### 3.5.2 Payload 组装逻辑

```go
func applyPayloadTemplate(
    payload map[string]any,
    rq DispatchRequest,
    template payloadTemplateConfig,
    diagnosis diagnosisSnapshot,
) {
    switch template.mode {
    case NoticePayloadModeFull:
        applyFullPayloadTemplate(payload, rq, template, diagnosis)
    default:
        applyCompactPayloadTemplate(payload, template, diagnosis)
    }
}
```

**Compact 模式**：

```go
func applyCompactPayloadTemplate(payload map[string]any, template payloadTemplateConfig, diagnosis diagnosisSnapshot) {
    if template.includeRootCause && diagnosis.rootCauseSummary != "" {
        payload["root_cause_summary"] = diagnosis.rootCauseSummary
    }
    if !template.includeDiagnosis {
        return
    }
    
    diagnosisMin := map[string]any{
        "confidence": diagnosis.confidence,
    }
    rootCause := compactRootCause(diagnosis)
    if len(rootCause) > 0 {
        diagnosisMin["root_cause"] = rootCause
    }
    if len(diagnosis.missingEvidence) > 0 {
        diagnosisMin["missing_evidence"] = diagnosis.missingEvidence
    }
    payload["diagnosis_min"] = diagnosisMin
}
```

**Full 模式**：

```go
func applyFullPayloadTemplate(payload map[string]any, rq DispatchRequest, template payloadTemplateConfig, diagnosis diagnosisSnapshot) {
    if template.includeRootCause {
        rootCause := compactRootCause(diagnosis)
        if len(rootCause) > 0 {
            payload["root_cause"] = rootCause
        }
    }
    if template.includeEvidenceIDs {
        payload["evidence_ids"] = diagnosis.evidenceIDs
    }
    if !template.includeDiagnosis {
        return
    }
    
    diagnosisPayload := map[string]any{
        "confidence": diagnosis.confidence,
    }
    rootCause := compactRootCause(diagnosis)
    if len(rootCause) > 0 {
        diagnosisPayload["root_cause"] = rootCause
    }
    if len(diagnosis.evidenceIDs) > 0 {
        diagnosisPayload["evidence_ids"] = diagnosis.evidenceIDs
    }
    if len(diagnosis.missingEvidence) > 0 {
        diagnosisPayload["missing_evidence"] = diagnosis.missingEvidence
    }
    payload["diagnosis"] = diagnosisPayload
    
    if strings.EqualFold(rq.EventType, EventTypeDiagnosisWritten) {
        jobID := strings.TrimSpace(rq.JobID)
        if jobID != "" {
            payload["job"] = map[string]any{
                "job_id": jobID,
            }
        }
    }
}
```

#### 3.5.3 Links 的构建

```go
func buildPayloadLinks(
    rq DispatchRequest,
    channel *model.NoticeChannelM,
    metadata payloadRenderMetadata,
) (map[string]any, bool) {
    baseURL := resolvePayloadLinksBaseURL(channel)
    if baseURL == "" {
        return nil, true // 链接被省略
    }
    
    links := map[string]any{
        "version":  "v1",
        "base_url": baseURL,
    }
    
    incidentID := strings.TrimSpace(rq.Incident.IncidentID)
    if incidentID != "" {
        pathIncidentID := url.PathEscape(incidentID)
        links["incident_url"] = joinPayloadLinksURL(baseURL, "/v1/incidents/"+pathIncidentID)
        links["evidence_list_url"] = joinPayloadLinksURL(baseURL, "/v1/incidents/"+pathIncidentID+"/evidence")
    }
    if deliveryID := strings.TrimSpace(metadata.deliveryID); deliveryID != "" {
        links["delivery_url"] = joinPayloadLinksURL(baseURL, "/v1/notice-deliveries/"+url.PathEscape(deliveryID))
    }
    if channelID := strings.TrimSpace(channel.ChannelID); channelID != "" {
        links["channel_url"] = joinPayloadLinksURL(baseURL, "/v1/notice-channels/"+url.PathEscape(channelID))
    }
    if jobID := strings.TrimSpace(rq.JobID); jobID != "" {
        links["job_url"] = joinPayloadLinksURL(baseURL, "/v1/ai/jobs/"+url.PathEscape(jobID))
    }
    return links, false
}
```

**为什么需要 Links**？

```
场景：值班员收到补充通知，想要深度查看

如果没有 Links：
  - 值班员需要手动打开浏览器
  - 手动输入 URL
  - 手动查找 Incident ID
  - 手动访问 Incident 页面
  - 耗时 10-20 秒

如果有 Links：
  - 值班员点击 "查看更多诊断" 链接
  - 直接跳转到 Incident 页面
  - 耗时 1-2 秒
```

**Base URL 的来源**：

```
优先级：
1. Channel 配置的 baseURL（最高优先级）
2. 系统配置的 notice.base_url（默认）

示例：
Channel 配置：baseURL = "https://rca.internal"
System 配置：notice.base_url = "https://rca-staging.internal"

结果：使用 "https://rca.internal"
```

#### 3.5.4 Payload 裁剪

**为什么需要裁剪**？

```
场景：payload 超过 1MB

如果没有裁剪：
  - webhook 发送失败（HTTP 413 Payload Too Large）
  - 投递失败
  - 值班员收不到补充通知

如果有裁剪：
  - 移除次要字段（如 missing_evidence）
  - 保留核心字段（如 incident、diagnosis）
  - webhook 发送成功
  - 值班员收到裁剪后的补充通知
```

**裁剪策略**：

```go
func shrinkPayload(payload map[string]any) bool {
    // 1. 移除 missing_evidence（次要字段）
    if diagnosis, ok := payload["diagnosis"].(map[string]any); ok {
        if shrinkStringSliceField(diagnosis, "missing_evidence") {
            return true
        }
    }
    
    // 2. 缩短 evidence_ids（保留前半部分）
    if diagnosis, ok := payload["diagnosis"].(map[string]any); ok {
        if shrinkStringSliceField(diagnosis, "evidence_ids") {
            return true
        }
    }
    
    // 3. 移除 diagnosis（保留 diagnosis_min）
    if _, ok := payload["diagnosis"]; ok {
        delete(payload, "diagnosis")
        return true
    }
    
    // 4. 移除 root_cause
    if _, ok := payload["root_cause"]; ok {
        delete(payload, "root_cause")
        return true
    }
    
    // 5. 移除 links
    if _, ok := payload["links"]; ok {
        delete(payload, "links")
        return true
    }
    
    // 6. 缩短 summary
    if shrinkStringField(payload, "summary") {
        return true
    }
    
    return false
}
```

**裁剪顺序**：

1. `missing_evidence`（次要字段）
2. `evidence_ids`（缩短）
3. `diagnosis`（移除，保留 `diagnosis_min`）
4. `root_cause`（移除）
5. `links`（移除）
6. `summary`（缩短）

**为什么这个顺序**？

- **优先保留核心信息**：`incident`、`diagnosis_min` 是核心，不能移除
- **优先移除次要信息**：`missing_evidence`、`links` 是次要，可以移除
- **渐进式裁剪**：每次裁剪一小部分，避免一次性移除太多

---

## 四、渠道抽象与呈现策略

### 4.1 通用后端 contract

#### 4.1.1 后端不关心渠道如何呈现

**核心原则**：

> **后端只负责发送 payload，不关心渠道如何呈现。**

**为什么这样设计**？

```
场景：支持多种渠道（飞书、Slack、企业微信、邮件、SMS）

如果后端关心呈现：
  - 需要为每种渠道实现不同的呈现逻辑
  - 代码复杂度高
  - 难以扩展新渠道

如果后端不关心呈现：
  - 所有渠道都接收相同的 JSON payload
  - 渠道自行决定如何呈现
  - 后端代码简洁
  - 易于扩展新渠道
```

**后端的职责**：

1. **事件触发**：`diagnosis_written` 事件触发
2. **Payload 组装**：构建结构化 JSON
3. **Outbox 写入**：写入 `notice_deliveries` 表
4. **可靠投递**：Worker 带重试的投递

**渠道的职责**：

1. **接收 payload**：Webhook endpoint 接收 JSON
2. **解析 payload**：提取需要的字段
3. **呈现消息**：根据渠道特性呈现（如飞书卡片、Slack Block Kit）
4. **处理交互**：处理用户的点击、回复等交互

#### 4.1.2 Payload 是通用 contract

**所有渠道接收相同的 payload**：

```json
{
  "event_type": "diagnosis_written",
  "timestamp": "2026-04-02T03:15:46Z",
  "incident": {
    "incident_id": "incident-xyz789",
    "namespace": "prod",
    "service": "demo",
    "severity": "critical",
    "rca_status": "diagnosed"
  },
  "diagnosis": {
    "confidence": 0.85,
    "root_cause": {
      "type": "database_connection_pool_exhausted",
      "summary": "数据库连接池耗尽"
    },
    "evidence_ids": ["evidence-1", "evidence-2", "evidence-3", "evidence-4"],
    "missing_evidence": []
  },
  "job": {
    "job_id": "job-abc123def456"
  },
  "links": {
    "version": "v1",
    "base_url": "https://rca.internal",
    "incident_url": "https://rca.internal/v1/incidents/incident-xyz789",
    "evidence_list_url": "https://rca.internal/v1/incidents/incident-xyz789/evidence",
    "job_url": "https://rca.internal/v1/ai/jobs/job-abc123def456"
  },
  "notice": {
    "channel_id": "channel-123",
    "delivery_id": "delivery-456",
    "attempt": 1,
    "status": "pending"
  },
  "summary": "diagnosis_written incident=incident-xyz789 severity=critical service=demo"
}
```

**渠道如何解析**：

```python
# 飞书机器人
@app.route('/webhook/feishu', methods=['POST'])
def feishu_webhook():
    payload = request.json
    
    # 1. 验证签名
    if not verify_signature(request.headers, request.data):
        return jsonify({"error": "invalid signature"}), 401
    
    # 2. 构建飞书卡片
    card = {
        "config": {"wide_screen_mode": True},
        "header": {
            "title": {
                "content": f"【AI 补充通知】🔴 [{payload['incident']['severity']}] {payload['incident']['service']} 异常",
                "tag": "plain_text"
            },
            "template": "red"
        },
        "elements": [
            {
                "tag": "div",
                "text": {
                    "content": f"**诊断时间**：{payload['timestamp']}\n**根因**：{payload['diagnosis']['root_cause']['summary']}（置信度 {int(payload['diagnosis']['confidence'] * 100)}%）",
                    "tag": "lark_md"
                }
            },
            {
                "tag": "action",
                "actions": [
                    {
                        "tag": "button",
                        "text": {"content": "查看完整诊断", "tag": "plain_text"},
                        "url": payload['links']['incident_url'],
                        "type": "default"
                    }
                ]
            }
        ]
    }
    
    # 3. 调用飞书 API
    response = requests.post(
        "https://open.feishu.cn/open-apis/im/v1/messages",
        json={"receive_id": "user_id", "content": json.dumps(card), "msg_type": "interactive"},
        headers={"Authorization": f"Bearer {os.environ['FEISHU_BOT_TOKEN']}"}
    )
    
    return jsonify({"success": True})
```

```python
# Slack 机器人
@app.route('/webhook/slack', methods=['POST'])
def slack_webhook():
    payload = request.json
    
    # 1. 验证签名
    if not verify_signature(request.headers, request.data):
        return jsonify({"error": "invalid signature"}), 401
    
    # 2. 构建 Slack Block Kit
    blocks = [
        {
            "type": "header",
            "text": {
                "type": "plain_text",
                "text": f"【AI 补充通知】🔴 [{payload['incident']['severity']}] {payload['incident']['service']} 异常"
            }
        },
        {
            "type": "section",
            "fields": [
                {
                    "type": "mrkdwn",
                    "text": f"*诊断时间*\n{payload['timestamp']}"
                },
                {
                    "type": "mrkdwn",
                    "text": f"*根因*\n{payload['diagnosis']['root_cause']['summary']}（置信度 {int(payload['diagnosis']['confidence'] * 100)}%）"
                }
            ]
        },
        {
            "type": "actions",
            "elements": [
                {
                    "type": "button",
                    "text": {"type": "plain_text", "text": "查看完整诊断"},
                    "url": payload['links']['incident_url']
                }
            ]
        }
    ]
    
    # 3. 调用 Slack API
    response = requests.post(
        os.environ["SLACK_WEBHOOK_URL"],
        json={"blocks": blocks}
    )
    
    return jsonify({"success": True})
```

### 4.2 特定渠道优化：thread/reply 交互

#### 4.2.1 为什么推荐 thread/reply？

**飞书/Slack 的 thread/reply 特性**：

```
主线程（初版告警）：
  用户 A: 🔴 [P0] Pod 反复重启 - demo 服务

回复（补充通知）：
  用户 B (回复): 【AI 补充通知】
                 根因：数据库连接池耗尽（置信度 85%）
                 证据：4 条
                 建议：扩容连接池

回复（值班员讨论）：
  用户 C (回复): 收到，正在扩容
  用户 D (回复): 建议同时检查慢查询
```

**优势**：

1. **上下文关联**：所有讨论都在同一个 thread 里，不会散落各处
2. **消息聚合**：同一个 Incident 的所有消息都在一个线程里，方便查看
3. **协作便利**：值班员可以直接在补充通知下方回复，讨论诊断结论

#### 4.2.2 如何实现 thread/reply？

**飞书**：

```python
# 1. 发送初版告警，记录 message_id
response = feishu_client.send_text("🔴 [P0] Pod 反复重启", receive_id)
initial_message_id = response["data"]["message_id"]

# 2. 发送补充通知，指定 root_id
response = feishu_client.send_card(
    card_content,
    receive_id,
    root_id=initial_message_id  # 回复到主线程
)
```

**Slack**：

```python
# 1. 发送初版告警，记录 ts
response = slack_client.chat_postMessage(
    channel="#oncall",
    text="🔴 [P0] Pod 反复重启"
)
initial_ts = response["ts"]

# 2. 发送补充通知，指定 thread_ts
response = slack_client.chat_postMessage(
    channel="#oncall",
    blocks=blocks,
    thread_ts=initial_ts  # 回复到主线程
)
```

#### 4.2.3 但这不是后端的硬性 contract

**重要提醒**：

> **thread/reply 是渠道的呈现策略，不是后端的硬性 contract。**

**为什么**？

```
场景：邮件渠道

邮件没有 thread/reply 概念：
  - 初版告警：发送一封邮件
  - 补充通知：发送另一封邮件
  - 两封邮件是独立的，没有关联

如果后端强制要求 thread/reply：
  - 邮件渠道无法实现
  - 后端需要为每种渠道实现不同的逻辑
  - 代码复杂度高

如果后端不强制要求：
  - 飞书/Slack 可以实现 thread/reply
  - 邮件可以发送独立邮件
  - 后端代码简洁
```

**后端只保证**：

- **Payload 一致性**：所有渠道接收相同的 JSON payload
- **Delivery 可靠性**：所有渠道都能收到补充通知
- **Idempotency**：不会重复投递

**渠道自行决定**：

- **如何呈现**：飞书卡片、Slack Block Kit、邮件 HTML、SMS 纯文本
- **是否 thread/reply**：飞书/Slack 支持，邮件不支持
- **如何处理交互**：点击链接、回复消息、按钮操作

### 4.3 Channel Selector：按需投递

#### 4.3.1 Selector 的设计

```json
{
  "event_types": ["diagnosis_written"],
  "namespaces": ["prod", "staging"],
  "services": ["demo", "api"],
  "severities": ["critical", "warning"],
  "root_cause_types": ["database_connection_pool_exhausted"]
}
```

**匹配规则**：

```
规则 1：所有维度必须匹配（AND 逻辑）
  event_type=diagnosis_written AND
  namespace=prod AND
  service=demo AND
  severity=critical AND
  root_cause_type=database_connection_pool_exhausted

规则 2：无 selector 视为匹配所有
  selectors=null → 匹配所有 event、namespace、service...

规则 3：空数组视为什么都不匹配
  event_types=[] → 不匹配任何 event
```

#### 4.3.2 实际应用

**场景 1：值班群（接收所有 P0/P1）**

```json
{
  "event_types": ["diagnosis_written"],
  "namespaces": ["prod"],
  "severities": ["critical", "warning"]
}
```

**场景 2：服务专属群（只接收本服务的诊断）**

```json
{
  "event_types": ["diagnosis_written"],
  "namespaces": ["prod", "staging"],
  "services": ["demo"]
}
```

**场景 3：数据库专项群（只接收数据库相关根因）**

```json
{
  "event_types": ["diagnosis_written"],
  "root_cause_types": [
    "database_connection_pool_exhausted",
    "database_slow_query",
    "database_lock_wait"
  ]
}
```

#### 4.3.3 Selector 的性能

**匹配算法复杂度**：

```
时间复杂度：O(D * V)
  D = 维度数量（event_types、namespaces、services...）
  V = 每个维度的值数量（平均 2-3 个）

实际性能：
  1 个 channel：O(1)，几乎瞬时
  100 个 channel：O(100)，毫秒级
  1000 个 channel：O(1000)，10 毫秒级

结论：性能足够，不需要优化
```

**为什么不需要优化**？

```
场景：1000 个 channel，1 个 diagnosis_written 事件

匹配过程：
  1. 查询所有启用的 channel（1000 个）
  2. 对每个 channel 执行 selector 匹配（1000 次）
  3. 过滤出匹配的 channel（假设 10 个）

耗时：
  查询 channel：10 毫秒（数据库索引）
  匹配 selector：10 毫秒（内存计算）
  总耗时：20 毫秒

20 毫秒可以接受（< 100 毫秒）
```

---

## 五、调试技巧：如何排查补充通知问题

### 5.1 问题 1：没有收到补充通知

#### 5.1.1 排查步骤

**Step 1：检查 diagnosis_written 事件是否触发**

```sql
SELECT * FROM incident_timelines
WHERE incident_id = 'incident-xyz789'
  AND event_type = 'diagnosis_written'
ORDER BY occurred_at DESC
LIMIT 10;
```

**预期结果**：

| incident_id | event_type | occurred_at | metadata |
|------------|-----------|-------------|----------|
| incident-xyz789 | diagnosis_written | 2026-04-02 03:15:46 | {"job_id": "job-abc123..."} |

**如果没有记录**：

- **原因 1**：AIJob Finalize 失败
  - 检查 AIJob 日志：`kubectl logs -l app=apiserver | grep "job-abc123"`
  - 检查 Finalize 错误：`SELECT * FROM ai_jobs WHERE job_id = 'job-abc123'`

- **原因 2**：diagnosis_json 为空
  - 检查 Incident：`SELECT diagnosis_json FROM incidents WHERE incident_id = 'incident-xyz789'`
  - 如果为空，说明 AI 分析失败或未生成诊断

**Step 2：检查 outbox 是否写入**

```sql
SELECT delivery_id, channel_id, event_type, status, attempts, created_at
FROM notice_deliveries
WHERE incident_id = 'incident-xyz789'
  AND event_type = 'diagnosis_written'
ORDER BY created_at DESC
LIMIT 10;
```

**预期结果**：

| delivery_id | channel_id | event_type | status | attempts | created_at |
|------------|-----------|-----------|--------|----------|------------|
| del-123 | ch-456 | diagnosis_written | pending | 0 | 2026-04-02 03:15:46 |
| del-456 | ch-789 | diagnosis_written | succeeded | 1 | 2026-04-02 03:15:46 |

**如果没有记录**：

- **原因 1**：没有匹配的 channel
  - 检查 channel selector：`SELECT channel_id, selectors_json FROM notice_channels WHERE enabled = true`
  - 检查 Incident 属性：`SELECT namespace, service, severity, root_cause_type FROM incidents WHERE incident_id = 'incident-xyz789'`
  - 确认 selector 是否匹配

- **原因 2**：DispatchBestEffort 失败
  - 检查 apiserver 日志：`kubectl logs -l app=apiserver | grep "notice payload build failed"`
  - 检查 payload 组装错误

**Step 3：检查投递状态**

```sql
SELECT delivery_id, channel_id, status, error_text, response_body, last_attempt_at
FROM notice_deliveries
WHERE incident_id = 'incident-xyz789'
  AND event_type = 'diagnosis_written'
  AND status IN ('failed', 'pending')
ORDER BY last_attempt_at DESC
LIMIT 10;
```

**预期结果**：

| delivery_id | channel_id | status | error_text | response_body | last_attempt_at |
|------------|-----------|--------|-----------|--------------|----------------|
| del-123 | ch-456 | failed | timeout: context deadline exceeded | NULL | 2026-04-02 03:15:50 |

**如果 status = failed**：

- **原因 1**：webhook endpoint 不可用
  - 检查 endpoint URL：`SELECT endpoint_url FROM notice_channels WHERE channel_id = 'ch-456'`
  - 手动测试 endpoint：`curl -X POST https://webhook.example.com -d '{"test": "data"}'`
  - 检查网络连通性：`kubectl exec -it apiserver-pod -- curl -v https://webhook.example.com`

- **原因 2**：webhook 验证失败
  - 检查 channel 的密钥指纹或版本号，确认最近是否发生过轮换
  - 检查签名算法：确认 webhook endpoint 的签名验证逻辑
  - 检查 response_body：查看 webhook 返回的错误信息

- **原因 3**：达到最大重试次数
  - 检查 attempts：`SELECT attempts, max_attempts FROM notice_deliveries WHERE delivery_id = 'del-123'`
  - 如果 attempts >= max_attempts，说明已经重试多次但仍然失败

#### 5.1.2 实战案例

**案例 1：channel selector 配置错误**

```
问题：值班员没有收到补充通知

排查：
  1. 检查 incident_timelines：有 diagnosis_written 记录 ✅
  2. 检查 notice_deliveries：没有记录 ❌
  3. 检查 channel selector：
     channel 配置：services = ["api"]
     incident 属性：service = "demo"
     不匹配 ❌

解决：
  修改 channel selector，添加 "demo" 服务
```

**案例 2：webhook endpoint 超时**

```
问题：补充通知偶尔收不到

排查：
  1. 检查 notice_deliveries：status = failed, error_text = "timeout: context deadline exceeded"
  2. 检查 endpoint URL：https://webhook.example.com（外部服务）
  3. 手动测试 endpoint：curl 超时（网络不稳定）

解决：
  1. 增加 timeout_ms：从 3000 毫秒增加到 10000 毫秒
  2. 增加 max_retries：从 3 次增加到 5 次
  3. 联系 webhook 服务提供方，优化性能
```

**案例 3：secret 指纹不匹配**

```
问题：补充通知投递失败，error = "secret_fingerprint_mismatch"

排查：
  1. 检查 notice_deliveries：error_text = "secret_fingerprint_mismatch"
  2. 检查 snapshot：snapshot_secret_fingerprint = "sha256:abc123"
  3. 检查 channel：current_secret_fingerprint = "sha256:def456"
  4. 对比 snapshot 指纹与当前指纹
  5. 不匹配 ❌

原因：
  channel secret 变更后，snapshot 仍然使用旧指纹

解决：
  使用 replay?useLatestChannel=1 重新投递：
  PATCH /v1/notice-deliveries/del-123/replay?useLatestChannel=true
```

### 5.2 问题 2：补充通知内容不完整

#### 5.2.1 排查步骤

**Step 1：检查 Incident 的 diagnosis_json**

```sql
SELECT incident_id, diagnosis_json
FROM incidents
WHERE incident_id = 'incident-xyz789';
```

**预期结果**：

```json
{
  "root_cause": {
    "type": "database_connection_pool_exhausted",
    "summary": "数据库连接池耗尽",
    "confidence": 0.85,
    "evidence_ids": ["evidence-1", "evidence-2", "evidence-3", "evidence-4"]
  },
  "missing_evidence": [],
  "hypotheses": [...]
}
```

**如果 diagnosis_json 不完整**：

- **原因 1**：worker 生成的 diagnosis 缺少字段
  - 检查 worker 日志：`kubectl logs -l app=worker | grep "job-abc123"`
  - 检查 diagnosis 生成逻辑：`internal/worker/diagnosis.go`

- **原因 2**：Finalize 时 validation 失败
  - 检查 apiserver 日志：`kubectl logs -l app=apiserver | grep "validation failed"`
  - 检查 diagnosis validation：`internal/apiserver/biz/v1/ai_job/validation.go`

**Step 2：检查 Evidence 是否完整**

```sql
SELECT evidence_id, type, query, result, created_at
FROM evidence
WHERE incident_id = 'incident-xyz789'
ORDER BY created_at DESC;
```

**预期结果**：

| evidence_id | type | query | result | created_at |
|------------|------|-------|--------|------------|
| evidence-1 | k8s_pod_events | kubectl describe pod... | CrashLoopBackOff | 2026-04-02 03:15:30 |
| evidence-2 | prometheus_metric | max by (instance)... | 100% | 2026-04-02 03:15:32 |
| evidence-3 | loki_logs | {service="demo"}... | Cannot get connection | 2026-04-02 03:15:35 |
| evidence-4 | tempo_traces | traces with error... | p99=5s | 2026-04-02 03:15:40 |

**如果 Evidence 不完整**：

- **原因 1**：Evidence 未发布
  - 检查 worker 日志：`kubectl logs -l app=worker | grep "evidence publish failed"`
  - 检查 Evidence 发布逻辑：`internal/worker/evidence.go`

- **原因 2**：Evidence 发布失败
  - 检查 apiserver 日志：`kubectl logs -l app=apiserver | grep "evidence create failed"`
  - 检查 Evidence 存储：`SELECT COUNT(*) FROM evidence WHERE incident_id = 'incident-xyz789'`

**Step 3：检查 payload 模板配置**

```sql
SELECT channel_id, payload_mode, include_diagnosis, include_evidence_ids, include_root_cause, include_links
FROM notice_channels
WHERE channel_id = 'channel-123';
```

**预期结果**：

| channel_id | payload_mode | include_diagnosis | include_evidence_ids | include_root_cause | include_links |
|-----------|-------------|------------------|---------------------|-------------------|--------------|
| channel-123 | full | true | true | true | true |

**如果配置不正确**：

- **原因 1**：payload_mode = compact
  - 结果：只包含 `diagnosis_min`，不包含 `diagnosis`、`evidence_ids`、`links`
  - 解决：修改为 `full`

- **原因 2**：include_evidence_ids = false
  - 结果：payload 中没有 `evidence_ids` 字段
  - 解决：修改为 `true`

#### 5.2.2 实战案例

**案例 1：payload_mode 配置错误**

```
问题：补充通知没有证据列表

排查：
  1. 检查 notice_deliveries.request_body：没有 evidence_ids ❌
  2. 检查 notice_channels：payload_mode = "compact" ❌
  3. 检查 Incident.diagnosis_json：有 evidence_ids ✅

解决：
  PATCH /v1/notice-channels/channel-123
  {
    "payload_mode": "full"
  }
```

**案例 2：Evidence 发布失败**

```
问题：补充通知的置信度很低（只有 50%）

排查：
  1. 检查 Incident.diagnosis_json：evidence_ids = ["evidence-1"]（只有 1 条）❌
  2. 检查 evidence 表：只有 1 条记录 ❌
  3. 检查 worker 日志：Loki 查询超时，Evidence 未发布 ❌

解决：
  1. 增加 Loki 查询超时时间
  2. 优化 Loki 查询（减少时间范围）
  3. 重新运行 AIJob（如果支持）
```

### 5.3 问题 3：补充通知投递失败

#### 5.3.1 排查步骤

**Step 1：检查失败记录**

```sql
SELECT delivery_id, channel_id, status, error_text, response_body, last_attempt_at, attempts, max_attempts
FROM notice_deliveries
WHERE status = 'failed'
  AND event_type = 'diagnosis_written'
ORDER BY last_attempt_at DESC
LIMIT 10;
```

**常见 error_text**：

| error_text | 含义 | 解决方案 |
|-----------|------|---------|
| timeout: context deadline exceeded | 超时 | 增加 timeout_ms |
| connection refused | 连接被拒绝 | 检查 endpoint URL |
| secret_fingerprint_mismatch | secret 指纹不匹配 | 使用 useLatestChannel 重试 |
| 429 Too Many Requests | 限流 | 降低 channel QPS |
| 500 Internal Server Error | 服务器错误 | 联系 webhook 提供方 |
| 401 Unauthorized | 认证失败 | 检查签名密钥配置 |
| 404 Not Found | 路径不存在 | 检查 endpoint URL |

**Step 2：检查 channel 配置**

```sql
SELECT channel_id, endpoint_url, enabled, timeout_ms, max_retries
FROM notice_channels
WHERE channel_id = 'channel-123';
```

**检查点**：

1. **enabled = true**：channel 是否启用
2. **endpoint_url**：URL 是否正确（协议、域名、路径）
3. **timeout_ms**：超时时间是否合理（建议 3000-10000 毫秒）
4. **max_retries**：重试次数是否合理（建议 3-5 次）
5. **签名密钥配置**：通过受控配置或密钥管理系统确认签名密钥版本是否正确

**Step 3：检查重试记录**

```sql
SELECT delivery_id, attempts, max_attempts, next_retry_at, last_attempt_at
FROM notice_deliveries
WHERE status = 'pending'
  AND next_retry_at > NOW()
ORDER BY next_retry_at ASC
LIMIT 10;
```

**检查点**：

1. **attempts < max_attempts**：是否还有重试机会
2. **next_retry_at**：下次重试时间（是否过长）
3. **last_attempt_at**：上次重试时间（是否太久）

#### 5.3.2 重试机制详解

**指数退避算法**：

```go
func computeRetryDelay(attempt int64, base time.Duration, capDelay time.Duration, jitterMax time.Duration) time.Duration {
    delay := base
    for i := int64(1); i < attempt; i++ {
        delay *= 2  // 指数增长
        if delay >= capDelay {
            delay = capDelay
            break
        }
    }
    // 随机抖动
    jitter := time.Duration(randomNanos(jitterMax.Nanoseconds()))
    return delay + jitter
}
```

**实际例子**：

```
假设：base = 1 秒，cap = 60 秒，jitter = 200 毫秒

第 1 次失败：
  delay = 1 秒 + 随机抖动（0-200 毫秒）
  next_retry_at = now + 1.0-1.2 秒

第 2 次失败：
  delay = 2 秒 + 随机抖动（0-200 毫秒）
  next_retry_at = now + 2.0-2.2 秒

第 3 次失败：
  delay = 4 秒 + 随机抖动（0-200 毫秒）
  next_retry_at = now + 4.0-4.2 秒

第 4 次失败：
  delay = 8 秒 + 随机抖动（0-200 毫秒）
  next_retry_at = now + 8.0-8.2 秒

第 5 次失败：
  delay = 16 秒 + 随机抖动（0-200 毫秒）
  next_retry_at = now + 16.0-16.2 秒

...
  
第 6 次失败：
  delay = 60 秒（达到上限）
  next_retry_at = now + 60.0-60.2 秒
```

**为什么指数退避**？

1. **减少对 webhook endpoint 的压力**：失败越多次，重试间隔越长
2. **给 webhook endpoint 恢复时间**：如果是临时故障，间隔越长，恢复概率越高
3. **避免雪崩效应**：如果所有失败的 delivery 同时重试，可能导致 webhook endpoint 再次崩溃

**为什么随机抖动**？

```
场景：100 个 delivery 同时失败，第 1 次重试

如果没有抖动：
  100 个 delivery 同时重试（1 秒后）
  webhook endpoint 可能被打爆

如果有抖动：
  100 个 delivery 分散重试（1.0-1.2 秒）
  webhook endpoint 负载均衡
```

#### 5.3.3 实战案例

**案例 1：webhook endpoint 返回 429**

```
问题：补充通知投递失败，error = "429 Too Many Requests"

排查：
  1. 检查 notice_deliveries：status = failed, error_text = "429 Too Many Requests"
  2. 检查 channel：global_qps = 20（系统默认）
  3. 检查 webhook 文档：QPS 限制 = 10

解决：
  PATCH /v1/notice-channels/channel-123
  {
    "max_retries": 5,
    "selectors": {...}
  }
  
  然后在系统配置中：
  notice:
    per_channel_concurrency: 2
    global_qps: 10
```

**案例 2：secret 指纹不匹配**

```
问题：补充通知投递失败，error = "secret_fingerprint_mismatch"

排查：
  1. 检查 notice_deliveries：snapshot_secret_fingerprint = "sha256:abc123"
  2. 检查 notice_channels：current_secret_fingerprint = "sha256:def456"
  3. 对比 snapshot 指纹与当前指纹
  4. 不匹配 ❌

原因：
  3 月 1 日创建了 delivery，当时 channel 使用旧指纹
  4 月 1 日轮换了 channel 密钥，对应指纹变为 "sha256:def456"
  但 delivery 的 snapshot 仍然使用旧指纹

解决：
  使用 replay API，指定 useLatestChannel=true：
  
  PATCH /v1/notice-deliveries/del-123/replay?useLatestChannel=true
  
  这会：
  1. 重新读取 channel 的最新配置
  2. 重新构建 snapshot
  3. 使用最新 channel 配置重新投递
```

**案例 3：Redis Stream 不可用**

```
问题：补充通知延迟很高（1 秒以上）

排查：
  1. 检查 notice_deliveries.created_at 和 sent_at 的差值：平均 1.2 秒
  2. 检查 Redis 状态：Redis 宕机
  3. 检查 Worker 日志：stream consume failed, fallback to db claim

原因：
  Redis Stream 不可用，Worker 回退到 DB 轮询
  DB 轮询间隔 = 1 秒，所以延迟 1 秒

解决：
  1. 恢复 Redis
  2. 重启 Worker（自动切换回 Stream 模式）
  
  预防：
  1. 部署 Redis 高可用（哨兵或集群）
  2. 监控 Redis 状态
  3. 设置告警（Redis 不可用时）
```

---

## 六、设计哲学：为什么补充通知是平台闭环的关键

### 6.1 哲学 1：消息触达 vs 平台沉淀

#### 6.1.1 两种场景的不同需求

**值班场景**（消息触达）：

- **需求**：快速获取关键信息，决定是否需要立即介入
- **约束**：时间紧迫（0-3 秒），注意力有限
- **媒介**：手机、即时消息（飞书/Slack）
- **信息密度**：低（只包含核心信息）

**事后复盘**（平台沉淀）：

- **需求**：完整查看上下文，分析根因，总结经验
- **约束**：时间充裕（几分钟到几小时），需要深度分析
- **媒介**：电脑、网页
- **信息密度**：高（包含所有信息）

#### 6.1.2 为什么不能只用一种媒介？

**如果只用消息**：

```
问题 1：消息会被刷屏
  - 补充通知发出后，几分钟内就被其他消息淹没
  - 事后复盘时，找不到当时的补充通知
  - 需要值班员手动保存消息（不现实）

问题 2：消息没有结构化
  - 所有信息都在一条消息里
  - 难以查看完整诊断（需要滚动很长）
  - 难以添加新证据、更新根因

问题 3：消息不支持多轮交互
  - 无法添加评论
  - 无法标记解决状态
  - 无法导出报告
```

**如果只用网页**：

```
问题 1：触达速度慢
  - 值班员需要手动打开网页
  - 手动查找 Incident
  - 耗时 10-20 秒
  - 错过了"立即介入"的窗口

问题 2：需要主动查看
  - 值班员不知道有新的 Incident
  - 需要定期刷新网页（不现实）
  - 可能错过紧急问题
```

#### 6.1.3 "消息+网页"的协同设计

**设计**：

```
值班场景（消息触达）：
  1. 初版告警 → 飞书/Slack（1 秒）
  2. 补充通知 → 飞书/Slack（22 秒）
  3. 值班员快速决策（是否需要介入）

事后复盘（平台沉淀）：
  1. 点击深度链接 → Incident 网页
  2. 查看完整诊断、证据、时间线
  3. 添加评论、标记解决、导出报告
```

**优势**：

1. **快速触达**：消息渠道的触达速度比打开网页快
2. **深度查看**：网页提供结构化展示，方便深度查看
3. **持久记录**：网页永久存储，不会被刷屏
4. **多轮交互**：网页支持添加评论、更新状态等操作

#### 6.1.4 历史演进

**V1 版本（只有消息）**：

```
设计：只发送补充通知到飞书，没有网页

问题：
  - 值班员反馈：消息被刷屏，找不到历史记录
  - 值班员反馈：消息太长，难以查看完整诊断
  - 值班员反馈：无法添加评论、标记解决

结果：值班员满意度低，补充通知的价值没有体现
```

**V2 版本（消息+网页）**：

```
设计：补充通知包含深度链接，指向 Incident 网页

改进：
  - 值班员可以快速查看（消息）
  - 也可以深度查看（网页）
  - 网页永久存储，支持多轮交互

结果：值班员满意度提升，补充通知的价值充分体现
```

**V3 版本（优化消息内容）**：

```
设计：消息只包含核心信息，网页包含完整信息

改进：
  - 消息更简洁，适合手机查看
  - 网页更详细，适合电脑查看
  - 值班员可以根据场景选择查看方式

结果：值班员体验进一步提升
```

### 6.2 哲学 2：结构化数据 vs 自由文本

#### 6.2.1 自由文本的局限性

**如果补充通知是自由文本**：

```
示例：
"经过分析，我们发现 demo 服务的 Pod 反复重启是由于数据库连接池耗尽导致的。具体来说，连接池使用率达到了 100%，活跃连接数为 200，等待连接数为 50。应用日志中出现了 'Cannot get connection from pool' 的错误。建议扩容连接池到 400，并检查慢查询。"

问题 1：无法被检索
  - 无法按"根因类型"检索（如查找所有"连接池耗尽"的 Incident）
  - 无法按"置信度"检索（如查找所有置信度 ≥ 90% 的 Incident）

问题 2：无法被聚合
  - 无法统计"每种根因类型的数量"
  - 无法统计"平均置信度"
  - 无法生成报表

问题 3：无法被后续流程使用
  - 无法自动复盘（需要人工阅读文本）
  - 无法自动优化（需要人工分析文本）
  - 无法集成到其他系统（如监控大盘）
```

#### 6.2.2 结构化数据的优势

**如果补充通知是结构化数据**（JSON）：

```json
{
  "diagnosis": {
    "confidence": 0.85,
    "root_cause": {
      "type": "database_connection_pool_exhausted",
      "summary": "数据库连接池耗尽"
    },
    "evidence_ids": ["evidence-1", "evidence-2", "evidence-3", "evidence-4"],
    "missing_evidence": []
  }
}
```

**优势 1：可检索**

```sql
-- 按根因类型检索
SELECT * FROM incidents
WHERE root_cause_type = 'database_connection_pool_exhausted';

-- 按置信度检索
SELECT * FROM incidents
WHERE diagnosis_confidence >= 0.9;
```

**优势 2：可聚合**

```sql
-- 统计每种根因类型的数量
SELECT root_cause_type, COUNT(*) as count
FROM incidents
GROUP BY root_cause_type
ORDER BY count DESC;

-- 统计平均置信度
SELECT AVG(diagnosis_confidence) as avg_confidence
FROM incidents
WHERE root_cause_type = 'database_connection_pool_exhausted';
```

**优势 3：可被后续流程使用**

```
场景 1：自动复盘
  - 定期运行脚本，分析过去一周的 Incident
  - 统计每种根因类型的数量
  - 生成复盘报告

场景 2：自动优化
  - 发现"连接池耗尽"占比很高
  - 自动调整连接池配置（如增加默认大小）
  - 减少同类问题的发生

场景 3：集成到监控大盘
  - 在 Grafana 中展示"根因类型分布"
  - 展示"平均置信度趋势"
  - 展示"补充通知送达率"
```

#### 6.2.3 结构化数据的设计原则

**原则 1：字段命名要语义化**

```
❌ 不好的命名：
  "rc_type"（缩写不明确）
  "conf"（缩写不明确）
  "ev_list"（缩写不明确）

✅ 好的命名：
  "root_cause_type"（明确）
  "confidence"（明确）
  "evidence_ids"（明确）
```

**原则 2：字段类型要一致**

```
❌ 不一致：
  "confidence": "0.85"（字符串）
  "confidence": 0.85（数字）

✅ 一致：
  "confidence": 0.85（统一为数字）
```

**原则 3：预留扩展字段**

```
当前版本：
  "diagnosis": {
    "confidence": 0.85,
    "root_cause": {...},
    "evidence_ids": [...]
  }

未来扩展：
  "diagnosis": {
    "confidence": 0.85,
    "root_cause": {...},
    "evidence_ids": [...],
    "suggested_actions": [...],  // 新增字段
    "risk_level": "high"         // 新增字段
  }
```

#### 6.2.4 结构化与自由文本的平衡

**结构化字段**（必须）：

- `confidence`：置信度
- `root_cause.type`：根因类型
- `root_cause.summary`：根因摘要
- `evidence_ids`：证据 ID 列表

**自由文本字段**（可选）：

- `root_cause.details`：根因详细说明（自由文本）
- `suggested_actions[].description`：建议操作的详细说明（自由文本）

**为什么这样设计**？

```
结构化字段：
  - 用于检索、聚合、自动化
  - 必须严格规范

自由文本字段：
  - 用于人类阅读、理解
  - 可以灵活描述
```

### 6.3 哲学 3：异步 outbox vs 同步发送

#### 6.3.1 同步发送的局限性

**如果设计成同步发送**：

```go
func Finalize(ctx context.Context, rq *v1.FinalizeAIJobRequest) error {
    // 1. 验证状态转移
    // 2. 验证并规范化 diagnosis JSON
    // 3. 回写 diagnosis_json 到 Incident
    // 4. 清租约
    // 5. 同步发送补充通知
    err := sendNoticeSync(ctx, noticeReq)
    if err != nil {
        return err // 如果发送失败，整个 Finalize 失败
    }
    return nil
}
```

**问题 1：依赖外部系统可用性**

```
场景：webhook endpoint 不可用

T0: 03:15:45 → Finalize 执行
T1: 03:15:46 → 发送 webhook（超时 30 秒）
T2: 03:16:16 → webhook 超时，返回错误
T3: 03:16:16 → Finalize 失败，事务回滚
结果：诊断没有回写，Incident 状态异常
```

**问题 2：阻塞控制面**

```
场景：同时有 100 个 AIJob 完成

如果同步发送：
  - apiserver 需要等待 100 个 webhook 响应
  - apiserver 线程被阻塞，无法处理其他请求
  - apiserver 可能 OOM（内存溢出）
```

**问题 3：无法重试**

```
场景：webhook endpoint 返回 500 错误

如果同步发送：
  - Finalize 失败
  - 无法重试（因为事务已经回滚）
  - 诊断丢失
```

#### 6.3.2 异步 outbox 的优势

**设计**：

```go
func Finalize(ctx context.Context, rq *v1.FinalizeAIJobRequest) error {
    // 1. 验证状态转移
    // 2. 验证并规范化 diagnosis JSON
    // 3. 回写 diagnosis_json 到 Incident
    // 4. 清租约
    // 5. 写入 outbox（不等待外部系统）
    noticepkg.DispatchBestEffort(ctx, st, noticeReq)
    return nil
}
```

**优势 1：解耦**

```
控制面（apiserver）：
  - 只负责写入 outbox
  - 不依赖外部系统可用性
  - 不阻塞在 webhook 上

执行面（notice-worker）：
  - 负责投递 outbox
  - 可以重试、限流、监控
  - 独立扩缩容
```

**优势 2：可靠性**

```
场景：webhook endpoint 不可用

T0: 03:15:45 → Finalize 写入 outbox（成功）
T1: 03:15:46 → Finalize 返回成功
T2: 03:15:47 → notice-worker 尝试投递（失败）
T3: 03:15:48 → notice-worker 重试（1 秒后）
T4: 03:15:50 → notice-worker 重试（2 秒后）
T5: 03:15:54 → notice-worker 重试（4 秒后）
T6: 03:16:02 → webhook 恢复，投递成功
结果：诊断回写成功，补充通知最终送达
```

**优势 3：可观测性**

```
所有投递记录都保存在 notice_deliveries 表：

delivery_id | channel_id | event_type | status | attempts | error_text
------------|------------|------------|--------|----------|-----------
del-123     | ch-456     | diag_writ  | pending| 0        | NULL
del-456     | ch-789     | diag_writ  | failed | 3        | timeout
del-789     | ch-012     | diag_writ  | succ   | 1        | NULL

可以查询：
  - 哪些补充通知失败了？
  - 失败的原因是什么？
  - 重试了多少次？
```

#### 6.3.3 Outbox 模式的分布式系统原理

**Outbox 模式**（也叫 Transactional Outbox）：

```
定义：
  在同一个事务中，同时写入业务数据和 outbox 记录
  确保业务数据和 outbox 记录的一致性

优势：
  - 事务一致性：业务数据和 outbox 记录要么都成功，要么都失败
  - 解耦：业务逻辑不依赖外部系统
  - 可靠：outbox 记录可以重试投递
```

**为什么不用消息队列**？

```
如果用消息队列（如 Kafka）：

  T0: 03:15:45 → Finalize 执行
  T1: 03:15:46 → 写入业务数据（事务）
  T2: 03:15:47 → 发送消息到 Kafka（非事务）
  T3: 03:15:48 → Kafka 不可用，发送失败
  结果：业务数据写入成功，但消息丢失

如果用 Outbox：

  T0: 03:15:45 → Finalize 执行
  T1: 03:15:46 → 写入业务数据 + outbox 记录（同一个事务）
  T2: 03:15:47 → 事务提交成功
  T3: 03:15:48 → notice-worker 从 outbox 读取记录
  T4: 03:15:49 → 投递到 webhook
  结果：业务数据和 outbox 记录一致
```

**Outbox vs 消息队列**：

| 特性 | Outbox | 消息队列 |
|-----|--------|---------|
| 事务一致性 | ✅ 同一个事务 | ❌ 跨系统 |
| 可靠性 | ✅ 数据库持久化 | ✅ 消息队列持久化 |
| 延迟 | ⚠️ 1 秒轮询 | ✅ 毫秒级 |
| 复杂度 | ✅ 简单（数据库表） | ❌ 复杂（需要部署 Kafka） |
| 运维成本 | ✅ 低（已有数据库） | ❌ 高（需要运维 Kafka） |

**为什么选择 Outbox**？

1. **事务一致性**：业务数据和 outbox 记录必须一致
2. **简单可靠**：不需要额外部署消息队列
3. **延迟可接受**：1 秒延迟对于补充通知足够
4. **运维成本低**：利用已有数据库，不需要运维 Kafka

#### 6.3.4 Outbox 模式的历史演进

**V1 版本（同步发送）**：

```
设计：Finalize 同步发送 webhook

问题：
  - webhook 不可用时，Finalize 失败
  - 诊断丢失
  - 值班员收不到补充通知

结果：可靠性差，值班员不信任
```

**V2 版本（消息队列）**：

```
设计：Finalize 发送消息到 Kafka，Consumer 投递 webhook

问题：
  - 事务不一致：业务数据写入成功，但消息发送失败
  - 运维成本高：需要部署和运维 Kafka
  - 延迟问题：Kafka 不可用时，消息丢失

结果：复杂度高，收益有限
```

**V3 版本（Outbox 模式）**：

```
设计：Finalize 写入 outbox（数据库表），Worker 轮询投递

优势：
  - 事务一致：业务数据和 outbox 记录同一个事务
  - 简单可靠：不需要额外部署 Kafka
  - 可重试：Worker 可以重试投递
  - 可观测：所有投递记录都保存在数据库

结果：可靠性高，值班员信任
```

---

## 七、补充通知与 AI RCA 的关系

### 7.1 与告警治理的关系

#### 7.1.1 告警治理定义问题边界

**告警治理链**（文章 5）：

```
原始告警
  ↓ [幂等]
去重后的告警
  ↓ [静默]
静默过滤
  ↓ [去重]
聚合后的告警
  ↓ [指纹聚合]
创建 Incident
```

**补充通知依赖告警治理**：

```
如果没有告警治理：
  - 同一个问题的多个告警，会创建多个 Incident
  - 补充通知会发送多次
  - 值班员收到重复消息
  - 根因分析可能矛盾（同一问题，不同 Incident 得出不同结论）

有了告警治理：
  - 同一个问题的多个告警，聚合到一个 Incident
  - 补充通知只发送一次
  - 值班员收到一条消息
  - 根因分析一致（基于同一个 Incident）
```

#### 7.1.2 实际场景对比

**场景：数据库连接池耗尽，触发 10 个告警**

```
没有告警治理：
  1. 告警 1 → 创建 Incident-1
  2. 告警 2 → 创建 Incident-2
  3. ...
  10. 告警 10 → 创建 Incident-10
  
  AIJob-1 → 分析 Incident-1 → 补充通知 1
  AIJob-2 → 分析 Incident-2 → 补充通知 2
  ...
  AIJob-10 → 分析 Incident-10 → 补充通知 10
  
  值班员收到 10 条补充通知，内容可能不一致

有告警治理：
  1. 告警 1-10 → 指纹聚合 → 创建 Incident-1（包含 10 个告警）
  
  AIJob-1 → 分析 Incident-1（包含 10 个告警）→ 补充通知 1
  
  值班员收到 1 条补充通知，内容完整
```

#### 7.1.3 告警治理与补充通知的协同

**告警治理**：

- **职责**：定义"一个问题"的边界
- **输入**：原始告警
- **输出**：Incident（一个问题）

**补充通知**：

- **职责**：传递"一个问题"的诊断结论
- **输入**：Incident + Diagnosis
- **输出**：补充通知（诊断结论）

**协同设计**：

```
告警治理链：
  告警 → 幂等 → 静默 → 去重 → 聚合 → Incident

补充通知链：
  Incident → AIJob → Evidence → Diagnosis → 补充通知

完整链路：
  告警 → 告警治理 → Incident → AIJob → Diagnosis → 补充通知
```

### 7.2 与主链路的关系

#### 7.2.1 主链路生成 Diagnosis

**主链路**（文章 2）：

```
AlertEvent
  ↓
Incident
  ↓
AIJob
  ↓
Evidence
  ↓
Diagnosis
```

**补充通知**：

```
Diagnosis
  ↓
补充通知
  ↓
值班员
```

**补充通知是主链路的延伸**：

- **主链路**：负责生成 Diagnosis（技术侧）
- **补充通知**：负责传递 Diagnosis（用户侧）

#### 7.2.2 完整链路示意图

```
告警触发
  ↓
告警治理链（幂等、静默、去重、聚合）
  ↓
创建 Incident
  ↓
启动 AIJob
  ↓
查询证据（K8s + Prometheus + Loki + Tempo）
  ↓
生成 Evidence
  ↓
分析诊断（LLM）
  ↓
生成 Diagnosis（结构化）
  ↓
回写 Diagnosis 到 Incident（Finalize）
  ↓
触发 diagnosis_written 事件
  ↓
DispatchBestEffort（写入 outbox）
  ↓
Notice Worker（投递 webhook）
  ↓
补充通知（飞书/Slack）
  ↓
值班员（基于诊断做决策）
```

#### 7.2.3 失败场景分析

**场景 1：AIJob 失败**

```
告警 → Incident → AIJob（失败）→ 无 Diagnosis → 无补充通知

值班员视角：
  - 收到初版告警
  - 没有收到补充通知
  - 需要手动排查

系统视角：
  - AIJob 失败记录在数据库
  - 可以查询失败原因
  - 可以重新运行 AIJob（如果支持）
```

**场景 2：补充通知投递失败**

```
告警 → Incident → AIJob → Diagnosis → Finalize（成功）→ outbox → Worker（失败）→ 无补充通知

值班员视角：
  - 收到初版告警
  - 没有收到补充通知
  - 需要手动查看 Incident 网页

系统视角：
  - outbox 记录在数据库
  - Worker 会重试投递
  - 可以查询失败原因
  - 可以手动重试（replay API）
```

### 7.3 与控制面/执行面的关系

#### 7.3.1 控制面写入 outbox

**控制面**（apiserver）：

```go
// internal/apiserver/pkg/notice/dispatch.go:49-65
func DispatchBestEffort(ctx context.Context, st store.IStore, rq DispatchRequest) {
    // 1. 准备投递计划（选择 channels）
    plan, ok := prepareDispatchPlan(ctx, st, rq)
    if !ok {
        return
    }
    // 2. 为每个 channel 写入 outbox（status=pending）
    for _, channel := range plan.channels {
        enqueueDeliveryForChannel(ctx, st, plan, rq, channel)
    }
}
```

**职责**：

- 写入 `notice_deliveries` 表（status=pending）
- 不等待外部系统响应
- 立即返回成功

#### 7.3.2 执行面投递 outbox

**执行面**（notice-worker）：

```go
// internal/apiserver/pkg/notice/worker.go:160-182
func (w *Worker) RunOnce(ctx context.Context) (int, error) {
    // 1. 从 Stream 消费（优先）
    processedByStream, streamErr := w.runOnceFromStream(ctx)
    if processedByStream > 0 {
        return processedByStream, nil
    }
    
    // 2. 从 DB 回退消费
    processedByDB, err := w.runOnceFromDB(ctx)
    return processedByDB, err
}
```

**职责**：

- 轮询 `notice_deliveries` 表（status=pending）
- 发送 webhook
- 更新 status（succeeded/failed/retry）

#### 7.3.3 为什么需要分层？

**问题 1：控制面不应该阻塞**

```
如果没有分层：
  - apiserver 需要等待 webhook 响应
  - webhook 不可用时，apiserver 阻塞
  - apiserver 无法处理其他请求

有了分层：
  - apiserver 写入 outbox 就返回
  - notice-worker 异步投递
  - apiserver 不阻塞
```

**问题 2：执行面可以独立扩缩容**

```
场景：补充通知投递负载高

如果没有分层：
  - 需要扩容 apiserver（控制面 + 执行面一起扩容）
  - 资源浪费（控制面可能不需要扩容）

有了分层：
  - 只需要扩容 notice-worker（执行面）
  - 资源利用率高
```

**问题 3：执行面可以独立演进**

```
场景：需要支持新的渠道（如企业微信）

如果没有分层：
  - 需要修改 apiserver 代码
  - 需要重新部署 apiserver
  - 风险高（影响控制面）

有了分层：
  - 只需要修改 notice-worker 代码
  - 只需要重新部署 notice-worker
  - 风险低（不影响控制面）
```

---

## 八、总结

### 8.1 补充通知的核心价值

补充通知不是"再发一条消息"，而是**值班体验设计和平台闭环的关键环节**。它的核心价值在于：

#### 8.1.1 分两次发：尊重值班场景的两个决策阶段

- **紧急判断**（0-3 秒）：是否需要立即介入？→ 初版告警
- **详细分析**（22 秒+）：问题是什么，如何解决？→ 补充通知

这两个阶段有不同的认知模式、信息需求、时间窗口。分两次发，是为了**尊重这两个不同的决策阶段**，而不是简单地"多发一条消息"。

#### 8.1.2 可信度表达：通过置信度、证据列表，传递诊断的可信程度

- **置信度**：不是魔术数字，而是证据覆盖率、证据质量、矛盾证据的综合评估
- **证据列表**：可引用、可回复的上下文锚点，支持协作讨论
- **深度链接**：连接消息触达和平台沉淀，支持深度查看和多轮交互

这些设计，让补充通知不仅是"传递信息"，更是"传递可信度"。

#### 8.1.3 异步 outbox：通过 outbox + worker，保证系统可靠性

- **解耦**：控制面不依赖外部系统可用性
- **重试**：notice-worker 可以重试投递，失败不会阻塞诊断回写
- **削峰**：notice-worker 可以限流投递，避免瞬时流量打爆外部系统
- **审计**：所有投递记录都保存在 `notice_deliveries` 表，方便排查问题

这些设计，让补充通知不仅是"发送消息"，更是"可靠投递"。

#### 8.1.4 结构化 payload：通过 JSON 格式，保证数据可追溯、可扩展

- **可检索**：可以按根因类型、置信度检索
- **可聚合**：可以统计每种根因类型的数量、平均置信度
- **可扩展**：预留 `links`、`missing_evidence` 等字段，方便后续扩展
- **可裁剪**：支持 `full` 和 `compact` 两种模式，适应不同渠道需求

这些设计，让补充通知不仅是"文本消息"，更是"结构化数据"。

### 8.2 补充通知的设计哲学

补充通知的设计，体现了三个核心哲学：

#### 8.2.1 哲学 1：消息触达 vs 平台沉淀

> **消息只负责触达，平台页面负责沉淀和复看。**

这个哲学的背后，是对"值班场景"和"事后复盘"的区分：
- **值班场景**：需要快速触达，消息渠道更合适
- **事后复盘**：需要完整上下文，平台页面更合适

补充通知通过"消息+深度链接"的设计，兼顾了两个场景。

#### 8.2.2 哲学 2：结构化数据 vs 自由文本

> **补充通知是结构化数据，不是自由文本。**

这个哲学的背后，是对"可信度"和"可追溯"的追求：
- **自由文本**：无法被检索、无法被聚合、无法被后续流程使用
- **结构化数据**：可以被检索（按根因类型）、可以被聚合（按置信度）、可以被后续流程使用（自动复盘）

补充通知通过 JSON payload 的设计，保证了数据的结构化。

#### 8.2.3 哲学 3：异步 outbox vs 同步发送

> **补充通知使用异步 outbox，不依赖外部系统可用性。**

这个哲学的背后，是对"系统可靠性"的追求：
- **同步发送**：如果外部 webhook 不可用，apiserver 会阻塞或失败
- **异步 outbox**：apiserver 写完 outbox 就返回，不等待外部系统响应

补充通知通过 outbox + worker 的设计，保证了系统的可靠性。

### 8.3 补充通知与 AI RCA 的关系

补充通知是 AI RCA 平台闭环的关键环节，但它不是孤立的：

- **与告警治理的关系**：告警治理定义了"一个问题"的边界，补充通知基于这个边界传递诊断结论
- **与主链路的关系**：主链路生成 Diagnosis，补充通知传递 Diagnosis
- **与控制面/执行面的关系**：控制面写入 outbox，执行面投递 outbox

这些关系，让补充通知成为"辅助决策"落地为"可运行、可运维、可扩展的平台级系统"的关键一环。

---

## 九、延伸阅读

- [文章 5：告警治理与 Incident 前置条件](./05-alert-to-incident-governance.md)
- [文章 2：AI RCA 的主链路设计](./02-main-closed-loop.md)
- [文章 3：控制面与执行面的分层设计](./03-control-plane-vs-execution-plane.md)
- [附录 P1-3：Notice 投递队列与重试规范](../devel/zh-CN/附录P1-3_Notice_投递队列与重试规范.md)
- [internal/apiserver/pkg/notice 包文档](../../../internal/apiserver/pkg/notice/README.md)

---

## 十、关键代码引用

- `internal/apiserver/biz/v1/ai_job/ai_job.go:705-711` - diagnosis_written 事件触发
- `internal/apiserver/pkg/notice/dispatch.go:49-65` - DispatchBestEffort 异步 outbox
- `internal/apiserver/pkg/notice/worker.go:160-182` - Worker 投递与重试
- `internal/apiserver/pkg/notice/payload_template.go:64-106` - Payload 组装
- `internal/apiserver/pkg/notice/dispatch.go:167-198` - Channel selector 匹配
