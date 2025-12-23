//nolint:dupl
package store

import (
	"context"
	"time"

	"zk8s.com/rca-api/internal/apiserver/model"
	"zk8s.com/rca-api/pkg/store/where"
)

//nolint:interfacebloat // Store interface follows repo pattern (CRUD + list helpers + expansion).
type SilenceStore interface {
	Create(ctx context.Context, obj *model.SilenceM) error
	Update(ctx context.Context, obj *model.SilenceM) error
	Get(ctx context.Context, opts *where.Options) (*model.SilenceM, error)
	List(ctx context.Context, opts *where.Options) (int64, []*model.SilenceM, error)
	ListActive(ctx context.Context, namespace string, now time.Time) ([]*model.SilenceM, error)

	SilenceExpansion
}

//nolint:iface,modernize // Keep explicit empty interface as a placeholder expansion point.
type SilenceExpansion interface{}

type silenceStore struct {
	s *store
}

func newSilenceStore(s *store) *silenceStore { return &silenceStore{s: s} }

func (s *silenceStore) Create(ctx context.Context, obj *model.SilenceM) error {
	return s.s.DB(ctx).Create(obj).Error
}

func (s *silenceStore) Update(ctx context.Context, obj *model.SilenceM) error {
	return s.s.DB(ctx).Save(obj).Error
}

func (s *silenceStore) Get(ctx context.Context, opts *where.Options) (*model.SilenceM, error) {
	db := s.s.DB(ctx)
	if opts != nil {
		db = opts.Where(db)
	}
	var out model.SilenceM
	if err := db.First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *silenceStore) List(ctx context.Context, opts *where.Options) (int64, []*model.SilenceM, error) {
	db := s.s.DB(ctx)

	base := db
	if opts != nil {
		base = base.Where(opts.Filters).Clauses(opts.Clauses...)
		for _, q := range opts.Queries {
			conds := base.Statement.BuildCondition(q.Query, q.Args...)
			base = base.Clauses(conds...)
		}
	}

	var total int64
	if err := base.Model(&model.SilenceM{}).Count(&total).Error; err != nil {
		return 0, nil, err
	}

	listDB := base
	if opts != nil {
		listDB = listDB.Offset(opts.Offset).Limit(opts.Limit)
	}

	var list []*model.SilenceM
	if err := listDB.Order("created_at DESC").Order("id DESC").Find(&list).Error; err != nil {
		return 0, nil, err
	}
	return total, list, nil
}

func (s *silenceStore) ListActive(ctx context.Context, namespace string, now time.Time) ([]*model.SilenceM, error) {
	var out []*model.SilenceM
	err := s.s.DB(ctx).
		Where("namespace = ? AND enabled = ? AND starts_at <= ? AND ends_at >= ?", namespace, true, now, now).
		Order("created_at DESC").
		Order("id DESC").
		Find(&out).Error
	if err != nil {
		return nil, err
	}
	return out, nil
}
