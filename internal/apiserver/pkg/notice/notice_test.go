package notice

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

func TestDispatchBestEffort_EnqueuePending(t *testing.T) {
	var hitCount atomic.Int32
	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hitCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer mockSrv.Close()

	s := newNoticeTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.NoticeChannel().Create(ctx, &model.NoticeChannelM{
		Name:        "hook-enqueue",
		Type:        "webhook",
		Enabled:     true,
		EndpointURL: mockSrv.URL,
		TimeoutMs:   1500,
		MaxRetries:  3,
	}))

	longSummary := strings.Repeat("x", RequestBodyMaxBytes+1024)
	DispatchBestEffort(ctx, s, DispatchRequest{
		EventType: EventTypeDiagnosisWritten,
		Incident: &model.IncidentM{
			IncidentID:       "incident-enqueue-1",
			Namespace:        "default",
			Service:          "checkout",
			Severity:         "P1",
			RCAStatus:        "done",
			RootCauseSummary: &longSummary,
		},
		JobID:      "ai-job-enqueue-1",
		OccurredAt: time.Now().UTC(),
	})

	require.Equal(t, int32(0), hitCount.Load(), "dispatch should not perform network send in P1-3")

	total, list, err := s.NoticeDelivery().List(ctx, where.T(ctx).P(0, 20))
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Equal(t, DeliveryStatusPending, list[0].Status)
	require.Equal(t, int64(0), list[0].Attempts)
	require.Equal(t, int64(3), list[0].MaxAttempts)
	require.NotEmpty(t, list[0].IdempotencyKey)
	require.Len(t, list[0].RequestBody, RequestBodyMaxBytes)
	require.NotNil(t, list[0].SnapshotEndpointURL)
	require.Equal(t, mockSrv.URL, *list[0].SnapshotEndpointURL)
	require.NotNil(t, list[0].SnapshotTimeoutMs)
	require.Equal(t, int64(1500), *list[0].SnapshotTimeoutMs)
	require.Nil(t, list[0].SnapshotHeadersJSON)
}

func TestDispatchBestEffort_SelectorsRouting(t *testing.T) {
	s := newNoticeTestStore(t)
	ctx := context.Background()

	require.NoError(t, s.NoticeChannel().Create(ctx, &model.NoticeChannelM{
		Name:        "hook-all",
		Type:        "webhook",
		Enabled:     true,
		EndpointURL: "http://127.0.0.1:18081/all",
		TimeoutMs:   1000,
		MaxRetries:  3,
	}))
	require.NoError(t, s.NoticeChannel().Create(ctx, &model.NoticeChannelM{
		Name:        "hook-diagnosis-only",
		Type:        "webhook",
		Enabled:     true,
		EndpointURL: "http://127.0.0.1:18081/diag",
		SelectorsJSON: mustSelectorJSON(t, &model.NoticeSelectors{
			EventTypes: []string{EventTypeDiagnosisWritten},
		}),
		TimeoutMs:  1000,
		MaxRetries: 3,
	}))

	incident := &model.IncidentM{
		IncidentID:    "incident-selector-1",
		Namespace:     "default",
		Service:       "checkout",
		Severity:      "P1",
		RCAStatus:     "done",
		RootCauseType: strPtrNoticeTest("database"),
	}
	DispatchBestEffort(ctx, s, DispatchRequest{
		EventType:  EventTypeIncidentCreated,
		Incident:   incident,
		OccurredAt: time.Now().UTC(),
	})
	DispatchBestEffort(ctx, s, DispatchRequest{
		EventType:  EventTypeDiagnosisWritten,
		Incident:   incident,
		JobID:      "ai-job-selector-1",
		OccurredAt: time.Now().UTC(),
	})

	_, allDeliveries, err := s.NoticeDelivery().List(ctx, where.T(ctx).P(0, 50).F("incident_id", incident.IncidentID))
	require.NoError(t, err)
	require.Len(t, allDeliveries, 3)

	_, incidentCreatedDeliveries, err := s.NoticeDelivery().List(
		ctx,
		where.T(ctx).P(0, 50).F("incident_id", incident.IncidentID, "event_type", EventTypeIncidentCreated),
	)
	require.NoError(t, err)
	require.Len(t, incidentCreatedDeliveries, 1)

	_, diagnosisDeliveries, err := s.NoticeDelivery().List(
		ctx,
		where.T(ctx).P(0, 50).F("incident_id", incident.IncidentID, "event_type", EventTypeDiagnosisWritten),
	)
	require.NoError(t, err)
	require.Len(t, diagnosisDeliveries, 2)
}

