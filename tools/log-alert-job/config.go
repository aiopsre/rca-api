package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultConfigPath       = "configs/log_alert_rules.yaml"
	defaultTickSeconds      = 60
	defaultWindowSeconds    = 300
	defaultCooldownSeconds  = 300
	defaultSamples          = 3
	defaultESRequestTimeout = 5000
	defaultRCARequestTimeMS = 5000
	defaultMetricsAddr      = "127.0.0.1:19558"
	defaultMaxDocsPerRule   = 1000
	defaultESMaxRetries     = 2

	ruleKindIngress5XX    = "ingress_5xx_spike"
	ruleKindIngressSlow   = "ingress_slow_spike"
	ruleKindMicrosvcError = "microsvc_error_cluster"
)

var (
	errNoESURLs      = errors.New("es.urls is required")
	errInvalidConfig = errors.New("invalid log-alert-job config")
)

type config struct {
	Job     jobConfig         `yaml:"job"`
	ES      esConfig          `yaml:"es"`
	RCA     rcaConfig         `yaml:"rca"`
	Fields  fieldsConfig      `yaml:"fields"`
	Indices map[string]string `yaml:"indices"`
	Rules   []ruleConfig      `yaml:"rules"`
}

type jobConfig struct {
	TickSeconds    int    `yaml:"tick_seconds"`
	MetricsAddr    string `yaml:"metrics_addr"`
	MaxDocsPerRule int    `yaml:"max_docs_per_rule"`
}

type esConfig struct {
	URLs       []string `yaml:"urls"`
	Username   string   `yaml:"username"`
	Password   string   `yaml:"password"`
	TimeoutMS  int      `yaml:"timeout_ms"`
	MaxRetries int      `yaml:"max_retries"`
}

type rcaConfig struct {
	BaseURL   string `yaml:"base_url"`
	TimeoutMS int    `yaml:"timeout_ms"`
}

type fieldsConfig struct {
	Timestamp string `yaml:"timestamp"`
	TraceID   string `yaml:"trace_id"`
	RequestID string `yaml:"request_id"`
	Msg       string `yaml:"msg"`
	Message   string `yaml:"message"`
}

type ruleConfig struct {
	ID              string             `yaml:"id"`
	Enabled         bool               `yaml:"enabled"`
	Kind            string             `yaml:"kind"`
	IndexRef        string             `yaml:"index_ref"`
	WindowSeconds   int                `yaml:"window_seconds"`
	CooldownSeconds int                `yaml:"cooldown_seconds"`
	Selector        selectorConfig     `yaml:"selector"`
	GroupBy         []string           `yaml:"group_by"`
	Trigger         triggerConfig      `yaml:"trigger"`
	Samples         int                `yaml:"samples"`
	RCAEvent        ruleRCAEventConfig `yaml:"rca_event"`
}

type selectorConfig struct {
	QueryString string `yaml:"query_string"`
}

type triggerConfig struct {
	Type  string `yaml:"type"`
	Value int    `yaml:"value"`
}

type ruleRCAEventConfig struct {
	Severity         string         `yaml:"severity"`
	FingerprintParts []string       `yaml:"fingerprint_parts"`
	SummaryTemplate  string         `yaml:"summary_template"`
	Hints            ruleHintConfig `yaml:"hints"`
}

type ruleHintConfig struct {
	IncludeESQuery bool `yaml:"include_es_query"`
}

type runtimeOptions struct {
	ConfigPath  string
	Once        bool
	MaxTicks    int
	TickSeconds int
	MetricsAddr string
}

func defaultRuntimeOptions() runtimeOptions {
	path := strings.TrimSpace(os.Getenv("CONFIG_PATH"))
	if path == "" {
		path = defaultConfigPath
	}
	return runtimeOptions{
		ConfigPath: path,
	}
}

func loadConfig(path string) (config, error) {
	cleanPath := strings.TrimSpace(path)
	if cleanPath == "" {
		cleanPath = defaultConfigPath
	}
	content, err := os.ReadFile(filepath.Clean(cleanPath))
	if err != nil {
		return config{}, fmt.Errorf("read config: %w", err)
	}

	cfg := config{}
	if err = yaml.Unmarshal(content, &cfg); err != nil {
		return config{}, fmt.Errorf("unmarshal config: %w", err)
	}

	if err = applyEnvOverrides(&cfg); err != nil {
		return config{}, err
	}
	applyDefaults(&cfg)
	if err = validateConfig(cfg); err != nil {
		return config{}, err
	}
	return cfg, nil
}

