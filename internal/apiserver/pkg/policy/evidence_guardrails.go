package policy

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	defaultMaxMetricsRangeHours = 24
	defaultMaxLogsRangeHours    = 6
	defaultMaxLogsLimit         = 500
	defaultMaxPageSize          = 200
	defaultMaxResultBytes       = 256 * 1024
	defaultMaxMetricsRows       = 5000
	defaultMaxLogsRows          = 2000
	defaultDefaultTimeoutMs     = 5000
	defaultMinTimeoutMs         = 500
	defaultMaxTimeoutMs         = 120000
	defaultQueryRatePerSec      = 5.0
	defaultQueryRateBurst       = 10
)

// EvidenceGuardrails keeps hard limits for evidence query/save paths.
type EvidenceGuardrails struct {
	MaxMetricsRange time.Duration
	MaxLogsRange    time.Duration
	MaxLogsLimit    int64
	MaxPageSize     int64
	MaxResultBytes  int
	MaxMetricsRows  int64
	MaxLogsRows     int64

	DefaultDatasourceTimeout time.Duration
	MinDatasourceTimeout     time.Duration
	MaxDatasourceTimeout     time.Duration

	QueryRatePerSecond float64
	QueryRateBurst     int
}

// DefaultEvidenceGuardrails returns P0 defaults from docs.
func DefaultEvidenceGuardrails() EvidenceGuardrails {
	return EvidenceGuardrails{
		MaxMetricsRange:          time.Duration(defaultMaxMetricsRangeHours) * time.Hour,
		MaxLogsRange:             time.Duration(defaultMaxLogsRangeHours) * time.Hour,
		MaxLogsLimit:             defaultMaxLogsLimit,
		MaxPageSize:              defaultMaxPageSize,
		MaxResultBytes:           defaultMaxResultBytes,
		MaxMetricsRows:           defaultMaxMetricsRows,
		MaxLogsRows:              defaultMaxLogsRows,
		DefaultDatasourceTimeout: time.Duration(defaultDefaultTimeoutMs) * time.Millisecond,
		MinDatasourceTimeout:     time.Duration(defaultMinTimeoutMs) * time.Millisecond,
		MaxDatasourceTimeout:     time.Duration(defaultMaxTimeoutMs) * time.Millisecond,
		QueryRatePerSecond:       defaultQueryRatePerSec,
		QueryRateBurst:           defaultQueryRateBurst,
	}
}

// ClampDatasourceTimeout keeps timeout in safe bounds.
func (g EvidenceGuardrails) ClampDatasourceTimeout(timeoutMs int64) time.Duration {
	if timeoutMs <= 0 {
		return g.DefaultDatasourceTimeout
	}
	timeout := time.Duration(timeoutMs) * time.Millisecond
	if timeout < g.MinDatasourceTimeout {
		return g.MinDatasourceTimeout
	}
	if timeout > g.MaxDatasourceTimeout {
		return g.MaxDatasourceTimeout
	}
	return timeout
}

// DatasourceRateLimiter applies per-datasource query throttling.
type DatasourceRateLimiter struct {
	ratePerSecond float64
	burst         int

	mu       sync.Mutex
	limiters map[string]*rate.Limiter
}

// NewDatasourceRateLimiter creates a datasource-level rate limiter.
func NewDatasourceRateLimiter(g EvidenceGuardrails) *DatasourceRateLimiter {
	return &DatasourceRateLimiter{
		ratePerSecond: g.QueryRatePerSecond,
		burst:         g.QueryRateBurst,
		limiters:      map[string]*rate.Limiter{},
	}
}

// Allow returns whether one query call is allowed.
func (d *DatasourceRateLimiter) Allow(datasourceID string) bool {
	if datasourceID == "" {
		return false
	}

	d.mu.Lock()
	limiter, ok := d.limiters[datasourceID]
	if !ok {
		limiter = rate.NewLimiter(rate.Limit(d.ratePerSecond), d.burst)
		d.limiters[datasourceID] = limiter
	}
	d.mu.Unlock()

	return limiter.Allow()
}
