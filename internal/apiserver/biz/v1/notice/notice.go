package notice

//go:generate mockgen -destination mock_notice.go -package notice zk8s.com/rca-api/internal/apiserver/biz/v1/notice NoticeBiz

import (
	"context"
	"strings"

	"github.com/onexstack/onexstack/pkg/errorsx"
	"gorm.io/gorm"

	"zk8s.com/rca-api/internal/apiserver/model"
	"zk8s.com/rca-api/internal/apiserver/pkg/conversion"
	"zk8s.com/rca-api/internal/apiserver/store"
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
	defaultNoticeRetries   = int64(0)
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

	obj := &model.NoticeChannelM{
		Name:        strings.TrimSpace(rq.GetName()),
		Type:        chType,
		Enabled:     enabled,
		EndpointURL: strings.TrimSpace(rq.GetEndpointURL()),
		Secret:      normalizeOptionalString(rq.Secret),
		HeadersJSON: conversion.EncodeStringMap(rq.GetHeaders()),
		TimeoutMs:   timeoutMs,
		MaxRetries:  maxRetries,
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
