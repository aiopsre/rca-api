package kb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

const (
	maxRootCauseSummaryLen      = 512
	maxPatternsJSONBytes        = 4096
	maxEvidenceSignatureBytes   = 4096
	maxPatternTypeLen           = 64
	maxPatternValueLen          = 256
	maxSearchScanRows           = 200
	maxSearchLimit              = 10
	defaultSearchLimit          = 3
	maxMatchedOnItems           = 8
	maxMatchedOnItemLen         = 128
	maxRefPatterns              = 8
	defaultPatternWeight        = 0.5
	defaultWritebackConfidence  = 0.7
	maxEvidenceSignatureIDCount = 20
)

var (
	errInvalidWritebackInput = errors.New("invalid kb writeback input")
	sensitiveWordRegex       = regexp.MustCompile(`(?i)(secret|token|authorization|headers?|password|passwd|api[_-]?key|bearer)`)
)

// Pattern defines one normalized rule token stored in KB.
type Pattern struct {
	Type   string  `json:"type"`
	Value  string  `json:"value"`
	Weight float64 `json:"weight"`
}

// Ref describes one KB hit returned to toolcall.output.
type Ref struct {
	KBID             string    `json:"kb_id"`
	Score            float64   `json:"score"`
	MatchedOn        []string  `json:"matched_on"`
	RootCauseType    string    `json:"root_cause_type"`
	RootCauseSummary string    `json:"root_cause_summary"`
	Patterns         []Pattern `json:"patterns"`
}

// WritebackInput defines data required to persist one KB entry.
type WritebackInput struct {
	Namespace         string
	Service           string
	RootCauseType     string
	RootCauseSummary  string
	Patterns          []Pattern
	EvidenceSignature map[string]any
	Confidence        float64
}

// SearchInput defines query context for rule-based KB retrieval.
type SearchInput struct {
	Namespace     string
	Service       string
	RootCauseType string
	Patterns      []Pattern
	Limit         int
}

type biz struct {
	store store.IStore
}

// New creates KB biz.
func New(store store.IStore) *biz {
	return &biz{store: store}
}

// Writeback performs idempotent KB upsert by unique (namespace, service, root_cause_type, patterns_hash).
func (b *biz) Writeback(ctx context.Context, input WritebackInput) (*model.KBEntryM, error) {
	rootCauseType := normalizeRootCauseType(input.RootCauseType)
	rootCauseSummary := sanitizeText(input.RootCauseSummary, maxRootCauseSummaryLen)
	if rootCauseType == "" || rootCauseSummary == "" {
		return nil, errInvalidWritebackInput
	}

	normalizedPatterns, patternsJSON, patternsHash := normalizeAndHashPatterns(input.Patterns)
	if len(normalizedPatterns) == 0 || patternsJSON == "" || patternsHash == "" {
		return nil, errInvalidWritebackInput
	}

	evidenceSignatureJSON := marshalLimitedJSON(input.EvidenceSignature, maxEvidenceSignatureBytes)
	confidence := normalizeConfidence(input.Confidence)

	entry := &model.KBEntryM{
		Namespace:             sanitizeScope(input.Namespace, 128),
		Service:               sanitizeScope(input.Service, 256),
		RootCauseType:         rootCauseType,
		RootCauseSummary:      rootCauseSummary,
		PatternsJSON:          patternsJSON,
		PatternsHash:          patternsHash,
		EvidenceSignatureJSON: evidenceSignatureJSON,
		Confidence:            confidence,
	}
	return b.store.KBEntry().Upsert(ctx, entry)
}

// Search retrieves top-k KB refs by deterministic rule scoring.
func (b *biz) Search(ctx context.Context, input SearchInput) ([]Ref, error) {
	normalizedInput := SearchInput{
		Namespace:     sanitizeScope(input.Namespace, 128),
		Service:       sanitizeScope(input.Service, 256),
		RootCauseType: normalizeRootCauseType(input.RootCauseType),
		Patterns:      normalizePatterns(input.Patterns),
		Limit:         normalizeSearchLimit(input.Limit),
	}

	namespaces := []string{""}
	if normalizedInput.Namespace != "" {
		namespaces = append(namespaces, normalizedInput.Namespace)
	}

	services := []string{""}
	if normalizedInput.Service != "" {
		services = append(services, normalizedInput.Service)
	}

	_, entries, err := b.store.KBEntry().List(ctx, where.T(ctx).
		P(0, maxSearchScanRows).
		Q("namespace IN ?", namespaces).
		Q("service IN ?", services))
	if err != nil {
		return nil, err
	}

	refs := scoreAndSortEntries(entries, normalizedInput)
	if len(refs) == 0 {
		return nil, nil
	}
	if len(refs) > normalizedInput.Limit {
		refs = refs[:normalizedInput.Limit]
	}

	now := time.Now().UTC()
	for _, ref := range refs {
		_ = b.store.KBEntry().IncrementHit(ctx, ref.KBID, now)
	}
	return refs, nil
}

