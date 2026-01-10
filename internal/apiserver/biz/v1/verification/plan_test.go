package verification

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeExpectedAllowList(t *testing.T) {
	logsFallback := normalizeExpected("mcp.query_logs", Expected{Type: "unknown_type"})
	require.Equal(t, "contains_keyword", logsFallback.Type)
	require.Equal(t, "error", logsFallback.Keyword)

	metricsFallback := normalizeExpected("mcp.query_metrics", Expected{Type: "not_allowed"})
	require.Equal(t, "exists", metricsFallback.Type)

	above := normalizeExpected("mcp.query_metrics", Expected{Type: "threshold_above", Field: "rowCount", Value: 3})
	require.Equal(t, "threshold_above", above.Type)
	require.Equal(t, "rowCount", above.Field)
	require.Equal(t, 3.0, above.Value)

	below := normalizeExpected("mcp.query_metrics", Expected{Type: "threshold_below", Field: "value", Value: 2})
	require.Equal(t, "threshold_below", below.Type)
	require.Equal(t, "value", below.Field)
	require.Equal(t, 2.0, below.Value)
}

func TestPlanSizeAndFieldLimit(t *testing.T) {
	hugeQueryJSON := map[string]any{
		"blob": strings.Repeat("x", 9000),
	}

	plan := Plan{
		Version: PlanVersionA5,
		Steps: []Step{
			{
				ID:   strings.Repeat("a", 80),
				Tool: "mcp.query_logs",
				Why:  strings.Repeat("b", 800),
				Params: map[string]any{
					"datasource_id":    "ds-1",
					"query":            strings.Repeat("error ", 300),
					"time_range_start": "2026-01-01T00:00:00Z",
					"time_range_end":   "2026-01-01T00:10:00Z",
					"limit":            100,
					"query_json":       hugeQueryJSON,
					"headers":          map[string]any{"Authorization": "Bearer token"},
				},
				Expected: Expected{Type: "contains_keyword", Keyword: strings.Repeat("error", 80)},
			},
			{
				ID:   "step-2",
				Tool: "mcp.query_metrics",
				Why:  "keep",
				Params: map[string]any{
					"datasource_id":    "ds-2",
					"expr":             "sum(up)",
					"time_range_start": "2026-01-01T00:00:00Z",
					"time_range_end":   "2026-01-01T00:10:00Z",
					"step_seconds":     30,
				},
				Expected: Expected{Type: "exists"},
			},
			{
				ID:   "step-3",
				Tool: "mcp.query_metrics",
				Why:  "keep",
				Params: map[string]any{
					"datasource_id":    "ds-3",
					"expr":             "sum(up)",
					"time_range_start": "2026-01-01T00:00:00Z",
					"time_range_end":   "2026-01-01T00:10:00Z",
					"step_seconds":     30,
				},
				Expected: Expected{Type: "exists"},
			},
			{
				ID:   "step-4",
				Tool: "mcp.query_metrics",
				Why:  "drop due to max steps",
				Params: map[string]any{
					"datasource_id":    "ds-4",
					"expr":             "sum(up)",
					"time_range_start": "2026-01-01T00:00:00Z",
					"time_range_end":   "2026-01-01T00:10:00Z",
				},
				Expected: Expected{Type: "exists"},
			},
		},
	}

	normalized := normalizePlan(plan)
	require.Equal(t, PlanVersionA5, normalized.Version)
	require.NotEmpty(t, normalized.Steps)
	require.LessOrEqual(t, len(normalized.Steps), maxSteps)

	for _, step := range normalized.Steps {
		require.LessOrEqual(t, len([]rune(step.ID)), maxStepIDLen)
		require.LessOrEqual(t, len([]rune(step.Why)), maxWhyLen)
		if query, ok := step.Params["query"].(string); ok {
			require.LessOrEqual(t, len([]rune(query)), maxQueryLen)
		}
		if expr, ok := step.Params["expr"].(string); ok {
			require.LessOrEqual(t, len([]rune(expr)), maxQueryLen)
		}
		require.Contains(t, allowedExpectedTypes, step.Expected.Type)
	}

	raw, err := json.Marshal(normalized)
	require.NoError(t, err)
	require.LessOrEqual(t, len(raw), maxPlanBytes)
	require.NotContains(t, strings.ToLower(string(raw)), "authorization")
	require.NotContains(t, strings.ToLower(string(raw)), "token")
	require.NotContains(t, strings.ToLower(string(raw)), "secret")
	require.NotContains(t, strings.ToLower(string(raw)), "headers")
}
