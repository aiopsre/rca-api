package alert_event

import (
	"context"
	"testing"
	"time"

	triggerbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/trigger"
	alertingpolicy "github.com/aiopsre/rca-api/internal/apiserver/pkg/alerting/policy"
	"github.com/stretchr/testify/require"
)

func TestMaybeTriggerOnIngestAIJob_BlockedWhenSilenced(t *testing.T) {
	fake := &fakeTriggerBizForRunPlan{}
	biz := &alertEventBiz{triggerBiz: fake}
	in := &ingestInput{
		alertName:  "cpu_high",
		severity:   "p1",
		lastSeenAt: time.Now().UTC(),
	}

	evalCalled := false
	restore := withEvaluateOnIngestRunPlanForTest(func(_ context.Context, _ alertingpolicy.EvaluateInput) (alertingpolicy.RunPlan, error) {
		evalCalled = true
		return alertingpolicy.RunPlan{}, nil
	})
	defer restore()

	biz.maybeTriggerOnIngestAIJob(context.Background(), "incident-1", in, "current_created", true, false)

	require.False(t, evalCalled)
	require.Equal(t, 0, fake.dispatchCalls)
}

func TestMaybeTriggerOnIngestAIJob_BlockedWhenSuppressIncident(t *testing.T) {
	fake := &fakeTriggerBizForRunPlan{}
	biz := &alertEventBiz{triggerBiz: fake}
	in := &ingestInput{
		alertName:  "cpu_high",
		severity:   "p1",
		lastSeenAt: time.Now().UTC(),
	}

	evalCalled := false
	restore := withEvaluateOnIngestRunPlanForTest(func(_ context.Context, _ alertingpolicy.EvaluateInput) (alertingpolicy.RunPlan, error) {
		evalCalled = true
		return alertingpolicy.RunPlan{}, nil
	})
	defer restore()

	biz.maybeTriggerOnIngestAIJob(context.Background(), "incident-2", in, "current_updated", false, true)

	require.False(t, evalCalled)
	require.Equal(t, 0, fake.dispatchCalls)
}

func TestMaybeTriggerOnIngestAIJob_NotBlockedContinuesEvaluateAndRun(t *testing.T) {
	fake := &fakeTriggerBizForRunPlan{
		dispatchResp: &triggerbiz.TriggerResult{JobID: "ai-job-on-ingest-1"},
	}
	biz := &alertEventBiz{triggerBiz: fake}
	now := time.Now().UTC().Truncate(time.Second)
	in := &ingestInput{
		alertName:  "cpu_high",
		severity:   "p1",
		lastSeenAt: now,
	}

	evalCalled := false
	restore := withEvaluateOnIngestRunPlanForTest(func(_ context.Context, _ alertingpolicy.EvaluateInput) (alertingpolicy.RunPlan, error) {
		evalCalled = true
		return alertingpolicy.RunPlan{
			ShouldRun:      true,
			Decision:       "run",
			Trigger:        alertingpolicy.TriggerOnIngest,
			Pipeline:       "basic_rca",
			CreatedBy:      "system",
			TimeRangeStart: now.Add(-60 * time.Minute),
			TimeRangeEnd:   now,
		}, nil
	})
	defer restore()

	biz.maybeTriggerOnIngestAIJob(context.Background(), "incident-3", in, "current_updated", false, false)

	require.True(t, evalCalled)
	require.Equal(t, 1, fake.dispatchCalls)
	require.NotNil(t, fake.lastDispatchRequest)
	require.Equal(t, "incident-3", fake.lastDispatchRequest.IncidentHint.IncidentID)
	require.NotNil(t, fake.lastDispatchRequest.RunRequest)
	require.Equal(t, "incident-3", fake.lastDispatchRequest.RunRequest.GetIncidentID())
	require.Equal(t, "alert_ingest", fake.lastDispatchRequest.Source)
	require.Equal(t, triggerbiz.TriggerTypeAlert, fake.lastDispatchRequest.TriggerType)
}

func withEvaluateOnIngestRunPlanForTest(fn func(context.Context, alertingpolicy.EvaluateInput) (alertingpolicy.RunPlan, error)) func() {
	old := evaluateOnIngestRunPlan
	evaluateOnIngestRunPlan = fn
	return func() {
		evaluateOnIngestRunPlan = old
	}
}

type fakeTriggerBizForRunPlan struct {
	dispatchCalls       int
	lastDispatchRequest *triggerbiz.TriggerRequest
	dispatchResp        *triggerbiz.TriggerResult
	dispatchErr         error
}

var _ triggerbiz.TriggerBiz = (*fakeTriggerBizForRunPlan)(nil)

func (f *fakeTriggerBizForRunPlan) Dispatch(
	_ context.Context,
	rq *triggerbiz.TriggerRequest,
) (*triggerbiz.TriggerResult, error) {
	f.dispatchCalls++
	f.lastDispatchRequest = rq
	if f.dispatchResp == nil {
		f.dispatchResp = &triggerbiz.TriggerResult{
			IncidentID: "incident-fake",
			JobID:      "ai-job-fake",
			Pipeline:   "basic_rca",
			Created:    true,
			Message:    "trigger_routed",
		}
	}
	return f.dispatchResp, f.dispatchErr
}
