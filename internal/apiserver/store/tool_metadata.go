package store

import (
	"context"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

type ToolMetadataStore interface {
	Create(ctx context.Context, obj *model.ToolMetadataM) error
	Update(ctx context.Context, obj *model.ToolMetadataM) error
	Delete(ctx context.Context, opts *where.Options) error
	Get(ctx context.Context, opts *where.Options) (*model.ToolMetadataM, error)
	List(ctx context.Context, opts *where.Options) (int64, []*model.ToolMetadataM, error)
	// BatchGetByToolNames retrieves multiple tool metadata records by tool names.
	BatchGetByToolNames(ctx context.Context, toolNames []string) ([]*model.ToolMetadataM, error)

	ToolMetadataExpansion
}

type ToolMetadataExpansion interface{}

type toolMetadataStore struct {
	s *store
}

func newToolMetadataStore(s *store) *toolMetadataStore { return &toolMetadataStore{s: s} }

func (t *toolMetadataStore) Create(ctx context.Context, obj *model.ToolMetadataM) error {
	return t.s.DB(ctx).Create(obj).Error
}

func (t *toolMetadataStore) Update(ctx context.Context, obj *model.ToolMetadataM) error {
	return t.s.DB(ctx).Save(obj).Error
}

func (t *toolMetadataStore) Delete(ctx context.Context, opts *where.Options) error {
	db := t.s.DB(ctx)
	if opts != nil {
		db = opts.Where(db)
	}
	return db.Delete(&model.ToolMetadataM{}).Error
}

func (t *toolMetadataStore) Get(ctx context.Context, opts *where.Options) (*model.ToolMetadataM, error) {
	db := t.s.DB(ctx)
	if opts != nil {
		db = opts.Where(db)
	}
	var out model.ToolMetadataM
	if err := db.First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

func (t *toolMetadataStore) List(ctx context.Context, opts *where.Options) (int64, []*model.ToolMetadataM, error) {
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
	if err := base.Model(&model.ToolMetadataM{}).Count(&total).Error; err != nil {
		return 0, nil, err
	}

	listDB := base
	if opts != nil {
		listDB = listDB.Offset(opts.Offset).Limit(opts.Limit)
	}
	var list []*model.ToolMetadataM
	if err := listDB.Order("id DESC").Find(&list).Error; err != nil {
		return 0, nil, err
	}
	return total, list, nil
}

// BatchGetByToolNames retrieves multiple tool metadata records by tool names.
// Returns all matching records, ignoring any that don't exist.
func (t *toolMetadataStore) BatchGetByToolNames(ctx context.Context, toolNames []string) ([]*model.ToolMetadataM, error) {
	if len(toolNames) == 0 {
		return nil, nil
	}

	var list []*model.ToolMetadataM
	if err := t.s.DB(ctx).Where("tool_name IN ?", toolNames).Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}