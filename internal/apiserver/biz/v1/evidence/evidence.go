package evidence

//go:generate mockgen -destination mock_evidence.go -package evidence github.com/aiopsre/rca-api/internal/apiserver/biz/v1/evidence EvidenceBiz

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/onexstack/onexstack/pkg/errorsx"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/conversion"
	integrationds "github.com/aiopsre/rca-api/internal/apiserver/pkg/integrations/datasource"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/metrics"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/policy"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	"github.com/aiopsre/rca-api/internal/pkg/contextx"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

const (
	defaultQueryStepSeconds = int64(30)
	defaultLogsLimit        = int64(200)
	defaultListLimit        = int64(20)
	defaultEvidenceCreator  = "system"
)

// EvidenceBiz defines evidence use-cases.
//
//nolint:interfacebloat // Query/save/get/list/search are intentionally grouped in one biz entrypoint.
type EvidenceBiz interface {
	QueryMetrics(ctx context.Context, rq *v1.QueryMetricsRequest) (*v1.QueryMetricsResponse, error)
	QueryLogs(ctx context.Context, rq *v1.QueryLogsRequest) (*v1.QueryLogsResponse, error)
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
	limiter    *policy.DatasourceRateLimiter
	adapter    integrationds.QueryAdapter
}

type SearchEvidenceRequest struct {
	Offset       int64
	Limit        int64
	IncidentID   *string
	DatasourceID *string
	Type         *string
	TimeFrom     *time.Time
	TimeTo       *time.Time
}

type SearchEvidenceResponse struct {
	TotalCount int64
	Evidence   []*v1.Evidence
}

var _ EvidenceBiz = (*evidenceBiz)(nil)

// New creates evidence biz with default guardrails.
func New(store store.IStore) *evidenceBiz {
	guardrails := policy.DefaultEvidenceGuardrails()
	return NewWithDeps(store, guardrails, integrationds.NewHTTPAdapter())
}

// NewWithDeps creates evidence biz with injected dependencies for tests.
func NewWithDeps(store store.IStore, guardrails policy.EvidenceGuardrails, adapter integrationds.QueryAdapter) *evidenceBiz {
	return &evidenceBiz{
		store:      store,
		guardrails: guardrails,
		limiter:    policy.NewDatasourceRateLimiter(guardrails),
		adapter:    adapter,
	}
}

//nolint:gocognit,gocyclo // Guardrails and error mapping are intentionally explicit.
func (b *evidenceBiz) QueryMetrics(ctx context.Context, rq *v1.QueryMetricsRequest) (*v1.QueryMetricsResponse, error) {
	startedAt := time.Now()
	outcome := "ok"
	datasourceType := "prometheus"
	defer func() {
		if metrics.M != nil {
			metrics.M.RecordEvidenceQuery(ctx, "metrics", datasourceType, outcome, time.Since(startedAt))
		}
	}()

	datasource, err := b.getActiveDatasource(ctx, rq.GetDatasourceID())
	if err != nil {
		outcome = "failed"
		return nil, err
	}
	datasourceType = strings.ToLower(strings.TrimSpace(datasource.Type))
	if !b.limiter.Allow(rq.GetDatasourceID()) {
		outcome = "rate_limited"
		return nil, errno.ErrEvidenceRateLimited
	}

	start := rq.GetTimeRangeStart().AsTime()
	end := rq.GetTimeRangeEnd().AsTime()
	if err := b.validateTimeRange(start, end, b.guardrails.MaxMetricsRange); err != nil {
		outcome = "invalid_argument"
		return nil, err
	}

	step := rq.GetStepSeconds()
	if step <= 0 {
		step = defaultQueryStepSeconds
	}
	if step > 300 {
		outcome = "invalid_argument"
		return nil, errorsx.ErrInvalidArgument
	}

	timeout := b.guardrails.ClampDatasourceTimeout(datasource.TimeoutMs)
	queryResult, err := b.adapter.QueryMetricsRange(
		ctx,
		datasource,
		integrationds.MetricsRangeQuery{
			PromQL:      strings.TrimSpace(rq.GetPromql()),
			Start:       start,
			End:         end,
			StepSeconds: step,
			Timeout:     timeout,
		},
	)
	if err != nil {
		mapped := toDatasourceQueryError(err)
		if errors.Is(mapped, errno.ErrEvidenceQueryTimeout) {
			outcome = "timeout"
		} else {
			outcome = "dependency_error"
		}
		return nil, mapped
	}

	resultJSON, sizeBytes, truncated := b.normalizeResult(queryResult.ResultJSON, b.guardrails.MaxResultBytes)
	rowCount := queryResult.RowCount
	if rowCount > b.guardrails.MaxMetricsRows {
		truncated = true
		resultJSON = b.truncatedResultJSON(resultJSON, "max_metrics_rows_exceeded")
		sizeBytes = int64(len(resultJSON))
	}

	slog.InfoContext(ctx, "evidence metrics query done",
		"request_id", contextx.RequestID(ctx),
		"incident_id", "",
		"job_id", "",
		"tool_call_id", "",
		"datasource_id", rq.GetDatasourceID(),
		"row_count", rowCount,
		"truncated", truncated,
	)

	return &v1.QueryMetricsResponse{
		QueryResultJSON: resultJSON,
		ResultSizeBytes: sizeBytes,
		RowCount:        rowCount,
		IsTruncated:     truncated,
	}, nil
}

