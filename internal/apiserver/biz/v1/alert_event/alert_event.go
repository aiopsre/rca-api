//nolint:gocognit,gocyclo,nestif,nilerr,nilnil,protogetter,modernize,whitespace
package alert_event

//go:generate mockgen -destination mock_alert_event.go -package alert_event github.com/aiopsre/rca-api/internal/apiserver/biz/v1/alert_event AlertEventBiz

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/onexstack/onexstack/pkg/errorsx"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	alertingingest "github.com/aiopsre/rca-api/internal/apiserver/pkg/alerting/ingest"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/audit"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/conversion"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/metrics"
	noticepkg "github.com/aiopsre/rca-api/internal/apiserver/pkg/notice"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/silenceutil"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	"github.com/aiopsre/rca-api/internal/pkg/contextx"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

const (
	alertStatusFiring     = "firing"
	alertStatusResolved   = "resolved"
	alertStatusSuppressed = "suppressed"

	incidentStatusNew           = "new"
	incidentStatusInvestigating = "investigating"
	incidentStatusResolved      = "resolved"
	incidentStatusClosed        = "closed"

	defaultAlertSource   = "alertmanager"
	defaultAlertSeverity = "warning"
	defaultService       = "unknown"
	defaultCluster       = "default"
	defaultNamespace     = "default"
	defaultWorkload      = "unknown"

	defaultListLimit = int64(20)
	maxListLimit     = int64(200)
)

var volatileLabelKeys = map[string]struct{}{
	"pod":          {},
	"instance":     {},
	"endpoint":     {},
	"container_id": {},
	"request_id":   {},
	"trace_id":     {},
	"span_id":      {},
	"ip":           {},
	"node_ip":      {},
}

// AlertEventBiz defines alert-event use-cases.
//
//nolint:interfacebloat // Domain biz surface intentionally aggregates alert-event operations.
type AlertEventBiz interface {
	Ingest(ctx context.Context, rq *v1.IngestAlertEventRequest) (*v1.IngestAlertEventResponse, error)
	IngestByAdapter(ctx context.Context, adapter string, rq *v1.IngestAlertEventRequest) (*v1.IngestAlertEventResponse, error)
	ListCurrent(ctx context.Context, rq *v1.ListCurrentAlertEventsRequest) (*v1.ListCurrentAlertEventsResponse, error)
	ListHistory(ctx context.Context, rq *v1.ListHistoryAlertEventsRequest) (*v1.ListHistoryAlertEventsResponse, error)
	Ack(ctx context.Context, rq *v1.AckAlertEventRequest) (*v1.AckAlertEventResponse, error)
	Close() error

	AlertEventExpansion
}

type AlertEventExpansion interface{}

type alertEventBiz struct {
	store          store.IStore
	ingestPipeline *alertingingest.Pipeline
	rolloutConfig  alertingingest.RolloutConfig
	closeOnce      sync.Once
	closeErr       error
}

var _ AlertEventBiz = (*alertEventBiz)(nil)

func New(store store.IStore) *alertEventBiz {
	runtimeCfg := alertingingest.CurrentRuntimeConfig()
	pipeline := alertingingest.NewDefaultPipeline(runtimeCfg, func(ctx context.Context, fingerprint string) (*time.Time, error) {
		fingerprint = strings.TrimSpace(fingerprint)
		if fingerprint == "" {
			return nil, nil
		}
		current, err := store.AlertEvent().Get(ctx, where.T(ctx).F("current_key", fingerprint))
		if err != nil {
			if errorsx.Is(err, gorm.ErrRecordNotFound) || isTableNotFoundError(err) {
				return nil, nil
			}
			return nil, err
		}
		lastSeen := current.LastSeenAt.UTC()
		return &lastSeen, nil
	})

	return &alertEventBiz{
		store:          store,
		ingestPipeline: pipeline,
		rolloutConfig:  runtimeCfg.Rollout,
	}
}

type ingestInput struct {
	idempotencyKey  string
	fingerprint     string
	dedupKey        string
	source          string
	status          string
	severity        string
	alertName       string
	service         string
	cluster         string
	namespace       string
	workload        string
	startsAt        *time.Time
	endsAt          *time.Time
	lastSeenAt      time.Time
	labelsJSON      *string
	annotationsJSON *string
	generatorURL    *string
	rawEventJSON    *string
}

type ingestOptions struct {
	adapter      string
	applyRollout bool
}

type rolloutDecision struct {
	allowed        bool
	shouldProgress bool
	dropReason     string
}

func (b *alertEventBiz) Ingest(ctx context.Context, rq *v1.IngestAlertEventRequest) (*v1.IngestAlertEventResponse, error) {
	return b.ingest(ctx, rq, ingestOptions{})
}

func (b *alertEventBiz) IngestByAdapter(ctx context.Context, adapter string, rq *v1.IngestAlertEventRequest) (*v1.IngestAlertEventResponse, error) {
	return b.ingest(ctx, rq, ingestOptions{
		adapter:      strings.ToLower(strings.TrimSpace(adapter)),
		applyRollout: true,
	})
}

// Close releases resources held by ingest pipeline backends.
func (b *alertEventBiz) Close() error {
	if b == nil || b.ingestPipeline == nil {
		return nil
	}
	b.closeOnce.Do(func() {
		b.closeErr = b.ingestPipeline.Close()
	})
	return b.closeErr
}

