package store

import (
	"context"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

type IncidentActionLogStore interface {
	Create(ctx context.Context, obj *model.IncidentActionLogM) error
	List(ctx context.Context, opts *where.Options) (int64, []*model.IncidentActionLogM, error)
}

type incidentActionLogStore struct {
	s *store
}

func newIncidentActionLogStore(s *store) *incidentActionLogStore {
	return &incidentActionLogStore{s: s}
}

func (i *incidentActionLogStore) Create(ctx context.Context, obj *model.IncidentActionLogM) error {
	return i.s.DB(ctx).Create(obj).Error
}

//nolint:dupl // Follow shared store list pattern used by other domain stores.
func (i *incidentActionLogStore) List(ctx context.Context, opts *where.Options) (int64, []*model.IncidentActionLogM, error) {
	db := i.s.DB(ctx)

	base := db
	if opts != nil {
		base = base.Where(opts.Filters).Clauses(opts.Clauses...)
		for _, q := range opts.Queries {
			conds := base.Statement.BuildCondition(q.Query, q.Args...)
			base = base.Clauses(conds...)
		}
	}

	var total int64
	if err := base.Model(&model.IncidentActionLogM{}).Count(&total).Error; err != nil {
		return 0, nil, err
	}

	listDB := base
	if opts != nil {
		listDB = listDB.Offset(opts.Offset).Limit(opts.Limit)
	}

	var list []*model.IncidentActionLogM
	if err := listDB.Order("id DESC").Find(&list).Error; err != nil {
		return 0, nil, err
	}
	return total, list, nil
}
