package store

import (
	"context"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

type EvidenceStore interface {
	Create(ctx context.Context, obj *model.EvidenceM) error
	Update(ctx context.Context, obj *model.EvidenceM) error
	Delete(ctx context.Context, opts *where.Options) error
	Get(ctx context.Context, opts *where.Options) (*model.EvidenceM, error)
	List(ctx context.Context, opts *where.Options) (int64, []*model.EvidenceM, error)

	EvidenceExpansion
}

type EvidenceExpansion interface{}

type evidenceStore struct {
	s *store
}

func newEvidenceStore(s *store) *evidenceStore { return &evidenceStore{s: s} }

func (e *evidenceStore) Create(ctx context.Context, obj *model.EvidenceM) error {
	return e.s.DB(ctx).Create(obj).Error
}

func (e *evidenceStore) Update(ctx context.Context, obj *model.EvidenceM) error {
	return e.s.DB(ctx).Save(obj).Error
}

func (e *evidenceStore) Delete(ctx context.Context, opts *where.Options) error {
	db := e.s.DB(ctx)
	if opts != nil {
		db = opts.Where(db)
	}
	return db.Delete(&model.EvidenceM{}).Error
}

func (e *evidenceStore) Get(ctx context.Context, opts *where.Options) (*model.EvidenceM, error) {
	db := e.s.DB(ctx)
	if opts != nil {
		db = opts.Where(db)
	}
	var out model.EvidenceM
	if err := db.First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

func (e *evidenceStore) List(ctx context.Context, opts *where.Options) (int64, []*model.EvidenceM, error) {
	db := e.s.DB(ctx)

	base := db
	if opts != nil {
		base = base.Where(opts.Filters).Clauses(opts.Clauses...)
		for _, q := range opts.Queries {
			conds := base.Statement.BuildCondition(q.Query, q.Args...)
			base = base.Clauses(conds...)
		}
	}

	var total int64
	if err := base.Model(&model.EvidenceM{}).Count(&total).Error; err != nil {
		return 0, nil, err
	}

	listDB := base
	if opts != nil {
		listDB = listDB.Offset(opts.Offset).Limit(opts.Limit)
	}
	var list []*model.EvidenceM
	if err := listDB.Order("id DESC").Find(&list).Error; err != nil {
		return 0, nil, err
	}
	return total, list, nil
}
