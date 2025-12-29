package notice

import (
	"context"
	crand "crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"strings"
	"time"

	"gorm.io/gorm"
	"zk8s.com/rca-api/internal/apiserver/model"
	"zk8s.com/rca-api/internal/apiserver/pkg/metrics"
	"zk8s.com/rca-api/internal/apiserver/store"
	"zk8s.com/rca-api/pkg/store/where"
)

// WorkerOptions contains notice-worker runtime options.
type WorkerOptions struct {
	WorkerID     string
	BatchSize    int
	PollInterval time.Duration
	LockTimeout  time.Duration
	BaseBackoff  time.Duration
	CapBackoff   time.Duration
	JitterMax    time.Duration
}

var (
	errNoticeChannelNotFound = errors.New("notice channel not found")
	errNoticeChannelDisabled = errors.New("notice channel disabled")
)

const (
	secretFingerprintMismatchKeyword    = "secret_fingerprint_mismatch"
	secretFingerprintMismatchReplayHint = "replay?useLatestChannel=1"
	fingerprintPrefixLength             = 12
)

type secretFingerprintMismatchError struct {
	snapshotFingerprint string
	channelFingerprint  string
}

type snapshotSecretResolution struct {
	secret *string
}

func (e *secretFingerprintMismatchError) Error() string {
	return fmt.Sprintf(
		"%s: snapshot/channel secret changed, %s",
		secretFingerprintMismatchKeyword,
		secretFingerprintMismatchReplayHint,
	)
}

// DefaultWorkerOptions returns default worker options.
func DefaultWorkerOptions() WorkerOptions {
	return WorkerOptions{
		WorkerID:     fmt.Sprintf("notice-worker-%d-%d", os.Getpid(), time.Now().UTC().UnixNano()),
		BatchSize:    16,
		PollInterval: 1 * time.Second,
		LockTimeout:  60 * time.Second,
		BaseBackoff:  1 * time.Second,
		CapBackoff:   60 * time.Second,
		JitterMax:    200 * time.Millisecond,
	}
}

// Worker consumes pending notice deliveries and sends webhooks with retry.
type Worker struct {
	store store.IStore
	opts  WorkerOptions
}

// NewWorker creates a notice worker.
func NewWorker(st store.IStore, opts WorkerOptions) *Worker {
	dft := DefaultWorkerOptions()
	if strings.TrimSpace(opts.WorkerID) == "" {
		opts.WorkerID = dft.WorkerID
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = dft.BatchSize
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = dft.PollInterval
	}
	if opts.LockTimeout <= 0 {
		opts.LockTimeout = dft.LockTimeout
	}
	if opts.BaseBackoff <= 0 {
		opts.BaseBackoff = dft.BaseBackoff
	}
	if opts.CapBackoff <= 0 {
		opts.CapBackoff = dft.CapBackoff
	}
	if opts.JitterMax < 0 {
		opts.JitterMax = dft.JitterMax
	}
	if opts.BaseBackoff > opts.CapBackoff {
		opts.BaseBackoff = opts.CapBackoff
	}

	return &Worker{
		store: st,
		opts:  opts,
	}
}

// Run starts the worker polling loop until context canceled.
func (w *Worker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.opts.PollInterval)
	defer ticker.Stop()

	for {
		if _, err := w.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.ErrorContext(ctx, "notice worker run once failed", "worker_id", w.opts.WorkerID, "error", err)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// RunOnce claims and processes one batch of pending deliveries.
func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	deliveries, err := w.store.NoticeDelivery().ClaimPending(ctx, w.opts.WorkerID, w.opts.BatchSize, time.Now().UTC(), w.opts.LockTimeout)
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, delivery := range deliveries {
		if delivery == nil {
			continue
		}
		processed++
		if err := w.processDelivery(ctx, delivery); err != nil {
			slog.ErrorContext(ctx, "notice worker process delivery failed",
				"worker_id", w.opts.WorkerID,
				"delivery_id", delivery.DeliveryID,
				"channel_id", delivery.ChannelID,
				"event_type", delivery.EventType,
				"incident_id", derefString(delivery.IncidentID),
				"job_id", derefString(delivery.JobID),
				"attempts", delivery.Attempts,
				"error", err,
			)
		}
	}
	return processed, nil
}

