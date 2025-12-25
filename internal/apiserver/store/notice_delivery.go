//nolint:dupl
package store

import (
	"context"

	"zk8s.com/rca-api/internal/apiserver/model"
	"zk8s.com/rca-api/pkg/store/where"
)

//nolint:interfacebloat // Store interface follows repo pattern (CRUD + list + expansion).
type NoticeDeliveryStore interface {
	Create(ctx context.Context, obj *model.NoticeDeliveryM) error
	Get(ctx context.Context, opts *where.Options) (*model.NoticeDeliveryM, error)
	List(ctx context.Context, opts *where.Options) (int64, []*model.NoticeDeliveryM, error)

	NoticeDeliveryExpansion
}

//nolint:iface,modernize // Keep explicit empty interface as a placeholder expansion point.
type NoticeDeliveryExpansion interface{}

type noticeDeliveryStore struct {
	s *store
}

func newNoticeDeliveryStore(s *store) *noticeDeliveryStore { return &noticeDeliveryStore{s: s} }

func (n *noticeDeliveryStore) Create(ctx context.Context, obj *model.NoticeDeliveryM) error {
	return n.s.DB(ctx).Create(obj).Error
}

func (n *noticeDeliveryStore) Get(ctx context.Context, opts *where.Options) (*model.NoticeDeliveryM, error) {
	db := n.s.DB(ctx)
	if opts != nil {
		db = opts.Where(db)
	}
	var out model.NoticeDeliveryM
	if err := db.First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

func (n *noticeDeliveryStore) List(ctx context.Context, opts *where.Options) (int64, []*model.NoticeDeliveryM, error) {
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
	if err := base.Model(&model.NoticeDeliveryM{}).Count(&total).Error; err != nil {
		return 0, nil, err
	}

	listDB := base
	if opts != nil {
		listDB = listDB.Offset(opts.Offset).Limit(opts.Limit)
	}

	var list []*model.NoticeDeliveryM
	if err := listDB.Order("created_at DESC").Order("id DESC").Find(&list).Error; err != nil {
		return 0, nil, err
	}
	return total, list, nil
}
