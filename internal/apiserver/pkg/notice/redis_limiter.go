package notice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/aiopsre/rca-api/internal/apiserver/pkg/metrics"
)

const (
	rateLimitModeRedis = "redis"
	rateLimitModeLocal = "local"

	rateLimitResultOK    = "ok"
	rateLimitResultDeny  = "deny"
	rateLimitResultError = "error"

	rateLimitReasonUnknown     = "unknown"
	rateLimitReasonLocal       = "local"
	rateLimitReasonGlobalQPS   = "global_qps"
	rateLimitReasonChannelQPS  = "channel_qps"
	rateLimitReasonChannelConc = "channel_conc"
	rateLimitReasonRedisError  = "redis_error"

	defaultNoticeRedisKeyPrefix     = "rca:notice"
	defaultNoticeRedisWindowTTL     = 2 * time.Second
	defaultNoticeRedisConcTTL       = 60 * time.Second
	defaultNoticeRetryAfterFallback = 200 * time.Millisecond
)

var (
	errUnexpectedRedisLimiterAcquireResult = errors.New("unexpected redis limiter acquire result")
	errUnexpectedRedisLimiterAllowedType   = errors.New("unexpected redis limiter allowed type")
	errUnexpectedRedisLimiterRetryType     = errors.New("unexpected redis limiter retry_after type")
)

type redisScriptRunner interface {
	Run(ctx context.Context, c redis.Scripter, keys []string, args ...any) *redis.Cmd
}

// NoticeRateLimiter controls send permits before webhook dispatch.
type NoticeRateLimiter interface {
	Acquire(ctx context.Context, channelID string) (RateLimitAcquireResult, error)
	Release(ctx context.Context, permit RateLimitPermit)
}

// RateLimitAcquireResult is one limiter acquire result.
type RateLimitAcquireResult struct {
	Allowed    bool
	RetryAfter time.Duration
	Reason     string
	Permit     RateLimitPermit
}

// RateLimitPermit carries release information for one acquired permit.
type RateLimitPermit struct {
	Mode     string
	Channel  string
	release  func()
	acquired bool
}

// RedisRateLimiterOptions defines redis global limiter behavior.
type RedisRateLimiterOptions struct {
	Enabled               bool
	FailOpen              bool
	KeyPrefix             string
	GlobalQPS             float64
	PerChannelQPS         float64
	PerChannelConcurrency int
	WindowTTL             time.Duration
	ConcurrencyTTL        time.Duration
}

func (o *RedisRateLimiterOptions) applyDefaults() {
	if o == nil {
		return
	}
	o.KeyPrefix = strings.TrimSpace(o.KeyPrefix)
	if o.KeyPrefix == "" {
		o.KeyPrefix = defaultNoticeRedisKeyPrefix
	}
	if o.WindowTTL <= 0 {
		o.WindowTTL = defaultNoticeRedisWindowTTL
	}
	if o.ConcurrencyTTL <= 0 {
		o.ConcurrencyTTL = defaultNoticeRedisConcTTL
	}
	if o.PerChannelConcurrency <= 0 {
		o.PerChannelConcurrency = 1
	}
	if o.GlobalQPS <= 0 {
		o.GlobalQPS = 1
	}
}

type redisRateLimiter struct {
	client *redis.Client
	opts   RedisRateLimiterOptions
	local  *localRateLimiter

	acquireScript redisScriptRunner
	releaseScript redisScriptRunner
}

// NewRedisRateLimiter creates a redis-backed limiter with fail-open local fallback.
func NewRedisRateLimiter(client *redis.Client, opts RedisRateLimiterOptions) *redisRateLimiter {
	opts.applyDefaults()
	return &redisRateLimiter{
		client: client,
		opts:   opts,
		local:  newLocalRateLimiter(opts.GlobalQPS, opts.PerChannelConcurrency),
		acquireScript: redis.NewScript(`
local globalKey = KEYS[1]
local channelQpsKey = KEYS[2]
local channelConcKey = KEYS[3]

local globalLimit = tonumber(ARGV[1]) or 0
local channelLimit = tonumber(ARGV[2]) or 0
local concLimit = tonumber(ARGV[3]) or 0
local windowTtlSec = tonumber(ARGV[4]) or 2
local concTtlMs = tonumber(ARGV[5]) or 60000
local retryAfterMs = tonumber(ARGV[6]) or 1000

local globalCount = redis.call("INCR", globalKey)
if globalCount == 1 then
	redis.call("EXPIRE", globalKey, windowTtlSec)
end
if globalLimit > 0 and globalCount > globalLimit then
	redis.call("DECR", globalKey)
	return {0, retryAfterMs, "global_qps"}
end

if channelLimit > 0 and channelQpsKey ~= "" then
	local channelCount = redis.call("INCR", channelQpsKey)
	if channelCount == 1 then
		redis.call("EXPIRE", channelQpsKey, windowTtlSec)
	end
	if channelCount > channelLimit then
		redis.call("DECR", channelQpsKey)
		redis.call("DECR", globalKey)
		return {0, retryAfterMs, "channel_qps"}
	end
end

local concCount = redis.call("INCR", channelConcKey)
redis.call("PEXPIRE", channelConcKey, concTtlMs)
if concLimit > 0 and concCount > concLimit then
	redis.call("DECR", channelConcKey)
	if channelLimit > 0 and channelQpsKey ~= "" then
		redis.call("DECR", channelQpsKey)
	end
	redis.call("DECR", globalKey)
	return {0, retryAfterMs, "channel_conc"}
end

return {1, 0, "ok"}
`),
		releaseScript: redis.NewScript(`
local channelConcKey = KEYS[1]
local current = redis.call("DECR", channelConcKey)
if current <= 0 then
	redis.call("DEL", channelConcKey)
end
return 1
`),
	}
}

