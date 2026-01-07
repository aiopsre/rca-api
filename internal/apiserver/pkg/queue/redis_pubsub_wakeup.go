package queue

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/aiopsre/rca-api/internal/apiserver/pkg/metrics"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/redisx"
)

const (
	redisSubscribeBackoffMin = 200 * time.Millisecond
	redisSubscribeBackoffMax = 2 * time.Second
)

var errRedisSubscribeChannelClosed = errors.New("subscribe channel closed")
var errNilContext = errors.New("nil context")

// PubSubWakeup bridges redis pub/sub messages into in-process queue notifier wakeups.
type PubSubWakeup struct {
	client *redis.Client
	topic  string

	ready atomic.Bool
	once  sync.Once
}

var _ AIJobQueueWakeup = (*PubSubWakeup)(nil)

// NewPubSubWakeup creates a redis pub/sub wakeup bridge.
func NewPubSubWakeup(client *redis.Client, topic string) *PubSubWakeup {
	trimmedTopic := strings.TrimSpace(topic)
	if trimmedTopic == "" {
		trimmedTopic = redisx.DefaultAIJobQueueSignalTopic
	}
	return &PubSubWakeup{
		client: client,
		topic:  trimmedTopic,
	}
}

// PublishAIJobQueueSignal publishes a best-effort queue wakeup signal.
func (w *PubSubWakeup) PublishAIJobQueueSignal(ctx context.Context) error {
	if w == nil || w.client == nil {
		return nil
	}
	if ctx == nil {
		return errNilContext
	}

	err := w.client.Publish(ctx, w.topic, "").Err()
	result := "ok"
	if err != nil {
		result = "error"
		slog.Error("redis publish ai job queue signal failed",
			"topic", w.topic,
			"capability", "pubsub",
			"fallback", true,
			"err", err,
		)
	}
	if metrics.M != nil {
		metrics.M.RecordRedisPubSubPublish(w.topic, result)
	}
	return err
}

// StartSubscribe starts the background redis subscription loop.
// It is safe to call multiple times; only the first call starts the loop.
func (w *PubSubWakeup) StartSubscribe(ctx context.Context, onMessage func()) {
	if w == nil || w.client == nil {
		return
	}
	if ctx == nil {
		return
	}
	w.once.Do(func() {
		go w.subscribeLoop(ctx, onMessage)
	})
}

// Ready reports current redis subscribe readiness.
func (w *PubSubWakeup) Ready() bool {
	if w == nil {
		return false
	}
	return w.ready.Load()
}

//nolint:gocognit,gocyclo // Subscription loop keeps reconnect and readiness transitions explicit.
func (w *PubSubWakeup) subscribeLoop(ctx context.Context, onMessage func()) {
	defer w.setReady(false)
	defer func() {
		if w.client != nil {
			_ = w.client.Close()
		}
	}()

	backoff := redisSubscribeBackoffMin
	w.setReady(false)
	for ctx.Err() == nil {
		pubsub := w.client.Subscribe(ctx, w.topic)
		if _, err := pubsub.Receive(ctx); err != nil {
			w.setReady(false)
			_ = pubsub.Close()
			slog.Error("redis subscribe ai job queue signal failed",
				"topic", w.topic,
				"capability", "pubsub",
				"fallback", true,
				"err", err,
			)
			if !waitRetry(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		w.setReady(true)
		backoff = redisSubscribeBackoffMin

		channel := pubsub.Channel()
		closed := false
		for !closed && ctx.Err() == nil {
			select {
			case <-ctx.Done():
				_ = pubsub.Close()
				return

			case msg, ok := <-channel:
				if !ok {
					closed = true
					continue
				}
				if msg == nil {
					continue
				}
				if onMessage != nil {
					onMessage()
				}
			}
		}

		w.setReady(false)
		_ = pubsub.Close()
		if ctx.Err() == nil {
			slog.Error("redis subscribe ai job queue signal failed",
				"topic", w.topic,
				"capability", "pubsub",
				"fallback", true,
				"err", errRedisSubscribeChannelClosed,
			)
			if !waitRetry(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
		}
	}
}

func (w *PubSubWakeup) setReady(ready bool) {
	if w == nil {
		return
	}
	w.ready.Store(ready)
	if metrics.M != nil {
		metrics.M.SetRedisPubSubSubscribeState(w.topic, ready)
	}
}

func waitRetry(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextBackoff(current time.Duration) time.Duration {
	if current <= 0 {
		return redisSubscribeBackoffMin
	}
	next := current * 2
	if next > redisSubscribeBackoffMax {
		return redisSubscribeBackoffMax
	}
	return next
}
