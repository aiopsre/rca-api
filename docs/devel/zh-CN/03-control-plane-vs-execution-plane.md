# 控制面与执行面的分层设计：Go rca-api 与 Python orchestrator 为什么必须拆开

> **系列导读**：这是 AI RCA 八篇系列的第 3 篇。第 1 篇解释了 AI RCA 的价值定位（辅助决策），第 2 篇讲解了主链路全貌（从 alert event 到 diagnosis/notice 的对象生命周期）。本篇要回答一个架构问题：**为什么 Go rca-apiserver 和 Python orchestrator 必须拆开，而不是合并成一个同步单体服务？**

这个问题背后隐藏着一个更深层的设计哲学：**当你构建的是一个"平台"而不是"脚本"时，架构分层的必要性不是来自教条，而是来自系统必须面对的物理约束。**

---

## 一、问题的本质：为什么"简单方案"行不通？

如果你熟悉传统的 API 服务设计，可能会问出这个问题：

> "既然都是处理 RCA 请求，为什么要把 LangGraph 执行逻辑放在独立的 Python 进程里？直接写在 Go API 服务里不是更简单吗？"

这个问题很合理。毕竟：
- 少一个进程，部署更简单
- 没有网络调用，延迟更低
- 调试更容易，不需要跨进程追踪

我们也不是没有考虑过这个选项。但在深入分析后，我们发现**合并方案会违反三个基本的物理约束**：

### 约束 1：请求响应周期 vs 不确定执行时间

HTTP 请求有一个隐含假设：**调用方期望在可预测的时间内得到响应**。

这个"可预测的时间"通常是：
- 前端超时：60 秒（浏览器默认）
- API Gateway 超时：29 秒（AWS ALB 默认）
- 服务间调用超时：10-30 秒（大多数 RPC 框架默认）

但 AI RCA 的执行时间是**本质上不可预测**的：

| 场景 | 执行时间 | 不确定性来源 |
|------|----------|-------------|
| 简单告警（CPU 飙高） | 30-60 秒 | 工具调用少，指标查询快 |
| 中等告警（Pod 反复重启） | 2-5 分钟 | 需要查日志、事件、依赖服务 |
| 复杂告警（级联故障） | 10 分钟 + | 多服务依赖分析，多轮 LLM 推理 |

**不确定性不是工程问题，而是 LLM 应用的本质属性**：
- LLM 响应时间不可预测（有时 1 秒，有时 10 秒）
- 工具调用次数不可预测（取决于 LLM 决定）
- 图执行路径不可预测（取决于 router 节点判断）

如果你把不可预测的长执行逻辑塞进同步 HTTP 请求，会发生什么？

**后果 1：超时是必然的，不是偶然的**

```
[前端] → [API Gateway] → [Go Service] → [LangGraph 执行]
   ↓          ↓              ↓              ↓
 60s       29s            ∞              120s+
                            ↑
                       这里卡住了，但上层已经在超时
```

当 LangGraph 执行到 29 秒时，API Gateway 返回 504 Timeout；执行到 60 秒时，前端显示"请求失败"。但此时 LangGraph 还在执行——**执行没有失败，但调用方已经放弃了**。

**后果 2：资源泄漏是必然的**

每个超时请求都会留下：
- 一个半执行的 goroutine（等待 LLM 返回）
- 一个未释放的数据库连接（事务未提交/回滚）
- 一个未清理的临时状态（部分写入的数据）

累积 100 个超时请求后，连接池耗尽，新请求无法进入，服务雪崩。

**后果 3：重试导致问题恶化**

调用方看到超时后，第一反应是什么？**重试**。

```
T0: 请求 A 进入，LangGraph 开始执行
T29s: API Gateway 超时，返回 504
T30s: 前端重试，请求 B 进入
T35s: LangGraph 执行完成（但响应已无法返回）
T36s: 请求 B 开始执行，重复消费相同资源
```

结果：**同一个分析任务被执行了两次**，浪费 LLM Token、重复查询数据库、可能创建重复的 Evidence 记录。

**这就是为什么不能合并的第一个原因**：同步模型无法容纳本质上不确定的执行时间。

### 约束 2：故障域的隔离需求

**任何一个足够复杂的执行逻辑，都会以某种你意想不到的方式失败。**

Python worker 可能因为以下原因崩溃：
- LLM SDK 的内存泄漏（`langchain-core` 历史上多次出现）
- 工具调用 panic（某个 MCP server 返回意外格式）
- LangGraph 节点死循环（router 逻辑 bug）
- 依赖库冲突（`pip install` 引入了不兼容版本）

如果合并到 Go 进程里：

```
┌─────────────────────────────────────────┐
│         Go + Python 单体进程              │
│                                         │
│  [HTTP Handler] ───┐                   │
│       ↓            │                   │
│  [LangGraph] ──────┘ (panic: nil ptr) │
│                                         │
│  → 整个进程退出，HTTP 服务不可用          │
│  → 正在处理的其他请求全部失败            │
│  → 需要重启整个服务恢复                  │
└─────────────────────────────────────────┘
```

**这不是假设，而是必然发生的事件**。问题不在于"会不会崩溃"，而在于"崩溃时影响范围有多大"。

分层设计隔离故障域：

```
┌─────────────────────┐      ┌─────────────────────┐
│  Go rca-apiserver   │      │ Python orchestrator │
│  (控制面)           │      │ (执行面)            │
│  3 个实例            │      │ 10 个实例           │
│                     │      │                     │
│  HTTP API 正常       │      │  Worker-3 崩溃       │
│  继续服务其他请求    │      │  → Worker-5 reclaim │
│                     │      │  → 其他 9 个实例正常  │
└─────────────────────┘      └─────────────────────┘
         ↓                            ↓
      MySQL (真相来源，独立进程)
```

**关键洞察**：故障隔离不是"预防故障"，而是"限制故障的影响范围"。

控制面不应该因为执行面的故障而不可用——就像 Web 服务器不应该因为后端 worker 崩溃而返回 500。