// InjectRefsToToolCall appends kb_refs to an existing toolcall response_json.
func (b *biz) InjectRefsToToolCall(ctx context.Context, toolCall *model.AIToolCallM, refs []Ref) error {
	if toolCall == nil || toolCall.ResponseJSON == nil || len(refs) == 0 {
		return nil
	}

	payload := parseJSONObject(*toolCall.ResponseJSON)
	if payload == nil {
		payload = make(map[string]any)
	}
	payload["kb_refs"] = refs

	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	value := string(raw)
	toolCall.ResponseJSON = &value
	toolCall.ResponseSizeBytes = int64(len(raw))
	return b.store.AIToolCall().Update(ctx, toolCall)
}

// ExtractQualityGateDecision reads quality_gate.decision from toolcall response_json.
func ExtractQualityGateDecision(toolCalls []*model.AIToolCallM) string {
	for _, toolCall := range toolCalls {
		payload := parseToolCallResponse(toolCall)
		if payload == nil {
			continue
		}
		gate, _ := payload["quality_gate"].(map[string]any)
		decision := strings.ToLower(strings.TrimSpace(anyToString(gate["decision"])))
		if decision != "" {
			return decision
		}
	}
	return ""
}

// SelectPrimaryToolCall chooses a deterministic toolcall for kb_refs injection.
func SelectPrimaryToolCall(toolCalls []*model.AIToolCallM) *model.AIToolCallM {
	if len(toolCalls) == 0 {
		return nil
	}

	var withGate []*model.AIToolCallM
	for _, toolCall := range toolCalls {
		payload := parseToolCallResponse(toolCall)
		if payload == nil {
			continue
		}
		if _, ok := payload["quality_gate"].(map[string]any); ok {
			withGate = append(withGate, toolCall)
		}
	}
	if len(withGate) == 0 {
		return toolCalls[len(toolCalls)-1]
	}
	sort.SliceStable(withGate, func(i, j int) bool {
		if withGate[i].Seq != withGate[j].Seq {
			return withGate[i].Seq < withGate[j].Seq
		}
		return withGate[i].ID < withGate[j].ID
	})
	return withGate[0]
}

// ExtractPatternsFromDiagnosis reads diagnosis_json.patterns.
func ExtractPatternsFromDiagnosis(raw string) []Pattern {
	payload := parseJSONObject(raw)
	if payload == nil {
		return nil
	}

	items, _ := payload["patterns"].([]any)
	patterns := make([]Pattern, 0, len(items))
	for _, item := range items {
		obj, _ := item.(map[string]any)
		if obj == nil {
			continue
		}
		patterns = append(patterns, Pattern{
			Type:   anyToString(obj["type"]),
			Value:  anyToString(obj["value"]),
			Weight: anyToFloat64(obj["weight"]),
		})
	}
	return normalizePatterns(patterns)
}

// ExtractPatternsFromToolCalls builds fallback patterns from toolcall output.
//
//nolint:gocognit,gocyclo,nestif,wsl_v5 // Pattern extraction needs explicit nested parsing for heterogeneous payloads.
func ExtractPatternsFromToolCalls(toolCalls []*model.AIToolCallM) []Pattern {
	collected := make([]Pattern, 0, len(toolCalls)*4)
	for _, toolCall := range toolCalls {
		payload := parseToolCallResponse(toolCall)
		if payload == nil {
			continue
		}

		evidencePlan, _ := payload["evidence_plan"].(map[string]any)
		if evidencePlan != nil {
			candidates, _ := evidencePlan["candidates"].([]any)
			for _, candidate := range candidates {
				obj, _ := candidate.(map[string]any)
				if obj == nil {
					continue
				}
				name := anyToString(obj["name"])
				queryType := anyToString(obj["type"])
				if name != "" {
					collected = append(collected, Pattern{Type: "plan_candidate", Value: name, Weight: 0.55})
				}
				if queryType != "" {
					collected = append(collected, Pattern{Type: "query_type", Value: queryType, Weight: 0.45})
				}
			}
		}

		qualityGate, _ := payload["quality_gate"].(map[string]any)
		if qualityGate != nil {
			reasons, _ := qualityGate["reasons"].([]any)
			for _, reason := range reasons {
				value := anyToString(reason)
				if value == "" {
					continue
				}
				collected = append(collected, Pattern{Type: "quality_gate_reason", Value: value, Weight: 0.35})
			}

			evidenceSummary, _ := qualityGate["evidence_summary"].(map[string]any)
			if evidenceSummary != nil {
				sources, _ := evidenceSummary["sources"].([]any)
				for _, source := range sources {
					value := anyToString(source)
					if value == "" {
						continue
					}
					collected = append(collected, Pattern{Type: "evidence_source", Value: value, Weight: 0.6})
				}
			}
		}
	}
	return normalizePatterns(collected)
}