func TestWorker_UsesSnapshotAfterChannelEndpointChange(t *testing.T) {
	var hitOld atomic.Int32
	var hitNew atomic.Int32
	oldSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hitOld.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer oldSrv.Close()
	newSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hitNew.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer newSrv.Close()

	s := newNoticeTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.NoticeChannel().Create(ctx, &model.NoticeChannelM{
		Name:        "hook-snapshot-old",
		Type:        "webhook",
		Enabled:     true,
		EndpointURL: oldSrv.URL,
		TimeoutMs:   1000,
		MaxRetries:  3,
		HeadersJSON: mustHeaderJSON(t, map[string]string{
			"Authorization": "Bearer old-token",
		}),
	}))

	incidentID := "incident-snapshot-1"
	DispatchBestEffort(ctx, s, DispatchRequest{
		EventType: EventTypeIncidentCreated,
		Incident: &model.IncidentM{
			IncidentID: incidentID,
			Namespace:  "default",
			Service:    "checkout",
			Severity:   "P1",
			RCAStatus:  "pending",
		},
		OccurredAt: time.Now().UTC(),
	})

	_, queuedDeliveries, err := s.NoticeDelivery().List(ctx, where.T(ctx).P(0, 20).F("incident_id", incidentID))
	require.NoError(t, err)
	require.Len(t, queuedDeliveries, 1)
	require.NotNil(t, queuedDeliveries[0].SnapshotEndpointURL)
	require.Equal(t, oldSrv.URL, *queuedDeliveries[0].SnapshotEndpointURL)

	channel, err := s.NoticeChannel().Get(ctx, where.T(ctx).F("channel_id", queuedDeliveries[0].ChannelID))
	require.NoError(t, err)
	channel.EndpointURL = newSrv.URL
	require.NoError(t, s.NoticeChannel().Update(ctx, channel))

	worker := NewWorker(s, WorkerOptions{
		WorkerID: "test-worker-snapshot",
	})
	_, err = worker.RunOnce(ctx)
	require.NoError(t, err)

	_, finalDeliveries, err := s.NoticeDelivery().List(ctx, where.T(ctx).P(0, 20).F("incident_id", incidentID))
	require.NoError(t, err)
	require.Len(t, finalDeliveries, 1)
	require.Equal(t, DeliveryStatusSucceeded, finalDeliveries[0].Status)
	require.Equal(t, int32(1), hitOld.Load())
	require.Equal(t, int32(0), hitNew.Load())
}

