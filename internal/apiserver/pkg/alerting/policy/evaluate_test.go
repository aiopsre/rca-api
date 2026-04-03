package policy

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEvaluate_OnIngestMatchBuildsRunPlan(t *testing.T) {
	cfg := DefaultPolicyConfig()
	cfg.Triggers.OnIngest.Rules = []TriggerRule{
		{
			Name: "ingest-match",
			Match: RuleMatch{
				AlertName: "cpu_high",
				Labels: map[string][]string{
					"service": []string{"checkout"},
				},
			},
			Action: TriggerAction{
				Run:           true,
				Pipeline:      "basic_rca",
				WindowSeconds: 1800,
			},
		},
	}
	withRuntimePolicyForTest(t, cfg)

	alertTime := time.Date(2026, 2, 27, 12, 0, 0, 0, time.UTC)
	plan, err := Evaluate(context.Background(), EvaluateInput{
		Trigger:        TriggerOnIngest,
		IncidentID:     "incident-1",
		AlertName:      "cpu_high",
		Labels:         map[string]string{"service": "checkout"},
		AlertTime:      &alertTime,
		IdempotencyKey: " idem-1 ",
	})
	require.NoError(t, err)
	require.True(t, plan.ShouldRun)
	require.Equal(t, TriggerOnIngest, plan.Trigger)
	require.Equal(t, "basic_rca", plan.Pipeline)
	require.Equal(t, "system", plan.CreatedBy)
	require.Equal(t, alertTime, plan.TimeRangeEnd)
	require.Equal(t, 1800*time.Second, plan.TimeRangeEnd.Sub(plan.TimeRangeStart))
	require.NotNil(t, plan.IdempotencyKey)
	require.Equal(t, "idem-1", *plan.IdempotencyKey)
}

func TestEvaluate_ScheduledBucketAlignment(t *testing.T) {
	cfg := DefaultPolicyConfig()
	cfg.Triggers.Scheduled.Rules = []TriggerRule{
		{
			Name:  "scheduled-match",
			Match: RuleMatch{},
			Action: TriggerAction{
				Run:                      true,
				WindowSeconds:            3600,
				IdempotencyBucketSeconds: 3600,
			},
		},
	}
	withRuntimePolicyForTest(t, cfg)

	now := time.Date(2026, 2, 27, 15, 37, 31, 0, time.UTC)
	plan, err := Evaluate(context.Background(), EvaluateInput{
		Trigger:       TriggerScheduled,
		IncidentID:    "incident-2",
		SchedulerName: "nightly",
		Now:           now,
	})
	require.NoError(t, err)
	require.True(t, plan.ShouldRun)
	require.Equal(t, TriggerScheduled, plan.Trigger)
	require.Equal(t, "scheduler:nightly", plan.CreatedBy)
	require.Equal(t, int64(0), plan.TimeRangeStart.Unix()%3600)
	require.Equal(t, int64(0), plan.TimeRangeEnd.Unix()%3600)
	require.Equal(t, 3600*time.Second, plan.TimeRangeEnd.Sub(plan.TimeRangeStart))
	require.NotNil(t, plan.IdempotencyKey)
	require.NotEmpty(t, *plan.IdempotencyKey)
}

func TestEvaluate_RejectsInvalidTrigger(t *testing.T) {
	_, err := Evaluate(context.Background(), EvaluateInput{Trigger: ""})
	require.Error(t, err)

	_, err = Evaluate(context.Background(), EvaluateInput{Trigger: "unknown"})
	require.Error(t, err)
}

func TestEvaluate_NormalizesEmptyIdempotency(t *testing.T) {
	cfg := DefaultPolicyConfig()
	cfg.Triggers.OnEscalation.Rules = []TriggerRule{
		{
			Name: "escalation-match",
			Match: RuleMatch{
				IncidentSeverity: []string{"p1"},
			},
			Action: TriggerAction{
				Run: true,
			},
		},
	}
	withRuntimePolicyForTest(t, cfg)

	plan, err := Evaluate(context.Background(), EvaluateInput{
		Trigger:          TriggerOnEscalation,
		IncidentID:       "incident-3",
		IncidentSeverity: "P1",
		IdempotencyKey:   "   ",
	})
	require.NoError(t, err)
	require.True(t, plan.ShouldRun)
	require.Nil(t, plan.IdempotencyKey)
}

func withRuntimePolicyForTest(t *testing.T, cfg PolicyConfig) {
	t.Helper()
	old := CurrentRuntimeConfig()
	SetRuntimeConfig(RuntimeConfig{
		Policy:       cfg,
		Source:       RuleSourceDynamicDB,
		ActiveSource: PolicyActiveSourceDynamicDB,
	})
	t.Cleanup(func() {
		SetRuntimeConfig(old)
	})
}
