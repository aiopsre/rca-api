package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

const (
	bootstrapPolicyName      = "system-default"
	bootstrapPolicyLineageID = "bootstrap-alerting-policy-default"
	bootstrapPolicyCreatedBy = "system:bootstrap"
)

// Load loads runtime alerting policy with dynamic configuration precedence.
// Priority:
// 1. Active AlertingPolicy from database
// 2. Built-in default config when the database does not yet have an active policy
func Load(ctx context.Context, st store.IStore) (PolicyConfig, string, error) {
	defaultCfg := DefaultPolicyConfig()
	activePolicy, source, err := ensureActivePolicy(ctx, st)
	if err != nil {
		defaultCfg.applyDefaults()
		return defaultCfg, PolicyActiveSourceDefault, err
	}
	if activePolicy == nil {
		defaultCfg.applyDefaults()
		return defaultCfg, PolicyActiveSourceDefault, nil
	}

	cfg, err := LoadFromActivePolicy(activePolicy)
	if err != nil {
		defaultCfg.applyDefaults()
		return defaultCfg, PolicyActiveSourceDefault, err
	}
	return cfg, source, nil
}

// SyncRuntimeConfig reloads process-local runtime config from the active database policy.
// When no active policy exists, it falls back to the built-in default config.
func SyncRuntimeConfig(ctx context.Context, st store.IStore) error {
	policyCfg, activeSource, err := Load(ctx, st)
	source := RuleSourceDefault
	if activeSource == PolicyActiveSourceDynamicDB {
		source = RuleSourceDynamicDB
	}
	SetRuntimeConfig(RuntimeConfig{
		Policy:       policyCfg,
		Source:       source,
		ActiveSource: activeSource,
	})
	return err
}

func ensureActivePolicy(ctx context.Context, st store.IStore) (*model.AlertingPolicyM, string, error) {
	if st == nil || st.AlertingPolicy() == nil {
		return nil, PolicyActiveSourceDefault, nil
	}

	activePolicy, err := st.AlertingPolicy().GetActive(ctx)
	if err == nil && activePolicy != nil && activePolicy.Active {
		return activePolicy, PolicyActiveSourceDynamicDB, nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, PolicyActiveSourceDefault, err
	}

	total, _, err := st.AlertingPolicy().List(ctx, where.T(ctx).O(0).L(1))
	if err != nil {
		return nil, PolicyActiveSourceDefault, err
	}
	if total > 0 {
		return nil, PolicyActiveSourceDefault, nil
	}

	rawConfig, err := json.Marshal(DefaultPolicyConfig())
	if err != nil {
		return nil, PolicyActiveSourceDefault, fmt.Errorf("marshal default alerting policy: %w", err)
	}

	description := "Auto-seeded built-in alerting policy. Manage later versions via HTTP API."
	now := time.Now().UTC()
	obj := &model.AlertingPolicyM{
		Name:        bootstrapPolicyName,
		Description: &description,
		LineageID:   bootstrapPolicyLineageID,
		Version:     1,
		ConfigJSON:  string(rawConfig),
		Active:      true,
		ActivatedAt: &now,
		ActivatedBy: strPtr(bootstrapPolicyCreatedBy),
		CreatedBy:   bootstrapPolicyCreatedBy,
		UpdatedBy:   strPtr(bootstrapPolicyCreatedBy),
	}
	if err := st.AlertingPolicy().Create(ctx, obj); err != nil {
		return nil, PolicyActiveSourceDefault, err
	}
	return obj, PolicyActiveSourceDynamicDB, nil
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
