package ingest

import (
	"strings"
	"sync/atomic"

	"github.com/aiopsre/rca-api/internal/apiserver/pkg/redisx"
)

const (
	// DefaultRedisKeyPrefix is the default key prefix for alert ingest policy short-state.
	DefaultRedisKeyPrefix = "rca:alert"
)

// BurstConfig controls optional burst suppression thresholds.
type BurstConfig struct {
	WindowSeconds int `json:"window_seconds" mapstructure:"window_seconds"`
	Threshold     int `json:"threshold" mapstructure:"threshold"`
}

// RedisBackendConfig controls whether redis is used for ingest short-state decisions.
type RedisBackendConfig struct {
	Enabled   bool   `json:"enabled" mapstructure:"enabled"`
	KeyPrefix string `json:"key_prefix" mapstructure:"key_prefix"`
}

// PolicyConfig controls alert ingest denoise/suppress policy.
type PolicyConfig struct {
	DedupWindowSeconds int                `json:"dedup_window_seconds" mapstructure:"dedup_window_seconds"`
	Burst              BurstConfig        `json:"burst" mapstructure:"burst"`
	RedisBackend       RedisBackendConfig `json:"redis_backend" mapstructure:"redis_backend"`
}

// RuntimeConfig carries process-level runtime options used by policy pipeline builders.
type RuntimeConfig struct {
	Policy PolicyConfig        `json:"policy" mapstructure:"policy"`
	Redis  redisx.RedisOptions `json:"redis" mapstructure:"redis"`
}

var runtimeConfig atomic.Value

// DefaultPolicyConfig returns the default compatible ingest policy.
func DefaultPolicyConfig() PolicyConfig {
	return PolicyConfig{
		DedupWindowSeconds: 0,
		Burst: BurstConfig{
			WindowSeconds: 0,
			Threshold:     0,
		},
		RedisBackend: RedisBackendConfig{
			Enabled:   false,
			KeyPrefix: DefaultRedisKeyPrefix,
		},
	}
}

// DefaultRuntimeConfig returns process-level defaults.
func DefaultRuntimeConfig() RuntimeConfig {
	return RuntimeConfig{
		Policy: DefaultPolicyConfig(),
		Redis:  redisx.NewRedisOptions(),
	}
}

// ApplyDefaults normalizes policy fields and applies defaults for empty values.
func (c *PolicyConfig) ApplyDefaults() {
	if c == nil {
		return
	}
	if c.DedupWindowSeconds < 0 {
		c.DedupWindowSeconds = 0
	}
	if c.Burst.WindowSeconds < 0 {
		c.Burst.WindowSeconds = 0
	}
	if c.Burst.Threshold < 0 {
		c.Burst.Threshold = 0
	}
	c.RedisBackend.KeyPrefix = strings.TrimSpace(c.RedisBackend.KeyPrefix)
	if c.RedisBackend.KeyPrefix == "" {
		c.RedisBackend.KeyPrefix = DefaultRedisKeyPrefix
	}
}

// SetRuntimeConfig sets process-level ingest policy runtime options.
func SetRuntimeConfig(cfg RuntimeConfig) {
	cfg.Policy.ApplyDefaults()
	cfg.Redis.ApplyDefaults()
	runtimeConfig.Store(cfg)
}

// CurrentRuntimeConfig returns process-level ingest policy runtime options.
func CurrentRuntimeConfig() RuntimeConfig {
	if cfg, ok := runtimeConfig.Load().(RuntimeConfig); ok {
		cfg.Policy.ApplyDefaults()
		cfg.Redis.ApplyDefaults()
		return cfg
	}
	cfg := DefaultRuntimeConfig()
	cfg.Policy.ApplyDefaults()
	cfg.Redis.ApplyDefaults()
	return cfg
}
