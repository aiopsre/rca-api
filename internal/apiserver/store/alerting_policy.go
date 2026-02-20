package store

import (
	"context"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

type AlertingPolicyStore interface {
	Create(ctx context.Context, obj *model.AlertingPolicyM) error
	Update(ctx context.Context, obj *model.AlertingPolicyM) error
	Delete(ctx context.Context, opts *where.Options) error
	Get(ctx context.Context, opts *where.Options) (*model.AlertingPolicyM, error)
	List(ctx context.Context, opts *where.Options) (int64, []*model.AlertingPolicyM, error)
	GetActive(ctx context.Context) (*model.AlertingPolicyM, error)
	Activate(ctx context.Context, id int64, operator string) error
	Deactivate(ctx context.Context, id int64) error

	AlertingPolicyExpansion
}

type AlertingPolicyExpansion interface{}

type alertingPolicyStore struct {
	s *store
}

func newAlertingPolicyStore(s *store) *alertingPolicyStore { return &alertingPolicyStore{s: s} }

func (a *alertingPolicyStore) Create(ctx context.Context, obj *model.AlertingPolicyM) error {
	return a.s.DB(ctx).Create(obj).Error
}

func (a *alertingPolicyStore) Update(ctx context.Context, obj *model.AlertingPolicyM) error {
	return a.s.DB(ctx).Save(obj).Error
}

func (a *alertingPolicyStore) Delete(ctx context.Context, opts *where.Options) error {
	db := a.s.DB(ctx)
	if opts != nil {
		db = opts.Where(db)
	}
	return db.Delete(&model.AlertingPolicyM{}).Error
}

func (a *alertingPolicyStore) Get(ctx context.Context, opts *where.Options) (*model.AlertingPolicyM, error) {
	db := a.s.DB(ctx)
	if opts != nil {
		db = opts.Where(db)
	}
	var out model.AlertingPolicyM
	if err := db.First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

func (a *alertingPolicyStore) List(ctx context.Context, opts *where.Options) (int64, []*model.AlertingPolicyM, error) {
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
	if err := base.Model(&model.AlertingPolicyM{}).Count(&total).Error; err != nil {
		return 0, nil, err
	}

	listDB := base
	if opts != nil {
		listDB = listDB.Offset(opts.Offset).Limit(opts.Limit)
	}
	var list []*model.AlertingPolicyM
	if err := listDB.Order("id DESC").Find(&list).Error; err != nil {
		return 0, nil, err
	}
	return total, list, nil
}

func (a *alertingPolicyStore) GetActive(ctx context.Context) (*model.AlertingPolicyM, error) {
	var out model.AlertingPolicyM
	if err := a.s.DB(ctx).Where("active = ?", true).First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

func (a *alertingPolicyStore) Activate(ctx context.Context, id int64, operator string) error {
	return a.s.TX(ctx, func(ctx context.Context) error {
		db := a.s.DB(ctx)

		if err := db.Model(&model.AlertingPolicyM{}).Where("active = ?", true).Updates(map[string]any{
			"active": false,
		}).Error; err != nil {
			return err
		}

		now := a.s.DB(ctx).NowFunc()
		return db.Model(&model.AlertingPolicyM{}).Where("id = ?", id).Updates(map[string]any{
			"active":       true,
			"activated_at": &now,
			"activated_by": &operator,
			"updated_by":   &operator,
		}).Error
	})
}

func (a *alertingPolicyStore) Deactivate(ctx context.Context, id int64) error {
	return a.s.DB(ctx).Model(&model.AlertingPolicyM{}).Where("id = ?", id).Updates(map[string]any{
		"active": false,
	}).Error
}
