package alert_event

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	incidentv1 "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/incident"
	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

func TestAlertEventBiz_P0Policies(t *testing.T) {
	db := newAlertEventTestDB(t)
	s := store.NewStore(db)
	biz := New(s)
	incBiz := incidentv1.New(s)
	ctx := context.Background()

	reset := func(t *testing.T) {
		t.Helper()
		require.NoError(t, db.Exec("DELETE FROM alert_events_history").Error)
		require.NoError(t, db.Exec("DELETE FROM incidents").Error)
		require.NoError(t, db.Exec("DELETE FROM silences").Error)
	}

	base := time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC)

	t.Run("ingest idempotency keeps one current and one incident", func(t *testing.T) {
		reset(t)

		req := &v1.IngestAlertEventRequest{
			IdempotencyKey: ptrAlertString("idem-alert-1"),
			Fingerprint:    ptrAlertString("fp-idem"),
			Status:         "firing",
			Severity:       "P1",
			Service:        ptrAlertString("checkout"),
			Cluster:        ptrAlertString("prod-a"),
			Namespace:      ptrAlertString("shop"),
			Workload:       ptrAlertString("checkout-api"),
			LastSeenAt:     timestamppb.New(base),
			LabelsJSON:     ptrAlertString(`{"alertname":"HTTP5xxHigh","service":"checkout","cluster":"prod-a","namespace":"shop","pod":"checkout-abc"}`),
		}

		first, err := biz.Ingest(ctx, req)
		require.NoError(t, err)
		require.NotEmpty(t, first.EventID)
		require.NotNil(t, first.IncidentID)

		second, err := biz.Ingest(ctx, req)
		require.NoError(t, err)
		require.True(t, second.Reused)
		require.Equal(t, first.EventID, second.EventID)
		require.Equal(t, first.IncidentID, second.IncidentID)

		current, err := biz.ListCurrent(ctx, &v1.ListCurrentAlertEventsRequest{
			Fingerprint: ptrAlertString("fp-idem"),
			Offset:      0,
			Limit:       20,
		})
		require.NoError(t, err)
		require.Equal(t, int64(1), current.TotalCount)

		history, err := biz.ListHistory(ctx, &v1.ListHistoryAlertEventsRequest{
			Fingerprint: ptrAlertString("fp-idem"),
			Offset:      0,
			Limit:       20,
		})
		require.NoError(t, err)
		require.Equal(t, int64(1), history.TotalCount)

		incidentTotal, _, err := s.Incident().List(ctx, where.T(ctx).O(0).L(20).F("fingerprint", "fp-idem"))
		require.NoError(t, err)
		require.Equal(t, int64(1), incidentTotal)
	})

	t.Run("merge updates current last_seen and appends history", func(t *testing.T) {
		reset(t)
		t1 := base.Add(1 * time.Minute)
		t2 := t1.Add(5 * time.Minute)

		_, err := biz.Ingest(ctx, &v1.IngestAlertEventRequest{
			IdempotencyKey: ptrAlertString("idem-alert-2-a"),
			Fingerprint:    ptrAlertString("fp-merge"),
			Status:         "firing",
			Severity:       "warning",
			Service:        ptrAlertString("payments"),
			Cluster:        ptrAlertString("prod-b"),
			Namespace:      ptrAlertString("checkout"),
			Workload:       ptrAlertString("payments-api"),
			LastSeenAt:     timestamppb.New(t1),
		})
		require.NoError(t, err)

		second, err := biz.Ingest(ctx, &v1.IngestAlertEventRequest{
			IdempotencyKey: ptrAlertString("idem-alert-2-b"),
			Fingerprint:    ptrAlertString("fp-merge"),
			Status:         "firing",
			Severity:       "critical",
			Service:        ptrAlertString("payments"),
			Cluster:        ptrAlertString("prod-b"),
			Namespace:      ptrAlertString("checkout"),
			Workload:       ptrAlertString("payments-api"),
			LastSeenAt:     timestamppb.New(t2),
		})
		require.NoError(t, err)
		require.Equal(t, "current_updated", second.MergeResult)

		current, err := biz.ListCurrent(ctx, &v1.ListCurrentAlertEventsRequest{
			Fingerprint: ptrAlertString("fp-merge"),
			Offset:      0,
			Limit:       20,
		})
		require.NoError(t, err)
		require.Equal(t, int64(1), current.TotalCount)
		require.Equal(t, t2.Unix(), current.Events[0].GetLastSeenAt().AsTime().Unix())

		history, err := biz.ListHistory(ctx, &v1.ListHistoryAlertEventsRequest{
			Fingerprint: ptrAlertString("fp-merge"),
			Offset:      0,
			Limit:       20,
		})
		require.NoError(t, err)
		require.Equal(t, int64(2), history.TotalCount)

		filtered, err := biz.ListCurrent(ctx, &v1.ListCurrentAlertEventsRequest{
			Severity:  ptrAlertString("critical"),
			Service:   ptrAlertString("payments"),
			Cluster:   ptrAlertString("prod-b"),
			Namespace: ptrAlertString("checkout"),
			Offset:    0,
			Limit:     20,
		})
		require.NoError(t, err)
		require.Equal(t, int64(1), filtered.TotalCount)

		incidentTotal, _, err := s.Incident().List(ctx, where.T(ctx).O(0).L(20).F("fingerprint", "fp-merge"))
		require.NoError(t, err)
		require.Equal(t, int64(1), incidentTotal)
	})

	t.Run("closed incident creates new incident for same fingerprint", func(t *testing.T) {
		reset(t)
		t1 := base.Add(2 * time.Minute)
		t2 := t1.Add(10 * time.Minute)

		first, err := biz.Ingest(ctx, &v1.IngestAlertEventRequest{
			IdempotencyKey: ptrAlertString("idem-alert-3-a"),
			Fingerprint:    ptrAlertString("fp-policy"),
			Status:         "firing",
			Severity:       "critical",
			Service:        ptrAlertString("cart"),
			Cluster:        ptrAlertString("prod-c"),
			Namespace:      ptrAlertString("cart"),
			Workload:       ptrAlertString("cart-api"),
			LastSeenAt:     timestamppb.New(t1),
		})
		require.NoError(t, err)
		require.NotNil(t, first.IncidentID)

		_, err = incBiz.Update(ctx, &v1.UpdateIncidentRequest{
			IncidentID: *first.IncidentID,
			Status:     ptrAlertString("closed"),
		})
		require.NoError(t, err)

		second, err := biz.Ingest(ctx, &v1.IngestAlertEventRequest{
			IdempotencyKey: ptrAlertString("idem-alert-3-b"),
			Fingerprint:    ptrAlertString("fp-policy"),
			Status:         "firing",
			Severity:       "critical",
			Service:        ptrAlertString("cart"),
			Cluster:        ptrAlertString("prod-c"),
			Namespace:      ptrAlertString("cart"),
			Workload:       ptrAlertString("cart-api"),
			LastSeenAt:     timestamppb.New(t2),
		})
		require.NoError(t, err)
		require.NotNil(t, second.IncidentID)
		require.NotEqual(t, *first.IncidentID, *second.IncidentID)

		closedIncident, err := s.Incident().Get(ctx, where.T(ctx).F("incident_id", *first.IncidentID))
		require.NoError(t, err)
		require.Nil(t, closedIncident.ActiveFingerprintKey)

		incidentTotal, _, err := s.Incident().List(ctx, where.T(ctx).O(0).L(20).F("fingerprint", "fp-policy"))
		require.NoError(t, err)
		require.Equal(t, int64(2), incidentTotal)
	})

	t.Run("active silence blocks incident creation and disabled silence restores merge", func(t *testing.T) {
		reset(t)
		now := time.Now().UTC().Truncate(time.Second)

		matchersJSON := `[{"key":"fingerprint","op":"=","value":"fp-silenced"}]`
		silence := &model.SilenceM{
			Namespace:    "default",
			Enabled:      true,
			StartsAt:     now.Add(-1 * time.Minute),
			EndsAt:       now.Add(1 * time.Hour),
			MatchersJSON: matchersJSON,
		}
		require.NoError(t, s.Silence().Create(ctx, silence))
		require.NotEmpty(t, silence.SilenceID)

		first, err := biz.Ingest(ctx, &v1.IngestAlertEventRequest{
			IdempotencyKey: ptrAlertString("idem-alert-4-a"),
			Fingerprint:    ptrAlertString("fp-silenced"),
			Status:         "firing",
			Severity:       "warning",
			Service:        ptrAlertString("checkout"),
			Cluster:        ptrAlertString("prod-a"),
			Namespace:      ptrAlertString("default"),
			Workload:       ptrAlertString("checkout-api"),
			LastSeenAt:     timestamppb.New(now),
		})
		require.NoError(t, err)
		require.True(t, first.GetSilenced())
		require.NotNil(t, first.SilenceID)
		require.Equal(t, silence.SilenceID, first.GetSilenceID())
		require.Nil(t, first.IncidentID)

		incidentTotal, _, err := s.Incident().List(ctx, where.T(ctx).O(0).L(20).F("fingerprint", "fp-silenced"))
		require.NoError(t, err)
		require.Equal(t, int64(0), incidentTotal)

		current, err := biz.ListCurrent(ctx, &v1.ListCurrentAlertEventsRequest{
			Fingerprint: ptrAlertString("fp-silenced"),
			Offset:      0,
			Limit:       20,
		})
		require.NoError(t, err)
		require.Equal(t, int64(1), current.TotalCount)
		require.True(t, current.Events[0].GetIsSilenced())
		require.Equal(t, silence.SilenceID, current.Events[0].GetSilenceID())

		silence.Enabled = false
		require.NoError(t, s.Silence().Update(ctx, silence))

		second, err := biz.Ingest(ctx, &v1.IngestAlertEventRequest{
			IdempotencyKey: ptrAlertString("idem-alert-4-b"),
			Fingerprint:    ptrAlertString("fp-silenced"),
			Status:         "firing",
			Severity:       "critical",
			Service:        ptrAlertString("checkout"),
			Cluster:        ptrAlertString("prod-a"),
			Namespace:      ptrAlertString("default"),
			Workload:       ptrAlertString("checkout-api"),
			LastSeenAt:     timestamppb.New(now.Add(2 * time.Minute)),
		})
		require.NoError(t, err)
		require.False(t, second.GetSilenced())
		require.NotNil(t, second.IncidentID)
		require.Nil(t, second.SilenceID)

		incidentTotal, _, err = s.Incident().List(ctx, where.T(ctx).O(0).L(20).F("fingerprint", "fp-silenced"))
		require.NoError(t, err)
		require.Equal(t, int64(1), incidentTotal)
	})

	t.Run("active silence clears stale current incident link", func(t *testing.T) {
		reset(t)
		now := time.Now().UTC().Truncate(time.Second)

		seed, err := biz.Ingest(ctx, &v1.IngestAlertEventRequest{
			IdempotencyKey: ptrAlertString("idem-alert-5-a"),
			Fingerprint:    ptrAlertString("fp-silenced-existing"),
			Status:         "firing",
			Severity:       "warning",
			Service:        ptrAlertString("checkout"),
			Cluster:        ptrAlertString("prod-a"),
			Namespace:      ptrAlertString("default"),
			Workload:       ptrAlertString("checkout-api"),
			LastSeenAt:     timestamppb.New(now.Add(-2 * time.Minute)),
		})
		require.NoError(t, err)
		require.NotNil(t, seed.IncidentID)

		matchersJSON := `[{"key":"fingerprint","op":"=","value":"fp-silenced-existing"}]`
		silence := &model.SilenceM{
			Namespace:    "default",
			Enabled:      true,
			StartsAt:     now.Add(-1 * time.Minute),
			EndsAt:       now.Add(1 * time.Hour),
			MatchersJSON: matchersJSON,
		}
		require.NoError(t, s.Silence().Create(ctx, silence))
		require.NotEmpty(t, silence.SilenceID)

		second, err := biz.Ingest(ctx, &v1.IngestAlertEventRequest{
			IdempotencyKey: ptrAlertString("idem-alert-5-b"),
			Fingerprint:    ptrAlertString("fp-silenced-existing"),
			Status:         "firing",
			Severity:       "critical",
			Service:        ptrAlertString("checkout"),
			Cluster:        ptrAlertString("prod-a"),
			Namespace:      ptrAlertString("default"),
			Workload:       ptrAlertString("checkout-api"),
			LastSeenAt:     timestamppb.New(now),
		})
		require.NoError(t, err)
		require.Equal(t, "silenced_current_updated", second.GetMergeResult())
		require.True(t, second.GetSilenced())
		require.Nil(t, second.IncidentID)
		require.Equal(t, silence.SilenceID, second.GetSilenceID())

		current, err := biz.ListCurrent(ctx, &v1.ListCurrentAlertEventsRequest{
			Fingerprint: ptrAlertString("fp-silenced-existing"),
			Offset:      0,
			Limit:       20,
		})
		require.NoError(t, err)
		require.Equal(t, int64(1), current.TotalCount)
		require.True(t, current.Events[0].GetIsSilenced())
		require.Equal(t, silence.SilenceID, current.Events[0].GetSilenceID())
		require.Nil(t, current.Events[0].IncidentID)

		incidentTotal, _, err := s.Incident().List(ctx, where.T(ctx).O(0).L(20).F("fingerprint", "fp-silenced-existing"))
		require.NoError(t, err)
		require.Equal(t, int64(1), incidentTotal)
	})
}

func newAlertEventTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
CREATE TABLE incidents (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	incident_id TEXT NOT NULL DEFAULT '',
	tenant_id TEXT NOT NULL DEFAULT 'default',
	cluster TEXT NOT NULL DEFAULT 'default',
	namespace TEXT NOT NULL,
	workload_kind TEXT NOT NULL DEFAULT 'Deployment',
	workload_name TEXT NOT NULL,
	pod TEXT,
	node TEXT,
	service TEXT NOT NULL,
	environment TEXT NOT NULL DEFAULT 'prod',
	version TEXT,
	source TEXT NOT NULL DEFAULT 'alertmanager',
	alertname TEXT,
	fingerprint TEXT,
	active_fingerprint_key TEXT,
	rule_id TEXT,
	labels_json TEXT,
	annotations_json TEXT,
	severity TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'open',
	start_at DATETIME,
	end_at DATETIME,
	rca_status TEXT NOT NULL DEFAULT 'pending',
	root_cause_type TEXT,
	root_cause_summary TEXT,
	diagnosis_json TEXT,
	evidence_refs_json TEXT,
	action_status TEXT NOT NULL DEFAULT 'none',
	action_summary TEXT,
	trace_id TEXT,
	log_trace_key TEXT,
	change_id TEXT,
	created_by TEXT,
	approved_by TEXT,
	closed_by TEXT,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
)`).Error)
	require.NoError(t, db.Exec("CREATE UNIQUE INDEX uniq_incidents_incident_id ON incidents(incident_id)").Error)
	require.NoError(t, db.Exec("CREATE UNIQUE INDEX uniq_incidents_active_fingerprint_key ON incidents(active_fingerprint_key)").Error)
	require.NoError(t, db.AutoMigrate(&model.AlertEventM{}))
	require.NoError(t, db.AutoMigrate(&model.SilenceM{}))
	require.NoError(t, db.AutoMigrate(&model.NoticeChannelM{}, &model.NoticeDeliveryM{}))
	return db
}

func ptrAlertString(v string) *string { return &v }

func TestRetryIncidentDuplicateReadback(t *testing.T) {
	t.Run("retries record not found then succeeds", func(t *testing.T) {
		attempt := 0
		incident, err := retryIncidentDuplicateReadback(context.Background(), 5, 0, func() (*model.IncidentM, error) {
			attempt++
			if attempt < 3 {
				return nil, gorm.ErrRecordNotFound
			}
			return &model.IncidentM{IncidentID: "incident-readback"}, nil
		})

		require.NoError(t, err)
		require.NotNil(t, incident)
		require.Equal(t, "incident-readback", incident.IncidentID)
		require.Equal(t, 3, attempt)
	})

	t.Run("returns immediately on non not-found error", func(t *testing.T) {
		sentinel := errors.New("boom")
		attempt := 0
		incident, err := retryIncidentDuplicateReadback(context.Background(), 5, 0, func() (*model.IncidentM, error) {
			attempt++
			return nil, sentinel
		})

		require.Nil(t, incident)
		require.ErrorIs(t, err, sentinel)
		require.Equal(t, 1, attempt)
	})

	t.Run("stops when context is canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		attempt := 0
		incident, err := retryIncidentDuplicateReadback(ctx, 5, 5*time.Millisecond, func() (*model.IncidentM, error) {
			attempt++
			return nil, gorm.ErrRecordNotFound
		})

		require.Nil(t, incident)
		require.ErrorIs(t, err, context.Canceled)
		require.Equal(t, 1, attempt)
	})
}

func TestAlertEventBiz_IncidentCreatedTriggersNoticeDelivery(t *testing.T) {
	db := newAlertEventTestDB(t)
	s := store.NewStore(db)
	biz := New(s)
	ctx := context.Background()

	require.NoError(t, s.NoticeChannel().Create(ctx, &model.NoticeChannelM{
		Name:        "notice-alert-ingest",
		Type:        "webhook",
		Enabled:     true,
		EndpointURL: "http://127.0.0.1:19999/hook",
		TimeoutMs:   1000,
	}))

	now := time.Now().UTC()
	ingestResp, err := biz.Ingest(ctx, &v1.IngestAlertEventRequest{
		IdempotencyKey: ptrAlertString("idem-notice-ingest"),
		Fingerprint:    ptrAlertString("fp-notice-ingest"),
		Status:         "firing",
		Severity:       "P1",
		Service:        ptrAlertString("checkout"),
		Cluster:        ptrAlertString("prod"),
		Namespace:      ptrAlertString("default"),
		Workload:       ptrAlertString("checkout-api"),
		LastSeenAt:     timestamppb.New(now),
	})
	require.NoError(t, err)
	require.NotNil(t, ingestResp.GetIncidentID())

	total, deliveries, err := s.NoticeDelivery().List(ctx, where.T(ctx).P(0, 20).F("incident_id", ingestResp.GetIncidentID()))
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Equal(t, "incident_created", deliveries[0].EventType)
	require.Equal(t, "pending", deliveries[0].Status)
	require.Equal(t, int64(0), deliveries[0].Attempts)
}
