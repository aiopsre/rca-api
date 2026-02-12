package cachex

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	defaultCacheOpTimeout = 1500 * time.Millisecond
	defaultScanCount      = int64(200)

	// Read-side cache TTLs.
	TTLInbox     = 45 * time.Second
	TTLWorkbench = 45 * time.Second
	TTLDashboard = 1 * time.Minute
	TTLTrace     = 2 * time.Minute
	TTLCompare   = 2 * time.Minute
	TTLHistory   = 45 * time.Second
	TTLSession   = 45 * time.Second
)

var (
	clientMu sync.RWMutex
	client   *redis.Client
)

// ConfigureRedisClient binds one process-wide redis client for read-side caches.
func ConfigureRedisClient(c *redis.Client) {
	clientMu.Lock()
	old := client
	client = c
	clientMu.Unlock()

	if old != nil && old != c {
		_ = old.Close()
	}
}

// Close releases the process-wide cache client.
func Close() error {
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

// GetJSON reads one json value from redis cache.
func GetJSON(ctx context.Context, key string, out any) bool {
	c := loadClient()
	key = strings.TrimSpace(key)
	if c == nil || key == "" || out == nil {
		return false
	}
	opCtx, cancel := operationContext(ctx)
	defer cancel()
	raw, err := c.Get(opCtx, key).Bytes()
	if err != nil || len(raw) == 0 {
		return false
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return false
	}
	return true
}

// SetJSON writes one json value into redis cache.
func SetJSON(ctx context.Context, key string, value any, ttl time.Duration) bool {
	c := loadClient()
	key = strings.TrimSpace(key)
	if c == nil || key == "" || value == nil {
		return false
	}
	if ttl <= 0 {
		ttl = TTLInbox
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return false
	}
	opCtx, cancel := operationContext(ctx)
	defer cancel()
	return c.Set(opCtx, key, raw, ttl).Err() == nil
}

// Delete removes specific keys.
func Delete(ctx context.Context, keys ...string) bool {
	c := loadClient()
	if c == nil || len(keys) == 0 {
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
		return false
	}
	opCtx, cancel := operationContext(ctx)
	defer cancel()
	return c.Del(opCtx, cleaned...).Err() == nil
}

// DeleteByPrefix removes cache entries by key prefix, with one bounded scan.
func DeleteByPrefix(ctx context.Context, prefix string, maxKeys int64) int64 {
	c := loadClient()
	prefix = strings.TrimSpace(prefix)
	if c == nil || prefix == "" {
		return 0
	}
	if maxKeys <= 0 {
		maxKeys = defaultScanCount
	}

	var cursor uint64
	var deleted int64
	pattern := prefix + "*"
	for {
		opCtx, cancel := operationContext(ctx)
		keys, nextCursor, err := c.Scan(opCtx, cursor, pattern, maxKeys).Result()
		cancel()
		if err != nil {
			return deleted
		}
		if len(keys) > 0 {
			opCtx, cancel = operationContext(ctx)
			n, delErr := c.Del(opCtx, keys...).Result()
			cancel()
			if delErr == nil {
				deleted += n
			}
		}
		cursor = nextCursor
		if cursor == 0 || deleted >= maxKeys {
			break
		}
	}
	return deleted
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
		return
	}
	_ = Delete(ctx,
		BuildWorkbenchKey(sessionID),
		BuildSessionStateKey(sessionID),
	)
	_ = DeleteByPrefix(ctx, "history:"+normalizeCacheKeyToken(sessionID, "unknown")+":", defaultScanCount)
}

// InvalidateOperatorReadModels clears global/operator queue/dashboard cache entries.
func InvalidateOperatorReadModels(ctx context.Context) {
	_ = DeleteByPrefix(ctx, "inbox:", defaultScanCount)
	_ = DeleteByPrefix(ctx, "dashboard:", defaultScanCount)
}

// InvalidateTraceReadModels clears trace/compare cache entries.
func InvalidateTraceReadModels(ctx context.Context, jobID string) {
	jobID = strings.TrimSpace(jobID)
	if jobID != "" {
		_ = Delete(ctx, BuildTraceKey(jobID))
	}
	_ = DeleteByPrefix(ctx, "compare:", defaultScanCount)
}
