//nolint:dupl
package store

import (
	"context"
	"strings"
	"time"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/pkg/store/where"
	"gorm.io/gorm"
)

//nolint:interfacebloat // Store interface follows repo pattern (CRUD + list + expansion + lease ops).
type AIJobStore interface {
	Create(ctx context.Context, obj *model.AIJobM) error
	Update(ctx context.Context, obj *model.AIJobM) error
	Delete(ctx context.Context, opts *where.Options) error
	Get(ctx context.Context, opts *where.Options) (*model.AIJobM, error)
	List(ctx context.Context, opts *where.Options) (int64, []*model.AIJobM, error)
	ListByStatus(ctx context.Context, status string, offset int, limit int, ascending bool) (int64, []*model.AIJobM, error)
	UpdateStatus(ctx context.Context, jobID string, fromStatuses []string, updates map[string]any) (int64, error)
	UpdateStatusWithLeaseOwner(ctx context.Context, jobID string, fromStatuses []string, leaseOwner *string, updates map[string]any) (int64, error)
	ClaimQueued(ctx context.Context, jobID string, leaseOwner string, now time.Time, leaseTTL time.Duration) (int64, error)
	RenewLease(ctx context.Context, jobID string, leaseOwner string, now time.Time, leaseTTL time.Duration) (int64, error)
	ReclaimExpiredRunning(ctx context.Context, now time.Time, limit int) (int64, error)

	AIJobExpansion
}

//nolint:iface,modernize // Keep explicit empty interface as a placeholder expansion point.
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
	return a.UpdateStatusWithLeaseOwner(ctx, jobID, fromStatuses, nil, updates)
}

func (a *aiJobStore) UpdateStatusWithLeaseOwner(
	ctx context.Context,
	jobID string,
	fromStatuses []string,
	leaseOwner *string,
	updates map[string]any,
) (int64, error) {

	db := a.s.DB(ctx).Model(&model.AIJobM{}).Where("job_id = ?", jobID)
	if len(fromStatuses) > 0 {
		db = db.Where("status IN ?", fromStatuses)
	}
	if leaseOwner != nil {
		owner := strings.TrimSpace(*leaseOwner)
		if owner == "" {
			db = db.Where("lease_owner IS NULL OR lease_owner = ''")
		} else {
			db = db.Where("lease_owner = ?", owner)
		}
	}
	res := db.Updates(updates)
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

func (a *aiJobStore) ClaimQueued(
	ctx context.Context,
	jobID string,
	leaseOwner string,
	now time.Time,
	leaseTTL time.Duration,
) (int64, error) {

	owner := strings.TrimSpace(leaseOwner)
	if owner == "" {
		return 0, nil
	}
	now = normalizeLeaseNow(now)
	leaseTTL = normalizeLeaseTTL(leaseTTL)
	expiresAt := now.Add(leaseTTL)

	res := a.s.DB(ctx).Model(&model.AIJobM{}).
		Where("job_id = ? AND status = ?", jobID, "queued").
		Updates(map[string]any{
			"status":           "running",
			"started_at":       now,
			"lease_owner":      owner,
			"lease_expires_at": expiresAt,
			"heartbeat_at":     now,
			"lease_version":    gorm.Expr("lease_version + 1"),
		})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

func (a *aiJobStore) RenewLease(
	ctx context.Context,
	jobID string,
	leaseOwner string,
	now time.Time,
	leaseTTL time.Duration,
) (int64, error) {

	owner := strings.TrimSpace(leaseOwner)
	if owner == "" {
		return 0, nil
	}
	now = normalizeLeaseNow(now)
	leaseTTL = normalizeLeaseTTL(leaseTTL)
	expiresAt := now.Add(leaseTTL)

	res := a.s.DB(ctx).Model(&model.AIJobM{}).
		Where("job_id = ? AND status = ? AND lease_owner = ?", jobID, "running", owner).
		Updates(map[string]any{
			"lease_expires_at": expiresAt,
			"heartbeat_at":     now,
			"lease_version":    gorm.Expr("lease_version + 1"),
		})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

//nolint:gocognit // Reclaim keeps explicit conditional updates for deterministic row ownership.
func (a *aiJobStore) ReclaimExpiredRunning(ctx context.Context, now time.Time, limit int) (int64, error) {
	now = normalizeLeaseNow(now)
	if limit <= 0 {
		limit = 100
	}

	var reclaimed int64
	err := a.s.DB(ctx).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var candidateIDs []int64
		if err := tx.Model(&model.AIJobM{}).
			Where("status = ? AND lease_expires_at IS NOT NULL AND lease_expires_at < ?", "running", now).
			Order("lease_expires_at ASC").
			Order("id ASC").
			Limit(limit).
			Pluck("id", &candidateIDs).Error; err != nil {
			return err
		}

		for _, id := range candidateIDs {
			res := tx.Model(&model.AIJobM{}).
				Where("id = ? AND status = ? AND lease_expires_at IS NOT NULL AND lease_expires_at < ?", id, "running", now).
				Updates(map[string]any{
					"status":           "queued",
					"started_at":       nil,
					"lease_owner":      nil,
					"lease_expires_at": nil,
					"heartbeat_at":     nil,
					"lease_version":    gorm.Expr("lease_version + 1"),
				})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 1 {
				reclaimed++
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return reclaimed, nil
}

func normalizeLeaseNow(now time.Time) time.Time {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return now.UTC()
}

func normalizeLeaseTTL(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return 30 * time.Second
	}
	return ttl
}
