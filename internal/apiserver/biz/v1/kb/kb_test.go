package kb

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
)

func TestNormalizeAndHashPatternsStable(t *testing.T) {
	inputA := []Pattern{
		{Type: " log keyword ", Value: "  NullPointerException ", Weight: 0.9},
		{Type: "LOG_KEYWORD", Value: "nullpointerexception", Weight: 0.4},
		{Type: "metric_name", Value: " http_server_requests_seconds ", Weight: 0.4},
		{Type: "http-status", Value: "5XX", Weight: 0.6},
	}
	inputB := []Pattern{
		{Type: "http-status", Value: "5xx", Weight: 0.6},
		{Type: "metric_name", Value: "http_server_requests_seconds", Weight: 0.4},
		{Type: "log_keyword", Value: "NullPointerException", Weight: 0.9},
	}

	normalizedA, jsonA, hashA := normalizeAndHashPatterns(inputA)
	normalizedB, jsonB, hashB := normalizeAndHashPatterns(inputB)

	require.NotEmpty(t, normalizedA)
	require.NotEmpty(t, jsonA)
	require.NotEmpty(t, hashA)
	require.Equal(t, normalizedA, normalizedB)
	require.Equal(t, jsonA, jsonB)
	require.Equal(t, hashA, hashB)

	expected := []Pattern{
		{Type: "http_status", Value: "5xx", Weight: 0.6},
		{Type: "log_keyword", Value: "nullpointerexception", Weight: 0.9},
		{Type: "metric_name", Value: "http_server_requests_seconds", Weight: 0.4},
	}
	require.Equal(t, expected, normalizedA)

	for i := 0; i < 5; i++ {
		normalized, jsonText, hash := normalizeAndHashPatterns(inputA)
		require.Equal(t, expected, normalized)
		require.Equal(t, jsonA, jsonText)
		require.Equal(t, hashA, hash)
	}
}

func TestScoreAndSortEntriesStable(t *testing.T) {
	patternsJSON := mustPatternsJSON(t, []Pattern{
		{Type: "log_keyword", Value: "nullpointerexception", Weight: 0.9},
		{Type: "query_type", Value: "logs", Weight: 0.5},
	})
	otherPatternsJSON := mustPatternsJSON(t, []Pattern{
		{Type: "query_type", Value: "metrics", Weight: 0.5},
	})

	entries := []*model.KBEntryM{
		{
			KBID:                  "kb-b",
			Namespace:             "prod-ns",
			Service:               "checkout",
			RootCauseType:         "app",
			RootCauseSummary:      "entry b",
			PatternsJSON:          patternsJSON,
			PatternsHash:          "h-b",
			Confidence:            0.7,
			HitCount:              0,
			EvidenceSignatureJSON: nil,
		},
		{
			KBID:                  "kb-a",
			Namespace:             "prod-ns",
			Service:               "checkout",
			RootCauseType:         "app",
			RootCauseSummary:      "entry a",
			PatternsJSON:          patternsJSON,
			PatternsHash:          "h-a",
			Confidence:            0.7,
			HitCount:              0,
			EvidenceSignatureJSON: nil,
		},
		{
			KBID:             "kb-c",
			Namespace:        "",
			Service:          "checkout",
			RootCauseType:    "network",
			RootCauseSummary: "entry c",
			PatternsJSON:     otherPatternsJSON,
			PatternsHash:     "h-c",
			Confidence:       0.7,
			HitCount:         0,
		},
	}

	input := SearchInput{
		Namespace:     "prod-ns",
		Service:       "checkout",
		RootCauseType: "app",
		Patterns: []Pattern{
			{Type: "log_keyword", Value: "NullPointerException", Weight: 0.5},
		},
		Limit: 3,
	}

	resultA := scoreAndSortEntries(entries, input)
	resultB := scoreAndSortEntries(entries, input)

	require.Len(t, resultA, 3)
	require.Equal(t, resultA, resultB)
	require.Equal(t, "kb-a", resultA[0].KBID)
	require.Equal(t, "kb-b", resultA[1].KBID)
	require.Equal(t, "kb-c", resultA[2].KBID)
	require.GreaterOrEqual(t, resultA[0].Score, resultA[1].Score)
	require.GreaterOrEqual(t, resultA[1].Score, resultA[2].Score)
	require.NotEmpty(t, resultA[0].MatchedOn)
}

func mustPatternsJSON(t *testing.T, patterns []Pattern) string {
	t.Helper()
	raw, err := json.Marshal(patterns)
	require.NoError(t, err)
	return string(raw)
}
