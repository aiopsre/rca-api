package notice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"zk8s.com/rca-api/internal/apiserver/model"
	"zk8s.com/rca-api/internal/apiserver/store"
	"zk8s.com/rca-api/internal/pkg/errno"
	v1 "zk8s.com/rca-api/pkg/api/apiserver/v1"
)

func TestNoticeBiz_ChannelCRUDAndDeliveryQuery(t *testing.T) {
	store.ResetForTest()
	t.Cleanup(store.ResetForTest)

	db := newNoticeTestDB(t)
	s := store.NewStore(db)
	biz := New(s)
	ctx := context.Background()

	createResp, err := biz.CreateChannel(ctx, &v1.CreateNoticeChannelRequest{
		Name:        "ops-webhook",
		EndpointURL: "http://127.0.0.1:18080/hook",
		BaseURL:     strPtrNoticeBiz("https://rca.example.test"),
		SummaryTemplate: strPtrNoticeBiz(
			"[${severity}] ${service} ${event_type} incident=${incident_id}",
		),
		Headers: map[string]string{
			"X-Token": "abc",
		},
		TimeoutMs:  int64PtrNoticeBiz(1500),
		MaxRetries: int64PtrNoticeBiz(0),
		Secret:     strPtrNoticeBiz("secret-1"),
		Selectors: &v1.NoticeSelectors{
			EventTypes: []string{"incident_created"},
			Services:   []string{"checkout"},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, createResp.GetNoticeChannel())
	require.True(t, strings.HasPrefix(createResp.GetNoticeChannel().GetChannelID(), "notice-channel-"))

	channelID := createResp.GetNoticeChannel().GetChannelID()

	getResp, err := biz.GetChannel(ctx, &v1.GetNoticeChannelRequest{ChannelID: channelID})
	require.NoError(t, err)
	require.Equal(t, "ops-webhook", getResp.GetNoticeChannel().GetName())
	require.Equal(t, "webhook", getResp.GetNoticeChannel().GetType())
	require.Equal(t, "abc", getResp.GetNoticeChannel().GetHeaders()["X-Token"])
	require.Equal(t, []string{"incident_created"}, getResp.GetNoticeChannel().GetSelectors().GetEventTypes())
	require.Equal(t, []string{"checkout"}, getResp.GetNoticeChannel().GetSelectors().GetServices())
	require.Equal(t, v1.NoticePayloadMode_NOTICE_PAYLOAD_MODE_COMPACT, getResp.GetNoticeChannel().GetPayloadMode())
	require.False(t, getResp.GetNoticeChannel().GetIncludeDiagnosis())
	require.False(t, getResp.GetNoticeChannel().GetIncludeEvidenceIds())
	require.False(t, getResp.GetNoticeChannel().GetIncludeRootCause())
	require.False(t, getResp.GetNoticeChannel().GetIncludeLinks())
	require.Equal(t, "https://rca.example.test", getResp.GetNoticeChannel().GetBaseURL())
	require.Equal(t, "[${severity}] ${service} ${event_type} incident=${incident_id}", getResp.GetNoticeChannel().GetSummaryTemplate())

	listResp, err := biz.ListChannels(ctx, &v1.ListNoticeChannelsRequest{
		Enabled: boolPtrNoticeBiz(true),
		Limit:   20,
		Offset:  0,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), listResp.GetTotalCount())

	_, err = biz.PatchChannel(ctx, &v1.PatchNoticeChannelRequest{
		ChannelID:    channelID,
		Enabled:      boolPtrNoticeBiz(false),
		EndpointURL:  strPtrNoticeBiz("https://example.org/new"),
		Headers:      map[string]string{},
		TimeoutMs:    int64PtrNoticeBiz(5000),
		PayloadMode:  noticePayloadModePtrNoticeBiz(v1.NoticePayloadMode_NOTICE_PAYLOAD_MODE_FULL),
		IncludeLinks: boolPtrNoticeBiz(false),
		BaseURL:      strPtrNoticeBiz("https://rca.example.test/v2"),
		SummaryTemplate: strPtrNoticeBiz(
			"[${severity}] ${service} ${event_type} incident=${incident_id} delivery=${delivery_id}",
		),
		Selectors: &v1.NoticeSelectors{
			EventTypes: []string{"diagnosis_written"},
			Severities: []string{"warning"},
		},
	})
	require.NoError(t, err)

	getAfterPatch, err := biz.GetChannel(ctx, &v1.GetNoticeChannelRequest{ChannelID: channelID})
	require.NoError(t, err)
	require.False(t, getAfterPatch.GetNoticeChannel().GetEnabled())
	require.Equal(t, "https://example.org/new", getAfterPatch.GetNoticeChannel().GetEndpointURL())
	require.Empty(t, getAfterPatch.GetNoticeChannel().GetHeaders())
	require.Equal(t, []string{"diagnosis_written"}, getAfterPatch.GetNoticeChannel().GetSelectors().GetEventTypes())
	require.Equal(t, []string{"warning"}, getAfterPatch.GetNoticeChannel().GetSelectors().GetSeverities())
	require.Equal(t, v1.NoticePayloadMode_NOTICE_PAYLOAD_MODE_FULL, getAfterPatch.GetNoticeChannel().GetPayloadMode())
	require.True(t, getAfterPatch.GetNoticeChannel().GetIncludeDiagnosis())
	require.True(t, getAfterPatch.GetNoticeChannel().GetIncludeEvidenceIds())
	require.True(t, getAfterPatch.GetNoticeChannel().GetIncludeRootCause())
	require.False(t, getAfterPatch.GetNoticeChannel().GetIncludeLinks())
	require.Equal(t, "https://rca.example.test/v2", getAfterPatch.GetNoticeChannel().GetBaseURL())
	require.Equal(t, "[${severity}] ${service} ${event_type} incident=${incident_id} delivery=${delivery_id}", getAfterPatch.GetNoticeChannel().GetSummaryTemplate())

	_, err = biz.DeleteChannel(ctx, &v1.DeleteNoticeChannelRequest{ChannelID: channelID})
	require.NoError(t, err)

	delivery := &model.NoticeDeliveryM{
		ChannelID:      channelID,
		EventType:      "incident_created",
		IncidentID:     strPtrNoticeBiz("incident-1"),
		JobID:          nil,
		RequestBody:    `{"event_type":"incident_created"}`,
		LatencyMs:      22,
		Status:         "succeeded",
		Attempts:       1,
		MaxAttempts:    3,
		NextRetryAt:    time.Now().UTC(),
		IdempotencyKey: "notice-test-idem-1",
	}
	require.NoError(t, s.NoticeDelivery().Create(ctx, delivery))
	require.NotEmpty(t, delivery.DeliveryID)

	listDeliveriesResp, err := biz.ListDeliveries(ctx, &v1.ListNoticeDeliveriesRequest{
		IncidentID: strPtrNoticeBiz("incident-1"),
		Limit:      20,
		Offset:     0,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), listDeliveriesResp.GetTotalCount())
	require.Equal(t, delivery.DeliveryID, listDeliveriesResp.GetNoticeDeliveries()[0].GetDeliveryID())

	getDeliveryResp, err := biz.GetDelivery(ctx, &v1.GetNoticeDeliveryRequest{DeliveryID: delivery.DeliveryID})
	require.NoError(t, err)
	require.Equal(t, "incident_created", getDeliveryResp.GetNoticeDelivery().GetEventType())
}

func TestNoticeBiz_ReplayAndCancelDeliveryOps(t *testing.T) {
	store.ResetForTest()
	t.Cleanup(store.ResetForTest)

	db := newNoticeTestDB(t)
	s := store.NewStore(db)
	biz := New(s)
	ctx := context.Background()

	lockedBy := "worker-1"
	lockedAt := time.Now().UTC().Add(-1 * time.Minute)
	responseBody := `{"ok":false}`
	errText := "http_status_500"
	snapshotEndpoint := "http://127.0.0.1:19099/old-webhook"
	snapshotTimeout := int64(1200)
	snapshotHeaders := `{"Authorization":"Bearer old-token"}`
	snapshotSecretFingerprint := "sha256:testfingerprint"
	snapshotChannelVersion := int64(12345)
	delivery := &model.NoticeDeliveryM{
		ChannelID:                 "notice-channel-op-1",
		EventType:                 "incident_created",
		IncidentID:                strPtrNoticeBiz("incident-op-1"),
		RequestBody:               `{"event_type":"incident_created"}`,
		ResponseCode:              int32PtrNoticeBiz(500),
		ResponseBody:              &responseBody,
		LatencyMs:                 37,
		Status:                    "failed",
		Attempts:                  2,
		MaxAttempts:               3,
		NextRetryAt:               time.Now().UTC().Add(10 * time.Minute),
		SnapshotEndpointURL:       &snapshotEndpoint,
		SnapshotTimeoutMs:         &snapshotTimeout,
		SnapshotHeadersJSON:       &snapshotHeaders,
		SnapshotSecretFingerprint: &snapshotSecretFingerprint,
		SnapshotChannelVersion:    &snapshotChannelVersion,
		LockedBy:                  &lockedBy,
		LockedAt:                  &lockedAt,
		IdempotencyKey:            "notice-test-op-idem-1",
		Error:                     &errText,
	}
	require.NoError(t, s.NoticeDelivery().Create(ctx, delivery))

	replayResp, err := biz.ReplayDelivery(ctx, &v1.ReplayNoticeDeliveryRequest{DeliveryID: delivery.DeliveryID})
	require.NoError(t, err)
	require.Equal(t, "pending", replayResp.GetNoticeDelivery().GetStatus())
	require.Equal(t, int64(0), replayResp.GetNoticeDelivery().GetAttempts())
	require.Nil(t, replayResp.GetNoticeDelivery().LockedBy)
	require.Nil(t, replayResp.GetNoticeDelivery().LockedAt)
	require.Nil(t, replayResp.GetNoticeDelivery().ResponseCode)
	require.Nil(t, replayResp.GetNoticeDelivery().ResponseBody)
	require.Nil(t, replayResp.GetNoticeDelivery().Error)
	require.Equal(t, snapshotEndpoint, replayResp.GetNoticeDelivery().GetSnapshot().GetEndpointURL())
	require.Equal(t, snapshotTimeout, replayResp.GetNoticeDelivery().GetSnapshot().GetTimeoutMs())
	require.Equal(t, "Bearer old-token", replayResp.GetNoticeDelivery().GetSnapshot().GetHeaders()["Authorization"])
	require.Equal(t, snapshotSecretFingerprint, replayResp.GetNoticeDelivery().GetSnapshot().GetSecretFingerprint())
	require.Equal(t, snapshotChannelVersion, replayResp.GetNoticeDelivery().GetSnapshot().GetChannelVersion())

	replayResp2, err := biz.ReplayDelivery(ctx, &v1.ReplayNoticeDeliveryRequest{DeliveryID: delivery.DeliveryID})
	require.NoError(t, err)
	require.Equal(t, "pending", replayResp2.GetNoticeDelivery().GetStatus())
	require.Equal(t, int64(0), replayResp2.GetNoticeDelivery().GetAttempts())

	cancelResp, err := biz.CancelDelivery(ctx, &v1.CancelNoticeDeliveryRequest{DeliveryID: delivery.DeliveryID})
	require.NoError(t, err)
	require.Equal(t, "canceled", cancelResp.GetNoticeDelivery().GetStatus())
	require.Nil(t, cancelResp.GetNoticeDelivery().LockedBy)
	require.Nil(t, cancelResp.GetNoticeDelivery().LockedAt)
	require.Equal(t, snapshotEndpoint, cancelResp.GetNoticeDelivery().GetSnapshot().GetEndpointURL())
	require.Equal(t, snapshotTimeout, cancelResp.GetNoticeDelivery().GetSnapshot().GetTimeoutMs())

	cancelResp2, err := biz.CancelDelivery(ctx, &v1.CancelNoticeDeliveryRequest{DeliveryID: delivery.DeliveryID})
	require.NoError(t, err)
	require.Equal(t, "canceled", cancelResp2.GetNoticeDelivery().GetStatus())

	// Replay on canceled is idempotent no-op and returns current status.
	replayResp3, err := biz.ReplayDelivery(ctx, &v1.ReplayNoticeDeliveryRequest{DeliveryID: delivery.DeliveryID})
	require.NoError(t, err)
	require.Equal(t, "canceled", replayResp3.GetNoticeDelivery().GetStatus())
}

func TestNoticeBiz_ReplayDeliveryUseLatestChannel(t *testing.T) {
	store.ResetForTest()
	t.Cleanup(store.ResetForTest)

	db := newNoticeTestDB(t)
	s := store.NewStore(db)
	biz := New(s)
	ctx := context.Background()

	secret := "secret-old"
	headersJSON := `{"Authorization":"Bearer old-token"}`
	channel := &model.NoticeChannelM{
		Name:        "notice-channel-replay-latest",
		Type:        "webhook",
		Enabled:     true,
		EndpointURL: "http://127.0.0.1:19095/old",
		Secret:      &secret,
		HeadersJSON: &headersJSON,
		TimeoutMs:   1200,
		MaxRetries:  3,
	}
	require.NoError(t, s.NoticeChannel().Create(ctx, channel))
	require.NotEmpty(t, channel.ChannelID)

	oldSnapshotEndpoint := "http://127.0.0.1:19095/old"
	oldSnapshotTimeout := int64(1200)
	oldSnapshotHeaders := `{"Authorization":"Bearer old-token"}`
	delivery := &model.NoticeDeliveryM{
		ChannelID:              channel.ChannelID,
		EventType:              "incident_created",
		IncidentID:             strPtrNoticeBiz("incident-replay-latest-1"),
		RequestBody:            `{"event_type":"incident_created"}`,
		Status:                 "failed",
		Attempts:               1,
		MaxAttempts:            3,
		NextRetryAt:            time.Now().UTC().Add(2 * time.Minute),
		SnapshotEndpointURL:    &oldSnapshotEndpoint,
		SnapshotTimeoutMs:      &oldSnapshotTimeout,
		SnapshotHeadersJSON:    &oldSnapshotHeaders,
		IdempotencyKey:         "notice-replay-latest-idem-1",
		SnapshotChannelVersion: int64PtrNoticeBiz(100),
	}
	require.NoError(t, s.NoticeDelivery().Create(ctx, delivery))
	require.NotEmpty(t, delivery.DeliveryID)

	newSecret := "secret-new"
	newHeaders := `{"Authorization":"Bearer new-token","X-Trace":"abc"}`
	channel.EndpointURL = "http://127.0.0.1:19095/new"
	channel.TimeoutMs = 12000 // should clamp to 10000 in snapshot builder
	channel.Secret = &newSecret
	channel.HeadersJSON = &newHeaders
	require.NoError(t, s.NoticeChannel().Update(ctx, channel))

	resp, err := biz.ReplayDelivery(ctx, &v1.ReplayNoticeDeliveryRequest{
		DeliveryID:       delivery.DeliveryID,
		UseLatestChannel: boolPtrNoticeBiz(true),
	})
	require.NoError(t, err)
	require.Equal(t, "pending", resp.GetNoticeDelivery().GetStatus())
	require.Equal(t, int64(0), resp.GetNoticeDelivery().GetAttempts())
	require.Equal(t, "http://127.0.0.1:19095/new", resp.GetNoticeDelivery().GetSnapshot().GetEndpointURL())
	require.Equal(t, int64(10000), resp.GetNoticeDelivery().GetSnapshot().GetTimeoutMs())
	require.Equal(t, "Bearer new-token", resp.GetNoticeDelivery().GetSnapshot().GetHeaders()["Authorization"])
	require.Equal(t, "abc", resp.GetNoticeDelivery().GetSnapshot().GetHeaders()["X-Trace"])
	require.Equal(t, "notice-replay-latest-idem-1", resp.GetNoticeDelivery().GetIdempotencyKey())
	require.NotZero(t, resp.GetNoticeDelivery().GetSnapshot().GetChannelVersion())

	sum := sha256.Sum256([]byte(newSecret))
	require.Equal(t, "sha256:"+hex.EncodeToString(sum[:]), resp.GetNoticeDelivery().GetSnapshot().GetSecretFingerprint())
}

func TestNoticeBiz_ReplayUseLatestChannelMissingChannelConflict(t *testing.T) {
	store.ResetForTest()
	t.Cleanup(store.ResetForTest)

	db := newNoticeTestDB(t)
	s := store.NewStore(db)
	biz := New(s)
	ctx := context.Background()

	deliveryKeepSnapshot := &model.NoticeDeliveryM{
		ChannelID:      "notice-channel-missing",
		EventType:      "incident_created",
		IncidentID:     strPtrNoticeBiz("incident-replay-missing-1"),
		RequestBody:    `{"event_type":"incident_created"}`,
		Status:         "failed",
		Attempts:       1,
		MaxAttempts:    3,
		NextRetryAt:    time.Now().UTC().Add(2 * time.Minute),
		IdempotencyKey: "notice-replay-missing-idem-1",
	}
	require.NoError(t, s.NoticeDelivery().Create(ctx, deliveryKeepSnapshot))
	resp, err := biz.ReplayDelivery(ctx, &v1.ReplayNoticeDeliveryRequest{DeliveryID: deliveryKeepSnapshot.DeliveryID})
	require.NoError(t, err)
	require.Equal(t, "pending", resp.GetNoticeDelivery().GetStatus())

	deliveryNeedLatest := &model.NoticeDeliveryM{
		ChannelID:      "notice-channel-missing",
		EventType:      "incident_created",
		IncidentID:     strPtrNoticeBiz("incident-replay-missing-2"),
		RequestBody:    `{"event_type":"incident_created"}`,
		Status:         "failed",
		Attempts:       1,
		MaxAttempts:    3,
		NextRetryAt:    time.Now().UTC().Add(2 * time.Minute),
		IdempotencyKey: "notice-replay-missing-idem-2",
	}
	require.NoError(t, s.NoticeDelivery().Create(ctx, deliveryNeedLatest))
	_, err = biz.ReplayDelivery(ctx, &v1.ReplayNoticeDeliveryRequest{
		DeliveryID:       deliveryNeedLatest.DeliveryID,
		UseLatestChannel: boolPtrNoticeBiz(true),
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, errno.ErrNoticeDeliveryReplayLatestChannelNotFound))
}

func newNoticeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.NoticeChannelM{}, &model.NoticeDeliveryM{}))
	return db
}

func boolPtrNoticeBiz(v bool) *bool { return &v }

func int64PtrNoticeBiz(v int64) *int64 { return &v }

func strPtrNoticeBiz(v string) *string { return &v }

func int32PtrNoticeBiz(v int32) *int32 { return &v }

func noticePayloadModePtrNoticeBiz(v v1.NoticePayloadMode) *v1.NoticePayloadMode { return &v }
