package evidence

//go:generate mockgen -destination mock_evidence.go -package evidence github.com/aiopsre/rca-api/internal/apiserver/biz/v1/evidence EvidenceBiz

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/onexstack/onexstack/pkg/errorsx"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/conversion"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/policy"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	"github.com/aiopsre/rca-api/internal/pkg/contextx"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

const (
	defaultListLimit       = int64(20)
	defaultEvidenceCreator = "system"
)

// EvidenceBiz defines evidence use-cases.
//
//nolint:interfacebloat // Query/save/get/list/search are intentionally grouped in one biz entrypoint.
type EvidenceBiz interface {
	Save(ctx context.Context, rq *v1.SaveEvidenceRequest) (*v1.SaveEvidenceResponse, error)
	Get(ctx context.Context, rq *v1.GetEvidenceRequest) (*v1.GetEvidenceResponse, error)
	ListByIncident(ctx context.Context, rq *v1.ListIncidentEvidenceRequest) (*v1.ListIncidentEvidenceResponse, error)
	Search(ctx context.Context, rq *SearchEvidenceRequest) (*SearchEvidenceResponse, error)

	EvidenceExpansion
}

//nolint:modernize // Keep explicit empty interface as placeholder expansion point.
type EvidenceExpansion interface{}

type evidenceBiz struct {
	store      store.IStore
	guardrails policy.EvidenceGuardrails
}

type SearchEvidenceRequest struct {
	Offset        int64
	Limit         int64
	IncidentID    *string
	DatasourceRef *string
	Type          *string
	TimeFrom      *time.Time
	TimeTo        *time.Time
}

type SearchEvidenceResponse struct {
	TotalCount int64
	Evidence   []*v1.Evidence
}

var _ EvidenceBiz = (*evidenceBiz)(nil)

// New creates evidence biz with default guardrails.
func New(store store.IStore) *evidenceBiz {
	guardrails := policy.DefaultEvidenceGuardrails()
	return &evidenceBiz{
		store:      store,
		guardrails: guardrails,
	}
}

//nolint:gocognit,gocyclo // Save flow keeps idempotency and guardrails explicit.
func (b *evidenceBiz) Save(ctx context.Context, rq *v1.SaveEvidenceRequest) (*v1.SaveEvidenceResponse, error) {
	start := rq.GetTimeRangeStart().AsTime()
	end := rq.GetTimeRangeEnd().AsTime()
	if err := b.validateTimeRange(start, end, b.guardrails.MaxMetricsRange); err != nil {
		return nil, err
	}

	idempotencyKey := ""
	if rq.IdempotencyKey != nil {
		idempotencyKey = strings.TrimSpace(rq.GetIdempotencyKey())
	}
	if idempotencyKey != "" {
		existing, err := b.store.Evidence().Get(ctx, where.T(ctx).F("incident_id", rq.GetIncidentID(), "idempotency_key", idempotencyKey))
		if err == nil {
			return &v1.SaveEvidenceResponse{EvidenceID: existing.EvidenceID}, nil
		}
		if err != nil && !errorsx.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrEvidenceSaveFailed
		}
	}

	resultJSON, sizeBytes, truncated := b.normalizeRawResult(strings.TrimSpace(rq.GetResultJSON()), b.guardrails.MaxResultBytes)
	queryHash := hashQuery(rq.GetDatasourceID(), rq.GetType(), rq.GetQueryText(), rq.GetQueryJSON(), start, end)

	createdBy := strings.TrimSpace(rq.GetCreatedBy())
	if createdBy == "" {
		createdBy = defaultEvidenceCreator
	}

	m := &model.EvidenceM{
		IncidentID:      strings.TrimSpace(rq.GetIncidentID()),
		Type:            strings.ToLower(strings.TrimSpace(rq.GetType())),
		QueryText:       strings.TrimSpace(rq.GetQueryText()),
		QueryHash:       queryHash,
		TimeRangeStart:  start,
		TimeRangeEnd:    end,
		ResultJSON:      resultJSON,
		ResultSizeBytes: sizeBytes,
		IsTruncated:     truncated,
		CreatedBy:       createdBy,
	}

	if rq.JobID != nil {
		v := strings.TrimSpace(rq.GetJobID())
		m.JobID = &v
	}
	if rq.DatasourceID != nil {
		v := strings.TrimSpace(rq.GetDatasourceID())
		m.DatasourceID = &v
	}
	if rq.QueryJSON != nil {
		v := strings.TrimSpace(rq.GetQueryJSON())
		m.QueryJSON = &v
	}
	if rq.Summary != nil {
		v := strings.TrimSpace(rq.GetSummary())
		m.Summary = &v
	}
	if idempotencyKey != "" {
		m.IdempotencyKey = &idempotencyKey
	}

	if err := b.store.Evidence().Create(ctx, m); err != nil {
		// Safe fallback for duplicate concurrent idempotency key writes.
		if idempotencyKey != "" && isDuplicateKeyError(err) {
			existing, getErr := b.store.Evidence().Get(ctx, where.T(ctx).F("incident_id", rq.GetIncidentID(), "idempotency_key", idempotencyKey))
			if getErr == nil {
				return &v1.SaveEvidenceResponse{EvidenceID: existing.EvidenceID}, nil
			}
			return nil, errno.ErrEvidenceIdempotencyConflict
		}
		return nil, errno.ErrEvidenceSaveFailed
	}

	slog.InfoContext(ctx, "evidence saved",
		"request_id", contextx.RequestID(ctx),
		"incident_id", rq.GetIncidentID(),
		"job_id", rq.GetJobID(),
		"tool_call_id", "",
		"datasource_ref", rq.GetDatasourceID(),
		"evidence_id", m.EvidenceID,
		"idempotency_key", idempotencyKey,
	)

	return &v1.SaveEvidenceResponse{EvidenceID: m.EvidenceID}, nil
}

