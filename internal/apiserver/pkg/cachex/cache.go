package cachex

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/redis/go-redis/v9"
)

const (
	defaultCacheOpTimeout = 1500 * time.Millisecond
	defaultScanCount      = int64(200)
	defaultStatsInterval  = 30 * time.Second
	luaDeleteScanCount    = int64(500)
	ttlAdaptiveWindow     = 60 * time.Second

	// Read-side cache TTLs.
	TTLInbox     = 45 * time.Second
	TTLWorkbench = 45 * time.Second
	TTLDashboard = 1 * time.Minute
	TTLTrace     = 2 * time.Minute
	TTLCompare   = 2 * time.Minute
	TTLHistory   = 45 * time.Second
	TTLSession   = 45 * time.Second
)

const globalAssignmentHistoryPrefix = "history:global_assignment:"

type moduleTTLPolicy struct {
	Base     time.Duration
	Min      time.Duration
	Max      time.Duration
	Dynamic  bool
	FixedTTL bool
}

type moduleTTLStat struct {
	WindowStart      time.Time
	AccessCount      int64
	InvalidationHits int64
}

var (
	clientMu sync.RWMutex
	client   *redis.Client

	statsCollectorMu     sync.Mutex
	statsCollectorCancel context.CancelFunc

	cacheOperationTotal = promauto.With(prometheus.DefaultRegisterer).NewCounterVec(prometheus.CounterOpts{
		Name: "rca_cache_operation_total",
		Help: "Total redis cache operations by op/module/result.",
	}, []string{"op", "module", "result"})

	cacheOperationLatencyMS = promauto.With(prometheus.DefaultRegisterer).NewHistogramVec(prometheus.HistogramOpts{
		Name:    "rca_cache_operation_latency_ms",
		Help:    "Redis cache operation latency in milliseconds by op/module.",
		Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 20, 50, 100, 250, 500, 1000},
	}, []string{"op", "module"})

	cacheInvalidationTotal = promauto.With(prometheus.DefaultRegisterer).NewCounterVec(prometheus.CounterOpts{
		Name: "rca_cache_invalidation_total",
		Help: "Total cache invalidation operations by scope/result.",
	}, []string{"scope", "result"})

	cacheInvalidatedKeysTotal = promauto.With(prometheus.DefaultRegisterer).NewCounterVec(prometheus.CounterOpts{
		Name: "rca_cache_invalidated_keys_total",
		Help: "Total number of redis cache keys invalidated by module.",
	}, []string{"module"})

	cacheConfiguredTTLSeconds = promauto.With(prometheus.DefaultRegisterer).NewGaugeVec(prometheus.GaugeOpts{
		Name: "rca_cache_ttl_config_seconds",
		Help: "Configured cache ttl seconds by module.",
	}, []string{"module"})

	cacheRedisCollectorTotal = promauto.With(prometheus.DefaultRegisterer).NewCounterVec(prometheus.CounterOpts{
		Name: "rca_cache_redis_collector_total",
		Help: "Total redis runtime stats collection attempts by result.",
	}, []string{"result"})

	cacheRedisCollectorUp = promauto.With(prometheus.DefaultRegisterer).NewGauge(prometheus.GaugeOpts{
		Name: "rca_cache_redis_collector_up",
		Help: "Redis runtime stats collector health (1=ok, 0=error).",
	})

	cacheRedisRuntimeValue = promauto.With(prometheus.DefaultRegisterer).NewGaugeVec(prometheus.GaugeOpts{
		Name: "rca_cache_redis_runtime_value",
		Help: "Selected redis runtime values from INFO stats/memory.",
	}, []string{"metric"})

	cacheRedisKeyCount = promauto.With(prometheus.DefaultRegisterer).NewGaugeVec(prometheus.GaugeOpts{
		Name: "rca_cache_redis_key_count",
		Help: "Redis key counts by DB section from INFO keyspace.",
	}, []string{"db"})

	cacheTTLEffectiveSeconds = promauto.With(prometheus.DefaultRegisterer).NewGaugeVec(prometheus.GaugeOpts{
		Name: "rca_cache_ttl_effective_seconds",
		Help: "Current effective ttl seconds selected by adaptive ttl policy per module.",
	}, []string{"module"})

	cacheTTLAdjustTotal = promauto.With(prometheus.DefaultRegisterer).NewCounterVec(prometheus.CounterOpts{
		Name: "rca_cache_ttl_adjust_total",
		Help: "Total adaptive ttl decisions by module/decision.",
	}, []string{"module", "decision"})

	deleteByPrefixLua = redis.NewScript(`
local pattern = ARGV[1]
local scan_count = tonumber(ARGV[2])
local max_delete = tonumber(ARGV[3])
local res = redis.call('SCAN', '0', 'MATCH', pattern, 'COUNT', scan_count)
local keys = res[2]
local deleted = 0
if #keys > 0 then
  if max_delete > 0 and #keys > max_delete then
    while #keys > max_delete do
      table.remove(keys)
    end
  end
  local unlink_result = redis.pcall('UNLINK', unpack(keys))
  if type(unlink_result) == 'table' and unlink_result['err'] then
    redis.call('DEL', unpack(keys))
  end
  deleted = #keys
end
return {deleted}
`)

	disableDeleteByPrefixLua uint32
	ttlStateMu               sync.Mutex
	ttlStateByModule         = map[string]*moduleTTLStat{}
)

