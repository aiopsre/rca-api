package queue

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Notifier broadcasts queue updates to all waiters in-process.
type Notifier struct {
	version atomic.Uint64
	mu      sync.Mutex
	ch      chan struct{}
}

// NewNotifier creates a queue notifier for a single apiserver process.
func NewNotifier() *Notifier {
	n := &Notifier{
		ch: make(chan struct{}),
	}
	n.version.Store(1)
	return n
}

// Version returns the current broadcast version.
func (n *Notifier) Version() uint64 {
	return n.version.Load()
}

// Notify wakes all current waiters and advances to the next wait channel.
func (n *Notifier) Notify() {
	n.mu.Lock()
	n.version.Add(1)
	close(n.ch)
	n.ch = make(chan struct{})
	n.mu.Unlock()
}

// Wait blocks until a Notify happens after version, context is canceled, or timeout.
// It returns true when woken by notify; false on timeout or context cancellation.
func (n *Notifier) Wait(ctx context.Context, version uint64, timeout time.Duration) bool {
	if timeout <= 0 {
		return n.Version() != version
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if n.Version() != version {
		return true
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	n.mu.Lock()
	ch := n.ch
	current := n.version.Load()
	n.mu.Unlock()
	if current != version {
		return true
	}

	select {
	case <-ch:
		return true
	case <-ctx.Done():
		return false
	case <-timer.C:
		return false
	}
}
