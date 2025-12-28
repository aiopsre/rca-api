//nolint:dupl
package store

import (
	"context"
	"encoding/json"
	"time"

	"gorm.io/gorm"

	"zk8s.com/rca-api/internal/apiserver/model"
	"zk8s.com/rca-api/pkg/store/where"
)

//nolint:interfacebloat // Store interface follows repo pattern (CRUD + list + expansion).
type NoticeDeliveryStore interface {
	Create(ctx context.Context, obj *model.NoticeDeliveryM) error
	Get(ctx context.Context, opts *where.Options) (*model.NoticeDeliveryM, error)
	GetClaimedPending(ctx context.Context, deliveryID string, workerID string) (*model.NoticeDeliveryM, error)
	List(ctx context.Context, opts *where.Options) (int64, []*model.NoticeDeliveryM, error)
	Replay(ctx context.Context, deliveryID string, now time.Time, opts ReplayDeliveryOptions) (*model.NoticeDeliveryM, error)
	Cancel(ctx context.Context, deliveryID string, now time.Time) (*model.NoticeDeliveryM, error)
	ClaimPending(ctx context.Context, workerID string, limit int, now time.Time, lockTimeout time.Duration) ([]*model.NoticeDeliveryM, error)
	MarkSucceeded(
		ctx context.Context,
		deliveryID string,
		workerID string,
		responseCode *int32,
		responseBody *string,
		latencyMs int64,
	) error
	MarkRetry(
		ctx context.Context,
		deliveryID string,
		workerID string,
		responseCode *int32,
		responseBody *string,
		errText *string,
		latencyMs int64,
		nextRetryAt time.Time,
	) error
	MarkFailed(
		ctx context.Context,
		deliveryID string,
		workerID string,
		responseCode *int32,
		responseBody *string,
		errText *string,
		latencyMs int64,
	) error

	NoticeDeliveryExpansion
}

//nolint:iface,modernize // Keep explicit empty interface as a placeholder expansion point.
type NoticeDeliveryExpansion interface{}

type noticeDeliveryStore struct {
	s *store
}