func init() {
	cacheConfiguredTTLSeconds.WithLabelValues("inbox").Set(TTLInbox.Seconds())
	cacheConfiguredTTLSeconds.WithLabelValues("workbench").Set(TTLWorkbench.Seconds())
	cacheConfiguredTTLSeconds.WithLabelValues("dashboard").Set(TTLDashboard.Seconds())
	cacheConfiguredTTLSeconds.WithLabelValues("trace").Set(TTLTrace.Seconds())
	cacheConfiguredTTLSeconds.WithLabelValues("compare").Set(TTLCompare.Seconds())
	cacheConfiguredTTLSeconds.WithLabelValues("history").Set(TTLHistory.Seconds())
	cacheConfiguredTTLSeconds.WithLabelValues("assignment_history").Set(TTLHistory.Seconds())
	cacheConfiguredTTLSeconds.WithLabelValues("assignment_history_global").Set(TTLHistory.Seconds())
	cacheConfiguredTTLSeconds.WithLabelValues("session_state").Set(TTLSession.Seconds())

	cacheOperationTotal.WithLabelValues("get", "unknown", "hit").Add(0)
	cacheOperationTotal.WithLabelValues("get", "unknown", "miss").Add(0)
	cacheOperationTotal.WithLabelValues("set", "unknown", "ok").Add(0)
	cacheOperationTotal.WithLabelValues("delete", "unknown", "ok").Add(0)
	cacheOperationTotal.WithLabelValues("delete_prefix", "unknown", "ok").Add(0)
	cacheInvalidationTotal.WithLabelValues("session", "ok").Add(0)
	cacheInvalidationTotal.WithLabelValues("operator", "ok").Add(0)
	cacheInvalidationTotal.WithLabelValues("trace", "ok").Add(0)
	cacheTTLEffectiveSeconds.WithLabelValues("inbox").Set(TTLInbox.Seconds())
	cacheTTLEffectiveSeconds.WithLabelValues("workbench").Set(TTLWorkbench.Seconds())
	cacheTTLEffectiveSeconds.WithLabelValues("dashboard").Set(TTLDashboard.Seconds())
	cacheTTLEffectiveSeconds.WithLabelValues("trace").Set(TTLTrace.Seconds())
	cacheTTLEffectiveSeconds.WithLabelValues("compare").Set(TTLCompare.Seconds())
	cacheTTLEffectiveSeconds.WithLabelValues("history").Set(TTLHistory.Seconds())
	cacheTTLEffectiveSeconds.WithLabelValues("assignment_history").Set(TTLHistory.Seconds())
	cacheTTLEffectiveSeconds.WithLabelValues("assignment_history_global").Set(TTLHistory.Seconds())
	cacheTTLEffectiveSeconds.WithLabelValues("session_state").Set(TTLSession.Seconds())
	cacheTTLAdjustTotal.WithLabelValues("inbox", "baseline").Add(0)
	cacheTTLAdjustTotal.WithLabelValues("dashboard", "baseline").Add(0)
	cacheTTLAdjustTotal.WithLabelValues("trace", "baseline").Add(0)
	cacheRedisCollectorTotal.WithLabelValues("ok").Add(0)
	cacheRedisCollectorTotal.WithLabelValues("error").Add(0)
	cacheRedisCollectorTotal.WithLabelValues("skipped").Add(0)
	cacheRedisCollectorUp.Set(0)
}

