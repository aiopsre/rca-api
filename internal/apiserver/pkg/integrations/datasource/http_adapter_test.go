package datasource

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
)

type fakeDatasourceHTTPClient struct {
	queryPrometheusRangeFn func(ctx context.Context, ds *model.DatasourceM, promql string, start, end time.Time, stepSeconds int64) (map[string]any, int64, error)
	queryLokiRangeFn       func(ctx context.Context, ds *model.DatasourceM, queryText string, start, end time.Time, limit int64) (map[string]any, int64, error)
	queryElasticsearchFn   func(ctx context.Context, ds *model.DatasourceM, queryText string, queryJSON *string, start, end time.Time, limit int64) (map[string]any, int64, error)
}

func (f *fakeDatasourceHTTPClient) QueryPrometheusRange(
	ctx context.Context,
	ds *model.DatasourceM,
	promql string,
	start time.Time,
	end time.Time,
	stepSeconds int64,
) (map[string]any, int64, error) {
	if f.queryPrometheusRangeFn != nil {
		return f.queryPrometheusRangeFn(ctx, ds, promql, start, end, stepSeconds)
	}
	return map[string]any{}, 0, nil
}

func (f *fakeDatasourceHTTPClient) QueryLokiRange(
	ctx context.Context,
	ds *model.DatasourceM,
	queryText string,
	start time.Time,
	end time.Time,
	limit int64,
) (map[string]any, int64, error) {
	if f.queryLokiRangeFn != nil {
		return f.queryLokiRangeFn(ctx, ds, queryText, start, end, limit)
	}
	return map[string]any{}, 0, nil
}

func (f *fakeDatasourceHTTPClient) QueryElasticsearch(
	ctx context.Context,
	ds *model.DatasourceM,
	queryText string,
	queryJSON *string,
	start time.Time,
	end time.Time,
	limit int64,
) (map[string]any, int64, error) {
	if f.queryElasticsearchFn != nil {
		return f.queryElasticsearchFn(ctx, ds, queryText, queryJSON, start, end, limit)
	}
	return map[string]any{}, 0, nil
}

func TestHTTPAdapter_QueryLogsRange_Loki(t *testing.T) {
	start := time.Now().UTC().Add(-10 * time.Minute)
	end := time.Now().UTC()
	client := &fakeDatasourceHTTPClient{
		queryLokiRangeFn: func(
			_ context.Context,
			ds *model.DatasourceM,
			queryText string,
			gotStart, gotEnd time.Time,
			limit int64,
		) (map[string]any, int64, error) {
			require.Equal(t, "loki", ds.Type)
			require.Equal(t, "{app=\"demo\"}", queryText)
			require.Equal(t, start, gotStart)
			require.Equal(t, end, gotEnd)
			require.Equal(t, int64(200), limit)
			return map[string]any{"status": "ok"}, 3, nil
		},
	}

	adapter := NewHTTPAdapterWithClient(client)
	result, err := adapter.QueryLogsRange(
		context.Background(),
		&model.DatasourceM{Type: "loki"},
		LogsRangeQuery{
			QueryText: "{app=\"demo\"}",
			Start:     start,
			End:       end,
			Limit:     200,
			Timeout:   time.Second,
		},
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, int64(3), result.RowCount)
	require.Equal(t, "ok", result.ResultJSON["status"])
}

func TestHTTPAdapter_QueryLogsRange_UnsupportedDatasourceType(t *testing.T) {
	adapter := NewHTTPAdapterWithClient(&fakeDatasourceHTTPClient{})

	result, err := adapter.QueryLogsRange(
		context.Background(),
		&model.DatasourceM{Type: "prometheus"},
		LogsRangeQuery{
			QueryText: "error",
			Start:     time.Now().UTC().Add(-5 * time.Minute),
			End:       time.Now().UTC(),
			Limit:     10,
			Timeout:   time.Second,
		},
	)
	require.Nil(t, result)
	require.Error(t, err)
	var queryErr *QueryError
	require.True(t, errors.As(err, &queryErr))
	require.Equal(t, QueryErrorCodeUnsupportedType, queryErr.Code)
}

func TestHTTPAdapter_QueryMetricsRange_TimeoutClassification(t *testing.T) {
	client := &fakeDatasourceHTTPClient{
		queryPrometheusRangeFn: func(
			_ context.Context,
			_ *model.DatasourceM,
			_ string,
			_, _ time.Time,
			_ int64,
		) (map[string]any, int64, error) {
			return nil, 0, timeoutErr{}
		},
	}
	adapter := NewHTTPAdapterWithClient(client)

	result, err := adapter.QueryMetricsRange(
		context.Background(),
		&model.DatasourceM{Type: "prometheus"},
		MetricsRangeQuery{
			PromQL:      "up",
			Start:       time.Now().UTC().Add(-5 * time.Minute),
			End:         time.Now().UTC(),
			StepSeconds: 30,
			Timeout:     time.Second,
		},
	)
	require.Nil(t, result)
	require.Error(t, err)
	var queryErr *QueryError
	require.True(t, errors.As(err, &queryErr))
	require.Equal(t, QueryErrorCodeTimeout, queryErr.Code)
}

type timeoutErr struct{}

var _ net.Error = timeoutErr{}

func (timeoutErr) Error() string   { return "timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }
