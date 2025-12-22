package queue

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNotifierWait_WakeupOnNotify(t *testing.T) {
	n := NewNotifier()
	version := n.Version()

	started := time.Now()
	done := make(chan bool, 1)
	go func() {
		done <- n.Wait(context.Background(), version, time.Second)
	}()

	time.Sleep(100 * time.Millisecond)
	n.Notify()

	select {
	case woke := <-done:
		require.True(t, woke)
		require.Less(t, time.Since(started), 700*time.Millisecond)
	case <-time.After(time.Second):
		t.Fatal("waiter did not wake in time")
	}
}

func TestNotifierWait_Timeout(t *testing.T) {
	n := NewNotifier()
	version := n.Version()

	started := time.Now()
	woke := n.Wait(context.Background(), version, 120*time.Millisecond)

	require.False(t, woke)
	require.GreaterOrEqual(t, time.Since(started), 100*time.Millisecond)
}

func TestNotifierWait_FastPathWhenVersionChanged(t *testing.T) {
	n := NewNotifier()
	version := n.Version()

	n.Notify()
	started := time.Now()
	woke := n.Wait(context.Background(), version, 2*time.Second)

	require.True(t, woke)
	require.Less(t, time.Since(started), 50*time.Millisecond)
}
