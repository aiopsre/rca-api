# 从 alert event 到 incident：去重、聚合、降噪是 AI RCA 真正的前置条件

> **系列导读**：这是 AI RCA 八篇系列的第 5 篇。前面 4 篇讲解了价值定位、主链路设计、控制面/执行面分层、AIJob 租约机制。本篇要回答一个被忽视但至关重要的问题：**为什么 AI RCA 不能直接"收告警就分析"，而是必须先定义问题边界？**

凌晨 3 点，监控系统检测到异常：

```
告警名称：K8sPodCrashLoopBackOff
实例：demo-deployment-7b8c9d6e5f-x4y2z
触发时间：2026-04-02 03:15:22
告警标签：service=demo, cluster=prod-cn1, namespace=default
           pod=demo-deployment-7b8c9d6e5f-x4y2z
           trace_id=abc123def456
```

这条告警进入 AI RCA 平台后，会发生什么？

**如果你以为是这样的**：
```
告警进来 → 创建 Incident → 启动 AIJob → 开始分析
```

**那你就错了。**

**真实路径是这样的**：
```
告警进来 
  ↓
【第一关】幂等检查 → 是重试吗？是 → 复用，流程结束
  ↓ 否
【第二关】静默检查 → 在维护窗口吗？是 → 静默，标记 is_silenced
  ↓ 否
【第三关】策略管道 → dedup/burst 风暴抑制？是 → 抑制推进
  ↓ 否
【第四关】灰度控制 → 在灰度范围内吗？否 → 抑制推进
  ↓ 是
【第五关】Incident 聚合 → 有活跃 Incident 吗？是 → 复用
  ↓ 否
创建新 Incident → 记录 timeline → 启动 AIJob
```

**这就是告警治理链**——不是"优化"，而是"前置条件"。没有它，AI RCA 根本无法可信运行。

---

## 一、为什么必须先定义问题边界？

### 1.1 AI 不能在噪声中工作

假设没有告警治理，系统直接"收一条告警，建一个 Incident"：

```
03:15:22 Pod x4y2z CrashLoopBackOff → Incident-001
03:15:23 Pod abc123 CrashLoopBackOff → Incident-002
03:15:24 Pod def456 CrashLoopBackOff → Incident-003
03:15:25 Pod ghi789 CrashLoopBackOff → Incident-004
...
03:15:30 Pod xyz999 CrashLoopBackOff → Incident-010
```

10 秒内，同一问题创建了 10 个 Incident。然后：

- **值班员收到 10 条告警通知**：以为是 10 个独立问题
- **AI 启动 10 个 AIJob**：每个都独立分析，重复查询相同的指标
- **浪费 10 倍的 LLM Token**：每个 AIJob 都调用 LLM，得出相同的结论
- **值班员看到 10 个重复的诊断**：不知道该看哪个

**这就是噪声**。在噪声中，AI 不仅浪费资源，还可能得出矛盾的结论——因为每轮分析的证据覆盖度不同。

### 1.2 可信 RCA 需要清晰的问题边界

RCA 的本质是"根因分析"。分析的前提是**问题边界清晰**：

- 同一个问题的所有告警应该聚合到同一个 Incident
- 不同问题的告警不应该被错误聚合
- 重复告警不应该创建重复的 Incident

如果问题边界模糊，AI 的输出就会模糊：

```
Incident-001: 根因是数据库连接池耗尽（置信度 0.85）
Incident-002: 根因是应用配置错误（置信度 0.75）
Incident-003: 根因是网络分区（置信度 0.65）
```

值班员会问："到底哪个是对的？" 答案是：**它们都是同一个问题，但因为告警被错误拆分，AI 得出了不同的结论**。

### 1.3 治理链的本质：保留审计，抑制推进

**关键洞察**：告警治理不是"丢弃告警"，而是"保留审计，抑制推进"。

```
┌─────────────────────────────────────────────────────┐
│                 告警进入系统                          │
├─────────────────────────────────────────────────────┤
│ AlertEvent（完整落库，保留审计）                      │
│  - 标准化字段                                          │
│  - 原始 payload                                       │
│  - 时间戳、来源                                        │
│  - idempotency_key                                    │
│  - incident_id（可能为空）                             │
├─────────────────────────────────────────────────────┤
│ 治理链决策：                                          │
│  ├─ 幂等 → 复用已有 AlertEvent                        │
│  ├─ 静默 → 不推进 Incident，保留 AlertEvent           │
│  ├─ 去重 → 不推进 timeline，保留 AlertEvent           │
│  ├─ 风暴抑制 → 只保留审计，抑制推进                   │
│  └─ 聚合 → 复用或创建 Incident                        │
├─────────────────────────────────────────────────────┤
│ Incident（问题单，可能被抑制）                        │
│  - 只有通过治理链的告警才推进到 Incident              │
│  - 同一 fingerprint 复用同一个 Incident               │
│  - resolved 后才允许创建新的                          │
└─────────────────────────────────────────────────────┘
```

