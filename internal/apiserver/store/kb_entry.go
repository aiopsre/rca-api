package store

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

//nolint:interfacebloat // Store interfaces in this repository intentionally aggregate CRUD and extension methods.
type KBEntryStore interface {
	Create(ctx context.Context, obj *model.KBEntryM) error
	Update(ctx context.Context, obj *model.KBEntryM) error
	Delete(ctx context.Context, opts *where.Options) error
	Get(ctx context.Context, opts *where.Options) (*model.KBEntryM, error)
	List(ctx context.Context, opts *where.Options) (int64, []*model.KBEntryM, error)
	Upsert(ctx context.Context, obj *model.KBEntryM) (*model.KBEntryM, error)
	IncrementHit(ctx context.Context, kbID string, now time.Time) error

	KBEntryExpansion
}

//nolint:iface,modernize // Keep explicit empty interface as a placeholder expansion point.
type KBEntryExpansion interface{}

type kbEntryStore struct {
	s *store
}

func newKBEntryStore(s *store) *kbEntryStore { return &kbEntryStore{s: s} }

func (k *kbEntryStore) Create(ctx context.Context, obj *model.KBEntryM) error {
	return k.s.DB(ctx).Create(obj).Error
}

func (k *kbEntryStore) Update(ctx context.Context, obj *model.KBEntryM) error {
	return k.s.DB(ctx).Save(obj).Error
}

func (k *kbEntryStore) Delete(ctx context.Context, opts *where.Options) error {
	db := k.s.DB(ctx)
	if opts != nil {
		db = opts.Where(db)
	}
	return db.Delete(&model.KBEntryM{}).Error
}

func (k *kbEntryStore) Get(ctx context.Context, opts *where.Options) (*model.KBEntryM, error) {
	db := k.s.DB(ctx)
	if opts != nil {
		db = opts.Where(db)
	}
	var out model.KBEntryM
	if err := db.First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

//nolint:dupl // Keep list implementation explicit and aligned with other stores.
func (k *kbEntryStore) List(ctx context.Context, opts *where.Options) (int64, []*model.KBEntryM, error) {
	db := k.s.DB(ctx)
	base := db
	if opts != nil {
		base = base.Where(opts.Filters).Clauses(opts.Clauses...)
		for _, q := range opts.Queries {
			conds := base.Statement.BuildCondition(q.Query, q.Args...)
			base = base.Clauses(conds...)
		}
	}

	var total int64
	if err := base.Model(&model.KBEntryM{}).Count(&total).Error; err != nil {
		return 0, nil, err
	}

	listDB := base
	if opts != nil {
		listDB = listDB.Offset(opts.Offset).Limit(opts.Limit)
	}

	var list []*model.KBEntryM
	if err := listDB.Order("updated_at DESC").Find(&list).Error; err != nil {
		return 0, nil, err
	}
	return total, list, nil
}

func (k *kbEntryStore) Upsert(ctx context.Context, obj *model.KBEntryM) (*model.KBEntryM, error) {
	now := time.Now().UTC()
	db := k.s.DB(ctx)
	entry := *obj
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = now
	}
	entry.UpdatedAt = now

	if err := db.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "namespace"},
			{Name: "service"},
			{Name: "root_cause_type"},
			{Name: "patterns_hash"},
		},
		DoUpdates: clause.Assignments(map[string]any{
			"root_cause_summary":      entry.RootCauseSummary,
			"patterns_json":           entry.PatternsJSON,
			"evidence_signature_json": entry.EvidenceSignatureJSON,
			"confidence":              entry.Confidence,
			"updated_at":              now,
		}),
	}).Create(&entry).Error; err != nil {
		return nil, err
	}

	var out model.KBEntryM
	if err := db.Where(
		"namespace = ? AND service = ? AND root_cause_type = ? AND patterns_hash = ?",
		entry.Namespace, entry.Service, entry.RootCauseType, entry.PatternsHash,
	).First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

func (k *kbEntryStore) IncrementHit(ctx context.Context, kbID string, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return k.s.DB(ctx).
		Model(&model.KBEntryM{}).
		Where("kb_id = ?", kbID).
		Updates(map[string]any{
			"hit_count":   gorm.Expr("hit_count + 1"),
			"last_hit_at": now.UTC(),
			"updated_at":  now.UTC(),
		}).Error
}