func applyEnvOverrides(cfg *config) error {
	overrideURLs := strings.TrimSpace(os.Getenv("ES_URLS"))
	if overrideURLs != "" {
		cfg.ES.URLs = splitCommaValues(overrideURLs)
	}

	if user := strings.TrimSpace(os.Getenv("ES_USER")); user != "" {
		cfg.ES.Username = user
	}
	if pass := strings.TrimSpace(os.Getenv("ES_PASS")); pass != "" {
		cfg.ES.Password = pass
	}

	if baseURL := strings.TrimSpace(os.Getenv("RCA_BASE_URL")); baseURL != "" {
		cfg.RCA.BaseURL = strings.TrimRight(baseURL, "/")
	}

	if rawTick := strings.TrimSpace(os.Getenv("JOB_TICK_SECONDS")); rawTick != "" {
		parsed, err := strconv.Atoi(rawTick)
		if err != nil {
			return fmt.Errorf("invalid JOB_TICK_SECONDS: %w", err)
		}
		cfg.Job.TickSeconds = parsed
	}

	if metricsAddr := strings.TrimSpace(os.Getenv("METRICS_ADDR")); metricsAddr != "" {
		cfg.Job.MetricsAddr = metricsAddr
	}

	return nil
}

//nolint:gocognit,gocyclo
func applyDefaults(cfg *config) {
	if cfg.Indices == nil {
		cfg.Indices = map[string]string{}
	}

	cfg.Job.TickSeconds = defaultInt(cfg.Job.TickSeconds, defaultTickSeconds)
	cfg.Job.MaxDocsPerRule = defaultInt(cfg.Job.MaxDocsPerRule, defaultMaxDocsPerRule)
	if strings.TrimSpace(cfg.Job.MetricsAddr) == "" {
		cfg.Job.MetricsAddr = defaultMetricsAddr
	}

	cfg.ES.TimeoutMS = defaultInt(cfg.ES.TimeoutMS, defaultESRequestTimeout)
	cfg.ES.MaxRetries = defaultInt(cfg.ES.MaxRetries, defaultESMaxRetries)
	cfg.RCA.TimeoutMS = defaultInt(cfg.RCA.TimeoutMS, defaultRCARequestTimeMS)
	if strings.TrimSpace(cfg.RCA.BaseURL) == "" {
		cfg.RCA.BaseURL = "http://127.0.0.1:5655"
	}

	if strings.TrimSpace(cfg.Fields.Timestamp) == "" {
		cfg.Fields.Timestamp = "@timestamp"
	}
	if strings.TrimSpace(cfg.Fields.TraceID) == "" {
		cfg.Fields.TraceID = "Trace.Id"
	}
	if strings.TrimSpace(cfg.Fields.RequestID) == "" {
		cfg.Fields.RequestID = "user_agent.request_id"
	}
	if strings.TrimSpace(cfg.Fields.Msg) == "" {
		cfg.Fields.Msg = "Msg"
	}
	if strings.TrimSpace(cfg.Fields.Message) == "" {
		cfg.Fields.Message = "message"
	}

	for idx := range cfg.Rules {
		rule := &cfg.Rules[idx]
		rule.ID = strings.TrimSpace(rule.ID)
		rule.Kind = normalizeRuleKind(rule.Kind)
		rule.IndexRef = strings.TrimSpace(rule.IndexRef)
		rule.WindowSeconds = defaultInt(rule.WindowSeconds, defaultWindowSeconds)
		rule.CooldownSeconds = defaultInt(rule.CooldownSeconds, defaultCooldownSeconds)
		rule.Trigger.Type = strings.TrimSpace(rule.Trigger.Type)
		rule.Trigger.Value = defaultInt(rule.Trigger.Value, 1)
		rule.Samples = defaultInt(rule.Samples, defaultSamples)
		rule.Selector.QueryString = strings.TrimSpace(rule.Selector.QueryString)
		rule.RCAEvent.Severity = strings.ToUpper(strings.TrimSpace(rule.RCAEvent.Severity))
		if rule.RCAEvent.Severity == "" {
			rule.RCAEvent.Severity = "P2"
		}
		if rule.Trigger.Type == "" {
			rule.Trigger.Type = "count_gte"
		}
	}
}