**为什么这样设计**？

1. **审计需求**：将来有人问"那天晚上到底有没有告警"，系统能给出答案
2. **调试需求**：治理链抑制了推进，但保留了告警记录，方便排查为什么被抑制
3. **统计需求**：可以统计"总告警数"、"被抑制数"、"推进数"，分析系统健康度

---

## 二、治理链详解：一条告警的闯关之旅

让我们跟随一条告警，看它如何通过治理链的五道关卡。

### 2.1 第一关：幂等检查（Idempotency）

**场景**：告警系统发送告警后，30 秒没有收到响应，于是重试发送同一条告警。

```
T0: 03:15:22 → 发送告警 A
T30s: 03:15:52 → 没有响应，重试发送告警 A（相同 fingerprint + 相同时间窗口）
```

**治理链如何判断**：

```go
// internal/apiserver/biz/v1/alert_event/alert_event.go:230-245
if in.idempotencyKey != "" {
    existing, getErr := b.store.AlertEvent().Get(txCtx, 
        where.T(txCtx).F("idempotency_key", in.idempotencyKey))
    if getErr == nil {
        // 检查指纹是否匹配，防止幂等键冲突
        if existing.Fingerprint != in.fingerprint {
            return errno.ErrAlertEventIdempotencyConflict
        }
        // 复用已存在的事件
        reused = true
        eventID = existing.EventID
        incidentID = derefString(existing.IncidentID)
        silenced = existing.IsSilenced
        silenceID = derefString(existing.SilenceID)
        mergeResult = "idempotent_reused"
        return nil
    }
}
```

**关键点**：

- **幂等键的生成**：`fingerprint + timestamp_window`（时间窗口通常为 5 分钟）
- **复用逻辑**：如果找到相同的 `idempotency_key`，直接复用已存在的 `AlertEvent`
- **指纹校验**：防止不同告警意外使用相同的幂等键（冲突检查）
- **结果**：重试的告警不会创建新记录，也不会推进新的 Incident

**为什么需要幂等**？

- 网络不可靠，重试是常态
- 如果不幂等，同一条告警会被处理多次，浪费资源
- 更严重的是：可能触发多次 AI 分析，得出重复或矛盾的结论

### 2.2 第二关：静默检查（Silence）

**场景**：凌晨 4 点，运维团队正在进行数据库升级，已配置静默规则：

```
静默规则：
- 时间：04:00 - 06:00
- 标签：cluster=prod-cn1
- 原因：计划内数据库升级
```

此时，一条关于数据库连接的告警进入系统：

```
告警：MySQLConnectionTimeout
时间：04:30:15
标签：cluster=prod-cn1, service=payment
```

**治理链如何判断**：

```go
// internal/apiserver/biz/v1/alert_event/alert_event.go:253-260
matchedSilence, matchErr := b.matchActiveSilence(txCtx, in)
if matchErr != nil {
    return matchErr
}
if matchedSilence != nil {
    silenced = true
    silenceID = matchedSilence.SilenceID
}
```

**静默规则匹配逻辑**：

```go
// internal/apiserver/biz/v1/alert_event/alert_event.go:XX-XX（简化版）
func (b *alertEventBiz) matchActiveSilence(ctx context.Context, in *alertEventInput) (*model.SilenceM, error) {
    now := time.Now()
    
    // 查询所有活跃的静默规则
    silences, err := b.store.Silence().ListActive(ctx, now)
    if err != nil {
        return nil, err
    }
    
    // 逐个匹配告警标签
    for _, silence := range silences {
        if silence.StartsAt.Before(now) && silence.EndsAt.After(now) {
            // 时间窗口匹配
            if b.matchLabels(in.labels, silence.MatchLabels) {
                return silence, nil
            }
        }
    }
    
    return nil, nil
}
```

**关键点**：

- **静默 ≠ 丢弃**：告警仍然会落库到 `AlertEvent` 表，只是不推进 Incident
- **审计保留**：将来可以通过 `silence_id` 查询"这条告警为什么被静默"
- **时间窗口**：静默规则有明确的开始和结束时间，过期后自动失效
- **标记字段**：`is_silenced=true`，`silence_id="silence-xxx"`

**为什么需要静默**？

