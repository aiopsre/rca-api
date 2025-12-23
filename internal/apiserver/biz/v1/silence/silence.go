package silence

//go:generate mockgen -destination mock_silence.go -package silence zk8s.com/rca-api/internal/apiserver/biz/v1/silence SilenceBiz

import (
	"context"
	"strings"
	"time"

	"github.com/onexstack/onexstack/pkg/errorsx"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"zk8s.com/rca-api/internal/apiserver/model"
	"zk8s.com/rca-api/internal/apiserver/pkg/conversion"
	"zk8s.com/rca-api/internal/apiserver/pkg/silenceutil"
	"zk8s.com/rca-api/internal/apiserver/store"
	"zk8s.com/rca-api/internal/pkg/contextx"
	"zk8s.com/rca-api/internal/pkg/errno"
	v1 "zk8s.com/rca-api/pkg/api/apiserver/v1"
	"zk8s.com/rca-api/pkg/store/where"
)

const (
	defaultListLimit = int64(20)
	maxListLimit     = int64(200)
)

// SilenceBiz defines silence use-cases.
//
//nolint:interfacebloat // CRUD + list intentionally grouped in one biz entrypoint.
type SilenceBiz interface {
	Create(ctx context.Context, rq *v1.CreateSilenceRequest) (*v1.CreateSilenceResponse, error)
	Get(ctx context.Context, rq *v1.GetSilenceRequest) (*v1.GetSilenceResponse, error)
	List(ctx context.Context, rq *v1.ListSilencesRequest) (*v1.ListSilencesResponse, error)
	Patch(ctx context.Context, rq *v1.PatchSilenceRequest) (*v1.PatchSilenceResponse, error)
	Delete(ctx context.Context, rq *v1.DeleteSilenceRequest) (*v1.DeleteSilenceResponse, error)

	SilenceExpansion
}

//nolint:modernize // Keep explicit empty interface as placeholder expansion point.
type SilenceExpansion interface{}

type silenceBiz struct {
	store store.IStore
}

var _ SilenceBiz = (*silenceBiz)(nil)

// New creates silence biz.
func New(store store.IStore) *silenceBiz {
	return &silenceBiz{store: store}
}

func (b *silenceBiz) Create(ctx context.Context, rq *v1.CreateSilenceRequest) (*v1.CreateSilenceResponse, error) {
	matchers := conversion.SilenceMatchersFromV1(rq.GetMatchers())
	matchersJSON, err := silenceutil.EncodeMatchers(matchers)
	if err != nil {
		return nil, errorsx.ErrInvalidArgument
	}

	namespace := silenceutil.NormalizeNamespace(rq.GetNamespace())
	enabled := true
	if rq.Enabled != nil {
		enabled = rq.GetEnabled()
	}

	startsAt := rq.GetStartsAt().AsTime().UTC()
	endsAt := rq.GetEndsAt().AsTime().UTC()
	reason := normalizeReason(rq.Reason)
	createdBy := normalizeCreatedBy(ctx, rq.CreatedBy)

	obj := &model.SilenceM{
		Namespace:    namespace,
		Enabled:      enabled,
		StartsAt:     startsAt,
		EndsAt:       endsAt,
		MatchersJSON: matchersJSON,
		CreatedBy:    strPtrOrNil(createdBy),
		Reason:       reason,
	}
	if err := b.store.Silence().Create(ctx, obj); err != nil {
		return nil, errno.ErrSilenceCreateFailed
	}

	return &v1.CreateSilenceResponse{
		Silence: conversion.SilenceMToSilenceV1(obj),
	}, nil
}

func (b *silenceBiz) Get(ctx context.Context, rq *v1.GetSilenceRequest) (*v1.GetSilenceResponse, error) {
	m, err := b.store.Silence().Get(ctx, where.T(ctx).F("silence_id", strings.TrimSpace(rq.GetSilenceID())))
	if err != nil {
		return nil, toSilenceGetError(err)
	}
	return &v1.GetSilenceResponse{Silence: conversion.SilenceMToSilenceV1(m)}, nil
}

