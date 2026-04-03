package notice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/aiopsre/rca-api/internal/apiserver/pkg/metrics"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/redisx"
)

const (
	noticeDeliveryStreamFieldDeliveryID = "delivery_id"
	defaultStreamReadCount              = int64(16)
)

var (
	errNilNoticeStreamContext = errors.New("nil context")
	errNilNoticeStreamClient  = errors.New("nil notice stream redis client")
)

// NoticeStreamMessage is one redis stream message carrying a delivery id.
type NoticeStreamMessage struct {
	StreamID   string
	DeliveryID string
}

// NoticeDeliverySignalPublisher is used by producer-side paths (dispatch/replay).
type NoticeDeliverySignalPublisher interface {
	Enabled() bool
	PublishDeliveryID(ctx context.Context, deliveryID string) error
}

// NoticeDeliveryStreamConsumer is used by notice-worker stream consumption paths.
type NoticeDeliveryStreamConsumer interface {
	Enabled() bool
	ReadNew(ctx context.Context, consumer string, count int64, block time.Duration) ([]NoticeStreamMessage, error)
	ClaimPendingIdle(ctx context.Context, consumer string, count int64, minIdle time.Duration) ([]NoticeStreamMessage, error)
	Ack(ctx context.Context, streamIDs ...string) error
}

type noopNoticeDeliverySignalPublisher struct{}
type NoopNoticeDeliveryStreamConsumer struct{}

func (noopNoticeDeliverySignalPublisher) Enabled() bool { return false }
func (noopNoticeDeliverySignalPublisher) PublishDeliveryID(context.Context, string) error {
	return nil
}
func (NoopNoticeDeliveryStreamConsumer) Enabled() bool { return false }
func (NoopNoticeDeliveryStreamConsumer) ReadNew(context.Context, string, int64, time.Duration) ([]NoticeStreamMessage, error) {
	return nil, nil
}
func (NoopNoticeDeliveryStreamConsumer) ClaimPendingIdle(context.Context, string, int64, time.Duration) ([]NoticeStreamMessage, error) {
	return nil, nil
}
func (NoopNoticeDeliveryStreamConsumer) Ack(context.Context, ...string) error { return nil }

var (
	noticeDeliverySignalPublisherMu sync.RWMutex
	noticeDeliverySignalPublisher   NoticeDeliverySignalPublisher = noopNoticeDeliverySignalPublisher{}
)

// SetNoticeDeliverySignalPublisher sets process-level best-effort delivery signal publisher.
func SetNoticeDeliverySignalPublisher(p NoticeDeliverySignalPublisher) {
	noticeDeliverySignalPublisherMu.Lock()
	defer noticeDeliverySignalPublisherMu.Unlock()
	if p == nil {
		noticeDeliverySignalPublisher = noopNoticeDeliverySignalPublisher{}
		return
	}
	noticeDeliverySignalPublisher = p
}

// PublishNoticeDeliverySignalBestEffort publishes delivery id signal (best-effort).
func PublishNoticeDeliverySignalBestEffort(ctx context.Context, deliveryID string) {
	deliveryID = strings.TrimSpace(deliveryID)
	if deliveryID == "" {
		return
	}
	noticeDeliverySignalPublisherMu.RLock()
	publisher := noticeDeliverySignalPublisher
	noticeDeliverySignalPublisherMu.RUnlock()
	if publisher == nil || !publisher.Enabled() {
		return
	}
	if err := publisher.PublishDeliveryID(ctx, deliveryID); err != nil {
		slog.ErrorContext(ctx, "notice delivery stream publish failed",
			"delivery_id", deliveryID,
			"capability", "streams",
			"fallback", true,
			"error", err,
		)
	}
}

// RedisNoticeDeliveryStreamOptions defines redis stream settings for notice delivery dispatch.
type RedisNoticeDeliveryStreamOptions struct {
	Enabled bool
	Key     string
	Group   string
}

func (o *RedisNoticeDeliveryStreamOptions) applyDefaults() {
	if o == nil {
		return
	}
	o.Key = strings.TrimSpace(o.Key)
	if o.Key == "" {
		o.Key = redisx.DefaultNoticeDeliveryStreamKey
	}
	o.Group = strings.TrimSpace(o.Group)
	if o.Group == "" {
		o.Group = redisx.DefaultNoticeDeliveryStreamGroup
	}
}

