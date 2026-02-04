package store

import (
	"context"
	"strings"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

// SessionContextStore defines persistence operations for session context.
//
//nolint:interfacebloat // Keep CRUD + lookup helpers in a single store entrypoint.
type SessionContextStore interface {
	Create(ctx context.Context, obj *model.SessionContextM) error
	Update(ctx context.Context, obj *model.SessionContextM) error
	Delete(ctx context.Context, opts *where.Options) error
	Get(ctx context.Context, opts *where.Options) (*model.SessionContextM, error)
	GetByTypeAndBusinessKey(ctx context.Context, sessionType string, businessKey string) (*model.SessionContextM, error)
	GetByIncidentID(ctx context.Context, incidentID string) (*model.SessionContextM, error)
	List(ctx context.Context, opts *where.Options) (int64, []*model.SessionContextM, error)

	SessionContextExpansion
}

//nolint:modernize // Keep explicit empty interface as placeholder expansion point.
type SessionContextExpansion interface{}

type sessionContextStore struct {
	s *store
}

func newSessionContextStore(s *store) *sessionContextStore { return &sessionContextStore{s: s} }

func (sctx *sessionContextStore) Create(ctx context.Context, obj *model.SessionContextM) error {
	return sctx.s.DB(ctx).Create(obj).Error
}

func (sctx *sessionContextStore) Update(ctx context.Context, obj *model.SessionContextM) error {
	return sctx.s.DB(ctx).Save(obj).Error
}

func (sctx *sessionContextStore) Delete(ctx context.Context, opts *where.Options) error {
	db := sctx.s.DB(ctx)
	if opts != nil {
		db = opts.Where(db)
	}
	return db.Delete(&model.SessionContextM{}).Error
}

func (sctx *sessionContextStore) Get(ctx context.Context, opts *where.Options) (*model.SessionContextM, error) {
	db := sctx.s.DB(ctx)
	if opts != nil {
		db = opts.Where(db)
	}
	var out model.SessionContextM
	if err := db.First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

func (sctx *sessionContextStore) GetByTypeAndBusinessKey(
	ctx context.Context,
	sessionType string,
	businessKey string,
) (*model.SessionContextM, error) {
	return sctx.Get(ctx, where.T(ctx).F(
		"session_type", strings.ToLower(strings.TrimSpace(sessionType)),
		"business_key", strings.TrimSpace(businessKey),
	))
}

func (sctx *sessionContextStore) GetByIncidentID(ctx context.Context, incidentID string) (*model.SessionContextM, error) {
	return sctx.Get(ctx, where.T(ctx).F("incident_id", strings.TrimSpace(incidentID)))
}

func (sctx *sessionContextStore) List(ctx context.Context, opts *where.Options) (int64, []*model.SessionContextM, error) {
	db := sctx.s.DB(ctx)

	base := db
	if opts != nil {
		base = base.Where(opts.Filters).Clauses(opts.Clauses...)
		for _, q := range opts.Queries {
			conds := base.Statement.BuildCondition(q.Query, q.Args...)
			base = base.Clauses(conds...)
		}
	}

	var total int64
	if err := base.Model(&model.SessionContextM{}).Count(&total).Error; err != nil {
		return 0, nil, err
	}

	listDB := base
	if opts != nil {
		listDB = listDB.Offset(opts.Offset).Limit(opts.Limit)
	}
	var list []*model.SessionContextM
	if err := listDB.Order("created_at DESC").Find(&list).Error; err != nil {
		return 0, nil, err
	}
	return total, list, nil
}