### 约束 3：状态所有权的一致性要求

**每个系统都应该有一个真相来源（Source of Truth），且只有一个。**

在 AI RCA 中，这个真相来源是 MySQL：
- `incidents` 表：Incident 的状态（active/resolved/closed）
- `ai_jobs` 表：AIJob 的状态（queued/running/succeeded/failed）
- `evidence` 表：Evidence 的元数据
- `notice_deliveries` 表：Notice 的投递状态

如果合并成单体，会发生什么？

**场景：内存状态 vs 持久化状态的不一致**

```go
// ❌ 错误设计：内存中持有状态
type AnalysisService struct {
    jobs map[string]*JobState  // 内存状态
    db   *gorm.DB              // 持久化状态
}

func (s *AnalysisService) Execute(jobID string) {
    // 内存中创建状态
    s.jobs[jobID] = &JobState{Status: "running"}
    
    // 执行可能失败
    err := s.runLangGraph(jobID)
    
    // 如果 panic 了，内存状态丢失，但 DB 可能已部分写入
}
```

当进程崩溃时：
- 内存状态 `jobs` 丢失
- DB 中可能有部分写入的记录
- 重启后无法恢复"执行到哪一步"

**这就是状态分裂**：内存说"正在执行"，DB 说"queued"，两者不一致。

分层设计明确了状态所有权：

```
┌─────────────────────────────────────────────────────────────┐
│                    MySQL (唯一真相来源)                       │
│  - incidents.status                                         │
│  - ai_jobs.status, lease_owner, lease_expires_at            │
│  - evidence.*                                               │
│  - notice_deliveries.*                                      │
└─────────────────────────────────────────────────────────────┘
              ↑                        ↑
              │                        │
    ┌─────────────────┐    ┌─────────────────────┐
    │ Go apiserver    │    │ Python worker       │
    │ (状态修改者)     │    │ (状态读取者 + 建议者) │
    │                 │    │                     │
    │ 通过事务修改状态 │    │ 读取状态，执行，    │
    │ 租约校验         │    │ 回写结果            │
    └─────────────────┘    └─────────────────────┘
```

**关键原则**：
- 控制面是状态的**唯一修改者**（通过事务）
- 执行面是状态的**读取者和建议者**（claim job，执行，回写结果）
- 执行面不持有持久化状态（崩溃后无损失）

---

## 二、三平面职责：谁负责什么？

理解了"为什么必须拆分"，再来看"每个平面负责什么"。

AI RCA 系统由三个独立的平面组成，每个平面都有清晰的职责边界：

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              控制面 (Control Plane)                          │
│                         Go rca-apiserver                                     │
│  ┌─────────────────────────────────────────────────────────────────────────┐ │
│  │  主状态 ownership: Incident, AlertEvent, Evidence, AIJob, Notice        │ │
│  │  HTTP API 暴露，平台治理，策略下发，guardrails 执行                      │ │
│  │                                                                         │ │
│  │  核心不变性：                                                            │ │
│  │  1. 所有状态变更必须通过事务                                            │ │
│  │  2. 租约校验必须检查 owner + expires_at                                 │ │
│  │  3. 幂等性必须检查 idempotency_key                                      │ │
│  └─────────────────────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────────────────────┘
                                      ↓ claim/job dispatch
┌─────────────────────────────────────────────────────────────────────────────┐
│                            执行面 (Execution Plane)                          │
│                        Python ai-orchestrator                                │
│  ┌─────────────────────────────────────────────────────────────────────────┐ │
│  │  执行过程 ownership: claim job, execute LangGraph, call tools,         │ │
│  │                       publish evidence, generate diagnosis, finalize   │ │
│  │                                                                         │ │
│  │  核心不变性：                                                            │ │
│  │  1. 不假设自己持有真相（每次操作前从控制面读取状态）                    │ │
│  │  2. 不假设自己不会崩溃（定期 heartbeat 续租）                           │ │
│  │  3. 不假设执行会成功（准备失败回滚策略）                                │ │
│  └─────────────────────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────────────────────┘
                                      ↓ diagnosis_written event
┌─────────────────────────────────────────────────────────────────────────────┐
│                            交付面 (Delivery Plane)                           │
│                          Go notice-worker                                    │
│  ┌─────────────────────────────────────────────────────────────────────────┐ │
│  │  异步投递 ownership: poll outbox, deliver to Feishu/Slack/Webhook,     │ │
│  │                       retry, rate limit                                 │ │
│  │                                                                         │ │
│  │  核心不变性：                                                            │ │
│  │  1. 投递失败不阻塞诊断回写                                              │ │
│  │  2. 外部系统不可用时指数退避                                            │ │
│  │  3. 投递状态可追溯（谁、何时、投递到哪里、结果如何）                    │ │
│  └─────────────────────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────────────────────┘
```

**关键区分**：
- **控制面**：持有平台主状态（MySQL 是真相来源）
- **执行面**：持有执行过程（临时状态，执行结束后消失）
- **交付面**：持有异步投递流程（独立于诊断生成）

这三个平面不能合并，原因已在前文详述。接下来我们深入每个平面的设计细节。

---

## 三、控制面设计：如何守护真相？

控制面的核心职责是**守护真相**——确保所有状态变更都是原子的、可追溯的、符合业务规则的。

### 3.1 状态机的守护者

每个对象都有明确的状态机，控制面负责确保状态转移合法。

**AIJob 状态机**：

```
queued ──claim──→ running ──finalize──→ succeeded
                        │                  │
                        │                  ├──→ failed
                        │                  │
                        │                  └──→ canceled
                        │
                        └──reclaim──→ queued (租约过期)
