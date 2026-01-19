package incident

//go:generate mockgen -destination mock_incident.go -package incident github.com/aiopsre/rca-api/internal/apiserver/biz/v1/incident IncidentBiz

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jinzhu/copier"
	"github.com/onexstack/onexstack/pkg/errorsx"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	aijobbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/ai_job"
	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/audit"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/conversion"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	"github.com/aiopsre/rca-api/internal/pkg/contextx"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
	apiv1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

const (
	defaultOperatorListLimit = int64(20)
	maxOperatorListLimit     = int64(200)
	defaultOperatorActor     = "system"

	maxActionActorLen       = 128
	maxActionTypeLen        = 64
	maxActionSummaryLen     = 256
	maxActionDetailsJSONLen = 8 * 1024

	maxVerificationSourceLen     = 64
	maxVerificationToolLen       = 128
	maxVerificationObservedLen   = 512
	maxVerificationParamsJSONLen = 2 * 1024

	operatorWarningRedacted  = "REDACTION_APPLIED"
	operatorWarningTruncated = "PAYLOAD_TRUNCATED"
)

// IncidentBiz 定义 incident 相关业务能力（先最小：Create）
//
// 参考 miniblog 的 PostBiz：入参是 proto request，返回 proto response。:contentReference[oaicite:4]{index=4}
//
//nolint:interfacebloat // Incident biz intentionally groups CRUD + query helpers.
type IncidentBiz interface {
	Create(ctx context.Context, rq *apiv1.CreateIncidentRequest) (*apiv1.CreateIncidentResponse, error)
	Update(ctx context.Context, rq *apiv1.UpdateIncidentRequest) (*apiv1.UpdateIncidentResponse, error)
	Delete(ctx context.Context, rq *apiv1.DeleteIncidentRequest) (*apiv1.DeleteIncidentResponse, error)
	Get(ctx context.Context, rq *apiv1.GetIncidentRequest) (*apiv1.GetIncidentResponse, error)
	List(ctx context.Context, rq *apiv1.ListIncidentRequest) (*apiv1.ListIncidentResponse, error)
	CreateAction(ctx context.Context, rq *apiv1.CreateIncidentActionRequest) (*apiv1.CreateIncidentActionResponse, error)
	ListActions(ctx context.Context, rq *apiv1.ListIncidentActionsRequest) (*apiv1.ListIncidentActionsResponse, error)
	CreateVerificationRun(ctx context.Context, rq *apiv1.CreateIncidentVerificationRunRequest) (*apiv1.CreateIncidentVerificationRunResponse, error)
	ListVerificationRuns(ctx context.Context, rq *apiv1.ListIncidentVerificationRunsRequest) (*apiv1.ListIncidentVerificationRunsResponse, error)
	TriggerScheduledRun(ctx context.Context, rq *TriggerScheduledRunRequest) (*TriggerScheduledRunResponse, error)
	Search(ctx context.Context, rq *SearchIncidentsRequest) (*SearchIncidentsResponse, error)
	ListTimeline(ctx context.Context, rq *ListIncidentTimelineRequest) (*ListIncidentTimelineResponse, error)

	IncidentExpansion
}

// IncidentExpansion 预留扩展方法（对齐 miniblog 的写法）。:contentReference[oaicite:5]{index=5}
//
//nolint:modernize // Keep explicit empty interface as placeholder expansion point.
type IncidentExpansion interface{}

// incidentBiz 是 IncidentBiz 的实现
type incidentBiz struct {
	store       store.IStore
	runAIJobBiz aijobbiz.AIJobBiz
}

type SearchIncidentsRequest struct {
	Offset        int64
	Limit         int64
	Namespace     *string
	Service       *string
	Severity      *string
	Status        *string
	RCAStatus     *string
	RootCauseType *string
	Q             *string
	TimeFrom      *time.Time
	TimeTo        *time.Time
}

type SearchIncidentsResponse struct {
	TotalCount int64
	Incidents  []*apiv1.Incident
}

type ListIncidentTimelineRequest struct {
	IncidentID string
	Offset     int64
	Limit      int64
}

