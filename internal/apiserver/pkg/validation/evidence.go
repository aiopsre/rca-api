package validation

import (
	"context"
	"strings"
	"time"

	"github.com/onexstack/onexstack/pkg/errorsx"

	v1 "zk8s.com/rca-api/pkg/api/apiserver/v1"
)

const (
	defaultEvidenceListLimit = int64(20)
	maxEvidenceListLimit     = int64(200)
	defaultQueryLogsLimit    = int64(200)
	maxQueryLogsLimit        = int64(500)
	maxStepSeconds           = int64(300)
	maxQueryLength           = 4096
	maxMetricsRange          = 24 * time.Hour
	maxLogsRange             = 6 * time.Hour
)

var allowedEvidenceTypes = map[string]struct{}{
	"metrics": {},
	"logs":    {},
	"traces":  {},
	"k8s":     {},
	"script":  {},
}

func (v *Validator) ValidateQueryMetricsRequest(ctx context.Context, rq *v1.QueryMetricsRequest) error {
	_ = ctx
	if strings.TrimSpace(rq.GetDatasourceID()) == "" || strings.TrimSpace(rq.GetPromql()) == "" {
		return errorsx.ErrInvalidArgument
	}
	if len(strings.TrimSpace(rq.GetPromql())) > maxQueryLength {
		return errorsx.ErrInvalidArgument
	}
	start := rq.GetTimeRangeStart().AsTime()
	end := rq.GetTimeRangeEnd().AsTime()
	if err := validateRange(start, end, maxMetricsRange); err != nil {
		return err
	}
	if rq.StepSeconds != nil && (rq.GetStepSeconds() <= 0 || rq.GetStepSeconds() > maxStepSeconds) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func (v *Validator) ValidateQueryLogsRequest(ctx context.Context, rq *v1.QueryLogsRequest) error {
	_ = ctx
	if strings.TrimSpace(rq.GetDatasourceID()) == "" || strings.TrimSpace(rq.GetQueryText()) == "" {
		return errorsx.ErrInvalidArgument
	}
	if len(strings.TrimSpace(rq.GetQueryText())) > maxQueryLength {
		return errorsx.ErrInvalidArgument
	}
	start := rq.GetTimeRangeStart().AsTime()
	end := rq.GetTimeRangeEnd().AsTime()
	if err := validateRange(start, end, maxLogsRange); err != nil {
		return err
	}
	if rq.Limit == nil || rq.GetLimit() <= 0 {
		rq.Limit = ptrEvidenceInt64(defaultQueryLogsLimit)
	}
	if rq.GetLimit() > maxQueryLogsLimit {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func (v *Validator) ValidateSaveEvidenceRequest(ctx context.Context, rq *v1.SaveEvidenceRequest) error {
	_ = ctx
	if strings.TrimSpace(rq.GetIncidentID()) == "" {
		return errorsx.ErrInvalidArgument
	}
	if strings.TrimSpace(rq.GetQueryText()) == "" || strings.TrimSpace(rq.GetResultJSON()) == "" {
		return errorsx.ErrInvalidArgument
	}
	if len(strings.TrimSpace(rq.GetQueryText())) > maxQueryLength {
		return errorsx.ErrInvalidArgument
	}
	typ := strings.ToLower(strings.TrimSpace(rq.GetType()))
	if _, ok := allowedEvidenceTypes[typ]; !ok {
		return errorsx.ErrInvalidArgument
	}
	start := rq.GetTimeRangeStart().AsTime()
	end := rq.GetTimeRangeEnd().AsTime()
	if err := validateRange(start, end, maxMetricsRange); err != nil {
		return err
	}
	if rq.IdempotencyKey != nil && len(strings.TrimSpace(rq.GetIdempotencyKey())) > 128 {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func (v *Validator) ValidateGetEvidenceRequest(ctx context.Context, rq *v1.GetEvidenceRequest) error {
	_ = ctx
	if strings.TrimSpace(rq.GetEvidenceID()) == "" {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func (v *Validator) ValidateListIncidentEvidenceRequest(ctx context.Context, rq *v1.ListIncidentEvidenceRequest) error {
	_ = ctx
	if strings.TrimSpace(rq.GetIncidentID()) == "" {
		return errorsx.ErrInvalidArgument
	}
	if rq.GetOffset() < 0 {
		return errorsx.ErrInvalidArgument
	}
	if rq.GetLimit() <= 0 {
		rq.Limit = defaultEvidenceListLimit
	}
	if rq.GetLimit() > maxEvidenceListLimit {
		return errorsx.ErrInvalidArgument
	}
	if rq.Type != nil && strings.TrimSpace(rq.GetType()) != "" {
		typ := strings.ToLower(strings.TrimSpace(rq.GetType()))
		if _, ok := allowedEvidenceTypes[typ]; !ok {
			return errorsx.ErrInvalidArgument
		}
	}
	return nil
}

func validateRange(start, end time.Time, maxRange time.Duration) error {
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

func ptrEvidenceInt64(v int64) *int64 { return &v }
