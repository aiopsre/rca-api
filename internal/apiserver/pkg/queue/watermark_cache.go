package queue

import (
	"context"
	"sync"
	"time"
)

const defaultWatermarkCacheTTL = time.Second

// WatermarkCache provides one process-wide shared cache for queue signal version reads.
// It deduplicates concurrent refreshes to avoid N waiters issuing N database reads.
type WatermarkCache struct {
	ttl time.Duration

	mu        sync.Mutex
	value     int64
	hasValue  bool
	fetchedAt time.Time
	inFlight  chan struct{}
}

// NewWatermarkCache creates one queue watermark cache with ttl.
func NewWatermarkCache(ttl time.Duration) *WatermarkCache {
	if ttl <= 0 {
		ttl = defaultWatermarkCacheTTL
	}
	return &WatermarkCache{ttl: ttl}
}

// Get returns cached watermark when fresh, otherwise refreshes via reader.
// It returns fromCache=true when no underlying reader call is made.
//
//nolint:gocognit,contextcheck // Cache refresh arbitration is explicit; ctx is passed through from caller.
func (c *WatermarkCache) Get(
	ctx context.Context,
	reader func(context.Context) (int64, error),
) (int64, error, bool) {

	if reader == nil {
		return 0, nil, true
	}
	if c == nil {
		v, readErr := reader(ctx)
		return v, readErr, false
	}
	if ctx == nil {
		ctx = context.Background()
	}

	for {
		c.mu.Lock()
		if c.isFreshLocked(time.Now()) {
			v := c.value
			c.mu.Unlock()
			return v, nil, true
		}
		if c.inFlight != nil {
			wait := c.inFlight
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return 0, ctx.Err(), true
			case <-wait:
				continue
			}
		}

		wait := make(chan struct{})
		c.inFlight = wait
		c.mu.Unlock()

		v, readErr := reader(ctx)

		c.mu.Lock()
		if readErr == nil {
			c.value = v
			c.hasValue = true
			c.fetchedAt = time.Now()
		}
		close(wait)
		c.inFlight = nil
		c.mu.Unlock()

		return v, readErr, false
	}
}

func (c *WatermarkCache) isFreshLocked(now time.Time) bool {
	if c == nil || !c.hasValue {
		return false
	}
	if c.ttl <= 0 {
		return false
	}
	return now.Sub(c.fetchedAt) < c.ttl
}
