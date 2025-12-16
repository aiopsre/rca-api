package store

import (
	"context"

	"github.com/onexstack/onexstack/pkg/store/where"
	"zk8s.com/rca-api/internal/apiserver/model"
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
	if opts != nil {
		db = opts.Where(db)
	}

	var total int64
	if err := db.Model(&model.IncidentM{}).Count(&total).Error; err != nil {
		return 0, nil, err
	}

	var list []*model.IncidentM
	if err := db.Find(&list).Error; err != nil {
		return 0, nil, err
	}
	return total, list, nil
}
