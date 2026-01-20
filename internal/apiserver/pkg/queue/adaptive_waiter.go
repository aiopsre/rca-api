package queue

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	LongPollWakeupSourceRedis       = "redis"
	LongPollWakeupSourceDBWatermark = "db_watermark"
	LongPollWakeupSourceTimeout     = "timeout"

	LongPollFallbackRedisUnavailable = "redis_unavailable"
	LongPollFallbackAdaptiveL3Waiter = "adaptive_l3_waiters"
	LongPollFallbackAdaptiveL3DBErr  = "adaptive_l3_db_error_rate"
)

const (
	waitLevel1 = 1
	waitLevel2 = 2
	waitLevel3 = 3
)

const (
	defaultAdaptivePollInterval         = time.Second
	defaultAdaptiveWatermarkCacheTTL    = time.Second
	defaultAdaptiveMaxPollingWaiters    = int64(200)
	defaultAdaptiveDBErrorWindow        = 20
	defaultAdaptiveDBErrorRateThreshold = 0.5
	defaultAdaptiveDBErrMinSamples      = 6
)

// AdaptiveWaiterOptions controls long-poll progressive degrade behavior.
type AdaptiveWaiterOptions struct {
	PollInterval         time.Duration
	WatermarkCacheTTL    time.Duration
	MaxPollingWaiters    int64
	DBErrorWindow        int
	DBErrorRateThreshold float64
	DBErrorMinSamples    int
}

func (o *AdaptiveWaiterOptions) applyDefaults() {
	if o == nil {
		return
	}
	if o.PollInterval <= 0 {
		o.PollInterval = defaultAdaptivePollInterval
	}
	if o.WatermarkCacheTTL <= 0 {
		o.WatermarkCacheTTL = defaultAdaptiveWatermarkCacheTTL
	}
	if o.MaxPollingWaiters <= 0 {
		o.MaxPollingWaiters = defaultAdaptiveMaxPollingWaiters
	}
	if o.DBErrorWindow <= 0 {
		o.DBErrorWindow = defaultAdaptiveDBErrorWindow
	}
	if o.DBErrorRateThreshold <= 0 || o.DBErrorRateThreshold > 1 {
		o.DBErrorRateThreshold = defaultAdaptiveDBErrorRateThreshold
	}
	if o.DBErrorMinSamples <= 0 {
		o.DBErrorMinSamples = defaultAdaptiveDBErrMinSamples
	}
}

// DefaultAdaptiveWaiterOptions returns repository defaults.
func DefaultAdaptiveWaiterOptions() AdaptiveWaiterOptions {
	opts := AdaptiveWaiterOptions{}
	opts.applyDefaults()
	return opts
}

// ApplyAdaptiveWaiterEnvOverrides applies optional env-based overrides for runtime tuning.
func ApplyAdaptiveWaiterEnvOverrides(opts AdaptiveWaiterOptions) AdaptiveWaiterOptions {
	if v := parseEnvInt64("RCA_AI_JOB_LONGPOLL_MAX_POLLING_WAITERS"); v > 0 {
		opts.MaxPollingWaiters = v
	}
	if v := parseEnvInt("RCA_AI_JOB_LONGPOLL_DB_ERROR_WINDOW"); v > 0 {
		opts.DBErrorWindow = v
	}
	if v := parseEnvInt("RCA_AI_JOB_LONGPOLL_DB_ERROR_MIN_SAMPLES"); v > 0 {
		opts.DBErrorMinSamples = v
	}
	if v := parseEnvFloat("RCA_AI_JOB_LONGPOLL_DB_ERROR_RATE_THRESHOLD"); v > 0 && v <= 1 {
		opts.DBErrorRateThreshold = v
	}
	if v := parseEnvDurationMS("RCA_AI_JOB_LONGPOLL_POLL_INTERVAL_MS"); v > 0 {
		opts.PollInterval = v
	}
	if v := parseEnvDurationMS("RCA_AI_JOB_LONGPOLL_CACHE_TTL_MS"); v > 0 {
		opts.WatermarkCacheTTL = v
	}
	opts.applyDefaults()
	return opts
}

// WaitResult is one adaptive wait decision result.
type WaitResult struct {
	WakeupSource   string
	Level          int
	FallbackReason string
}

