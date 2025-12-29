package notice

//go:generate mockgen -destination mock_notice.go -package notice zk8s.com/rca-api/internal/apiserver/biz/v1/notice NoticeBiz

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/onexstack/onexstack/pkg/errorsx"
	"gorm.io/gorm"

	"zk8s.com/rca-api/internal/apiserver/model"
	"zk8s.com/rca-api/internal/apiserver/pkg/conversion"
	"zk8s.com/rca-api/internal/apiserver/pkg/metrics"
	noticepkg "zk8s.com/rca-api/internal/apiserver/pkg/notice"
	"zk8s.com/rca-api/internal/apiserver/store"
	"zk8s.com/rca-api/internal/pkg/contextx"
	"zk8s.com/rca-api/internal/pkg/errno"
	v1 "zk8s.com/rca-api/pkg/api/apiserver/v1"
	"zk8s.com/rca-api/pkg/store/where"
)

const (
	noticeTypeWebhook = "webhook"

	defaultNoticeChannelListLimit  = int64(20)
	maxNoticeChannelListLimit      = int64(200)
	defaultNoticeDeliveryListLimit = int64(20)
	maxNoticeDeliveryListLimit     = int64(200)

	defaultNoticeTimeoutMs = int64(3000)
	defaultNoticeRetries   = int64(3)

	replayModeSnapshot = "snapshot"
	replayModeLatest   = "latest"
)

// NoticeBiz defines notice use-cases.
//
//nolint:interfacebloat // CRUD + list intentionally grouped in one biz entrypoint.
type NoticeBiz interface {
	CreateChannel(ctx context.Context, rq *v1.CreateNoticeChannelRequest) (*v1.CreateNoticeChannelResponse, error)
	GetChannel(ctx context.Context, rq *v1.GetNoticeChannelRequest) (*v1.GetNoticeChannelResponse, error)
	ListChannels(ctx context.Context, rq *v1.ListNoticeChannelsRequest) (*v1.ListNoticeChannelsResponse, error)
	PatchChannel(ctx context.Context, rq *v1.PatchNoticeChannelRequest) (*v1.PatchNoticeChannelResponse, error)
	DeleteChannel(ctx context.Context, rq *v1.DeleteNoticeChannelRequest) (*v1.DeleteNoticeChannelResponse, error)

	GetDelivery(ctx context.Context, rq *v1.GetNoticeDeliveryRequest) (*v1.GetNoticeDeliveryResponse, error)
	ListDeliveries(ctx context.Context, rq *v1.ListNoticeDeliveriesRequest) (*v1.ListNoticeDeliveriesResponse, error)
	ReplayDelivery(ctx context.Context, rq *v1.ReplayNoticeDeliveryRequest) (*v1.ReplayNoticeDeliveryResponse, error)
	CancelDelivery(ctx context.Context, rq *v1.CancelNoticeDeliveryRequest) (*v1.CancelNoticeDeliveryResponse, error)

	NoticeExpansion
}

//nolint:modernize // Keep explicit empty interface as placeholder expansion point.
type NoticeExpansion interface{}

type noticeBiz struct {
	store store.IStore
}

var _ NoticeBiz = (*noticeBiz)(nil)

// New creates notice biz.
func New(store store.IStore) *noticeBiz {
	return &noticeBiz{store: store}
}