func (b *alertEventBiz) ingest(ctx context.Context, rq *v1.IngestAlertEventRequest, options ingestOptions) (*v1.IngestAlertEventResponse, error) {
	// 记录开始时间和初始化响应指标
	startedAt := time.Now()
	outcome := "ok"
	mergeResult := ""
	isAdapterIngest := strings.TrimSpace(options.adapter) != ""
	adapter := strings.ToLower(strings.TrimSpace(options.adapter))

	// 如果是适配器摄入，记录总摄入量指标
	if isAdapterIngest && metrics.M != nil {
		metrics.M.RecordAlertIngestTotal(adapter)
	}

	// 延迟执行：记录最终的摄入指标
	defer func() {
		if metrics.M != nil {
			metrics.M.RecordAlertEventIngest(ctx, mergeResult, outcome, time.Since(startedAt))
		}
	}()

	// 1. 标准化输入参数
	in, err := normalizeIngestInput(rq)
	if err != nil {
		outcome = "invalid_argument"
		return nil, err
	}

	// 2. 评估灰度发布决策（控制哪些告警可以进入系统）
	rollout := b.evaluateRolloutDecision(in, options)
	if isAdapterIngest && rollout.allowed && metrics.M != nil {
		metrics.M.RecordAlertIngestAllowed(adapter)
	}
	if isAdapterIngest && rollout.dropReason != "" && metrics.M != nil {
		metrics.M.RecordAlertIngestDropped(adapter, rollout.dropReason)
	}

	// 初始化返回结果变量
	var (
		eventID               string                  // 事件ID
		incidentID            string                  // 关联的事件ID
		reused                bool                    // 是否复用了已存在的事件
		incidentCreated       bool                    // 是否创建了新事件
		incidentStatusChanged bool                    // 事件状态是否发生变化
		silenced              bool                    // 是否被静默
		silenceID             string                  // 静默规则ID
		policyDecision        alertingingest.Decision // 策略决策结果
		suppressIncident      bool                    // 是否抑制事件创建
		suppressTimeline      bool                    // 是否抑制时间线记录
	)

	// 3. 在数据库事务中执行核心业务逻辑
	err = b.store.TX(ctx, func(txCtx context.Context) error {
		// 3.1 幂等性检查：如果提供了幂等键，检查是否已存在相同事件
		if in.idempotencyKey != "" {
			existing, getErr := b.store.AlertEvent().Get(txCtx, where.T(txCtx).F("idempotency_key", in.idempotencyKey))
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
			// 处理查询错误（除了未找到记录的情况）
			if getErr != nil && !errorsx.Is(getErr, gorm.ErrRecordNotFound) {
				return errno.ErrAlertEventGetFailed
			}
		}

		// 3.2 静默规则匹配：检查当前是否有匹配的活跃静默规则
		matchedSilence, matchErr := b.matchActiveSilence(txCtx, in)
		if matchErr != nil {
			return matchErr
		}
		if matchedSilence != nil {
			silenced = true
			silenceID = matchedSilence.SilenceID
		}

		// 3.3 执行摄入策略管道评估
		if b.ingestPipeline != nil {
			policyDecision, err = b.ingestPipeline.Evaluate(txCtx, alertingingest.EvaluateInput{
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
		} else if silenced {
			// 如果没有策略管道但已被静默，则默认抑制事件和时间线
			suppressIncident = true
			suppressTimeline = true
		}

		// 3.4 应用灰度发布控制：如果不应该继续处理，则抑制事件创建
		if !rollout.shouldProgress {
			suppressIncident = true
			suppressTimeline = true
		}

		// 3.5 解析关联的事件：根据指纹查找或创建对应的事件
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

		// 3.6 创建历史记录：将当前摄入作为历史事件保存
		history := buildAlertEventModel(in, incidentID, false, nil, strPtr(in.idempotencyKey), silenced, silenceID)
        // 先尝试创建历史记录
        if err := b.store.AlertEvent().Create(txCtx, history); err != nil {
            // 创建失败后，再判断失败的原因
            if in.idempotencyKey != "" && isDuplicateKeyError(err) {
                // 如果是幂等性键重复导致的错误，才去查询已存在的记录
                existing, getErr := b.store.AlertEvent().Get(txCtx, where.T(txCtx).F("idempotency_key", in.idempotencyKey))
                if getErr == nil {
                    // 查询成功，说明是重复请求，重用已有数据
                    reused = true
                    eventID = existing.EventID
                    // ... 重用其他字段
                    mergeResult = "idempotent_reused"
                    return nil
                }
                return errno.ErrAlertEventIdempotencyConflict
            }
            // 如果不是幂等性问题，而是其他创建失败原因，直接返回错误
            return errno.ErrAlertEventIngestFailed
        }
		eventID = history.EventID

		// 3.7 合并到当前活动告警：更新或创建当前活动的告警记录
		mergeResult, err = b.mergeCurrentAlert(txCtx, in, incidentID, silenced, silenceID)
		if err != nil {
			return err
		}
		// 如果被静默，在合并结果前加上静默前缀
		if silenced && mergeResult != "idempotent_reused" {
			mergeResult = "silenced_" + mergeResult
		}

		// 3.8 记录审计日志到事件时间线
		if silenced {
			// 记录告警被静默的事件
			audit.AppendIncidentTimelineIfExists(txCtx, b.store.DB(txCtx), incidentID, "alert_silenced", eventID, map[string]any{
				"event_id":    eventID,
				"fingerprint": in.fingerprint,
				"silence_id":  silenceID,
			})
		}

		if !silenced && !suppressTimeline && incidentID != "" {
			// 记录告警摄入事件
			audit.AppendIncidentTimelineIfExists(txCtx, b.store.DB(txCtx), incidentID, "alert_ingested", eventID, map[string]any{
				"event_id":     eventID,
				"fingerprint":  in.fingerprint,
				"status":       in.status,
				"merge_result": mergeResult,
			})

			// 记录事件创建事件
			if incidentCreated {
				audit.AppendIncidentTimelineIfExists(txCtx, b.store.DB(txCtx), incidentID, "incident_created", incidentID, map[string]any{
					"event_id":    eventID,
					"fingerprint": in.fingerprint,
					"status":      incidentStatusNew,
				})
			}

			// 记录事件状态变更事件
			if incidentStatusChanged {
				audit.AppendIncidentTimelineIfExists(txCtx, b.store.DB(txCtx), incidentID, "incident_status_changed", incidentID, map[string]any{
					"event_id":    eventID,
					"from_status": incidentStatusInvestigating,
					"to_status":   incidentStatusResolved,
				})
			}
		}

		return nil
	})

	// 4. 处理事务执行结果
	if err != nil {
		outcome = "failed"
		return nil, err
	}

	// 5. 更新指标统计
	if reused {
		outcome = "reused"
	}
	if isAdapterIngest && !suppressIncident && metrics.M != nil {
		metrics.M.RecordAlertIngestProgressed(adapter)
	}
	if isAdapterIngest && silenced && metrics.M != nil {
		metrics.M.RecordAlertIngestSilenced(adapter)
	}
	if isAdapterIngest && mergeResult != "" && strings.Contains(mergeResult, "current_") && metrics.M != nil {
		metrics.M.RecordAlertIngestMerged(adapter, mergeResult)
	}
	if isAdapterIngest && incidentCreated && metrics.M != nil {
		metrics.M.RecordAlertIngestNewIncident(adapter)
	}

	// 6. 触发通知：如果创建了新事件且未被静默，发送事件创建通知
	if incidentCreated && !silenced && incidentID != "" {
		incidentModel, getErr := b.store.Incident().Get(ctx, where.T(ctx).F("incident_id", incidentID))
		if getErr != nil {
			slog.WarnContext(ctx, "notice incident_created skipped: incident lookup failed",
				"incident_id", incidentID,
				"error", getErr,
			)
		} else {
			noticepkg.DispatchBestEffort(ctx, b.store, noticepkg.DispatchRequest{
				EventType:  noticepkg.EventTypeIncidentCreated,
				Incident:   incidentModel,
				OccurredAt: time.Now().UTC(),
			})
		}
	}

	// 7. 记录详细的摄入日志
	slog.InfoContext(ctx, "alert event ingested",
		"request_id", contextx.RequestID(ctx),
		"incident_id", incidentID,
		"event_id", eventID,
		"job_id", "",
		"tool_call_id", "",
		"datasource_id", "",
		"fingerprint", in.fingerprint,
		"status", in.status,
		"reused", reused,
		"merge_result", mergeResult,
		"idempotency_key", in.idempotencyKey,
		"silenced", silenced,
		"silence_id", silenceID,
		"adapter", adapter,
		"rollout_should_progress", rollout.shouldProgress,
		"rollout_drop_reason", rollout.dropReason,
		"policy_decision", policyDecision.Decision,
		"policy_backend", policyDecision.Backend,
		"policy_deduped", policyDecision.Deduped,
		"policy_burst_suppressed", policyDecision.BurstSuppressed,
	)

	// 8. 构造并返回响应
	resp := &v1.IngestAlertEventResponse{
		EventID:     eventID,
		Fingerprint: in.fingerprint,
		Status:      in.status,
		Reused:      reused,
		MergeResult: mergeResult,
		Silenced:    silenced,
	}
	if incidentID != "" {
		resp.IncidentID = &incidentID
	}
	if silenceID != "" {
		resp.SilenceID = &silenceID
	}
	return resp, nil
}

/*
 * evaluateRolloutDecision 评估告警事件的发布决策
 * 该函数用于控制哪些告警事件应该被处理，哪些应该被丢弃或观察
 * 主要用于灰度发布或分阶段推出新功能时的流量控制
 *
 * 参数:
 * - in: 告警事件输入数据，包含命名空间、服务等信息
 * - options: 操作选项，包含是否应用发布控制的标志
 *
 * 返回:
 * - rolloutDecision: 包含以下字段的决策结果
 *   - allowed: 是否允许该告警事件通过
 *   - shouldProgress: 是否应该继续处理该告警事件
 *   - dropReason: 如果被丢弃，说明丢弃原因
 */
func (b *alertEventBiz) evaluateRolloutDecision(in *ingestInput, options ingestOptions) rolloutDecision {
	// 初始化默认决策：允许且继续处理
	decision := rolloutDecision{
		allowed:        true,  // 默认允许通过
		shouldProgress: true,  // 默认继续处理
		dropReason:     "",    // 默认无丢弃原因
	}
	// 如果未启用发布控制，则直接返回默认决策（允许并继续处理）
	if !options.applyRollout {
		return decision
	}
	// 获取发布配置
	cfg := b.rolloutConfig
	// 应用默认配置值
	cfg.ApplyDefaults()
	// 如果发布控制未启用，则直接返回默认决策
	if !cfg.Enabled {
		return decision
	}

	// 根据配置判断当前告警事件是否在允许范围内
	decision.allowed = rolloutAllowMatched(cfg, in.namespace, in.service)
	// 根据发布模式决定如何处理
	switch cfg.Mode {
	case alertingingest.RolloutModeObserve: // 观察模式：记录但不实际处理
		decision.shouldProgress = false  // 不继续处理
		decision.dropReason = "observe_mode"  // 设置丢弃原因为观察模式

	case alertingingest.RolloutModeEnforce: // 强制执行模式：根据规则严格控制
		if !decision.allowed {  // 如果不在允许范围内
			decision.shouldProgress = false  // 不继续处理
			decision.dropReason = "not_allowed"  // 设置丢弃原因为不允许
		}
	}
	return decision
}

func (b *alertEventBiz) ListCurrent(ctx context.Context, rq *v1.ListCurrentAlertEventsRequest) (*v1.ListCurrentAlertEventsResponse, error) {
	limit := normalizeListLimit(rq.GetLimit())
	whr := where.T(ctx).O(int(rq.GetOffset())).L(int(limit)).F("is_current", true)

	if v := trimOptional(rq.Severity); v != "" {
		whr = whr.F("severity", normalizeSeverity(v))
	}
	if v := trimOptional(rq.Service); v != "" {
		whr = whr.F("service", v)
	}
	if v := trimOptional(rq.Cluster); v != "" {
		whr = whr.F("cluster", v)
	}
	if v := trimOptional(rq.Namespace); v != "" {
		whr = whr.F("namespace", v)
	}
	if v := trimOptional(rq.Fingerprint); v != "" {
		whr = whr.F("fingerprint", v)
	}
	if v := trimOptional(rq.Status); v != "" {
		whr = whr.F("status", strings.ToLower(v))
	}
	if rq.LastSeenStart != nil {
		whr = whr.C(clause.Expr{SQL: "last_seen_at >= ?", Vars: []any{rq.GetLastSeenStart().AsTime().UTC()}})
	}
	if rq.LastSeenEnd != nil {
		whr = whr.C(clause.Expr{SQL: "last_seen_at <= ?", Vars: []any{rq.GetLastSeenEnd().AsTime().UTC()}})
	}

	total, list, err := b.store.AlertEvent().List(ctx, whr)
	if err != nil {
		return nil, errno.ErrAlertEventListFailed
	}

	out := make([]*v1.AlertEvent, 0, len(list))
	for _, item := range list {
		out = append(out, conversion.AlertEventMToAlertEventV1(item))
	}
	return &v1.ListCurrentAlertEventsResponse{TotalCount: total, Events: out}, nil
}

func (b *alertEventBiz) ListHistory(ctx context.Context, rq *v1.ListHistoryAlertEventsRequest) (*v1.ListHistoryAlertEventsResponse, error) {
	limit := normalizeListLimit(rq.GetLimit())
	whr := where.T(ctx).O(int(rq.GetOffset())).L(int(limit)).F("is_current", false)

	if v := trimOptional(rq.Severity); v != "" {
		whr = whr.F("severity", normalizeSeverity(v))
	}
	if v := trimOptional(rq.Service); v != "" {
		whr = whr.F("service", v)
	}
	if v := trimOptional(rq.Cluster); v != "" {
		whr = whr.F("cluster", v)
	}
	if v := trimOptional(rq.Namespace); v != "" {
		whr = whr.F("namespace", v)
	}
	if v := trimOptional(rq.Fingerprint); v != "" {
		whr = whr.F("fingerprint", v)
	}
	if v := trimOptional(rq.Status); v != "" {
		whr = whr.F("status", strings.ToLower(v))
	}
	if v := trimOptional(rq.IncidentID); v != "" {
		whr = whr.F("incident_id", v)
	}
	if rq.LastSeenStart != nil {
		whr = whr.C(clause.Expr{SQL: "last_seen_at >= ?", Vars: []any{rq.GetLastSeenStart().AsTime().UTC()}})
	}
	if rq.LastSeenEnd != nil {
		whr = whr.C(clause.Expr{SQL: "last_seen_at <= ?", Vars: []any{rq.GetLastSeenEnd().AsTime().UTC()}})
	}

	total, list, err := b.store.AlertEvent().List(ctx, whr)
	if err != nil {
		return nil, errno.ErrAlertEventListFailed
	}

	out := make([]*v1.AlertEvent, 0, len(list))
	for _, item := range list {
		out = append(out, conversion.AlertEventMToAlertEventV1(item))
	}
	return &v1.ListHistoryAlertEventsResponse{TotalCount: total, Events: out}, nil
}

func (b *alertEventBiz) Ack(ctx context.Context, rq *v1.AckAlertEventRequest) (*v1.AckAlertEventResponse, error) {
	eventID := strings.TrimSpace(rq.GetEventID())
	if eventID == "" {
		return nil, errorsx.ErrInvalidArgument
	}

	ackedAt := time.Now().UTC()
	ackedBy := normalizeAckedBy(ctx, rq.AckedBy)
	incidentID := ""

	err := b.store.TX(ctx, func(txCtx context.Context) error {
		event, err := b.store.AlertEvent().Get(txCtx, where.T(txCtx).F("event_id", eventID))
		if err != nil {
			return toAlertEventGetError(err)
		}

		if event.AckedAt != nil {
			ackedAt = event.AckedAt.UTC()
			ackedBy = derefString(event.AckedBy)
			incidentID = derefString(event.IncidentID)
			if ackedBy == "" {
				ackedBy = normalizeAckedBy(ctx, rq.AckedBy)
			}
			return nil
		}

		event.AckedAt = &ackedAt
		event.AckedBy = &ackedBy
		if err := b.store.AlertEvent().Update(txCtx, event); err != nil {
			return errno.ErrAlertEventAckFailed
		}

		incidentID = derefString(event.IncidentID)
		if incidentID != "" {
			audit.AppendIncidentTimelineIfExists(txCtx, b.store.DB(txCtx), incidentID, "alert_acked", eventID, map[string]any{
				"event_id": eventID,
				"acked_by": ackedBy,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	slog.InfoContext(ctx, "alert event acknowledged",
		"request_id", contextx.RequestID(ctx),
		"incident_id", incidentID,
		"event_id", eventID,
		"job_id", "",
		"tool_call_id", "",
		"datasource_id", "",
		"acked_by", ackedBy,
	)

	resp := &v1.AckAlertEventResponse{
		EventID: eventID,
		AckedAt: timestamppb.New(ackedAt),
		AckedBy: ackedBy,
	}
	if incidentID != "" {
		resp.IncidentID = &incidentID
	}
	return resp, nil
}

// Merge policy (deterministic, strategy A):
// one fingerprint can bind only one non-closed incident via incidents.active_fingerprint_key unique index.
// When incident is resolved/closed, active_fingerprint_key is cleared so the next firing ingest creates a new incident.
func (b *alertEventBiz) resolveIncidentForIngest(ctx context.Context, in *ingestInput) (*model.IncidentM, bool, bool, error) {
	incident, err := b.store.Incident().Get(ctx, where.T(ctx).F("active_fingerprint_key", in.fingerprint))
	if err == nil {
		if isIncidentClosed(incident.Status) {
			incident.ActiveFingerprintKey = nil
			if updateErr := b.store.Incident().Update(ctx, incident); updateErr != nil {
				return nil, false, false, errno.ErrIncidentUpdateFailed
			}
		} else {
			statusChanged := false
			incident.Severity = in.severity
			if in.startsAt != nil && (incident.StartAt == nil || in.startsAt.Before(*incident.StartAt)) {
				incident.StartAt = cloneTimePtr(in.startsAt)
			}
			if in.status == alertStatusResolved {
				incident.Status = incidentStatusResolved
				incident.ActiveFingerprintKey = nil
				if in.endsAt != nil {
					incident.EndAt = cloneTimePtr(in.endsAt)
				} else {
					resolvedAt := in.lastSeenAt
					incident.EndAt = &resolvedAt
				}
				statusChanged = true
			}
			if updateErr := b.store.Incident().Update(ctx, incident); updateErr != nil {
				return nil, false, false, errno.ErrIncidentUpdateFailed
			}
			return incident, false, statusChanged, nil
		}
	}
	if err != nil && !errorsx.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, false, errno.ErrIncidentGetFailed
	}

	if in.status == alertStatusResolved {
		return nil, false, false, nil
	}

	incident = &model.IncidentM{
		TenantID:             "default",
		Cluster:              in.cluster,
		Namespace:            in.namespace,
		WorkloadKind:         "Deployment",
		WorkloadName:         in.workload,
		Service:              in.service,
		Environment:          "prod",
		Source:               in.source,
		Severity:             in.severity,
		Status:               incidentStatusNew,
		StartAt:              cloneTimePtr(in.startsAt),
		RCAStatus:            "pending",
		ActionStatus:         "none",
		Fingerprint:          strPtr(in.fingerprint),
		ActiveFingerprintKey: strPtr(in.fingerprint),
		LabelsJSON:           cloneStringPtr(in.labelsJSON),
		AnnotationsJSON:      cloneStringPtr(in.annotationsJSON),
	}
	if in.alertName != "" {
		incident.AlertName = strPtr(in.alertName)
	}

	if createErr := b.store.Incident().Create(ctx, incident); createErr != nil {
		if isDuplicateKeyError(createErr) {
			existing, getErr := b.retryGetIncidentAfterDuplicateCreate(ctx, in.fingerprint)
			if getErr == nil {
				return existing, false, false, nil
			}
			slog.WarnContext(ctx, "incident create duplicate fallback read failed",
				"request_id", contextx.RequestID(ctx),
				"incident_id", "",
				"event_id", "",
				"job_id", "",
				"tool_call_id", "",
				"datasource_id", "",
				"fingerprint", in.fingerprint,
				"error", getErr,
			)
		}
		return nil, false, false, errno.ErrIncidentCreateFailed
	}

	return incident, true, false, nil
}

/*
 * mergeCurrentAlert 将告警事件合并到当前活动告警记录
 * 该函数负责维护当前活动告警的最新状态，处理告警的激活、更新和解决
 * 每个fingerprint对应一条当前活动告警记录（current_key字段唯一索引）
 *
 * 参数:
 * - ctx: 上下文
 * - in: 告警事件输入数据
 * - incidentID: 关联的事件单ID
 * - silenced: 是否被静默
 * - silenceID: 静默规则ID
 *
 * 返回:
 * - string: 合并操作的结果类型
 * - error: 错误信息
 *
 * 返回值说明:
 * - "history_appended": 仅追加历史记录（告警已解决但无当前活动记录）
 * - "current_resolved": 当前告警已解决（更新为历史记录）
 * - "current_created": 创建新的当前活动告警记录
 * - "current_updated": 更新现有的当前活动告警记录
 * - "current_resolved" + "current_created" = "current_resolved_created" (复合操作)
 */
func (b *alertEventBiz) mergeCurrentAlert(ctx context.Context, in *ingestInput, incidentID string, silenced bool, silenceID string) (string, error) {
	// 1. 尝试获取当前活动告警记录（通过fingerprint作为current_key）
	current, err := b.store.AlertEvent().Get(ctx, where.T(ctx).F("current_key", in.fingerprint))
	if err != nil && !errorsx.Is(err, gorm.ErrRecordNotFound) {
		return "", errno.ErrAlertEventGetFailed
	}

	// 2. 处理告警已解决的情况
	if in.status == alertStatusResolved {
		// 如果没有当前活动告警记录，说明这是首次解决，只需追加历史记录
		if err != nil {
			return "history_appended", nil
		}
		// 更新当前活动告警记录为已解决状态
		applyIngestOnCurrent(current, in, incidentID, silenced, silenceID)
		current.IsCurrent = false  // 标记为非当前记录
		current.CurrentKey = nil   // 清除current_key，使其不再作为当前活动告警
		if in.endsAt == nil {
			resolvedAt := in.lastSeenAt
			current.EndsAt = &resolvedAt  // 设置结束时间为最后看到时间
		}
		if updateErr := b.store.AlertEvent().Update(ctx, current); updateErr != nil {
			return "", errno.ErrAlertEventIngestFailed
		}
		return "current_resolved", nil  // 返回当前告警已解决
	}

	// 3. 处理告警激活/更新的情况
	// 3.1 如果没有当前活动告警记录，需要创建新的
	if err != nil {
		curKey := in.fingerprint
		obj := buildAlertEventModel(in, incidentID, true, &curKey, nil, silenced, silenceID)
		if createErr := b.store.AlertEvent().Create(ctx, obj); createErr != nil {
			// 如果创建失败是由于重复键冲突（并发创建），尝试获取已存在的记录并更新
			if isDuplicateKeyError(createErr) {
				existing, getErr := b.store.AlertEvent().Get(ctx, where.T(ctx).F("current_key", in.fingerprint))
				if getErr != nil {
					return "", errno.ErrAlertEventIngestFailed
				}
				applyIngestOnCurrent(existing, in, incidentID, silenced, silenceID)
				if updateErr := b.store.AlertEvent().Update(ctx, existing); updateErr != nil {
					return "", errno.ErrAlertEventIngestFailed
				}
				return "current_updated", nil
			}
			return "", errno.ErrAlertEventIngestFailed
		}
		return "current_created", nil  // 返回当前告警已创建
	}

	// 3.2 如果存在当前活动告警记录，直接更新
	applyIngestOnCurrent(current, in, incidentID, silenced, silenceID)
	if updateErr := b.store.AlertEvent().Update(ctx, current); updateErr != nil {
		return "", errno.ErrAlertEventIngestFailed
	}
	return "current_updated", nil  // 返回当前告警已更新
}

func normalizeIngestInput(rq *v1.IngestAlertEventRequest) (*ingestInput, error) {
	if rq == nil {
		return nil, errorsx.ErrInvalidArgument
	}

	status := strings.ToLower(strings.TrimSpace(rq.GetStatus()))
	switch status {
	case alertStatusFiring, alertStatusResolved, alertStatusSuppressed:
	default:
		return nil, errorsx.ErrInvalidArgument
	}

	now := time.Now().UTC()
	lastSeenAt := now
	if rq.LastSeenAt != nil {
		lastSeenAt = rq.GetLastSeenAt().AsTime().UTC()
	}

	var startsAt *time.Time
	if rq.StartsAt != nil {
		t := rq.GetStartsAt().AsTime().UTC()
		startsAt = &t
	}
	var endsAt *time.Time
	if rq.EndsAt != nil {
		t := rq.GetEndsAt().AsTime().UTC()
		endsAt = &t
	}
	if status == alertStatusResolved && endsAt == nil {
		t := lastSeenAt
		endsAt = &t
	}
	if startsAt != nil && endsAt != nil && startsAt.After(*endsAt) {
		return nil, errorsx.ErrInvalidArgument
	}

	labelsJSON := trimOptionalPtr(rq.LabelsJSON)
	annotationsJSON := trimOptionalPtr(rq.AnnotationsJSON)
	generatorURL := trimOptionalPtr(rq.GeneratorURL)
	rawEventJSON := trimOptionalPtr(rq.RawEventJSON)
	labels := parseLabelsMap(labelsJSON)

	alertName := firstNonEmpty(trimOptional(rq.AlertName), labels["alertname"])
	service := firstNonEmpty(trimOptional(rq.Service), labels["service"], labels["app"], defaultService)
	cluster := firstNonEmpty(trimOptional(rq.Cluster), labels["cluster"], labels["kubernetes_cluster"], defaultCluster)
	namespace := firstNonEmpty(trimOptional(rq.Namespace), labels["namespace"], labels["kubernetes_namespace"], defaultNamespace)
	workload := firstNonEmpty(trimOptional(rq.Workload), labels["workload"], labels["deployment"], labels["statefulset"], defaultWorkload)

	fingerprint := strings.TrimSpace(rq.GetFingerprint())
	if fingerprint == "" {
		fingerprint = deriveFingerprint(labels, service, cluster, namespace, workload, alertName)
	}
	if fingerprint == "" {
		return nil, errorsx.ErrInvalidArgument
	}

	dedupKey := strings.TrimSpace(rq.GetDedupKey())
	if dedupKey == "" {
		dedupKey = fingerprint
	}

	source := strings.ToLower(strings.TrimSpace(rq.GetSource()))
	if source == "" {
		source = defaultAlertSource
	}

	return &ingestInput{
		idempotencyKey:  trimOptional(rq.IdempotencyKey),
		fingerprint:     fingerprint,
		dedupKey:        dedupKey,
		source:          source,
		status:          status,
		severity:        normalizeSeverity(strings.TrimSpace(rq.GetSeverity())),
		alertName:       alertName,
		service:         service,
		cluster:         cluster,
		namespace:       namespace,
		workload:        workload,
		startsAt:        startsAt,
		endsAt:          endsAt,
		lastSeenAt:      lastSeenAt,
		labelsJSON:      labelsJSON,
		annotationsJSON: annotationsJSON,
		generatorURL:    generatorURL,
		rawEventJSON:    rawEventJSON,
	}, nil
}

func buildAlertEventModel(
	in *ingestInput,
	incidentID string,
	isCurrent bool,
	currentKey *string,
	idempotencyKey *string,
	silenced bool,
	silenceID string,
) *model.AlertEventM {
	obj := &model.AlertEventM{
		IncidentID:      nil,
		Fingerprint:     in.fingerprint,
		DedupKey:        in.dedupKey,
		Source:          in.source,
		Status:          in.status,
		Severity:        in.severity,
		AlertName:       strPtr(in.alertName),
		Service:         strPtr(in.service),
		Cluster:         strPtr(in.cluster),
		Namespace:       strPtr(in.namespace),
		Workload:        strPtr(in.workload),
		StartsAt:        cloneTimePtr(in.startsAt),
		EndsAt:          cloneTimePtr(in.endsAt),
		LastSeenAt:      in.lastSeenAt,
		LabelsJSON:      cloneStringPtr(in.labelsJSON),
		AnnotationsJSON: cloneStringPtr(in.annotationsJSON),
		GeneratorURL:    cloneStringPtr(in.generatorURL),
		RawEventJSON:    cloneStringPtr(in.rawEventJSON),
		IsCurrent:       isCurrent,
		CurrentKey:      cloneStringPtr(currentKey),
		IdempotencyKey:  cloneStringPtr(idempotencyKey),
		IsSilenced:      silenced,
		SilenceID:       strPtr(silenceID),
	}
	if incidentID != "" {
		obj.IncidentID = strPtr(incidentID)
	}
	return obj
}

/*
 * applyIngestOnCurrent 将告警事件数据应用到当前活动告警记录
 * 该函数用于更新当前活动告警的字段，使其反映最新的告警状态
 * 通常在创建新当前记录或更新现有当前记录时调用
 *
 * 参数:
 * - current: 当前活动告警记录（将被更新）
 * - in: 告警事件输入数据（作为数据源）
 * - incidentID: 关联的事件单ID
 * - silenced: 是否被静默
 * - silenceID: 静默规则ID
 *
 * 更新逻辑:
 * - 基础字段：直接从 in 复制
 * - 时间字段：startsAt 取最早值，lastSeenAt 取最新值
 * - 状态字段：非 resolved 状态时保持 current 标记和 current_key
 * - 元数据：清除 idempotency_key，设置静默相关字段
 */
func applyIngestOnCurrent(current *model.AlertEventM, in *ingestInput, incidentID string, silenced bool, silenceID string) {
	// 1. 复制基础告警字段
	current.Fingerprint = in.fingerprint
	current.DedupKey = in.dedupKey
	current.Source = in.source
	current.Status = in.status
	current.Severity = in.severity
	current.AlertName = strPtr(in.alertName)
	current.Service = strPtr(in.service)
	current.Cluster = strPtr(in.cluster)
	current.Namespace = strPtr(in.namespace)
	current.Workload = strPtr(in.workload)

	// 2. 处理时间字段
	// startsAt: 取最早的时间（告警首次触发时间）
	if in.startsAt != nil {
		if current.StartsAt == nil || in.startsAt.Before(*current.StartsAt) {
			current.StartsAt = cloneTimePtr(in.startsAt)
		}
	}
	// endsAt: 如果提供了结束时间则设置，否则根据状态决定
	if in.endsAt != nil {
		current.EndsAt = cloneTimePtr(in.endsAt)
	} else if in.status != alertStatusResolved {
		current.EndsAt = nil  // 非 resolved 状态时，清除结束时间
	}
	// lastSeenAt: 取最新的时间（最后看到时间）
	if in.lastSeenAt.After(current.LastSeenAt) {
		current.LastSeenAt = in.lastSeenAt
	}

	// 3. 复制标签和注解
	current.LabelsJSON = cloneStringPtr(in.labelsJSON)
	current.AnnotationsJSON = cloneStringPtr(in.annotationsJSON)
	current.GeneratorURL = cloneStringPtr(in.generatorURL)
	current.RawEventJSON = cloneStringPtr(in.rawEventJSON)

	// 4. 处理当前活动状态
	if in.status != alertStatusResolved {
		current.IsCurrent = true
		current.CurrentKey = strPtr(in.fingerprint)  // 设置 current_key 以便查询
	}

	// 5. 处理幂等性和静默相关字段
	current.IdempotencyKey = nil  // 当前记录不使用幂等性键
	current.IsSilenced = silenced
	current.SilenceID = strPtr(silenceID)

	// 6. 处理事件单关联
	if incidentID != "" {
		current.IncidentID = strPtr(incidentID)  // 关联事件单
	} else if silenced {
		// 静默的当前记录不关联事件单（避免影响事件单进度）
		current.IncidentID = nil
	}
}

func normalizeSeverity(raw string) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	switch v {
	case "critical", "p0", "high":
		return "critical"
	case "warning", "warn", "p1", "medium":
		return "warning"
	case "info", "p2", "low":
		return "info"
	case "":
		return defaultAlertSeverity
	default:
		return v
	}
}

func deriveFingerprint(labels map[string]string, service string, cluster string, namespace string, workload string, alertName string) string {
	stable := make(map[string]string, len(labels)+5)
	for k, v := range labels {
		key := strings.ToLower(strings.TrimSpace(k))
		val := strings.TrimSpace(v)
		if key == "" || val == "" {
			continue
		}
		if _, ok := volatileLabelKeys[key]; ok {
			continue
		}
		stable[key] = val
	}
	if _, ok := stable["service"]; !ok && service != "" {
		stable["service"] = service
	}
	if _, ok := stable["cluster"]; !ok && cluster != "" {
		stable["cluster"] = cluster
	}
	if _, ok := stable["namespace"]; !ok && namespace != "" {
		stable["namespace"] = namespace
	}
	if _, ok := stable["workload"]; !ok && workload != "" {
		stable["workload"] = workload
	}
	if _, ok := stable["alertname"]; !ok && alertName != "" {
		stable["alertname"] = alertName
	}
	if len(stable) == 0 {
		return ""
	}

	keys := make([]string, 0, len(stable))
	for k := range stable {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	b := strings.Builder{}
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(stable[k])
		b.WriteByte('\n')
	}

	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:16])
}

func parseLabelsMap(raw *string) map[string]string {
	out := map[string]string{}
	if raw == nil {
		return out
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(*raw), &decoded); err != nil {
		return out
	}
	for k, v := range decoded {
		key := strings.ToLower(strings.TrimSpace(k))
		if key == "" || v == nil {
			continue
		}
		if s, ok := v.(string); ok {
			out[key] = strings.TrimSpace(s)
			continue
		}
		encoded, err := json.Marshal(v)
		if err != nil {
			continue
		}
		out[key] = strings.TrimSpace(string(encoded))
	}
	return out
}

func toAlertEventGetError(err error) error {
	if errorsx.Is(err, gorm.ErrRecordNotFound) {
		return errno.ErrAlertEventNotFound
	}
	return errno.ErrAlertEventGetFailed
}

func normalizeAckedBy(ctx context.Context, ackedBy *string) string {
	if v := trimOptional(ackedBy); v != "" {
		return v
	}
	if u := strings.TrimSpace(contextx.Username(ctx)); u != "" {
		return "user:" + u
	}
	if uid := strings.TrimSpace(contextx.UserID(ctx)); uid != "" {
		return "user:" + uid
	}
	return "system"
}

func normalizeListLimit(limit int64) int64 {
	if limit <= 0 {
		return defaultListLimit
	}
	if limit > maxListLimit {
		return maxListLimit
	}
	return limit
}

func (b *alertEventBiz) matchActiveSilence(ctx context.Context, in *ingestInput) (*model.SilenceM, error) {
	active, err := b.store.Silence().ListActive(ctx, in.namespace, time.Now().UTC())
	if err != nil {
		if isTableNotFoundError(err) {
			return nil, nil
		}
		return nil, errno.ErrSilenceListFailed
	}
	if len(active) == 0 {
		return nil, nil
	}

	attrs := map[string]string{
		"fingerprint":  in.fingerprint,
		"service":      in.service,
		"workloadkind": silenceutil.DefaultWorkloadKind,
		"workloadname": in.workload,
		"severity":     in.severity,
	}
	for _, candidate := range active {
		matchers, decodeErr := silenceutil.DecodeMatchers(candidate.MatchersJSON)
		if decodeErr != nil || len(matchers) == 0 {
			continue
		}
		if silenceutil.MatchesAll(matchers, attrs) {
			return candidate, nil
		}
	}
	return nil, nil
}

func (b *alertEventBiz) retryGetIncidentAfterDuplicateCreate(ctx context.Context, fingerprint string) (*model.IncidentM, error) {
	const (
		maxAttempts = 6
		backoff     = 20 * time.Millisecond
	)
	//nolint:contextcheck // Detach from TX-scoped context to avoid stale readback after duplicate-key races.
	readbackCtx := context.Background()

	return retryIncidentDuplicateReadback(ctx, maxAttempts, backoff, func() (*model.IncidentM, error) {
		return b.store.Incident().Get(readbackCtx, where.T(ctx).F("active_fingerprint_key", fingerprint))
	})
}

func retryIncidentDuplicateReadback(ctx context.Context, attempts int, backoff time.Duration, readFn func() (*model.IncidentM, error)) (*model.IncidentM, error) {
	attempts, backoff = normalizeDuplicateReadbackPolicy(attempts, backoff)

	for i := range attempts {
		incident, err := readFn()
		if err == nil {
			return incident, nil
		}
		if !errorsx.Is(err, gorm.ErrRecordNotFound) || i == attempts-1 {
			return nil, err
		}
		if waitErr := waitDuplicateReadback(ctx, backoff); waitErr != nil {
			return nil, waitErr
		}
	}

	return nil, gorm.ErrRecordNotFound
}

func normalizeDuplicateReadbackPolicy(attempts int, backoff time.Duration) (int, time.Duration) {
	if attempts <= 0 {
		attempts = 1
	}
	if backoff < 0 {
		backoff = 0
	}

	return attempts, backoff
}

func waitDuplicateReadback(ctx context.Context, backoff time.Duration) error {
	if backoff == 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(backoff):
		return nil
	}
}

func isDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "duplicate") || strings.Contains(lower, "unique constraint")
}

func isTableNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "no such table") || strings.Contains(lower, "doesn't exist")
}

func isIncidentClosed(status string) bool {
	s := strings.ToLower(strings.TrimSpace(status))
	return s == incidentStatusResolved || s == incidentStatusClosed
}

func trimOptional(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

func trimOptionalPtr(v *string) *string {
	if v == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*v)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func cloneStringPtr(v *string) *string {
	if v == nil {
		return nil
	}
	value := *v
	return &value
}

func cloneTimePtr(v *time.Time) *time.Time {
	if v == nil {
		return nil
	}
	value := v.UTC()
	return &value
}

func strPtr(v string) *string {
	trimmed := strings.TrimSpace(v)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func derefString(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func rolloutAllowMatched(cfg alertingingest.RolloutConfig, namespace string, service string) bool {
	nsAllowed := allowListMatched(cfg.AllowedNamespaces, namespace)
	svcAllowed := allowListMatched(cfg.AllowedServices, service)
	return nsAllowed && svcAllowed
}

func allowListMatched(allowList []string, value string) bool {
	if len(allowList) == 0 {
		return true
	}
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return false
	}
	for _, item := range allowList {
		if normalized == strings.ToLower(strings.TrimSpace(item)) {
			return true
		}
	}
	return false
}
