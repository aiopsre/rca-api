# AI RCA 第一阶段复盘：哪些问题真的被解决了，哪些还只是开始

> **系列导读**：这是 AI RCA 八篇系列的收束之作。前七篇已分别从值班场景价值、主链路设计、控制面与执行面分层、运行时租约、告警治理、补充通知、Skills 装配等角度完成了技术深潜。本篇退后一步，用复盘视角回答三个本质问题：**做之前我们面对什么？第一阶段已做到什么？下一阶段要面对什么？**
>
> **与第一篇的呼应**：第一篇《从告警到补充通知》聚焦"为什么做"和"做什么"，用具体场景展示价值。本篇作为收束，聚焦"做得怎么样"和"接下来往哪走"，用全局视角审视成果与边界。

---

## 一、启程之时：我们面对的是什么？

### 1.1 一个看似无解的困局

2026 年初项目启动时，团队面对的不是"没有工具"，而是**工具太多、数据太散、协同太累**。

```mermaid
mindmap
  root((值班困境))
    工具孤岛
      告警系统
      K8s Dashboard
      Prometheus/Grafana
      Loki
      Tempo
    数据被动
      不会主动告知
      需要人工拼装
      跨系统查询
    协同成本高
      重复操作
      信息不同步
      等待响应
    AI 无法落地
      只能聊天
      没有证据
      无法编排
```

**核心洞察**：问题不在于"缺工具"，而在于**缺少一个能把工具、数据、协同串起来的"中枢"**。

### 1.2 关键边界：为什么选择"辅助决策"而非"自动处置"？

项目启动时有一个关键决策，直接塑造了第一阶段的形态：

```mermaid
graph LR
    subgraph Decision["关键决策"]
        D1["辅助决策<br/>而非<br/>自动处置"]
    end
    
    subgraph Why["三个工程判断"]
        W1["可靠性门槛<br/>85% vs 99.99%"]
        W2["证据完整性<br/>70% vs 95%+"]
        W3["组织接受度<br/>建立信任需要时间"]
    end
    
    subgraph Impact["对第一阶段的影响"]
        I1["聚焦诊断质量"]
        I2["不做处置动作"]
        I3["人 remains in the loop"]
    end
    
    Decision --> Why
    Decision --> Impact
    
    style Decision fill:#ffe6e6,stroke:#ff6b6b
    style Why fill:#e6ffe6,stroke:#4caf50
    style Impact fill:#e6e6ff,stroke:#6b6bff
```

这个决策不是"保守"，而是**对生产环境的敬畏**——辅助决策的容错空间大，自动处置的容错空间极小。

---

## 二、第一阶段：我们走到了哪里？

### 2.1 最小闭环：从 0 到 1 的突破

**核心里程碑**：从告警到 Incident、AIJob、Evidence/Diagnosis 回写到 Notice 异步交付的最小闭环已经跑通。

```mermaid
sequenceDiagram
    participant A as 告警系统
    participant P as AI RCA 平台
    participant I as Incident
    participant J as AIJob
    participant W as Worker
    participant S as Skills/MCP
    participant N as Notice
    
    A->>P: 告警触发
    P->>I: 创建 Incident
    P->>J: 创建 AIJob (claim/reclaim)
    J->>W: Worker Claim
    W->>S: 执行 Skills (K8s/Prometheus/Loki/Tempo)
    S-->>W: 证据返回
    W->>I: 发布 Evidence
    W->>I: 回写 Diagnosis
    I->>N: 触发异步投递
    N-->>User: 补充通知
    
    Note over A,N: 完整链路 ≈ 22 秒
```

**实际收益**（对比第一篇的详细时间线，此处只给结果）：

| 环节 | 传统流程 | AI 辅助 | 改进 |
|------|----------|---------|------|
| 上下文切换 | 75 秒 | 0 秒 | ✅ 100% 消除 |
| 数据查询 | 100 秒 | 22 秒 | ✅ 78% 减少 |
| 沟通协调 | 40 秒 | 10 秒 | ✅ 75% 减少 |
| **总计** | **17 分钟** | **2 分钟** | ✅ **88% 减少** |

