package trigger

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/onexstack/onexstack/pkg/errorsx"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	sessionbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/session"
	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/pkg/contextx"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

func TestDispatchManual_EnsuresSessionAndRunsAIJob(t *testing.T) {
	incident := &model.IncidentM{
		IncidentID:   "incident-1",
		Service:      "checkout",
		WorkloadName: "checkout-api",
	}
	incidentStore := &fakeIncidentStore{incident: incident}
	runner := &fakeAIJobRunner{resp: &v1.RunAIJobResponse{JobID: "ai-job-1"}}
	sessionSvc := &fakeSessionEnsurer{
		resp: &sessionbiz.ResolveOrCreateResponse{
			Session: &model.SessionContextM{SessionID: "session-1"},
		},
	}
	biz := newWithDeps(incidentStore, runner, sessionSvc)

	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-30 * time.Minute)
	resp, err := biz.Dispatch(context.Background(), &TriggerRequest{
		TriggerType: TriggerTypeManual,
		Source:      "manual_api",
		BusinessKey: incident.IncidentID,
		IncidentHint: &IncidentHint{
			IncidentID: incident.IncidentID,
		},
		DesiredPipeline: strPtr("basic_rca"),
		Initiator:       strPtr("user:tester"),
		TimeRange: &TriggerTimeRange{
			Start: start,
			End:   end,
		},
		RunRequest: &v1.RunAIJobRequest{
			IncidentID:     incident.IncidentID,
			IdempotencyKey: strPtr("manual-idem-1"),
			Trigger:        strPtr("manual"),
			TimeRangeStart: timestamppb.New(start),
			TimeRangeEnd:   timestamppb.New(end),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, "session-1", resp.SessionID)
	require.Equal(t, incident.IncidentID, resp.IncidentID)
	require.Equal(t, "ai-job-1", resp.JobID)
	require.Equal(t, "basic_rca", resp.Pipeline)
	require.True(t, resp.Created)

	require.Equal(t, 1, sessionSvc.calls)
	require.Equal(t, incident.IncidentID, sessionSvc.lastReq.IncidentID)
	require.Equal(t, 1, runner.calls)
	require.NotNil(t, runner.lastReq)
	require.Equal(t, incident.IncidentID, runner.lastReq.GetIncidentID())
	require.Equal(t, "manual", runner.lastReq.GetTrigger())
	require.Equal(t, TriggerTypeManual, runner.lastTriggerType)
	require.Equal(t, "manual_api", runner.lastTriggerSource)
	require.Equal(t, "user:tester", runner.lastInitiator)
}

func TestDispatchAlert_DefaultsPipelineAndTrigger(t *testing.T) {
	incident := &model.IncidentM{IncidentID: "incident-2"}
	incidentStore := &fakeIncidentStore{incident: incident}
	runner := &fakeAIJobRunner{resp: &v1.RunAIJobResponse{JobID: "ai-job-2"}}
	sessionSvc := &fakeSessionEnsurer{}
	biz := newWithDeps(incidentStore, runner, sessionSvc)

	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-time.Hour)
	resp, err := biz.Dispatch(context.Background(), &TriggerRequest{
		TriggerType: TriggerTypeAlert,
		Source:      "alert_ingest",
		BusinessKey: incident.IncidentID,
		IncidentHint: &IncidentHint{
			IncidentID: incident.IncidentID,
		},
		TimeRange: &TriggerTimeRange{
			Start: start,
			End:   end,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, "ai-job-2", resp.JobID)
	require.Equal(t, "basic_rca", runner.lastReq.GetPipeline())
	require.Equal(t, "on_ingest", runner.lastReq.GetTrigger())
	require.Equal(t, incident.IncidentID, runner.lastReq.GetIncidentID())
	require.Equal(t, TriggerTypeAlert, runner.lastTriggerType)
	require.Equal(t, "alert_ingest", runner.lastTriggerSource)
}

func TestDispatchReplay_ResolvesIncidentFromSessionID(t *testing.T) {
	incident := &model.IncidentM{IncidentID: "incident-replay-1"}
	incidentStore := &fakeIncidentStore{incident: incident}
	runner := &fakeAIJobRunner{resp: &v1.RunAIJobResponse{JobID: "ai-job-replay-1"}}
	sessionSvc := &fakeSessionEnsurer{
		getResp: &sessionbiz.GetSessionContextResponse{
			Session: &model.SessionContextM{
				SessionID:   "session-replay-1",
				SessionType: sessionbiz.SessionTypeIncident,
				BusinessKey: incident.IncidentID,
				IncidentID:  strPtr(incident.IncidentID),
			},
		},
	}
	biz := newWithDeps(incidentStore, runner, sessionSvc)

	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-45 * time.Minute)
	resp, err := biz.Dispatch(context.Background(), &TriggerRequest{
		TriggerType: TriggerTypeReplay,
		Source:      "replay_api",
		BusinessKey: "replay:session-replay-1",
		SessionHint: &SessionHint{
			SessionID: "session-replay-1",
		},
		Initiator: strPtr("user:replay"),
		TimeRange: &TriggerTimeRange{
			Start: start,
			End:   end,
		},
		RunRequest: &v1.RunAIJobRequest{
			TimeRangeStart: timestamppb.New(start),
			TimeRangeEnd:   timestamppb.New(end),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, incident.IncidentID, resp.IncidentID)
	require.Equal(t, "session-replay-1", resp.SessionID)
	require.Equal(t, "ai-job-replay-1", resp.JobID)

	require.Equal(t, 0, sessionSvc.calls)
	require.Equal(t, 1, sessionSvc.getCalls)
	require.NotNil(t, runner.lastReq)
	require.Equal(t, incident.IncidentID, runner.lastReq.GetIncidentID())
	require.Equal(t, TriggerTypeReplay, runner.lastReq.GetTrigger())
	require.Equal(t, TriggerTypeReplay, runner.lastTriggerType)
	require.Equal(t, "replay_api", runner.lastTriggerSource)
	require.Equal(t, "user:replay", runner.lastInitiator)
}

func TestDispatchFollowUp_UsesIncidentAndEnsuresSession(t *testing.T) {
	incident := &model.IncidentM{IncidentID: "incident-follow-up-1"}
	incidentStore := &fakeIncidentStore{incident: incident}
	runner := &fakeAIJobRunner{resp: &v1.RunAIJobResponse{JobID: "ai-job-follow-up-1"}}
	sessionSvc := &fakeSessionEnsurer{
		resp: &sessionbiz.ResolveOrCreateResponse{
			Session: &model.SessionContextM{
				SessionID: "session-follow-up-1",
			},
		},
	}
	biz := newWithDeps(incidentStore, runner, sessionSvc)

	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-30 * time.Minute)
	resp, err := biz.Dispatch(context.Background(), &TriggerRequest{
		TriggerType: TriggerTypeFollowUp,
		Source:      "follow_up_api",
		BusinessKey: incident.IncidentID,
		IncidentHint: &IncidentHint{
			IncidentID: incident.IncidentID,
		},
		TimeRange: &TriggerTimeRange{
			Start: start,
			End:   end,
		},
		RunRequest: &v1.RunAIJobRequest{
			TimeRangeStart: timestamppb.New(start),
			TimeRangeEnd:   timestamppb.New(end),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, "session-follow-up-1", resp.SessionID)
	require.Equal(t, incident.IncidentID, resp.IncidentID)
	require.Equal(t, "ai-job-follow-up-1", resp.JobID)

	require.Equal(t, 1, sessionSvc.calls)
	require.Equal(t, 0, sessionSvc.getCalls)
	require.NotNil(t, runner.lastReq)
	require.Equal(t, TriggerTypeFollowUp, runner.lastReq.GetTrigger())
	require.Equal(t, TriggerTypeFollowUp, runner.lastTriggerType)
	require.Equal(t, "follow_up_api", runner.lastTriggerSource)
}

func TestDispatchCron_BusinessKeySessionAndIncidentCreate(t *testing.T) {
	incidentStore := &fakeIncidentStore{}
	runner := &fakeAIJobRunner{resp: &v1.RunAIJobResponse{JobID: "ai-job-cron-1"}}
	sessionSvc := &fakeSessionEnsurer{
		resolveResp: &sessionbiz.ResolveOrCreateResponse{
			Session: &model.SessionContextM{
				SessionID:   "session-cron-1",
				SessionType: sessionbiz.SessionTypeService,
				BusinessKey: "service:checkout:env:prod:ns:payments:tenant:tenant-a",
			},
		},
	}
	biz := newWithDeps(incidentStore, runner, sessionSvc)

	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-15 * time.Minute)
	resp, err := biz.Dispatch(context.Background(), &TriggerRequest{
		TriggerType: TriggerTypeCron,
		Source:      "cron_router",
		Payload: map[string]any{
			"service":     "checkout",
			"namespace":   "payments",
			"environment": "prod",
			"tenant":      "tenant-a",
		},
		Initiator: strPtr("system:cron"),
		TimeRange: &TriggerTimeRange{
			Start: start,
			End:   end,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, "session-cron-1", resp.SessionID)
	require.Equal(t, "incident-created-1", resp.IncidentID)
	require.Equal(t, "ai-job-cron-1", resp.JobID)
	require.Equal(t, defaultPipeline, resp.Pipeline)

	require.Equal(t, 1, incidentStore.createCalls)
	require.NotNil(t, incidentStore.createdIncident)
	require.Equal(t, "checkout", incidentStore.createdIncident.Service)
	require.Equal(t, "payments", incidentStore.createdIncident.Namespace)
	require.Equal(t, "prod", incidentStore.createdIncident.Environment)
	require.Equal(t, 1, sessionSvc.resolveCalls)
	require.NotNil(t, sessionSvc.lastResolve)
	require.Equal(t, sessionbiz.SessionTypeService, sessionSvc.lastResolve.SessionType)
	require.Equal(t, "service:checkout:env:prod:ns:payments:tenant:tenant-a", sessionSvc.lastResolve.BusinessKey)
	require.Equal(t, 1, sessionSvc.updateCalls)
	require.NotNil(t, sessionSvc.lastUpdate)
	require.Equal(t, "session-cron-1", sessionSvc.lastUpdate.SessionID)
	require.Equal(t, "incident-created-1", *sessionSvc.lastUpdate.IncidentID)
	require.Equal(t, TriggerTypeCron, runner.lastReq.GetTrigger())
	require.Equal(t, TriggerTypeCron, runner.lastTriggerType)
	require.Equal(t, "cron_router", runner.lastTriggerSource)
}

func TestDispatchChange_BusinessKeySessionAndIncidentCreate(t *testing.T) {
	incidentStore := &fakeIncidentStore{}
	runner := &fakeAIJobRunner{resp: &v1.RunAIJobResponse{JobID: "ai-job-change-1"}}
	sessionSvc := &fakeSessionEnsurer{
		resolveResp: &sessionbiz.ResolveOrCreateResponse{
			Session: &model.SessionContextM{
				SessionID:   "session-change-1",
				SessionType: sessionbiz.SessionTypeChange,
				BusinessKey: "change:chg-20260307-001",
			},
		},
	}
	biz := newWithDeps(incidentStore, runner, sessionSvc)

	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-20 * time.Minute)
	resp, err := biz.Dispatch(context.Background(), &TriggerRequest{
		TriggerType: TriggerTypeChange,
		Source:      "change_router",
		Payload: map[string]any{
			"change_id":   "chg-20260307-001",
			"service":     "checkout",
			"namespace":   "payments",
			"environment": "prod",
			"release_id":  "release-42",
		},
		Initiator: strPtr("system:change"),
		TimeRange: &TriggerTimeRange{
			Start: start,
			End:   end,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, "session-change-1", resp.SessionID)
	require.Equal(t, "incident-created-1", resp.IncidentID)
	require.Equal(t, "ai-job-change-1", resp.JobID)
	require.Equal(t, defaultPipeline, resp.Pipeline)

	require.Equal(t, 1, incidentStore.createCalls)
	require.NotNil(t, incidentStore.createdIncident)
	require.NotNil(t, incidentStore.createdIncident.ChangeID)
	require.Equal(t, "chg-20260307-001", *incidentStore.createdIncident.ChangeID)
	require.NotNil(t, incidentStore.createdIncident.Version)
	require.Equal(t, "release-42", *incidentStore.createdIncident.Version)
	require.Equal(t, 1, sessionSvc.resolveCalls)
	require.NotNil(t, sessionSvc.lastResolve)
	require.Equal(t, sessionbiz.SessionTypeChange, sessionSvc.lastResolve.SessionType)
	require.Equal(t, "change:chg-20260307-001", sessionSvc.lastResolve.BusinessKey)
	require.Equal(t, 1, sessionSvc.updateCalls)
	require.NotNil(t, sessionSvc.lastUpdate)
	require.Equal(t, "session-change-1", sessionSvc.lastUpdate.SessionID)
	require.Equal(t, "incident-created-1", *sessionSvc.lastUpdate.IncidentID)
	require.Equal(t, TriggerTypeChange, runner.lastReq.GetTrigger())
	require.Equal(t, TriggerTypeChange, runner.lastTriggerType)
	require.Equal(t, "change_router", runner.lastTriggerSource)
}

func TestDispatch_ReturnsIncidentNotFound(t *testing.T) {
	biz := newWithDeps(
		&fakeIncidentStore{err: gorm.ErrRecordNotFound},
		&fakeAIJobRunner{},
		&fakeSessionEnsurer{},
	)

	_, err := biz.Dispatch(context.Background(), &TriggerRequest{
		TriggerType: TriggerTypeManual,
		Source:      "manual_api",
		BusinessKey: "incident-missing",
		IncidentHint: &IncidentHint{
			IncidentID: "incident-missing",
		},
		TimeRange: &TriggerTimeRange{
			Start: time.Now().UTC().Add(-time.Minute),
			End:   time.Now().UTC(),
		},
	})
	require.Error(t, err)
	require.Equal(t, errno.ErrIncidentNotFound, err)
}

func TestDispatch_InvalidRequest(t *testing.T) {
	biz := newWithDeps(&fakeIncidentStore{}, &fakeAIJobRunner{}, &fakeSessionEnsurer{})
	_, err := biz.Dispatch(context.Background(), &TriggerRequest{
		TriggerType: "unknown",
	})
	require.Error(t, err)
	require.Equal(t, errorsx.ErrInvalidArgument, err)
}

func TestDispatchReplay_InvalidWithoutIncidentOrSession(t *testing.T) {
	biz := newWithDeps(&fakeIncidentStore{}, &fakeAIJobRunner{}, &fakeSessionEnsurer{})
	_, err := biz.Dispatch(context.Background(), &TriggerRequest{
		TriggerType: TriggerTypeReplay,
		Source:      "replay_api",
		TimeRange: &TriggerTimeRange{
			Start: time.Now().UTC().Add(-10 * time.Minute),
			End:   time.Now().UTC(),
		},
		RunRequest: &v1.RunAIJobRequest{
			TimeRangeStart: timestamppb.Now(),
			TimeRangeEnd:   timestamppb.Now(),
		},
	})
	require.Error(t, err)
	require.Equal(t, errorsx.ErrInvalidArgument, err)
}

func TestDispatchCron_InvalidWithoutBusinessKeySessionOrIncident(t *testing.T) {
	biz := newWithDeps(&fakeIncidentStore{}, &fakeAIJobRunner{}, &fakeSessionEnsurer{})
	_, err := biz.Dispatch(context.Background(), &TriggerRequest{
		TriggerType: TriggerTypeCron,
		Source:      "cron_router",
		TimeRange: &TriggerTimeRange{
			Start: time.Now().UTC().Add(-10 * time.Minute),
			End:   time.Now().UTC(),
		},
	})
	require.Error(t, err)
	require.Equal(t, errorsx.ErrInvalidArgument, err)
}

type fakeIncidentStore struct {
	incident        *model.IncidentM
	err             error
	createCalls     int
	createdIncident *model.IncidentM
	createErr       error
}

func (f *fakeIncidentStore) Create(_ context.Context, obj *model.IncidentM) error {
	f.createCalls++
	if f.createErr != nil {
		return f.createErr
	}
	if obj != nil {
		if strings.TrimSpace(obj.IncidentID) == "" {
			obj.IncidentID = "incident-created-1"
		}
		f.createdIncident = obj
		f.incident = obj
	}
	return nil
}

func (f *fakeIncidentStore) Get(_ context.Context, _ *where.Options) (*model.IncidentM, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.incident == nil {
		return nil, gorm.ErrRecordNotFound
	}
	return f.incident, nil
}

type fakeAIJobRunner struct {
	calls   int
	lastReq *v1.RunAIJobRequest
	resp    *v1.RunAIJobResponse
	err     error

	lastTriggerType   string
	lastTriggerSource string
	lastInitiator     string
}

func (f *fakeAIJobRunner) Run(ctx context.Context, rq *v1.RunAIJobRequest) (*v1.RunAIJobResponse, error) {
	f.calls++
	f.lastReq = rq
	f.lastTriggerType = contextx.TriggerType(ctx)
	f.lastTriggerSource = contextx.TriggerSource(ctx)
	f.lastInitiator = contextx.TriggerInitiator(ctx)
	if f.err != nil {
		return nil, f.err
	}
	if f.resp == nil {
		f.resp = &v1.RunAIJobResponse{JobID: "ai-job-fake"}
	}
	return f.resp, nil
}

type fakeSessionEnsurer struct {
	calls      int
	lastReq    *sessionbiz.EnsureIncidentSessionRequest
	resp       *sessionbiz.ResolveOrCreateResponse
	err        error
	getCalls   int
	lastGet    *sessionbiz.GetSessionContextRequest
	getResp    *sessionbiz.GetSessionContextResponse
	getErr     error
	resolveErr error

	resolveCalls int
	lastResolve  *sessionbiz.ResolveOrCreateRequest
	resolveResp  *sessionbiz.ResolveOrCreateResponse

	updateCalls int
	lastUpdate  *sessionbiz.UpdateSessionContextRequest
	updateResp  *sessionbiz.UpdateSessionContextResponse
	updateErr   error
}

func (f *fakeSessionEnsurer) EnsureIncidentSession(
	_ context.Context,
	rq *sessionbiz.EnsureIncidentSessionRequest,
) (*sessionbiz.ResolveOrCreateResponse, error) {
	f.calls++
	f.lastReq = rq
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func (f *fakeSessionEnsurer) Get(
	_ context.Context,
	rq *sessionbiz.GetSessionContextRequest,
) (*sessionbiz.GetSessionContextResponse, error) {
	f.getCalls++
	f.lastGet = rq
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.getResp, nil
}

func (f *fakeSessionEnsurer) ResolveOrCreate(
	_ context.Context,
	rq *sessionbiz.ResolveOrCreateRequest,
) (*sessionbiz.ResolveOrCreateResponse, error) {
	f.resolveCalls++
	f.lastResolve = rq
	if f.resolveErr != nil {
		return nil, f.resolveErr
	}
	return f.resolveResp, nil
}

func (f *fakeSessionEnsurer) Update(
	_ context.Context,
	rq *sessionbiz.UpdateSessionContextRequest,
) (*sessionbiz.UpdateSessionContextResponse, error) {
	f.updateCalls++
	f.lastUpdate = rq
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	return f.updateResp, nil
}
