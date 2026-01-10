package verification

import (
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	PlanVersionA5                = "a5"
	WarningTruncatedVerification = "TRUNCATED_VERIFICATION_PLAN"

	maxPlanBytes   = 4 * 1024
	maxSteps       = 3
	maxStepIDLen   = 32
	maxWhyLen      = 256
	maxQueryLen    = 512
	maxKeywordLen  = 128
	minQueryWindow = 10 * time.Minute
)

var (
	allowedExpectedTypes = map[string]struct{}{
		"exists":           {},
		"contains_keyword": {},
		"threshold_below":  {},
		"threshold_above":  {},
	}
	sensitiveWordRegex = regexp.MustCompile(`(?i)(secret|token|authorization|headers?)`)
)

// KBPattern holds one KB pattern token for verification rule generation fallback.
type KBPattern struct {
	Type  string
	Value string
}

// BuildInput defines stable, rule-based inputs for A5 verification plan generation.
type BuildInput struct {
	Executed     []string
	KBPatterns   []KBPattern
	Now          time.Time
	PrometheusID string
	LogsID       string
	DefaultID    string
}

// Plan defines A5 verification plan payload.
type Plan struct {
	Version  string   `json:"version"`
	Steps    []Step   `json:"steps"`
	Warnings []string `json:"warnings,omitempty"`
}

// Step defines one re-check action.
type Step struct {
	ID       string         `json:"id"`
	Tool     string         `json:"tool"`
	Why      string         `json:"why"`
	Params   map[string]any `json:"params"`
	Expected Expected       `json:"expected"`
}

// Expected defines lightweight expected shape for one verification step.
type Expected struct {
	Type    string  `json:"type"`
	Field   string  `json:"field,omitempty"`
	Value   float64 `json:"value,omitempty"`
	Keyword string  `json:"keyword,omitempty"`
}

// BuildPlan generates a deterministic A5 verification plan.
//
//nolint:gocognit,gocyclo,wsl_v5 // Rule-based branching keeps deterministic tool/expected selection explicit.
func BuildPlan(input BuildInput) *Plan {
	now := input.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	start := now.Add(-minQueryWindow).Format(time.RFC3339)
	end := now.Format(time.RFC3339)

	plan := Plan{
		Version: PlanVersionA5,
		Steps:   make([]Step, 0, maxSteps),
	}

	metricsID := strings.TrimSpace(input.PrometheusID)
	logsID := strings.TrimSpace(input.LogsID)
	defaultID := strings.TrimSpace(input.DefaultID)

	if metricsID == "" {
		metricsID = defaultID
	}
	if logsID == "" {
		logsID = defaultID
	}

	_, signal := detectPrimarySignal(input.Executed, input.KBPatterns)
	if metricsID != "" {
		plan.Steps = append(plan.Steps, newMetricsStep(metricsID, signal, start, end))
	} else if logsID != "" {
		plan.Steps = append(plan.Steps, newLogsStep(logsID, signal, start, end))
	}

	if len(plan.Steps) == 0 {
		return nil
	}

	normalized := normalizePlan(plan)
	if len(normalized.Steps) == 0 {
		return nil
	}
	return &normalized
}

func newMetricsStep(datasourceID string, signal string, start string, end string) Step {
	expr := `sum(rate(http_requests_total{status=~"5.."}[5m]))`
	why := "Re-check metrics signal against the same window to verify reproducibility."
	if strings.Contains(signal, "latency") {
		expr = `histogram_quantile(0.99,sum(rate(http_request_duration_seconds_bucket[5m])) by (le))`
		why = "Re-check latency metric trend in the same window to confirm repeatability."
	}

	return Step{
		ID:   "recheck-metrics-1",
		Tool: "mcp.query_metrics",
		Why:  why,
		Params: map[string]any{
			"datasource_id":    datasourceID,
			"expr":             expr,
			"time_range_start": start,
			"time_range_end":   end,
			"step_seconds":     30,
		},
		Expected: Expected{
			Type: "exists",
		},
	}
}