// BuildEvidenceSignature collects compact evidence signature from diagnosis/toolcalls.
func BuildEvidenceSignature(diagnosisJSON string, toolCalls []*model.AIToolCallM) map[string]any {
	signature := make(map[string]any)
	payload := parseJSONObject(diagnosisJSON)
	if payload == nil {
		return nil
	}

	ids := collectEvidenceIDs(payload)
	if len(ids) > 0 {
		if len(ids) > maxEvidenceSignatureIDCount {
			ids = ids[:maxEvidenceSignatureIDCount]
		}
		signature["evidence_ids"] = ids
	}

	decision := ExtractQualityGateDecision(toolCalls)
	if decision != "" {
		signature["quality_gate"] = decision
	}

	patterns := ExtractPatternsFromDiagnosis(diagnosisJSON)
	if len(patterns) > 0 {
		signature["pattern_count"] = len(patterns)
	}

	if len(signature) == 0 {
		return nil
	}
	return signature
}

//nolint:gocognit // Scoring loop keeps explicit checks for determinism.
func scoreAndSortEntries(entries []*model.KBEntryM, input SearchInput) []Ref {
	if len(entries) == 0 {
		return nil
	}

	type scoredRef struct {
		ref   Ref
		score float64
	}
	scored := make([]scoredRef, 0, len(entries))
	inputPatternKeys := makePatternKeySet(input.Patterns)

	for _, entry := range entries {
		if entry == nil {
			continue
		}
		entryPatterns := normalizePatterns(parsePatternsJSON(entry.PatternsJSON))
		entryPatternKeys := makePatternKeySet(entryPatterns)
		overlapKeys := intersectPatternKeys(inputPatternKeys, entryPatternKeys)

		score, matchedOn := scoreOneEntry(entry, input, overlapKeys, len(entryPatternKeys))
		if score <= 0 {
			continue
		}

		ref := Ref{
			KBID:             strings.TrimSpace(entry.KBID),
			Score:            roundScore(score),
			MatchedOn:        normalizeMatchedOn(matchedOn),
			RootCauseType:    normalizeRootCauseType(entry.RootCauseType),
			RootCauseSummary: sanitizeText(entry.RootCauseSummary, maxRootCauseSummaryLen),
			Patterns:         limitPatterns(entryPatterns, maxRefPatterns),
		}
		if ref.KBID == "" {
			continue
		}
		scored = append(scored, scoredRef{ref: ref, score: score})
	}

	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].ref.KBID < scored[j].ref.KBID
	})

	refs := make([]Ref, 0, len(scored))
	for _, item := range scored {
		refs = append(refs, item.ref)
	}
	return refs
}

//nolint:gocognit,gocyclo // Weighted rule score stays explicit for auditability.
func scoreOneEntry(entry *model.KBEntryM, input SearchInput, overlapKeys []string, entryPatternCount int) (float64, []string) {
	score := 0.0
	matchedOn := make([]string, 0, maxMatchedOnItems)
	entryNamespace := sanitizeScope(entry.Namespace, 128)
	entryService := sanitizeScope(entry.Service, 256)
	entryRootType := normalizeRootCauseType(entry.RootCauseType)

	if input.Namespace != "" {
		switch entryNamespace {
		case input.Namespace:
			score += 0.30
			matchedOn = append(matchedOn, "namespace")

		case "":
			score += 0.08
			matchedOn = append(matchedOn, "namespace:global")
		}
	}
	if input.Service != "" {
		switch entryService {
		case input.Service:
			score += 0.30
			matchedOn = append(matchedOn, "service")

		case "":
			score += 0.08
			matchedOn = append(matchedOn, "service:global")
		}
	}
	if input.RootCauseType != "" && entryRootType == input.RootCauseType {
		score += 0.22
		matchedOn = append(matchedOn, "root_cause_type")
	}
	if entryPatternCount > 0 && len(overlapKeys) > 0 {
		score += 0.30 * (float64(len(overlapKeys)) / float64(entryPatternCount))
		for _, key := range overlapKeys {
			matchedOn = append(matchedOn, "pattern:"+key)
		}
	}

	if score > 1 {
		score = 1
	}
	return score, matchedOn
}