- 计划内维护期间，告警是预期的，不应该触发 RCA
- 避免值班员被"预期的告警"打扰
- 但保留审计，方便事后复盘"维护期间到底发生了什么"

### 2.3 第三关：策略管道评估（Policy Pipeline）

**场景**：同一 Pod 的 CrashLoopBackOff 告警每 30 秒触发一次：

```
03:15:22 → Pod x4y2z CrashLoopBackOff
03:15:52 → Pod x4y2z CrashLoopBackOff（相同问题，重复告警）
03:16:22 → Pod x4y2z CrashLoopBackOff（相同问题，重复告警）
```

**策略管道的作用**：执行 dedup（去重）和 burst（风暴抑制）

```go
// internal/apiserver/biz/v1/alert_event/alert_event.go:263-278
if b.ingestPipeline != nil {
    policyDecision, err := b.ingestPipeline.Evaluate(txCtx, alertingingest.EvaluateInput{
        Fingerprint: in.fingerprint,
        Status:      in.status,
        LastSeenAt:  in.lastSeenAt,
        SilenceID:   silenceID,
    })
    if err != nil {
        return errno.ErrAlertEventIngestFailed
    }
    
    // 更新静默和抑制状态
    silenced = policyDecision.Silenced
    silenceID = policyDecision.SilenceID
    suppressIncident = policyDecision.SuppressIncident
    suppressTimeline = policyDecision.SuppressTimeline
}
```

**策略管道返回的关键标志**：

| 标志 | 含义 | 影响 |
|------|------|------|
| `Deduped` | 去重命中 | 不推进 timeline |
| `BurstSuppressed` | 风暴抑制命中 | 不推进 Incident/timeline |
| `SuppressIncident` | 抑制 Incident 推进 | 不创建/复用 Incident |
| `SuppressTimeline` | 抑制 timeline 推进 | 不记录 timeline 事件 |

**Dedup（去重）逻辑**：

```go
// internal/apiserver/pkg/alerting/ingest/policy.go:XX-XX（概念代码）
func (p *Pipeline) evaluateDedup(ctx context.Context, fingerprint string, lastSeenAt time.Time) bool {
    // 查询相同 fingerprint 在去重窗口内的告警
    recentEvent, err := p.store.AlertEvent().GetCurrentByFingerprint(ctx, fingerprint)
    if err != nil {
        return false
    }
    
    // 如果 3 分钟内有相同 fingerprint 的告警，且状态为 firing
    dedupWindow := 3 * time.Minute
    if recentEvent != nil && 
       recentEvent.Status == "firing" &&
       time.Since(recentEvent.LastSeenAt) < dedupWindow {
        return true // 去重命中
    }
    
    return false
}
```

**Burst（风暴抑制）逻辑**：

```go
// internal/apiserver/pkg/alerting/ingest/policy.go:XX-XX（概念代码）
func (p *Pipeline) evaluateBurst(ctx context.Context, fingerprint string) bool {
    // 查询相同 fingerprint 在 1 分钟内的告警数量
    count, err := p.store.AlertEvent().CountRecent(ctx, 
        fingerprint, 
        time.Now().Add(-1*time.Minute),
    )
    if err != nil {
        return false
    }
    
    // 如果 1 分钟内超过 10 条，触发风暴抑制
    burstThreshold := 10
    if count >= burstThreshold {
        slog.WarnContext(ctx, "Burst suppression triggered", 
            "fingerprint", fingerprint, 
            "count", count)
        return true // 风暴抑制命中
    }
    
    return false
}
```

**关键点**：

- **去重**：相同 fingerprint 在窗口内（通常 3-5 分钟）不推进 timeline
- **风暴抑制**：短时间内（1 分钟）大量相同告警，抑制 Incident/timeline 推进
- **保留审计**：即使被抑制，告警仍然落库到 `AlertEvent` 表
- **可配置**：窗口时间和阈值都可以根据业务调整

**为什么需要策略管道**？

- **去重**：避免监控系统周期性检测产生重复告警
- **风暴抑制**：防止级联故障产生告警风暴，导致系统过载
- **资源保护**：避免大量告警同时触发 AI 分析，消耗过多资源

### 2.4 第四关：灰度控制（Rollout）

**场景**：新版本的告警治理策略需要灰度发布，只对部分服务生效：

```
灰度配置：
- 模式：enforce（强制执行）
- 允许列表：service in ["payment", "order"]
- 其他服务：观察模式（只记录，不执行）
```

**治理链如何判断**：

```go
// internal/apiserver/biz/v1/alert_event/alert_event.go:284-288
if !rollout.shouldProgress {
    suppressIncident = true
    suppressTimeline = true
}
```