func newLogsStep(datasourceID string, signal string, start string, end string) Step {
	keyword := "error"
	if strings.TrimSpace(signal) != "" {
		keyword = strings.ToLower(strings.TrimSpace(signal))
	}
	query := `{job=~".+"} |= "` + keyword + `"`
	return Step{
		ID:   "recheck-logs-1",
		Tool: "mcp.query_logs",
		Why:  "Re-check logs using the same keyword pattern to validate reproducibility.",
		Params: map[string]any{
			"datasource_id":    datasourceID,
			"query":            query,
			"time_range_start": start,
			"time_range_end":   end,
			"limit":            100,
		},
		Expected: Expected{
			Type:    "contains_keyword",
			Keyword: keyword,
		},
	}
}

func detectPrimarySignal(executed []string, patterns []KBPattern) (string, string) {
	for _, item := range executed {
		value := strings.ToLower(strings.TrimSpace(item))
		switch {
		case strings.HasPrefix(value, "query_metrics:"):
			return "metrics", strings.TrimPrefix(value, "query_metrics:")
		case strings.HasPrefix(value, "query_logs:"):
			return "logs", strings.TrimPrefix(value, "query_logs:")
		}
	}

	for _, item := range patterns {
		pType := strings.ToLower(strings.TrimSpace(item.Type))
		pValue := strings.ToLower(strings.TrimSpace(item.Value))
		switch {
		case strings.Contains(pType, "metric"), strings.Contains(pType, "threshold"), strings.Contains(pValue, "5xx"), strings.Contains(pValue, "latency"):
			return "metrics", pValue
		case strings.Contains(pType, "log"), strings.Contains(pType, "keyword"), strings.Contains(pValue, "error"):
			return "logs", pValue
		}
	}

	return "metrics", ""
}

func normalizePlan(in Plan) Plan {
	out := Plan{
		Version:  PlanVersionA5,
		Steps:    make([]Step, 0, maxSteps),
		Warnings: append([]string(nil), in.Warnings...),
	}

	for _, step := range in.Steps {
		normalized, ok := normalizeStep(step)
		if !ok {
			continue
		}
		out.Steps = append(out.Steps, normalized)
		if len(out.Steps) >= maxSteps {
			break
		}
	}
	if len(out.Steps) == 0 {
		return out
	}
	return enforcePlanSize(out)
}

func normalizeStep(in Step) (Step, bool) {
	step := Step{
		ID:     truncate(cleanString(in.ID), maxStepIDLen),
		Tool:   normalizeTool(in.Tool),
		Why:    truncate(cleanString(in.Why), maxWhyLen),
		Params: normalizeParams(in.Tool, in.Params),
	}
	if step.ID == "" || step.Tool == "" {
		return Step{}, false
	}
	if step.Why == "" {
		step.Why = "Re-check with deterministic query parameters."
	}
	if len(step.Params) == 0 {
		return Step{}, false
	}
	step.Expected = normalizeExpected(in.Tool, in.Expected)
	return step, true
}

func normalizeTool(tool string) string {
	value := strings.ToLower(strings.TrimSpace(tool))
	switch value {
	case "mcp.query_metrics", "query_metrics":
		return "mcp.query_metrics"
	case "mcp.query_logs", "query_logs":
		return "mcp.query_logs"
	default:
		return ""
	}
}

//nolint:gocognit,gocyclo,wsl_v5 // Param normalization keeps explicit whitelist/type coercion for safety.
func normalizeParams(tool string, in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}

	allowed := allowedParamsForTool(normalizeTool(tool))
	if len(allowed) == 0 {
		return nil
	}

	out := make(map[string]any, len(allowed))
	keys := make([]string, 0, len(in))
	for key := range in {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		normalizedKey := strings.ToLower(strings.TrimSpace(key))
		if _, ok := allowed[normalizedKey]; !ok {
			continue
		}
		if sensitiveWordRegex.MatchString(normalizedKey) {
			continue
		}
		value := in[key]
		switch normalizedKey {
		case "datasource_id", "time_range_start", "time_range_end":
			v := cleanString(anyToString(value))
			if v == "" {
				continue
			}
			out[normalizedKey] = v
		case "expr", "query":
			v := truncate(cleanString(anyToString(value)), maxQueryLen)
			if v == "" {
				continue
			}
			out[normalizedKey] = v
		case "step_seconds", "limit":
			n := int64(anyToFloat64(value))
			if n <= 0 {
				if normalizedKey == "step_seconds" {
					n = 30
				} else {
					n = 100
				}
			}
			out[normalizedKey] = n
		case "query_json":
			if obj, ok := value.(map[string]any); ok {
				if raw, err := json.Marshal(obj); err == nil && len(raw) <= 512 {
					out[normalizedKey] = obj
				}
			}
		}
	}
	return out
}

