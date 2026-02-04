package trigger

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/onexstack/onexstack/pkg/errorsx"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	aijobbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/ai_job"
	sessionbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/session"
	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

const (
	TriggerTypeAlert  = "alert"
	TriggerTypeManual = "manual"

	defaultPipeline = "basic_rca"
	defaultMessage  = "trigger_routed"
)

var allowedTriggerTypes = map[string]struct{}{
	TriggerTypeAlert:  {},
	TriggerTypeManual: {},
}

type IncidentHint struct {
	IncidentID string
}

type TriggerTimeRange struct {
	Start time.Time
	End   time.Time
}

// TriggerRequest is the unified internal trigger model for entrypoint orchestration.
// alert/manual currently share this model and dispatch through the same path.
type TriggerRequest struct {
	TriggerType string
	Source      string
	BusinessKey string

	IncidentHint *IncidentHint
	Payload      map[string]any
	Initiator    *string

	DesiredPipeline *string
	TimeRange       *TriggerTimeRange

	// RunRequest keeps compatibility with existing paths that already build
	// canonical RunAIJobRequest payloads (e.g. run plan conversion).
	RunRequest *v1.RunAIJobRequest
}

type TriggerResult struct {
	SessionID  string
	IncidentID string
	JobID      string
	Pipeline   string
	Created    bool
	Message    string
}

// TriggerBiz orchestrates trigger normalization/session resolve/AIJob enqueue.
type TriggerBiz interface {
	Dispatch(ctx context.Context, rq *TriggerRequest) (*TriggerResult, error)

	TriggerExpansion
}

//nolint:modernize // Keep explicit empty interface as placeholder expansion point.
type TriggerExpansion interface{}

type incidentStore interface {
	Get(ctx context.Context, opts *where.Options) (*model.IncidentM, error)
}

type aiJobRunner interface {
	Run(ctx context.Context, rq *v1.RunAIJobRequest) (*v1.RunAIJobResponse, error)
}

type incidentSessionEnsurer interface {
	EnsureIncidentSession(ctx context.Context, rq *sessionbiz.EnsureIncidentSessionRequest) (*sessionbiz.ResolveOrCreateResponse, error)
}

type triggerBiz struct {
	incidentStore incidentStore
	runAIJobBiz   aiJobRunner
	sessionBiz    incidentSessionEnsurer
}

var _ TriggerBiz = (*triggerBiz)(nil)

func New(store store.IStore) *triggerBiz {
	return &triggerBiz{
		incidentStore: store.Incident(),
		runAIJobBiz:   aijobbiz.New(store),
		sessionBiz:    sessionbiz.New(store),
	}
}

func newWithDeps(incidentStore incidentStore, runAIJobBiz aiJobRunner, sessionBiz incidentSessionEnsurer) *triggerBiz {
	return &triggerBiz{
		incidentStore: incidentStore,
		runAIJobBiz:   runAIJobBiz,
		sessionBiz:    sessionBiz,
	}
}

func (b *triggerBiz) Dispatch(ctx context.Context, rq *TriggerRequest) (*TriggerResult, error) {
	normalized, err := normalizeTriggerRequest(rq)
	if err != nil {
		return nil, err
	}
	incident, err := b.getIncident(ctx, normalized.incidentID)
	if err != nil {
		return nil, err
	}

	sessionID := b.ensureIncidentSessionIDBestEffort(ctx, incident)
	runReq, err := normalized.toRunAIJobRequest()
	if err != nil {
		return nil, err
	}

	runResp, err := b.runAIJobBiz.Run(ctx, runReq)
	if err != nil {
		return nil, err
	}

	result := &TriggerResult{
		SessionID:  sessionID,
		IncidentID: incident.IncidentID,
		JobID:      strings.TrimSpace(runResp.GetJobID()),
		Pipeline:   strings.TrimSpace(runReq.GetPipeline()),
		Created:    strings.TrimSpace(runResp.GetJobID()) != "",
		Message:    defaultMessage,
	}

	slog.InfoContext(ctx, "trigger dispatched",
		"trigger_type", normalized.triggerType,
		"source", normalized.source,
		"business_key", normalized.businessKey,
		"incident_id", result.IncidentID,
		"session_id", result.SessionID,
		"job_id", result.JobID,
		"pipeline", result.Pipeline,
	)

	return result, nil
}

func (b *triggerBiz) getIncident(ctx context.Context, incidentID string) (*model.IncidentM, error) {
	if b == nil || b.incidentStore == nil {
		return nil, errorsx.ErrInvalidArgument
	}
	incident, err := b.incidentStore.Get(ctx, where.T(ctx).F("incident_id", incidentID))
	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrIncidentNotFound
		}
		return nil, errno.ErrIncidentGetFailed
	}
	return incident, nil
}

func (b *triggerBiz) ensureIncidentSessionIDBestEffort(ctx context.Context, incident *model.IncidentM) string {
	if b == nil || b.sessionBiz == nil || incident == nil {
		return ""
	}
	incidentID := strings.TrimSpace(incident.IncidentID)
	if incidentID == "" {
		return ""
	}

	var title *string
	if value := strings.TrimSpace(incident.Service); value != "" {
		title = strPtr(value)
	} else if value := strings.TrimSpace(incident.WorkloadName); value != "" {
		title = strPtr(value)
	}

	resp, err := b.sessionBiz.EnsureIncidentSession(ctx, &sessionbiz.EnsureIncidentSessionRequest{
		IncidentID: incidentID,
		Title:      title,
	})
	if err != nil {
		slog.WarnContext(ctx, "trigger session ensure skipped",
			"incident_id", incidentID,
			"error", err,
		)
		return ""
	}
	if resp == nil || resp.Session == nil {
		return ""
	}
	return strings.TrimSpace(resp.Session.SessionID)
}

