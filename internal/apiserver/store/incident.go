package store

import (
	"context"

	"zk8s.com/rca-api/internal/apiserver/model"
	"zk8s.com/rca-api/pkg/store/where"
)

type IncidentStore interface {
	Create(ctx context.Context, obj *model.IncidentM) error
	Update(ctx context.Context, obj *model.IncidentM) error
	Delete(ctx context.Context, opts *where.Options) error
	Get(ctx context.Context, opts *where.Options) (*model.IncidentM, error)
	List(ctx context.Context, opts *where.Options) (int64, []*model.IncidentM, error)

	IncidentExpansion
}

type IncidentExpansion interface{}

type incidentStore struct {
	s *store
}

func newIncidentStore(s *store) *incidentStore { return &incidentStore{s: s} }

func (i *incidentStore) Create(ctx context.Context, obj *model.IncidentM) error {
	return i.s.DB(ctx).Create(obj).Error
}

func (i *incidentStore) Update(ctx context.Context, obj *model.IncidentM) error {
	return i.s.DB(ctx).Save(obj).Error
}

func (i *incidentStore) Delete(ctx context.Context, opts *where.Options) error {
	db := i.s.DB(ctx)
	if opts != nil {
		db = opts.Where(db)
	}
	return db.Delete(&model.IncidentM{}).Error
}

func (i *incidentStore) Get(ctx context.Context, opts *where.Options) (*model.IncidentM, error) {
	db := i.s.DB(ctx)
	if opts != nil {
		db = opts.Where(db)
	}
	var out model.IncidentM
	if err := db.First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

func (i *incidentStore) List(ctx context.Context, opts *where.Options) (int64, []*model.IncidentM, error) {
	db := i.s.DB(ctx)

	// base：只应用 filters/clauses（不应用 offset/limit）
	base := db
	if opts != nil {
		// 复用 opts.Where 的逻辑，但不要 Offset/Limit
		base = base.Where(opts.Filters).Clauses(opts.Clauses...)
		for _, q := range opts.Queries {
			conds := base.Statement.BuildCondition(q.Query, q.Args...)
			base = base.Clauses(conds...)
		}
	}

	var total int64
	if err := base.Model(&model.IncidentM{}).Count(&total).Error; err != nil {
		return 0, nil, err
	}

	// list：带分页 + 建议加稳定排序
	listDB := base
	if opts != nil {
		listDB = listDB.Offset(opts.Offset).Limit(opts.Limit)
	}
	var list []*model.IncidentM
	if err := listDB.Order("id DESC").Find(&list).Error; err != nil {
		return 0, nil, err
	}
	return total, list, nil
}