// ConfigureRedisClient binds one process-wide redis client for read-side caches.
func ConfigureRedisClient(c *redis.Client) {
	clientMu.Lock()
	old := client
	client = c
	clientMu.Unlock()

	if old != nil && old != c {
		_ = old.Close()
	}
	if c == nil {
		StopRuntimeStatsCollector()
	}
}

// Close releases the process-wide cache client.
func Close() error {
	StopRuntimeStatsCollector()
	clientMu.Lock()
	old := client
	client = nil
	clientMu.Unlock()
	if old == nil {
		return nil
	}
	return old.Close()
}

func loadClient() *redis.Client {
	clientMu.RLock()
	defer clientMu.RUnlock()
	return client
}

func operationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, defaultCacheOpTimeout)
}

// Enabled reports whether redis cache backend is available.
func Enabled() bool {
	return loadClient() != nil
}

func defaultTTLByModule(module string) time.Duration {
	switch module {
	case "inbox":
		return TTLInbox
	case "workbench":
		return TTLWorkbench
	case "dashboard":
		return TTLDashboard
	case "trace":
		return TTLTrace
	case "compare":
		return TTLCompare
	case "history", "assignment_history", "assignment_history_global":
		return TTLHistory
	case "session_state":
		return TTLSession
	default:
		return TTLInbox
	}
}

func ttlPolicy(module string) moduleTTLPolicy {
	switch module {
	case "inbox":
		return moduleTTLPolicy{Base: TTLInbox, Min: 30 * time.Second, Max: 1 * time.Minute, Dynamic: true}
	case "workbench":
		return moduleTTLPolicy{Base: TTLWorkbench, Min: 30 * time.Second, Max: 1 * time.Minute, Dynamic: true}
	case "history", "assignment_history", "assignment_history_global":
		return moduleTTLPolicy{Base: TTLHistory, Min: 30 * time.Second, Max: 1 * time.Minute, Dynamic: true}
	case "session_state":
		return moduleTTLPolicy{Base: TTLSession, Min: 30 * time.Second, Max: 1 * time.Minute, Dynamic: true}
	case "dashboard":
		return moduleTTLPolicy{Base: TTLDashboard, Min: 1 * time.Minute, Max: 1 * time.Minute, Dynamic: false, FixedTTL: true}
	case "trace":
		return moduleTTLPolicy{Base: TTLTrace, Min: 1 * time.Minute, Max: 5 * time.Minute, Dynamic: true}
	case "compare":
		return moduleTTLPolicy{Base: TTLCompare, Min: 1 * time.Minute, Max: 5 * time.Minute, Dynamic: true}
	default:
		base := defaultTTLByModule(module)
		return moduleTTLPolicy{Base: base, Min: base, Max: base, Dynamic: false}
	}
}

func shouldApplyAdaptiveTTL(requested time.Duration, policy moduleTTLPolicy) bool {
	if requested <= 0 {
		return true
	}
	// Preserve explicit custom TTL (e.g. tests with very short expiry).
	return requested == policy.Base
}