### 2.2 架构成果：分层与装配

第一阶段不仅跑通了闭环，更重要的是**建立了可持续演进的架构**。

```mermaid
graph TB
    subgraph CP["Control-Plane (Go)"]
        C1["HTTP API"]
        C2["Strategy Resolve"]
        C3["MySQL 持久化"]
        C4["Reclaim 机制"]
    end
    
    subgraph EP["Execution-Plane (Python)"]
        E1["LangGraph 执行"]
        E2["Skills 装配"]
        E3["MCP 协议"]
        E4["LLM 交互"]
    end
    
    subgraph KL["Knowledge Layer"]
        K1["Knowledge Skills"]
        K2["Progressive Disclosure"]
    end
    
    subgraph CL["Capability Layer"]
        C5["CAPABILITY_CONFIGS"]
        C6["7 步执行流程"]
    end
    
    subgraph PL["Process Layer"]
        P1["Route Agent"]
        P2["Domain Agents"]
        P3["Platform Special Agent"]
    end
    
    CP <-->|"AIJob Claim"| EP
    EP --> KL
    KL --> CL
    CL --> PL
    
    style CP fill:#4a90e2,color:#fff
    style EP fill:#50c878,color:#fff
    style KL fill:#ff8c42,color:#fff
    style CL fill:#9b59b6,color:#fff
    style PL fill:#e74c3c,color:#fff
```

**关键收益**：
- 控制面专注高并发 HTTP 处理
- 执行面专注 LLM 应用执行
- 技术栈各自发挥优势（Go + Python）
- 三层装配支持灵活扩展

### 2.3 阶段性价值对照图

```mermaid
graph LR
    subgraph Before["做之前<br/>(2026 Q1 之前)"]
        B1["告警孤立"]
        B2["数据被动"]
        B3["AI 无法落地"]
        B4["人工拼装"]
        B1 --> B2 --> B3 --> B4
    end
    
    subgraph Phase1["第一阶段<br/>(2026 Q1-Q2)"]
        P1["最小闭环跑通"]
        P2["分层架构落地"]
        P3["异步投递建立"]
        P4["三层装配完成"]
        P1 --> P2 --> P3 --> P4
    end
    
    subgraph Gap["仍待解决"]
        G1["证据完整性"]
        G2["判断稳定性"]
        G3["场景覆盖"]
        G1 --> G2 --> G3
    end
    
    Before -.->|"17min→2min"| Phase1
    Phase1 -.->"但..."| Gap
    
    style Before fill:#ffe6e6,stroke:#ff6b6b
    style Phase1 fill:#e6ffe6,stroke:#4caf50
    style Gap fill:#fff3e6,stroke:#ff9800
```

---

## 三、未竟之路：哪些还只是开始？

### 3.1 核心难点：证据不完整时的判断稳定性

这是第一阶段**没有完全解决**、但必须直面的问题。

```mermaid
xychart-beta
    title "证据完整性 vs 诊断置信度"
    x-axis ["K8s", "Prometheus", "Loki", "Tempo", "DB", "Redis", "MQ"]
    y-axis "覆盖率 %" 0 --> 100
    bar [95, 80, 70, 60, 40, 30, 20]
    line [95, 87, 78, 71, 65, 60, 55]
```

**问题分解**：

| 问题 | 表现 | 影响 |
|------|------|------|
| 数据源覆盖不足 | DB/Redis/MQ 监控未接入 | 40% 场景证据不全 |
| 采集延迟 | Loki 延迟 120s、Prometheus 间隔 60s | 关键证据可能过期 |
| 采样率问题 | Tempo 采样 10% | 90% 追踪数据丢失 |
| LLM 幻觉 | 编造证据、过度推断 | 诊断可信度下降 |

### 3.2 边界再申：AI 不是值班替身

