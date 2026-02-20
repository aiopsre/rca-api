package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
)

var (
	errReadAlertingPolicyFile       = errors.New("read alerting policy file failed")
	errParseAlertingPolicyFile      = errors.New("parse alerting policy file failed")
	errUnsupportedAlertingPolicyVer = errors.New("unsupported alerting policy version")
)

// Load loads external alerting trigger policy with database-first precedence.
// Priority:
// 1. Active AlertingPolicy from database
// 2. YAML file at path (if provided)
// 3. Built-in default config
//
// Returned source indicates where the config was loaded from:
// - "dynamic_db": loaded from active database record
// - "file": loaded from external file
// - "default": using built-in default
//
// When strict=false, file read/parse/version errors return default config with non-nil err.
// When strict=true, file errors are still returned and should be treated as startup blocking.
// Database errors are logged but fall back to file/default (non-blocking).
func Load(ctx context.Context, st store.IStore, path string, strict bool) (PolicyConfig, string, error) {
	defaultCfg := DefaultPolicyConfig()

	// Priority 1: Try to load active policy from database
	activePolicy, err := st.AlertingPolicy().GetActive(ctx)
	if err == nil && activePolicy != nil && activePolicy.Active {
		cfg, parseErr := parseAlertingPolicyConfig(activePolicy.ConfigJSON)
		if parseErr != nil {
			// Log error but continue to fallback
			fmt.Printf("WARN: parse active database policy failed, falling back: %v\n", parseErr)
		} else {
			cfg.applyDefaults()
			return cfg, "dynamic_db", nil
		}
	}

	// Priority 2: Load from YAML file
	cleanPath := strings.TrimSpace(path)
	if cleanPath == "" {
		return defaultCfg, PolicyActiveSourceDefault, nil
	}

	raw, fileErr := os.ReadFile(cleanPath)
	if fileErr != nil {
		return fallback(defaultCfg, strict, fmt.Errorf("%w: path=%q err=%v", errReadAlertingPolicyFile, cleanPath, fileErr))
	}

	var cfg PolicyConfig
	if unmarshalErr := yaml.Unmarshal(raw, &cfg); unmarshalErr != nil {
		return fallback(defaultCfg, strict, fmt.Errorf("%w: path=%q err=%v", errParseAlertingPolicyFile, cleanPath, unmarshalErr))
	}

	if cfg.Version != PolicyVersion1 {
		return fallback(defaultCfg, strict, fmt.Errorf("%w: version=%d path=%q", errUnsupportedAlertingPolicyVer, cfg.Version, cleanPath))
	}

	cfg.applyDefaults()
	return cfg, PolicyActiveSourceFile, nil
}

// LoadFromYAML loads alerting policy from YAML file only (backward compatible).
//
// Returned source is "file" on successful file load, otherwise "default".
// When strict=false, any read/parse/version error returns default config with non-nil err.
// When strict=true, errors are still returned and should be treated as startup blocking.
func LoadFromYAML(path string, strict bool) (PolicyConfig, string, error) {
	defaultCfg := DefaultPolicyConfig()
	cleanPath := strings.TrimSpace(path)
	if cleanPath == "" {
		return defaultCfg, PolicyActiveSourceDefault, nil
	}

	raw, err := os.ReadFile(cleanPath)
	if err != nil {
		return fallback(defaultCfg, strict, fmt.Errorf("%w: path=%q err=%v", errReadAlertingPolicyFile, cleanPath, err))
	}

	var cfg PolicyConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return fallback(defaultCfg, strict, fmt.Errorf("%w: path=%q err=%v", errParseAlertingPolicyFile, cleanPath, err))
	}

	if cfg.Version != PolicyVersion1 {
		return fallback(defaultCfg, strict, fmt.Errorf("%w: version=%d path=%q", errUnsupportedAlertingPolicyVer, cfg.Version, cleanPath))
	}

	cfg.applyDefaults()
	return cfg, PolicyActiveSourceFile, nil
}

func fallback(defaultCfg PolicyConfig, _ bool, err error) (PolicyConfig, string, error) {
	defaultCfg.applyDefaults()
	return defaultCfg, PolicyActiveSourceDefault, err
}

// parseAlertingPolicyConfig parses JSON config string from database into PolicyConfig.
func parseAlertingPolicyConfig(configJSON string) (PolicyConfig, error) {
	var cfg PolicyConfig
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return cfg, fmt.Errorf("unmarshal alerting policy config: %w", err)
	}
	return cfg, nil
}

// LoadFromActivePolicy loads policy directly from an active database policy record.
// Returns error if policy is not active or parsing fails.
func LoadFromActivePolicy(policy *model.AlertingPolicyM) (PolicyConfig, error) {
	if policy == nil {
		return PolicyConfig{}, fmt.Errorf("policy is nil")
	}
	if !policy.Active {
		return PolicyConfig{}, fmt.Errorf("policy is not active")
	}

	cfg, err := parseAlertingPolicyConfig(policy.ConfigJSON)
	if err != nil {
		return cfg, err
	}
	cfg.applyDefaults()
	return cfg, nil
}
