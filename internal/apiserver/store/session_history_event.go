package store

import (
	"context"
	"strings"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
)

type SessionHistoryEventStore interface {
	Create(ctx context.Context, obj *model.SessionHistoryEventM) error
	ListBySession(ctx context.Context, sessionID string, offset int, limit int, ascending bool) (int64, []*model.SessionHistoryEventM, error)
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
	sessionID = strings.TrimSpace(sessionID)
	db := sse.s.DB(ctx).Where("session_id = ?", sessionID)

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
