package queue

import "context"

// AIJobQueueWakeup is a cross-instance wakeup signal bridge for AIJob long polling.
type AIJobQueueWakeup interface {
	// PublishAIJobQueueSignal notifies peers that queue state may have changed.
	PublishAIJobQueueSignal(ctx context.Context) error
	// Ready reports whether cross-instance wakeup channel is currently usable.
	Ready() bool
}

type noopWakeup struct{}

// NewNoopWakeup returns a wakeup bridge that is always not-ready and no-op on publish.
func NewNoopWakeup() *noopWakeup {
	return &noopWakeup{}
}

func (*noopWakeup) PublishAIJobQueueSignal(context.Context) error { return nil }

func (*noopWakeup) Ready() bool { return false }
