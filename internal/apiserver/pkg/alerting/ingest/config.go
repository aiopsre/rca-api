package ingest

import (
	"sort"
	"strings"
	"sync/atomic"

	"github.com/aiopsre/rca-api/internal/apiserver/pkg/redisx"
)

const (
	// DefaultRedisKeyPrefix is the default key prefix for alert ingest policy short-state.
	DefaultRedisKeyPrefix = "rca:alert"
	// RolloutModeObserve writes alert events but does not progress incidents.
	RolloutModeObserve = "observe"
	// RolloutModeEnforce progresses incidents only when allow-list is matched.
	RolloutModeEnforce = "enforce"
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

// RolloutConfig controls adapter ingest rollout in trial environments.
type RolloutConfig struct {
	Enabled           bool     `json:"enabled" mapstructure:"enabled"`
	AllowedNamespaces []string `json:"allowed_namespaces" mapstructure:"allowed_namespaces"`
	AllowedServices   []string `json:"allowed_services" mapstructure:"allowed_services"`
	Mode              string   `json:"mode" mapstructure:"mode"`
}

// RuntimeConfig carries process-level runtime options used by policy pipeline builders.
type RuntimeConfig struct {
	Policy  PolicyConfig        `json:"policy" mapstructure:"policy"`
	Rollout RolloutConfig       `json:"rollout" mapstructure:"rollout"`
	Redis   redisx.RedisOptions `json:"redis" mapstructure:"redis"`
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

// DefaultRolloutConfig returns default rollout profile.
func DefaultRolloutConfig() RolloutConfig {
	return RolloutConfig{
		Enabled: false,
		Mode:    RolloutModeObserve,
	}
}

// DefaultRuntimeConfig returns process-level defaults.
func DefaultRuntimeConfig() RuntimeConfig {
	return RuntimeConfig{
		Policy:  DefaultPolicyConfig(),
		Rollout: DefaultRolloutConfig(),
		Redis:   redisx.NewRedisOptions(),
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

// ApplyDefaults normalizes rollout fields and applies defaults for empty values.
func (c *RolloutConfig) ApplyDefaults() {
	if c == nil {
		return
	}
	c.Mode = strings.ToLower(strings.TrimSpace(c.Mode))
	switch c.Mode {
	case RolloutModeObserve, RolloutModeEnforce:
	default:
		c.Mode = RolloutModeObserve
	}
	c.AllowedNamespaces = normalizeAllowList(c.AllowedNamespaces)
	c.AllowedServices = normalizeAllowList(c.AllowedServices)
}

// SetRuntimeConfig sets process-level ingest policy runtime options.
func SetRuntimeConfig(cfg RuntimeConfig) {
	cfg.Policy.ApplyDefaults()
	cfg.Rollout.ApplyDefaults()
	cfg.Redis.ApplyDefaults()
	runtimeConfig.Store(cfg)
}

// CurrentRuntimeConfig returns process-level ingest policy runtime options.
func CurrentRuntimeConfig() RuntimeConfig {
	if cfg, ok := runtimeConfig.Load().(RuntimeConfig); ok {
		cfg.Policy.ApplyDefaults()
		cfg.Rollout.ApplyDefaults()
		cfg.Redis.ApplyDefaults()
		return cfg
	}
	cfg := DefaultRuntimeConfig()
	cfg.Policy.ApplyDefaults()
	cfg.Rollout.ApplyDefaults()
	cfg.Redis.ApplyDefaults()
	return cfg
}

func normalizeAllowList(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, item := range values {
		v := strings.ToLower(strings.TrimSpace(item))
		if v == "" {
			continue
		}
		set[v] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
