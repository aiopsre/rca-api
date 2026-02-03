package datasource

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
)

// MetricsRangeQuery is the integration-facing request model for metrics queries.
type MetricsRangeQuery struct {
	PromQL      string
	Start       time.Time
	End         time.Time
	StepSeconds int64
	Timeout     time.Duration
}

// LogsRangeQuery is the integration-facing request model for logs queries.
type LogsRangeQuery struct {
	QueryText string
	QueryJSON *string
	Start     time.Time
	End       time.Time
	Limit     int64
	Timeout   time.Duration
}

// NormalizedQueryResult keeps a datasource-agnostic response envelope for evidence domain.
type NormalizedQueryResult struct {
	ResultJSON map[string]any
	RowCount   int64
}

// QueryAdapter defines datasource integration capabilities consumed by evidence domain.
type QueryAdapter interface {
	QueryMetricsRange(ctx context.Context, ds *model.DatasourceM, rq MetricsRangeQuery) (*NormalizedQueryResult, error)
	QueryLogsRange(ctx context.Context, ds *model.DatasourceM, rq LogsRangeQuery) (*NormalizedQueryResult, error)
}

// QueryErrorCode is a stable integration-layer error classification.
type QueryErrorCode string

const (
	QueryErrorCodeTimeout         QueryErrorCode = "timeout"
	QueryErrorCodeDependency      QueryErrorCode = "dependency_error"
	QueryErrorCodeUnsupportedType QueryErrorCode = "unsupported_datasource_type"
)

// QueryError wraps integration adapter failures with stable classification.
type QueryError struct {
	Code           QueryErrorCode
	DatasourceType string
	Err            error
}

func (e *QueryError) Error() string {
	dsType := strings.TrimSpace(e.DatasourceType)
	if e.Err == nil {
		if dsType == "" {
			return fmt.Sprintf("datasource query failed: code=%s", e.Code)
		}
		return fmt.Sprintf("datasource query failed: code=%s datasource_type=%s", e.Code, dsType)
	}
	if dsType == "" {
		return fmt.Sprintf("datasource query failed: code=%s err=%v", e.Code, e.Err)
	}
	return fmt.Sprintf("datasource query failed: code=%s datasource_type=%s err=%v", e.Code, dsType, e.Err)
}

func (e *QueryError) Unwrap() error {
	return e.Err
}
