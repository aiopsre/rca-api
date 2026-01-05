package notice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestRedisRateLimiter_FallbackLocalOnRedisError(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
	defer func() { _ = client.Close() }()

	limiter := NewRedisRateLimiter(client, RedisRateLimiterOptions{
		Enabled:               true,
		FailOpen:              true,
		KeyPrefix:             "rca:test:r2:fallback",
		GlobalQPS:             20,
		PerChannelConcurrency: 2,
		WindowTTL:             2 * time.Second,
		ConcurrencyTTL:        60 * time.Second,
	})
	limiter.acquireScript = fakeRedisScriptRunner{err: errors.New("mock redis error")}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	result, err := limiter.Acquire(ctx, "notice-channel-fallback")
	require.NoError(t, err)
	require.True(t, result.Allowed)
	require.Equal(t, rateLimitModeLocal, result.Permit.Mode)

	limiter.Release(ctx, result.Permit)
}

func TestRedisRateLimiter_DenyReturnsRetryAfter(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
	defer func() { _ = client.Close() }()

	limiter := NewRedisRateLimiter(client, RedisRateLimiterOptions{
		Enabled:               true,
		FailOpen:              false,
		KeyPrefix:             "rca:test:r2:deny",
		GlobalQPS:             1,
		PerChannelQPS:         0,
		PerChannelConcurrency: 2,
		WindowTTL:             2 * time.Second,
		ConcurrencyTTL:        60 * time.Second,
	})
	limiter.acquireScript = fakeRedisScriptRunner{
		result: []any{int64(0), int64(250), rateLimitReasonGlobalQPS},
	}

	ctx := context.Background()
	second, err := limiter.Acquire(ctx, "notice-channel-deny")
	require.NoError(t, err)
	require.False(t, second.Allowed)
	require.Equal(t, rateLimitReasonGlobalQPS, second.Reason)
	require.Equal(t, 250*time.Millisecond, second.RetryAfter)
}

type fakeRedisScriptRunner struct {
	result any
	err    error
}

func (f fakeRedisScriptRunner) Run(ctx context.Context, _ redis.Scripter, _ []string, _ ...any) *redis.Cmd {
	cmd := redis.NewCmd(ctx)
	if f.err != nil {
		cmd.SetErr(f.err)
		return cmd
	}
	cmd.SetVal(f.result)
	return cmd
}
