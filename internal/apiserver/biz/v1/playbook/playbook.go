package playbook

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	// VersionT6 is the playbook payload version persisted under diagnosis_json.playbook.
	VersionT6 = "t6"

	// StrategyRootCauseType selects rules matched by root_cause_type.
	StrategyRootCauseType = "root_cause_type"
	// StrategyPatternMatch selects rules matched by diagnosis patterns.
	StrategyPatternMatch = "pattern_match"
	// StrategyFallback selects default generic guidance when no rule is matched.
	StrategyFallback = "fallback"

	maxPlaybookItems       = 10
	maxStepTextLen         = 256
	maxExpectedOutcomeLen  = 256
	maxPlaybookTitleLen    = 128
	maxPlaybookRationale   = 256
	maxPlaybookItemIDLen   = 64
	envPlaybookConfig      = "RCA_PLAYBOOK_CONFIG"
	fallbackWarningRuleCfg = "PLAYBOOK_RULES_LOAD_FALLBACK"
)

var sensitiveWordRegex = regexp.MustCompile(`(?i)(secret|token|authorization|headers?)`)

var errInvalidDiagnosisJSON = errors.New("invalid diagnosis_json payload")

// BuildInput defines normalized inputs required for T6 playbook generation.
type BuildInput struct {
	DiagnosisJSON string
	RootCauseType string
}

// Playbook is written into incident diagnosis_json.playbook.
type Playbook struct {
	Version  string   `json:"version"`
	Strategy string   `json:"strategy"`
	Items    []Item   `json:"items"`
	Warnings []string `json:"warnings,omitempty"`
}

// Item defines one suggestion card in the generated playbook.
type Item struct {
	ID           string       `json:"id,omitempty"`
	Title        string       `json:"title"`
	Risk         string       `json:"risk"`
	Rationale    string       `json:"rationale"`
	Steps        []Step       `json:"steps"`
	Verification Verification `json:"verification"`
}

// Step defines one human-oriented playbook action/check item.
type Step struct {
	Type          string `json:"type"`
	Text          string `json:"text"`
	RequiresHuman bool   `json:"requires_human,omitempty"`
}

// Verification links one playbook item to A5 verification_plan steps.
type Verification struct {
	UseVerificationPlan bool   `json:"use_verification_plan"`
	RecommendedSteps    []int  `json:"recommended_steps"`
	ExpectedOutcome     string `json:"expected_outcome"`
}

type ruleDocument struct {
	Version  string       `yaml:"version"`
	Rules    []rule       `yaml:"rules"`
	Fallback fallbackRule `yaml:"fallback"`
}

type fallbackRule struct {
	Items []ruleItem `yaml:"items"`
}

type rule struct {
	ID    string     `yaml:"id"`
	Match ruleMatch  `yaml:"match"`
	Items []ruleItem `yaml:"items"`
}

type ruleMatch struct {
	RootCauseTypes  []string `yaml:"root_cause_types"`
	PatternsContain []string `yaml:"patterns_contains"`
}

type ruleItem struct {
	ID           string           `yaml:"id"`
	Title        string           `yaml:"title"`
	Risk         string           `yaml:"risk"`
	Rationale    string           `yaml:"rationale"`
	Steps        []ruleStep       `yaml:"steps"`
	Verification ruleVerification `yaml:"verification"`
}

type ruleStep struct {
	Type          string `yaml:"type"`
	Text          string `yaml:"text"`
	RequiresHuman bool   `yaml:"requires_human"`
}

type ruleVerification struct {
	RecommendedSteps []int  `yaml:"recommended_steps"`
	ExpectedOutcome  string `yaml:"expected_outcome"`
}

type ruleLoader struct {
	once     sync.Once
	doc      ruleDocument
	warnings []string
}

var defaultRuleLoader = &ruleLoader{}

// Build generates one T6 playbook payload from diagnosis_json and rule config.
func Build(input BuildInput) (*Playbook, bool, error) {
	rules, warnings := defaultRuleLoader.load()
	playbook, ok, err := buildFromRuleDocument(input, rules, warnings)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return &Playbook{}, false, nil
	}
	return &playbook, true, nil
}