```

控制面确保：
- 只有 `queued` 状态的 job 能被 claim
- 只有 `running` 状态的 job 能 finalize
- 终态（succeeded/failed/canceled）不能再转移

```go
// internal/apiserver/biz/v1/ai_job/ai_job.go:586-818
func (b *aiJobBiz) Finalize(ctx context.Context, rq *v1.FinalizeAIJobRequest) (
    *v1.FinalizeAIJobResponse, error) {
    
    // 1. 验证当前状态
    if job.Status != "running" {
        return nil, errors.New("job is not running, cannot finalize")
    }
    
    // 2. 验证 owner
    if job.LeaseOwner != rq.LeaseOwner {
        return nil, errors.New("lease owner mismatch")
    }
    
    // 3. 验证终态转移
    if rq.Status != "succeeded" && rq.Status != "failed" {
        return nil, errors.New("invalid final status")
    }
    
    // 4. 事务内执行所有变更
    return b.store.Transaction(func(tx store.IStore) error {
        // 更新 job 状态
        tx.AIJob().UpdateStatus(jobID, rq.Status)
        
        // 清租约
        tx.AIJob().ClearLease(jobID)
        
        // 回写 diagnosis
        tx.Incident().UpdateDiagnosis(incidentID, rq.Diagnosis)
        
        // 触发 notice
        noticepkg.DispatchBestEffort(tx, rq.NoticeRequest)
        
        return nil
    })
}
```

**这就是控制面的价值**：把业务规则编码成状态机校验，防止非法状态转移。

### 3.2 幂等性的守护者

网络是不可靠的。同一个请求可能被发送多次（重试、网络抖动、客户端 bug）。

控制面通过幂等键保证**相同请求只处理一次**：

```go
// internal/apiserver/biz/v1/alert_event/alert_event.go:230-244
if in.idempotencyKey != "" {
    existing, getErr := b.store.AlertEvent().Get(txCtx, 
        where.T(txCtx).F("idempotency_key", in.idempotencyKey))
    if getErr == nil {
        // 复用已存在的事件
        reused = true
        eventID = existing.EventID
        incidentID = derefString(existing.IncidentID)
        return nil  // 直接返回，不创建新记录
    }
}
```

**幂等性不是"防止重复"，而是"允许安全重试"**。

客户端可以安全地重试请求，而不用担心创建重复数据。

### 3.3 租约的守护者

租约是多实例 worker 协作的核心机制。控制面负责：
- 分配租约（claim）
- 续租约（heartbeat）
- 回收租约（reclaim）
- 清理租约（finalize）

```go
// internal/apiserver/store/ai_job.go:159-189
func (a *aiJobStore) ClaimQueued(ctx context.Context, jobID string, 
    leaseOwner string, now time.Time, leaseTTL time.Duration) (int64, error) {
    
    // 查询条件：job_id = ? AND status = 'queued'
    // 确保只有 queued 状态的 job 能被 claim
    res := a.s.DB(ctx).Model(&model.AIJobM{}).
        Where("job_id = ? AND status = ?", jobID, "queued").
        Updates(map[string]any{
            "status":           "running",
            "lease_owner":      owner,
            "lease_expires_at": expiresAt,
            "heartbeat_at":     now,
            "lease_version":    gorm.Expr("lease_version + 1"),
        })
    return res.RowsAffected, nil
}
```

**租约的本质是分布式锁的变体**：
- 有明确的所有者（`lease_owner`）
- 有过期时间（`lease_expires_at`）
- 有版本号防止并发（`lease_version`）

但与传统分布式锁不同，租约设计**假设持有者会崩溃**，所以有过期自动回收机制。

---

## 四、执行面设计：如何优雅地失败？

执行面的核心职责是**执行**——领取任务，调用工具，生成诊断，回写结果。

但执行面设计的关键不是"如何成功"，而是**"如何优雅地失败"**。

### 4.1 假设自己随时会崩溃

生产环境的假设必须是悲观的：

| 乐观假设 | 悲观假设 |
|----------|----------|
| worker 启动后会一直运行 | worker 可能随时崩溃 |
| 执行会顺利完成 | 执行可能在中途失败 |
| 网络调用会成功 | 网络调用可能超时/失败 |
| LLM 会返回结果 | LLM 可能 rate limit/服务不可用 |

基于悲观假设，执行面必须：

**1. 定期 heartbeat 续租**

```python
# tools/ai-orchestrator/orchestrator/runtime/lease.py
class LeaseManager:
    def __init__(self, job_id: str, instance_id: str):
        self.job_id = job_id
        self.instance_id = instance_id
        self.lease_ttl = 30  # 秒
    
    def start_heartbeat(self):
        # 每 10 秒续租一次（lease_ttl 的 1/3）
        interval = self.lease_ttl // 3
        while self.running:
            self._renew()
            time.sleep(interval)
    
    def _renew(self):
        # 续租失败不崩溃，只是停止 heartbeat
        try:
            self.client.renew_heartbeat(self.job_id, self.instance_id)
        except Exception as e:
            logger.warning(f"Heartbeat failed: {e}")
            self.running = False  # 停止 heartbeat，让其他实例 reclaim
```

**2. 执行失败后回写失败状态**

```python
# tools/ai-orchestrator/orchestrator/daemon/job_handler.py
def handle_job(self, job: AIJob):
    try:
        result = self.execute_lang_graph(job)
        self.finalize(job.id, "succeeded", result)
    except Exception as e:
        # 失败也要回写状态，让控制面知道发生了什么
        self.finalize(job.id, "failed", {"error": str(e)})
```

**3. 不持有持久化状态**

```python
# ❌ 错误设计：内存持有状态
class Worker:
    def __init__(self):
        self.job_states = {}  # 崩溃后丢失，无法恢复
    
# ✅ 正确设计：状态在 MySQL
class Worker:
    def handle_job(self, job_id: str):
        # 每次从控制面读取状态
        job = self.client.get_job(job_id)
        # 执行，回写