**灰度决策逻辑**：

```go
// internal/apiserver/biz/v1/alert_event/alert_event.go:476-511
func (b *alertEventBiz) evaluateRolloutDecision(in *ingestInput, options ingestOptions) rolloutDecision {
    decision := rolloutDecision{
        allowed:        true,
        shouldProgress: true,
        dropReason:     "",
    }
    
    if !options.applyRollout {
        return decision
    }
    
    cfg := b.rolloutConfig
    cfg.ApplyDefaults()
    
    if !cfg.Enabled {
        return decision
    }
    
    // 根据配置判断当前告警事件是否在允许范围内
    decision.allowed = rolloutAllowMatched(cfg, in.namespace, in.service)
    
    switch cfg.Mode {
    case alertingingest.RolloutModeObserve:
        // 观察模式：记录但不实际处理
        decision.shouldProgress = false
        decision.dropReason = "observe_mode"
    
    case alertingingest.RolloutModeEnforce:
        // 强制执行模式：根据规则严格控制
        if !decision.allowed {
            decision.shouldProgress = false
            decision.dropReason = "not_allowed"
        }
    }
    
    return decision
}
```

**关键点**：

- **观察模式**：`shouldProgress=false`，只记录日志，不执行治理逻辑
- **强制模式**：不在允许列表的服务，`shouldProgress=false`，抑制推进
- **灰度发布**：可以逐步扩大允许范围，控制风险
- **调试便利**：观察模式下可以看到"如果执行会怎样"，但不影响实际业务

**为什么需要灰度控制**？

- **新功能风险控制**：新治理策略可能有 bug，需要灰度验证
- **业务差异**：不同服务对告警的容忍度不同，需要差异化治理
- **平滑升级**：避免全量发布导致的系统震荡

### 2.5 第五关：Incident 聚合

**场景**：同一个服务的多个 Pod 出现相同的 CrashLoopBackOff 问题：

```
告警 1：Pod x4y2z CrashLoopBackOff, service=demo, cluster=prod-cn1
告警 2：Pod abc123 CrashLoopBackOff, service=demo, cluster=prod-cn1
告警 3：Pod def456 CrashLoopBackOff, service=demo, cluster=prod-cn1
```

**Fingerprint 计算**：系统使用稳定标签计算 fingerprint：

```go
// internal/apiserver/pkg/fingerprint/fingerprint.go:XX-XX
func ComputeFingerprint(labels map[string]string) string {
    // 稳定标签：不会因为基础设施变化而改变
    stableLabels := map[string]string{
        "service":   labels["service"],      // 服务名
        "cluster":   labels["cluster"],      // 集群
        "alertname": labels["alertname"],    // 告警名称
        "severity":  labels["severity"],     // 严重程度
    }
    
    // 忽略高波动标签（volatileLabelKeys 定义在 alert_event.go:58-68）：
    // - pod: Pod 会重启，名称会变
    // - instance: 实例会重建，IP 会变
    // - trace_id: 每次请求都不同
    // - request_id: 每次请求都不同
    // - ip: IP 会变化
    
    // 排序并哈希
    sorted := sortLabels(stableLabels)
    return sha256Hex(sorted)
}
```

**治理链如何聚合**：

```go
// internal/apiserver/biz/v1/alert_event/alert_event.go:291-301
if !suppressIncident {
    incident, created, statusChanged, resolveErr := b.resolveIncidentForIngest(txCtx, in)
    if resolveErr != nil {
        return resolveErr
    }
    incidentCreated = created
    incidentStatusChanged = statusChanged
    if incident != nil {
        incidentID = incident.IncidentID
    }
}
```

**resolveIncidentForIngest 完整逻辑**：

```go
// internal/apiserver/biz/v1/alert_event/alert_event.go:resolveIncidentForIngest (概念代码)
func (b *alertEventBiz) resolveIncidentForIngest(ctx context.Context, in *alertEventInput) (*model.IncidentM, bool, bool, error) {
    // 1. 查询是否有活跃的 Incident 绑定到这个 fingerprint
    incident, err := b.store.Incident().GetByActiveFingerprint(ctx, in.fingerprint)
    if err == nil {
        // 找到活跃 Incident，复用
        return incident, false, false, nil
    }
    
    if !errorsx.Is(err, gorm.ErrRecordNotFound) {
        return nil, false, false, err
    }
    
    // 2. 没有活跃 Incident，检查是否应该创建新的
    // 如果是 resolved 状态的告警，且没有活跃 Incident，不创建
    if in.status == alertStatusResolved {
        return nil, false, false, nil
    }
    
    // 3. 创建新的 Incident
    incidentID := uid.New("incident")
    incident = &model.IncidentM{
        IncidentID:           incidentID,
        ActiveFingerprintKey: &in.fingerprint,
        Status:               incidentStatusNew,
        ServiceName:          in.labels["service"],
        ClusterName:          in.labels["cluster"],
        // ... 其他字段
    }
    
    err = b.store.Incident().Create(ctx, incident)
    if err != nil {
        return nil, false, false, err
    }
    
    return incident, true, false, nil
}
```