func (b *noticeBiz) CreateChannel(ctx context.Context, rq *v1.CreateNoticeChannelRequest) (*v1.CreateNoticeChannelResponse, error) {
	chType := strings.ToLower(strings.TrimSpace(rq.GetType()))
	if chType == "" {
		chType = noticeTypeWebhook
	}

	enabled := true
	if rq.Enabled != nil {
		enabled = rq.GetEnabled()
	}

	timeoutMs := defaultNoticeTimeoutMs
	if rq.TimeoutMs != nil {
		timeoutMs = rq.GetTimeoutMs()
	}
	maxRetries := defaultNoticeRetries
	if rq.MaxRetries != nil {
		maxRetries = rq.GetMaxRetries()
	}
	payloadMode := noticePayloadModeFromV1(rq.GetPayloadMode())

	obj := &model.NoticeChannelM{
		Name:               strings.TrimSpace(rq.GetName()),
		Type:               chType,
		Enabled:            enabled,
		EndpointURL:        strings.TrimSpace(rq.GetEndpointURL()),
		Secret:             normalizeOptionalString(rq.Secret),
		HeadersJSON:        conversion.EncodeStringMap(rq.GetHeaders()),
		SelectorsJSON:      conversion.EncodeNoticeSelectors(rq.GetSelectors()),
		TimeoutMs:          timeoutMs,
		MaxRetries:         maxRetries,
		PayloadMode:        payloadMode,
		IncludeDiagnosis:   resolveNoticeTemplateInclude(rq.IncludeDiagnosis, payloadMode),
		IncludeEvidenceIDs: resolveNoticeTemplateInclude(rq.IncludeEvidenceIds, payloadMode),
		IncludeRootCause:   resolveNoticeTemplateInclude(rq.IncludeRootCause, payloadMode),
		IncludeLinks:       resolveNoticeTemplateInclude(rq.IncludeLinks, payloadMode),
	}
	if err := b.store.NoticeChannel().Create(ctx, obj); err != nil {
		return nil, errno.ErrNoticeChannelCreateFailed
	}

	return &v1.CreateNoticeChannelResponse{
		NoticeChannel: conversion.NoticeChannelMToNoticeChannelV1(obj),
	}, nil
}

func (b *noticeBiz) GetChannel(ctx context.Context, rq *v1.GetNoticeChannelRequest) (*v1.GetNoticeChannelResponse, error) {
	m, err := b.store.NoticeChannel().Get(ctx, where.T(ctx).F("channel_id", strings.TrimSpace(rq.GetChannelID())))
	if err != nil {
		return nil, toNoticeChannelGetError(err)
	}
	return &v1.GetNoticeChannelResponse{
		NoticeChannel: conversion.NoticeChannelMToNoticeChannelV1(m),
	}, nil
}

func (b *noticeBiz) ListChannels(ctx context.Context, rq *v1.ListNoticeChannelsRequest) (*v1.ListNoticeChannelsResponse, error) {
	limit := normalizeNoticeListLimit(rq.GetLimit(), defaultNoticeChannelListLimit, maxNoticeChannelListLimit)
	whr := where.T(ctx).O(int(rq.GetOffset())).L(int(limit))
	if rq.Enabled != nil {
		whr = whr.F("enabled", rq.GetEnabled())
	}

	total, list, err := b.store.NoticeChannel().List(ctx, whr)
	if err != nil {
		return nil, errno.ErrNoticeChannelListFailed
	}

	out := make([]*v1.NoticeChannel, 0, len(list))
	for _, item := range list {
		out = append(out, conversion.NoticeChannelMToNoticeChannelV1(item))
	}
	return &v1.ListNoticeChannelsResponse{
		TotalCount:     total,
		NoticeChannels: out,
	}, nil
}

