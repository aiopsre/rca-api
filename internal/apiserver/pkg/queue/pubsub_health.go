package queue

import (
	"sync"
	"time"
)

const (
	defaultPubSubHealthyTTL       = 45 * time.Second
	defaultPubSubFailuresToDown   = 3
	defaultPubSubSuccessesToReady = 2
)

// PubSubHealthOptions controls pubsub readiness anti-flap behavior.
type PubSubHealthOptions struct {
	HealthyTTL     time.Duration
	FailuresToDown int
	SuccessesToUp  int
}

func (o *PubSubHealthOptions) applyDefaults() {
	if o == nil {
		return
	}
	if o.HealthyTTL <= 0 {
		o.HealthyTTL = defaultPubSubHealthyTTL
	}
	if o.FailuresToDown <= 0 {
		o.FailuresToDown = defaultPubSubFailuresToDown
	}
	if o.SuccessesToUp <= 0 {
		o.SuccessesToUp = defaultPubSubSuccessesToReady
	}
}

// PubSubHealth tracks pubsub readiness with healthy window + hysteresis.
type PubSubHealth struct {
	opts PubSubHealthOptions

	mu              sync.Mutex
	up              bool
	lastOK          time.Time
	consecutiveFail int
	consecutiveOK   int
}

// NewPubSubHealth creates one pubsub health tracker.
func NewPubSubHealth(opts PubSubHealthOptions) *PubSubHealth {
	opts.applyDefaults()
	return &PubSubHealth{opts: opts}
}

// MarkSuccess records one successful subscribe/heartbeat event.
func (h *PubSubHealth) MarkSuccess(now time.Time) bool {
	if h == nil {
		return false
	}
	now = normalizeHealthNow(now)

	h.mu.Lock()
	defer h.mu.Unlock()

	h.lastOK = now
	h.consecutiveFail = 0
	if h.up {
		h.consecutiveOK = 0
		return false
	}
	h.consecutiveOK++
	if h.consecutiveOK >= h.opts.SuccessesToUp {
		h.up = true
		h.consecutiveOK = 0
		return true
	}
	return false
}

// MarkFailure records one subscribe/heartbeat failure event.
func (h *PubSubHealth) MarkFailure(_ time.Time) bool {
	if h == nil {
		return false
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	h.consecutiveOK = 0
	h.consecutiveFail++
	if !h.up {
		return false
	}
	if h.consecutiveFail >= h.opts.FailuresToDown {
		h.up = false
		h.consecutiveFail = 0
		return true
	}
	return false
}

// Ready reports whether pubsub can be considered healthy for wakeup usage.
func (h *PubSubHealth) Ready(now time.Time) bool {
	if h == nil {
		return false
	}
	now = normalizeHealthNow(now)

	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.up {
		return false
	}
	if h.lastOK.IsZero() {
		return false
	}
	if h.opts.HealthyTTL <= 0 {
		return true
	}
	return now.Sub(h.lastOK) <= h.opts.HealthyTTL
}

func normalizeHealthNow(now time.Time) time.Time {
	now = now.UTC()
	if now.IsZero() {
		return time.Now().UTC()
	}
	return now
}
