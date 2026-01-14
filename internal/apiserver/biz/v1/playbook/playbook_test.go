package playbook

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestBuildFromRuleDocument_RootCausePriority(t *testing.T) {
	doc := ruleDocument{
		Version: VersionT6,
		Rules: []rule{
			{
				ID:    "r-root",
				Match: ruleMatch{RootCauseTypes: []string{"missing_evidence"}},
				Items: []ruleItem{
					{
						ID:        "item-root",
						Title:     "Root cause matched",
						Risk:      "LOW",
						Rationale: "root rule rationale",
						Steps:     []ruleStep{{Type: "check", Text: "check root"}},
						Verification: ruleVerification{
							RecommendedSteps: []int{1},
							ExpectedOutcome:  "root expected",
						},
					},
				},
			},
			{
				ID:    "r-pattern",
				Match: ruleMatch{PatternsContain: []string{"latency"}},
				Items: []ruleItem{
					{
						ID:        "item-pattern",
						Title:     "Pattern matched",
						Risk:      "LOW",
						Rationale: "pattern rule rationale",
						Steps:     []ruleStep{{Type: "check", Text: "check pattern"}},
						Verification: ruleVerification{
							RecommendedSteps: []int{0},
							ExpectedOutcome:  "pattern expected",
						},
					},
				},
			},
		},
		Fallback: fallbackRule{Items: []ruleItem{{
			ID:        "item-fallback",
			Title:     "Fallback matched",
			Risk:      "LOW",
			Rationale: "fallback rule rationale",
			Steps:     []ruleStep{{Type: "check", Text: "check fallback"}},
			Verification: ruleVerification{
				RecommendedSteps: []int{0},
				ExpectedOutcome:  "fallback expected",
			},
		}}},
	}

	diagnosisJSON := mustDiagnosisJSON(t, "missing_evidence", []string{"latency"}, 2)

	playbook, ok, err := buildFromRuleDocument(BuildInput{DiagnosisJSON: diagnosisJSON}, doc, nil)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, StrategyRootCauseType, playbook.Strategy)
	require.Len(t, playbook.Items, 1)
	require.Equal(t, "item-root", playbook.Items[0].ID)
	require.Equal(t, []int{1}, playbook.Items[0].Verification.RecommendedSteps)
}

func TestBuildFromRuleDocument_PatternMatchBeforeFallback(t *testing.T) {
	doc := ruleDocument{
		Version: VersionT6,
		Rules: []rule{
			{
				ID:    "r-pattern",
				Match: ruleMatch{PatternsContain: []string{"latency"}},
				Items: []ruleItem{{
					ID:        "item-pattern",
					Title:     "Pattern matched",
					Risk:      "LOW",
					Rationale: "pattern rule rationale",
					Steps:     []ruleStep{{Type: "check", Text: "check pattern"}},
					Verification: ruleVerification{
						RecommendedSteps: []int{0},
						ExpectedOutcome:  "pattern expected",
					},
				}},
			},
		},
		Fallback: fallbackRule{Items: []ruleItem{{
			ID:        "item-fallback",
			Title:     "Fallback matched",
			Risk:      "LOW",
			Rationale: "fallback rule rationale",
			Steps:     []ruleStep{{Type: "check", Text: "check fallback"}},
			Verification: ruleVerification{
				RecommendedSteps: []int{0},
				ExpectedOutcome:  "fallback expected",
			},
		}}},
	}

	diagnosisJSON := mustDiagnosisJSON(t, "unknown", []string{"latency"}, 1)

	playbook, ok, err := buildFromRuleDocument(BuildInput{DiagnosisJSON: diagnosisJSON}, doc, nil)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, StrategyPatternMatch, playbook.Strategy)
	require.Len(t, playbook.Items, 1)
	require.Equal(t, "item-pattern", playbook.Items[0].ID)
}

func TestBuildFromRuleDocument_EnforcesLimitsAndSensitiveFiltering(t *testing.T) {
	fallbackItems := make([]ruleItem, 0, 12)
	for i := 0; i < 12; i++ {
		risk := "LOW"
		stepText := "run deterministic check"
		expected := "verification step should return stable output"
		if i == 0 {
			risk = "MEDIUM"
			stepText = strings.Repeat("a", 280) + " token authorization headers"
			expected = strings.Repeat("b", 300) + " secret"
		}
		fallbackItems = append(fallbackItems, ruleItem{
			ID:        "item-" + anyToString(i),
			Title:     "title-" + anyToString(i),
			Risk:      risk,
			Rationale: "rationale token for item",
			Steps:     []ruleStep{{Type: "check", Text: stepText}},
			Verification: ruleVerification{
				RecommendedSteps: []int{9},
				ExpectedOutcome:  expected,
			},
		})
	}

	doc := ruleDocument{
		Version:  VersionT6,
		Fallback: fallbackRule{Items: fallbackItems},
	}

	diagnosisJSON := mustDiagnosisJSON(t, "unknown", nil, 1)

	playbook, ok, err := buildFromRuleDocument(BuildInput{DiagnosisJSON: diagnosisJSON}, doc, []string{fallbackWarningRuleCfg})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, StrategyFallback, playbook.Strategy)
	require.Len(t, playbook.Items, maxPlaybookItems)
	require.Contains(t, playbook.Warnings, fallbackWarningRuleCfg)

	first := playbook.Items[0]
	require.Equal(t, "MEDIUM", first.Risk)
	require.True(t, first.Steps[0].RequiresHuman)
	require.LessOrEqual(t, utf8.RuneCountInString(first.Steps[0].Text), maxStepTextLen)
	require.LessOrEqual(t, utf8.RuneCountInString(first.Verification.ExpectedOutcome), maxExpectedOutcomeLen)
	require.Equal(t, []int{0}, first.Verification.RecommendedSteps)

	raw, marshalErr := json.Marshal(playbook)
	require.NoError(t, marshalErr)
	lowered := strings.ToLower(string(raw))
	require.NotContains(t, lowered, "token")
	require.NotContains(t, lowered, "authorization")
	require.NotContains(t, lowered, "headers")
	require.NotContains(t, lowered, "secret")
}

func TestBuildFromRuleDocument_NoVerificationPlanStepsReturnsNil(t *testing.T) {
	doc := defaultRuleDocument()
	diagnosisJSON := mustDiagnosisJSON(t, "unknown", []string{"latency"}, 0)

	playbook, ok, err := buildFromRuleDocument(BuildInput{DiagnosisJSON: diagnosisJSON}, doc, nil)
	require.NoError(t, err)
	require.False(t, ok)
	require.Empty(t, playbook.Items)
}

func mustDiagnosisJSON(t *testing.T, rootCauseType string, patterns []string, verificationSteps int) string {
	t.Helper()

	payload := map[string]any{
		"summary": "diagnosis summary",
		"root_cause": map[string]any{
			"type":       rootCauseType,
			"category":   "unknown",
			"statement":  "",
			"confidence": 0.2,
		},
	}

	if len(patterns) > 0 {
		items := make([]map[string]any, 0, len(patterns))
		for _, pattern := range patterns {
			items = append(items, map[string]any{
				"type":  "signal",
				"value": pattern,
			})
		}
		payload["patterns"] = items
	}

	if verificationSteps >= 0 {
		steps := make([]map[string]any, 0, verificationSteps)
		for i := 0; i < verificationSteps; i++ {
			steps = append(steps, map[string]any{
				"id":   "step-" + anyToString(i),
				"tool": "mcp.query_metrics",
			})
		}
		payload["verification_plan"] = map[string]any{
			"version": "a5",
			"steps":   steps,
		}
	}

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	return string(raw)
}