//nolint:gocognit,gocyclo // Worker attempt state transitions are explicit for auditability.
func (w *Worker) processDelivery(ctx context.Context, delivery *model.NoticeDeliveryM) error {
	latest, err := w.store.NoticeDelivery().GetClaimedPending(ctx, delivery.DeliveryID, w.opts.WorkerID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			slog.InfoContext(ctx, "notice delivery skipped due stale claim",
				"worker_id", w.opts.WorkerID,
				"delivery_id", delivery.DeliveryID,
				"channel_id", delivery.ChannelID,
				"event_type", delivery.EventType,
				"incident_id", derefString(delivery.IncidentID),
				"job_id", derefString(delivery.JobID),
			)
			return nil
		}
		return err
	}
	delivery = latest

	sendCfg, err := w.resolveSendConfig(ctx, delivery)
	if err != nil {
		w.recordSecretFingerprintMismatch(ctx, delivery, err)
		return w.failWithoutSend(ctx, delivery, err)
	}

	code, responseBody, latencyMs, sendErr := sendWebhook(
		ctx,
		sendCfg,
		delivery.EventType,
		delivery.IdempotencyKey,
		[]byte(delivery.RequestBody),
	)
	responseBodyPtr := strPtrOrNil(truncateString(responseBody, ResponseBodyMaxBytes))
	errText := formatSendError(sendErr, code)

	retryable := isRetryable(sendErr, code)
	maxAttempts := delivery.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultDeliveryMaxAttempts
	}
	attemptNow := delivery.Attempts + 1
	var outcome string

	switch {
	case sendErr == nil && code != nil && *code >= 200 && *code < 300:
		if err := w.store.NoticeDelivery().MarkSucceeded(ctx, delivery.DeliveryID, w.opts.WorkerID, code, responseBodyPtr, latencyMs); err != nil {
			return w.handleMarkClaimLost(ctx, delivery, err)
		}
		outcome = DeliveryStatusSucceeded

	case retryable && attemptNow < maxAttempts:
		nextRetryAt := time.Now().UTC().Add(computeRetryDelay(attemptNow, w.opts.BaseBackoff, w.opts.CapBackoff, w.opts.JitterMax))
		if err := w.store.NoticeDelivery().MarkRetry(ctx, delivery.DeliveryID, w.opts.WorkerID, code, responseBodyPtr, errText, latencyMs, nextRetryAt); err != nil {
			return w.handleMarkClaimLost(ctx, delivery, err)
		}
		outcome = "retry"

	default:
		if err := w.store.NoticeDelivery().MarkFailed(ctx, delivery.DeliveryID, w.opts.WorkerID, code, responseBodyPtr, errText, latencyMs); err != nil {
			return w.handleMarkClaimLost(ctx, delivery, err)
		}
		outcome = DeliveryStatusFailed
	}

	if metrics.M != nil {
		metrics.M.RecordNoticeDeliverySend(ctx, delivery.EventType, outcome, time.Duration(latencyMs)*time.Millisecond)
		if outcome == DeliveryStatusFailed {
			metrics.M.RecordNoticeDeliveryFailed(ctx, delivery.EventType)
		}
	}

	slog.InfoContext(ctx, "notice delivery processed",
		"worker_id", w.opts.WorkerID,
		"delivery_id", delivery.DeliveryID,
		"channel_id", delivery.ChannelID,
		"event_type", delivery.EventType,
		"incident_id", derefString(delivery.IncidentID),
		"job_id", derefString(delivery.JobID),
		"attempts", attemptNow,
		"http_code", derefInt32(code),
		"status", outcome,
		"error", derefString(errText),
	)
	return nil
}