```

### 4.2 假设其他实例会 reclaim 你

多实例环境下，一个实例崩溃后，其他实例会 reclaim 它的 job。

**这不是失败，而是设计的一部分**。

```
T0: Worker-A claim job-123, lease_expires_at = T0 + 30s
T10: Worker-A heartbeat, lease_expires_at = T10 + 30s
T15: Worker-A 崩溃（网络断了）
T45: lease_expires_at 到达，租约过期
T46: Worker-B reclaim job-123（状态回滚到 queued）
T47: Worker-B claim job-123，重新执行
```

**关键洞察**：reclaim 不是"抢占"，而是"故障恢复"。

执行面必须接受这个事实：**你执行的 job 可能被其他实例 reclaim，你崩溃后你的 job 也会被其他实例 reclaim**。

这不是问题，而是特性。

### 4.3 假设 LLM 会失败

LLM 调用本质上是不稳定的：
- Rate limit（Token 配额耗尽）
- Service unavailable（API 临时故障）
- Timeout（响应太慢）
- Invalid response（返回格式异常）

执行面必须处理这些场景：

```python
# tools/ai-orchestrator/orchestrator/langgraph/nodes/observability.py
def run_observability_agent(state: GraphState, config: RunnableConfig):
    max_retries = 3
    for attempt in range(max_retries):
        try:
            response = llm.invoke(prompt)
            return parse_response(response)
        except RateLimitError as e:
            if attempt == max_retries - 1:
                raise  # 最后一次重试失败，抛出异常
            wait_time = (2 ** attempt) + random.uniform(0, 1)  # 指数退避 + jitter
            time.sleep(wait_time)
        except TimeoutError:
            # 超时立即重试，不加等待
            continue
```

**这就是生产级代码**：不是"假设会成功"，而是"假设会失败，准备好重试策略"。

---

## 五、交付面设计：为什么必须异步？

Notice 投递是典型的"最好有"（best-effort）场景：
- 投递失败不应该阻塞诊断回写
- 投递延迟不应该影响分析执行
- 投递状态应该可追溯

### 5.1 Outbox 模式：解耦生产者和消费者

**同步投递的问题**：

```go
// ❌ 错误设计：同步发送通知
func (b *aiJobBiz) Finalize(jobID string) {
    // ... 回写诊断
    
    // 直接发送通知
    err := sendNotification(diagnosis)  // 可能失败
    if err != nil {
        // 怎么办？回滚 diagnosis 吗？
        return err
    }
}
```

**问题**：
1. 通知发送失败会阻塞诊断回写
2. 外部 API 超时会导致整个事务超时
3. 无法重试（已经返回给调用方）

**异步 Outbox 模式**：

```go
// ✅ 正确设计：写入 outbox，立即返回
func (b *aiJobBiz) Finalize(jobID string) {
    // 事务内：回写诊断 + 写入 outbox
    tx.Exec("UPDATE incidents SET diagnosis_json = ?", diagnosis)
    tx.Exec("INSERT INTO notice_deliveries (event_type, status) VALUES (?, 'pending')", diagnosis_written)
    
    // 立即返回，notice-worker 异步投递
}
```

**好处**：
1. 诊断回写和通知投递解耦
2. notice-worker 可以重试、限流、降级
3. 投递状态可追溯（谁、何时、投递到哪里、结果如何）

### 5.2 投递失败的处理策略

Notice-worker 必须处理各种失败场景：

| 失败类型 | 处理策略 |
|----------|----------|
| 外部 API 超时 | 指数退避重试 |
| Rate limit | 等待后重试 |
| 无效收件人 | 标记失败，通知管理员 |
| 网络故障 | 重试，超过阈值标记失败 |

```go
// internal/apiserver/pkg/notice/worker.go
func (w *worker) deliver(d *NoticeDelivery) error {
    maxRetries := 5
    for attempt := 0; attempt < maxRetries; attempt++ {
        err := w.send(d)
        if err == nil {
            return nil  // 成功
        }
        
        if isPermanentError(err) {
            // 永久错误，不再重试
            w.markFailed(d, err)
            return err
        }
        
        // 临时错误，等待后重试
        waitTime := time.Duration(math.Pow(2, float64(attempt))) * time.Second
        time.Sleep(waitTime)
    }
    
    // 超过最大重试次数
    w.markFailed(d, ErrMaxRetriesExceeded)
    return ErrMaxRetriesExceeded
}
```

---

## 六、MySQL vs Redis：谁是真相来源？

### 6.1 MySQL：主状态真源

**MySQL 存储的对象**：
- `incidents`：Incident 主状态（rca_status、diagnosis_json）
- `alert_events`：告警事件历史
- `ai_jobs`：AIJob 状态（status、lease_owner、lease_expires_at）
- `evidence`：Evidence 元数据
- `notice_deliveries`：Notice 投递状态

**关键设计**：AIJob 的租约由 MySQL 驱动，不是 Redis。

```go
// internal/apiserver/store/ai_job.go:159-189
func (a *aiJobStore) ClaimQueued(ctx context.Context, jobID string, 
    leaseOwner string, now time.Time, leaseTTL time.Duration) (int64, error) {
    
    res := a.s.DB(ctx).Model(&model.AIJobM{}).
        Where("job_id = ? AND status = ?", jobID, "queued").
        Updates(map[string]any{
            "status":           "running",
            "lease_owner":      owner,
            "lease_expires_at": expiresAt,
            "lease_version":    gorm.Expr("lease_version + 1"),
        })
    return res.RowsAffected, nil
}
```

**为什么不用 Redis 存储租约**？

1. **持久化需求**：租约状态需要持久化，Redis 是内存存储
2. **事务需求**：租约变更需要和其他状态变更在同一个事务中
3. **查询需求**：需要复杂查询（找过期租约、按 owner 筛选）

### 6.2 Redis：协调与信号

**Redis 的用途**：
- 长轮询唤醒（worker claim job 时减少轮询延迟）
- 限流（rate limiter）
- 流式协调（dispatch stream）
- Template Registry 缓存（orchestrator 启动时注册）

**关键区分**：Redis 是辅助协调设施，不是主状态机。

```
┌─────────────────────────────────────────────────────────────┐
│                      MySQL (真源)                            │
│  ┌───────────────────────────────────────────────────────┐  │
│  │  ai_jobs: status='queued', lease_owner=NULL           │  │
│  │           → status='running', lease_owner='worker-1'  │  │
│  └───────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
                          ↓ 状态变更事件