func resolveEffectiveTTL(module string, requested time.Duration) time.Duration {
	module = normalizeMetricLabel(module)
	policy := ttlPolicy(module)
	target := requested
	if target <= 0 {
		target = policy.Base
	}
	if requested > 0 && !shouldApplyAdaptiveTTL(requested, policy) {
		cacheTTLEffectiveSeconds.WithLabelValues(module).Set(requested.Seconds())
		cacheTTLAdjustTotal.WithLabelValues(module, "explicit").Inc()
		return requested
	}

	decision := "baseline"
	if policy.FixedTTL {
		target = policy.Base
		decision = "fixed"
	} else if policy.Dynamic && shouldApplyAdaptiveTTL(requested, policy) {
		accessCount, invalidationCount := snapshotTTLAdaptiveStats(module)
		switch {
		case invalidationCount >= 20 && invalidationCount >= accessCount:
			target = policy.Min
			decision = "shorten_hot_invalidation"
		case accessCount >= 200 && invalidationCount < 20:
			target = policy.Max
			decision = "extend_hot_read"
		case accessCount >= 80 && invalidationCount < 10:
			target = clampDuration(policy.Base+15*time.Second, policy.Min, policy.Max)
			decision = "extend_warm_read"
		case invalidationCount >= 10:
			target = clampDuration(policy.Base-15*time.Second, policy.Min, policy.Max)
			decision = "shorten_warm_invalidation"
		default:
			target = clampDuration(policy.Base, policy.Min, policy.Max)
			decision = "baseline"
		}
	}
	target = clampDuration(target, policy.Min, policy.Max)
	cacheTTLEffectiveSeconds.WithLabelValues(module).Set(target.Seconds())
	cacheTTLAdjustTotal.WithLabelValues(module, decision).Inc()
	return target
}

func clampDuration(value time.Duration, min time.Duration, max time.Duration) time.Duration {
	if min > 0 && value < min {
		return min
	}
	if max > 0 && value > max {
		return max
	}
	return value
}

func recordTTLAccess(module string) {
	module = normalizeMetricLabel(module)
	now := time.Now().UTC()
	ttlStateMu.Lock()
	defer ttlStateMu.Unlock()
	state := ttlStateByModule[module]
	if state == nil || now.Sub(state.WindowStart) >= ttlAdaptiveWindow {
		state = &moduleTTLStat{WindowStart: now}
		ttlStateByModule[module] = state
	}
	state.AccessCount++
}

func recordTTLInvalidation(module string, count int64) {
	module = normalizeMetricLabel(module)
	if count <= 0 {
		count = 1
	}
	now := time.Now().UTC()
	ttlStateMu.Lock()
	defer ttlStateMu.Unlock()
	state := ttlStateByModule[module]
	if state == nil || now.Sub(state.WindowStart) >= ttlAdaptiveWindow {
		state = &moduleTTLStat{WindowStart: now}
		ttlStateByModule[module] = state
	}
	state.InvalidationHits += count
}

func snapshotTTLAdaptiveStats(module string) (int64, int64) {
	module = normalizeMetricLabel(module)
	now := time.Now().UTC()
	ttlStateMu.Lock()
	defer ttlStateMu.Unlock()
	state := ttlStateByModule[module]
	if state == nil {
		return 0, 0
	}
	if now.Sub(state.WindowStart) >= ttlAdaptiveWindow {
		state.WindowStart = now
		state.AccessCount = 0
		state.InvalidationHits = 0
		return 0, 0
	}
	return state.AccessCount, state.InvalidationHits
}

// GetJSON reads one json value from redis cache.
func GetJSON(ctx context.Context, key string, out any) bool {
	c := loadClient()
	key = strings.TrimSpace(key)
	module := cacheModuleFromKey(key)
	recordTTLAccess(module)
	start := time.Now()
	defer recordCacheOperationLatency("get", module, time.Since(start))
	if c == nil || key == "" || out == nil {
		recordCacheOperation("get", module, "bypass")
		return false
	}
	opCtx, cancel := operationContext(ctx)
	defer cancel()
	raw, err := c.Get(opCtx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			recordCacheOperation("get", module, "miss")
		} else {
			recordCacheOperation("get", module, "error")
		}
		return false
	}
	if len(raw) == 0 {
		recordCacheOperation("get", module, "miss")
		return false
	}
	if err := json.Unmarshal(raw, out); err != nil {
		recordCacheOperation("get", module, "error")
		return false
	}
	recordCacheOperation("get", module, "hit")
	return true
}