**Active Fingerprint Key 机制**：

```go
// internal/apiserver/model/incident.go:28
type IncidentM struct {
    // ...
    ActiveFingerprintKey *string `gorm:"column:active_fingerprint_key;type:varchar(128);uniqueIndex:uniq_incidents_active_fingerprint_key"`
    Status               string  `gorm:"column:status;type:varchar(32);not null"`
    // ...
}
```

**关键点**：

- **唯一索引**：`active_fingerprint_key` 有唯一索引，保证同一 fingerprint 同时只绑定一个活跃 Incident
- **Resolved 后释放**：当 Incident 被 resolved 后，`active_fingerprint_key` 被清空，允许下一次 firing 创建新的 Incident
- **设计取舍**：fingerprint 刻意忽略高波动标签，避免同一问题被错误拆分
- **状态检查**：resolved 状态的告警，即使没有活跃 Incident，也不会创建新的

**为什么需要 Fingerprint 聚合**？

- 同一个问题可能触发多个告警（多个 Pod、多个实例）
- 如果不聚合，同一问题会被拆分成多个 Incident，AI 会重复分析
- 更严重的是：值班员会看到多个相似的 Incident，不知道该处理哪个

---

## 三、治理链的完整时序图

```mermaid
sequenceDiagram
    participant AM as AlertManager
    participant G as Go apiserver
    participant DB as MySQL
    participant AE as AlertEvent
    participant I as Incident
    participant P as Policy Pipeline
    participant R as Rollout Control

    Note over AM,I: 告警进入系统
    AM->>G: Ingest Alert Event
    G->>DB: 开启事务

    Note over G,DB: 第一关：幂等检查
    G->>DB: SELECT * FROM alert_events WHERE idempotency_key = ?
    alt 找到（复用）
        G->>AE: 复用已存在的 AlertEvent
        G-->>AM: 返回（流程结束）
    else 未找到
        G->>DB: INSERT INTO alert_events_history (...) is_current=false

        Note over G,DB: 第二关：静默检查
        G->>DB: SELECT * FROM silences WHERE active AND match_labels
        alt 命中静默
            G->>AE: 标记 is_silenced=true, silence_id=xxx
        end

        Note over G,DB: 第三关：策略管道评估
        G->>P: Evaluate(fingerprint, status, last_seen_at)
        P->>P: 检查 Dedup（去重）
        P->>P: 检查 Burst（风暴抑制）
        P-->>G: policyDecision {deduped, burst_suppressed, suppress_*}
        alt deduped 或 burst_suppressed
            G->>AE: 标记抑制标志
        end

        Note over G,DB: 第四关：灰度控制
        G->>R: evaluateRolloutDecision(namespace, service)
        R-->>G: rollout {shouldProgress, dropReason}
        alt shouldProgress=false
            G->>AE: 标记 suppressIncident=true, suppressTimeline=true
        end

        Note over G,DB: 第五关：Incident 聚合
        G->>DB: SELECT * FROM incidents WHERE active_fingerprint_key = ? AND status != 'resolved'
        alt 找到活跃 Incident
            G->>I: 复用 Incident
            G->>AE: 绑定 incident_id
        else 未找到
            alt suppressIncident=false
                G->>DB: INSERT INTO incidents (active_fingerprint_key, status='new', ...)
                G->>AE: 绑定新 incident_id
            end
        end

        Note over G,DB: 写入 current 视图
        G->>DB: INSERT/UPDATE alert_events_history SET is_current=true, current_key=fingerprint

        Note over G,DB: 推进 timeline（如果未被抑制）
        alt suppressTimeline=false AND incident_id != ""
            G->>DB: INSERT INTO incident_timelines (event='alert_ingested', ...)
            alt incident_created
                G->>DB: INSERT INTO incident_timelines (event='incident_created', ...)
            end
        end

    G->>DB: 提交事务
    G-->>AM: 返回（Incident 已推进）
```

**关键路径**：