第一阶段结束时，这个边界**依然成立**：

```mermaid
graph TB
    subgraph What["AI 是什么"]
        W1["辅助决策"]
        W2["信息聚合"]
        W3["诊断建议"]
    end
    
    subgraph WhatNot["AI 不是什么"]
        N1["值班替身"]
        N2["自动处置"]
        N3["最终决策者"]
    end
    
    subgraph Why["为什么"]
        H1["证据完整性不足"]
        H2["判断稳定性不足"]
        H3["组织信任需建立"]
    end
    
    What --> Why
    WhatNot --> Why
    
    style What fill:#e6ffe6,stroke:#4caf50
    style WhatNot fill:#ffe6e6,stroke:#ff6b6b
    style Why fill:#e6e6ff,stroke:#6b6bff
```

### 3.3 第一阶段能力成熟度评估

```mermaid
radar
    title "第一阶段能力成熟度（1-5 分）"
    axis data_sources["数据源覆盖"], stability["判断稳定性"], coverage["场景覆盖"], assembly["装配效率"], observability["可观测性"]
    
    current["当前状态"]{3.5, 3, 2.5, 3, 2}
    target["第二阶段目标"]{4.5, 4.5, 4, 4.5, 4}
    
    max 5
    min 0
```

---

## 四、下一阶段：往哪里去？

### 4.1 三大方向

第二阶段的重点**不是重新定义平台**，而是在第一阶段的基础上**扎扎实实做深做广**：

```mermaid
graph TB
    subgraph Direction1["判断更稳"]
        D1["数据源覆盖 ≥ 95%"]
        D2["采集延迟 ≤ 30s"]
        D3["诊断准确率 ≥ 95%"]
        D4["幻觉检测自动化"]
    end
    
    subgraph Direction2["覆盖更广"]
        C1["场景覆盖 20+"]
        C2["团队覆盖 20+"]
        C3["服务覆盖 200+"]
        C4["云厂商集成"]
    end
    
    subgraph Direction3["装配更快"]
        A1["Skills 目录与绑定治理"]
        A2["灰度发布机制"]
        A3["配置即代码"]
        A4["标准化部署"]
    end
    
    Direction1 --> Direction2 --> Direction3
    
    style Direction1 fill:#e6f3ff,stroke:#4a90e2
    style Direction2 fill:#e6ffe6,stroke:#50c878
    style Direction3 fill:#fff0e6,stroke:#ff8c42
```

### 4.2 第二阶段能力目标

第二阶段更适合用能力目标来描述，而不是绑定到容易过时的具体日期：

- **判断更稳**：补齐数据库、Redis、MQ 等关键数据源接入，降低采集延迟，并完善幻觉检测与质量门控
- **覆盖更广**：扩展云厂商监控、业务指标和更多场景模板，提升跨团队、跨服务的覆盖能力
- **装配更快**：完善 Skills 目录与绑定治理、灰度发布机制和配置即代码能力，缩短策略迭代周期

### 4.3 从"辅助决策"到"半自动处置"的演进路径

```mermaid
graph LR
    subgraph Phase1["第一阶段<br/>辅助决策"]
        P1["人 remains in the loop"]
        P2["AI 生成诊断"]
        P3["人做决策"]
    end
    
    subgraph Phase2["第二阶段<br/>半自动"]
        S1["AI 生成处置方案"]
        S2["人确认执行"]
        S3["可灰度可回滚"]
    end
    
    subgraph Phase3["第三阶段<br/>全自动 (特定场景)"]
        A1["已知模式自动处置"]
        A2["未知模式人工介入"]
        A3["持续学习与优化"]
    end
    
    Phase1 -->|"信任建立"| Phase2
    Phase2 -->|"场景收敛"| Phase3
    
    style Phase1 fill:#e6ffe6,stroke:#4caf50
    style Phase2 fill:#fff3e6,stroke:#ff9800
    style Phase3 fill:#e6e6ff,stroke:#6b6bff
```

