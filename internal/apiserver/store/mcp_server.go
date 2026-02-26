package store

import (
	"context"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

type McpServerStore interface {
	Create(ctx context.Context, obj *model.McpServerM) error
	Update(ctx context.Context, obj *model.McpServerM) error
	Delete(ctx context.Context, opts *where.Options) error
	Get(ctx context.Context, opts *where.Options) (*model.McpServerM, error)
	List(ctx context.Context, opts *where.Options) (int64, []*model.McpServerM, error)

	McpServerExpansion
}

type McpServerExpansion interface{}

type mcpServerStore struct {
	s *store
}

func newMcpServerStore(s *store) *mcpServerStore { return &mcpServerStore{s: s} }

func (m *mcpServerStore) Create(ctx context.Context, obj *model.McpServerM) error {
	return m.s.DB(ctx).Create(obj).Error
}

func (m *mcpServerStore) Update(ctx context.Context, obj *model.McpServerM) error {
	return m.s.DB(ctx).Save(obj).Error
}

func (m *mcpServerStore) Delete(ctx context.Context, opts *where.Options) error {
	db := m.s.DB(ctx)
	if opts != nil {
		db = opts.Where(db)
	}
	return db.Delete(&model.McpServerM{}).Error
}

func (m *mcpServerStore) Get(ctx context.Context, opts *where.Options) (*model.McpServerM, error) {
	db := m.s.DB(ctx)
	if opts != nil {
		db = opts.Where(db)
	}
	var out model.McpServerM
	if err := db.First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

func (m *mcpServerStore) List(ctx context.Context, opts *where.Options) (int64, []*model.McpServerM, error) {
	db := m.s.DB(ctx)

	base := db
	if opts != nil {
		base = base.Where(opts.Filters).Clauses(opts.Clauses...)
		for _, q := range opts.Queries {
			conds := base.Statement.BuildCondition(q.Query, q.Args...)
			base = base.Clauses(conds...)
		}
	}

	var total int64
	if err := base.Model(&model.McpServerM{}).Count(&total).Error; err != nil {
		return 0, nil, err
	}

	listDB := base
	if opts != nil {
		listDB = listDB.Offset(opts.Offset).Limit(opts.Limit)
	}
	var list []*model.McpServerM
	if err := listDB.Order("id DESC").Find(&list).Error; err != nil {
		return 0, nil, err
	}
	return total, list, nil
}

type McpServerConfigStore interface {
	Create(ctx context.Context, obj *model.McpServerConfigM) error
	Update(ctx context.Context, obj *model.McpServerConfigM) error
	Delete(ctx context.Context, opts *where.Options) error
	Get(ctx context.Context, opts *where.Options) (*model.McpServerConfigM, error)
	List(ctx context.Context, opts *where.Options) (int64, []*model.McpServerConfigM, error)

	McpServerConfigExpansion
}

type McpServerConfigExpansion interface{}

type mcpServerConfigStore struct {
	s *store
}

func newMcpServerConfigStore(s *store) *mcpServerConfigStore { return &mcpServerConfigStore{s: s} }

func (m *mcpServerConfigStore) Create(ctx context.Context, obj *model.McpServerConfigM) error {
	return m.s.DB(ctx).Create(obj).Error
}

func (m *mcpServerConfigStore) Update(ctx context.Context, obj *model.McpServerConfigM) error {
	return m.s.DB(ctx).Save(obj).Error
}

func (m *mcpServerConfigStore) Delete(ctx context.Context, opts *where.Options) error {
	db := m.s.DB(ctx)
	if opts != nil {
		db = opts.Where(db)
	}
	return db.Delete(&model.McpServerConfigM{}).Error
}

func (m *mcpServerConfigStore) Get(ctx context.Context, opts *where.Options) (*model.McpServerConfigM, error) {
	db := m.s.DB(ctx)
	if opts != nil {
		db = opts.Where(db)
	}
	var out model.McpServerConfigM
	if err := db.First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

func (m *mcpServerConfigStore) List(ctx context.Context, opts *where.Options) (int64, []*model.McpServerConfigM, error) {
	db := m.s.DB(ctx)

	base := db
	if opts != nil {
		base = base.Where(opts.Filters).Clauses(opts.Clauses...)
		for _, q := range opts.Queries {
			conds := base.Statement.BuildCondition(q.Query, q.Args...)
			base = base.Clauses(conds...)
		}
	}

	var total int64
	if err := base.Model(&model.McpServerConfigM{}).Count(&total).Error; err != nil {
		return 0, nil, err
	}

	listDB := base
	if opts != nil {
		listDB = listDB.Offset(opts.Offset).Limit(opts.Limit)
	}
	var list []*model.McpServerConfigM
	if err := listDB.Order("id DESC").Find(&list).Error; err != nil {
		return 0, nil, err
	}
	return total, list, nil
}