//nolint:gocognit,gocyclo // Guardrails and backend selection are intentionally explicit.
func (b *evidenceBiz) QueryLogs(ctx context.Context, rq *v1.QueryLogsRequest) (*v1.QueryLogsResponse, error) {
	startedAt := time.Now()
	outcome := "ok"
	datasourceType := "logs"
	defer func() {
		if metrics.M != nil {
			metrics.M.RecordEvidenceQuery(ctx, "logs", datasourceType, outcome, time.Since(startedAt))
		}
	}()

	datasource, err := b.getActiveDatasource(ctx, rq.GetDatasourceID())
	if err != nil {
		outcome = "failed"
		return nil, err
	}
	datasourceType = strings.ToLower(strings.TrimSpace(datasource.Type))
	if !b.limiter.Allow(rq.GetDatasourceID()) {
		outcome = "rate_limited"
		return nil, errno.ErrEvidenceRateLimited
	}

	start := rq.GetTimeRangeStart().AsTime()
	end := rq.GetTimeRangeEnd().AsTime()
	if err := b.validateTimeRange(start, end, b.guardrails.MaxLogsRange); err != nil {
		outcome = "invalid_argument"
		return nil, err
	}

	limit := rq.GetLimit()
	if limit <= 0 {
		limit = defaultLogsLimit
	}
	if limit > b.guardrails.MaxLogsLimit {
		outcome = "invalid_argument"
		return nil, errorsx.ErrInvalidArgument
	}

	timeout := b.guardrails.ClampDatasourceTimeout(datasource.TimeoutMs)
	queryResult, err := b.adapter.QueryLogsRange(
		ctx,
		datasource,
		integrationds.LogsRangeQuery{
			QueryText: strings.TrimSpace(rq.GetQueryText()),
			QueryJSON: rq.QueryJSON,
			Start:     start,
			End:       end,
			Limit:     limit,
			Timeout:   timeout,
		},
	)
	if err != nil {
		mapped := toDatasourceQueryError(err)
		if errors.Is(mapped, errno.ErrEvidenceQueryTimeout) {
			outcome = "timeout"
		} else {
			outcome = "dependency_error"
		}
		return nil, mapped
	}

	resultJSON, sizeBytes, truncated := b.normalizeResult(queryResult.ResultJSON, b.guardrails.MaxResultBytes)
	rowCount := queryResult.RowCount
	if rowCount > b.guardrails.MaxLogsRows {
		truncated = true
		resultJSON = b.truncatedResultJSON(resultJSON, "max_logs_rows_exceeded")
		sizeBytes = int64(len(resultJSON))
	}

	slog.InfoContext(ctx, "evidence logs query done",
		"request_id", contextx.RequestID(ctx),
		"incident_id", "",
		"job_id", "",
		"tool_call_id", "",
		"datasource_id", rq.GetDatasourceID(),
		"row_count", rowCount,
		"truncated", truncated,
	)

	return &v1.QueryLogsResponse{
		QueryResultJSON: resultJSON,
		ResultSizeBytes: sizeBytes,
		RowCount:        rowCount,
		IsTruncated:     truncated,
	}, nil
}

//nolint:gocognit,gocyclo // Save flow keeps idempotency and guardrails explicit.
func (b *evidenceBiz) Save(ctx context.Context, rq *v1.SaveEvidenceRequest) (*v1.SaveEvidenceResponse, error) {
	start := rq.GetTimeRangeStart().AsTime()
	end := rq.GetTimeRangeEnd().AsTime()
	if err := b.validateTimeRange(start, end, b.guardrails.MaxMetricsRange); err != nil {
		return nil, err
	}

	if rq.DatasourceID != nil && strings.TrimSpace(rq.GetDatasourceID()) != "" {
		if _, err := b.getActiveDatasource(ctx, rq.GetDatasourceID()); err != nil {
			return nil, err
		}
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
		"datasource_id", rq.GetDatasourceID(),
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
	if v := trimOptionalString(rq.DatasourceID); v != "" {
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

func (b *evidenceBiz) getActiveDatasource(ctx context.Context, datasourceID string) (*model.DatasourceM, error) {
	m, err := b.store.Datasource().Get(ctx, where.T(ctx).F("datasource_id", strings.TrimSpace(datasourceID)))
	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrDatasourceNotFound
		}
		return nil, errno.ErrDatasourceGetFailed
	}
	if !m.IsEnabled {
		return nil, errno.ErrDatasourceDisabled
	}
	return m, nil
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

func (b *evidenceBiz) normalizeResult(result map[string]any, maxBytes int) (string, int64, bool) {
	raw, err := json.Marshal(result)
	if err != nil {
		msg := fmt.Sprintf(`{"error":"marshal result failed: %s"}`, err.Error())
		return msg, int64(len(msg)), false
	}
	return b.normalizeRawResult(string(raw), maxBytes)
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

func (b *evidenceBiz) truncatedResultJSON(existing string, reason string) string {
	wrapped := map[string]any{
		"truncated": true,
		"reason":    reason,
		"preview":   existing,
	}
	out, err := json.Marshal(wrapped)
	if err != nil {
		return `{"truncated":true,"reason":"marshal_failed"}`
	}
	return string(out)
}

func toDatasourceQueryError(err error) error {
	if err == nil {
		return nil
	}

	var integrationErr *integrationds.QueryError
	if errors.As(err, &integrationErr) {
		switch integrationErr.Code {
		case integrationds.QueryErrorCodeTimeout:
			return errno.ErrEvidenceQueryTimeout
		case integrationds.QueryErrorCodeUnsupportedType:
			return errno.ErrDatasourceUnsupportedType
		default:
			return errno.ErrEvidenceQueryFailed
		}
	}

	return errno.ErrEvidenceQueryFailed
}

func hashQuery(datasourceID string, typ string, queryText string, queryJSON string, start time.Time, end time.Time) string {
	payload := strings.Join([]string{
		strings.TrimSpace(datasourceID),
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