**关键前提**：
- 证据完整性 ≥ 95%
- 诊断准确率 ≥ 95%
- 故障演练覆盖 ≥ 90%
- 组织接受度 ≥ 80%

---

## 五、复盘与反思

### 5.1 做对的决策

```mermaid
mindmap
  root((做对的决策))
    分层架构
      Control/Execution 分离
      Go + Python 优势
    边界声明
      辅助决策定位
      不做自动处置
    Session 设计
      多轮共享上下文
      人工交互锚点
    Outbox 模式
      诊断不丢失
      支持重试限流
    三层装配
      Knowledge/Capability/Process
      灵活扩展
```

### 5.2 可以做得更好的地方

| 领域 | 当前状态 | 改进方向 |
|------|----------|----------|
| 文档与示例 | 分散在各处 | 统一文档中心 |
| 可观测性 | 基础监控 | Tracing/Logging/Metrics 完整体系 |
| 开发者体验 | 手动配置 | CLI 工具一键创建 |
| 测试覆盖 | 核心场景 | 故障演练体系 |

### 5.3 给后来者的建议

> - 从价值出发，不是从技术出发
> - 明确边界，不做过度承诺
> - 小步快跑，快速迭代
> - 建立信任，不是取代信任

---

## 六、总结：八篇系列全景

### 6.1 八篇关系图

```mermaid
graph TB
    subgraph Foundation["基础篇 (1-3)"]
        F1["1. 值班价值与边界"]
        F2["2. 主链路设计"]
        F3["3. 控制面/执行面分层"]
    end
    
    subgraph Core["核心篇 (4-6)"]
        C1["4. AIJob 租约与运行时"]
        C2["5. 告警治理"]
        C3["6. 补充通知设计"]
    end
    
    subgraph Advanced["进阶篇 (7)"]
        A1["7. Skills/MCP/LangGraph 三层装配"]
    end
    
    subgraph Retrospective["复盘篇 (8)"]
        R1["8. 第一阶段复盘"]
    end
    
    Foundation --> Core
    Core --> Advanced
    Advanced --> Retrospective
    
    style Foundation fill:#e6f3ff,stroke:#4a90e2
    style Core fill:#e6ffe6,stroke:#50c878
    style Advanced fill:#fff3e6,stroke:#ff9800
    style Retrospective fill:#f3e6ff,stroke:#9b59b6
```

### 6.2 第一阶段核心成果与边界

**成果**：
- ✅ 最小闭环已跑通
- ✅ 分层架构落地
- ✅ 异步投递建立
- ✅ 三层装配完成
- ✅ 响应时间 17 分钟 → 2 分钟

**边界**：
- ⚠️ AI 是辅助决策，不是值班替身
- ⚠️ 第一阶段不做自动处置
- ⚠️ 证据不完整时判断稳定性不足

### 6.3 第二阶段核心方向

- 🎯 **判断更稳**：数据源、采集质量、诊断准确率
- 🎯 **覆盖更广**：场景、团队、服务
- 🎯 **装配更快**：Skills 目录与绑定治理、灰度发布、配置即代码

---

## 附录：快速索引

想要深入了解技术细节的读者，可参考前七篇：

| 主题 | 文章 | 核心内容 |
|------|------|----------|
| 值班价值 | 第 1 篇 | 为什么做、做什么 |
| 主链路 | 第 2 篇 | 告警→Incident→AIJob→Evidence→Diagnosis→Notice |
| 分层架构 | 第 3 篇 | Control-Plane vs Execution-Plane |
| 租约运行时 | 第 4 篇 | AIJob Claim/Reclaim、Worker 调度 |
| 告警治理 | 第 5 篇 | Alert → Incident 收敛与关联 |
| 通知设计 | 第 6 篇 | 可信度、引用回复、可回看 |
| 三层装配 | 第 7 篇 | Knowledge/Capability/Process 深度剖析 |

---

**（全文完）**
