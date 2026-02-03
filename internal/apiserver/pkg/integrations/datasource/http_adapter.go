package datasource

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/clients"
)

type datasourceHTTPClient interface {
	QueryPrometheusRange(ctx context.Context, ds *model.DatasourceM, promql string, start, end time.Time, stepSeconds int64) (map[string]any, int64, error)
	QueryLokiRange(ctx context.Context, ds *model.DatasourceM, queryText string, start, end time.Time, limit int64) (map[string]any, int64, error)
	QueryElasticsearch(ctx context.Context, ds *model.DatasourceM, queryText string, queryJSON *string, start, end time.Time, limit int64) (map[string]any, int64, error)
}

// HTTPAdapter bridges datasource HTTP client implementations into integration QueryAdapter boundary.
type HTTPAdapter struct {
	client datasourceHTTPClient
}

var _ QueryAdapter = (*HTTPAdapter)(nil)

// NewHTTPAdapter creates a datasource integration adapter backed by datasource HTTP client.
func NewHTTPAdapter() *HTTPAdapter {
	return NewHTTPAdapterWithClient(clients.NewDatasourceHTTPClient())
}

// NewHTTPAdapterWithClient creates adapter with injectable HTTP client dependency.
func NewHTTPAdapterWithClient(client datasourceHTTPClient) *HTTPAdapter {
	return &HTTPAdapter{client: client}
}

func (a *HTTPAdapter) QueryMetricsRange(
	ctx context.Context,
	ds *model.DatasourceM,
	rq MetricsRangeQuery,
) (*NormalizedQueryResult, error) {
	dsType := normalizeDatasourceType(ds)
	if dsType != "prometheus" {
		return nil, &QueryError{
			Code:           QueryErrorCodeUnsupportedType,
			DatasourceType: dsType,
		}
	}

	queryCtx, cancel := withOptionalTimeout(ctx, rq.Timeout)
	defer cancel()

	result, rowCount, err := a.client.QueryPrometheusRange(
		queryCtx,
		ds,
		strings.TrimSpace(rq.PromQL),
		rq.Start,
		rq.End,
		rq.StepSeconds,
	)
	if err != nil {
		return nil, classifyQueryError(dsType, err)
	}
	return &NormalizedQueryResult{
		ResultJSON: result,
		RowCount:   rowCount,
	}, nil
}

func (a *HTTPAdapter) QueryLogsRange(
	ctx context.Context,
	ds *model.DatasourceM,
	rq LogsRangeQuery,
) (*NormalizedQueryResult, error) {
	dsType := normalizeDatasourceType(ds)
	if dsType != "loki" && dsType != "elasticsearch" {
		return nil, &QueryError{
			Code:           QueryErrorCodeUnsupportedType,
			DatasourceType: dsType,
		}
	}

	queryCtx, cancel := withOptionalTimeout(ctx, rq.Timeout)
	defer cancel()

	queryText := strings.TrimSpace(rq.QueryText)
	var (
		result   map[string]any
		rowCount int64
		err      error
	)

	if dsType == "loki" {
		result, rowCount, err = a.client.QueryLokiRange(
			queryCtx,
			ds,
			queryText,
			rq.Start,
			rq.End,
			rq.Limit,
		)
	} else {
		queryJSON := normalizeOptionalQueryJSON(rq.QueryJSON)
		result, rowCount, err = a.client.QueryElasticsearch(
			queryCtx,
			ds,
			queryText,
			queryJSON,
			rq.Start,
			rq.End,
			rq.Limit,
		)
	}
	if err != nil {
		return nil, classifyQueryError(dsType, err)
	}
	return &NormalizedQueryResult{
		ResultJSON: result,
		RowCount:   rowCount,
	}, nil
}

func withOptionalTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

func normalizeOptionalQueryJSON(raw *string) *string {
	if raw == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*raw)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func normalizeDatasourceType(ds *model.DatasourceM) string {
	if ds == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(ds.Type))
}

func classifyQueryError(datasourceType string, err error) error {
	if err == nil {
		return nil
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return &QueryError{
			Code:           QueryErrorCodeTimeout,
			DatasourceType: datasourceType,
			Err:            err,
		}
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &QueryError{
			Code:           QueryErrorCodeTimeout,
			DatasourceType: datasourceType,
			Err:            err,
		}
	}

	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return &QueryError{
			Code:           QueryErrorCodeTimeout,
			DatasourceType: datasourceType,
			Err:            err,
		}
	}

	return &QueryError{
		Code:           QueryErrorCodeDependency,
		DatasourceType: datasourceType,
		Err:            err,
	}
}