func (l *redisRateLimiter) Acquire(ctx context.Context, channelID string) (RateLimitAcquireResult, error) {
	if l == nil {
		return RateLimitAcquireResult{}, nil
	}

	channel := normalizeNoticeChannelID(channelID)
	if !l.opts.Enabled || l.client == nil {
		return l.local.Acquire(ctx, channel)
	}

	result, err := l.acquireRedis(ctx, channel)
	if err != nil {
		recordNoticeRateLimitAcquire(rateLimitModeRedis, rateLimitResultError, rateLimitReasonRedisError)
		if !l.opts.FailOpen {
			return RateLimitAcquireResult{}, err
		}
		slog.ErrorContext(ctx, "notice redis limiter acquire failed, fallback to local limiter",
			"channel_id", channel,
			"error", err,
		)
		return l.local.Acquire(ctx, channel)
	}

	if !result.Allowed {
		recordNoticeRateLimitAcquire(rateLimitModeRedis, rateLimitResultDeny, result.Reason)
		return result, nil
	}

	recordNoticeRateLimitAcquire(rateLimitModeRedis, rateLimitResultOK, result.Reason)
	return result, nil
}

func (l *redisRateLimiter) Release(ctx context.Context, permit RateLimitPermit) {
	if l == nil || !permit.acquired {
		return
	}
	if strings.EqualFold(strings.TrimSpace(permit.Mode), rateLimitModeLocal) {
		l.local.Release(ctx, permit)
		return
	}
	if !strings.EqualFold(strings.TrimSpace(permit.Mode), rateLimitModeRedis) || l.client == nil {
		return
	}

	channel := normalizeNoticeChannelID(permit.Channel)
	key := l.channelConcurrencyKey(channel)
	if _, err := l.releaseScript.Run(ctx, l.client, []string{key}).Result(); err != nil {
		slog.ErrorContext(ctx, "notice redis limiter release failed",
			"channel_id", channel,
			"error", err,
		)
	}
}

func (l *redisRateLimiter) acquireRedis(ctx context.Context, channel string) (RateLimitAcquireResult, error) {
	now := time.Now().UTC()
	retryAfter := time.Second - time.Duration(now.Nanosecond())
	if retryAfter <= 0 {
		retryAfter = defaultNoticeRetryAfterFallback
	}

	channelQpsKey := ""
	channelLimit := roundQPSLimit(l.opts.PerChannelQPS)
	if channelLimit > 0 {
		channelQpsKey = l.channelQPSKey(channel, now.Unix())
	}

	args := []any{
		roundQPSLimit(l.opts.GlobalQPS),
		channelLimit,
		int64(l.opts.PerChannelConcurrency),
		ttlSeconds(l.opts.WindowTTL),
		l.opts.ConcurrencyTTL.Milliseconds(),
		retryAfter.Milliseconds(),
	}
	raw, err := l.acquireScript.Run(ctx, l.client, []string{
		l.globalQPSKey(now.Unix()),
		channelQpsKey,
		l.channelConcurrencyKey(channel),
	}, args...).Result()
	if err != nil {
		return RateLimitAcquireResult{}, err
	}

	allowed, retryAfterMS, reason, parseErr := parseAcquireLuaResult(raw)
	if parseErr != nil {
		return RateLimitAcquireResult{}, parseErr
	}
	if reason == "" {
		reason = rateLimitReasonUnknown
	}
	return RateLimitAcquireResult{
		Allowed:    allowed,
		RetryAfter: time.Duration(retryAfterMS) * time.Millisecond,
		Reason:     reason,
		Permit: RateLimitPermit{
			Mode:     rateLimitModeRedis,
			Channel:  channel,
			acquired: allowed,
		},
	}, nil
}

func (l *redisRateLimiter) globalQPSKey(epochSecond int64) string {
	return fmt.Sprintf("%s:rl:global:%d", l.opts.KeyPrefix, epochSecond)
}

