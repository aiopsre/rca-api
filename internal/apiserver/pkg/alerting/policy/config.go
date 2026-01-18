package policy

import (
	"strings"
	"sync/atomic"
)

const (
	// PolicyVersion1 is the supported external policy schema version.
	PolicyVersion1 = 1

	// RuleSourceCLI indicates policy path comes from CLI.
	RuleSourceCLI = "cli"
	// RuleSourceYAML indicates policy path comes from rca-apiserver.yaml.
	RuleSourceYAML = "yaml"
	// RuleSourceDefault indicates no external policy path is provided.
	RuleSourceDefault = "default"

	// PolicyActiveSourceDefault indicates runtime policy is built-in default.
	PolicyActiveSourceDefault = "default"
	// PolicyActiveSourceFile indicates runtime policy is loaded from external file.
	PolicyActiveSourceFile = "file"
)

const (
	defaultRuleName = "default"
	defaultPipeline = "basic_rca"
)

var runtimeConfig atomic.Value

// ExternalPolicyOptions configures external alerting policy file loading.
type ExternalPolicyOptions struct {
	Enabled bool   `json:"enabled" yaml:"enabled" mapstructure:"enabled"`
	Path    string `json:"path" yaml:"path" mapstructure:"path"`
	Strict  bool   `json:"strict" yaml:"strict" mapstructure:"strict"`

	// PathSetByCLI marks whether --alerting-policy-path is explicitly set.
	PathSetByCLI bool `json:"-" mapstructure:"-"`
	// StrictSetByCLI marks whether --alerting-policy-strict is explicitly set.
	StrictSetByCLI bool `json:"-" mapstructure:"-"`
}

// LoadInput contains resolved policy loading inputs after precedence resolution.
type LoadInput struct {
	Path   string
	Strict bool
	Source string
}

// RuntimeConfig carries process-level alerting trigger policy config.
type RuntimeConfig struct {
	Policy       PolicyConfig `json:"policy" yaml:"policy" mapstructure:"policy"`
	PolicyPath   string       `json:"policy_path" yaml:"policy_path" mapstructure:"policy_path"`
	Strict       bool         `json:"strict" yaml:"strict" mapstructure:"strict"`
	Source       string       `json:"source" yaml:"source" mapstructure:"source"`                      // cli|yaml|default
	ActiveSource string       `json:"active_source" yaml:"active_source" mapstructure:"active_source"` // file|default
}

// PolicyConfig describes external alerting trigger policy schema.
type PolicyConfig struct {
	Version  int            `json:"version" yaml:"version" mapstructure:"version"`
	Defaults PolicyDefaults `json:"defaults" yaml:"defaults" mapstructure:"defaults"`
	Triggers PolicyTriggers `json:"triggers" yaml:"triggers" mapstructure:"triggers"`
}

// PolicyDefaults keeps per-trigger default enabled states.
type PolicyDefaults struct {
	OnIngest     TriggerDefaults `json:"on_ingest" yaml:"on_ingest" mapstructure:"on_ingest"`
	OnEscalation TriggerDefaults `json:"on_escalation" yaml:"on_escalation" mapstructure:"on_escalation"`
	Scheduled    TriggerDefaults `json:"scheduled" yaml:"scheduled" mapstructure:"scheduled"`
}

// TriggerDefaults defines default switch for one trigger class.
type TriggerDefaults struct {
	Enabled bool `json:"enabled" yaml:"enabled" mapstructure:"enabled"`
}

// PolicyTriggers groups trigger-specific rule sets.
type PolicyTriggers struct {
	OnIngest     TriggerRules `json:"on_ingest" yaml:"on_ingest" mapstructure:"on_ingest"`
	OnEscalation TriggerRules `json:"on_escalation" yaml:"on_escalation" mapstructure:"on_escalation"`
	Scheduled    TriggerRules `json:"scheduled" yaml:"scheduled" mapstructure:"scheduled"`
}

// TriggerRules contains rules for one trigger type.
type TriggerRules struct {
	Rules []TriggerRule `json:"rules" yaml:"rules" mapstructure:"rules"`
}

// TriggerRule defines a single match->action rule.
type TriggerRule struct {
	Name   string        `json:"name" yaml:"name" mapstructure:"name"`
	Match  RuleMatch     `json:"match" yaml:"match" mapstructure:"match"`
	Action TriggerAction `json:"action" yaml:"action" mapstructure:"action"`
}

// RuleMatch is the minimal matcher set for trigger policy.
type RuleMatch struct {
	AlertName        string              `json:"alert_name" yaml:"alert_name" mapstructure:"alert_name"`
	AlertNameRegex   string              `json:"alert_name_regex" yaml:"alert_name_regex" mapstructure:"alert_name_regex"`
	Labels           map[string][]string `json:"labels" yaml:"labels" mapstructure:"labels"`
	IncidentSeverity []string            `json:"incident_severity" yaml:"incident_severity" mapstructure:"incident_severity"`
}

// TriggerAction describes allowed trigger behavior parameters.
type TriggerAction struct {
	Run                      bool   `json:"run" yaml:"run" mapstructure:"run"`
	Pipeline                 string `json:"pipeline" yaml:"pipeline" mapstructure:"pipeline"`
	CooldownSeconds          int    `json:"cooldown_seconds" yaml:"cooldown_seconds" mapstructure:"cooldown_seconds"`
	DedupWindowSeconds       int    `json:"dedup_window_seconds" yaml:"dedup_window_seconds" mapstructure:"dedup_window_seconds"`
	IdempotencyBucketSeconds int    `json:"idempotency_bucket_seconds" yaml:"idempotency_bucket_seconds" mapstructure:"idempotency_bucket_seconds"`
	OncePerStage             bool   `json:"once_per_stage" yaml:"once_per_stage" mapstructure:"once_per_stage"`
}

// DefaultExternalPolicyOptions returns fail-open defaults with no external file.
func DefaultExternalPolicyOptions() ExternalPolicyOptions {
	return ExternalPolicyOptions{
		Enabled: false,
		Path:    "",
		Strict:  false,
	}
}

// ApplyDefaults normalizes external policy options.
func (o *ExternalPolicyOptions) ApplyDefaults() {
	if o == nil {
		return
	}
	o.Path = strings.TrimSpace(o.Path)
}

// ResolveLoadInput resolves policy file load path/source with precedence:
// CLI --alerting-policy-path > rca-apiserver.yaml alerting_policy.path > default(no file).
func ResolveLoadInput(opts ExternalPolicyOptions) LoadInput {
	opts.ApplyDefaults()
	input := LoadInput{
		Path:   "",
		Strict: opts.Strict,
		Source: RuleSourceDefault,
	}

	if opts.PathSetByCLI {
		input.Path = opts.Path
		if input.Path != "" {
			input.Source = RuleSourceCLI
		}
		return input
	}

	if opts.Enabled && opts.Path != "" {
		input.Path = opts.Path
		input.Source = RuleSourceYAML
	}
	return input
}

// DefaultPolicyConfig returns built-in default alerting trigger policy.
func DefaultPolicyConfig() PolicyConfig {
	return PolicyConfig{
		Version: PolicyVersion1,
		Defaults: PolicyDefaults{
			OnIngest:     TriggerDefaults{Enabled: false},
			OnEscalation: TriggerDefaults{Enabled: false},
			Scheduled:    TriggerDefaults{Enabled: false},
		},
		Triggers: PolicyTriggers{
			OnIngest:     TriggerRules{Rules: []TriggerRule{defaultDisabledRule()}},
			OnEscalation: TriggerRules{Rules: []TriggerRule{defaultDisabledRule()}},
			Scheduled:    TriggerRules{Rules: []TriggerRule{defaultDisabledRule()}},
		},
	}
}

// DefaultRuntimeConfig returns the process-level default policy runtime config.
func DefaultRuntimeConfig() RuntimeConfig {
	return RuntimeConfig{
		Policy:       DefaultPolicyConfig(),
		Source:       RuleSourceDefault,
		ActiveSource: PolicyActiveSourceDefault,
	}
}