func TestWorker_FailFastOnSecretFingerprintMismatch(t *testing.T) {
	var hitCount atomic.Int32
	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hitCount.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer mockSrv.Close()

	s := newNoticeTestStore(t)
	ctx := context.Background()
	secretS1 := "secret-s1"
	require.NoError(t, s.NoticeChannel().Create(ctx, &model.NoticeChannelM{
		Name:        "hook-secret-mismatch",
		Type:        "webhook",
		Enabled:     true,
		EndpointURL: mockSrv.URL,
		Secret:      &secretS1,
		TimeoutMs:   1000,
		MaxRetries:  3,
	}))

	incidentID := "incident-secret-mismatch-1"
	DispatchBestEffort(ctx, s, DispatchRequest{
		EventType: EventTypeIncidentCreated,
		Incident: &model.IncidentM{
			IncidentID: incidentID,
			Namespace:  "default",
			Service:    "checkout",
			Severity:   "P1",
			RCAStatus:  "pending",
		},
		OccurredAt: time.Now().UTC(),
	})

	_, queuedDeliveries, err := s.NoticeDelivery().List(ctx, where.T(ctx).P(0, 20).F("incident_id", incidentID))
	require.NoError(t, err)
	require.Len(t, queuedDeliveries, 1)
	require.NotNil(t, queuedDeliveries[0].SnapshotSecretFingerprint)

	channel, err := s.NoticeChannel().Get(ctx, where.T(ctx).F("channel_id", queuedDeliveries[0].ChannelID))
	require.NoError(t, err)
	secretS2 := "secret-s2"
	channel.Secret = &secretS2
	require.NoError(t, s.NoticeChannel().Update(ctx, channel))

	worker := NewWorker(s, WorkerOptions{
		WorkerID: "test-worker-secret-mismatch",
	})
	_, err = worker.RunOnce(ctx)
	require.NoError(t, err)

	got, err := s.NoticeDelivery().Get(ctx, where.T(ctx).F("delivery_id", queuedDeliveries[0].DeliveryID))
	require.NoError(t, err)
	require.Equal(t, DeliveryStatusFailed, got.Status)
	require.Equal(t, int64(1), got.Attempts)
	require.NotNil(t, got.Error)
	require.Contains(t, *got.Error, "secret_fingerprint_mismatch")
	require.Contains(t, *got.Error, "replay?useLatestChannel=1")
	require.LessOrEqual(t, len(*got.Error), ErrorBodyMaxBytes)
	require.Equal(t, int32(0), hitCount.Load())
}

func TestWorker_RetryThenSucceed(t *testing.T) {
	var hitCount atomic.Int32
	var headersMu sync.Mutex
	idemKeys := make([]string, 0, 4)

	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headersMu.Lock()
		idemKeys = append(idemKeys, strings.TrimSpace(r.Header.Get("Idempotency-Key")))
		headersMu.Unlock()

		n := hitCount.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"ok":false}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer mockSrv.Close()

	s := newNoticeTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.NoticeChannel().Create(ctx, &model.NoticeChannelM{
		Name:        "hook-retry",
		Type:        "webhook",
		Enabled:     true,
		EndpointURL: mockSrv.URL,
		TimeoutMs:   1000,
		MaxRetries:  3,
	}))

	incidentID := "incident-retry-1"
	DispatchBestEffort(ctx, s, DispatchRequest{
		EventType: EventTypeIncidentCreated,
		Incident: &model.IncidentM{
			IncidentID: incidentID,
			Namespace:  "default",
			Service:    "checkout",
			Severity:   "P1",
			RCAStatus:  "pending",
		},
		OccurredAt: time.Now().UTC(),
	})

	worker := NewWorker(s, WorkerOptions{
		WorkerID:     "test-worker-retry",
		BatchSize:    8,
		PollInterval: 10 * time.Millisecond,
		LockTimeout:  2 * time.Second,
		BaseBackoff:  20 * time.Millisecond,
		CapBackoff:   50 * time.Millisecond,
		JitterMax:    0,
	})

	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := worker.RunOnce(ctx)
		require.NoError(t, err)

		_, list, err := s.NoticeDelivery().List(ctx, where.T(ctx).P(0, 20).F("incident_id", incidentID))
		require.NoError(t, err)
		require.Len(t, list, 1)
		if list[0].Status == DeliveryStatusSucceeded {
			require.GreaterOrEqual(t, list[0].Attempts, int64(3))
			break
		}

		if time.Now().After(deadline) {
			t.Fatalf("delivery did not reach succeeded before timeout; status=%s attempts=%d", list[0].Status, list[0].Attempts)
		}
		time.Sleep(20 * time.Millisecond)
	}

	require.GreaterOrEqual(t, hitCount.Load(), int32(3))
	headersMu.Lock()
	defer headersMu.Unlock()
	require.GreaterOrEqual(t, len(idemKeys), 3)
	for _, key := range idemKeys {
		require.NotEmpty(t, key)
	}
}

