package store

import (
	"context"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

type ToolsetProviderBindingStore interface {
	Create(ctx context.Context, obj *model.ToolsetProviderBinding) error
	Update(ctx context.Context, obj *model.ToolsetProviderBinding) error
	Delete(ctx context.Context, opts *where.Options) error
	Get(ctx context.Context, opts *where.Options) (*model.ToolsetProviderBinding, error)
	List(ctx context.Context, opts *where.Options) (int64, []*model.ToolsetProviderBinding, error)
	// ListByToolsetNames returns all enabled bindings for the given toolset names.
	ListByToolsetNames(ctx context.Context, toolsetNames []string) ([]*model.ToolsetProviderBinding, error)

	ToolsetProviderBindingExpansion
}

type ToolsetProviderBindingExpansion interface{}

type toolsetProviderBindingStore struct {
	s *store
}

func newToolsetProviderBindingStore(s *store) *toolsetProviderBindingStore {
	return &toolsetProviderBindingStore{s: s}
}

func (t *toolsetProviderBindingStore) Create(ctx context.Context, obj *model.ToolsetProviderBinding) error {
	return t.s.DB(ctx).Create(obj).Error
}

func (t *toolsetProviderBindingStore) Update(ctx context.Context, obj *model.ToolsetProviderBinding) error {
	return t.s.DB(ctx).Save(obj).Error
}

func (t *toolsetProviderBindingStore) Delete(ctx context.Context, opts *where.Options) error {
	db := t.s.DB(ctx)
	if opts != nil {
		db = opts.Where(db)
	}
	return db.Delete(&model.ToolsetProviderBinding{}).Error
}

func (t *toolsetProviderBindingStore) Get(ctx context.Context, opts *where.Options) (*model.ToolsetProviderBinding, error) {
	db := t.s.DB(ctx)
	if opts != nil {
		db = opts.Where(db)
	}
	var out model.ToolsetProviderBinding
	if err := db.First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

func (t *toolsetProviderBindingStore) List(ctx context.Context, opts *where.Options) (int64, []*model.ToolsetProviderBinding, error) {
	db := t.s.DB(ctx)

	base := db
	if opts != nil {
		base = base.Where(opts.Filters).Clauses(opts.Clauses...)
		for _, q := range opts.Queries {
			conds := base.Statement.BuildCondition(q.Query, q.Args...)
			base = base.Clauses(conds...)
		}
	}

	var total int64
	if err := base.Model(&model.ToolsetProviderBinding{}).Count(&total).Error; err != nil {
		return 0, nil, err
	}

	listDB := base
	if opts != nil {
		listDB = listDB.Offset(opts.Offset).Limit(opts.Limit)
	}
	var list []*model.ToolsetProviderBinding
	if err := listDB.Order("priority ASC, id ASC").Find(&list).Error; err != nil {
		return 0, nil, err
	}
	return total, list, nil
}

// ListByToolsetNames returns all enabled bindings for the given toolset names.
// Results are ordered by priority ASC, mcp_server_id ASC.
func (t *toolsetProviderBindingStore) ListByToolsetNames(ctx context.Context, toolsetNames []string) ([]*model.ToolsetProviderBinding, error) {
	if len(toolsetNames) == 0 {
		return nil, nil
	}
	var list []*model.ToolsetProviderBinding
	if err := t.s.DB(ctx).
		Where("toolset_name IN ?", toolsetNames).
		Where("enabled = ?", true).
		Order("priority ASC, mcp_server_id ASC").
		Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}