func (w *Worker) resolveSendConfig(ctx context.Context, delivery *model.NoticeDeliveryM) (webhookSendConfig, error) {
	if hasDeliverySnapshot(delivery) {
		endpoint := strings.TrimSpace(derefString(delivery.SnapshotEndpointURL))
		if endpoint == "" {
			return webhookSendConfig{}, fmt.Errorf("%w: empty snapshot endpoint", errNoticeChannelNotFound)
		}
		resolution, err := w.resolveSnapshotSecret(ctx, delivery)
		if err != nil {
			return webhookSendConfig{}, err
		}
		return webhookSendConfig{
			EndpointURL: endpoint,
			TimeoutMs:   derefInt64(delivery.SnapshotTimeoutMs),
			HeadersJSON: delivery.SnapshotHeadersJSON,
			Secret:      resolution.secret,
		}, nil
	}

	channel, err := w.store.NoticeChannel().Get(ctx, where.T(ctx).F("channel_id", delivery.ChannelID))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return webhookSendConfig{}, fmt.Errorf("%w: %s", errNoticeChannelNotFound, delivery.ChannelID)
		}
		return webhookSendConfig{}, err
	}
	if !channel.Enabled {
		return webhookSendConfig{}, errNoticeChannelDisabled
	}
	return webhookSendConfig{
		EndpointURL: channel.EndpointURL,
		TimeoutMs:   channel.TimeoutMs,
		HeadersJSON: channel.HeadersJSON,
		Secret:      channel.Secret,
	}, nil
}

func (w *Worker) resolveSnapshotSecret(ctx context.Context, delivery *model.NoticeDeliveryM) (snapshotSecretResolution, error) {
	snapshotFingerprint := strings.TrimSpace(derefString(delivery.SnapshotSecretFingerprint))
	if snapshotFingerprint == "" {
		return snapshotSecretResolution{}, nil
	}

	channel, err := w.store.NoticeChannel().Get(ctx, where.T(ctx).F("channel_id", delivery.ChannelID))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return snapshotSecretResolution{}, fmt.Errorf("%w: %s", errNoticeChannelNotFound, delivery.ChannelID)
		}
		return snapshotSecretResolution{}, err
	}

	channelFingerprint := strings.TrimSpace(derefString(buildSecretFingerprint(channel.Secret)))
	if snapshotFingerprint != channelFingerprint {
		return snapshotSecretResolution{}, &secretFingerprintMismatchError{
			snapshotFingerprint: snapshotFingerprint,
			channelFingerprint:  channelFingerprint,
		}
	}
	return snapshotSecretResolution{secret: channel.Secret}, nil
}

func (w *Worker) failWithoutSend(ctx context.Context, delivery *model.NoticeDeliveryM, reason error) error {
	text := truncateString(reason.Error(), ErrorBodyMaxBytes)
	errPtr := &text
	if err := w.store.NoticeDelivery().MarkFailed(ctx, delivery.DeliveryID, w.opts.WorkerID, nil, nil, errPtr, 0); err != nil {
		return w.handleMarkClaimLost(ctx, delivery, err)
	}
	if metrics.M != nil {
		metrics.M.RecordNoticeDeliverySend(ctx, delivery.EventType, DeliveryStatusFailed, 0)
		metrics.M.RecordNoticeDeliveryFailed(ctx, delivery.EventType)
	}
	slog.WarnContext(ctx, "notice delivery failed before send",
		"worker_id", w.opts.WorkerID,
		"delivery_id", delivery.DeliveryID,
		"channel_id", delivery.ChannelID,
		"event_type", delivery.EventType,
		"incident_id", derefString(delivery.IncidentID),
		"job_id", derefString(delivery.JobID),
		"attempts", delivery.Attempts+1,
		"status", DeliveryStatusFailed,
		"error", text,
	)
	return nil
}