func (l *ruleLoader) load() (ruleDocument, []string) {
	l.once.Do(func() {
		doc, err := loadRuleDocument()
		if err != nil {
			l.doc = defaultRuleDocument()
			l.warnings = appendWarning(l.warnings, fallbackWarningRuleCfg)
			return
		}
		l.doc = normalizeRuleDocument(doc)
	})
	warnings := append([]string(nil), l.warnings...)
	return l.doc, warnings
}

func loadRuleDocument() (ruleDocument, error) {
	source, isDir := resolveRuleSource()
	if source == "" {
		return normalizeRuleDocument(defaultRuleDocument()), nil
	}
	if isDir {
		return loadRuleDocumentFromDir(source)
	}
	return loadRuleDocumentFromFile(source)
}

//nolint:gocognit // Source resolution keeps explicit env/file/dir fallback order.
func resolveRuleSource() (string, bool) {
	override := strings.TrimSpace(os.Getenv(envPlaybookConfig))
	if override != "" {
		if info, err := os.Stat(override); err == nil {
			return override, info.IsDir()
		}
		return "", false
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", false
	}

	dir := cwd
	for range 10 {
		filePath := filepath.Join(dir, "configs", "playbooks.yaml")
		if isRegularFile(filePath) {
			return filePath, false
		}

		dirPath := filepath.Join(dir, "configs", "playbooks")
		if isDirectory(dirPath) {
			return dirPath, true
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return "", false
}

func loadRuleDocumentFromFile(path string) (ruleDocument, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ruleDocument{}, err
	}
	var doc ruleDocument
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return ruleDocument{}, err
	}
	return normalizeRuleDocument(doc), nil
}

//nolint:gocognit // Merge keeps explicit file filtering + parse handling for deterministic ordering.
func loadRuleDocumentFromDir(path string) (ruleDocument, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return ruleDocument{}, err
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(entry.Name()))
		if strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)

	merged := ruleDocument{}
	for _, name := range names {
		raw, readErr := os.ReadFile(filepath.Join(path, name))
		if readErr != nil {
			return ruleDocument{}, readErr
		}
		part := ruleDocument{}
		if unmarshalErr := yaml.Unmarshal(raw, &part); unmarshalErr != nil {
			return ruleDocument{}, unmarshalErr
		}
		if merged.Version == "" {
			merged.Version = strings.TrimSpace(part.Version)
		}
		merged.Rules = append(merged.Rules, part.Rules...)
		merged.Fallback.Items = append(merged.Fallback.Items, part.Fallback.Items...)
	}

	return normalizeRuleDocument(merged), nil
}

func normalizeRuleDocument(doc ruleDocument) ruleDocument {
	defaults := defaultRuleDocument()
	if strings.TrimSpace(doc.Version) == "" {
		doc.Version = defaults.Version
	}
	doc.Version = strings.ToLower(strings.TrimSpace(doc.Version))
	if doc.Version == "" {
		doc.Version = VersionT6
	}

	for i := range doc.Rules {
		doc.Rules[i].ID = strings.TrimSpace(doc.Rules[i].ID)
		doc.Rules[i].Match.RootCauseTypes = normalizeLowerStringSlice(doc.Rules[i].Match.RootCauseTypes)
		doc.Rules[i].Match.PatternsContain = normalizeLowerStringSlice(doc.Rules[i].Match.PatternsContain)
	}

	if len(doc.Rules) == 0 {
		doc.Rules = defaults.Rules
	}
	if len(doc.Fallback.Items) == 0 {
		doc.Fallback.Items = defaults.Fallback.Items
	}
	return doc
}

//nolint:gocognit // Rule-based build keeps explicit early-return branches for safety.
func buildFromRuleDocument(input BuildInput, doc ruleDocument, loaderWarnings []string) (Playbook, bool, error) {
	payload := parseJSONObject(input.DiagnosisJSON)
	if payload == nil {
		return Playbook{}, false, errInvalidDiagnosisJSON
	}

	verificationStepCount := countVerificationSteps(payload)
	if verificationStepCount <= 0 {
		return Playbook{}, false, nil
	}

	rootCauseType := strings.ToLower(strings.TrimSpace(input.RootCauseType))
	if rootCauseType == "" {
		rootCauseType = extractRootCauseType(payload)
	}
	patternTokens := extractPatternTokens(payload)

	rawItems, strategy := selectRuleItems(doc, rootCauseType, patternTokens)
	if len(rawItems) == 0 {
		return Playbook{}, false, nil
	}

	items := make([]Item, 0, min(maxPlaybookItems, len(rawItems)))
	warnings := append([]string(nil), loaderWarnings...)
	for _, src := range rawItems {
		if len(items) >= maxPlaybookItems {
			warnings = appendWarning(warnings, "PLAYBOOK_ITEMS_TRUNCATED")
			break
		}
		item, ok := buildItem(src, verificationStepCount)
		if !ok {
			continue
		}
		items = append(items, *item)
	}

	if len(items) == 0 {
		return Playbook{}, false, nil
	}

	playbook := Playbook{
		Version:  VersionT6,
		Strategy: strategy,
		Items:    items,
	}
	if len(warnings) > 0 {
		playbook.Warnings = normalizeStringSlice(warnings)
	}
	return playbook, true, nil
}

