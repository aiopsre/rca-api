package cachex

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestCacheJSON_GetSetDeleteAndPrefixInvalidation(t *testing.T) {
	ctx := context.Background()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ConfigureRedisClient(client)
	defer func() { _ = Close() }()

	key := BuildInboxKey("operator:test", "f123")
	payload := map[string]any{
		"total_count": 2,
		"items":       []string{"s1", "s2"},
	}
	require.True(t, SetJSON(ctx, key, payload, 30*time.Second))

	var out map[string]any
	require.True(t, GetJSON(ctx, key, &out))
	require.EqualValues(t, 2, out["total_count"])

	require.True(t, Delete(ctx, key))
	require.False(t, GetJSON(ctx, key, &out))

	require.True(t, SetJSON(ctx, BuildWorkbenchKey("session-a"), payload, 30*time.Second))
	require.True(t, SetJSON(ctx, BuildWorkbenchKey("session-b"), payload, 30*time.Second))
	require.GreaterOrEqual(t, DeleteByPrefix(ctx, "workbench:", 100), int64(2))
	require.False(t, GetJSON(ctx, BuildWorkbenchKey("session-a"), &out))
}

func TestCacheInvalidateHelpers(t *testing.T) {
	ctx := context.Background()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ConfigureRedisClient(client)
	defer func() { _ = Close() }()

	require.True(t, SetJSON(ctx, BuildWorkbenchKey("session-x"), map[string]any{"ok": true}, TTLWorkbench))
	require.True(t, SetJSON(ctx, BuildHistoryKey("session-x", 0, 10, false), map[string]any{"ok": true}, TTLHistory))
	require.True(t, SetJSON(ctx, BuildSessionStateKey("session-x"), map[string]any{"ok": true}, TTLSession))
	require.True(t, SetJSON(ctx, BuildInboxKey("op-a", "f1"), map[string]any{"ok": true}, TTLInbox))
	require.True(t, SetJSON(ctx, BuildDashboardKey("team-a"), map[string]any{"ok": true}, TTLDashboard))
	require.True(t, SetJSON(ctx, BuildTraceKey("job-a"), map[string]any{"ok": true}, TTLTrace))
	require.True(t, SetJSON(ctx, BuildCompareKey("job-a", "job-b"), map[string]any{"ok": true}, TTLCompare))
	require.True(t, SetJSON(ctx, BuildGlobalAssignmentHistoryKey("all"), map[string]any{"ok": true}, TTLHistory))

	InvalidateSessionReadModels(ctx, "session-x")
	InvalidateOperatorReadModels(ctx)
	InvalidateTraceReadModels(ctx, "job-a")

	var out map[string]any
	require.False(t, GetJSON(ctx, BuildWorkbenchKey("session-x"), &out))
	require.False(t, GetJSON(ctx, BuildHistoryKey("session-x", 0, 10, false), &out))
	require.False(t, GetJSON(ctx, BuildSessionStateKey("session-x"), &out))
	require.False(t, GetJSON(ctx, BuildInboxKey("op-a", "f1"), &out))
	require.False(t, GetJSON(ctx, BuildDashboardKey("team-a"), &out))
	require.False(t, GetJSON(ctx, BuildTraceKey("job-a"), &out))
	require.False(t, GetJSON(ctx, BuildCompareKey("job-a", "job-b"), &out))
	require.False(t, GetJSON(ctx, BuildGlobalAssignmentHistoryKey("all"), &out))
}

func TestCacheJSON_TTLExpiration(t *testing.T) {
	ctx := context.Background()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ConfigureRedisClient(client)
	defer func() { _ = Close() }()

	key := BuildWorkbenchKey("session-expire")
	require.True(t, SetJSON(ctx, key, map[string]any{"ok": true}, 2*time.Second))

	var out map[string]any
	require.True(t, GetJSON(ctx, key, &out))

	mr.FastForward(3 * time.Second)
	require.False(t, GetJSON(ctx, key, &out))
}

func TestCacheMetricsHitMissAndInvalidation(t *testing.T) {
	ctx := context.Background()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ConfigureRedisClient(client)
	defer func() { _ = Close() }()

	key := BuildInboxKey("operator:test", "f")
	hitBefore := testutil.ToFloat64(cacheOperationTotal.WithLabelValues("get", "inbox", "hit"))
	missBefore := testutil.ToFloat64(cacheOperationTotal.WithLabelValues("get", "inbox", "miss"))
	invalidateBefore := testutil.ToFloat64(cacheInvalidationTotal.WithLabelValues("operator", "ok"))

	var out map[string]any
	require.False(t, GetJSON(ctx, key, &out))
	require.True(t, SetJSON(ctx, key, map[string]any{"ok": true}, TTLInbox))
	require.True(t, GetJSON(ctx, key, &out))

	InvalidateOperatorReadModels(ctx)

	hitAfter := testutil.ToFloat64(cacheOperationTotal.WithLabelValues("get", "inbox", "hit"))
	missAfter := testutil.ToFloat64(cacheOperationTotal.WithLabelValues("get", "inbox", "miss"))
	invalidateAfter := testutil.ToFloat64(cacheInvalidationTotal.WithLabelValues("operator", "ok"))
	require.GreaterOrEqual(t, hitAfter-hitBefore, float64(1))
	require.GreaterOrEqual(t, missAfter-missBefore, float64(1))
	require.GreaterOrEqual(t, invalidateAfter-invalidateBefore, float64(1))
}

