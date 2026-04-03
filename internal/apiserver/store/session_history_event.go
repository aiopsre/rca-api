package store

import (
	"context"
	"strings"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
)

type SessionHistoryEventStore interface {
	Create(ctx context.Context, obj *model.SessionHistoryEventM) error
	ListBySession(ctx context.Context, sessionID string, offset int, limit int, ascending bool) (int64, []*model.SessionHistoryEventM, error)
	ListBySessionAndEventTypes(
		ctx context.Context,
		sessionID string,
		eventTypes []string,
		offset int,
		limit int,
		ascending bool,
	) (int64, []*model.SessionHistoryEventM, error)
	ListBySessionIDsAndEventTypes(
		ctx context.Context,
		sessionIDs []string,
		eventTypes []string,
		offset int,
		limit int,
		ascending bool,
	) (int64, []*model.SessionHistoryEventM, error)
}

type sessionHistoryEventStore struct {
	s *store
}

func newSessionHistoryEventStore(s *store) *sessionHistoryEventStore {
	return &sessionHistoryEventStore{s: s}
}

func (sse *sessionHistoryEventStore) Create(ctx context.Context, obj *model.SessionHistoryEventM) error {
	return sse.s.DB(ctx).Create(obj).Error
}

func (sse *sessionHistoryEventStore) ListBySession(
	ctx context.Context,
	sessionID string,
	offset int,
	limit int,
	ascending bool,
) (int64, []*model.SessionHistoryEventM, error) {
	return sse.listBySessionIDs(ctx, []string{sessionID}, nil, offset, limit, ascending)
}

func (sse *sessionHistoryEventStore) ListBySessionAndEventTypes(
	ctx context.Context,
	sessionID string,
	eventTypes []string,
	offset int,
	limit int,
	ascending bool,
) (int64, []*model.SessionHistoryEventM, error) {
	return sse.listBySessionIDs(ctx, []string{sessionID}, eventTypes, offset, limit, ascending)
}

func (sse *sessionHistoryEventStore) ListBySessionIDsAndEventTypes(
	ctx context.Context,
	sessionIDs []string,
	eventTypes []string,
	offset int,
	limit int,
	ascending bool,
) (int64, []*model.SessionHistoryEventM, error) {
	return sse.listBySessionIDs(ctx, sessionIDs, eventTypes, offset, limit, ascending)
}

func (sse *sessionHistoryEventStore) listBySessionIDs(
	ctx context.Context,
	sessionIDs []string,
	eventTypes []string,
	offset int,
	limit int,
	ascending bool,
) (int64, []*model.SessionHistoryEventM, error) {
	normalizedSessionIDs := normalizeSessionHistorySessionIDs(sessionIDs)
	if len(normalizedSessionIDs) == 0 {
		return 0, []*model.SessionHistoryEventM{}, nil
	}
	db := sse.s.DB(ctx).Where("session_id IN ?", normalizedSessionIDs)
	filteredEventTypes := normalizeSessionHistoryEventTypes(eventTypes)
	if len(filteredEventTypes) > 0 {
		db = db.Where("event_type IN ?", filteredEventTypes)
	}

	var total int64
	if err := db.Model(&model.SessionHistoryEventM{}).Count(&total).Error; err != nil {
		return 0, nil, err
	}

	orderExpr := "created_at DESC, id DESC"
	if ascending {
		orderExpr = "created_at ASC, id ASC"
	}
	listDB := db.Order(orderExpr).Offset(offset).Limit(limit)
	var list []*model.SessionHistoryEventM
	if err := listDB.Find(&list).Error; err != nil {
		return 0, nil, err
	}
	return total, list, nil
}

func normalizeSessionHistoryEventTypes(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, item := range in {
		value := strings.TrimSpace(item)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func normalizeSessionHistorySessionIDs(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, item := range in {
		value := strings.TrimSpace(item)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
