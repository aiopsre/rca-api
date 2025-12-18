package incident

//go:generate mockgen -destination mock_incident.go -package incident zk8s.com/rca-api/internal/apiserver/biz/v1/incident IncidentBiz

import (
	"context"
	"strings"
	"time"

	"github.com/jinzhu/copier"
	"gorm.io/gorm/clause"
	"zk8s.com/rca-api/internal/apiserver/model"
	"zk8s.com/rca-api/internal/apiserver/pkg/audit"
	"zk8s.com/rca-api/internal/apiserver/pkg/conversion"
	"zk8s.com/rca-api/internal/apiserver/store"
	"zk8s.com/rca-api/internal/pkg/contextx"
	apiv1 "zk8s.com/rca-api/pkg/api/apiserver/v1"
	"zk8s.com/rca-api/pkg/store/where"
)

// IncidentBiz 定义 incident 相关业务能力（先最小：Create）
//
// 参考 miniblog 的 PostBiz：入参是 proto request，返回 proto response。:contentReference[oaicite:4]{index=4}
type IncidentBiz interface {
	Create(ctx context.Context, rq *apiv1.CreateIncidentRequest) (*apiv1.CreateIncidentResponse, error)
	Update(ctx context.Context, rq *apiv1.UpdateIncidentRequest) (*apiv1.UpdateIncidentResponse, error)
	Delete(ctx context.Context, rq *apiv1.DeleteIncidentRequest) (*apiv1.DeleteIncidentResponse, error)
	Get(ctx context.Context, rq *apiv1.GetIncidentRequest) (*apiv1.GetIncidentResponse, error)
	List(ctx context.Context, rq *apiv1.ListIncidentRequest) (*apiv1.ListIncidentResponse, error)

	IncidentExpansion
}

// IncidentExpansion 预留扩展方法（对齐 miniblog 的写法）。:contentReference[oaicite:5]{index=5}
type IncidentExpansion interface{}

// incidentBiz 是 IncidentBiz 的实现
type incidentBiz struct {
	store store.IStore
}

var _ IncidentBiz = (*incidentBiz)(nil)

// New 创建 incidentBiz 实例（对齐 miniblog New(store) 风格）。:contentReference[oaicite:6]{index=6}
func New(store store.IStore) *incidentBiz {
	return &incidentBiz{store: store}
}

// Create 创建事件单：把 CreateIncidentRequest 映射到 model.IncidentM，然后落库。
// IncidentID 由 model.AfterCreate hook 自动生成（incident-xxxxxx）。:contentReference[oaicite:7]{index=7}
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

func (b *incidentBiz) List(ctx context.Context, rq *apiv1.ListIncidentRequest) (*apiv1.ListIncidentResponse, error) {
	// 1) 兜底分页参数
	whr := where.T(ctx).P(int(rq.GetOffset()), int(rq.GetLimit()))

	// 2) 组 where
	if rq.Service != nil && *rq.Service != "" {
		whr = whr.F("service", *rq.Service)
	}
	if rq.Namespace != nil && *rq.Namespace != "" {
		whr = whr.F("namespace", *rq.Namespace)
	}
	if rq.Status != nil && *rq.Status != "" {
		whr = whr.F("status", *rq.Status)
	}
	if rq.Severity != nil && *rq.Severity != "" {
		whr = whr.F("severity", *rq.Severity)
	}
	// created_at 范围
	if rq.CreatedAtStart != nil {
		whr = whr.C(clause.Expr{SQL: "created_at >= ?", Vars: []any{rq.CreatedAtStart.AsTime()}})
	}
	if rq.CreatedAtEnd != nil {
		whr = whr.C(clause.Expr{SQL: "created_at <= ?", Vars: []any{rq.CreatedAtEnd.AsTime()}})
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

func (b *incidentBiz) Delete(ctx context.Context, rq *apiv1.DeleteIncidentRequest) (*apiv1.DeleteIncidentResponse, error) {
	whr := where.T(ctx).F("incident_id", rq.GetIncidentIDs())
	if err := b.store.Incident().Delete(ctx, whr); err != nil {
		return nil, err
	}

	return &apiv1.DeleteIncidentResponse{}, nil
}