// AdaptiveWaiter handles long-poll wait with progressive degrade (L1/L2/L3).
type AdaptiveWaiter struct {
	notifier *Notifier
	wakeup   AIJobQueueWakeup

	queueSignalVersion func(context.Context) (int64, error)
	cache              *WatermarkCache
	opts               AdaptiveWaiterOptions

	activeWaiters atomic.Int64
	errorRate     *dbErrorRate
}

// NewAdaptiveWaiter creates one adaptive waiter shared by one apiserver instance.
func NewAdaptiveWaiter(
	notifier *Notifier,
	wakeup AIJobQueueWakeup,
	queueSignalVersion func(context.Context) (int64, error),
	opts AdaptiveWaiterOptions,
) *AdaptiveWaiter {

	opts.applyDefaults()
	return &AdaptiveWaiter{
		notifier:           notifier,
		wakeup:             wakeup,
		queueSignalVersion: queueSignalVersion,
		cache:              NewWatermarkCache(opts.WatermarkCacheTTL),
		opts:               opts,
		errorRate:          newDBErrorRate(opts.DBErrorWindow),
	}
}

// Wait blocks until wake/timeout with progressive degrade.
// Caller must always re-list MySQL after returning.
//
//nolint:gocognit,gocyclo,contextcheck // Long-poll loop keeps level transitions explicit and receives request context from handler.
func (w *AdaptiveWaiter) Wait(ctx context.Context, waitDur time.Duration) (WaitResult, error) {
	if waitDur <= 0 {
		return WaitResult{WakeupSource: LongPollWakeupSourceTimeout, Level: waitLevel3}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	currentWaiters := w.activeWaiters.Add(1)
	defer w.activeWaiters.Add(-1)

	notifierVersion := w.notifierVersion()
	waitCh, changed := w.notifierWaitChannel(notifierVersion)
	if changed {
		return WaitResult{
			WakeupSource:   w.selectNotifyWakeupSource(),
			Level:          w.selectNotifyLevel(),
			FallbackReason: w.selectFallbackReasonForNotify(),
		}, nil
	}

	level, fallbackReason, pollEnabled := w.selectLevel(currentWaiters)
	interval := w.pollIntervalForWaiters(currentWaiters)

	var (
		baseline int64
		haveBase bool
		ticker   *time.Ticker
	)
	if pollEnabled && w.queueSignalVersion != nil {
		version, err, fromCache := w.cache.Get(ctx, w.queueSignalVersion)
		if !fromCache {
			w.observeDBPoll(err)
		}
		if err != nil {
			level = waitLevel3
			pollEnabled = false
			fallbackReason = LongPollFallbackAdaptiveL3DBErr
		} else {
			baseline = version
			haveBase = true
			ticker = time.NewTicker(interval)
		}
	}
	if !pollEnabled {
		slog.Info("ai job long poll adaptive self protect",
			"level", waitLevel3,
			"fallback_reason", fallbackReason,
			"waiters", currentWaiters,
		)
	}

	timer := time.NewTimer(waitDur)
	defer timer.Stop()
	if ticker != nil {
		defer ticker.Stop()
	}

	for {
		select {
		case <-ctx.Done():
			return WaitResult{}, ctx.Err()

		case <-waitCh:
			return WaitResult{
				WakeupSource:   w.selectNotifyWakeupSource(),
				Level:          w.selectNotifyLevel(),
				FallbackReason: w.selectFallbackReasonForNotify(),
			}, nil

		case <-timer.C:
			return WaitResult{
				WakeupSource:   LongPollWakeupSourceTimeout,
				Level:          level,
				FallbackReason: fallbackReason,
			}, nil

		case <-tickerC(ticker):
			version, err, fromCache := w.cache.Get(ctx, w.queueSignalVersion)
			if !fromCache {
				w.observeDBPoll(err)
			}
			if err != nil {
				if w.shouldProtectByDBErrorRate() {
					level = waitLevel3
					fallbackReason = LongPollFallbackAdaptiveL3DBErr
					ticker.Stop()
					ticker = nil
				}
				continue
			}
			if !haveBase {
				baseline = version
				haveBase = true
				continue
			}
			if version != baseline {
				return WaitResult{
					WakeupSource:   LongPollWakeupSourceDBWatermark,
					Level:          waitLevel2,
					FallbackReason: LongPollFallbackRedisUnavailable,
				}, nil
			}
		}
	}
}

func (w *AdaptiveWaiter) notifierVersion() uint64 {
	if w == nil || w.notifier == nil {
		return 0
	}
	return w.notifier.Version()
}

func (w *AdaptiveWaiter) notifierWaitChannel(version uint64) (<-chan struct{}, bool) {
	if w == nil || w.notifier == nil {
		closed := make(chan struct{})
		close(closed)
		return closed, true
	}
	return w.notifier.WaitChannel(version)
}

func (w *AdaptiveWaiter) selectLevel(waiters int64) (int, string, bool) {
	if w != nil && w.MaxPollingWaiters() > 0 && waiters > w.MaxPollingWaiters() {
		return waitLevel3, LongPollFallbackAdaptiveL3Waiter, false
	}
	if w != nil && w.shouldProtectByDBErrorRate() {
		return waitLevel3, LongPollFallbackAdaptiveL3DBErr, false
	}
	if w != nil && w.wakeupReady() {
		return waitLevel1, "", false
	}
	return waitLevel2, LongPollFallbackRedisUnavailable, true
}

func (w *AdaptiveWaiter) selectNotifyWakeupSource() string {
	if w.wakeupReady() {
		return LongPollWakeupSourceRedis
	}
	return LongPollWakeupSourceDBWatermark
}

func (w *AdaptiveWaiter) selectNotifyLevel() int {
	if w.wakeupReady() {
		return waitLevel1
	}
	return waitLevel2
}

func (w *AdaptiveWaiter) selectFallbackReasonForNotify() string {
	if w.wakeupReady() {
		return ""
	}
	return LongPollFallbackRedisUnavailable
}

func (w *AdaptiveWaiter) wakeupReady() bool {
	if w == nil || w.wakeup == nil {
		return false
	}
	return w.wakeup.Ready()
}

func (w *AdaptiveWaiter) MaxPollingWaiters() int64 {
	if w == nil {
		return 0
	}
	return w.opts.MaxPollingWaiters
}

func (w *AdaptiveWaiter) pollIntervalForWaiters(waiters int64) time.Duration {
	interval := w.opts.PollInterval
	if interval <= 0 {
		interval = defaultAdaptivePollInterval
	}
	if w.opts.MaxPollingWaiters <= 0 {
		return interval
	}
	half := w.opts.MaxPollingWaiters / 2
	if half <= 0 {
		return interval
	}
	if waiters >= half {
		scaled := interval + interval/2
		if scaled > 2*time.Second {
			return 2 * time.Second
		}
		return scaled
	}
	return interval
}

func (w *AdaptiveWaiter) observeDBPoll(err error) {
	if w == nil || w.errorRate == nil {
		return
	}
	w.errorRate.Add(err != nil)
}

func (w *AdaptiveWaiter) shouldProtectByDBErrorRate() bool {
	if w == nil || w.errorRate == nil {
		return false
	}
	rate, samples := w.errorRate.Rate()
	return samples >= w.opts.DBErrorMinSamples && rate >= w.opts.DBErrorRateThreshold
}

func tickerC(t *time.Ticker) <-chan time.Time {
	if t == nil {
		return nil
	}
	return t.C
}

func parseEnvInt64(key string) int64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

func parseEnvInt(key string) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return v
}

func parseEnvFloat(key string) float64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0
	}
	return v
}

func parseEnvDurationMS(key string) time.Duration {
	v := parseEnvInt(key)
	if v <= 0 {
		return 0
	}
	return time.Duration(v) * time.Millisecond
}

type dbErrorRate struct {
	mu     sync.Mutex
	window []bool
	next   int
	filled int
	errCnt int
}

func newDBErrorRate(size int) *dbErrorRate {
	if size <= 0 {
		size = defaultAdaptiveDBErrorWindow
	}
	return &dbErrorRate{
		window: make([]bool, size),
	}
}

func (r *dbErrorRate) Add(err bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.filled < len(r.window) {
		r.window[r.next] = err
		if err {
			r.errCnt++
		}
		r.filled++
		r.next = (r.next + 1) % len(r.window)
		return
	}

	if r.window[r.next] {
		r.errCnt--
	}
	r.window[r.next] = err
	if err {
		r.errCnt++
	}
	r.next = (r.next + 1) % len(r.window)
}

func (r *dbErrorRate) Rate() (float64, int) {
	if r == nil {
		return 0, 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.filled == 0 {
		return 0, 0
	}
	return float64(r.errCnt) / float64(r.filled), r.filled
}