//nolint:gocognit,gocyclo // Patch keeps explicit field-by-field behavior for auditability.
func (b *noticeBiz) PatchChannel(ctx context.Context, rq *v1.PatchNoticeChannelRequest) (*v1.PatchNoticeChannelResponse, error) {
	obj, err := b.store.NoticeChannel().Get(ctx, where.T(ctx).F("channel_id", strings.TrimSpace(rq.GetChannelID())))
	if err != nil {
		return nil, toNoticeChannelGetError(err)
	}

	if rq.Enabled != nil {
		obj.Enabled = rq.GetEnabled()
	}
	if rq.EndpointURL != nil {
		obj.EndpointURL = strings.TrimSpace(rq.GetEndpointURL())
	}
	if rq.Headers != nil {
		obj.HeadersJSON = conversion.EncodeStringMap(rq.GetHeaders())
	}
	if rq.TimeoutMs != nil {
		obj.TimeoutMs = rq.GetTimeoutMs()
	}
	if rq.MaxRetries != nil {
		obj.MaxRetries = rq.GetMaxRetries()
	}
	if rq.Secret != nil {
		obj.Secret = normalizeOptionalString(rq.Secret)
	}
	if rq.GetSelectors() != nil {
		obj.SelectorsJSON = conversion.EncodeNoticeSelectors(rq.GetSelectors())
	}
	currentPayloadMode := normalizeNoticePayloadMode(obj.PayloadMode)
	targetPayloadMode := currentPayloadMode
	payloadModeChanged := false
	if rq.PayloadMode != nil {
		targetPayloadMode = noticePayloadModeFromV1(rq.GetPayloadMode())
		payloadModeChanged = targetPayloadMode != currentPayloadMode
		obj.PayloadMode = targetPayloadMode
	}
	if rq.IncludeDiagnosis != nil {
		obj.IncludeDiagnosis = rq.GetIncludeDiagnosis()
	} else if payloadModeChanged {
		obj.IncludeDiagnosis = noticePayloadModeDefaultInclude(targetPayloadMode)
	}
	if rq.IncludeEvidenceIds != nil {
		obj.IncludeEvidenceIDs = rq.GetIncludeEvidenceIds()
	} else if payloadModeChanged {
		obj.IncludeEvidenceIDs = noticePayloadModeDefaultInclude(targetPayloadMode)
	}
	if rq.IncludeRootCause != nil {
		obj.IncludeRootCause = rq.GetIncludeRootCause()
	} else if payloadModeChanged {
		obj.IncludeRootCause = noticePayloadModeDefaultInclude(targetPayloadMode)
	}
	if rq.IncludeLinks != nil {
		obj.IncludeLinks = rq.GetIncludeLinks()
	} else if payloadModeChanged {
		obj.IncludeLinks = noticePayloadModeDefaultInclude(targetPayloadMode)
	}
	if err := b.store.NoticeChannel().Update(ctx, obj); err != nil {
		return nil, errno.ErrNoticeChannelUpdateFailed
	}
	return &v1.PatchNoticeChannelResponse{}, nil
}

func (b *noticeBiz) DeleteChannel(ctx context.Context, rq *v1.DeleteNoticeChannelRequest) (*v1.DeleteNoticeChannelResponse, error) {
	obj, err := b.store.NoticeChannel().Get(ctx, where.T(ctx).F("channel_id", strings.TrimSpace(rq.GetChannelID())))
	if err != nil {
		return nil, toNoticeChannelGetError(err)
	}
	if !obj.Enabled {
		return &v1.DeleteNoticeChannelResponse{}, nil
	}
	obj.Enabled = false
	if err := b.store.NoticeChannel().Update(ctx, obj); err != nil {
		return nil, errno.ErrNoticeChannelDeleteFailed
	}
	return &v1.DeleteNoticeChannelResponse{}, nil
}

func (b *noticeBiz) GetDelivery(ctx context.Context, rq *v1.GetNoticeDeliveryRequest) (*v1.GetNoticeDeliveryResponse, error) {
	m, err := b.store.NoticeDelivery().Get(ctx, where.T(ctx).F("delivery_id", strings.TrimSpace(rq.GetDeliveryID())))
	if err != nil {
		return nil, toNoticeDeliveryGetError(err)
	}
	return &v1.GetNoticeDeliveryResponse{
		NoticeDelivery: conversion.NoticeDeliveryMToNoticeDeliveryV1(m),
	}, nil
}

func (b *noticeBiz) ListDeliveries(ctx context.Context, rq *v1.ListNoticeDeliveriesRequest) (*v1.ListNoticeDeliveriesResponse, error) {
	limit := normalizeNoticeListLimit(rq.GetLimit(), defaultNoticeDeliveryListLimit, maxNoticeDeliveryListLimit)
	whr := where.T(ctx).O(int(rq.GetOffset())).L(int(limit))

	if v := strings.TrimSpace(rq.GetChannelID()); v != "" {
		whr = whr.F("channel_id", v)
	}
	if v := strings.TrimSpace(rq.GetIncidentID()); v != "" {
		whr = whr.F("incident_id", v)
	}
	if v := strings.TrimSpace(rq.GetEventType()); v != "" {
		whr = whr.F("event_type", strings.ToLower(v))
	}
	if v := strings.TrimSpace(rq.GetStatus()); v != "" {
		whr = whr.F("status", strings.ToLower(v))
	}

	total, list, err := b.store.NoticeDelivery().List(ctx, whr)
	if err != nil {
		return nil, errno.ErrNoticeDeliveryListFailed
	}

	out := make([]*v1.NoticeDelivery, 0, len(list))
	for _, item := range list {
		out = append(out, conversion.NoticeDeliveryMToNoticeDeliveryV1(item))
	}
	return &v1.ListNoticeDeliveriesResponse{
		TotalCount:       total,
		NoticeDeliveries: out,
	}, nil
}