func (w *Worker) recordSecretFingerprintMismatch(ctx context.Context, delivery *model.NoticeDeliveryM, reason error) {
	var mismatchErr *secretFingerprintMismatchError
	if !errors.As(reason, &mismatchErr) {
		return
	}
	if metrics.M != nil {
		metrics.M.RecordNoticeDeliverySnapshotMismatch(ctx, delivery.EventType)
	}
	slog.WarnContext(ctx, "notice delivery snapshot secret fingerprint mismatch",
		"worker_id", w.opts.WorkerID,
		"delivery_id", delivery.DeliveryID,
		"channel_id", delivery.ChannelID,
		"event_type", delivery.EventType,
		"incident_id", derefString(delivery.IncidentID),
		"job_id", derefString(delivery.JobID),
		"mismatch", true,
		"snap_fp_prefix", fingerprintPrefix(mismatchErr.snapshotFingerprint),
		"channel_fp_prefix", fingerprintPrefix(mismatchErr.channelFingerprint),
		"replay_hint", secretFingerprintMismatchReplayHint,
		"error", truncateString(reason.Error(), ErrorBodyMaxBytes),
	)
}

func (w *Worker) handleMarkClaimLost(ctx context.Context, delivery *model.NoticeDeliveryM, err error) error {
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	slog.WarnContext(ctx, "notice delivery claim lost before status update",
		"worker_id", w.opts.WorkerID,
		"delivery_id", delivery.DeliveryID,
		"channel_id", delivery.ChannelID,
		"event_type", delivery.EventType,
		"incident_id", derefString(delivery.IncidentID),
		"job_id", derefString(delivery.JobID),
	)
	return nil
}

func isRetryable(sendErr error, code *int32) bool {
	if sendErr != nil {
		return true
	}
	if code == nil {
		return true
	}
	httpCode := *code
	if httpCode == 429 {
		return true
	}
	if httpCode >= 500 {
		return true
	}
	if httpCode >= 400 {
		return false
	}
	return false
}

func computeRetryDelay(attempt int64, base time.Duration, capDelay time.Duration, jitterMax time.Duration) time.Duration {
	if attempt <= 0 {
		attempt = 1
	}
	delay := base
	for i := int64(1); i < attempt; i++ {
		delay *= 2
		if delay >= capDelay {
			delay = capDelay
			break
		}
	}
	if delay > capDelay {
		delay = capDelay
	}
	if jitterMax <= 0 {
		return delay
	}
	jitter := time.Duration(randomNanos(jitterMax.Nanoseconds()))
	return delay + jitter
}

func formatSendError(sendErr error, code *int32) *string {
	if sendErr != nil {
		v := truncateString(sendErr.Error(), ErrorBodyMaxBytes)
		return &v
	}
	if code != nil && *code >= 400 {
		v := truncateString(fmt.Sprintf("http_status_%d", *code), ErrorBodyMaxBytes)
		return &v
	}
	return nil
}

func derefInt32(v *int32) int32 {
	if v == nil {
		return 0
	}
	return *v
}

func derefInt64(v *int64) int64 {
	if v == nil {
		return defaultTimeoutMs
	}
	return *v
}

func hasDeliverySnapshot(delivery *model.NoticeDeliveryM) bool {
	if delivery == nil {
		return false
	}
	return delivery.SnapshotEndpointURL != nil ||
		delivery.SnapshotTimeoutMs != nil ||
		delivery.SnapshotHeadersJSON != nil ||
		delivery.SnapshotSecretFingerprint != nil ||
		delivery.SnapshotChannelVersion != nil
}

func fingerprintPrefix(raw string) string {
	normalized := strings.TrimSpace(raw)
	if normalized == "" {
		return ""
	}
	if len(normalized) <= fingerprintPrefixLength {
		return normalized
	}
	return normalized[:fingerprintPrefixLength]
}

func randomNanos(maxNanos int64) int64 {
	if maxNanos <= 0 {
		return 0
	}
	n, err := crand.Int(crand.Reader, big.NewInt(maxNanos+1))
	if err != nil {
		return time.Now().UTC().UnixNano() % (maxNanos + 1)
	}
	return n.Int64()
}