func normalizeAndHashPatterns(in []Pattern) ([]Pattern, string, string) {
	normalized := normalizePatterns(in)
	trimmed, jsonText := marshalPatternsWithinLimit(normalized, maxPatternsJSONBytes)
	if len(trimmed) == 0 || jsonText == "" {
		return nil, "", ""
	}
	sum := sha256.Sum256([]byte(jsonText))
	return trimmed, jsonText, hex.EncodeToString(sum[:])
}

//nolint:gocognit,gocyclo // Normalization keeps explicit sanitation and dedup rules.
func normalizePatterns(in []Pattern) []Pattern {
	if len(in) == 0 {
		return nil
	}

	byKey := make(map[string]Pattern, len(in))
	for _, item := range in {
		typeValue := normalizePatternType(item.Type)
		value := normalizePatternValue(item.Value)
		if typeValue == "" || value == "" {
			continue
		}
		if hasSensitiveWord(typeValue) || hasSensitiveWord(value) {
			continue
		}
		normalized := Pattern{
			Type:   typeValue,
			Value:  value,
			Weight: normalizeWeight(item.Weight),
		}
		key := patternKey(normalized)
		current, ok := byKey[key]
		if !ok || normalized.Weight > current.Weight {
			byKey[key] = normalized
		}
	}

	out := make([]Pattern, 0, len(byKey))
	for _, item := range byKey {
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		if out[i].Value != out[j].Value {
			return out[i].Value < out[j].Value
		}
		return out[i].Weight > out[j].Weight
	})
	return out
}

func marshalPatternsWithinLimit(patterns []Pattern, limit int) ([]Pattern, string) {
	if len(patterns) == 0 || limit <= 2 {
		return nil, ""
	}

	for n := len(patterns); n >= 1; n-- {
		raw, err := json.Marshal(patterns[:n])
		if err != nil {
			continue
		}
		if len(raw) <= limit {
			return patterns[:n], string(raw)
		}
	}
	return nil, ""
}

func limitPatterns(patterns []Pattern, maxItems int) []Pattern {
	if len(patterns) == 0 {
		return nil
	}
	if maxItems <= 0 || len(patterns) <= maxItems {
		return patterns
	}
	return patterns[:maxItems]
}

//nolint:gocognit,gocyclo,wsl_v5 // Character-level normalization is intentionally explicit.
func normalizePatternType(in string) string {
	raw := strings.ToLower(strings.TrimSpace(in))
	if raw == "" {
		return ""
	}

	raw = collapseWhitespace(raw)
	builder := strings.Builder{}
	lastUnderscore := false
	for _, char := range raw {
		switch {
		case char >= 'a' && char <= 'z':
			builder.WriteRune(char)
			lastUnderscore = false
		case char >= '0' && char <= '9':
			builder.WriteRune(char)
			lastUnderscore = false
		case char == '_' || char == '-' || unicode.IsSpace(char) || char == '/' || char == '.' || char == ':':
			if !lastUnderscore && builder.Len() > 0 {
				builder.WriteRune('_')
				lastUnderscore = true
			}
		}
	}
	return truncateString(strings.Trim(builder.String(), "_"), maxPatternTypeLen)
}

func normalizePatternValue(in string) string {
	value := strings.ToLower(collapseWhitespace(strings.TrimSpace(in)))
	return truncateString(value, maxPatternValueLen)
}

func normalizeWeight(weight float64) float64 {
	if math.IsNaN(weight) || math.IsInf(weight, 0) || weight <= 0 {
		weight = defaultPatternWeight
	}
	if weight > 1 {
		weight = 1
	}
	return math.Round(weight*1000) / 1000
}

func normalizeRootCauseType(in string) string {
	return truncateString(strings.ToLower(strings.TrimSpace(in)), 64)
}

func normalizeConfidence(in float64) float64 {
	if math.IsNaN(in) || math.IsInf(in, 0) || in <= 0 {
		return defaultWritebackConfidence
	}
	if in > 1 {
		return 1
	}
	return math.Round(in*1000) / 1000
}