func normalizeExpected(tool string, in Expected) Expected {
	normalizedType := strings.ToLower(strings.TrimSpace(in.Type))
	if _, ok := allowedExpectedTypes[normalizedType]; !ok {
		normalizedType = "exists"
	}

	switch normalizedType {
	case "contains_keyword":
		keyword := truncate(cleanString(in.Keyword), maxKeywordLen)
		if keyword == "" {
			keyword = "error"
		}
		return Expected{
			Type:    normalizedType,
			Keyword: keyword,
		}

	case "threshold_above", "threshold_below":
		value := in.Value
		if math.IsNaN(value) || math.IsInf(value, 0) {
			value = 0
		}
		return Expected{
			Type:  normalizedType,
			Field: firstNonEmpty(truncate(cleanString(in.Field), 64), "value"),
			Value: value,
		}

	default:
		if normalizeTool(tool) == "mcp.query_logs" {
			return Expected{Type: "contains_keyword", Keyword: "error"}
		}
		return Expected{Type: "exists"}
	}
}

//nolint:gocognit,gocyclo // Size trimming keeps explicit staged truncation semantics.
func enforcePlanSize(in Plan) Plan {
	out := in
	if planSize(out) <= maxPlanBytes {
		return out
	}

	out.Warnings = appendWarning(out.Warnings, WarningTruncatedVerification)

	for len(out.Steps) > 1 && planSize(out) > maxPlanBytes {
		out.Steps = out.Steps[:len(out.Steps)-1]
	}
	if len(out.Steps) == 0 {
		return out
	}

	for planSize(out) > maxPlanBytes {
		step := out.Steps[0]
		shortened := false
		if utf8.RuneCountInString(step.Why) > 96 {
			step.Why = truncate(step.Why, 96)
			shortened = true
		}
		if query, ok := step.Params["query"].(string); ok && utf8.RuneCountInString(query) > 160 {
			step.Params["query"] = truncate(query, 160)
			shortened = true
		}
		if expr, ok := step.Params["expr"].(string); ok && utf8.RuneCountInString(expr) > 160 {
			step.Params["expr"] = truncate(expr, 160)
			shortened = true
		}
		out.Steps[0] = step
		if !shortened {
			break
		}
	}
	return out
}

func planSize(plan Plan) int {
	raw, err := json.Marshal(plan)
	if err != nil {
		return maxPlanBytes + 1
	}
	return len(raw)
}

func appendWarning(in []string, warning string) []string {
	for _, item := range in {
		if strings.TrimSpace(item) == warning {
			return in
		}
	}
	return append(in, warning)
}

func cleanString(in string) string {
	trimmed := strings.TrimSpace(in)
	if trimmed == "" {
		return ""
	}
	out := strings.Join(strings.Fields(trimmed), " ")
	return sensitiveWordRegex.ReplaceAllString(out, "[redacted]")
}

func truncate(in string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if utf8.RuneCountInString(in) <= maxLen {
		return in
	}

	runes := []rune(in)
	return string(runes[:maxLen])
}

func anyToString(in any) string {
	switch v := in.(type) {
	case string:
		return v
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return strings.Trim(string(raw), `"`)
	}
}

func anyToFloat64(in any) float64 {
	switch v := in.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case uint:
		return float64(v)
	case uint64:
		return float64(v)
	default:
		return 0
	}
}

func firstNonEmpty(values ...string) string {
	for _, item := range values {
		if strings.TrimSpace(item) != "" {
			return item
		}
	}
	return ""
}

func allowedParamsForTool(tool string) map[string]struct{} {
	switch tool {
	case "mcp.query_metrics":
		return map[string]struct{}{
			"datasource_id":    {},
			"expr":             {},
			"time_range_start": {},
			"time_range_end":   {},
			"step_seconds":     {},
		}

	case "mcp.query_logs":
		return map[string]struct{}{
			"datasource_id":    {},
			"query":            {},
			"time_range_start": {},
			"time_range_end":   {},
			"limit":            {},
			"query_json":       {},
		}

	default:
		return nil
	}
}
