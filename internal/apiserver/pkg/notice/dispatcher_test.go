package notice

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"zk8s.com/rca-api/internal/apiserver/model"
	"zk8s.com/rca-api/internal/apiserver/store"
	"zk8s.com/rca-api/pkg/store/where"
)

func TestDispatchBestEffort_SuccessAndAudit(t *testing.T) {
	var hitCount atomic.Int32
	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitCount.Add(1)
		require.Equal(t, http.MethodPost, r.Method)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer mockSrv.Close()

	s := newNoticeDispatcherStore(t)
	ctx := context.Background()
	require.NoError(t, s.NoticeChannel().Create(ctx, &model.NoticeChannelM{
		Name:        "hook-1",
		Type:        "webhook",
		Enabled:     true,
		EndpointURL: mockSrv.URL,
		TimeoutMs:   1500,
	}))

	rootSummary := "root cause summary"
	DispatchBestEffort(ctx, s, DispatchRequest{
		EventType: EventTypeIncidentCreated,
		Incident: &model.IncidentM{
			IncidentID:       "incident-test-1",
			Namespace:        "default",
			Service:          "checkout",
			Severity:         "P1",
			RCAStatus:        "pending",
			RootCauseSummary: &rootSummary,
		},
		OccurredAt: time.Now().UTC(),
	})

	require.Equal(t, int32(1), hitCount.Load())
	total, list, err := s.NoticeDelivery().List(ctx, where.T(ctx).P(0, 20))
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Equal(t, DeliveryStatusSucceeded, list[0].Status)
	require.NotNil(t, list[0].ResponseCode)
	require.Equal(t, int32(200), *list[0].ResponseCode)
}

func TestDispatchBestEffort_TruncatesBodiesOnFailure(t *testing.T) {
	largeResp := strings.Repeat("x", ResponseBodyMaxBytes+1024)
	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(largeResp))
	}))
	defer mockSrv.Close()

	s := newNoticeDispatcherStore(t)
	ctx := context.Background()
	require.NoError(t, s.NoticeChannel().Create(ctx, &model.NoticeChannelM{
		Name:        "hook-2",
		Type:        "webhook",
		Enabled:     true,
		EndpointURL: mockSrv.URL,
		TimeoutMs:   1200,
	}))

	longSummary := strings.Repeat("r", RequestBodyMaxBytes+2048)
	DispatchBestEffort(ctx, s, DispatchRequest{
		EventType: EventTypeDiagnosisWritten,
		Incident: &model.IncidentM{
			IncidentID:       "incident-test-2",
			Namespace:        "default",
			Service:          "billing",
			Severity:         "P2",
			RCAStatus:        "done",
			RootCauseSummary: &longSummary,
		},
		JobID:               "ai-job-test-1",
		DiagnosisConfidence: 0.75,
		DiagnosisEvidenceID: []string{"evidence-1"},
		OccurredAt:          time.Now().UTC(),
	})

	total, list, err := s.NoticeDelivery().List(ctx, where.T(ctx).P(0, 20))
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Equal(t, DeliveryStatusFailed, list[0].Status)
	require.NotNil(t, list[0].ResponseCode)
	require.Equal(t, int32(500), *list[0].ResponseCode)
	require.NotNil(t, list[0].ResponseBody)
	require.Len(t, *list[0].ResponseBody, ResponseBodyMaxBytes)
	require.Len(t, list[0].RequestBody, RequestBodyMaxBytes)
}

func newNoticeDispatcherStore(t *testing.T) store.IStore {
	t.Helper()
	store.ResetForTest()
	t.Cleanup(store.ResetForTest)

	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.NoticeChannelM{}, &model.NoticeDeliveryM{}))
	return store.NewStore(db)
}