type IncidentTimelineEvent struct {
	EventType string
	RefID     string
	Detail    string
	CreatedAt time.Time
}

type ListIncidentTimelineResponse struct {
	TotalCount int64
	Events     []*IncidentTimelineEvent
}

var _ IncidentBiz = (*incidentBiz)(nil)

// New 创建 incidentBiz 实例（对齐 miniblog New(store) 风格）。:contentReference[oaicite:6]{index=6}
func New(store store.IStore) *incidentBiz {
	return &incidentBiz{
		store:       store,
		runAIJobBiz: aijobbiz.New(store),
	}
}

// Create 创建事件单：把 CreateIncidentRequest 映射到 model.IncidentM，然后落库。
// IncidentID 由 model.AfterCreate hook 自动生成（incident-xxxxxx）。:contentReference[oaicite:7]{index=7}
//
//nolint:gocyclo // Default normalization remains intentionally explicit.
func (b *incidentBiz) Create(ctx context.Context, rq *apiv1.CreateIncidentRequest) (*apiv1.CreateIncidentResponse, error) {
	var m model.IncidentM

	// 1) 复制字段（要求 proto 字段名能映射到 struct 字段名：Service/Namespace/WorkloadKind/WorkloadName/Severity...）
	_ = copier.Copy(&m, rq) // 和 miniblog 一样：req -> model :contentReference[oaicite:8]{index=8}

	// 2) 填充默认值（避免 DB not null/default 依赖过强）
	if strings.TrimSpace(m.TenantID) == "" {
		m.TenantID = "default"
	}
	if strings.TrimSpace(m.Cluster) == "" {
		m.Cluster = "default"
	}
	if strings.TrimSpace(m.Environment) == "" {
		m.Environment = "prod"
	}
	if strings.TrimSpace(m.Source) == "" {
		m.Source = "api"
	}
	if strings.TrimSpace(m.Status) == "" {
		m.Status = "open"
	}
	if strings.TrimSpace(m.RCAStatus) == "" {
		m.RCAStatus = "pending"
	}
	if strings.TrimSpace(m.ActionStatus) == "" {
		m.ActionStatus = "none"
	}

	// 3) 审计字段：优先 username，其次 userID（你的 contextx 同时支持二者）
	if u := strings.TrimSpace(contextx.Username(ctx)); u != "" {
		m.CreatedBy = &u
	} else if uid := strings.TrimSpace(contextx.UserID(ctx)); uid != "" {
		m.CreatedBy = &uid
	}

	// 4) 落库
	if err := b.store.Incident().Create(ctx, &m); err != nil { // store 已经提供 Incident().Create :contentReference[oaicite:9]{index=9}
		return nil, err
	}

	// 5) 返回事件单 ID（AfterCreate hook 会生成并回写，所以这里拿得到）:contentReference[oaicite:10]{index=10}
	return &apiv1.CreateIncidentResponse{
		IncidentID: m.IncidentID,
	}, nil
}

func (b *incidentBiz) Get(ctx context.Context, rq *apiv1.GetIncidentRequest) (*apiv1.GetIncidentResponse, error) {
	whr := where.T(ctx).F("incident_id", rq.GetIncidentID())
	incidentM, err := b.store.Incident().Get(ctx, whr)
	if err != nil {
		return nil, err
	}

	return &apiv1.GetIncidentResponse{Incident: conversion.IncidentMToIncidentV1(incidentM)}, nil
}

