//nolint:dupl
package store

import (
	"context"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

//nolint:interfacebloat // Store interface follows repo pattern (CRUD + list + expansion).
type NoticeChannelStore interface {
	Create(ctx context.Context, obj *model.NoticeChannelM) error
	Update(ctx context.Context, obj *model.NoticeChannelM) error
	Get(ctx context.Context, opts *where.Options) (*model.NoticeChannelM, error)
	List(ctx context.Context, opts *where.Options) (int64, []*model.NoticeChannelM, error)
	ListEnabledWebhook(ctx context.Context) ([]*model.NoticeChannelM, error)

	NoticeChannelExpansion
}

//nolint:iface,modernize // Keep explicit empty interface as a placeholder expansion point.
type NoticeChannelExpansion interface{}

type noticeChannelStore struct {
	s *store
}

func newNoticeChannelStore(s *store) *noticeChannelStore { return &noticeChannelStore{s: s} }

func (n *noticeChannelStore) Create(ctx context.Context, obj *model.NoticeChannelM) error {
	return n.s.DB(ctx).Create(obj).Error
}

func (n *noticeChannelStore) Update(ctx context.Context, obj *model.NoticeChannelM) error {
	return n.s.DB(ctx).Save(obj).Error
}

func (n *noticeChannelStore) Get(ctx context.Context, opts *where.Options) (*model.NoticeChannelM, error) {
	db := n.s.DB(ctx)
	if opts != nil {
		db = opts.Where(db)
	}
	var out model.NoticeChannelM
	if err := db.First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

func (n *noticeChannelStore) List(ctx context.Context, opts *where.Options) (int64, []*model.NoticeChannelM, error) {
	db := n.s.DB(ctx)

	base := db
	if opts != nil {
		base = base.Where(opts.Filters).Clauses(opts.Clauses...)
		for _, q := range opts.Queries {
			conds := base.Statement.BuildCondition(q.Query, q.Args...)
			base = base.Clauses(conds...)
		}
	}

	var total int64
	if err := base.Model(&model.NoticeChannelM{}).Count(&total).Error; err != nil {
		return 0, nil, err
	}

	listDB := base
	if opts != nil {
		listDB = listDB.Offset(opts.Offset).Limit(opts.Limit)
	}

	var list []*model.NoticeChannelM
	if err := listDB.Order("created_at DESC").Order("id DESC").Find(&list).Error; err != nil {
		return 0, nil, err
	}
	return total, list, nil
}

func (n *noticeChannelStore) ListEnabledWebhook(ctx context.Context) ([]*model.NoticeChannelM, error) {
	var out []*model.NoticeChannelM
	if err := n.s.DB(ctx).
		Where("enabled = ? AND type = ?", true, "webhook").
		Order("id ASC").
		Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}
