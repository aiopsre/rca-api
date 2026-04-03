package store

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
)

const defaultAIJobQueueSignalID int64 = 1

type AIJobQueueSignalStore interface {
	GetVersion(ctx context.Context) (int64, error)
	Bump(ctx context.Context, now time.Time) (int64, error)

	AIJobQueueSignalExpansion
}

//nolint:iface,modernize // Keep explicit empty interface as a placeholder expansion point.
type AIJobQueueSignalExpansion interface{}

type aiJobQueueSignalStore struct {
	s *store
}

func newAIJobQueueSignalStore(s *store) *aiJobQueueSignalStore { return &aiJobQueueSignalStore{s: s} }

func (a *aiJobQueueSignalStore) GetVersion(ctx context.Context) (int64, error) {
	now := time.Now().UTC()
	db := a.s.DB(ctx)
	if err := ensureQueueSignalRow(db, now); err != nil {
		return 0, err
	}

	var out model.AIJobQueueSignalM
	if err := db.Where("id = ?", defaultAIJobQueueSignalID).First(&out).Error; err != nil {
		return 0, err
	}
	return out.Version, nil
}

func (a *aiJobQueueSignalStore) Bump(ctx context.Context, now time.Time) (int64, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()

	db := a.s.DB(ctx)
	entry := &model.AIJobQueueSignalM{
		ID:        defaultAIJobQueueSignalID,
		Version:   2,
		UpdatedAt: now,
	}
	if err := db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"version":    gorm.Expr("version + 1"),
			"updated_at": now,
		}),
	}).Create(entry).Error; err != nil {
		return 0, err
	}

	var out model.AIJobQueueSignalM
	if err := db.Where("id = ?", defaultAIJobQueueSignalID).First(&out).Error; err != nil {
		return 0, err
	}
	return out.Version, nil
}

func ensureQueueSignalRow(db *gorm.DB, now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	row := &model.AIJobQueueSignalM{
		ID:        defaultAIJobQueueSignalID,
		Version:   1,
		UpdatedAt: now.UTC(),
	}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoNothing: true,
	}).Create(row).Error
}
