package queue

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWatermarkCache_ReducesConcurrentDBReads(t *testing.T) {
	cache := NewWatermarkCache(300 * time.Millisecond)
	var calls atomic.Int64

	reader := func(context.Context) (int64, error) {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		return 42, nil
	}

	const workers = 50
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			v, err, _ := cache.Get(context.Background(), reader)
			require.NoError(t, err)
			require.Equal(t, int64(42), v)
		}()
	}
	wg.Wait()

	firstWaveCalls := calls.Load()
	require.Equal(t, int64(1), firstWaveCalls)

	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			v, err, fromCache := cache.Get(context.Background(), reader)
			require.NoError(t, err)
			require.Equal(t, int64(42), v)
			require.True(t, fromCache)
		}()
	}
	wg.Wait()

	require.Equal(t, firstWaveCalls, calls.Load())
}