1. **幂等路径**：如果命中幂等，直接复用，流程结束
2. **静默路径**：如果命中静默，标记 `is_silenced`，但仍落库
3. **策略抑制路径**：如果 dedup 或 burst 命中，标记抑制标志，不推进 timeline/Incident
4. **灰度抑制路径**：如果灰度控制不允许，标记抑制标志
5. **聚合路径**：如果通过所有检查，复用或创建 Incident，推进 timeline

---

## 四、AlertEvent 数据模型设计

### 4.1 History/Current 双视图设计

```go
// internal/apiserver/model/alert_event.go:1-44
type AlertEventM struct {
    ID              int64      `gorm:"column:id;primaryKey;autoIncrement:true"`
    EventID         string     `gorm:"column:event_id;type:varchar(64);uniqueIndex"`
    IncidentID      *string    `gorm:"column:incident_id;type:varchar(64);index"`
    Fingerprint     string     `gorm:"column:fingerprint;type:varchar(128);index"`
    DedupKey        string     `gorm:"column:dedup_key;type:varchar(128);default:''"`
    // ... 其他字段
    
    // History/Current 双视图关键字段：
    IsCurrent  bool    `gorm:"column:is_current;not null;default:false;index"`
    CurrentKey *string `gorm:"column:current_key;type:varchar(128);uniqueIndex:uniq_alert_events_current_key"`
    
    // 治理链相关字段：
    IdempotencyKey *string `gorm:"column:idempotency_key;type:varchar(128);uniqueIndex:uniq_alert_events_idempotency_key"`
    IsSilenced     bool    `gorm:"column:is_silenced;not null;default:false"`
    SilenceID      *string `gorm:"column:silence_id;type:varchar(64)"`
    
    CreatedAt time.Time `gorm:"column:created_at;index"`
    UpdatedAt time.Time `gorm:"column:updated_at"`
}
```

**关键字段说明**：

| 字段 | 用途 | 索引类型 |
|------|------|---------|
| `is_current` | 区分历史记录（false）和当前记录（true） | 普通索引 |
| `current_key` | 保证同一 fingerprint 只有一个当前记录 | **唯一索引** |
| `idempotency_key` | 幂等检查 | **唯一索引** |
| `fingerprint` | 告警指纹，用于聚合和去重 | 普通索引 |
| `is_silenced` | 是否被静默 | 普通索引 |
| `silence_id` | 静默规则 ID | 普通索引 |

### 4.2 为什么需要双视图？

**场景**：同一 fingerprint 的告警多次触发：

```
T0: 03:15:22 → 告警 A（fingerprint=X）→ 创建 history + current
T1: 03:15:52 → 告警 A（fingerprint=X）→ 创建 history，更新 current
T2: 03:16:22 → 告警 A（fingerprint=X）→ 创建 history，更新 current
```

**表中记录**：

| ID | event_id | fingerprint | is_current | current_key | created_at |
|----|----------|-------------|------------|-------------|------------|
| 1  | event-001 | X          | false      | NULL        | 03:15:22   |
| 2  | event-002 | X          | false      | NULL        | 03:15:52   |
| 3  | event-003 | X          | true       | X           | 03:16:22   |

**查询示例**：

```sql
-- 查询当前活跃的告警（去重后）
SELECT * FROM alert_events_history 
WHERE is_current = true AND status = 'firing';

-- 查询某个 fingerprint 的历史记录
SELECT * FROM alert_events_history 
WHERE fingerprint = 'X' 
ORDER BY created_at DESC;

-- 查询被静默的告警
SELECT * FROM alert_events_history 
WHERE is_silenced = true 
  AND created_at > NOW() - INTERVAL 1 HOUR;
```

**优势**：

1. **历史可追溯**：所有告警记录都保留，不会丢失
2. **当前去重**：通过 `current_key` 唯一索引，保证同一 fingerprint 只有一个当前记录
3. **高效查询**：`is_current` 索引支持快速查询当前告警
4. **审计便利**：可以查询任意时间点的告警状态

### 4.3 mergeCurrentAlert 逻辑

在创建历史记录后，系统会更新或创建当前记录：

```go
// internal/apiserver/biz/v1/alert_event/alert_event.go:327-334
mergeResult, err := b.mergeCurrentAlert(txCtx, in, incidentID, silenced, silenceID)
if err != nil {
    return err
}
// 如果被静默，在合并结果前加上静默前缀
if silenced && mergeResult != "idempotent_reused" {
    mergeResult = "silenced_" + mergeResult
}
```

**mergeCurrentAlert 的作用**：

1. 如果 `current_key=fingerprint` 的记录不存在，创建新的 current 记录
2. 如果已存在，更新 `last_seen_at`、`status` 等字段
3. 返回 `merge_result`，说明是创建还是更新（如 `current_created`、`current_updated`）

