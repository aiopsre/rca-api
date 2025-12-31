package store

import (
	"context"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

type AIToolCallStore interface {
	Create(ctx context.Context, obj *model.AIToolCallM) error
	Update(ctx context.Context, obj *model.AIToolCallM) error
	Delete(ctx context.Context, opts *where.Options) error
	Get(ctx context.Context, opts *where.Options) (*model.AIToolCallM, error)
	List(ctx context.Context, opts *where.Options) (int64, []*model.AIToolCallM, error)

	AIToolCallExpansion
}

type AIToolCallExpansion interface{}

type aiToolCallStore struct {
	s *store
}

func newAIToolCallStore(s *store) *aiToolCallStore { return &aiToolCallStore{s: s} }

func (a *aiToolCallStore) Create(ctx context.Context, obj *model.AIToolCallM) error {
	return a.s.DB(ctx).Create(obj).Error
}

func (a *aiToolCallStore) Update(ctx context.Context, obj *model.AIToolCallM) error {
	return a.s.DB(ctx).Save(obj).Error
}

func (a *aiToolCallStore) Delete(ctx context.Context, opts *where.Options) error {
	db := a.s.DB(ctx)
	if opts != nil {
		db = opts.Where(db)
	}
	return db.Delete(&model.AIToolCallM{}).Error
}

func (a *aiToolCallStore) Get(ctx context.Context, opts *where.Options) (*model.AIToolCallM, error) {
	db := a.s.DB(ctx)
	if opts != nil {
		db = opts.Where(db)
	}
	var out model.AIToolCallM
	if err := db.First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

func (a *aiToolCallStore) List(ctx context.Context, opts *where.Options) (int64, []*model.AIToolCallM, error) {
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
	if err := base.Model(&model.AIToolCallM{}).Count(&total).Error; err != nil {
		return 0, nil, err
	}

	listDB := base
	if opts != nil {
		listDB = listDB.Offset(opts.Offset).Limit(opts.Limit)
	}

	var list []*model.AIToolCallM
	if err := listDB.Order("seq ASC").Find(&list).Error; err != nil {
		return 0, nil, err
	}

	return total, list, nil
}