func selectRuleItems(doc ruleDocument, rootCauseType string, patternTokens []string) ([]ruleItem, string) {
	if rootCauseType != "" {
		items := collectRuleItems(doc.Rules, func(candidate rule) bool {
			return matchesRootCause(candidate.Match, rootCauseType)
		})
		if len(items) > 0 {
			return items, StrategyRootCauseType
		}
	}

	if len(patternTokens) > 0 {
		items := collectRuleItems(doc.Rules, func(candidate rule) bool {
			return matchesPattern(candidate.Match, patternTokens)
		})
		if len(items) > 0 {
			return items, StrategyPatternMatch
		}
	}

	if len(doc.Fallback.Items) > 0 {
		return append([]ruleItem(nil), doc.Fallback.Items...), StrategyFallback
	}

	return nil, StrategyFallback
}

//nolint:gocognit // Match+dedup flow keeps explicit short-circuit by item cap.
func collectRuleItems(rules []rule, matchFn func(rule) bool) []ruleItem {
	out := make([]ruleItem, 0, maxPlaybookItems)
	seen := map[string]struct{}{}
	for _, candidate := range rules {
		if !matchFn(candidate) {
			continue
		}
		for _, item := range candidate.Items {
			key := buildRuleItemKey(item)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, item)
			if len(out) >= maxPlaybookItems {
				return out
			}
		}
	}
	return out
}

func buildRuleItemKey(item ruleItem) string {
	id := strings.ToLower(strings.TrimSpace(item.ID))
	if id != "" {
		return id
	}
	title := strings.ToLower(strings.TrimSpace(item.Title))
	if title != "" {
		return title
	}
	return strings.ToLower(strings.TrimSpace(item.Rationale))
}

func buildItem(src ruleItem, verificationStepCount int) (*Item, bool) {
	risk := normalizeRisk(src.Risk)
	item := &Item{
		ID:        truncate(sanitizeText(src.ID), maxPlaybookItemIDLen),
		Title:     truncate(sanitizeText(src.Title), maxPlaybookTitleLen),
		Risk:      risk,
		Rationale: truncate(sanitizeText(src.Rationale), maxPlaybookRationale),
	}
	if item.Title == "" {
		return nil, false
	}
	if item.Rationale == "" {
		item.Rationale = "Recommendation is generated by deterministic playbook rules."
	}

	if risk == "HIGH" {
		item.Title = "Contact human operator"
		item.Rationale = "High-risk mitigation requires explicit human confirmation."
		item.Steps = []Step{{
			Type:          "action",
			Text:          "Contact on-call human operator before executing any high-risk action.",
			RequiresHuman: true,
		}}
	} else {
		item.Steps = normalizeSteps(src.Steps, risk)
	}
	if len(item.Steps) == 0 {
		return nil, false
	}

	item.Verification = buildVerification(src.Verification, verificationStepCount)
	if len(item.Verification.RecommendedSteps) == 0 {
		return nil, false
	}

	return item, true
}

func normalizeSteps(in []ruleStep, risk string) []Step {
	out := make([]Step, 0, len(in))
	forceHuman := risk == "MEDIUM" || risk == "HIGH"

	for _, src := range in {
		text := truncate(sanitizeText(src.Text), maxStepTextLen)
		if text == "" {
			continue
		}

		stepType := strings.ToLower(strings.TrimSpace(src.Type))
		if stepType != "check" && stepType != "action" {
			stepType = "check"
		}

		step := Step{
			Type: stepType,
			Text: text,
		}
		if forceHuman || src.RequiresHuman {
			step.RequiresHuman = true
		}
		out = append(out, step)
	}

	return out
}

