package store

import (
	"context"

	"zk8s.com/rca-api/internal/apiserver/model"
	"zk8s.com/rca-api/pkg/store/where"
)

type AIJobStore interface {
	Create(ctx context.Context, obj *model.AIJobM) error
	Update(ctx context.Context, obj *model.AIJobM) error
	Delete(ctx context.Context, opts *where.Options) error
	Get(ctx context.Context, opts *where.Options) (*model.AIJobM, error)
	List(ctx context.Context, opts *where.Options) (int64, []*model.AIJobM, error)
	ListByStatus(ctx context.Context, status string, offset int, limit int, ascending bool) (int64, []*model.AIJobM, error)
	UpdateStatus(ctx context.Context, jobID string, fromStatuses []string, updates map[string]any) (int64, error)

	AIJobExpansion
}

type AIJobExpansion interface{}

type aiJobStore struct {
	s *store
}

func newAIJobStore(s *store) *aiJobStore { return &aiJobStore{s: s} }

func (a *aiJobStore) Create(ctx context.Context, obj *model.AIJobM) error {
	return a.s.DB(ctx).Create(obj).Error
}

func (a *aiJobStore) Update(ctx context.Context, obj *model.AIJobM) error {
	return a.s.DB(ctx).Save(obj).Error
}

func (a *aiJobStore) Delete(ctx context.Context, opts *where.Options) error {
	db := a.s.DB(ctx)
	if opts != nil {
		db = opts.Where(db)
	}
	return db.Delete(&model.AIJobM{}).Error
}

func (a *aiJobStore) Get(ctx context.Context, opts *where.Options) (*model.AIJobM, error) {
	db := a.s.DB(ctx)
	if opts != nil {
		db = opts.Where(db)
	}
	var out model.AIJobM
	if err := db.First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

func (a *aiJobStore) List(ctx context.Context, opts *where.Options) (int64, []*model.AIJobM, error) {
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
	if err := base.Model(&model.AIJobM{}).Count(&total).Error; err != nil {
		return 0, nil, err
	}

	listDB := base
	if opts != nil {
		listDB = listDB.Offset(opts.Offset).Limit(opts.Limit)
	}

	var list []*model.AIJobM
	if err := listDB.Order("created_at DESC").Find(&list).Error; err != nil {
		return 0, nil, err
	}

	return total, list, nil
}

func (a *aiJobStore) ListByStatus(ctx context.Context, status string, offset int, limit int, ascending bool) (int64, []*model.AIJobM, error) {
	base := a.s.DB(ctx).Model(&model.AIJobM{}).Where("status = ?", status)

	var total int64
	if err := base.Count(&total).Error; err != nil {
		return 0, nil, err
	}

	if offset < 0 {
		offset = 0
	}

	orderBy := "created_at DESC, id DESC"
	if ascending {
		orderBy = "created_at ASC, id ASC"
	}

	listDB := base.Order(orderBy).Offset(offset)
	if limit > 0 {
		listDB = listDB.Limit(limit)
	}

	var list []*model.AIJobM
	if err := listDB.Find(&list).Error; err != nil {
		return 0, nil, err
	}

	return total, list, nil
}

func (a *aiJobStore) UpdateStatus(ctx context.Context, jobID string, fromStatuses []string, updates map[string]any) (int64, error) {
	db := a.s.DB(ctx).Model(&model.AIJobM{}).Where("job_id = ?", jobID)
	if len(fromStatuses) > 0 {
		db = db.Where("status IN ?", fromStatuses)
	}
	res := db.Updates(updates)
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}