func (l *redisRateLimiter) channelQPSKey(channel string, epochSecond int64) string {
	return fmt.Sprintf("%s:rl:ch:%s:%d", l.opts.KeyPrefix, channel, epochSecond)
}

func (l *redisRateLimiter) channelConcurrencyKey(channel string) string {
	return fmt.Sprintf("%s:conc:ch:%s", l.opts.KeyPrefix, channel)
}

func parseAcquireLuaResult(raw any) (bool, int64, string, error) {
	values, ok := raw.([]any)
	if !ok || len(values) < 3 {
		return false, 0, "", fmt.Errorf("%w: %T", errUnexpectedRedisLimiterAcquireResult, raw)
	}

	allowed, ok := toInt64(values[0])
	if !ok {
		return false, 0, "", fmt.Errorf("%w: %T", errUnexpectedRedisLimiterAllowedType, values[0])
	}
	retryAfterMS, ok := toInt64(values[1])
	if !ok {
		return false, 0, "", fmt.Errorf("%w: %T", errUnexpectedRedisLimiterRetryType, values[1])
	}
	reason := strings.TrimSpace(fmt.Sprint(values[2]))
	return allowed == 1, retryAfterMS, reason, nil
}

func toInt64(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case int:
		return int64(x), true
	case float64:
		return int64(x), true
	case string:
		if x == "" {
			return 0, false
		}
		var parsed int64
		if _, err := fmt.Sscan(x, &parsed); err != nil {
			return 0, false
		}

		return parsed, true

	default:
		return 0, false
	}
}

func ttlSeconds(d time.Duration) int64 {
	if d <= 0 {
		return int64(defaultNoticeRedisWindowTTL.Seconds())
	}
	return int64(math.Ceil(d.Seconds()))
}

func roundQPSLimit(qps float64) int64 {
	if qps <= 0 {
		return 0
	}
	return int64(math.Ceil(qps))
}

func normalizeNoticeChannelID(channelID string) string {
	id := strings.TrimSpace(channelID)
	if id == "" {
		return "__empty__"
	}
	id = strings.ReplaceAll(id, " ", "_")
	return id
}

func recordNoticeRateLimitAcquire(mode string, result string, reason string) {
	if metrics.M == nil {
		return
	}
	metrics.M.RecordNoticeRateLimitAcquire(mode, result, reason)
}

type localRateLimiter struct {
	perChannelConcurrency int
	globalLimiter         *tokenBucket

	mu         sync.Mutex
	channelSem map[string]chan struct{}
}

func newLocalRateLimiter(globalQPS float64, perChannelConcurrency int) *localRateLimiter {
	if perChannelConcurrency <= 0 {
		perChannelConcurrency = 1
	}
	return &localRateLimiter{
		perChannelConcurrency: perChannelConcurrency,
		globalLimiter:         newTokenBucket(globalQPS),
		channelSem:            make(map[string]chan struct{}),
	}
}

func (l *localRateLimiter) Acquire(ctx context.Context, channelID string) (RateLimitAcquireResult, error) {
	if l == nil {
		return RateLimitAcquireResult{Allowed: true}, nil
	}

	release, err := l.acquireChannelSlot(ctx, channelID)
	if err != nil {
		recordNoticeRateLimitAcquire(rateLimitModeLocal, rateLimitResultError, rateLimitReasonUnknown)
		return RateLimitAcquireResult{}, err
	}
	if err := l.waitGlobalToken(ctx); err != nil {
		release()
		recordNoticeRateLimitAcquire(rateLimitModeLocal, rateLimitResultError, rateLimitReasonUnknown)
		return RateLimitAcquireResult{}, err
	}

	recordNoticeRateLimitAcquire(rateLimitModeLocal, rateLimitResultOK, rateLimitReasonLocal)
	return RateLimitAcquireResult{
		Allowed: true,
		Reason:  rateLimitReasonLocal,
		Permit: RateLimitPermit{
			Mode:     rateLimitModeLocal,
			Channel:  normalizeNoticeChannelID(channelID),
			release:  release,
			acquired: true,
		},
	}, nil
}

func (l *localRateLimiter) Release(_ context.Context, permit RateLimitPermit) {
	if !permit.acquired || permit.release == nil {
		return
	}
	permit.release()
}

func (l *localRateLimiter) acquireChannelSlot(ctx context.Context, channelID string) (func(), error) {
	id := normalizeNoticeChannelID(channelID)
	l.mu.Lock()
	sem, ok := l.channelSem[id]
	if !ok {
		sem = make(chan struct{}, l.perChannelConcurrency)
		l.channelSem[id] = sem
	}
	l.mu.Unlock()

	select {
	case sem <- struct{}{}:
		return func() {
			select {
			case <-sem:

			default:
			}
		}, nil

	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (l *localRateLimiter) waitGlobalToken(ctx context.Context) error {
	if l == nil || l.globalLimiter == nil {
		return nil
	}
	return l.globalLimiter.Wait(ctx)
}