func normalizeSearchLimit(limit int) int {
	if limit <= 0 {
		return defaultSearchLimit
	}
	if limit > maxSearchLimit {
		return maxSearchLimit
	}
	return limit
}

func normalizeMatchedOn(items []string) []string {
	if len(items) == 0 {
		return []string{"scope_match"}
	}

	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		trimmed := truncateString(strings.TrimSpace(item), maxMatchedOnItemLen)
		if trimmed == "" || hasSensitiveWord(trimmed) {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
		if len(out) >= maxMatchedOnItems {
			break
		}
	}
	if len(out) == 0 {
		return []string{"scope_match"}
	}
	return out
}

func sanitizeScope(in string, maxLen int) string {
	return truncateString(strings.TrimSpace(in), maxLen)
}

func sanitizeText(in string, maxLen int) string {
	normalized := collapseWhitespace(strings.TrimSpace(in))
	redacted := sensitiveWordRegex.ReplaceAllString(normalized, "[redacted]")
	return truncateString(redacted, maxLen)
}

func hasSensitiveWord(in string) bool {
	return sensitiveWordRegex.MatchString(in)
}

func marshalLimitedJSON(in map[string]any, maxBytes int) *string {
	if len(in) == 0 {
		return nil
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return nil
	}
	if len(raw) > maxBytes {
		return nil
	}
	value := string(raw)
	return &value
}

func parsePatternsJSON(raw string) []Pattern {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []Pattern
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func parseToolCallResponse(toolCall *model.AIToolCallM) map[string]any {
	if toolCall == nil || toolCall.ResponseJSON == nil {
		return nil
	}
	return parseJSONObject(*toolCall.ResponseJSON)
}

func parseJSONObject(raw string) map[string]any {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return nil
	}
	return out
}

//nolint:gocognit // Explicit collection avoids hidden assumptions across diagnosis shapes.
func collectEvidenceIDs(diagnosis map[string]any) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, maxEvidenceSignatureIDCount)
	appendID := func(value string) {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return
		}
		if _, ok := seen[trimmed]; ok {
			return
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}

	rootCause, _ := diagnosis["root_cause"].(map[string]any)
	if rootCause != nil {
		rootIDs, _ := rootCause["evidence_ids"].([]any)
		for _, item := range rootIDs {
			appendID(anyToString(item))
		}
	}

	hypotheses, _ := diagnosis["hypotheses"].([]any)
	for _, hypothesis := range hypotheses {
		obj, _ := hypothesis.(map[string]any)
		if obj == nil {
			continue
		}
		ids, _ := obj["supporting_evidence_ids"].([]any)
		for _, item := range ids {
			appendID(anyToString(item))
		}
	}

	sort.Strings(out)
	return out
}

func makePatternKeySet(patterns []Pattern) map[string]struct{} {
	if len(patterns) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(patterns))
	for _, item := range patterns {
		out[patternKey(item)] = struct{}{}
	}
	return out
}

func intersectPatternKeys(left map[string]struct{}, right map[string]struct{}) []string {
	if len(left) == 0 || len(right) == 0 {
		return nil
	}
	overlap := make([]string, 0, len(left))
	for key := range left {
		if _, ok := right[key]; ok {
			if hasSensitiveWord(key) {
				continue
			}
			overlap = append(overlap, key)
		}
	}
	sort.Strings(overlap)
	return overlap
}

func patternKey(item Pattern) string {
	return item.Type + ":" + item.Value
}

func collapseWhitespace(in string) string {
	if strings.TrimSpace(in) == "" {
		return ""
	}
	return strings.Join(strings.Fields(in), " ")
}

func truncateString(in string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	runes := []rune(in)
	if len(runes) <= maxLen {
		return in
	}
	return string(runes[:maxLen])
}

func anyToString(in any) string {
	switch value := in.(type) {
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

//nolint:wsl_v5 // Switch cases are intentionally compact for primitive conversions.
func anyToFloat64(in any) float64 {
	switch value := in.(type) {
	case float64:
		return value
	case float32:
		return float64(value)
	case int:
		return float64(value)
	case int64:
		return float64(value)
	case json.Number:
		f, _ := value.Float64()
		return f
	case string:
		f, _ := json.Number(strings.TrimSpace(value)).Float64()
		return f
	default:
		return 0
	}
}

func roundScore(in float64) float64 {
	return math.Round(in*1000) / 1000
}
