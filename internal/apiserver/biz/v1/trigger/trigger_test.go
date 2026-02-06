package trigger

import (
	"context"
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

type fakeIncidentStore struct {
	incident *model.IncidentM
	err      error
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
	calls   int
	lastReq *sessionbiz.EnsureIncidentSessionRequest
	resp    *sessionbiz.ResolveOrCreateResponse
	err     error
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