//nolint:dupl // Replay/Cancel wrappers intentionally mirror each other with different operation specs.
func (b *noticeBiz) ReplayDelivery(ctx context.Context, rq *v1.ReplayNoticeDeliveryRequest) (*v1.ReplayNoticeDeliveryResponse, error) {
	deliveryID := strings.TrimSpace(rq.GetDeliveryID())
	replayMode := replayModeSnapshot
	if rq.GetUseLatestChannel() {
		replayMode = replayModeLatest
	}

	opts, beforeSnapshotEndpoint, err := b.prepareReplayOptions(ctx, deliveryID, rq.GetUseLatestChannel())
	if err != nil {
		return nil, err
	}

	out, err := b.store.NoticeDelivery().Replay(ctx, deliveryID, time.Now().UTC(), opts)
	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrNoticeDeliveryNotFound
		}
		return nil, errno.ErrNoticeDeliveryReplayFailed
	}
	status := strings.ToLower(strings.TrimSpace(out.Status))
	if metrics.M != nil {
		metrics.M.RecordNoticeDeliveryReplay(ctx, status, replayMode)
	}
	slog.InfoContext(ctx, "notice delivery op succeeded",
		"op", "replay",
		"replay_mode", replayMode,
		"delivery_id", out.DeliveryID,
		"status", out.Status,
		"snapshot_endpoint_before", beforeSnapshotEndpoint,
		"snapshot_endpoint_after", snapshotEndpointFromDelivery(out),
		"request_id", contextx.RequestID(ctx),
	)
	noticeDelivery := conversion.NoticeDeliveryMToNoticeDeliveryV1(out)

	return &v1.ReplayNoticeDeliveryResponse{
		NoticeDelivery: noticeDelivery,
	}, nil
}

//nolint:dupl // Replay/Cancel wrappers intentionally mirror each other with different operation specs.
func (b *noticeBiz) CancelDelivery(ctx context.Context, rq *v1.CancelNoticeDeliveryRequest) (*v1.CancelNoticeDeliveryResponse, error) {
	noticeDelivery, err := b.operateDelivery(ctx, strings.TrimSpace(rq.GetDeliveryID()), deliveryOpSpec{
		op:          "cancel",
		exec:        b.store.NoticeDelivery().Cancel,
		internalErr: errno.ErrNoticeDeliveryCancelFailed,
		recordMetric: func(status string) {
			if metrics.M != nil {
				metrics.M.RecordNoticeDeliveryCancel(ctx, status)
			}
		},
	})
	if err != nil {
		return nil, err
	}

	return &v1.CancelNoticeDeliveryResponse{
		NoticeDelivery: noticeDelivery,
	}, nil
}

func (b *noticeBiz) prepareReplayOptions(
	ctx context.Context,
	deliveryID string,
	useLatest bool,
) (store.ReplayDeliveryOptions, string, error) {

	if !useLatest {
		return store.ReplayDeliveryOptions{}, "", nil
	}

	current, err := b.store.NoticeDelivery().Get(ctx, where.T(ctx).F("delivery_id", deliveryID))
	if err != nil {
		return store.ReplayDeliveryOptions{}, "", toNoticeDeliveryGetError(err)
	}
	beforeSnapshotEndpoint := snapshotEndpointFromDelivery(current)
	if !isReplayStatusMutable(current.Status) {
		return store.ReplayDeliveryOptions{}, beforeSnapshotEndpoint, nil
	}

	channel, err := b.store.NoticeChannel().Get(ctx, where.T(ctx).F("channel_id", current.ChannelID))
	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return store.ReplayDeliveryOptions{}, "", errno.ErrNoticeDeliveryReplayLatestChannelNotFound
		}
		return store.ReplayDeliveryOptions{}, "", errno.ErrNoticeDeliveryReplayFailed
	}
	snapshot, err := noticepkg.BuildDeliverySnapshotFromChannel(channel)
	if err != nil {
		return store.ReplayDeliveryOptions{}, "", errno.ErrNoticeDeliveryReplayFailed
	}
	return store.ReplayDeliveryOptions{Snapshot: snapshot}, beforeSnapshotEndpoint, nil
}