type normalizedTriggerRequest struct {
	triggerType string
	source      string
	businessKey string
	incidentID  string
	runRequest  *v1.RunAIJobRequest
}

func normalizeTriggerRequest(rq *TriggerRequest) (*normalizedTriggerRequest, error) {
	if rq == nil {
		return nil, errorsx.ErrInvalidArgument
	}
	triggerType := strings.ToLower(strings.TrimSpace(rq.TriggerType))
	if _, ok := allowedTriggerTypes[triggerType]; !ok {
		return nil, errorsx.ErrInvalidArgument
	}

	source := strings.TrimSpace(rq.Source)
	if source == "" {
		source = triggerType
	}
	businessKey := strings.TrimSpace(rq.BusinessKey)
	incidentID := resolveIncidentID(rq, businessKey)
	if incidentID == "" {
		return nil, errorsx.ErrInvalidArgument
	}

	runReq, err := buildRunRequest(rq, triggerType, incidentID)
	if err != nil {
		return nil, err
	}

	return &normalizedTriggerRequest{
		triggerType: triggerType,
		source:      source,
		businessKey: businessKey,
		incidentID:  incidentID,
		runRequest:  runReq,
	}, nil
}

func resolveIncidentID(rq *TriggerRequest, businessKey string) string {
	if rq == nil {
		return ""
	}
	if rq.IncidentHint != nil {
		if value := strings.TrimSpace(rq.IncidentHint.IncidentID); value != "" {
			return value
		}
	}
	if rq.RunRequest != nil {
		if value := strings.TrimSpace(rq.RunRequest.GetIncidentID()); value != "" {
			return value
		}
	}
	// Transitional fallback: allow business_key to carry incident id when explicit hint is absent.
	return strings.TrimSpace(businessKey)
}

func buildRunRequest(rq *TriggerRequest, triggerType string, incidentID string) (*v1.RunAIJobRequest, error) {
	runReq := cloneRunRequest(rq.RunRequest)
	if runReq == nil {
		runReq = &v1.RunAIJobRequest{}
	}
	runReq.IncidentID = incidentID

	if pipeline := trimOptional(rq.DesiredPipeline); pipeline != "" {
		runReq.Pipeline = strPtr(pipeline)
	} else if trimOptional(runReq.Pipeline) == "" {
		runReq.Pipeline = strPtr(defaultPipeline)
	}

	if trigger := trimOptional(runReq.Trigger); trigger == "" {
		runReq.Trigger = strPtr(defaultTriggerByType(triggerType))
	}

	applyTimeRange(runReq, rq.TimeRange)
	start := runReq.GetTimeRangeStart().AsTime().UTC()
	end := runReq.GetTimeRangeEnd().AsTime().UTC()
	if start.IsZero() || end.IsZero() || start.After(end) {
		return nil, errorsx.ErrInvalidArgument
	}

	return runReq, nil
}

func applyTimeRange(runReq *v1.RunAIJobRequest, tr *TriggerTimeRange) {
	if runReq == nil || tr == nil {
		return
	}
	start := tr.Start.UTC()
	end := tr.End.UTC()
	runReq.TimeRangeStart = timestamppb.New(start)
	runReq.TimeRangeEnd = timestamppb.New(end)
}

func defaultTriggerByType(triggerType string) string {
	switch strings.ToLower(strings.TrimSpace(triggerType)) {
	case TriggerTypeAlert:
		return "on_ingest"
	default:
		return "manual"
	}
}

func cloneRunRequest(in *v1.RunAIJobRequest) *v1.RunAIJobRequest {
	if in == nil {
		return nil
	}
	cloned := *in
	if in.IdempotencyKey != nil {
		cloned.IdempotencyKey = strPtr(strings.TrimSpace(in.GetIdempotencyKey()))
	}
	if in.Pipeline != nil {
		cloned.Pipeline = strPtr(strings.TrimSpace(in.GetPipeline()))
	}
	if in.Trigger != nil {
		cloned.Trigger = strPtr(strings.TrimSpace(in.GetTrigger()))
	}
	if in.InputHintsJSON != nil {
		cloned.InputHintsJSON = strPtr(strings.TrimSpace(in.GetInputHintsJSON()))
	}
	if in.CreatedBy != nil {
		cloned.CreatedBy = strPtr(strings.TrimSpace(in.GetCreatedBy()))
	}
	return &cloned
}

func (r *normalizedTriggerRequest) toRunAIJobRequest() (*v1.RunAIJobRequest, error) {
	if r == nil || r.runRequest == nil {
		return nil, errorsx.ErrInvalidArgument
	}
	return r.runRequest, nil
}

func trimOptional(ptr *string) string {
	if ptr == nil {
		return ""
	}
	return strings.TrimSpace(*ptr)
}

func strPtr(v string) *string {
	value := strings.TrimSpace(v)
	if value == "" {
		return nil
	}
	return &value
}
