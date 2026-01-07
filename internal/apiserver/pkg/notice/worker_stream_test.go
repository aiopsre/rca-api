package notice

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

func TestWorker_RunOnce_StreamClaimThenAck(t *testing.T) {
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
		Name:        "hook-stream-ack",
		Type:        "webhook",
		Enabled:     true,
		EndpointURL: mockSrv.URL,
		TimeoutMs:   1000,
		MaxRetries:  3,
	}))
	DispatchBestEffort(ctx, s, DispatchRequest{
		EventType: EventTypeIncidentCreated,
		Incident: &model.IncidentM{
			IncidentID: "incident-stream-ack-1",
			Namespace:  "default",
			Service:    "checkout",
			Severity:   "P1",
			RCAStatus:  "pending",
		},
		OccurredAt: time.Now().UTC(),
	})

	_, list, err := s.NoticeDelivery().List(ctx, where.T(ctx).P(0, 20).F("incident_id", "incident-stream-ack-1"))
	require.NoError(t, err)
	require.Len(t, list, 1)

	stream := &fakeNoticeStreamConsumer{
		enabled: true,
		newMessages: []NoticeStreamMessage{
			{StreamID: "1-0", DeliveryID: list[0].DeliveryID},
		},
	}
	worker := NewWorker(s, WorkerOptions{
		WorkerID:       "test-worker-stream-ack",
		BatchSize:      4,
		LockTimeout:    2 * time.Second,
		StreamConsumer: stream,
	})

	processed, err := worker.RunOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, processed)
	require.Equal(t, int32(1), hitCount.Load())
	require.Equal(t, []string{"1-0"}, stream.AckedIDs())

	got, err := s.NoticeDelivery().Get(ctx, where.T(ctx).F("delivery_id", list[0].DeliveryID))
	require.NoError(t, err)
	require.Equal(t, DeliveryStatusSucceeded, got.Status)
}

func TestWorker_RunOnce_StreamSkipNonPendingAck(t *testing.T) {
	s := newNoticeTestStore(t)
	ctx := context.Background()
	delivery := &model.NoticeDeliveryM{
		ChannelID:      "notice-channel-stream-skip",
		EventType:      EventTypeIncidentCreated,
		IncidentID:     strPtrNoticeTest("incident-stream-skip-1"),
		RequestBody:    `{"event_type":"incident_created"}`,
		Status:         DeliveryStatusSucceeded,
		Attempts:       1,
		MaxAttempts:    3,
		NextRetryAt:    time.Now().UTC(),
		IdempotencyKey: "idem-stream-skip-1",
	}
	require.NoError(t, s.NoticeDelivery().Create(ctx, delivery))

	stream := &fakeNoticeStreamConsumer{
		enabled: true,
		newMessages: []NoticeStreamMessage{
			{StreamID: "2-0", DeliveryID: delivery.DeliveryID},
		},
	}
	worker := NewWorker(s, WorkerOptions{
		WorkerID:       "test-worker-stream-skip",
		BatchSize:      4,
		LockTimeout:    2 * time.Second,
		StreamConsumer: stream,
	})

	processed, err := worker.RunOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, processed)
	require.Equal(t, []string{"2-0"}, stream.AckedIDs())

	got, err := s.NoticeDelivery().Get(ctx, where.T(ctx).F("delivery_id", delivery.DeliveryID))
	require.NoError(t, err)
	require.Equal(t, DeliveryStatusSucceeded, got.Status)
	require.Equal(t, int64(1), got.Attempts)
}

func TestWorker_RunOnce_StreamReadErrorFallbackToDB(t *testing.T) {
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
		Name:        "hook-stream-fallback",
		Type:        "webhook",
		Enabled:     true,
		EndpointURL: mockSrv.URL,
		TimeoutMs:   1000,
		MaxRetries:  3,
	}))
	DispatchBestEffort(ctx, s, DispatchRequest{
		EventType: EventTypeIncidentCreated,
		Incident: &model.IncidentM{
			IncidentID: "incident-stream-fallback-1",
			Namespace:  "default",
			Service:    "checkout",
			Severity:   "P1",
			RCAStatus:  "pending",
		},
		OccurredAt: time.Now().UTC(),
	})

	stream := &fakeNoticeStreamConsumer{
		enabled: true,
		readErr: context.DeadlineExceeded,
	}
	worker := NewWorker(s, WorkerOptions{
		WorkerID:       "test-worker-stream-fallback",
		BatchSize:      4,
		LockTimeout:    2 * time.Second,
		StreamConsumer: stream,
	})

	processed, err := worker.RunOnce(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, processed)
	require.Equal(t, int32(1), hitCount.Load())
	require.Empty(t, stream.AckedIDs())
}

type fakeNoticeStreamConsumer struct {
	enabled bool

	reclaimMessages []NoticeStreamMessage
	newMessages     []NoticeStreamMessage

	reclaimErr error
	readErr    error
	ackErr     error

	mu      sync.Mutex
	ackedID []string
}

func (f *fakeNoticeStreamConsumer) Enabled() bool {
	return f != nil && f.enabled
}

func (f *fakeNoticeStreamConsumer) ReadNew(context.Context, string, int64, time.Duration) ([]NoticeStreamMessage, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]NoticeStreamMessage(nil), f.newMessages...)
	f.newMessages = nil
	return out, nil
}

func (f *fakeNoticeStreamConsumer) ClaimPendingIdle(context.Context, string, int64, time.Duration) ([]NoticeStreamMessage, error) {
	if f.reclaimErr != nil {
		return nil, f.reclaimErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]NoticeStreamMessage(nil), f.reclaimMessages...)
	f.reclaimMessages = nil
	return out, nil
}

func (f *fakeNoticeStreamConsumer) Ack(_ context.Context, streamIDs ...string) error {
	if f.ackErr != nil {
		return f.ackErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ackedID = append(f.ackedID, streamIDs...)
	return nil
}

func (f *fakeNoticeStreamConsumer) AckedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ackedID...)
}