func isReplayStatusMutable(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "pending":
		return true
	default:
		return false
	}
}

func snapshotEndpointFromDelivery(delivery *model.NoticeDeliveryM) string {
	if delivery == nil || delivery.SnapshotEndpointURL == nil {
		return ""
	}
	return strings.TrimSpace(*delivery.SnapshotEndpointURL)
}

type deliveryOpSpec struct {
	op           string
	exec         func(context.Context, string, time.Time) (*model.NoticeDeliveryM, error)
	internalErr  error
	recordMetric func(status string)
}

func (b *noticeBiz) operateDelivery(
	ctx context.Context,
	deliveryID string,
	spec deliveryOpSpec,
) (*v1.NoticeDelivery, error) {

	out, err := spec.exec(ctx, deliveryID, time.Now().UTC())
	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrNoticeDeliveryNotFound
		}
		return nil, spec.internalErr
	}

	status := strings.ToLower(strings.TrimSpace(out.Status))
	spec.recordMetric(status)
	slog.InfoContext(ctx, "notice delivery op succeeded",
		"op", spec.op,
		"delivery_id", out.DeliveryID,
		"status", out.Status,
		"request_id", contextx.RequestID(ctx),
	)
	return conversion.NoticeDeliveryMToNoticeDeliveryV1(out), nil
}

func noticePayloadModeFromV1(mode v1.NoticePayloadMode) string {
	switch mode {
	case v1.NoticePayloadMode_NOTICE_PAYLOAD_MODE_FULL:
		return noticepkg.NoticePayloadModeFull
	case v1.NoticePayloadMode_NOTICE_PAYLOAD_MODE_COMPACT, v1.NoticePayloadMode_NOTICE_PAYLOAD_MODE_UNSPECIFIED:
		return noticepkg.NoticePayloadModeCompact
	default:
		return noticepkg.NoticePayloadModeCompact
	}
}

func normalizeNoticePayloadMode(mode string) string {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case noticepkg.NoticePayloadModeFull:
		return noticepkg.NoticePayloadModeFull
	case noticepkg.NoticePayloadModeCompact:
		return noticepkg.NoticePayloadModeCompact
	default:
		return noticepkg.NoticePayloadModeCompact
	}
}

func noticePayloadModeDefaultInclude(mode string) bool {
	return normalizeNoticePayloadMode(mode) == noticepkg.NoticePayloadModeFull
}

func resolveNoticeTemplateInclude(v *bool, mode string) bool {
	if v != nil {
		return *v
	}
	return noticePayloadModeDefaultInclude(mode)
}

func normalizeOptionalString(v *string) *string {
	if v == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*v)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func normalizeNoticeListLimit(limit int64, defaultLimit int64, maxLimit int64) int64 {
	if limit <= 0 {
		return defaultLimit
	}
	if limit > maxLimit {
		return maxLimit
	}
	return limit
}

func toNoticeChannelGetError(err error) error {
	if errorsx.Is(err, gorm.ErrRecordNotFound) {
		return errno.ErrNoticeChannelNotFound
	}
	return errno.ErrNoticeChannelGetFailed
}

func toNoticeDeliveryGetError(err error) error {
	if errorsx.Is(err, gorm.ErrRecordNotFound) {
		return errno.ErrNoticeDeliveryNotFound
	}
	return errno.ErrNoticeDeliveryGetFailed
}
