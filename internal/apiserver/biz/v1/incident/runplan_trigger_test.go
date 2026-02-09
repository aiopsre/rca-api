package incident

import (
	"context"
	"testing"
	"time"

	aijobbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/ai_job"
	triggerbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/trigger"
	"github.com/aiopsre/rca-api/internal/apiserver/model"
	alertingpolicy "github.com/aiopsre/rca-api/internal/apiserver/pkg/alerting/policy"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
	"github.com/stretchr/testify/require"
)

func TestMaybeTriggerOnEscalationAIJob_BlockedWhenTerminal(t *testing.T) {
	fake := &fakeAIJobBizForEscalationRunPlan{}
	biz := &incidentBiz{runAIJobBiz: fake}
	incident := &model.IncidentM{
		IncidentID: "incident-terminal",
		Status:     "resolved",
		Severity:   "P1",
	}

	evalCalled := false
	restore := withEvaluateOnIncidentRunPlanForTest(func(_ context.Context, _ alertingpolicy.EvaluateInput) (alertingpolicy.RunPlan, error) {
		evalCalled = true
		return alertingpolicy.RunPlan{}, nil
	})
	defer restore()

	biz.maybeTriggerOnEscalationAIJob(context.Background(), incident, "open", "P3", "v1")

	require.False(t, evalCalled)
	require.Equal(t, 0, fake.runCalls)
}

func TestMaybeTriggerOnEscalationAIJob_BlockedWhenNoOp(t *testing.T) {
	version := "v1"
	fake := &fakeAIJobBizForEscalationRunPlan{}
	biz := &incidentBiz{runAIJobBiz: fake}
	incident := &model.IncidentM{
		IncidentID: "incident-noop",
		Status:     "open",
		Severity:   "P1",
		Version:    &version,
	}

	evalCalled := false
	restore := withEvaluateOnIncidentRunPlanForTest(func(_ context.Context, _ alertingpolicy.EvaluateInput) (alertingpolicy.RunPlan, error) {
		evalCalled = true
		return alertingpolicy.RunPlan{}, nil
	})
	defer restore()

	biz.maybeTriggerOnEscalationAIJob(context.Background(), incident, "open", "P1", "v1")

	require.False(t, evalCalled)
	require.Equal(t, 0, fake.runCalls)
}

func TestMaybeTriggerOnEscalationAIJob_NotBlockedContinuesEvaluateAndRun(t *testing.T) {
	fake := &fakeAIJobBizForEscalationRunPlan{
		runResp: &v1.RunAIJobResponse{JobID: "ai-job-on-escalation-1"},
	}
	biz := &incidentBiz{runAIJobBiz: fake}
	incident := &model.IncidentM{
		IncidentID: "incident-run",
		Status:     "open",
		Severity:   "P1",
	}
	now := time.Now().UTC().Truncate(time.Second)
	evalCalled := false
	restore := withEvaluateOnIncidentRunPlanForTest(func(_ context.Context, _ alertingpolicy.EvaluateInput) (alertingpolicy.RunPlan, error) {
		evalCalled = true
		return alertingpolicy.RunPlan{
			ShouldRun:      true,
			Decision:       "run",
			Trigger:        alertingpolicy.TriggerOnEscalation,
			Pipeline:       "basic_rca",
			CreatedBy:      "system",
			TimeRangeStart: now.Add(-120 * time.Minute),
			TimeRangeEnd:   now,
		}, nil
	})
	defer restore()

	biz.maybeTriggerOnEscalationAIJob(context.Background(), incident, "open", "P2", "v1")

	require.True(t, evalCalled)
	require.Equal(t, 1, fake.runCalls)
	require.NotNil(t, fake.lastRunRequest)
	require.Equal(t, "incident-run", fake.lastRunRequest.GetIncidentID())
}