//nolint:gocognit,gocyclo // Incident list filters are explicit for readability.
func (b *incidentBiz) List(ctx context.Context, rq *apiv1.ListIncidentRequest) (*apiv1.ListIncidentResponse, error) {
	// 1) 兜底分页参数
	whr := where.T(ctx).P(int(rq.GetOffset()), int(rq.GetLimit()))

	// 2) 组 where
	if rq.Service != nil && rq.GetService() != "" {
		whr = whr.F("service", rq.GetService())
	}
	if rq.Namespace != nil && rq.GetNamespace() != "" {
		whr = whr.F("namespace", rq.GetNamespace())
	}
	if rq.Status != nil && rq.GetStatus() != "" {
		whr = whr.F("status", rq.GetStatus())
	}
	if rq.Severity != nil && rq.GetSeverity() != "" {
		whr = whr.F("severity", rq.GetSeverity())
	}
	// created_at 范围
	if rq.GetCreatedAtStart() != nil {
		whr = whr.C(clause.Expr{SQL: "created_at >= ?", Vars: []any{rq.GetCreatedAtStart().AsTime()}})
	}
	if rq.GetCreatedAtEnd() != nil {
		whr = whr.C(clause.Expr{SQL: "created_at <= ?", Vars: []any{rq.GetCreatedAtEnd().AsTime()}})
	}
	// 3) 查 total + list
	count, incidentList, err := b.store.Incident().List(ctx, whr)
	if err != nil {
		return nil, err
	}
	incidents := make([]*apiv1.Incident, 0, len(incidentList))
	for _, incident := range incidentList {
		incidents = append(incidents, conversion.IncidentMToIncidentV1(incident))
	}

	return &apiv1.ListIncidentResponse{TotalCount: count, Incidents: incidents}, nil
}

func (b *incidentBiz) CreateAction(
	ctx context.Context,
	rq *apiv1.CreateIncidentActionRequest,
) (*apiv1.CreateIncidentActionResponse, error) {

	incidentID := strings.TrimSpace(rq.GetIncidentID())
	if err := b.ensureIncidentExists(ctx, incidentID); err != nil {
		return nil, err
	}

	actor := resolveOperatorActor(ctx, rq.Actor)
	actionType, _, _ := sanitizeOperatorTextWithLimit(rq.GetActionType(), maxActionTypeLen)
	summary, summaryRedacted, summaryTruncated := sanitizeOperatorTextWithLimit(rq.GetSummary(), maxActionSummaryLen)
	detailsJSON, warnings := sanitizeAndLimitJSONLike(rq.GetDetailsJSON(), maxActionDetailsJSONLen)
	if summaryRedacted {
		warnings = appendOperatorWarning(warnings, operatorWarningRedacted)
	}
	if summaryTruncated {
		warnings = appendOperatorWarning(warnings, operatorWarningTruncated)
	}

	m := &model.IncidentActionLogM{
		IncidentID: incidentID,
		Actor:      actor,
		ActionType: strings.ToLower(strings.TrimSpace(actionType)),
		Summary:    summary,
	}
	if detailsJSON != "" {
		m.DetailsJSON = &detailsJSON
	}

	if err := b.store.IncidentActionLog().Create(ctx, m); err != nil {
		return nil, errno.ErrIncidentActionCreateFailed
	}

	payload := map[string]any{
		"action_id":   m.ActionID,
		"actor":       m.Actor,
		"action_type": m.ActionType,
		"summary":     m.Summary,
	}
	if m.DetailsJSON != nil {
		payload["details_json"] = *m.DetailsJSON
	}
	if len(warnings) > 0 {
		payload["warnings"] = warnings
	}
	audit.AppendIncidentTimelineIfExists(ctx, b.store.DB(ctx), incidentID, "operator_action", m.ActionID, payload)

	return &apiv1.CreateIncidentActionResponse{
		Action:   conversion.IncidentActionLogMToOperatorActionLogV1(m),
		Warnings: warnings,
	}, nil
}

//nolint:dupl // Keep explicit list flow aligned with verification-runs endpoint behavior.
func (b *incidentBiz) ListActions(
	ctx context.Context,
	rq *apiv1.ListIncidentActionsRequest,
) (*apiv1.ListIncidentActionsResponse, error) {

	limit := normalizeOperatorListLimit(rq.GetLimit())
	page := normalizeOperatorPage(rq.GetPage())
	offset := (page - 1) * limit

	whr := where.T(ctx).
		O(int(offset)).
		L(int(limit)).
		F("incident_id", strings.TrimSpace(rq.GetIncidentID()))

	total, list, err := b.store.IncidentActionLog().List(ctx, whr)
	if err != nil {
		return nil, errno.ErrIncidentActionListFailed
	}

	actions := make([]*apiv1.OperatorActionLog, 0, len(list))
	for _, item := range list {
		actions = append(actions, conversion.IncidentActionLogMToOperatorActionLogV1(item))
	}
	return &apiv1.ListIncidentActionsResponse{
		TotalCount: total,
		Actions:    actions,
	}, nil
}