// RedisNoticeDeliveryStream bridges notice delivery signals and consumer-group reads.
type RedisNoticeDeliveryStream struct {
	client *redis.Client
	opts   RedisNoticeDeliveryStreamOptions
}

var _ NoticeDeliverySignalPublisher = (*RedisNoticeDeliveryStream)(nil)
var _ NoticeDeliveryStreamConsumer = (*RedisNoticeDeliveryStream)(nil)

// NewRedisNoticeDeliveryStream creates redis stream helper for notice delivery dispatch.
func NewRedisNoticeDeliveryStream(client *redis.Client, opts RedisNoticeDeliveryStreamOptions) *RedisNoticeDeliveryStream {
	opts.applyDefaults()
	return &RedisNoticeDeliveryStream{
		client: client,
		opts:   opts,
	}
}

func (s *RedisNoticeDeliveryStream) Enabled() bool {
	return s != nil && s.opts.Enabled && s.client != nil
}

func (s *RedisNoticeDeliveryStream) PublishDeliveryID(ctx context.Context, deliveryID string) error {
	if !s.Enabled() {
		return nil
	}
	if ctx == nil {
		return errNilNoticeStreamContext
	}
	deliveryID = strings.TrimSpace(deliveryID)
	if deliveryID == "" {
		return nil
	}
	if _, err := s.client.XAdd(ctx, &redis.XAddArgs{
		Stream: s.opts.Key,
		Values: map[string]any{
			noticeDeliveryStreamFieldDeliveryID: deliveryID,
		},
	}).Result(); err != nil {
		recordNoticeStreamRead("", "error")
		return err
	}
	recordNoticeStreamRead("", "ok")
	recordNoticeStreamMessage("xadd")
	return nil
}

//nolint:gocognit,nestif // Redis read flow keeps NOGROUP retry explicit for operability.
func (s *RedisNoticeDeliveryStream) ReadNew(
	ctx context.Context,
	consumer string,
	count int64,
	block time.Duration,
) ([]NoticeStreamMessage, error) {

	if !s.Enabled() {
		return nil, nil
	}
	if ctx == nil {
		return nil, errNilNoticeStreamContext
	}
	consumer = normalizeStreamConsumerName(consumer)
	if err := s.ensureGroup(ctx); err != nil {
		recordNoticeStreamRead(s.opts.Key, "error")
		return nil, err
	}
	if count <= 0 {
		count = defaultStreamReadCount
	}
	streams, err := s.client.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    s.opts.Group,
		Consumer: consumer,
		Streams:  []string{s.opts.Key, ">"},
		Count:    count,
		Block:    block,
		NoAck:    false,
	}).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			recordNoticeStreamRead(s.opts.Key, "ok")
			return nil, nil
		}
		if isNoticeStreamNoGroupError(err) {
			if groupErr := s.ensureGroup(ctx); groupErr == nil {
				streams, err = s.client.XReadGroup(ctx, &redis.XReadGroupArgs{
					Group:    s.opts.Group,
					Consumer: consumer,
					Streams:  []string{s.opts.Key, ">"},
					Count:    count,
					Block:    block,
					NoAck:    false,
				}).Result()
			}
		}
		if err != nil {
			recordNoticeStreamRead(s.opts.Key, "error")
			return nil, err
		}
	}
	recordNoticeStreamRead(s.opts.Key, "ok")
	return decodeNoticeStreamMessages(streams), nil
}