func TestTriggerScheduledRunWithIncident_BlockedWhenTerminal(t *testing.T) {
	fake := &fakeAIJobBizForEscalationRunPlan{}
	biz := &incidentBiz{runAIJobBiz: fake}
	incident := &model.IncidentM{
		IncidentID: "incident-scheduled-terminal",
		Status:     "closed",
		Severity:   "P1",
	}
	req := &TriggerScheduledRunRequest{
		IncidentID:    incident.IncidentID,
		SchedulerName: strPtrForTest("nightly"),
	}

	evalCalled := false
	restore := withEvaluateOnIncidentRunPlanForTest(func(_ context.Context, _ alertingpolicy.EvaluateInput) (alertingpolicy.RunPlan, error) {
		evalCalled = true
		return alertingpolicy.RunPlan{}, nil
	})
	defer restore()

	resp, err := biz.triggerScheduledRunWithIncident(context.Background(), incident, req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	require.False(t, resp.ShouldRun)
	require.Equal(t, triggerDecisionBlockedTerminal, resp.Decision)
	require.Equal(t, alertingpolicy.TriggerScheduled, resp.Trigger)
	require.False(t, evalCalled)
	require.Equal(t, 0, fake.runCalls)
}

func TestTriggerScheduledRunWithIncident_NotTerminalContinuesEvaluateAndRun(t *testing.T) {
	fake := &fakeAIJobBizForEscalationRunPlan{
		runResp: &v1.RunAIJobResponse{JobID: "ai-job-scheduled-1"},
	}
	biz := &incidentBiz{runAIJobBiz: fake}
	incident := &model.IncidentM{
		IncidentID: "incident-scheduled-open",
		Status:     "open",
		Severity:   "P2",
	}
	now := time.Now().UTC().Truncate(time.Second)
	req := &TriggerScheduledRunRequest{
		IncidentID:    incident.IncidentID,
		SchedulerName: strPtrForTest("hourly"),
	}

	evalCalled := false
	restore := withEvaluateOnIncidentRunPlanForTest(func(_ context.Context, in alertingpolicy.EvaluateInput) (alertingpolicy.RunPlan, error) {
		evalCalled = true
		require.Equal(t, alertingpolicy.TriggerScheduled, in.Trigger)
		return alertingpolicy.RunPlan{
			ShouldRun:      true,
			Decision:       "run",
			Trigger:        alertingpolicy.TriggerScheduled,
			Pipeline:       "basic_rca",
			CreatedBy:      "scheduler:hourly",
			TimeRangeStart: now.Add(-time.Hour),
			TimeRangeEnd:   now,
		}, nil
	})
	defer restore()

	resp, err := biz.triggerScheduledRunWithIncident(context.Background(), incident, req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	require.True(t, resp.ShouldRun)
	require.Equal(t, "run", resp.Decision)
	require.True(t, evalCalled)
	require.Equal(t, 1, fake.runCalls)
	require.NotNil(t, fake.lastRunRequest)
	require.Equal(t, incident.IncidentID, fake.lastRunRequest.GetIncidentID())
}

func TestRunScheduledPlan_UsesTriggerRouterWhenAvailable(t *testing.T) {
	aiJobFake := &fakeAIJobBizForEscalationRunPlan{}
	triggerFake := &fakeTriggerRouter{
		resp: &triggerbiz.TriggerResult{
			JobID: "ai-job-cron-router-1",
		},
	}
	biz := &incidentBiz{
		runAIJobBiz: aiJobFake,
		triggerBiz:  triggerFake,
	}

	now := time.Now().UTC().Truncate(time.Second)
	plan := alertingpolicy.RunPlan{
		ShouldRun:      true,
		Decision:       "run",
		Trigger:        alertingpolicy.TriggerScheduled,
		Pipeline:       "basic_rca",
		CreatedBy:      "scheduler:hourly",
		TimeRangeStart: now.Add(-time.Hour),
		TimeRangeEnd:   now,
		RuleName:       "hourly_cron",
		PolicySource:   "policy-default",
	}
	resp := &TriggerScheduledRunResponse{
		ShouldRun: true,
		Decision:  "run",
		Trigger:   plan.Trigger,
		Pipeline:  plan.Pipeline,
		CreatedBy: plan.CreatedBy,
	}
	out, err := biz.runScheduledPlan(context.Background(), "incident-cron-1", plan, resp)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotNil(t, out.JobID)
	require.Equal(t, "ai-job-cron-router-1", *out.JobID)
	require.Equal(t, 1, triggerFake.calls)
	require.NotNil(t, triggerFake.lastReq)
	require.Equal(t, triggerbiz.TriggerTypeCron, triggerFake.lastReq.TriggerType)
	require.Equal(t, "incident_scheduler", triggerFake.lastReq.Source)
	require.NotNil(t, triggerFake.lastReq.IncidentHint)
	require.Equal(t, "incident-cron-1", triggerFake.lastReq.IncidentHint.IncidentID)
	require.NotNil(t, triggerFake.lastReq.RunRequest)
	require.Equal(t, "incident-cron-1", triggerFake.lastReq.RunRequest.GetIncidentID())
	require.Equal(t, 0, aiJobFake.runCalls)
}

func withEvaluateOnIncidentRunPlanForTest(
	fn func(context.Context, alertingpolicy.EvaluateInput) (alertingpolicy.RunPlan, error),
) func() {
	old := evaluateOnIncidentRunPlan
	evaluateOnIncidentRunPlan = fn
	return func() {
		evaluateOnIncidentRunPlan = old
	}
}

type fakeAIJobBizForEscalationRunPlan struct {
	runCalls       int
	lastRunRequest *v1.RunAIJobRequest
	runResp        *v1.RunAIJobResponse
	runErr         error
}

var _ aijobbiz.AIJobBiz = (*fakeAIJobBizForEscalationRunPlan)(nil)

func (f *fakeAIJobBizForEscalationRunPlan) Run(
	_ context.Context,
	rq *v1.RunAIJobRequest,
) (*v1.RunAIJobResponse, error) {
	f.runCalls++
	f.lastRunRequest = rq
	if f.runResp == nil {
		f.runResp = &v1.RunAIJobResponse{JobID: "ai-job-fake"}
	}
	return f.runResp, f.runErr
}

func (f *fakeAIJobBizForEscalationRunPlan) Get(context.Context, *v1.GetAIJobRequest) (*v1.GetAIJobResponse, error) {
	return nil, nil
}

func (f *fakeAIJobBizForEscalationRunPlan) List(context.Context, *v1.ListAIJobsRequest) (*v1.ListAIJobsResponse, error) {
	return nil, nil
}

func (f *fakeAIJobBizForEscalationRunPlan) QueueSignalVersion(context.Context) (int64, error) {
	return 0, nil
}

func (f *fakeAIJobBizForEscalationRunPlan) ListByIncident(
	context.Context,
	*v1.ListIncidentAIJobsRequest,
) (*v1.ListIncidentAIJobsResponse, error) {
	return nil, nil
}

func (f *fakeAIJobBizForEscalationRunPlan) Start(
	context.Context,
	*v1.StartAIJobRequest,
) (*v1.StartAIJobResponse, error) {
	return nil, nil
}

func (f *fakeAIJobBizForEscalationRunPlan) Renew(
	context.Context,
	*v1.StartAIJobRequest,
) (*v1.StartAIJobResponse, error) {
	return nil, nil
}

func (f *fakeAIJobBizForEscalationRunPlan) Cancel(
	context.Context,
	*v1.CancelAIJobRequest,
) (*v1.CancelAIJobResponse, error) {
	return nil, nil
}

func (f *fakeAIJobBizForEscalationRunPlan) Finalize(
	context.Context,
	*v1.FinalizeAIJobRequest,
) (*v1.FinalizeAIJobResponse, error) {
	return nil, nil
}

func (f *fakeAIJobBizForEscalationRunPlan) CreateToolCall(
	context.Context,
	*v1.CreateAIToolCallRequest,
) (*v1.CreateAIToolCallResponse, error) {
	return nil, nil
}

func (f *fakeAIJobBizForEscalationRunPlan) ListToolCalls(
	context.Context,
	*v1.ListAIToolCallsRequest,
) (*v1.ListAIToolCallsResponse, error) {
	return nil, nil
}

func (f *fakeAIJobBizForEscalationRunPlan) SearchToolCalls(
	context.Context,
	*aijobbiz.SearchToolCallsRequest,
) (*aijobbiz.SearchToolCallsResponse, error) {
	return nil, nil
}

func (f *fakeAIJobBizForEscalationRunPlan) RecordToolCallAudit(
	context.Context,
	*aijobbiz.RecordToolCallAuditRequest,
) (string, error) {
	return "", nil
}

func (f *fakeAIJobBizForEscalationRunPlan) GetTraceReadModel(
	context.Context,
	*aijobbiz.GetTraceReadModelRequest,
) (*aijobbiz.GetTraceReadModelResponse, error) {
	return nil, nil
}

func (f *fakeAIJobBizForEscalationRunPlan) ListTraceReadModels(
	context.Context,
	*aijobbiz.ListTraceReadModelsRequest,
) (*aijobbiz.ListTraceReadModelsResponse, error) {
	return nil, nil
}

func (f *fakeAIJobBizForEscalationRunPlan) CompareTraceReadModels(
	context.Context,
	*aijobbiz.CompareTraceReadModelsRequest,
) (*aijobbiz.CompareTraceReadModelsResponse, error) {
	return nil, nil
}

func (f *fakeAIJobBizForEscalationRunPlan) GetSessionWorkbench(
	context.Context,
	*aijobbiz.GetSessionWorkbenchRequest,
) (*aijobbiz.GetSessionWorkbenchResponse, error) {
	return nil, nil
}

func (f *fakeAIJobBizForEscalationRunPlan) ListOperatorInbox(
	context.Context,
	*aijobbiz.ListOperatorInboxRequest,
) (*aijobbiz.ListOperatorInboxResponse, error) {
	return nil, nil
}

func strPtrForTest(v string) *string {
	value := v
	return &value
}

type fakeTriggerRouter struct {
	calls   int
	lastReq *triggerbiz.TriggerRequest
	resp    *triggerbiz.TriggerResult
	err     error
}

func (f *fakeTriggerRouter) Dispatch(
	_ context.Context,
	rq *triggerbiz.TriggerRequest,
) (*triggerbiz.TriggerResult, error) {
	f.calls++
	f.lastReq = rq
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}