func (b *incidentBiz) CreateVerificationRun(
	ctx context.Context,
	rq *apiv1.CreateIncidentVerificationRunRequest,
) (*apiv1.CreateIncidentVerificationRunResponse, error) {

	incidentID := strings.TrimSpace(rq.GetIncidentID())
	if err := b.ensureIncidentExists(ctx, incidentID); err != nil {
		return nil, err
	}

	actor := resolveOperatorActor(ctx, rq.Actor)
	source, _, _ := sanitizeOperatorTextWithLimit(rq.GetSource(), maxVerificationSourceLen)
	tool, _, _ := sanitizeOperatorTextWithLimit(rq.GetTool(), maxVerificationToolLen)
	observed, observedRedacted, observedTruncated := sanitizeOperatorTextWithLimit(rq.GetObserved(), maxVerificationObservedLen)
	paramsJSON, warnings := sanitizeAndLimitJSONLike(rq.GetParamsJSON(), maxVerificationParamsJSONLen)
	if observedRedacted {
		warnings = appendOperatorWarning(warnings, operatorWarningRedacted)
	}
	if observedTruncated {
		warnings = appendOperatorWarning(warnings, operatorWarningTruncated)
	}

	m := &model.IncidentVerificationRunM{
		IncidentID:       incidentID,
		Actor:            actor,
		Source:           strings.ToLower(strings.TrimSpace(source)),
		StepIndex:        rq.GetStepIndex(),
		Tool:             strings.TrimSpace(tool),
		Observed:         observed,
		MeetsExpectation: rq.GetMeetsExpectation(),
	}
	if paramsJSON != "" {
		m.ParamsJSON = &paramsJSON
	}

	if err := b.store.IncidentVerificationRun().Create(ctx, m); err != nil {
		return nil, errno.ErrIncidentVerificationRunCreateFailed
	}

	payload := map[string]any{
		"run_id":            m.RunID,
		"actor":             m.Actor,
		"source":            m.Source,
		"step_index":        m.StepIndex,
		"tool":              m.Tool,
		"observed":          m.Observed,
		"meets_expectation": m.MeetsExpectation,
	}
	if m.ParamsJSON != nil {
		payload["params_json"] = *m.ParamsJSON
	}
	if len(warnings) > 0 {
		payload["warnings"] = warnings
	}
	audit.AppendIncidentTimelineIfExists(ctx, b.store.DB(ctx), incidentID, "verification_run", m.RunID, payload)

	return &apiv1.CreateIncidentVerificationRunResponse{
		Run:      conversion.IncidentVerificationRunMToVerificationRunV1(m),
		Warnings: warnings,
	}, nil
}

//nolint:dupl // Keep explicit list flow aligned with actions endpoint behavior.
func (b *incidentBiz) ListVerificationRuns(
	ctx context.Context,
	rq *apiv1.ListIncidentVerificationRunsRequest,
) (*apiv1.ListIncidentVerificationRunsResponse, error) {

	limit := normalizeOperatorListLimit(rq.GetLimit())
	page := normalizeOperatorPage(rq.GetPage())
	offset := (page - 1) * limit

	whr := where.T(ctx).
		O(int(offset)).
		L(int(limit)).
		F("incident_id", strings.TrimSpace(rq.GetIncidentID()))

	total, list, err := b.store.IncidentVerificationRun().List(ctx, whr)
	if err != nil {
		return nil, errno.ErrIncidentVerificationRunListFailed
	}

	runs := make([]*apiv1.VerificationRun, 0, len(list))
	for _, item := range list {
		runs = append(runs, conversion.IncidentVerificationRunMToVerificationRunV1(item))
	}

	return &apiv1.ListIncidentVerificationRunsResponse{
		TotalCount: total,
		Runs:       runs,
	}, nil
}

