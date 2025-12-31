package evidence

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/policy"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

type mockDatasourceClient struct{}

func (m *mockDatasourceClient) QueryPrometheusRange(_ context.Context, _ *model.DatasourceM, _ string, _ time.Time, _ time.Time, _ int64) (map[string]any, int64, error) {
	return map[string]any{
		"status": "success",
		"data": map[string]any{
			"resultType": "matrix",
			"result": []any{
				map[string]any{"metric": map[string]any{"__name__": "up"}},
				map[string]any{"metric": map[string]any{"__name__": "http_requests_total"}},
			},
		},
	}, 2, nil
}

func (m *mockDatasourceClient) QueryLokiRange(_ context.Context, _ *model.DatasourceM, _ string, _ time.Time, _ time.Time, _ int64) (map[string]any, int64, error) {
	return map[string]any{}, 0, nil
}

func (m *mockDatasourceClient) QueryElasticsearch(_ context.Context, _ *model.DatasourceM, _ string, _ *string, _ time.Time, _ time.Time, _ int64) (map[string]any, int64, error) {
	return map[string]any{}, 0, nil
}

func TestEvidenceQuerySaveList_Idempotent(t *testing.T) {
	db := newTestDB(t)
	s := store.NewStore(db)
	guardrails := policy.DefaultEvidenceGuardrails()
	guardrails.QueryRatePerSecond = 1000
	guardrails.QueryRateBurst = 1000

	biz := NewWithDeps(s, guardrails, &mockDatasourceClient{})

	ds := &model.DatasourceM{
		Type:      "prometheus",
		Name:      "prom",
		BaseURL:   "http://mock-prometheus.local",
		AuthType:  "none",
		TimeoutMs: 5000,
		IsEnabled: true,
	}
	require.NoError(t, s.Datasource().Create(context.Background(), ds))
	require.NotEmpty(t, ds.DatasourceID)

	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-30 * time.Minute)

	queryResp, err := biz.QueryMetrics(context.Background(), &v1.QueryMetricsRequest{
		DatasourceID:   ds.DatasourceID,
		Promql:         "up",
		TimeRangeStart: toProtoTime(start),
		TimeRangeEnd:   toProtoTime(end),
		StepSeconds:    ptrInt64(30),
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), queryResp.RowCount)
	require.Contains(t, queryResp.QueryResultJSON, "success")

	saveResp1, err := biz.Save(context.Background(), &v1.SaveEvidenceRequest{
		IncidentID:     "incident-test",
		IdempotencyKey: ptrString("idem-evidence-1"),
		Type:           "metrics",
		DatasourceID:   ptrString(ds.DatasourceID),
		QueryText:      "up",
		TimeRangeStart: toProtoTime(start),
		TimeRangeEnd:   toProtoTime(end),
		ResultJSON:     queryResp.QueryResultJSON,
		Summary:        ptrString("up metric queried"),
		CreatedBy:      ptrString("system"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, saveResp1.EvidenceID)

	saveResp2, err := biz.Save(context.Background(), &v1.SaveEvidenceRequest{
		IncidentID:     "incident-test",
		IdempotencyKey: ptrString("idem-evidence-1"),
		Type:           "metrics",
		DatasourceID:   ptrString(ds.DatasourceID),
		QueryText:      "up",
		TimeRangeStart: toProtoTime(start),
		TimeRangeEnd:   toProtoTime(end),
		ResultJSON:     queryResp.QueryResultJSON,
		Summary:        ptrString("up metric queried"),
		CreatedBy:      ptrString("system"),
	})
	require.NoError(t, err)
	require.Equal(t, saveResp1.EvidenceID, saveResp2.EvidenceID)

	listResp, err := biz.ListByIncident(context.Background(), &v1.ListIncidentEvidenceRequest{
		IncidentID: "incident-test",
		Offset:     0,
		Limit:      20,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), listResp.TotalCount)
	require.Len(t, listResp.Evidence, 1)
	require.Equal(t, saveResp1.EvidenceID, listResp.Evidence[0].EvidenceID)
}

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.DatasourceM{}, &model.EvidenceM{}))
	return db
}

func toProtoTime(t time.Time) *timestamppb.Timestamp {
	return timestamppb.New(t)
}

func ptrString(v string) *string { return &v }
func ptrInt64(v int64) *int64    { return &v }
