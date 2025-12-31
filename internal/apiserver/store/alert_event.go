package store

import (
	"context"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

type AlertEventStore interface {
	Create(ctx context.Context, obj *model.AlertEventM) error
	Update(ctx context.Context, obj *model.AlertEventM) error
	Get(ctx context.Context, opts *where.Options) (*model.AlertEventM, error)
	List(ctx context.Context, opts *where.Options) (int64, []*model.AlertEventM, error)
	UpdateByEventID(ctx context.Context, eventID string, updates map[string]any) (int64, error)

	AlertEventExpansion
}

type AlertEventExpansion interface{}

type alertEventStore struct {
	s *store
}

func newAlertEventStore(s *store) *alertEventStore { return &alertEventStore{s: s} }

func (a *alertEventStore) Create(ctx context.Context, obj *model.AlertEventM) error {
	return a.s.DB(ctx).Create(obj).Error
}

func (a *alertEventStore) Update(ctx context.Context, obj *model.AlertEventM) error {
	return a.s.DB(ctx).Save(obj).Error
}

func (a *alertEventStore) Get(ctx context.Context, opts *where.Options) (*model.AlertEventM, error) {
	db := a.s.DB(ctx)
	if opts != nil {
		db = opts.Where(db)
	}

	var out model.AlertEventM
	if err := db.First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

func (a *alertEventStore) List(ctx context.Context, opts *where.Options) (int64, []*model.AlertEventM, error) {
	db := a.s.DB(ctx)

	base := db
	if opts != nil {
		base = base.Where(opts.Filters).Clauses(opts.Clauses...)
		for _, q := range opts.Queries {
			conds := base.Statement.BuildCondition(q.Query, q.Args...)
			base = base.Clauses(conds...)
		}
	}

	var total int64
	if err := base.Model(&model.AlertEventM{}).Count(&total).Error; err != nil {
		return 0, nil, err
	}

	listDB := base
	if opts != nil {
		listDB = listDB.Offset(opts.Offset).Limit(opts.Limit)
	}

	var list []*model.AlertEventM
	if err := listDB.Order("last_seen_at DESC").Order("id DESC").Find(&list).Error; err != nil {
		return 0, nil, err
	}

	return total, list, nil
}

func (a *alertEventStore) UpdateByEventID(ctx context.Context, eventID string, updates map[string]any) (int64, error) {
	res := a.s.DB(ctx).Model(&model.AlertEventM{}).Where("event_id = ?", eventID).Updates(updates)
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}
