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
	"github.com/aiopsre/rca-api/internal/pkg/contextx"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

const (
	TriggerTypeAlert    = "alert"
	TriggerTypeManual   = "manual"
	TriggerTypeReplay   = "replay"
	TriggerTypeFollowUp = "follow_up"

	defaultPipeline = "basic_rca"
	defaultMessage  = "trigger_routed"
)

var allowedTriggerTypes = map[string]struct{}{
	TriggerTypeAlert:    {},
	TriggerTypeManual:   {},
	TriggerTypeReplay:   {},
	TriggerTypeFollowUp: {},
}

type IncidentHint struct {
	IncidentID string
}

type SessionHint struct {
	SessionID string
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
	SessionHint  *SessionHint
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
	Get(ctx context.Context, rq *sessionbiz.GetSessionContextRequest) (*sessionbiz.GetSessionContextResponse, error)
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
	incident, resolvedSessionID, err := b.resolveExecutionContext(ctx, normalized)
	if err != nil {
		return nil, err
	}

	runReq, err := normalized.toRunAIJobRequest(incident.IncidentID)
	if err != nil {
		return nil, err
	}
	runCtx := attachTriggerContext(ctx, normalized)
	runResp, err := b.runAIJobBiz.Run(runCtx, runReq)
	if err != nil {
		return nil, err
	}

	result := &TriggerResult{
		SessionID:  resolvedSessionID,
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

func (b *triggerBiz) resolveExecutionContext(
	ctx context.Context,
	rq *normalizedTriggerRequest,
) (*model.IncidentM, string, error) {
	if rq == nil {
		return nil, "", errorsx.ErrInvalidArgument
	}

	incidentID := strings.TrimSpace(rq.incidentID)
	sessionID := strings.TrimSpace(rq.sessionID)

	if sessionID != "" {
		sessionObj, err := b.getSession(ctx, sessionID)
		if err != nil {
			return nil, "", err
		}
		sessionID = strings.TrimSpace(sessionObj.SessionID)
		derivedIncidentID := incidentIDFromSession(sessionObj)
		if incidentID == "" {
			incidentID = derivedIncidentID
		} else if derivedIncidentID != "" && derivedIncidentID != incidentID {
			return nil, "", errorsx.ErrInvalidArgument
		}
	}
	if incidentID == "" {
		return nil, "", errorsx.ErrInvalidArgument
	}

	incident, err := b.getIncident(ctx, incidentID)
	if err != nil {
		return nil, "", err
	}
	if sessionID == "" {
		sessionID = b.ensureIncidentSessionIDBestEffort(ctx, incident)
	}
	return incident, sessionID, nil
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

func (b *triggerBiz) getSession(ctx context.Context, sessionID string) (*model.SessionContextM, error) {
	if b == nil || b.sessionBiz == nil {
		return nil, errorsx.ErrInvalidArgument
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errorsx.ErrInvalidArgument
	}
	resp, err := b.sessionBiz.Get(ctx, &sessionbiz.GetSessionContextRequest{
		SessionID: strPtr(sessionID),
	})
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Session == nil {
		return nil, errno.ErrSessionContextNotFound
	}
	return resp.Session, nil
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
	initiator   string
	incidentID  string
	sessionID   string
	runRequest  *v1.RunAIJobRequest
	timeRange   *TriggerTimeRange
	pipeline    *string
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
	incidentID := resolveIncidentID(rq, triggerType, businessKey)
	sessionID := resolveSessionID(rq)
	if !hasValidAnchor(triggerType, incidentID, sessionID) {
		return nil, errorsx.ErrInvalidArgument
	}
	if incidentID == "" {
		if triggerType == TriggerTypeAlert || triggerType == TriggerTypeManual {
			return nil, errorsx.ErrInvalidArgument
		}
	}
	initiator := resolveInitiator(rq)

	return &normalizedTriggerRequest{
		triggerType: triggerType,
		source:      source,
		businessKey: businessKey,
		initiator:   initiator,
		incidentID:  incidentID,
		sessionID:   sessionID,
		runRequest:  cloneRunRequest(rq.RunRequest),
		timeRange:   rq.TimeRange,
		pipeline:    rq.DesiredPipeline,
	}, nil
}

func hasValidAnchor(triggerType string, incidentID string, sessionID string) bool {
	incidentID = strings.TrimSpace(incidentID)
	sessionID = strings.TrimSpace(sessionID)
	switch triggerType {
	case TriggerTypeReplay, TriggerTypeFollowUp:
		return incidentID != "" || sessionID != ""
	default:
		return incidentID != ""
	}
}

func resolveIncidentID(rq *TriggerRequest, triggerType string, businessKey string) string {
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
	if triggerType == TriggerTypeReplay || triggerType == TriggerTypeFollowUp {
		return ""
	}
	// Transitional fallback: allow business_key to carry incident id when explicit hint is absent.
	return strings.TrimSpace(businessKey)
}

func resolveSessionID(rq *TriggerRequest) string {
	if rq == nil {
		return ""
	}
	if rq.SessionHint != nil {
		if value := strings.TrimSpace(rq.SessionHint.SessionID); value != "" {
			return value
		}
	}
	if rq.Payload != nil {
		if raw, ok := rq.Payload["session_id"]; ok {
			if value := strings.TrimSpace(anyToString(raw)); value != "" {
				return value
			}
		}
		if raw, ok := rq.Payload["sessionID"]; ok {
			if value := strings.TrimSpace(anyToString(raw)); value != "" {
				return value
			}
		}
	}
	return ""
}

func buildRunRequest(r *normalizedTriggerRequest, incidentID string) (*v1.RunAIJobRequest, error) {
	runReq := cloneRunRequest(r.runRequest)
	if runReq == nil {
		runReq = &v1.RunAIJobRequest{}
	}
	runReq.IncidentID = incidentID

	if pipeline := trimOptional(r.pipeline); pipeline != "" {
		runReq.Pipeline = strPtr(pipeline)
	} else if trimOptional(runReq.Pipeline) == "" {
		runReq.Pipeline = strPtr(defaultPipeline)
	}

	if trigger := trimOptional(runReq.Trigger); trigger == "" {
		runReq.Trigger = strPtr(defaultTriggerByType(r.triggerType))
	}

	applyTimeRange(runReq, r.timeRange)
	start := runReq.GetTimeRangeStart().AsTime().UTC()
	end := runReq.GetTimeRangeEnd().AsTime().UTC()
	if start.IsZero() || end.IsZero() || start.After(end) {
		return nil, errorsx.ErrInvalidArgument
	}

	return runReq, nil
}

func resolveInitiator(rq *TriggerRequest) string {
	if rq == nil {
		return ""
	}
	if value := trimOptional(rq.Initiator); value != "" {
		return value
	}
	if rq.RunRequest != nil {
		return trimOptional(rq.RunRequest.CreatedBy)
	}
	return ""
}

func attachTriggerContext(ctx context.Context, rq *normalizedTriggerRequest) context.Context {
	if rq == nil {
		return ctx
	}
	ctx = contextx.WithTriggerType(ctx, rq.triggerType)
	ctx = contextx.WithTriggerSource(ctx, rq.source)
	if value := strings.TrimSpace(rq.initiator); value != "" {
		ctx = contextx.WithTriggerInitiator(ctx, value)
	}
	return ctx
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
	case TriggerTypeReplay:
		return TriggerTypeReplay
	case TriggerTypeFollowUp:
		return TriggerTypeFollowUp
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

func (r *normalizedTriggerRequest) toRunAIJobRequest(incidentID string) (*v1.RunAIJobRequest, error) {
	if r == nil {
		return nil, errorsx.ErrInvalidArgument
	}
	return buildRunRequest(r, incidentID)
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

func incidentIDFromSession(sessionObj *model.SessionContextM) string {
	if sessionObj == nil {
		return ""
	}
	if value := trimOptional(sessionObj.IncidentID); value != "" {
		return value
	}
	if strings.TrimSpace(sessionObj.SessionType) == sessionbiz.SessionTypeIncident {
		return strings.TrimSpace(sessionObj.BusinessKey)
	}
	return ""
}

func anyToString(value any) string {
	switch in := value.(type) {
	case string:
		return in
	default:
		return ""
	}
}
