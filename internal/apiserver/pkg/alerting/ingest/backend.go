package ingest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	backendMySQL = "mysql"
	backendRedis = "redis"
)

const defaultBackendOpTimeout = 300 * time.Millisecond

var errRedisClientNotInitialized = errors.New("redis client not initialized")

var burstIncrScript = redis.NewScript(`
local cnt = redis.call("INCR", KEYS[1])
if cnt == 1 then
  redis.call("PEXPIRE", KEYS[1], ARGV[1])
end
return cnt
`)

// CurrentAlertLookup returns current alert last_seen_at for one fingerprint.
// It should return (nil, nil) when current alert does not exist.
type CurrentAlertLookup func(ctx context.Context, fingerprint string) (*time.Time, error)

// Backend defines pluggable short-state policy backend.
type Backend interface {
	Name() string
	Dedup(ctx context.Context, fingerprint string, at time.Time, window time.Duration) (bool, error)
	Burst(ctx context.Context, fingerprint string, at time.Time, window time.Duration) (int64, error)
}

// MySQLBackend performs dedup checks against MySQL current alert state.
type MySQLBackend struct {
	lookup CurrentAlertLookup
}

// NewMySQLBackend creates a MySQL-based policy backend.
func NewMySQLBackend(lookup CurrentAlertLookup) *MySQLBackend {
	return &MySQLBackend{lookup: lookup}
}

// Name returns backend name label.
func (b *MySQLBackend) Name() string {
	return backendMySQL
}

// Dedup returns true when one current alert exists and last_seen_at is still in dedup window.
func (b *MySQLBackend) Dedup(ctx context.Context, fingerprint string, at time.Time, window time.Duration) (bool, error) {
	if b == nil || b.lookup == nil || window <= 0 {
		return false, nil
	}
	lastSeenAt, err := b.lookup(ctx, strings.TrimSpace(fingerprint))
	if err != nil || lastSeenAt == nil {
		return false, err
	}
	return at.UTC().Sub(lastSeenAt.UTC()) <= window, nil
}

// Burst is a no-op in MySQL backend for R4 and always reports zero count.
func (b *MySQLBackend) Burst(_ context.Context, _ string, _ time.Time, _ time.Duration) (int64, error) {
	return 0, nil
}

// RedisBackendOptions configures redis short-state backend.
type RedisBackendOptions struct {
	Addr      string
	Password  string
	DB        int
	KeyPrefix string
	Timeout   time.Duration
}

// RedisBackend uses Redis keys for dedup/burst counters.
type RedisBackend struct {
	opts      RedisBackendOptions
	client    *redis.Client
	closeOnce sync.Once
	closeErr  error
}

// NewRedisBackend creates a Redis-backed policy backend.
func NewRedisBackend(opts RedisBackendOptions) *RedisBackend {
	opts.Addr = strings.TrimSpace(opts.Addr)
	opts.KeyPrefix = strings.TrimSpace(opts.KeyPrefix)
	if opts.KeyPrefix == "" {
		opts.KeyPrefix = DefaultRedisKeyPrefix
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultBackendOpTimeout
	}
	if opts.DB < 0 {
		opts.DB = 0
	}
	client := redis.NewClient(&redis.Options{
		Addr:         opts.Addr,
		Password:     opts.Password,
		DB:           opts.DB,
		DialTimeout:  opts.Timeout,
		ReadTimeout:  opts.Timeout,
		WriteTimeout: opts.Timeout,
	})

	return &RedisBackend{
		opts:   opts,
		client: client,
	}
}

// Name returns backend name label.
func (b *RedisBackend) Name() string {
	return backendRedis
}

// Dedup uses SETNX+EX to detect duplicate events in a short window.
func (b *RedisBackend) Dedup(ctx context.Context, fingerprint string, _ time.Time, window time.Duration) (bool, error) {
	if b == nil || window <= 0 {
		return false, nil
	}
	client, err := b.redisClient()
	if err != nil {
		return false, err
	}

	opCtx, cancel := context.WithTimeout(ctx, b.opts.Timeout)
	defer cancel()

	key := fmt.Sprintf("%s:dedup:%s", b.opts.KeyPrefix, strings.TrimSpace(fingerprint))
	ok, err := client.SetNX(opCtx, key, "1", window).Result()
	if err != nil {
		return false, err
	}
	return !ok, nil
}

// Burst increments one window counter and returns the current count.
func (b *RedisBackend) Burst(ctx context.Context, fingerprint string, at time.Time, window time.Duration) (int64, error) {
	if b == nil || window <= 0 {
		return 0, nil
	}
	client, err := b.redisClient()
	if err != nil {
		return 0, err
	}

	windowMs := window.Milliseconds()
	if windowMs <= 0 {
		windowMs = 1000
	}
	bucket := at.UTC().UnixMilli() / windowMs
	key := fmt.Sprintf("%s:burst:%s:%d", b.opts.KeyPrefix, strings.TrimSpace(fingerprint), bucket)

	opCtx, cancel := context.WithTimeout(ctx, b.opts.Timeout)
	defer cancel()

	ttl := 2 * window
	if ttl <= 0 {
		ttl = 2 * time.Second
	}
	ttlMs := ttl.Milliseconds()
	if ttlMs <= 0 {
		ttlMs = 2000
	}

	count, err := burstIncrScript.Run(opCtx, client, []string{key}, ttlMs).Int64()
	if err != nil {
		return 0, err
	}

	return count, nil
}

func (b *RedisBackend) redisClient() (*redis.Client, error) {
	if b == nil || b.client == nil {
		return nil, errRedisClientNotInitialized
	}
	return b.client, nil
}

// Close closes the underlying redis client.
func (b *RedisBackend) Close() error {
	if b == nil || b.client == nil {
		return nil
	}
	b.closeOnce.Do(func() {
		b.closeErr = b.client.Close()
	})
	return b.closeErr
}
