package notice

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"zk8s.com/rca-api/internal/apiserver/model"
	"zk8s.com/rca-api/internal/apiserver/store"
	v1 "zk8s.com/rca-api/pkg/api/apiserver/v1"
)

func TestNoticeBiz_ChannelCRUDAndDeliveryQuery(t *testing.T) {
	db := newNoticeTestDB(t)
	s := store.NewStore(db)
	biz := New(s)
	ctx := context.Background()

	createResp, err := biz.CreateChannel(ctx, &v1.CreateNoticeChannelRequest{
		Name:        "ops-webhook",
		EndpointURL: "http://127.0.0.1:18080/hook",
		Headers: map[string]string{
			"X-Token": "abc",
		},
		TimeoutMs:  int64PtrNoticeBiz(1500),
		MaxRetries: int64PtrNoticeBiz(0),
		Secret:     strPtrNoticeBiz("secret-1"),
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

	listResp, err := biz.ListChannels(ctx, &v1.ListNoticeChannelsRequest{
		Enabled: boolPtrNoticeBiz(true),
		Limit:   20,
		Offset:  0,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), listResp.GetTotalCount())

	_, err = biz.PatchChannel(ctx, &v1.PatchNoticeChannelRequest{
		ChannelID:   channelID,
		Enabled:     boolPtrNoticeBiz(false),
		EndpointURL: strPtrNoticeBiz("https://example.org/new"),
		Headers:     map[string]string{},
		TimeoutMs:   int64PtrNoticeBiz(5000),
	})
	require.NoError(t, err)

	getAfterPatch, err := biz.GetChannel(ctx, &v1.GetNoticeChannelRequest{ChannelID: channelID})
	require.NoError(t, err)
	require.False(t, getAfterPatch.GetNoticeChannel().GetEnabled())
	require.Equal(t, "https://example.org/new", getAfterPatch.GetNoticeChannel().GetEndpointURL())
	require.Empty(t, getAfterPatch.GetNoticeChannel().GetHeaders())

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
	db := newNoticeTestDB(t)
	s := store.NewStore(db)
	biz := New(s)
	ctx := context.Background()

	lockedBy := "worker-1"
	lockedAt := time.Now().UTC().Add(-1 * time.Minute)
	responseBody := `{"ok":false}`
	errText := "http_status_500"
	delivery := &model.NoticeDeliveryM{
		ChannelID:      "notice-channel-op-1",
		EventType:      "incident_created",
		IncidentID:     strPtrNoticeBiz("incident-op-1"),
		RequestBody:    `{"event_type":"incident_created"}`,
		ResponseCode:   int32PtrNoticeBiz(500),
		ResponseBody:   &responseBody,
		LatencyMs:      37,
		Status:         "failed",
		Attempts:       2,
		MaxAttempts:    3,
		NextRetryAt:    time.Now().UTC().Add(10 * time.Minute),
		LockedBy:       &lockedBy,
		LockedAt:       &lockedAt,
		IdempotencyKey: "notice-test-op-idem-1",
		Error:          &errText,
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

	replayResp2, err := biz.ReplayDelivery(ctx, &v1.ReplayNoticeDeliveryRequest{DeliveryID: delivery.DeliveryID})
	require.NoError(t, err)
	require.Equal(t, "pending", replayResp2.GetNoticeDelivery().GetStatus())
	require.Equal(t, int64(0), replayResp2.GetNoticeDelivery().GetAttempts())

	cancelResp, err := biz.CancelDelivery(ctx, &v1.CancelNoticeDeliveryRequest{DeliveryID: delivery.DeliveryID})
	require.NoError(t, err)
	require.Equal(t, "canceled", cancelResp.GetNoticeDelivery().GetStatus())
	require.Nil(t, cancelResp.GetNoticeDelivery().LockedBy)
	require.Nil(t, cancelResp.GetNoticeDelivery().LockedAt)

	cancelResp2, err := biz.CancelDelivery(ctx, &v1.CancelNoticeDeliveryRequest{DeliveryID: delivery.DeliveryID})
	require.NoError(t, err)
	require.Equal(t, "canceled", cancelResp2.GetNoticeDelivery().GetStatus())

	// Replay on canceled is idempotent no-op and returns current status.
	replayResp3, err := biz.ReplayDelivery(ctx, &v1.ReplayNoticeDeliveryRequest{DeliveryID: delivery.DeliveryID})
	require.NoError(t, err)
	require.Equal(t, "canceled", replayResp3.GetNoticeDelivery().GetStatus())
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