┌─────────────────────────────────────────────────────────────┐
│                   Redis (信号/协调)                          │
│  ┌───────────────────────────────────────────────────────┐  │
│  │  rca:job:queued:push(jobID) → 唤醒长轮询 worker        │  │
│  │  rca:template:registry → 缓存已注册 templates          │  │
│  └───────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

**常见误解**：把 Redis 当成状态机。

错误表述："Redis 存储 AIJob 状态"。

正确表述："MySQL 存储 AIJob 状态，Redis 用于唤醒轮询的 worker"。

---

## 七、分层设计的收益总结

| 收益 | 说明 | 如果合并会怎样 |
|------|------|---------------|
| **长任务支持** | HTTP 请求不阻塞在执行上，worker 可以执行任意时长的分析 | 超时是必然的，服务会崩溃 |
| **独立伸缩** | 控制面根据 API 负载伸缩，执行面根据 job 队列伸缩 | 必须整体伸缩，资源浪费 |
| **故障隔离** | worker 崩溃不影响 API 服务，其他实例可以 reclaim | 整个服务崩溃，所有请求失败 |
| **技术栈解耦** | Go 负责状态管理，Python 负责 LLM 执行 | 必须选择单一技术栈，放弃优势 |
| **异步交付** | Notice 独立投递，不阻塞诊断回写 | 投递失败阻塞诊断，外部依赖耦合 |
| **可追溯性** | 所有状态变更存储在 MySQL，支持审计 | 内存状态无法追溯，崩溃后丢失 |

---

## 八、架构设计的哲学思考

回顾整个分层设计，我们可以提炼出几个核心原则：

### 原则 1：假设失败，而不是假设成功

- 假设 worker 会崩溃 → 设计租约 reclaim 机制
- 假设 LLM 会失败 → 设计重试和降级策略
- 假设网络会抖动 → 设计幂等性和重试

**好的架构不是预防所有失败，而是让失败变得可管理。**

### 原则 2：解耦生产者与消费者

- 控制面创建 job，执行面消费 job
- Finalize 写入 outbox，notice-worker 消费 outbox

**解耦让每个组件可以独立演化、独立伸缩、独立失败。**

### 原则 3：明确真相来源

- MySQL 是主状态真源
- Redis 是辅助协调设施
- 执行面不持有持久化状态

**多个真相来源会导致状态分裂，最终数据不一致。**

### 原则 4：状态机优于自由转移

- AIJob 状态机：queued → running → succeeded/failed
- 每个转移都有校验和前置条件

**状态机把业务规则编码成可执行代码，防止非法状态转移。**

---

## 九、实战补充：性能数据、部署指南与调试技巧

前面章节解释了"为什么"要分层，现在让我们看看"怎么做"——性能对比数据、部署指南和调试技巧。

### 8.1 性能对比数据

**理论分析 vs 示例压测数据**

| 指标 | 合并方案（理论） | 分层方案（示例压测） | 提升 |
|------|----------------|----------------|------|
| API 吞吐量 (QPS) | ~50（被执行阻塞） | ~300 | **+500%** |
| API P99 延迟 | 5000-120000ms | 20-50ms | **-99.9%** |
| Worker 扩容粒度 | 无法单独扩 | 从 2 实例到 50 实例 | **独立扩缩容** |
| 内存使用（Go） | 1.5GB（含 Python） | 200MB（仅 Go） | **-86%** |
| 内存使用（Python） | - | 1.2GB/实例 | **独立资源池** |

**为什么合并方案的吞吐量低？**

合并方案的吞吐量受限于执行时间：

```
假设：平均执行时间 = 2 分钟
那么：单实例每小时只能处理 30 个请求
所以：10 实例每小时最多处理 300 个请求

而分层方案：
- Go API 实例：每秒处理 100 个请求（创建 AIJob）
- Python Worker：每 2 分钟处理 1 个请求
- 可以独立增加 Worker 来提升处理能力
```

**示例压测数据**

下面是一组用于说明问题的脱敏压测样本：

```bash
# 测试场景：500 个 RCA 请求，10 个并发
# 环境：4核8G 服务器，MySQL 单节点

# 合并方案（模拟）
并发数: 10
成功数: 120/500
失败数: 380/500
失败原因: 504 Gateway Timeout (312), 503 Service Unavailable (68)
平均响应时间: 68,432ms
P99 响应时间: 120,000ms

# 分层方案（示例）
并发数: 10
成功数: 500/500
失败数: 0/500
API 平均响应时间: 38ms
Worker 平均处理时间: 118,432ms
```

**关键洞察**：

- 合并方案的"失败"不是执行失败，而是"超时失败"——执行其实成功了，但响应无法返回
- 分层方案通过"异步接收"解耦了响应时间和执行时间
- Worker 的处理时间再长，也不会影响 API 的响应速度

### 8.2 Kubernetes 部署配置

**控制面 Deployment**

```yaml
# control-plane-deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: rca-apiserver
spec:
  replicas: 3  # 高可用，防止单点故障
  selector:
    matchLabels:
      app: rca-apiserver
  template:
    metadata:
      labels:
        app: rca-apiserver
    spec:
      containers:
      - name: apiserver
        image: aiopsre/rca-apiserver:latest
        ports:
        - containerPort: 8080
        resources:
          requests:
            cpu: 500m
            memory: 256Mi
          limits:
            cpu: 1000m
            memory: 512Mi
        livenessProbe:
          httpGet:
            path: /healthz
            port: 8080
          initialDelaySeconds: 30
          periodSeconds: 10
        readinessProbe:
          httpGet:
            path: /readyz
            port: 8080
          initialDelaySeconds: 5
          periodSeconds: 5
        env:
        - name: MYSQL_DSN
          valueFrom:
            secretKeyRef:
              name: rca-secrets
              key: mysql-dsn
        - name: REDIS_ADDR
          value: "redis://rca-redis:6379"
        - name: LEASE_TTL_SECONDS
          value: "30"  # 租约时长 30 秒
        - name: RECLAIM_INTERVAL_SECONDS
          value: "10"  # reclaim 扫描间隔 10 秒
```