//nolint:gocognit,gocyclo // Incident search filters are explicit for readability.
func (b *incidentBiz) Search(ctx context.Context, rq *SearchIncidentsRequest) (*SearchIncidentsResponse, error) {
	if rq == nil {
		return nil, errorsx.ErrInvalidArgument
	}

	limit := rq.Limit
	if limit <= 0 {
		limit = 20
	}
	whr := where.T(ctx).P(int(rq.Offset), int(limit))

	if v := trimOptionalString(rq.Namespace); v != "" {
		whr = whr.F("namespace", v)
	}
	if v := trimOptionalString(rq.Service); v != "" {
		whr = whr.F("service", v)
	}
	if v := trimOptionalString(rq.Severity); v != "" {
		whr = whr.F("severity", v)
	}
	if v := trimOptionalString(rq.Status); v != "" {
		whr = whr.F("status", v)
	}
	if v := trimOptionalString(rq.RCAStatus); v != "" {
		whr = whr.F("rca_status", strings.ToLower(v))
	}
	if rq.TimeFrom != nil {
		whr = whr.C(clause.Expr{
			SQL:  "created_at >= ?",
			Vars: []any{rq.TimeFrom.UTC()},
		})
	}
	if rq.TimeTo != nil {
		whr = whr.C(clause.Expr{
			SQL:  "created_at <= ?",
			Vars: []any{rq.TimeTo.UTC()},
		})
	}

	if v := trimOptionalString(rq.RootCauseType); v != "" {
		whr = whr.Q("diagnosis_json LIKE ?", fmt.Sprintf("%%\"type\":\"%s\"%%", v))
	}
	if v := trimOptionalString(rq.Q); v != "" {
		like := "%" + v + "%"
		whr = whr.Q(
			"(service LIKE ? OR namespace LIKE ? OR root_cause_summary LIKE ? OR diagnosis_json LIKE ?)",
			like,
			like,
			like,
			like,
		)
	}

	total, list, err := b.store.Incident().List(ctx, whr)
	if err != nil {
		return nil, err
	}

	out := make([]*apiv1.Incident, 0, len(list))
	for _, item := range list {
		out = append(out, conversion.IncidentMToIncidentV1(item))
	}
	return &SearchIncidentsResponse{
		TotalCount: total,
		Incidents:  out,
	}, nil
}

//nolint:gocognit,gocyclo // Best-effort schema-compatible timeline read handles legacy columns.
func (b *incidentBiz) ListTimeline(ctx context.Context, rq *ListIncidentTimelineRequest) (*ListIncidentTimelineResponse, error) {
	if rq == nil || strings.TrimSpace(rq.IncidentID) == "" {
		return nil, errorsx.ErrInvalidArgument
	}

	db := b.store.DB(ctx)
	if !db.Migrator().HasTable("incident_timeline") {
		return &ListIncidentTimelineResponse{
			TotalCount: 0,
			Events:     []*IncidentTimelineEvent{},
		}, nil
	}

	columns, err := db.Migrator().ColumnTypes("incident_timeline")
	if err != nil {
		return nil, err
	}
	colSet := make(map[string]struct{}, len(columns))
	for _, c := range columns {
		colSet[strings.ToLower(strings.TrimSpace(c.Name()))] = struct{}{}
	}

	eventTypeCol := firstExistingColumn(colSet, "event_type", "type")
	refCol := firstExistingColumn(colSet, "ref_id", "ref")
	detailCol := firstExistingColumn(colSet, "payload_json", "detail_json", "detail", "message")
	tsCol := firstExistingColumn(colSet, "created_at", "event_at", "occurred_at", "ts", "updated_at")
	orderCol := firstExistingColumn(colSet, "created_at", "event_at", "occurred_at", "id")

	selectCols := make([]string, 0, 5)
	if eventTypeCol != "" {
		selectCols = append(selectCols, eventTypeCol)
	}
	if refCol != "" {
		selectCols = append(selectCols, refCol)
	}
	if detailCol != "" {
		selectCols = append(selectCols, detailCol)
	}
	if tsCol != "" {
		selectCols = append(selectCols, tsCol)
	}
	if len(selectCols) == 0 {
		selectCols = append(selectCols, "incident_id")
	}

	query := db.WithContext(ctx).Table("incident_timeline").Where("incident_id = ?", strings.TrimSpace(rq.IncidentID))
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}

	limit := rq.Limit
	if limit <= 0 {
		limit = 20
	}
	listQuery := query.Select(selectCols)
	if orderCol != "" {
		listQuery = listQuery.Order(orderCol + " DESC")
	}
	listQuery = listQuery.Order("id DESC").Offset(int(rq.Offset)).Limit(int(limit))

	rows := make([]map[string]any, 0, limit)
	if err := listQuery.Find(&rows).Error; err != nil {
		return nil, err
	}

	events := make([]*IncidentTimelineEvent, 0, len(rows))
	for _, row := range rows {
		event := &IncidentTimelineEvent{}
		if eventTypeCol != "" {
			event.EventType = readTimelineRowString(row, eventTypeCol)
		}
		if refCol != "" {
			event.RefID = readTimelineRowString(row, refCol)
		}
		if detailCol != "" {
			event.Detail = readTimelineRowString(row, detailCol)
		}
		if tsCol != "" {
			event.CreatedAt = readTimelineRowTime(row, tsCol)
		}
		events = append(events, event)
	}

	return &ListIncidentTimelineResponse{
		TotalCount: total,
		Events:     events,
	}, nil
}