**关键点**：

- **Upsert 模式**：先尝试插入，冲突时更新（利用 `current_key` 唯一索引）
- **状态同步**：current 记录的 `status` 会随着最新告警更新
- **时间戳更新**：`last_seen_at` 记录最新一次告警时间

---

## 五、治理链的设计哲学

### 5.1 审计优先原则

**任何进入系统的告警，都必须被记录**。即使被抑制、被静默、被去重，也必须保留审计记录。

```sql
-- AlertEvent 表包含所有告警，无论是否被抑制
SELECT 
    event_id,
    fingerprint,
    incident_id,      -- 可能为空（被抑制时）
    is_silenced,      -- 是否被静默
    silenced,         -- 是否被静默（字段名不一致，实际为 is_silenced）
    created_at
FROM alert_events_history
WHERE created_at > NOW() - INTERVAL 1 HOUR;
```

**为什么审计优先**？

- 事后复盘需要：为什么这条告警没有触发 Incident？
- 统计分析需要：总告警数、被抑制数、推进数
- 合规要求：所有告警必须可追溯

### 5.2 抑制而非丢弃原则

治理链的每个环节都是"抑制推进"，而不是"丢弃告警"：

| 治理环节 | 抑制什么 | 保留什么 |
|---------|---------|---------|
| 幂等 | 不创建新记录 | 复用已存在的 AlertEvent |
| 静默 | 不推进 Incident | 保留 AlertEvent + is_silenced=true |
| 去重 | 不推进 timeline | 保留 AlertEvent + deduped=true |
| 风暴抑制 | 不推进 Incident/timeline | 保留 AlertEvent + burst_suppressed=true |
| 灰度控制 | 不推进治理逻辑 | 保留 AlertEvent + shouldProgress=false |
| 聚合 | 不创建新 Incident | 复用已存在的 Incident |

**为什么抑制而非丢弃**？

- 丢弃意味着数据丢失，无法追溯
- 抑制意味着"暂时不推进"，但保留了所有信息
- 未来可以调整治理策略，重新评估被抑制的告警

### 5.3 问题边界清晰原则

Incident 不是"告警容器"，而是"问题单"。同一问题的所有告警应该聚合到同一个 Incident，不同问题的告警不应该被错误聚合。

**如何保证问题边界清晰**？

1. **稳定 Fingerprint**：忽略高波动标签，避免同一问题被错误拆分
2. **Active Fingerprint Key**：唯一索引保证同一 fingerprint 同时只绑定一个活跃 Incident
3. **Resolved 释放**：只有 resolved 后，才允许下一次 firing 创建新的 Incident
4. **Status 检查**：resolved 状态的告警，即使没有活跃 Incident，也不创建新的

### 5.4 可配置可演进原则

治理链的每个环节都是可配置的：

```yaml
# 治理配置示例
governance:
  idempotency:
    window: 5m  # 幂等窗口 5 分钟
  
  silence:
    enabled: true
  
  deduplication:
    window: 3m  # 去重窗口 3 分钟
  
  burst_suppression:
    window: 1m   # 风暴窗口 1 分钟
    threshold: 10  # 阈值 10 条
  
  fingerprint:
    stable_labels:
      - service
      - cluster
      - alertname
      - severity
    ignore_labels:
      - pod
      - instance
      - trace_id
      - request_id
      - ip
  
  rollout:
    enabled: true
    mode: enforce
    allow_list:
      services: ["payment", "order"]
```

**为什么可配置**？

- 不同业务有不同的治理需求
- 治理策略需要根据实际情况调整
- 未来可能引入更复杂的治理规则（如机器学习降噪）

---

## 六、治理链与 AI RCA 的关系

### 6.1 治理链是 AI RCA 的前置条件

**常见误解**：治理链是"告警平台的优化"，与 AI RCA 无关。

**正确理解**：治理链是 AI RCA 的**前置条件**。没有清晰的问题边界，AI 根本无法可信运行。

```
治理链输出 → 问题边界清晰的 Incident → AI 分析 → 可信的诊断
     ↓                                          ↓
  噪声告警                                    不可信的诊断
```

**为什么是前置条件**？

1. **输入质量决定输出质量**：如果输入是噪声，输出必然不可信
2. **资源效率**：治理链避免了重复分析，节省了 LLM Token 和计算资源
3. **用户体验**：值班员看到的是清晰的问题单，而不是重复的告警

### 6.2 治理链与 AI 的分工

**治理链负责什么**？

- 定义问题边界（聚合、去重、抑制）
- 保留审计记录
- 保证问题单的唯一性

