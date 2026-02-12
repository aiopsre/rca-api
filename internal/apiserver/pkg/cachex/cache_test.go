package cachex

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
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
}