//nolint:gocognit,gocyclo,nestif // State transition handling is explicit for auditability.
func (b *incidentBiz) Update(ctx context.Context, rq *apiv1.UpdateIncidentRequest) (*apiv1.UpdateIncidentResponse, error) {
	whr := where.T(ctx).F("incident_id", rq.GetIncidentID())
	incidentM, err := b.store.Incident().Get(ctx, whr)
	if err != nil {
		return nil, err
	}
	oldStatus := strings.ToLower(strings.TrimSpace(incidentM.Status))
	oldSeverity := strings.TrimSpace(incidentM.Severity)
	oldVersion := derefString(incidentM.Version)
	if rq.Status != nil {
		newStatus := strings.ToLower(strings.TrimSpace(rq.GetStatus()))
		if newStatus != "" {
			incidentM.Status = newStatus
			if isClosedIncidentStatus(newStatus) {
				incidentM.ActiveFingerprintKey = nil
				if incidentM.EndAt == nil {
					endedAt := time.Now().UTC()
					incidentM.EndAt = &endedAt
				}
			} else if incidentM.ActiveFingerprintKey == nil && incidentM.Fingerprint != nil && strings.TrimSpace(*incidentM.Fingerprint) != "" {
				fp := strings.TrimSpace(*incidentM.Fingerprint)
				incidentM.ActiveFingerprintKey = &fp
			}
		}
	}
	if rq.Severity != nil {
		incidentM.Severity = rq.GetSeverity()
	}

	if err := b.store.Incident().Update(ctx, incidentM); err != nil {
		return nil, err
	}
	if oldStatus != strings.ToLower(strings.TrimSpace(incidentM.Status)) {
		audit.AppendIncidentTimelineIfExists(ctx, b.store.DB(ctx), incidentM.IncidentID, "incident_status_changed", incidentM.IncidentID, map[string]any{
			"from_status": oldStatus,
			"to_status":   incidentM.Status,
		})
	}
	b.maybeTriggerOnEscalationAIJob(ctx, incidentM, oldStatus, oldSeverity, oldVersion)

	return &apiv1.UpdateIncidentResponse{}, nil
}

func isClosedIncidentStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "resolved", "closed":
		return true
	default:
		return false
	}
}