**AI 负责什么**？

- 分析问题根因
- 查询证据
- 生成诊断结论

**为什么这样分工**？

- 治理链是确定性逻辑（规则匹配、时间窗口、唯一索引）
- AI 是非确定性逻辑（LLM 推理、工具调用、证据分析）
- 确定性逻辑应该在非确定性逻辑之前执行，保证输入质量

### 6.3 治理链的局限性

治理链虽然重要，但也有局限性：

1. **无法解决所有噪声**：有些噪声需要 AI 来识别（如语义相似但 fingerprint 不同的告警）
2. **配置复杂**：治理策略需要根据业务调整，配置不当会导致误抑制或漏抑制
3. **无法预测未来**：治理链基于历史数据，无法预测未来的告警模式

**未来方向**：

- **AI 增强调理**：用机器学习识别噪声模式，动态调整治理策略
- **自适应指纹**：根据历史告警自动调整 fingerprint 的标签取舍
- **根因驱动聚合**：不仅基于 fingerprint，还基于根因相似度聚合告警

---

## 七、Debug 技巧：如何排查治理链问题？

### 7.1 查询被抑制的告警

```sql
-- 查询所有被抑制的告警
SELECT 
    event_id,
    fingerprint,
    CASE 
        WHEN is_silenced THEN 'silenced'
        WHEN idempotency_key IS NOT NULL THEN 'idempotent'
        ELSE 'policy_suppressed'
    END AS suppression_reason,
    created_at
FROM alert_events_history
WHERE incident_id IS NULL  -- 没有绑定到 Incident
  AND created_at > NOW() - INTERVAL 1 HOUR
ORDER BY created_at DESC;
```

### 7.2 查询治理链决策日志

```sql
-- 查询最近的告警摄入日志（Go 的 slog）
-- 通常在 Loki 或其他日志系统中
{job="rca-apiserver"} 
|~ "alert event ingested" 
| json 
| line_format "{{.timestamp}} {{.fingerprint}} incident_id={{.incident_id}} merge_result={{.merge_result}} silenced={{.silenced}}"
```

**关键字段**：

- `merge_result`：`idempotent_reused`、`current_created`、`current_updated`、`silenced_*`
- `silenced`：是否被静默
- `policy_decision`：deduped、burst_suppressed 标志
- `rollout_should_progress`：灰度控制是否允许

### 7.3 常见问题排查

**问题 1：告警没有创建 Incident**

```sql
-- 检查是否被抑制
SELECT * FROM alert_events_history 
WHERE fingerprint = 'xxx' 
ORDER BY created_at DESC 
LIMIT 5;

-- 检查是否有活跃的静默规则
SELECT * FROM silences 
WHERE starts_at < NOW() 
  AND ends_at > NOW() 
  AND match_labels LIKE '%cluster=prod-cn1%';
```

**问题 2：相同问题创建了多个 Incident**

```sql
-- 检查 fingerprint 是否一致
SELECT DISTINCT fingerprint, incident_id 
FROM alert_events_history 
WHERE service = 'demo' 
  AND alert_name = 'K8sPodCrashLoopBackOff'
ORDER BY created_at DESC 
LIMIT 10;

-- 检查是否忽略了高波动标签
SELECT labels_json FROM alert_events_history 
WHERE fingerprint = 'xxx' 
LIMIT 1;
```

**问题 3：告警被静默但不应该被静默**

```sql
-- 检查静默规则的匹配标签
SELECT * FROM silences 
WHERE silence_id = 'silence-xxx';

-- 检查告警的标签
SELECT labels_json FROM alert_events_history 
WHERE event_id = 'event-xxx';
```

---

## 八、系列后续文章预告

本文讲解了告警治理链的设计和实现，但很多细节没有展开：

| 篇号 | 标题 | 核心主题 |
|------|------|----------|
| 06 | [补充通知设计](./06-supplemental-notice-design.md) | 可信度、引用回复与 Incident 可回看 |
| 07 | [Skills、MCP 与 LangGraph](./07-skills-mcp-langgraph-runtime.md) | 知识/能力/流程三层装配 |
| 08 | [第一阶段复盘](./08-phase-one-retrospective.md) | 哪些问题解决了，哪些还只是开始 |

下一篇《[补充通知设计](./06-supplemental-notice-design.md)》将深入讲解补充通知的可信度设计、引用回复机制和 Incident 可回看能力。

---

*本文代码与实现基于 [aiopsre/rca-api](https://github.com/aiopsre/rca-api) 仓库（分支：`feature/skills-mcp-integration`）。*