// SetJSON writes one json value into redis cache.
func SetJSON(ctx context.Context, key string, value any, ttl time.Duration) bool {
	c := loadClient()
	key = strings.TrimSpace(key)
	module := cacheModuleFromKey(key)
	start := time.Now()
	defer recordCacheOperationLatency("set", module, time.Since(start))
	if c == nil || key == "" || value == nil {
		recordCacheOperation("set", module, "bypass")
		return false
	}
	ttl = resolveEffectiveTTL(module, ttl)
	raw, err := json.Marshal(value)
	if err != nil {
		recordCacheOperation("set", module, "error")
		return false
	}
	opCtx, cancel := operationContext(ctx)
	defer cancel()
	if err := c.Set(opCtx, key, raw, ttl).Err(); err != nil {
		recordCacheOperation("set", module, "error")
		return false
	}
	recordCacheOperation("set", module, "ok")
	return true
}

// Delete removes specific keys.
func Delete(ctx context.Context, keys ...string) bool {
	c := loadClient()
	start := time.Now()
	defer recordCacheOperationLatency("delete", "multi", time.Since(start))
	if c == nil || len(keys) == 0 {
		recordCacheOperation("delete", "unknown", "bypass")
		return false
	}
	cleaned := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		cleaned = append(cleaned, key)
	}
	if len(cleaned) == 0 {
		recordCacheOperation("delete", "unknown", "bypass")
		return false
	}
	opCtx, cancel := operationContext(ctx)
	defer cancel()
	if err := c.Del(opCtx, cleaned...).Err(); err != nil {
		for _, key := range cleaned {
			recordCacheOperation("delete", cacheModuleFromKey(key), "error")
		}
		return false
	}
	for _, key := range cleaned {
		module := cacheModuleFromKey(key)
		recordCacheOperation("delete", module, "ok")
		cacheInvalidatedKeysTotal.WithLabelValues(module).Inc()
		recordTTLInvalidation(module, 1)
	}
	return true
}

// DeleteByPrefix removes cache entries by key prefix, with one bounded scan.
func DeleteByPrefix(ctx context.Context, prefix string, maxKeys int64) int64 {
	c := loadClient()
	prefix = strings.TrimSpace(prefix)
	module := cacheModuleFromPrefix(prefix)
	start := time.Now()
	defer recordCacheOperationLatency("delete_prefix", module, time.Since(start))
	if c == nil || prefix == "" {
		recordCacheOperation("delete_prefix", module, "bypass")
		return 0
	}
	if maxKeys <= 0 {
		maxKeys = defaultScanCount
	}

	deleted := deleteByPrefixWithLuaOrScan(ctx, c, prefix, maxKeys)
	cacheInvalidatedKeysTotal.WithLabelValues(module).Add(float64(deleted))
	if deleted > 0 {
		recordTTLInvalidation(module, deleted)
	}
	recordCacheOperation("delete_prefix", module, "ok")
	return deleted
}

func deleteByPrefixWithLuaOrScan(ctx context.Context, c *redis.Client, prefix string, maxKeys int64) int64 {
	pattern := prefix + "*"
	deleted := int64(0)
	luaEnabled := atomic.LoadUint32(&disableDeleteByPrefixLua) == 0
	scanCount := maxKeys
	if scanCount <= 0 || scanCount > luaDeleteScanCount {
		scanCount = luaDeleteScanCount
	}

	for {
		remaining := maxKeys - deleted
		if remaining <= 0 {
			break
		}
		if luaEnabled {
			opCtx, cancel := operationContext(ctx)
			n, err := runDeleteByPrefixLua(opCtx, c, pattern, scanCount, remaining)
			cancel()
			if err == nil {
				deleted += n
				if deleted >= maxKeys {
					break
				}
				if n == 0 {
					break
				}
				continue
			}
			luaEnabled = false
			atomic.StoreUint32(&disableDeleteByPrefixLua, 1)
		}

		nextDeleted, done := deleteByPrefixScanFallback(ctx, c, pattern, scanCount, remaining)
		deleted += nextDeleted
		if done || deleted >= maxKeys {
			break
		}
		// Fallback path scans from zero each round; break when no progress.
		if nextDeleted == 0 {
			break
		}
	}
	return deleted
}