**执行面 Deployment**

```yaml
# execution-plane-deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: rca-orchestrator
spec:
  replicas: 5  # 根据负载调整，可以配置 HPA
  selector:
    matchLabels:
      app: rca-orchestrator
  template:
    metadata:
      labels:
        app: rca-orchestrator
    spec:
      containers:
      - name: orchestrator
        image: aiopsre/rca-orchestrator:latest
        resources:
          requests:
            cpu: 1000m
            memory: 1Gi
          limits:
            cpu: 2000m
            memory: 2Gi
        livenessProbe:
          exec:
            command:
            - /bin/sh
            - -c
            - ps aux | grep -q '[o]rchestrator'
          initialDelaySeconds: 30
          periodSeconds: 30
        env:
        - name: API_SERVER_URL
          value: "http://rca-apiserver:8080"
        - name: MAX_CONCURRENT_JOBS
          value: "2"  # 单实例并发数，避免 OOM
        - name: HEARTBEAT_INTERVAL_SECONDS
          value: "10"  # 心跳间隔 10 秒
        - name: PYTHONUNBUFFERED
          value: "1"
        volumeMounts:
        - name: skills-cache
          mountPath: /app/skills-cache
      volumes:
      - name: skills-cache
        emptyDir: {}
      affinity:
        podAntiAffinity:
          preferredDuringSchedulingIgnoredDuringExecution:
          - weight: 100
            podAffinityTerm:
              labelSelector:
                matchExpressions:
                - key: app
                  operator: In
                  values:
                  - rca-orchestrator
              topologyKey: kubernetes.io/hostname
```

**Notice Worker Deployment**

```yaml
# notice-worker-deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: rca-notice-worker
spec:
  replicas: 2  # 单实例足够，避免重复投递
  strategy:
    type: Recreate  # 避免滚动更新时双实例同时运行
  template:
    metadata:
      labels:
        app: rca-notice-worker
    spec:
      containers:
      - name: notice-worker
        image: aiopsre/rca-notice-worker:latest
        resources:
          requests:
            cpu: 200m
            memory: 128Mi
          limits:
            cpu: 500m
            memory: 256Mi
        env:
        - name: API_SERVER_URL
          value: "http://rca-apiserver:8080"
        - name: POLL_INTERVAL_SECONDS
          value: "5"  # 轮询间隔 5 秒
        - name: MAX_BATCH_SIZE
          value: "10"  # 每次批量处理 10 条
        - name: MAX_RETRY_COUNT
          value: "5"  # 最大重试次数
```

**Horizontal Pod Autoscaler（HPA）**

```yaml
# worker-hpa.yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: rca-orchestrator-hpa
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: rca-orchestrator
  minReplicas: 3
  maxReplicas: 20
  metrics:
  - type: Resource
    resource:
      name: cpu
      target:
        type: Utilization
        averageUtilization: 70
  - type: Pods
    pods:
      metric:
        name: running_jobs_count  # 自定义指标：运行中的 job 数
      target:
        type: AverageValue
        averageValue: "5"  # 平均每个实例 5 个 running jobs
```

**关键配置说明**

| 配置项 | 推荐值 | 依据 |
|--------|--------|------|
| `LEASE_TTL_SECONDS` | 30 | 大于心跳间隔，小于网络分区恢复时间 |
| `RECLAIM_INTERVAL_SECONDS` | 10 | 及时发现崩溃，但不过度扫描数据库 |
| `HEARTBEAT_INTERVAL_SECONDS` | 10 | `lease_ttl / 3`，避免太频繁 |
| `MAX_CONCURRENT_JOBS` | 2 | 1.2GB 内存/实例，每个 job ~600MB |
| `POLL_INTERVAL_SECONDS` | 5 | notice 投递不要求实时，5 秒足够 |
| `MAX_RETRY_COUNT` | 5 | 指数退避，5 次后放弃 |

### 8.3 监控和告警规则

**关键监控指标（Prometheus）**

```yaml
# prometheus-rules.yaml
groups:
- name: rca-control-plane
  rules:
  # Worker 实例数过少
  - alert: RCALowWorkerCount
    expr: sum(kube_deployment_status_replicas{deployment="rca-orchestrator"}) < 3
    for: 5m
    labels:
      severity: warning
    annotations:
      summary: "RCA worker 实例数过少"
      description: "{{ $value }} 个实例，低于最小要求 3"
  
  # Running jobs 过多（可能 worker 处理能力不足）
  - alert: RCARunningJobsTooHigh
    expr: sum(ai_job_status{status="running"}) > 50
    for: 10m
    labels:
      severity: warning
    annotations:
      summary: "Running AIJob 数量过多"
      description: "{{ $value }} 个 running jobs，考虑扩容 worker"
  
  # Reclaim 率过高（可能 worker 频繁崩溃）
  - alert: RCARelaclmRateTooHigh
    expr: rate(ai_job_reclaimed_total[5m]) > 0.1
    for: 10m
    labels:
      severity: critical
    annotations:
      summary: "AIJob reclaim 率过高"
      description: "过去 5 分钟内，每秒 {{ $value }} 个 reclaim，可能 worker 崩溃"
  
  # Notice 投递失败率过高
  - alert: RCANoticeDeliveryFailureRate
    expr: rate(notice_delivery_failed_total[5m]) / rate(notice_delivery_total[5m]) > 0.1
    for: 5m
    labels:
      severity: critical
    annotations:
      summary: "Notice 投递失败率过高"
      description: "失败率 {{ $value }}%，检查通知渠道"

- name: rca-execution-plane
  rules:
  # Worker 内存使用率过高
  - alert: RCAWorkerMemoryHigh
    expr: container_memory_usage_bytes{pod=~"rca-orchestrator.*"} / container_spec_memory_limit_bytes{pod=~"rca-orchestrator.*"} > 0.85
    for: 5m
    labels:
      severity: warning
    annotations:
      summary: "RCA worker 内存使用率过高"
      description: "Pod {{ $labels.pod }} 使用率 {{ $value }}%"
  
  # Worker 心跳失败
  - alert: RCAWorkerHeartbeatFailed
    expr: time() - max(orchestrator_last_heartbeat_time) > 60
    for: 2m
    labels:
      severity: critical
    annotations:
      summary: "RCA worker 心跳失败"
      description: "超过 60 秒没有心跳，可能 worker 崩溃"
```

