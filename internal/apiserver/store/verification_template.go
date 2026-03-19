package store

import (
	"context"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

type VerificationTemplateStore interface {
	Create(ctx context.Context, obj *model.VerificationTemplateM) error
	Update(ctx context.Context, obj *model.VerificationTemplateM) error
	Delete(ctx context.Context, opts *where.Options) error
	Get(ctx context.Context, opts *where.Options) (*model.VerificationTemplateM, error)
	List(ctx context.Context, opts *where.Options) (int64, []*model.VerificationTemplateM, error)
	GetActive(ctx context.Context) ([]*model.VerificationTemplateM, error)
	GetByID(ctx context.Context, id int64) (*model.VerificationTemplateM, error)
	GetByLineageID(ctx context.Context, lineageID string) ([]*model.VerificationTemplateM, error)
	Activate(ctx context.Context, id int64, operator string) error
	Deactivate(ctx context.Context, id int64) error

	VerificationTemplateExpansion
}

type VerificationTemplateExpansion interface{}

type verificationTemplateStore struct {
	s *store
}

func newVerificationTemplateStore(s *store) *verificationTemplateStore {
	return &verificationTemplateStore{s: s}
}

func (v *verificationTemplateStore) Create(ctx context.Context, obj *model.VerificationTemplateM) error {
	return v.s.DB(ctx).Create(obj).Error
}

func (v *verificationTemplateStore) Update(ctx context.Context, obj *model.VerificationTemplateM) error {
	return v.s.DB(ctx).Save(obj).Error
}

func (v *verificationTemplateStore) Delete(ctx context.Context, opts *where.Options) error {
	db := v.s.DB(ctx)
	if opts != nil {
		db = opts.Where(db)
	}
	return db.Delete(&model.VerificationTemplateM{}).Error
}

func (v *verificationTemplateStore) Get(ctx context.Context, opts *where.Options) (*model.VerificationTemplateM, error) {
	db := v.s.DB(ctx)
	if opts != nil {
		db = opts.Where(db)
	}
	var out model.VerificationTemplateM
	if err := db.First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

func (v *verificationTemplateStore) List(ctx context.Context, opts *where.Options) (int64, []*model.VerificationTemplateM, error) {
	db := v.s.DB(ctx)

	base := db
	if opts != nil {
		base = base.Where(opts.Filters).Clauses(opts.Clauses...)
		for _, q := range opts.Queries {
			conds := base.Statement.BuildCondition(q.Query, q.Args...)
			base = base.Clauses(conds...)
		}
	}

	var total int64
	if err := base.Model(&model.VerificationTemplateM{}).Count(&total).Error; err != nil {
		return 0, nil, err
	}

	listDB := base
	if opts != nil {
		listDB = listDB.Offset(opts.Offset).Limit(opts.Limit)
	}
	var list []*model.VerificationTemplateM
	if err := listDB.Order("id DESC").Find(&list).Error; err != nil {
		return 0, nil, err
	}
	return total, list, nil
}

func (v *verificationTemplateStore) GetActive(ctx context.Context) ([]*model.VerificationTemplateM, error) {
	var list []*model.VerificationTemplateM
	if err := v.s.DB(ctx).Where("active = ?", true).Order("id ASC").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

func (v *verificationTemplateStore) GetByID(ctx context.Context, id int64) (*model.VerificationTemplateM, error) {
	var out model.VerificationTemplateM
	if err := v.s.DB(ctx).Where("id = ?", id).First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

func (v *verificationTemplateStore) GetByLineageID(ctx context.Context, lineageID string) ([]*model.VerificationTemplateM, error) {
	var list []*model.VerificationTemplateM
	if err := v.s.DB(ctx).Where("lineage_id = ?", lineageID).Order("version DESC").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

func (v *verificationTemplateStore) Activate(ctx context.Context, id int64, operator string) error {
	return v.s.DB(ctx).Model(&model.VerificationTemplateM{}).Where("id = ?", id).Updates(map[string]any{
		"active":       true,
		"activated_at": v.s.DB(ctx).NowFunc(),
		"activated_by": &operator,
		"updated_by":   &operator,
	}).Error
}

func (v *verificationTemplateStore) Deactivate(ctx context.Context, id int64) error {
	return v.s.DB(ctx).Model(&model.VerificationTemplateM{}).Where("id = ?", id).Updates(map[string]any{
		"active": false,
	}).Error
}