func runDeleteByPrefixLua(
	ctx context.Context,
	c *redis.Client,
	pattern string,
	scanCount int64,
	maxDelete int64,
) (int64, error) {
	raw, err := deleteByPrefixLua.Run(
		ctx,
		c,
		nil,
		pattern,
		scanCount,
		maxDelete,
	).Result()
	if err != nil {
		return 0, err
	}
	array, ok := raw.([]any)
	if !ok || len(array) < 1 {
		return 0, fmt.Errorf("unexpected lua response: %T", raw)
	}
	return parseLuaInt64(array[0]), nil
}

func deleteByPrefixScanFallback(
	ctx context.Context,
	c *redis.Client,
	pattern string,
	scanCount int64,
	maxDelete int64,
) (int64, bool) {
	opCtx, cancel := operationContext(ctx)
	keys, _, scanErr := c.Scan(opCtx, 0, pattern, scanCount).Result()
	cancel()
	if scanErr != nil {
		return 0, true
	}
	if len(keys) == 0 {
		return 0, true
	}
	opCtx, cancel = operationContext(ctx)
	deleted, delErr := c.Del(opCtx, keys...).Result()
	cancel()
	if delErr != nil {
		return 0, true
	}
	if maxDelete > 0 && deleted > maxDelete {
		deleted = maxDelete
	}
	return deleted, deleted == 0 || (maxDelete > 0 && deleted >= maxDelete)
}

func parseLuaInt64(raw any) int64 {
	switch v := raw.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case uint64:
		return int64(v)
	case float64:
		return int64(v)
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err == nil {
			return parsed
		}
	}
	return 0
}

// HashKeyPart returns a stable short hash for cache key composition.
func HashKeyPart(parts ...string) string {
	hasher := sha1.New()
	for _, part := range parts {
		_, _ = hasher.Write([]byte(strings.TrimSpace(part)))
		_, _ = hasher.Write([]byte{0})
	}
	sum := hex.EncodeToString(hasher.Sum(nil))
	if len(sum) > 16 {
		return sum[:16]
	}
	return sum
}

func BuildInboxKey(operatorID string, filterHash string) string {
	operatorID = normalizeCacheKeyToken(operatorID, "anonymous")
	filterHash = normalizeCacheKeyToken(filterHash, "all")
	return "inbox:" + operatorID + ":" + filterHash
}

func BuildWorkbenchKey(sessionID string) string {
	return "workbench:" + normalizeCacheKeyToken(sessionID, "unknown")
}

func BuildDashboardKey(teamID string) string {
	return "dashboard:" + normalizeCacheKeyToken(teamID, "global")
}

func BuildTraceKey(jobID string) string {
	return "trace:" + normalizeCacheKeyToken(jobID, "unknown")
}

func BuildCompareKey(leftJobID string, rightJobID string) string {
	return "compare:" + normalizeCacheKeyToken(leftJobID, "unknown") + ":" + normalizeCacheKeyToken(rightJobID, "unknown")
}

func BuildHistoryKey(sessionID string, offset int64, limit int64, ascending bool) string {
	order := "desc"
	if ascending {
		order = "asc"
	}
	return "history:" + normalizeCacheKeyToken(sessionID, "unknown") + ":" + int64String(offset) + ":" + int64String(limit) + ":" + order
}

func BuildSessionStateKey(sessionID string) string {
	return "session_state:" + normalizeCacheKeyToken(sessionID, "unknown")
}

func BuildGlobalAssignmentHistoryKey(parts ...string) string {
	return globalAssignmentHistoryPrefix + HashKeyPart(parts...)
}

func int64String(v int64) string {
	return strconv.FormatInt(v, 10)
}

func normalizeCacheKeyToken(raw string, fallback string) string {
	token := strings.TrimSpace(strings.ToLower(raw))
	if token == "" {
		token = fallback
	}
	replacer := strings.NewReplacer(" ", "_", "/", "_", ":", "_", "?", "_", "&", "_", "=", "_")
	token = replacer.Replace(token)
	if token == "" {
		token = fallback
	}
	return token
}