func TestWorker_NonRetryable4xxFailed(t *testing.T) {
	var hitCount atomic.Int32
	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hitCount.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false}`))
	}))
	defer mockSrv.Close()

	s := newNoticeTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.NoticeChannel().Create(ctx, &model.NoticeChannelM{
		Name:        "hook-4xx",
		Type:        "webhook",
		Enabled:     true,
		EndpointURL: mockSrv.URL,
		TimeoutMs:   1000,
		MaxRetries:  3,
	}))

	incidentID := "incident-4xx-1"
	DispatchBestEffort(ctx, s, DispatchRequest{
		EventType: EventTypeIncidentCreated,
		Incident: &model.IncidentM{
			IncidentID: incidentID,
			Namespace:  "default",
			Service:    "checkout",
			Severity:   "P1",
			RCAStatus:  "pending",
		},
		OccurredAt: time.Now().UTC(),
	})

	worker := NewWorker(s, WorkerOptions{
		WorkerID:    "test-worker-4xx",
		BatchSize:   8,
		LockTimeout: 2 * time.Second,
		BaseBackoff: 10 * time.Millisecond,
		CapBackoff:  20 * time.Millisecond,
	})
	_, err := worker.RunOnce(ctx)
	require.NoError(t, err)

	_, list, err := s.NoticeDelivery().List(ctx, where.T(ctx).P(0, 20).F("incident_id", incidentID))
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, DeliveryStatusFailed, list[0].Status)
	require.Equal(t, int64(1), list[0].Attempts)
	require.Equal(t, int32(1), hitCount.Load())
}

func TestWorker_SkipCanceledAfterClaim(t *testing.T) {
	var hitCount atomic.Int32
	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hitCount.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer mockSrv.Close()

	s := newNoticeTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.NoticeChannel().Create(ctx, &model.NoticeChannelM{
		Name:        "hook-cancel-race",
		Type:        "webhook",
		Enabled:     true,
		EndpointURL: mockSrv.URL,
		TimeoutMs:   1000,
		MaxRetries:  3,
	}))

	DispatchBestEffort(ctx, s, DispatchRequest{
		EventType: EventTypeIncidentCreated,
		Incident: &model.IncidentM{
			IncidentID: "incident-cancel-race-1",
			Namespace:  "default",
			Service:    "checkout",
			Severity:   "P1",
			RCAStatus:  "pending",
		},
		OccurredAt: time.Now().UTC(),
	})

	claimed, err := s.NoticeDelivery().ClaimPending(ctx, "test-worker-cancel-race", 1, time.Now().UTC(), 2*time.Second)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	canceled, err := s.NoticeDelivery().Cancel(ctx, claimed[0].DeliveryID, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, DeliveryStatusCanceled, canceled.Status)

	worker := NewWorker(s, WorkerOptions{
		WorkerID: "test-worker-cancel-race",
	})
	require.NoError(t, worker.processDelivery(ctx, claimed[0]))
	require.Equal(t, int32(0), hitCount.Load())

	got, err := s.NoticeDelivery().Get(ctx, where.T(ctx).F("delivery_id", claimed[0].DeliveryID))
	require.NoError(t, err)
	require.Equal(t, DeliveryStatusCanceled, got.Status)
	require.Equal(t, int64(0), got.Attempts)
}

func newNoticeTestStore(t *testing.T) store.IStore {
	t.Helper()
	store.ResetForTest()
	t.Cleanup(store.ResetForTest)

	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.NoticeChannelM{}, &model.NoticeDeliveryM{}))
	return store.NewStore(db)
}

func mustSelectorJSON(t *testing.T, selectors *model.NoticeSelectors) *string {
	t.Helper()
	raw, err := json.Marshal(selectors)
	require.NoError(t, err)
	out := string(raw)
	return &out
}

func mustHeaderJSON(t *testing.T, headers map[string]string) *string {
	t.Helper()
	raw, err := json.Marshal(headers)
	require.NoError(t, err)
	out := string(raw)
	return &out
}

func strPtrNoticeTest(v string) *string { return &v }
