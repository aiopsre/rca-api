package store

import (
	"context"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

type DatasourceStore interface {
	Create(ctx context.Context, obj *model.DatasourceM) error
	Update(ctx context.Context, obj *model.DatasourceM) error
	Delete(ctx context.Context, opts *where.Options) error
	Get(ctx context.Context, opts *where.Options) (*model.DatasourceM, error)
	List(ctx context.Context, opts *where.Options) (int64, []*model.DatasourceM, error)

	DatasourceExpansion
}

type DatasourceExpansion interface{}

type datasourceStore struct {
	s *store
}

func newDatasourceStore(s *store) *datasourceStore { return &datasourceStore{s: s} }

func (d *datasourceStore) Create(ctx context.Context, obj *model.DatasourceM) error {
	return d.s.DB(ctx).Create(obj).Error
}

func (d *datasourceStore) Update(ctx context.Context, obj *model.DatasourceM) error {
	return d.s.DB(ctx).Save(obj).Error
}

func (d *datasourceStore) Delete(ctx context.Context, opts *where.Options) error {
	db := d.s.DB(ctx)
	if opts != nil {
		db = opts.Where(db)
	}
	return db.Delete(&model.DatasourceM{}).Error
}

func (d *datasourceStore) Get(ctx context.Context, opts *where.Options) (*model.DatasourceM, error) {
	db := d.s.DB(ctx)
	if opts != nil {
		db = opts.Where(db)
	}
	var out model.DatasourceM
	if err := db.First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

func (d *datasourceStore) List(ctx context.Context, opts *where.Options) (int64, []*model.DatasourceM, error) {
	db := d.s.DB(ctx)

	base := db
	if opts != nil {
		base = base.Where(opts.Filters).Clauses(opts.Clauses...)
		for _, q := range opts.Queries {
			conds := base.Statement.BuildCondition(q.Query, q.Args...)
			base = base.Clauses(conds...)
		}
	}

	var total int64
	if err := base.Model(&model.DatasourceM{}).Count(&total).Error; err != nil {
		return 0, nil, err
	}

	listDB := base
	if opts != nil {
		listDB = listDB.Offset(opts.Offset).Limit(opts.Limit)
	}
	var list []*model.DatasourceM
	if err := listDB.Order("id DESC").Find(&list).Error; err != nil {
		return 0, nil, err
	}
	return total, list, nil
}