// SetRuntimeConfig sets process-level external alerting trigger policy runtime config.
func SetRuntimeConfig(cfg RuntimeConfig) {
	cfg.Policy.applyDefaults()
	cfg.PolicyPath = strings.TrimSpace(cfg.PolicyPath)
	cfg.Source = normalizeRuleSource(cfg.Source)
	cfg.ActiveSource = normalizeActiveSource(cfg.ActiveSource)
	runtimeConfig.Store(cfg)
}

// CurrentRuntimeConfig returns process-level external alerting trigger policy runtime config.
func CurrentRuntimeConfig() RuntimeConfig {
	if cfg, ok := runtimeConfig.Load().(RuntimeConfig); ok {
		cfg.Policy.applyDefaults()
		cfg.PolicyPath = strings.TrimSpace(cfg.PolicyPath)
		cfg.Source = normalizeRuleSource(cfg.Source)
		cfg.ActiveSource = normalizeActiveSource(cfg.ActiveSource)
		return cfg
	}
	cfg := DefaultRuntimeConfig()
	cfg.Policy.applyDefaults()
	return cfg
}

func defaultDisabledRule() TriggerRule {
	return TriggerRule{
		Name:  defaultRuleName,
		Match: RuleMatch{},
		Action: TriggerAction{
			Run:      false,
			Pipeline: defaultPipeline,
		},
	}
}

func (c *PolicyConfig) applyDefaults() {
	if c == nil {
		return
	}
	if c.Version <= 0 {
		c.Version = PolicyVersion1
	}
	applyRuleDefaults(&c.Triggers.OnIngest)
	applyRuleDefaults(&c.Triggers.OnEscalation)
	applyRuleDefaults(&c.Triggers.Scheduled)
}

func applyRuleDefaults(rules *TriggerRules) {
	if rules == nil {
		return
	}
	if len(rules.Rules) == 0 {
		rules.Rules = []TriggerRule{defaultDisabledRule()}
		return
	}
	for i := range rules.Rules {
		normalizeRule(&rules.Rules[i])
	}
}

func normalizeRule(rule *TriggerRule) {
	if rule == nil {
		return
	}
	rule.Name = strings.TrimSpace(rule.Name)
	if rule.Name == "" {
		rule.Name = defaultRuleName
	}

	rule.Match.AlertName = strings.TrimSpace(rule.Match.AlertName)
	rule.Match.AlertNameRegex = strings.TrimSpace(rule.Match.AlertNameRegex)
	rule.Match.Labels = normalizeMatchLabels(rule.Match.Labels)
	rule.Match.IncidentSeverity = normalizeStringList(rule.Match.IncidentSeverity)

	rule.Action.Pipeline = strings.TrimSpace(rule.Action.Pipeline)
	if rule.Action.Pipeline == "" {
		rule.Action.Pipeline = defaultPipeline
	}
	if rule.Action.CooldownSeconds < 0 {
		rule.Action.CooldownSeconds = 0
	}
	if rule.Action.DedupWindowSeconds < 0 {
		rule.Action.DedupWindowSeconds = 0
	}
	if rule.Action.IdempotencyBucketSeconds < 0 {
		rule.Action.IdempotencyBucketSeconds = 0
	}
}

func normalizeMatchLabels(labels map[string][]string) map[string][]string {
	if len(labels) == 0 {
		return nil
	}
	out := make(map[string][]string, len(labels))
	for key, values := range labels {
		cleanKey := strings.TrimSpace(key)
		if cleanKey == "" {
			continue
		}
		cleanValues := normalizeStringList(values)
		if len(cleanValues) == 0 {
			continue
		}
		out[cleanKey] = cleanValues
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeStringList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		clean := strings.TrimSpace(value)
		if clean == "" {
			continue
		}
		out = append(out, clean)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeRuleSource(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case RuleSourceCLI:
		return RuleSourceCLI
	case RuleSourceYAML:
		return RuleSourceYAML
	default:
		return RuleSourceDefault
	}
}

func normalizeActiveSource(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case PolicyActiveSourceFile:
		return PolicyActiveSourceFile
	default:
		return PolicyActiveSourceDefault
	}
}