**Grafana 关键面板**

1. **AIJob 状态分布**：queued/running/succeeded/failed 的实时数量
2. **Worker 实例监控**：CPU、内存、running jobs、心跳时间
3. **Reclaim 率趋势**：每小时的 reclaim 数量和成功率
4. **Notice 投递成功率**：按 channel 分组的成功率
5. **执行时间分布**：P50/P95/P99 执行时间

**日志查询示例（Loki）**

```logql
# 查找某个 Incident 的所有 AIJob
{job="rca-apiserver"} |~ "incident_id=incident-123456" | json | line_format "{{.timestamp}} {{.level}} {{.event}} {{.job_id}}"

# 查找 worker 崩溃日志
{job="rca-orchestrator"} |~ "panic|SIGKILL|OOM" | json | line_format "{{.timestamp}} {{.level}} {{.error}}"

# 查找 reclaim 相关日志
{job="rca-apiserver"} |~ "Reclaimed expired job" | json | line_format "{{.timestamp}} {{.event}} job_id={{.job_id}} old_owner={{.old_owner}}"
```

### 8.4 跨进程调试技巧

#### 8.4.1 TraceID 传递

**TraceID 生成**

```go
// internal/apiserver/biz/v1/ai_job/ai_job.go
func CreateAIJob(ctx context.Context, rq *v1.CreateAIJobRequest) (*v1.CreateAIJobResponse, error) {
    // 生成 TraceID
    traceID := uid.New("trace")
    
    // 创建 AIJob 时带上 TraceID
    job := &model.AIJobM{
        JobID:    uid.New("job"),
        TraceID:  traceID,  // 关键：传递到 worker
        Pipeline: rq.Pipeline,
        Status:   "queued",
    }
    
    // 记录日志时带上 TraceID
    log.WithFields(log.Fields{
        "trace_id": traceID,
        "job_id":   job.JobID,
    }).Info("AIJob created")
    
    return &v1.CreateAIJobResponse{JobID: job.JobID}, nil
}
```

**TraceID 传递到 Worker**

```python
# Python orchestrator: claim_job.py
async def claim_job():
    # 查询 queued jobs
    jobs = await query_queued_jobs()
    
    for job in jobs:
        # Claim job 时，记录 TraceID
        logger.info(f"Claiming job {job.job_id}, trace_id={job.trace_id}")
        
        # 执行 LangGraph 时，所有日志带上 TraceID
        with trace_context(trace_id=job.trace_id):
            await execute_langgraph(job)
```

**日志格式**

```json
// Go 控制面日志
{
  "timestamp": "2026-04-02T15:30:00Z",
  "level": "INFO",
  "trace_id": "trace-abc123",
  "job_id": "job-123456",
  "event": "ai_job_created",
  "pipeline": "basic_rca"
}

// Python 执行面日志
{
  "timestamp": "2026-04-02T15:30:05Z",
  "level": "INFO",
  "trace_id": "trace-abc123",
  "job_id": "job-123456",
  "event": "langgraph_started",
  "node": "router"
}

// Notice Worker 日志
{
  "timestamp": "2026-04-02T15:32:00Z",
  "level": "INFO",
  "trace_id": "trace-abc123",
  "job_id": "job-123456",
  "event": "notice_delivered",
  "channel": "feishu"
}
```

#### 8.4.2 常见问题排查

**问题 1：Worker Claim 失败**

```bash
# 现象：Worker 长时间没有 claim 到 job
# 排查步骤：

# 1. 检查有多少 queued jobs
SELECT COUNT(*) FROM ai_jobs WHERE status = 'queued';
# 预期：> 0

# 2. 检查 worker 实例数
kubectl get pods -l app=rca-orchestrator
# 预期：>= 3

# 3. 检查 worker 日志
kubectl logs -l app=rca-orchestrator --tail=100 | grep -i "claim"
# 看是否有错误（数据库连接失败、权限问题等）

# 4. 检查数据库连接
kubectl exec -it <debug-pod> -- mysql --defaults-extra-file=/etc/mysql/client.cnf -h <mysql-host>
# 使用受控诊断凭据验证连通性，避免在命令行直接暴露密码
```

**问题 2：Worker Finalize 失败**

```bash
# 现象：Worker 执行完成但 finalize 失败，job 留在 running 状态
# 排查步骤：

# 1. 检查 finalize API 是否可达
curl -X POST http://rca-apiserver:8080/v1/ai-jobs/job-123/finalize
# 看返回状态码

# 2. 检查 job 状态
SELECT status, lease_owner, lease_expires_at FROM ai_jobs WHERE job_id = 'job-123';
# 如果 status='running' 但 lease_expires_at 已过期，说明 worker 崩溃了

# 3. 检查 worker finalize 时的错误日志
kubectl logs -l app=rca-orchestrator --tail=100 | grep -i "finalize"
# 看是否有诊断格式错误、网络超时等

# 4. 手动 reclaim 并重试
# 等待 reclaim 扫描（或手动触发）
# 观察 reclaim 日志：kubectl logs -l app=rca-apiserver | grep -i "reclaim"
```

**问题 3：Notice 投递失败**