func (b *silenceBiz) List(ctx context.Context, rq *v1.ListSilencesRequest) (*v1.ListSilencesResponse, error) {
	limit := normalizeListLimit(rq.GetLimit())
	whr := where.T(ctx).O(int(rq.GetOffset())).L(int(limit))

	if rq.Namespace != nil {
		ns := strings.TrimSpace(rq.GetNamespace())
		if ns != "" {
			whr = whr.F("namespace", ns)
		}
	}
	if rq.Enabled != nil {
		whr = whr.F("enabled", rq.GetEnabled())
	}
	if rq.Active != nil {
		now := time.Now().UTC()
		if rq.GetActive() {
			whr = whr.C(clause.Expr{SQL: "enabled = ? AND starts_at <= ? AND ends_at >= ?", Vars: []any{true, now, now}})
		} else {
			whr = whr.C(clause.Expr{SQL: "(enabled = ? OR starts_at > ? OR ends_at < ?)", Vars: []any{false, now, now}})
		}
	}

	total, list, err := b.store.Silence().List(ctx, whr)
	if err != nil {
		return nil, errno.ErrSilenceListFailed
	}

	out := make([]*v1.Silence, 0, len(list))
	for _, item := range list {
		out = append(out, conversion.SilenceMToSilenceV1(item))
	}
	return &v1.ListSilencesResponse{
		TotalCount: total,
		Silences:   out,
	}, nil
}

func (b *silenceBiz) Patch(ctx context.Context, rq *v1.PatchSilenceRequest) (*v1.PatchSilenceResponse, error) {
	obj, err := b.store.Silence().Get(ctx, where.T(ctx).F("silence_id", strings.TrimSpace(rq.GetSilenceID())))
	if err != nil {
		return nil, toSilenceGetError(err)
	}

	if rq.Enabled != nil {
		obj.Enabled = rq.GetEnabled()
	}
	if rq.GetEndsAt() != nil {
		endsAt := rq.GetEndsAt().AsTime().UTC()
		if !endsAt.After(obj.StartsAt) {
			return nil, errorsx.ErrInvalidArgument
		}
		obj.EndsAt = endsAt
	}
	if rq.Reason != nil {
		obj.Reason = normalizeReason(rq.Reason)
	}
	if err := b.store.Silence().Update(ctx, obj); err != nil {
		return nil, errno.ErrSilenceUpdateFailed
	}
	return &v1.PatchSilenceResponse{}, nil
}

func (b *silenceBiz) Delete(ctx context.Context, rq *v1.DeleteSilenceRequest) (*v1.DeleteSilenceResponse, error) {
	obj, err := b.store.Silence().Get(ctx, where.T(ctx).F("silence_id", strings.TrimSpace(rq.GetSilenceID())))
	if err != nil {
		return nil, toSilenceGetError(err)
	}

	if !obj.Enabled {
		return &v1.DeleteSilenceResponse{}, nil
	}
	obj.Enabled = false
	if err := b.store.Silence().Update(ctx, obj); err != nil {
		return nil, errno.ErrSilenceDeleteFailed
	}
	return &v1.DeleteSilenceResponse{}, nil
}

func toSilenceGetError(err error) error {
	if errorsx.Is(err, gorm.ErrRecordNotFound) {
		return errno.ErrSilenceNotFound
	}
	return errno.ErrSilenceGetFailed
}

func normalizeListLimit(limit int64) int64 {
	if limit <= 0 {
		return defaultListLimit
	}
	if limit > maxListLimit {
		return maxListLimit
	}
	return limit
}

func normalizeCreatedBy(ctx context.Context, in *string) string {
	if in != nil {
		if v := strings.TrimSpace(*in); v != "" {
			return v
		}
	}
	if username := strings.TrimSpace(contextx.Username(ctx)); username != "" {
		return "user:" + username
	}
	if userID := strings.TrimSpace(contextx.UserID(ctx)); userID != "" {
		return "user:" + userID
	}
	return "system"
}

func normalizeReason(reason *string) *string {
	if reason == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*reason)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func strPtrOrNil(v string) *string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	return &v
}