//nolint:gocognit,gocyclo
func validateConfig(cfg config) error {
	if len(cfg.ES.URLs) == 0 {
		return errNoESURLs
	}
	for _, rawURL := range cfg.ES.URLs {
		if strings.TrimSpace(rawURL) == "" {
			return configErr("es.urls contains empty url")
		}
	}
	if len(cfg.Rules) == 0 {
		return configErr("rules is required")
	}
	if cfg.Job.TickSeconds <= 0 {
		return configErr("job.tick_seconds must be > 0: %d", cfg.Job.TickSeconds)
	}
	if cfg.ES.TimeoutMS <= 0 {
		return configErr("es.timeout_ms must be > 0: %d", cfg.ES.TimeoutMS)
	}
	if cfg.RCA.TimeoutMS <= 0 {
		return configErr("rca.timeout_ms must be > 0: %d", cfg.RCA.TimeoutMS)
	}

	ruleIDs := make(map[string]struct{}, len(cfg.Rules))
	for _, rule := range cfg.Rules {
		if !rule.Enabled {
			continue
		}
		if rule.ID == "" {
			return configErr("enabled rule.id is required")
		}
		if _, exists := ruleIDs[rule.ID]; exists {
			return configErr("duplicate rule.id: %s", rule.ID)
		}
		ruleIDs[rule.ID] = struct{}{}

		if rule.Kind == "" {
			return configErr("rule.kind is required for rule=%s", rule.ID)
		}
		if !isSupportedRuleKind(rule.Kind) {
			return configErr("unsupported rule.kind=%s for rule=%s", rule.Kind, rule.ID)
		}
		if rule.IndexRef == "" {
			return configErr("rule.index_ref is required for rule=%s", rule.ID)
		}
		if _, ok := cfg.Indices[rule.IndexRef]; !ok {
			return configErr("rule.index_ref=%s not found in indices for rule=%s", rule.IndexRef, rule.ID)
		}
		if strings.TrimSpace(cfg.Indices[rule.IndexRef]) == "" {
			return configErr("indices.%s is empty", rule.IndexRef)
		}
		if rule.WindowSeconds <= 0 {
			return configErr("rule.window_seconds must be > 0 for rule=%s", rule.ID)
		}
		if rule.CooldownSeconds < 0 {
			return configErr("rule.cooldown_seconds must be >= 0 for rule=%s", rule.ID)
		}
		if len(rule.GroupBy) == 0 {
			return configErr("rule.group_by is required for rule=%s", rule.ID)
		}
		if !strings.EqualFold(rule.Trigger.Type, "count_gte") {
			return configErr("rule.trigger.type must be count_gte for rule=%s", rule.ID)
		}
		if rule.Trigger.Value <= 0 {
			return configErr("rule.trigger.value must be > 0 for rule=%s", rule.ID)
		}
		if rule.Samples <= 0 {
			return configErr("rule.samples must be > 0 for rule=%s", rule.ID)
		}
		if err := validateRuleQuery(rule); err != nil {
			return err
		}
		if err := validateRuleGroupBy(rule); err != nil {
			return err
		}
	}

	return nil
}

//nolint:gocognit,gocyclo
func validateRuleQuery(rule ruleConfig) error {
	query := strings.ToLower(strings.TrimSpace(rule.Selector.QueryString))
	if query == "" {
		return configErr("rule.selector.query_string is required for rule=%s", rule.ID)
	}

	switch rule.Kind {
	case ruleKindIngress5XX:
		mustContain := []string{
			"event.runenv:prod",
			"event.svctype:ingress",
			"http.response.status_code:[500 to 599]",
		}
		for _, fragment := range mustContain {
			if !strings.Contains(query, fragment) {
				return configErr("rule=%s query_string must contain %q", rule.ID, fragment)
			}
		}

	case ruleKindIngressSlow:
		mustContain := []string{
			"event.runenv:prod",
			"event.svctype:ingress",
			"nginx.upstream.response.time:[2 to *]",
		}
		for _, fragment := range mustContain {
			if !strings.Contains(query, fragment) {
				return configErr("rule=%s query_string must contain %q", rule.ID, fragment)
			}
		}

	case ruleKindMicrosvcError:
		if !strings.Contains(query, "level:error") {
			return configErr("rule=%s query_string must contain Level:ERROR", rule.ID)
		}
	}

	return nil
}

//nolint:gocognit,gocyclo
func validateRuleGroupBy(rule ruleConfig) error {
	groupBy := make(map[string]struct{}, len(rule.GroupBy))
	for _, field := range rule.GroupBy {
		groupBy[strings.TrimSpace(field)] = struct{}{}
	}

	switch rule.Kind {
	case ruleKindIngress5XX, ruleKindIngressSlow:
		required := []string{"destination.domain", "http.request.uri_path", "nginx.upstream.address"}
		for _, field := range required {
			if _, ok := groupBy[field]; !ok {
				return configErr("rule=%s group_by must contain %s", rule.ID, field)
			}
		}

	case ruleKindMicrosvcError:
		required := []string{"k8s.ns", "event.dataset", "msg_template_hash"}
		for _, field := range required {
			if _, ok := groupBy[field]; !ok {
				return configErr("rule=%s group_by must contain %s", rule.ID, field)
			}
		}
	}

	return nil
}

func normalizeRuleKind(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case "ingress_5xx", "ingress_5xx_spike":
		return ruleKindIngress5XX
	case "ingress_slow", "ingress_slow_spike":
		return ruleKindIngressSlow
	case ruleKindMicrosvcError:
		return ruleKindMicrosvcError
	default:
		return value
	}
}

func isSupportedRuleKind(kind string) bool {
	switch kind {
	case ruleKindIngress5XX, ruleKindIngressSlow, ruleKindMicrosvcError:
		return true
	default:
		return false
	}
}

func splitCommaValues(raw string) []string {
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		values = append(values, strings.TrimRight(trimmed, "/"))
	}
	return values
}

func defaultInt(value int, defaultValue int) int {
	if value <= 0 {
		return defaultValue
	}
	return value
}

func durationFromMillis(ms int) time.Duration {
	return time.Duration(ms) * time.Millisecond
}

func configErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errInvalidConfig, fmt.Sprintf(format, args...))
}