```bash
# 现象：Diagnosis 已写入但飞书没有收到通知
# 排查步骤：

# 1. 检查 notice_deliveries 表
SELECT * FROM notice_deliveries WHERE job_id = 'job-123';
# 看 status 是否为 'failed'

# 2. 检查 notice worker 日志
kubectl logs -l app=rca-notice-worker --tail=100 | grep -i "job-123"
# 看是否有 HTTP 错误、JSON 格式错误等

# 3. 检查 channel 配置
SELECT * FROM notice_channels WHERE channel_type = 'feishu';
# 看 webhook_url 是否正确

# 4. 手动重试投递
curl -X POST http://rca-apiserver:8080/v1/notice-deliveries/<delivery_id>/retry
```

### 8.5 技术栈选择的深度对比

#### 8.5.1 为什么选择 Go 做控制面？

**优势**

1. **高性能**：Go 的并发模型（goroutine）天生适合高并发场景
   ```go
   // Go 可以轻松处理 10,000+ 并发请求
   for i := 0; i < 10000; i++ {
       go handleRequest(i)
   }
   ```

2. **强类型**：编译时检查，避免运行时类型错误
   ```go
   // Go 会编译失败
   var count int = "string"  // 编译错误
   ```

3. **丰富的生态**：
   - 数据库：GORM（ORM）、sqlx（原生 SQL）
   - Web 框架：Gin（高性能）、Echo
   - K8s：client-go（官方 K8s 客户端）
   - 分布式：etcd（分布式 KV）、consul（服务发现）

4. **部署简单**：编译成静态二进制，不依赖外部运行时
   ```bash
   GOOS=linux GOARCH=amd64 go build -o rca-apiserver
   # 直接运行，不需要安装 Go runtime
   ```

**劣势**

1. **不适合 LLM 应用**：Go 的 LLM 生态远不如 Python
   - 没有 LangChain、LangGraph 等成熟框架
   - 需要自己实现 LLM 调用、工具编排、图执行

2. **泛型限制**：Go 1.18 才引入泛型，很多库还没有完全适配

#### 8.5.2 为什么选择 Python 做执行面？

**优势**

1. **LLM 生态成熟**：
   - **LangChain**：工具调用、链式调用、Agent 框架
   - **LangGraph**：有状态图执行、循环控制、条件分支
   - **llama-index**：索引构建、检索增强
   - **MCP SDK**：连接 Claude Skills 的标准库

2. **快速迭代**：动态类型、REPL（交互式环境）适合快速原型开发
   ```python
   # Python 可以直接在终端调试
   >>> from langgraph.graph import StateGraph
   >>> graph = StateGraph()
   >>> graph.add_node("router", router_node)
   >>> graph.add_edge("router", "tool_call")
   >>> app = graph.compile()
   >>> result = app.invoke({"input": "分析这个告警"})
   ```

3. **丰富的工具库**：
   - **requests**：HTTP 客户端
   - **pandas**：数据处理
   - **pydantic**：数据验证
   - **fastapi**：如果需要，也可以作为 Web 服务

**劣势**

1. **性能较差**：GIL（全局解释器锁）限制了多线程并发
   ```python
   # Python 多线程无法真正并行
   import threading
   threads = [threading.Thread(target=cpu_intensive_task) for _ in range(4)]
   for t in threads: t.start()
   # 实际仍然是串行执行
   ```

2. **类型系统弱**：运行时类型错误，调试困难
   ```python
   # Python 会运行时崩溃
   count = "string"
   result = count + 1  # 运行时错误
   ```

3. **依赖管理复杂**：`requirements.txt`、虚拟环境、包冲突
   ```bash
   pip install langchain-core==0.2.0
   pip install langgraph==0.1.0
   # 可能出现版本冲突
   ```

#### 8.5.3 为什么不选择单一技术栈？

| 方案 | 优势 | 劣势 | 适用场景 |
|------|------|------|---------|
| **全栈 Go** | 部署简单、性能高、类型安全 | 需要自己实现 LLM 框架，成本高 | 简单的 LLM 应用，不需要复杂工具编排 |
| **全栈 Python** | LLM 生态完整、开发快 | 高并发性能差、类型系统弱 | 原型验证、单机部署、低流量场景 |
| **Go + Python 混合** | 各自发挥优势、独立扩展 | 需要跨进程通信、调试复杂 | **生产级 LLM 应用，高并发 + 复杂编排** |

**混合方案的权衡**

- **开发成本**：需要维护两套技术栈，学习曲线陡峭
- **运维成本**：需要部署和监控两个服务
- **调试成本**：跨进程日志聚合、TraceID 传递

但这些成本被以下收益抵消：
- **可扩展性**：Go 处理高并发，Python 专注复杂执行
- **可靠性**：故障域隔离，Python 崩溃不影响 Go
- **性能**：Go API P99 < 50ms，Python worker 专注执行

**这就是为什么我们选择混合方案**：不是因为"喜欢复杂"，而是因为"简单的方案无法满足生产需求"。

---

## 十、系列后续文章预告

本文解释了控制面与执行面为什么必须拆开，但很多细节没有展开：

| 篇号 | 标题 | 核心主题 |
|------|------|----------|
| 04 | [AIJob 租约与运行时](./04-ai-job-lease-and-runtime.md) | 多实例 worker 下的单 owner 语义与租约机制详解 |
| 05 | [告警治理前置条件](./05-alert-to-incident-governance.md) | 没有问题边界，就没有可信 RCA |
| 06 | [补充通知设计](./06-supplemental-notice-design.md) | 可信度、引用回复与 Incident 可回看 |
| 07 | [Skills、MCP 与 LangGraph](./07-skills-mcp-langgraph-runtime.md) | 知识/能力/流程三层装配 |
| 08 | [第一阶段复盘](./08-phase-one-retrospective.md) | 哪些问题解决了，哪些还只是开始 |

下一篇《[AIJob 租约与运行时](./04-ai-job-lease-and-runtime.md)》将深入讲解租约机制、heartbeat 续租、reclaim 过期 job 的实现细节。

---

*本文代码与实现基于 [aiopsre/rca-api](https://github.com/aiopsre/rca-api) 仓库（分支：`feature/skills-mcp-integration`）。*
