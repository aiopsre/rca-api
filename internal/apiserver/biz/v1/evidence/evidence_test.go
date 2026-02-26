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
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

func TestEvidenceSaveGetList_Basic(t *testing.T) {
	db := newTestDB(t)
	s := store.NewStore(db)
	biz := New(s)

	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-30 * time.Minute)

	// Test Save
	saveResp, err := biz.Save(context.Background(), &v1.SaveEvidenceRequest{
		IncidentID:     "incident-test",
		IdempotencyKey: ptrString("idem-evidence-1"),
		Type:           "metrics",
		DatasourceID:   ptrString("external-prometheus"),
		QueryText:      "up",
		TimeRangeStart: toProtoTime(start),
		TimeRangeEnd:   toProtoTime(end),
		ResultJSON:     `{"status":"success","data":{"resultType":"matrix","result":[]}}`,
		Summary:        ptrString("up metric queried"),
		CreatedBy:      ptrString("system"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, saveResp.EvidenceID)

	// Test Get
	getResp, err := biz.Get(context.Background(), &v1.GetEvidenceRequest{
		EvidenceID: saveResp.EvidenceID,
	})
	require.NoError(t, err)
	require.Equal(t, saveResp.EvidenceID, getResp.Evidence.EvidenceID)
	require.Equal(t, "incident-test", getResp.Evidence.IncidentID)

	// Test ListByIncident
	listResp, err := biz.ListByIncident(context.Background(), &v1.ListIncidentEvidenceRequest{
		IncidentID: "incident-test",
		Offset:     0,
		Limit:      20,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), listResp.TotalCount)
	require.Len(t, listResp.Evidence, 1)
	require.Equal(t, saveResp.EvidenceID, listResp.Evidence[0].EvidenceID)
}

func TestEvidenceSave_Idempotent(t *testing.T) {
	db := newTestDB(t)
	s := store.NewStore(db)
	biz := New(s)

	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-30 * time.Minute)

	// First save
	saveResp1, err := biz.Save(context.Background(), &v1.SaveEvidenceRequest{
		IncidentID:     "incident-idem",
		IdempotencyKey: ptrString("idem-key-1"),
		Type:           "logs",
		QueryText:      "error",
		TimeRangeStart: toProtoTime(start),
		TimeRangeEnd:   toProtoTime(end),
		ResultJSON:     `{"logs":[]}`,
		CreatedBy:      ptrString("system"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, saveResp1.EvidenceID)

	// Second save with same idempotency key should return same ID
	saveResp2, err := biz.Save(context.Background(), &v1.SaveEvidenceRequest{
		IncidentID:     "incident-idem",
		IdempotencyKey: ptrString("idem-key-1"),
		Type:           "logs",
		QueryText:      "error",
		TimeRangeStart: toProtoTime(start),
		TimeRangeEnd:   toProtoTime(end),
		ResultJSON:     `{"logs":[]}`,
		CreatedBy:      ptrString("system"),
	})
	require.NoError(t, err)
	require.Equal(t, saveResp1.EvidenceID, saveResp2.EvidenceID)
}

func TestEvidenceSearch_ByDatasourceRef(t *testing.T) {
	db := newTestDB(t)
	s := store.NewStore(db)
	biz := New(s)

	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-30 * time.Minute)

	// Create two evidence with different datasource refs
	_, err := biz.Save(context.Background(), &v1.SaveEvidenceRequest{
		IncidentID:     "incident-search",
		Type:           "metrics",
		DatasourceID:   ptrString("prometheus-1"),
		QueryText:      "up",
		TimeRangeStart: toProtoTime(start),
		TimeRangeEnd:   toProtoTime(end),
		ResultJSON:     `{"result":[]}`,
		CreatedBy:      ptrString("system"),
	})
	require.NoError(t, err)

	_, err = biz.Save(context.Background(), &v1.SaveEvidenceRequest{
		IncidentID:     "incident-search",
		Type:           "logs",
		DatasourceID:   ptrString("loki-1"),
		QueryText:      "error",
		TimeRangeStart: toProtoTime(start),
		TimeRangeEnd:   toProtoTime(end),
		ResultJSON:     `{"logs":[]}`,
		CreatedBy:      ptrString("system"),
	})
	require.NoError(t, err)

	// Search by datasource_ref
	searchResp, err := biz.Search(context.Background(), &SearchEvidenceRequest{
		IncidentID:    ptrString("incident-search"),
		DatasourceRef: ptrString("prometheus-1"),
		Offset:        0,
		Limit:         10,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), searchResp.TotalCount)
	require.Equal(t, "prometheus-1", searchResp.Evidence[0].GetDatasourceID())
}

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.EvidenceM{}))
	return db
}

func toProtoTime(t time.Time) *timestamppb.Timestamp {
	return timestamppb.New(t)
}

func ptrString(v string) *string { return &v }