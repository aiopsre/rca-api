package store

import (
	"context"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

type PlaybookStore interface {
	Create(ctx context.Context, obj *model.PlaybookM) error
	Update(ctx context.Context, obj *model.PlaybookM) error
	Delete(ctx context.Context, opts *where.Options) error
	Get(ctx context.Context, opts *where.Options) (*model.PlaybookM, error)
	List(ctx context.Context, opts *where.Options) (int64, []*model.PlaybookM, error)
	GetActive(ctx context.Context) (*model.PlaybookM, error)
	Activate(ctx context.Context, id int64, operator string) error
	Deactivate(ctx context.Context, id int64) error

	PlaybookExpansion
}

type PlaybookExpansion interface{}

type playbookStore struct {
	s *store
}

func newPlaybookStore(s *store) *playbookStore { return &playbookStore{s: s} }

func (p *playbookStore) Create(ctx context.Context, obj *model.PlaybookM) error {
	return p.s.DB(ctx).Create(obj).Error
}

func (p *playbookStore) Update(ctx context.Context, obj *model.PlaybookM) error {
	return p.s.DB(ctx).Save(obj).Error
}

func (p *playbookStore) Delete(ctx context.Context, opts *where.Options) error {
	db := p.s.DB(ctx)
	if opts != nil {
		db = opts.Where(db)
	}
	return db.Delete(&model.PlaybookM{}).Error
}

func (p *playbookStore) Get(ctx context.Context, opts *where.Options) (*model.PlaybookM, error) {
	db := p.s.DB(ctx)
	if opts != nil {
		db = opts.Where(db)
	}
	var out model.PlaybookM
	if err := db.First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

func (p *playbookStore) List(ctx context.Context, opts *where.Options) (int64, []*model.PlaybookM, error) {
	db := p.s.DB(ctx)

	base := db
	if opts != nil {
		base = base.Where(opts.Filters).Clauses(opts.Clauses...)
		for _, q := range opts.Queries {
			conds := base.Statement.BuildCondition(q.Query, q.Args...)
			base = base.Clauses(conds...)
		}
	}

	var total int64
	if err := base.Model(&model.PlaybookM{}).Count(&total).Error; err != nil {
		return 0, nil, err
	}

	listDB := base
	if opts != nil {
		listDB = listDB.Offset(opts.Offset).Limit(opts.Limit)
	}
	var list []*model.PlaybookM
	if err := listDB.Order("id DESC").Find(&list).Error; err != nil {
		return 0, nil, err
	}
	return total, list, nil
}

func (p *playbookStore) GetActive(ctx context.Context) (*model.PlaybookM, error) {
	var out model.PlaybookM
	if err := p.s.DB(ctx).Where("active = ?", true).First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

func (p *playbookStore) Activate(ctx context.Context, id int64, operator string) error {
	return p.s.TX(ctx, func(ctx context.Context) error {
		db := p.s.DB(ctx)

		if err := db.Model(&model.PlaybookM{}).Where("active = ?", true).Updates(map[string]any{
			"active": false,
		}).Error; err != nil {
			return err
		}

		now := p.s.DB(ctx).NowFunc()
		return db.Model(&model.PlaybookM{}).Where("id = ?", id).Updates(map[string]any{
			"active":       true,
			"activated_at": &now,
			"activated_by": &operator,
			"updated_by":   &operator,
		}).Error
	})
}

func (p *playbookStore) Deactivate(ctx context.Context, id int64) error {
	return p.s.DB(ctx).Model(&model.PlaybookM{}).Where("id = ?", id).Updates(map[string]any{
		"active": false,
	}).Error
}