func (b *evidenceBiz) Get(ctx context.Context, rq *v1.GetEvidenceRequest) (*v1.GetEvidenceResponse, error) {
	m, err := b.store.Evidence().Get(ctx, where.T(ctx).F("evidence_id", rq.GetEvidenceID()))
	if err != nil {
		return nil, toEvidenceGetError(err)
	}
	return &v1.GetEvidenceResponse{Evidence: conversion.EvidenceMToEvidenceV1(m)}, nil
}

func (b *evidenceBiz) ListByIncident(ctx context.Context, rq *v1.ListIncidentEvidenceRequest) (*v1.ListIncidentEvidenceResponse, error) {
	limit := rq.GetLimit()
	if limit <= 0 {
		limit = defaultListLimit
	}

	whr := where.T(ctx).P(int(rq.GetOffset()), int(limit)).F("incident_id", rq.GetIncidentID())
	if rq.Type != nil && strings.TrimSpace(rq.GetType()) != "" {
		whr = whr.F("type", strings.ToLower(strings.TrimSpace(rq.GetType())))
	}
	if rq.DatasourceID != nil && strings.TrimSpace(rq.GetDatasourceID()) != "" {
		whr = whr.F("datasource_id", strings.TrimSpace(rq.GetDatasourceID()))
	}

	total, list, err := b.store.Evidence().List(ctx, whr)
	if err != nil {
		return nil, errno.ErrEvidenceListFailed
	}

	out := make([]*v1.Evidence, 0, len(list))
	for _, item := range list {
		out = append(out, conversion.EvidenceMToEvidenceV1(item))
	}

	return &v1.ListIncidentEvidenceResponse{
		TotalCount: total,
		Evidence:   out,
	}, nil
}

func (b *evidenceBiz) Search(ctx context.Context, rq *SearchEvidenceRequest) (*SearchEvidenceResponse, error) {
	if rq == nil {
		return nil, errorsx.ErrInvalidArgument
	}

	limit := rq.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	whr := where.T(ctx).P(int(rq.Offset), int(limit))
	if v := trimOptionalString(rq.IncidentID); v != "" {
		whr = whr.F("incident_id", v)
	}
	if v := trimOptionalString(rq.DatasourceRef); v != "" {
		whr = whr.F("datasource_id", v)
	}
	if v := trimOptionalString(rq.Type); v != "" {
		whr = whr.F("type", strings.ToLower(v))
	}
	if rq.TimeFrom != nil {
		whr = whr.C(clause.Expr{
			SQL:  "time_range_start >= ?",
			Vars: []any{rq.TimeFrom.UTC()},
		})
	}
	if rq.TimeTo != nil {
		whr = whr.C(clause.Expr{
			SQL:  "time_range_end <= ?",
			Vars: []any{rq.TimeTo.UTC()},
		})
	}

	total, list, err := b.store.Evidence().List(ctx, whr)
	if err != nil {
		return nil, errno.ErrEvidenceListFailed
	}

	out := make([]*v1.Evidence, 0, len(list))
	for _, item := range list {
		out = append(out, conversion.EvidenceMToEvidenceV1(item))
	}
	return &SearchEvidenceResponse{
		TotalCount: total,
		Evidence:   out,
	}, nil
}

func (b *evidenceBiz) validateTimeRange(start, end time.Time, maxRange time.Duration) error {
	if start.IsZero() || end.IsZero() {
		return errorsx.ErrInvalidArgument
	}
	if !start.Before(end) {
		return errorsx.ErrInvalidArgument
	}
	if end.Sub(start) > maxRange {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func (b *evidenceBiz) normalizeRawResult(result string, maxBytes int) (string, int64, bool) {
	raw := []byte(result)
	size := int64(len(raw))
	if len(raw) <= maxBytes {
		return result, size, false
	}

	preview := string(raw[:maxBytes])
	wrapped := map[string]any{
		"truncated": true,
		"reason":    "max_result_bytes_exceeded",
		"preview":   preview,
	}
	out, err := json.Marshal(wrapped)
	if err != nil {
		fallback := `{"truncated":true,"reason":"marshal_failed"}`
		return fallback, size, true
	}
	return string(out), size, true
}

func hashQuery(datasourceRef string, typ string, queryText string, queryJSON string, start time.Time, end time.Time) string {
	payload := strings.Join([]string{
		strings.TrimSpace(datasourceRef),
		strings.TrimSpace(strings.ToLower(typ)),
		strings.TrimSpace(queryText),
		strings.TrimSpace(queryJSON),
		start.UTC().Format(time.RFC3339Nano),
		end.UTC().Format(time.RFC3339Nano),
	}, "|")
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func toEvidenceGetError(err error) error {
	if errorsx.Is(err, gorm.ErrRecordNotFound) {
		return errno.ErrEvidenceNotFound
	}
	return errno.ErrEvidenceGetFailed
}

func isDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "duplicate") || strings.Contains(lower, "unique constraint")
}

func trimOptionalString(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}