func buildVerification(src ruleVerification, stepCount int) Verification {
	recommended := normalizeRecommendedSteps(src.RecommendedSteps, stepCount)
	expected := truncate(sanitizeText(src.ExpectedOutcome), maxExpectedOutcomeLen)
	if expected == "" {
		expected = "After applying the suggestion, selected verification plan checks should meet expected RCA signals."
	}
	return Verification{
		UseVerificationPlan: true,
		RecommendedSteps:    recommended,
		ExpectedOutcome:     expected,
	}
}

func normalizeRecommendedSteps(in []int, stepCount int) []int {
	if stepCount <= 0 {
		return nil
	}
	out := make([]int, 0, len(in))
	seen := map[int]struct{}{}
	for _, idx := range in {
		if idx < 0 || idx >= stepCount {
			continue
		}
		if _, ok := seen[idx]; ok {
			continue
		}
		seen[idx] = struct{}{}
		out = append(out, idx)
	}
	if len(out) == 0 {
		out = append(out, 0)
	}
	return out
}

func matchesRootCause(match ruleMatch, rootCauseType string) bool {
	if rootCauseType == "" || len(match.RootCauseTypes) == 0 {
		return false
	}
	return slices.Contains(match.RootCauseTypes, rootCauseType)
}

//nolint:gocognit // Nested keyword-token matching keeps explicit checks for empty values.
func matchesPattern(match ruleMatch, patternTokens []string) bool {
	if len(match.PatternsContain) == 0 || len(patternTokens) == 0 {
		return false
	}
	for _, keyword := range match.PatternsContain {
		if keyword == "" {
			continue
		}
		for _, token := range patternTokens {
			if token == "" {
				continue
			}
			if strings.Contains(token, keyword) || strings.Contains(keyword, token) {
				return true
			}
		}
	}
	return false
}

func extractRootCauseType(payload map[string]any) string {
	rootCause, _ := payload["root_cause"].(map[string]any)
	if rootCause == nil {
		return ""
	}
	if v := strings.ToLower(strings.TrimSpace(anyToString(rootCause["type"]))); v != "" {
		return v
	}
	return strings.ToLower(strings.TrimSpace(anyToString(rootCause["category"])))
}

//nolint:gocognit // Pattern extraction keeps explicit type guards for historical payloads.
func extractPatternTokens(payload map[string]any) []string {
	items, _ := payload["patterns"].([]any)
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items)*2)
	seen := map[string]struct{}{}
	for _, raw := range items {
		pattern, _ := raw.(map[string]any)
		if pattern == nil {
			continue
		}
		for _, key := range []string{"type", "value"} {
			value := strings.ToLower(strings.TrimSpace(anyToString(pattern[key])))
			if value == "" {
				continue
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			out = append(out, value)
		}
	}
	return out
}

func countVerificationSteps(payload map[string]any) int {
	plan, _ := payload["verification_plan"].(map[string]any)
	if plan == nil {
		return 0
	}
	steps, _ := plan["steps"].([]any)
	return len(steps)
}

func parseJSONObject(raw string) map[string]any {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	out := map[string]any{}
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return nil
	}
	return out
}

func anyToString(v any) string {
	switch value := v.(type) {
	case string:
		return value
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			return ""
		}
		return strings.Trim(string(raw), `"`)
	}
}

func normalizeRisk(raw string) string {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "LOW":
		return "LOW"
	case "MEDIUM":
		return "MEDIUM"
	case "HIGH":
		return "HIGH"
	default:
		return "LOW"
	}
}

func sanitizeText(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	normalized := strings.Join(strings.Fields(trimmed), " ")
	return sensitiveWordRegex.ReplaceAllString(normalized, "[redacted]")
}

func truncate(value string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if utf8.RuneCountInString(value) <= maxLen {
		return value
	}
	runes := []rune(value)
	return string(runes[:maxLen])
}

func appendWarning(warnings []string, warning string) []string {
	warning = strings.TrimSpace(warning)
	if warning == "" {
		return warnings
	}
	for _, item := range warnings {
		if strings.TrimSpace(item) == warning {
			return warnings
		}
	}
	return append(warnings, warning)
}

func normalizeLowerStringSlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, item := range in {
		trimmed := strings.ToLower(strings.TrimSpace(item))
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func normalizeStringSlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, item := range in {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular()
}

func isDirectory(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

func defaultRuleDocument() ruleDocument {
	return ruleDocument{
		Version: VersionT6,
		Rules: []rule{
			{
				ID:    "root-missing-evidence",
				Match: ruleMatch{RootCauseTypes: []string{"missing_evidence"}},
				Items: []ruleItem{
					{
						ID:        "pb-missing-evidence-collect",
						Title:     "Collect missing evidence before conclusion",
						Risk:      "LOW",
						Rationale: "Current diagnosis indicates missing signals. Collecting key evidence reduces false conclusions.",
						Steps: []ruleStep{
							{Type: "check", Text: "Confirm logs and traces are queryable for the incident time window."},
							{Type: "check", Text: "Re-run metrics/log queries with aligned start/end timestamps."},
						},
						Verification: ruleVerification{
							RecommendedSteps: []int{0},
							ExpectedOutcome:  "Verification checks should return non-empty aligned evidence after data gaps are fixed.",
						},
					},
				},
			},
			{
				ID:    "root-conflict-evidence",
				Match: ruleMatch{RootCauseTypes: []string{"conflict_evidence"}},
				Items: []ruleItem{
					{
						ID:        "pb-conflict-evidence-align",
						Title:     "Align evidence windows and escalate for human review",
						Risk:      "MEDIUM",
						Rationale: "Conflicting signals require careful human validation before any mitigation decision.",
						Steps: []ruleStep{
							{Type: "check", Text: "Align metrics, logs and traces to the exact same window and compare anomalies."},
							{Type: "action", Text: "Request on-call engineer confirmation before applying any mitigation."},
						},
						Verification: ruleVerification{
							RecommendedSteps: []int{0},
							ExpectedOutcome:  "Re-check result should consistently support one signal direction after alignment.",
						},
					},
				},
			},
			{
				ID:    "root-unknown",
				Match: ruleMatch{RootCauseTypes: []string{"unknown"}},
				Items: []ruleItem{
					{
						ID:        "pb-unknown-triage",
						Title:     "Run baseline triage checks",
						Risk:      "LOW",
						Rationale: "Unknown root cause should start from deterministic low-risk diagnostics.",
						Steps: []ruleStep{
							{Type: "check", Text: "Compare recent deployments/config changes with the incident start time."},
							{Type: "check", Text: "Inspect top error signals and hot endpoints for the same period."},
						},
						Verification: ruleVerification{
							RecommendedSteps: []int{0},
							ExpectedOutcome:  "Verification checks should reproduce the dominant failure signal used by diagnosis.",
						},
					},
				},
			},
			{
				ID:    "pattern-latency-or-5xx",
				Match: ruleMatch{PatternsContain: []string{"latency", "5xx", "timeout", "error_rate"}},
				Items: []ruleItem{
					{
						ID:        "pb-pattern-latency",
						Title:     "Validate latency and error spike scope",
						Risk:      "LOW",
						Rationale: "Pattern matches indicate request path saturation or partial dependency instability.",
						Steps: []ruleStep{
							{Type: "check", Text: "Break down latency and 5xx by route/workload to identify the hottest subset."},
							{Type: "check", Text: "Verify upstream dependency health around the same timestamps."},
						},
						Verification: ruleVerification{
							RecommendedSteps: []int{0},
							ExpectedOutcome:  "Verification should confirm whether latency/error spikes remain concentrated on the same subset.",
						},
					},
				},
			},
		},
		Fallback: fallbackRule{
			Items: []ruleItem{
				{
					ID:        "pb-fallback-baseline",
					Title:     "Apply generic low-risk RCA checks",
					Risk:      "LOW",
					Rationale: "No specific playbook rule matched; apply deterministic baseline checks.",
					Steps: []ruleStep{
						{Type: "check", Text: "Review recent change history and dependency status for the incident service."},
						{Type: "check", Text: "Re-run the primary verification query to confirm issue reproducibility."},
					},
					Verification: ruleVerification{
						RecommendedSteps: []int{0},
						ExpectedOutcome:  "Verification should reproduce the same key signal before any mitigation decision.",
					},
				},
			},
		},
	}
}
