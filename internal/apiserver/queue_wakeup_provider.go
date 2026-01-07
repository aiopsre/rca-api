package apiserver

import (
	"context"
	"log/slog"

	"github.com/aiopsre/rca-api/internal/apiserver/pkg/queue"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/redisx"
)

// ProvideJobQueueNotifier provides one per-process queue notifier shared by handler and pubsub bridge.
func ProvideJobQueueNotifier() *queue.Notifier {
	return queue.NewNotifier()
}

// ProvideAIJobQueueWakeup provides redis pub/sub wakeup bridge with fail-open fallback.
func ProvideAIJobQueueWakeup(
	ctx context.Context,
	cfg *Config,
	notifier *queue.Notifier,
) (queue.AIJobQueueWakeup, error) {

	opts := cfg.RedisOptions
	opts.ApplyDefaults()

	if !opts.PubSubEnabled() {
		return queue.NewNoopWakeup(), nil
	}

	client, err := redisx.NewClient(ctx, opts)
	if err != nil {
		if opts.FailOpen {
			slog.Error("redis client init failed, fallback to db watermark long poll",
				"addr", opts.Addr,
				"capability", "pubsub",
				"fallback", true,
				"err", err,
			)
			return queue.NewNoopWakeup(), nil
		}
		return nil, err
	}

	wakeup := queue.NewPubSubWakeup(client, opts.PubSub.TopicAIJobSignal)
	wakeup.StartSubscribe(ctx, notifier.Notify)
	return wakeup, nil
}