// InvalidateSessionReadModels clears session-centric cached read models.
func InvalidateSessionReadModels(ctx context.Context, sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		recordCacheInvalidation("session", "skip")
		return
	}
	_ = Delete(ctx,
		BuildWorkbenchKey(sessionID),
		BuildSessionStateKey(sessionID),
	)
	_ = DeleteByPrefix(ctx, "history:"+normalizeCacheKeyToken(sessionID, "unknown")+":", defaultScanCount)
	recordCacheInvalidation("session", "ok")
}

// InvalidateOperatorReadModels clears global/operator queue/dashboard cache entries.
func InvalidateOperatorReadModels(ctx context.Context) {
	_ = DeleteByPrefix(ctx, "inbox:", defaultScanCount)
	_ = DeleteByPrefix(ctx, "dashboard:", defaultScanCount)
	_ = DeleteByPrefix(ctx, globalAssignmentHistoryPrefix, defaultScanCount)
	recordCacheInvalidation("operator", "ok")
}

// InvalidateTraceReadModels clears trace/compare cache entries.
func InvalidateTraceReadModels(ctx context.Context, jobID string) {
	jobID = strings.TrimSpace(jobID)
	if jobID != "" {
		_ = Delete(ctx, BuildTraceKey(jobID))
	}
	_ = DeleteByPrefix(ctx, "compare:", defaultScanCount)
	recordCacheInvalidation("trace", "ok")
}

// StartRuntimeStatsCollector starts periodic redis runtime stats collection used by
// dashboards/alerts. Calling this function multiple times is safe.
func StartRuntimeStatsCollector(ctx context.Context, interval time.Duration) {
	if ctx == nil {
		ctx = context.Background()
	}
	if interval <= 0 {
		interval = defaultStatsInterval
	}
	if loadClient() == nil {
		return
	}

	statsCollectorMu.Lock()
	if statsCollectorCancel != nil {
		statsCollectorMu.Unlock()
		return
	}
	collectorCtx, cancel := context.WithCancel(context.Background())
	statsCollectorCancel = cancel
	statsCollectorMu.Unlock()

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		collectRuntimeStats(collectorCtx)
		for {
			select {
			case <-collectorCtx.Done():
				return
			case <-ticker.C:
				collectRuntimeStats(collectorCtx)
			case <-ctx.Done():
				StopRuntimeStatsCollector()
				return
			}
		}
	}()
}

// StopRuntimeStatsCollector stops periodic redis runtime stats collection.
func StopRuntimeStatsCollector() {
	statsCollectorMu.Lock()
	cancel := statsCollectorCancel
	statsCollectorCancel = nil
	statsCollectorMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func collectRuntimeStats(ctx context.Context) {
	c := loadClient()
	if c == nil {
		cacheRedisCollectorUp.Set(0)
		cacheRedisCollectorTotal.WithLabelValues("skipped").Inc()
		return
	}
	opCtx, cancel := operationContext(ctx)
	info, err := c.Info(opCtx, "memory", "stats", "keyspace").Result()
	cancel()
	if err != nil {
		cacheRedisCollectorUp.Set(0)
		cacheRedisCollectorTotal.WithLabelValues("error").Inc()
		return
	}
	metricsMap := parseRedisInfoPairs(info)
	updateRedisRuntimeGauge(metricsMap, "used_memory", "used_memory_bytes")
	updateRedisRuntimeGauge(metricsMap, "keyspace_hits", "keyspace_hits")
	updateRedisRuntimeGauge(metricsMap, "keyspace_misses", "keyspace_misses")
	updateRedisRuntimeGauge(metricsMap, "expired_keys", "expired_keys")
	updateRedisRuntimeGauge(metricsMap, "evicted_keys", "evicted_keys")

	for key, value := range metricsMap {
		if !strings.HasPrefix(key, "db") {
			continue
		}
		keys, ok := parseRedisKeyspaceKeys(value)
		if !ok {
			continue
		}
		cacheRedisKeyCount.WithLabelValues(strings.ToLower(strings.TrimSpace(key))).Set(keys)
	}

	opCtx, cancel = operationContext(ctx)
	dbSize, err := c.DBSize(opCtx).Result()
	cancel()
	if err == nil {
		cacheRedisRuntimeValue.WithLabelValues("dbsize").Set(float64(dbSize))
	}

	cacheRedisCollectorUp.Set(1)
	cacheRedisCollectorTotal.WithLabelValues("ok").Inc()
}

func updateRedisRuntimeGauge(metricsMap map[string]string, srcKey string, targetMetric string) {
	raw := strings.TrimSpace(metricsMap[strings.ToLower(strings.TrimSpace(srcKey))])
	if raw == "" {
		return
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return
	}
	cacheRedisRuntimeValue.WithLabelValues(strings.TrimSpace(targetMetric)).Set(value)
}

func parseRedisInfoPairs(raw string) map[string]string {
	out := map[string]string{}
	lines := strings.Split(raw, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(parts[0]))
		value := strings.TrimSpace(parts[1])
		if key == "" || value == "" {
			continue
		}
		out[key] = value
	}
	return out
}