func TestDeleteByPrefix_RemovesLargeKeySet(t *testing.T) {
	ctx := context.Background()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ConfigureRedisClient(client)
	defer func() { _ = Close() }()

	for i := 0; i < 650; i++ {
		key := BuildInboxKey("operator:test", fmt.Sprintf("f-%03d", i))
		require.True(t, SetJSON(ctx, key, map[string]any{"idx": i}, TTLInbox))
	}

	deleted := DeleteByPrefix(ctx, "inbox:operator_test:", 1000)
	require.GreaterOrEqual(t, deleted, int64(600))
	require.Equal(t, 0, len(mr.Keys()))
}

func TestDeleteByPrefix_ConcurrentInvalidationIsIdempotent(t *testing.T) {
	ctx := context.Background()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ConfigureRedisClient(client)
	defer func() { _ = Close() }()

	for i := 0; i < 300; i++ {
		key := BuildDashboardKey(fmt.Sprintf("team-%03d", i))
		require.True(t, SetJSON(ctx, key, map[string]any{"idx": i}, TTLDashboard))
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = DeleteByPrefix(ctx, "dashboard:", 2000)
		}()
	}
	wg.Wait()
	require.Equal(t, 0, len(mr.Keys()))
}

func TestDeleteByPrefix_ScanFallbackWhenLuaDisabled(t *testing.T) {
	ctx := context.Background()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ConfigureRedisClient(client)
	defer func() { _ = Close() }()

	atomic.StoreUint32(&disableDeleteByPrefixLua, 1)
	defer atomic.StoreUint32(&disableDeleteByPrefixLua, 0)

	for i := 0; i < 320; i++ {
		key := BuildHistoryKey(fmt.Sprintf("session-%03d", i), 0, 10, false)
		require.True(t, SetJSON(ctx, key, map[string]any{"idx": i}, TTLHistory))
	}

	deleted := DeleteByPrefix(ctx, "history:", 1000)
	require.GreaterOrEqual(t, deleted, int64(300))
	require.Equal(t, 0, len(mr.Keys()))
}

func TestAdaptiveTTL_ExtendsAndShortensWithinBounds(t *testing.T) {
	ctx := context.Background()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ConfigureRedisClient(client)
	defer func() { _ = Close() }()
	resetTTLAdaptiveStateForTest()

	module := "inbox"
	key := BuildInboxKey("operator:test", "adaptive")
	for i := 0; i < 240; i++ {
		recordTTLAccess(module)
	}
	require.True(t, SetJSON(ctx, key, map[string]any{"mode": "hot_read"}, TTLInbox))
	hotReadTTL := mr.TTL(key)
	require.GreaterOrEqual(t, hotReadTTL, 58*time.Second)
	require.LessOrEqual(t, hotReadTTL, 60*time.Second)
	hotGauge := testutil.ToFloat64(cacheTTLEffectiveSeconds.WithLabelValues(module))
	require.GreaterOrEqual(t, hotGauge, float64(58))

	for i := 0; i < 40; i++ {
		recordTTLInvalidation(module, 1)
	}
	require.True(t, SetJSON(ctx, key, map[string]any{"mode": "hot_invalidation"}, TTLInbox))
	hotInvalidationTTL := mr.TTL(key)
	require.GreaterOrEqual(t, hotInvalidationTTL, 29*time.Second)
	require.LessOrEqual(t, hotInvalidationTTL, 31*time.Second)
	shortGauge := testutil.ToFloat64(cacheTTLEffectiveSeconds.WithLabelValues(module))
	require.GreaterOrEqual(t, shortGauge, float64(29))
	require.LessOrEqual(t, shortGauge, float64(31))
}

func TestAdaptiveTTL_TraceModuleBoundedRange(t *testing.T) {
	ctx := context.Background()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ConfigureRedisClient(client)
	defer func() { _ = Close() }()
	resetTTLAdaptiveStateForTest()

	module := "trace"
	key := BuildTraceKey("job-ttl-adaptive")
	for i := 0; i < 260; i++ {
		recordTTLAccess(module)
	}
	require.True(t, SetJSON(ctx, key, map[string]any{"job_id": "job-ttl-adaptive"}, TTLTrace))
	ttl := mr.TTL(key)
	require.GreaterOrEqual(t, ttl, 4*time.Minute+58*time.Second)
	require.LessOrEqual(t, ttl, 5*time.Minute)
}

func TestParseRedisInfoPairsAndKeyspace(t *testing.T) {
	info := `# Stats
keyspace_hits:12
keyspace_misses:3
# Keyspace
db0:keys=9,expires=2,avg_ttl=1234`

	parsed := parseRedisInfoPairs(info)
	require.Equal(t, "12", parsed["keyspace_hits"])
	require.Equal(t, "3", parsed["keyspace_misses"])
	keys, ok := parseRedisKeyspaceKeys(parsed["db0"])
	require.True(t, ok)
	require.Equal(t, float64(9), keys)
}

func resetTTLAdaptiveStateForTest() {
	ttlStateMu.Lock()
	defer ttlStateMu.Unlock()
	ttlStateByModule = map[string]*moduleTTLStat{}
}