func trimOptionalString(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

func (b *incidentBiz) ensureIncidentExists(ctx context.Context, incidentID string) error {
	_, err := b.store.Incident().Get(ctx, where.T(ctx).F("incident_id", strings.TrimSpace(incidentID)))
	if err == nil {
		return nil
	}
	if errorsx.Is(err, gorm.ErrRecordNotFound) {
		return errno.ErrIncidentNotFound
	}
	return errno.ErrIncidentGetFailed
}

func resolveOperatorActor(ctx context.Context, actorOverride *string) string {
	if actor := strings.TrimSpace(trimOptionalString(actorOverride)); actor != "" {
		sanitized, _, _ := sanitizeOperatorTextWithLimit(actor, maxActionActorLen)
		if sanitized != "" {
			return sanitized
		}
	}
	if actor := strings.TrimSpace(contextx.Username(ctx)); actor != "" {
		sanitized, _, _ := sanitizeOperatorTextWithLimit(actor, maxActionActorLen)
		if sanitized != "" {
			return sanitized
		}
	}
	if actor := strings.TrimSpace(contextx.UserID(ctx)); actor != "" {
		sanitized, _, _ := sanitizeOperatorTextWithLimit(actor, maxActionActorLen)
		if sanitized != "" {
			return sanitized
		}
	}
	return defaultOperatorActor
}

func normalizeOperatorListLimit(limit int64) int64 {
	if limit <= 0 {
		return defaultOperatorListLimit
	}
	if limit > maxOperatorListLimit {
		return maxOperatorListLimit
	}
	return limit
}

func normalizeOperatorPage(page int64) int64 {
	if page <= 0 {
		return 1
	}
	return page
}

func sanitizeAndLimitJSONLike(raw string, maxBytes int) (string, []string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}

	redacted := false
	sanitized := sanitizeOperatorString(trimmed, &redacted)

	var payload any
	if err := json.Unmarshal([]byte(trimmed), &payload); err == nil {
		normalized := sanitizeOperatorAny(payload, &redacted)
		encoded, marshalErr := json.Marshal(normalized)
		if marshalErr == nil {
			sanitized = string(encoded)
		}
	}

	warnings := []string{}
	if redacted {
		warnings = appendOperatorWarning(warnings, operatorWarningRedacted)
	}
	limited, truncated := enforceJSONSizeLimit(sanitized, maxBytes)
	if truncated {
		warnings = appendOperatorWarning(warnings, operatorWarningTruncated)
	}
	return limited, warnings
}

//nolint:wsl_v5 // Recursive sanitizer intentionally handles mixed JSON-like types.
func sanitizeOperatorAny(value any, redacted *bool) any {
	switch typed := value.(type) {
	case map[string]any:
		return sanitizeOperatorMapAny(typed, redacted)
	case []any:
		return sanitizeOperatorSliceAny(typed, redacted)
	case map[string]string:
		return sanitizeOperatorMapString(typed, redacted)
	case string:
		return sanitizeOperatorString(typed, redacted)
	default:
		return typed
	}
}

func sanitizeOperatorMapAny(input map[string]any, redacted *bool) map[string]any {
	out := make(map[string]any, len(input))
	for key, item := range input {
		if isOperatorSensitiveKey(key) {
			markOperatorRedacted(redacted)
			continue
		}
		out[key] = sanitizeOperatorAny(item, redacted)
	}
	return out
}

func sanitizeOperatorSliceAny(input []any, redacted *bool) []any {
	out := make([]any, 0, len(input))
	for _, item := range input {
		out = append(out, sanitizeOperatorAny(item, redacted))
	}
	return out
}

func sanitizeOperatorMapString(input map[string]string, redacted *bool) map[string]any {
	out := make(map[string]any, len(input))
	for key, item := range input {
		if isOperatorSensitiveKey(key) {
			markOperatorRedacted(redacted)
			continue
		}
		out[key] = sanitizeOperatorAny(item, redacted)
	}
	return out
}

func markOperatorRedacted(redacted *bool) {
	if redacted != nil {
		*redacted = true
	}
}

func sanitizeOperatorTextWithLimit(raw string, maxBytes int) (string, bool, bool) {
	redacted := false
	out := sanitizeOperatorString(raw, &redacted)
	limited, truncated := truncateUTF8ByBytes(out, maxBytes)
	return limited, redacted, truncated
}