func parseRedisKeyspaceKeys(raw string) (float64, bool) {
	chunks := strings.Split(strings.TrimSpace(raw), ",")
	for _, chunk := range chunks {
		parts := strings.SplitN(strings.TrimSpace(chunk), "=", 2)
		if len(parts) != 2 {
			continue
		}
		if strings.TrimSpace(parts[0]) != "keys" {
			continue
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if err != nil {
			return 0, false
		}
		return value, true
	}
	return 0, false
}

func recordCacheOperation(op string, module string, result string) {
	cacheOperationTotal.WithLabelValues(normalizeMetricLabel(op), normalizeMetricLabel(module), normalizeMetricLabel(result)).Inc()
}

func recordCacheOperationLatency(op string, module string, duration time.Duration) {
	cacheOperationLatencyMS.WithLabelValues(normalizeMetricLabel(op), normalizeMetricLabel(module)).Observe(float64(duration) / float64(time.Millisecond))
}

func recordCacheInvalidation(scope string, result string) {
	cacheInvalidationTotal.WithLabelValues(normalizeMetricLabel(scope), normalizeMetricLabel(result)).Inc()
}

func cacheModuleFromPrefix(prefix string) string {
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	switch {
	case strings.HasPrefix(prefix, "inbox:"):
		return "inbox"
	case strings.HasPrefix(prefix, "workbench:"):
		return "workbench"
	case strings.HasPrefix(prefix, "dashboard:"):
		return "dashboard"
	case strings.HasPrefix(prefix, "trace:"):
		return "trace"
	case strings.HasPrefix(prefix, "compare:"):
		return "compare"
	case strings.HasPrefix(prefix, globalAssignmentHistoryPrefix):
		return "assignment_history_global"
	case strings.HasPrefix(prefix, "history:"):
		return "history"
	case strings.HasPrefix(prefix, "session_state:"):
		return "session_state"
	default:
		return "unknown"
	}
}

func cacheModuleFromKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	switch {
	case strings.HasPrefix(key, "inbox:"):
		return "inbox"
	case strings.HasPrefix(key, "workbench:"):
		return "workbench"
	case strings.HasPrefix(key, "dashboard:"):
		return "dashboard"
	case strings.HasPrefix(key, "trace:"):
		return "trace"
	case strings.HasPrefix(key, "compare:"):
		return "compare"
	case strings.HasPrefix(key, globalAssignmentHistoryPrefix):
		return "assignment_history_global"
	case strings.HasPrefix(key, "history:") && strings.HasSuffix(key, ":assignment"):
		return "assignment_history"
	case strings.HasPrefix(key, "history:"):
		return "history"
	case strings.HasPrefix(key, "session_state:"):
		return "session_state"
	default:
		return "unknown"
	}
}

func normalizeMetricLabel(raw string) string {
	out := strings.ToLower(strings.TrimSpace(raw))
	if out == "" {
		return "unknown"
	}
	replacer := strings.NewReplacer(" ", "_", "/", "_", ":", "_", "-", "_", ".", "_")
	out = replacer.Replace(out)
	if out == "" {
		return "unknown"
	}
	return out
}
