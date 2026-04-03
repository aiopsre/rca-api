package queue

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAdaptiveWaiter_Level3SelfProtectSkipsPolling(t *testing.T) {
	notifier := NewNotifier()
	wakeup := &fakeWakeupForAdaptiveWaiter{ready: false}

	gate := make(chan struct{})
	var calls atomic.Int64
	reader := func(ctx context.Context) (int64, error) {
		calls.Add(1)
		select {
		case <-gate:
			return 100, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}

	opts := DefaultAdaptiveWaiterOptions()
	opts.MaxPollingWaiters = 1
	waiter := NewAdaptiveWaiter(notifier, wakeup, reader, opts)

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_, _ = waiter.Wait(context.Background(), 250*time.Millisecond)
	}()

	time.Sleep(30 * time.Millisecond)

	result, err := waiter.Wait(context.Background(), 120*time.Millisecond)
	require.NoError(t, err)
	require.Equal(t, waitLevel3, result.Level)
	require.Equal(t, LongPollFallbackAdaptiveL3Waiter, result.FallbackReason)
	require.Equal(t, LongPollWakeupSourceTimeout, result.WakeupSource)
	require.Equal(t, int64(1), calls.Load(), "level3 waiter should skip polling calls")

	close(gate)
	<-firstDone
}

type fakeWakeupForAdaptiveWaiter struct {
	ready bool
}

func (f *fakeWakeupForAdaptiveWaiter) PublishAIJobQueueSignal(context.Context) error { return nil }
func (f *fakeWakeupForAdaptiveWaiter) Ready() bool                                   { return f.ready }