func sanitizeOperatorString(raw string, redacted *bool) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	lower := strings.ToLower(trimmed)
	if containsOperatorSensitiveToken(lower) {
		if redacted != nil {
			*redacted = true
		}
		return "[redacted]"
	}
	if idx := strings.Index(lower, "bearer "); idx >= 0 {
		if redacted != nil {
			*redacted = true
		}
		return trimmed[:idx] + "Bearer [redacted]"
	}
	return trimmed
}

func containsOperatorSensitiveToken(lower string) bool {
	return strings.Contains(lower, "secret") ||
		strings.Contains(lower, "authorization") ||
		strings.Contains(lower, "token") ||
		strings.Contains(lower, "headers") ||
		strings.Contains(lower, "header")
}

func isOperatorSensitiveKey(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	if lower == "" {
		return false
	}
	return strings.Contains(lower, "secret") ||
		strings.Contains(lower, "authorization") ||
		strings.Contains(lower, "token") ||
		strings.Contains(lower, "headers") ||
		strings.Contains(lower, "header")
}

func appendOperatorWarning(warnings []string, warning string) []string {
	if strings.TrimSpace(warning) == "" {
		return warnings
	}
	if slices.Contains(warnings, warning) {
		return warnings
	}
	return append(warnings, warning)
}

func enforceJSONSizeLimit(raw string, maxBytes int) (string, bool) {
	if maxBytes <= 0 {
		return "", strings.TrimSpace(raw) != ""
	}
	if len(raw) <= maxBytes {
		return raw, false
	}

	previewLimit := intMax(64, maxBytes/2)
	preview, _ := truncateUTF8ByBytes(raw, previewLimit)
	payload := map[string]any{
		"truncated": true,
		"reason":    "max_bytes_exceeded",
		"preview":   preview,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		clipped, _ := truncateUTF8ByBytes(raw, maxBytes)
		return clipped, true
	}
	if len(encoded) <= maxBytes {
		return string(encoded), true
	}
	clipped, _ := truncateUTF8ByBytes(string(encoded), maxBytes)
	return clipped, true
}

func truncateUTF8ByBytes(value string, maxBytes int) (string, bool) {
	if maxBytes <= 0 {
		return "", strings.TrimSpace(value) != ""
	}
	if len(value) <= maxBytes {
		return value, false
	}

	var builder strings.Builder
	builder.Grow(maxBytes)
	size := 0
	for _, r := range value {
		width := utf8.RuneLen(r)
		if width <= 0 {
			continue
		}
		if size+width > maxBytes {
			break
		}
		builder.WriteRune(r)
		size += width
	}
	return builder.String(), true
}

func intMax(left int, right int) int {
	if left > right {
		return left
	}
	return right
}

func firstExistingColumn(colSet map[string]struct{}, names ...string) string {
	for _, name := range names {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			continue
		}
		if _, ok := colSet[key]; ok {
			return key
		}
	}
	return ""
}

func readTimelineRowString(row map[string]any, key string) string {
	if row == nil {
		return ""
	}
	val, ok := row[key]
	if !ok || val == nil {
		return ""
	}
	switch typed := val.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []byte:
		return strings.TrimSpace(string(typed))
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", typed))
	}
}

//nolint:gocognit,gocyclo // Timeline timestamps support mixed legacy schemas.
func readTimelineRowTime(row map[string]any, key string) time.Time {
	if row == nil {
		return time.Time{}
	}
	val, ok := row[key]
	if !ok || val == nil {
		return time.Time{}
	}
	switch typed := val.(type) {
	case time.Time:
		return typed.UTC()
	case *time.Time:
		if typed != nil {
			return typed.UTC()
		}

	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return time.Time{}
		}
		if parsed, err := time.Parse(time.RFC3339Nano, trimmed); err == nil {
			return parsed.UTC()
		}
		if parsed, err := time.Parse(time.DateTime, trimmed); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func (b *incidentBiz) Delete(ctx context.Context, rq *apiv1.DeleteIncidentRequest) (*apiv1.DeleteIncidentResponse, error) {
	whr := where.T(ctx).F("incident_id", rq.GetIncidentIDs())
	if err := b.store.Incident().Delete(ctx, whr); err != nil {
		return nil, err
	}

	return &apiv1.DeleteIncidentResponse{}, nil
}
