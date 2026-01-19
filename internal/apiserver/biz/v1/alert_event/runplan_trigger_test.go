package alert_event

import (
	"context"
	"testing"
	"time"

	aijobbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/ai_job"
	alertingpolicy "github.com/aiopsre/rca-api/internal/apiserver/pkg/alerting/policy"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
	"github.com/stretchr/testify/require"
)

func TestMaybeTriggerOnIngestAIJob_BlockedWhenSilenced(t *testing.T) {
	fake := &fakeAIJobBizForRunPlan{}
	biz := &alertEventBiz{runAIJobBiz: fake}
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
	require.Equal(t, 0, fake.runCalls)
}

func TestMaybeTriggerOnIngestAIJob_BlockedWhenSuppressIncident(t *testing.T) {
	fake := &fakeAIJobBizForRunPlan{}
	biz := &alertEventBiz{runAIJobBiz: fake}
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
	require.Equal(t, 0, fake.runCalls)
}

func TestMaybeTriggerOnIngestAIJob_NotBlockedContinuesEvaluateAndRun(t *testing.T) {
	fake := &fakeAIJobBizForRunPlan{
		runResp: &v1.RunAIJobResponse{JobID: "ai-job-on-ingest-1"},
	}
	biz := &alertEventBiz{runAIJobBiz: fake}
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
	require.Equal(t, 1, fake.runCalls)
	require.NotNil(t, fake.lastRunRequest)
	require.Equal(t, "incident-3", fake.lastRunRequest.GetIncidentID())
}

func withEvaluateOnIngestRunPlanForTest(fn func(context.Context, alertingpolicy.EvaluateInput) (alertingpolicy.RunPlan, error)) func() {
	old := evaluateOnIngestRunPlan
	evaluateOnIngestRunPlan = fn
	return func() {
		evaluateOnIngestRunPlan = old
	}
}

type fakeAIJobBizForRunPlan struct {
	runCalls       int
	lastRunRequest *v1.RunAIJobRequest
	runResp        *v1.RunAIJobResponse
	runErr         error
}

var _ aijobbiz.AIJobBiz = (*fakeAIJobBizForRunPlan)(nil)

func (f *fakeAIJobBizForRunPlan) Run(_ context.Context, rq *v1.RunAIJobRequest) (*v1.RunAIJobResponse, error) {
	f.runCalls++
	f.lastRunRequest = rq
	if f.runResp == nil {
		f.runResp = &v1.RunAIJobResponse{JobID: "ai-job-fake"}
	}
	return f.runResp, f.runErr
}

func (f *fakeAIJobBizForRunPlan) Get(context.Context, *v1.GetAIJobRequest) (*v1.GetAIJobResponse, error) {
	return nil, nil
}

func (f *fakeAIJobBizForRunPlan) List(context.Context, *v1.ListAIJobsRequest) (*v1.ListAIJobsResponse, error) {
	return nil, nil
}

func (f *fakeAIJobBizForRunPlan) QueueSignalVersion(context.Context) (int64, error) {
	return 0, nil
}

func (f *fakeAIJobBizForRunPlan) ListByIncident(context.Context, *v1.ListIncidentAIJobsRequest) (*v1.ListIncidentAIJobsResponse, error) {
	return nil, nil
}

func (f *fakeAIJobBizForRunPlan) Start(context.Context, *v1.StartAIJobRequest) (*v1.StartAIJobResponse, error) {
	return nil, nil
}

func (f *fakeAIJobBizForRunPlan) Renew(context.Context, *v1.StartAIJobRequest) (*v1.StartAIJobResponse, error) {
	return nil, nil
}

func (f *fakeAIJobBizForRunPlan) Cancel(context.Context, *v1.CancelAIJobRequest) (*v1.CancelAIJobResponse, error) {
	return nil, nil
}

func (f *fakeAIJobBizForRunPlan) Finalize(context.Context, *v1.FinalizeAIJobRequest) (*v1.FinalizeAIJobResponse, error) {
	return nil, nil
}

func (f *fakeAIJobBizForRunPlan) CreateToolCall(context.Context, *v1.CreateAIToolCallRequest) (*v1.CreateAIToolCallResponse, error) {
	return nil, nil
}

func (f *fakeAIJobBizForRunPlan) ListToolCalls(context.Context, *v1.ListAIToolCallsRequest) (*v1.ListAIToolCallsResponse, error) {
	return nil, nil
}

func (f *fakeAIJobBizForRunPlan) SearchToolCalls(context.Context, *aijobbiz.SearchToolCallsRequest) (*aijobbiz.SearchToolCallsResponse, error) {
	return nil, nil
}

func (f *fakeAIJobBizForRunPlan) RecordToolCallAudit(context.Context, *aijobbiz.RecordToolCallAuditRequest) (string, error) {
	return "", nil
}
