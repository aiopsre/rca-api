package incident

//go:generate mockgen -destination mock_incident.go -package incident github.com/aiopsre/rca-api/internal/apiserver/biz/v1/incident IncidentBiz

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jinzhu/copier"
	"github.com/onexstack/onexstack/pkg/errorsx"
	"gorm.io/gorm/clause"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/audit"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/conversion"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	"github.com/aiopsre/rca-api/internal/pkg/contextx"
	apiv1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
	"github.com/aiopsre/rca-api/pkg/store/where"
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
	store store.IStore
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
	return &incidentBiz{store: store}
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