// ReplayDeliveryOptions controls replay mutation behavior.
type ReplayDeliveryOptions struct {
	Snapshot *model.NoticeDeliverySnapshot
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

func (n *noticeDeliveryStore) GetClaimedPending(ctx context.Context, deliveryID string, workerID string) (*model.NoticeDeliveryM, error) {
	var out model.NoticeDeliveryM
	if err := n.s.DB(ctx).
		Where("delivery_id = ? AND status = ? AND locked_by = ?", deliveryID, "pending", workerID).
		First(&out).Error; err != nil {
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

func (n *noticeDeliveryStore) Replay(ctx context.Context, deliveryID string, now time.Time, opts ReplayDeliveryOptions) (*model.NoticeDeliveryM, error) {
	return n.operateDelivery(ctx, deliveryID, now, canReplayDeliveryStatus, func(tx *gorm.DB, inDeliveryID string, inNow time.Time) error {
		return applyReplayUpdate(tx, inDeliveryID, inNow, opts.Snapshot)
	})
}

func (n *noticeDeliveryStore) Cancel(ctx context.Context, deliveryID string, now time.Time) (*model.NoticeDeliveryM, error) {
	return n.operateDelivery(ctx, deliveryID, now, canCancelDeliveryStatus, applyCancelUpdate)
}

//nolint:gocognit // Small transactional state switch is kept explicit for auditability.
func (n *noticeDeliveryStore) operateDelivery(
	ctx context.Context,
	deliveryID string,
	now time.Time,
	canApply func(status string) bool,
	applyUpdate func(tx *gorm.DB, deliveryID string, now time.Time) error,
) (*model.NoticeDeliveryM, error) {

	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()

	var out model.NoticeDeliveryM
	err := n.s.DB(ctx).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := findNoticeDeliveryForUpdate(tx, deliveryID)
		if err != nil {
			return err
		}

		if canApply(current.Status) {
			if err := applyUpdate(tx, deliveryID, now); err != nil {
				return err
			}
		}

		updated, err := findNoticeDeliveryForUpdate(tx, deliveryID)
		if err != nil {
			return err
		}
		out = *updated
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func canReplayDeliveryStatus(status string) bool {
	return status == "failed" || status == "pending"
}

func canCancelDeliveryStatus(status string) bool {
	return status == "pending" || status == "failed"
}

func (n *noticeDeliveryStore) ClaimPending(
	ctx context.Context,
	workerID string,
	limit int,
	now time.Time,
	lockTimeout time.Duration,
) ([]*model.NoticeDeliveryM, error) {

	if limit <= 0 {
		return nil, nil
	}

	now, staleBefore := normalizeClaimWindow(now, lockTimeout)
	claimedIDs, err := n.claimPendingIDs(ctx, workerID, limit, now, staleBefore)
	if err != nil {
		return nil, err
	}
	if len(claimedIDs) == 0 {
		return nil, nil
	}
	return n.getClaimedDeliveries(ctx, claimedIDs, workerID)
}

func normalizeClaimWindow(now time.Time, lockTimeout time.Duration) (time.Time, time.Time) {
	now = now.UTC()
	if lockTimeout <= 0 {
		lockTimeout = 60 * time.Second
	}
	return now, now.Add(-lockTimeout)
}

func (n *noticeDeliveryStore) claimPendingIDs(
	ctx context.Context,
	workerID string,
	limit int,
	now time.Time,
	staleBefore time.Time,
) ([]int64, error) {

	claimedIDs := make([]int64, 0, limit)
	err := n.s.DB(ctx).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		candidateIDs, err := listPendingCandidateIDs(tx, limit, now, staleBefore)
		if err != nil {
			return err
		}
		for _, id := range candidateIDs {
			ok, err := claimDeliveryID(tx, id, workerID, now, staleBefore)
			if err != nil {
				return err
			}
			if ok {
				claimedIDs = append(claimedIDs, id)
			}
		}
		return nil
	})
	return claimedIDs, err
}

func listPendingCandidateIDs(tx *gorm.DB, limit int, now time.Time, staleBefore time.Time) ([]int64, error) {
	var candidateIDs []int64
	err := tx.Model(&model.NoticeDeliveryM{}).
		Where("status = ? AND next_retry_at <= ? AND (locked_at IS NULL OR locked_at < ?)", "pending", now, staleBefore).
		Order("created_at ASC").
		Order("id ASC").
		Limit(limit).
		Pluck("id", &candidateIDs).Error
	if err != nil {
		return nil, err
	}
	return candidateIDs, nil
}

func claimDeliveryID(tx *gorm.DB, id int64, workerID string, now time.Time, staleBefore time.Time) (bool, error) {
	res := tx.Model(&model.NoticeDeliveryM{}).
		Where("id = ? AND status = ? AND (locked_at IS NULL OR locked_at < ?)", id, "pending", staleBefore).
		Updates(map[string]any{
			"locked_by": workerID,
			"locked_at": now,
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

func (n *noticeDeliveryStore) getClaimedDeliveries(
	ctx context.Context,
	claimedIDs []int64,
	workerID string,
) ([]*model.NoticeDeliveryM, error) {

	var out []*model.NoticeDeliveryM
	if err := n.s.DB(ctx).
		Where("id IN ? AND locked_by = ?", claimedIDs, workerID).
		Order("created_at ASC").
		Order("id ASC").
		Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

func findNoticeDeliveryForUpdate(tx *gorm.DB, deliveryID string) (*model.NoticeDeliveryM, error) {
	var out model.NoticeDeliveryM
	if err := tx.Where("delivery_id = ?", deliveryID).First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

func applyReplayUpdate(tx *gorm.DB, deliveryID string, now time.Time, snapshot *model.NoticeDeliverySnapshot) error {
	updates := map[string]any{
		"status":        "pending",
		"attempts":      int64(0),
		"next_retry_at": now,
		"locked_by":     nil,
		"locked_at":     nil,
		"response_code": nil,
		"response_body": nil,
		"latency_ms":    int64(0),
		"error":         nil,
	}
	if snapshot != nil {
		updates["snapshot_endpoint_url"] = snapshot.EndpointURL
		updates["snapshot_timeout_ms"] = snapshot.TimeoutMs
		updates["snapshot_headers_json"] = encodeSnapshotHeaders(snapshot.Headers)
		updates["snapshot_secret_fingerprint"] = snapshot.SecretFingerprint
		updates["snapshot_channel_version"] = snapshot.ChannelVersion
	}
	return tx.Model(&model.NoticeDeliveryM{}).Where("delivery_id = ?", deliveryID).Updates(updates).Error
}

func encodeSnapshotHeaders(headers map[string]string) *string {
	if len(headers) == 0 {
		return nil
	}
	raw, _ := json.Marshal(headers)
	out := string(raw)
	return &out
}

func applyCancelUpdate(tx *gorm.DB, deliveryID string, now time.Time) error {
	return tx.Model(&model.NoticeDeliveryM{}).
		Where("delivery_id = ?", deliveryID).
		Updates(map[string]any{
			"status":        "canceled",
			"next_retry_at": now,
			"locked_by":     nil,
			"locked_at":     nil,
		}).Error
}

func (n *noticeDeliveryStore) MarkSucceeded(
	ctx context.Context,
	deliveryID string,
	workerID string,
	responseCode *int32,
	responseBody *string,
	latencyMs int64,
) error {

	res := n.s.DB(ctx).Model(&model.NoticeDeliveryM{}).
		Where("delivery_id = ? AND locked_by = ?", deliveryID, workerID).
		Updates(map[string]any{
			"status":        "succeeded",
			"attempts":      gorm.Expr("attempts + 1"),
			"response_code": responseCode,
			"response_body": responseBody,
			"latency_ms":    latencyMs,
			"error":         nil,
			"locked_by":     nil,
			"locked_at":     nil,
			"next_retry_at": time.Now().UTC(),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (n *noticeDeliveryStore) MarkRetry(
	ctx context.Context,
	deliveryID string,
	workerID string,
	responseCode *int32,
	responseBody *string,
	errText *string,
	latencyMs int64,
	nextRetryAt time.Time,
) error {

	res := n.s.DB(ctx).Model(&model.NoticeDeliveryM{}).
		Where("delivery_id = ? AND locked_by = ?", deliveryID, workerID).
		Updates(map[string]any{
			"status":        "pending",
			"attempts":      gorm.Expr("attempts + 1"),
			"response_code": responseCode,
			"response_body": responseBody,
			"latency_ms":    latencyMs,
			"error":         errText,
			"locked_by":     nil,
			"locked_at":     nil,
			"next_retry_at": nextRetryAt.UTC(),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (n *noticeDeliveryStore) MarkFailed(
	ctx context.Context,
	deliveryID string,
	workerID string,
	responseCode *int32,
	responseBody *string,
	errText *string,
	latencyMs int64,
) error {

	res := n.s.DB(ctx).Model(&model.NoticeDeliveryM{}).
		Where("delivery_id = ? AND locked_by = ?", deliveryID, workerID).
		Updates(map[string]any{
			"status":        "failed",
			"attempts":      gorm.Expr("attempts + 1"),
			"response_code": responseCode,
			"response_body": responseBody,
			"latency_ms":    latencyMs,
			"error":         errText,
			"locked_by":     nil,
			"locked_at":     nil,
			"next_retry_at": time.Now().UTC(),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