//nolint:gocognit,gocyclo // Redis pending reclaim keeps each failure branch explicit for safety.
func (s *RedisNoticeDeliveryStream) ClaimPendingIdle(
	ctx context.Context,
	consumer string,
	count int64,
	minIdle time.Duration,
) ([]NoticeStreamMessage, error) {

	if !s.Enabled() {
		return nil, nil
	}
	if ctx == nil {
		return nil, errNilNoticeStreamContext
	}
	consumer = normalizeStreamConsumerName(consumer)
	if err := s.ensureGroup(ctx); err != nil {
		recordNoticeStreamRead(s.opts.Key, "error")
		return nil, err
	}
	if count <= 0 {
		count = defaultStreamReadCount
	}
	if minIdle <= 0 {
		minIdle = time.Duration(redisx.DefaultNoticeDeliveryReclaimIdleSeconds) * time.Second
	}
	pending, err := s.client.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: s.opts.Key,
		Group:  s.opts.Group,
		Start:  "-",
		End:    "+",
		Count:  count,
		Idle:   minIdle,
	}).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			recordNoticeStreamRead(s.opts.Key, "ok")
			return nil, nil
		}
		recordNoticeStreamRead(s.opts.Key, "error")
		return nil, err
	}
	if len(pending) == 0 {
		recordNoticeStreamRead(s.opts.Key, "ok")
		return nil, nil
	}
	streamIDs := make([]string, 0, len(pending))
	for _, item := range pending {
		id := strings.TrimSpace(item.ID)
		if id != "" {
			streamIDs = append(streamIDs, id)
		}
	}
	if len(streamIDs) == 0 {
		recordNoticeStreamRead(s.opts.Key, "ok")
		return nil, nil
	}
	msgs, err := s.client.XClaim(ctx, &redis.XClaimArgs{
		Stream:   s.opts.Key,
		Group:    s.opts.Group,
		Consumer: consumer,
		MinIdle:  minIdle,
		Messages: streamIDs,
	}).Result()
	if err != nil {
		recordNoticeStreamRead(s.opts.Key, "error")
		return nil, err
	}
	recordNoticeStreamRead(s.opts.Key, "ok")
	recordNoticeStreamMessage("reclaim")
	return decodeNoticeXMessages(msgs), nil
}

func (s *RedisNoticeDeliveryStream) Ack(ctx context.Context, streamIDs ...string) error {
	if !s.Enabled() || len(streamIDs) == 0 {
		return nil
	}
	if ctx == nil {
		return errNilNoticeStreamContext
	}
	normalized := make([]string, 0, len(streamIDs))
	for _, id := range streamIDs {
		trimmed := strings.TrimSpace(id)
		if trimmed != "" {
			normalized = append(normalized, trimmed)
		}
	}
	if len(normalized) == 0 {
		return nil
	}
	if err := s.client.XAck(ctx, s.opts.Key, s.opts.Group, normalized...).Err(); err != nil {
		return err
	}
	recordNoticeStreamMessage("ack")
	return nil
}

func (s *RedisNoticeDeliveryStream) Close() error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Close()
}

func (s *RedisNoticeDeliveryStream) ensureGroup(ctx context.Context) error {
	if s == nil || s.client == nil {
		return errNilNoticeStreamClient
	}
	if ctx == nil {
		return errNilNoticeStreamContext
	}
	err := s.client.XGroupCreateMkStream(ctx, s.opts.Key, s.opts.Group, "0").Err()
	if err == nil || strings.Contains(strings.ToUpper(err.Error()), "BUSYGROUP") {
		return nil
	}
	return err
}

func decodeNoticeStreamMessages(streams []redis.XStream) []NoticeStreamMessage {
	out := make([]NoticeStreamMessage, 0)
	for _, stream := range streams {
		out = append(out, decodeNoticeXMessages(stream.Messages)...)
	}
	return out
}

func decodeNoticeXMessages(messages []redis.XMessage) []NoticeStreamMessage {
	out := make([]NoticeStreamMessage, 0, len(messages))
	for _, msg := range messages {
		deliveryID := strings.TrimSpace(fmt.Sprint(msg.Values[noticeDeliveryStreamFieldDeliveryID]))
		if deliveryID == "" {
			continue
		}
		streamID := strings.TrimSpace(msg.ID)
		if streamID == "" {
			continue
		}
		out = append(out, NoticeStreamMessage{
			StreamID:   streamID,
			DeliveryID: deliveryID,
		})
	}
	return out
}

func isNoticeStreamNoGroupError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToUpper(err.Error()), "NOGROUP")
}

func normalizeStreamConsumerName(consumer string) string {
	consumer = strings.TrimSpace(consumer)
	if consumer == "" {
		return "notice-worker"
	}
	return consumer
}

func recordNoticeStreamRead(stream string, result string) {
	if metrics.M == nil {
		return
	}
	metrics.M.RecordNoticeStreamRead(result)
	if strings.TrimSpace(stream) != "" {
		metrics.M.RecordRedisStreamConsume(stream, result)
	}
}

func recordNoticeStreamMessage(action string) {
	if metrics.M == nil {
		return
	}
	metrics.M.RecordNoticeStreamMessage(action)
}
