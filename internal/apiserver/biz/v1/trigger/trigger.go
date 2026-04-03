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
	internalstrategyconfig "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/internal_strategy_config"
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
	TriggerTypeCron     = "cron"
	TriggerTypeChange   = "change"

	defaultPipeline = "basic_rca"
	defaultMessage  = "trigger_routed"
)

var allowedTriggerTypes = map[string]struct{}{
	TriggerTypeAlert:    {},
	TriggerTypeManual:   {},
	TriggerTypeReplay:   {},
	TriggerTypeFollowUp: {},
	TriggerTypeCron:     {},
	TriggerTypeChange:   {},
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
	Create(ctx context.Context, obj *model.IncidentM) error
	Get(ctx context.Context, opts *where.Options) (*model.IncidentM, error)
}

type aiJobRunner interface {
	Run(ctx context.Context, rq *v1.RunAIJobRequest) (*v1.RunAIJobResponse, error)
}

type incidentSessionEnsurer interface {
	ResolveOrCreate(ctx context.Context, rq *sessionbiz.ResolveOrCreateRequest) (*sessionbiz.ResolveOrCreateResponse, error)
	EnsureIncidentSession(ctx context.Context, rq *sessionbiz.EnsureIncidentSessionRequest) (*sessionbiz.ResolveOrCreateResponse, error)
	Get(ctx context.Context, rq *sessionbiz.GetSessionContextRequest) (*sessionbiz.GetSessionContextResponse, error)
	Update(ctx context.Context, rq *sessionbiz.UpdateSessionContextRequest) (*sessionbiz.UpdateSessionContextResponse, error)
}

type triggerBiz struct {
	incidentStore incidentStore
	runAIJobBiz   aiJobRunner
	sessionBiz    incidentSessionEnsurer
	configBiz     triggerConfigResolver
}

type triggerConfigResolver interface {
	ResolveTriggerPipeline(ctx context.Context, triggerType string) (pipelineID string, sessionType string, source string, err error)
	GetPipeline(
		ctx context.Context,
		rq *internalstrategyconfig.GetPipelineConfigRequest,
	) (*internalstrategyconfig.PipelineConfigView, error)
}

var _ TriggerBiz = (*triggerBiz)(nil)

func New(store store.IStore) *triggerBiz {
	return &triggerBiz{
		incidentStore: store.Incident(),
		runAIJobBiz:   aijobbiz.New(store),
		sessionBiz:    sessionbiz.New(store),
		configBiz:     internalstrategyconfig.New(store),
	}
}

func newWithDeps(
	incidentStore incidentStore,
	runAIJobBiz aiJobRunner,
	sessionBiz incidentSessionEnsurer,
	configBiz triggerConfigResolver,
) *triggerBiz {
	return &triggerBiz{
		incidentStore: incidentStore,
		runAIJobBiz:   runAIJobBiz,
		sessionBiz:    sessionBiz,
		configBiz:     configBiz,
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

	runReq, err := b.buildRunRequest(ctx, normalized, incident.IncidentID)
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
	if sessionID == "" && supportsBusinessSession(rq.triggerType) && strings.TrimSpace(rq.businessKey) != "" {
		sessionObj, err := b.resolveBusinessSession(ctx, rq)
		if err != nil {
			return nil, "", err
		}
		if sessionObj != nil {
			sessionID = strings.TrimSpace(sessionObj.SessionID)
			derivedIncidentID := incidentIDFromSession(sessionObj)
			if incidentID == "" {
				incidentID = derivedIncidentID
			} else if derivedIncidentID != "" && derivedIncidentID != incidentID {
				return nil, "", errorsx.ErrInvalidArgument
			}
		}
	}
	if incidentID == "" {
		if !supportsBusinessSession(rq.triggerType) {
			return nil, "", errorsx.ErrInvalidArgument
		}
		created, err := b.createIncidentFromTrigger(ctx, rq)
		if err != nil {
			return nil, "", err
		}
		incidentID = strings.TrimSpace(created.IncidentID)
		if sessionID != "" {
			b.bindSessionIncidentBestEffort(ctx, sessionID, incidentID)
		} else {
			sessionID = b.ensureBusinessSessionBindingBestEffort(ctx, rq, incidentID)
		}
		return created, sessionID, nil
	}

	incident, err := b.getIncident(ctx, incidentID)
	if err != nil {
		return nil, "", err
	}
	if sessionID == "" {
		if supportsBusinessSession(rq.triggerType) {
			sessionID = b.ensureBusinessSessionBindingBestEffort(ctx, rq, incidentID)
		}
		if sessionID == "" {
			sessionID = b.ensureIncidentSessionIDBestEffort(ctx, incident)
		}
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

func supportsBusinessSession(triggerType string) bool {
	switch strings.ToLower(strings.TrimSpace(triggerType)) {
	case TriggerTypeCron, TriggerTypeChange:
		return true
	default:
		return false
	}
}

func sessionTypeByTrigger(triggerType string) string {
	switch strings.ToLower(strings.TrimSpace(triggerType)) {
	case TriggerTypeCron:
		return sessionbiz.SessionTypeService
	case TriggerTypeChange:
		return sessionbiz.SessionTypeChange
	default:
		return ""
	}
}

func (b *triggerBiz) resolveBusinessSession(
	ctx context.Context,
	rq *normalizedTriggerRequest,
) (*model.SessionContextM, error) {
	if b == nil || b.sessionBiz == nil || rq == nil {
		return nil, errorsx.ErrInvalidArgument
	}
	sessionType := sessionTypeByTrigger(rq.triggerType)
	if sessionType == "" {
		return nil, errorsx.ErrInvalidArgument
	}
	businessKey := strings.TrimSpace(rq.businessKey)
	if businessKey == "" {
		return nil, errorsx.ErrInvalidArgument
	}

	title := firstNonEmpty(
		payloadString(rq.payload, "title"),
		payloadString(rq.payload, "service"),
		payloadString(rq.payload, "release_id"),
		payloadString(rq.payload, "releaseID"),
		payloadString(rq.payload, "change_id"),
		payloadString(rq.payload, "changeID"),
		businessKey,
	)
	resp, err := b.sessionBiz.ResolveOrCreate(ctx, &sessionbiz.ResolveOrCreateRequest{
		SessionType: sessionType,
		BusinessKey: businessKey,
		Title:       strPtr(title),
		Status:      strPtr(sessionbiz.SessionStatusActive),
	})
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Session == nil {
		return nil, errno.ErrSessionContextGetFailed
	}
	return resp.Session, nil
}

func (b *triggerBiz) bindSessionIncidentBestEffort(ctx context.Context, sessionID string, incidentID string) {
	if b == nil || b.sessionBiz == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	incidentID = strings.TrimSpace(incidentID)
	if sessionID == "" || incidentID == "" {
		return
	}
	_, err := b.sessionBiz.Update(ctx, &sessionbiz.UpdateSessionContextRequest{
		SessionID:  sessionID,
		IncidentID: strPtr(incidentID),
	})
	if err != nil {
		slog.WarnContext(ctx, "trigger session bind incident skipped",
			"session_id", sessionID,
			"incident_id", incidentID,
			"error", err,
		)
	}
}

func (b *triggerBiz) ensureBusinessSessionBindingBestEffort(
	ctx context.Context,
	rq *normalizedTriggerRequest,
	incidentID string,
) string {
	if b == nil || b.sessionBiz == nil || rq == nil {
		return ""
	}
	if !supportsBusinessSession(rq.triggerType) {
		return ""
	}
	sessionObj, err := b.resolveBusinessSession(ctx, rq)
	if err != nil || sessionObj == nil {
		if err != nil {
			slog.WarnContext(ctx, "trigger business session ensure skipped",
				"trigger_type", rq.triggerType,
				"business_key", rq.businessKey,
				"incident_id", strings.TrimSpace(incidentID),
				"error", err,
			)
		}
		return ""
	}
	sessionID := strings.TrimSpace(sessionObj.SessionID)
	if sessionID == "" {
		return ""
	}
	if trimOptional(sessionObj.IncidentID) == "" && strings.TrimSpace(incidentID) != "" {
		b.bindSessionIncidentBestEffort(ctx, sessionID, incidentID)
	}
	return sessionID
}

func (b *triggerBiz) createIncidentFromTrigger(
	ctx context.Context,
	rq *normalizedTriggerRequest,
) (*model.IncidentM, error) {
	if b == nil || b.incidentStore == nil || rq == nil {
		return nil, errorsx.ErrInvalidArgument
	}

	service := firstNonEmpty(
		payloadString(rq.payload, "service"),
		payloadString(rq.payload, "app"),
		serviceFromBusinessKey(rq.businessKey),
		"unknown-service",
	)
	namespace := firstNonEmpty(payloadString(rq.payload, "namespace"), "default")
	environment := firstNonEmpty(payloadString(rq.payload, "environment"), payloadString(rq.payload, "env"), "prod")
	tenant := firstNonEmpty(payloadString(rq.payload, "tenant"), payloadString(rq.payload, "tenant_id"), "default")
	cluster := firstNonEmpty(payloadString(rq.payload, "cluster"), "default")
	workload := firstNonEmpty(
		payloadString(rq.payload, "workload"),
		payloadString(rq.payload, "workload_name"),
		service,
	)
	source := firstNonEmpty(strings.TrimSpace(rq.source), strings.TrimSpace(rq.triggerType))
	severity := firstNonEmpty(payloadString(rq.payload, "severity"), "P2")

	obj := &model.IncidentM{
		TenantID:     tenant,
		Cluster:      cluster,
		Namespace:    namespace,
		WorkloadKind: "Deployment",
		WorkloadName: workload,
		Service:      service,
		Environment:  environment,
		Source:       source,
		Severity:     severity,
		Status:       "open",
		RCAStatus:    "pending",
		ActionStatus: "none",
		CreatedBy:    strPtr(rq.initiator),
	}
	if rq.timeRange != nil {
		if !rq.timeRange.Start.IsZero() {
			start := rq.timeRange.Start.UTC()
			obj.StartAt = &start
		}
	}
	switch rq.triggerType {
	case TriggerTypeChange:
		changeID := firstNonEmpty(
			payloadString(rq.payload, "change_id"),
			payloadString(rq.payload, "changeID"),
			payloadString(rq.payload, "release_id"),
			payloadString(rq.payload, "releaseID"),
			payloadString(rq.payload, "deploy_id"),
			payloadString(rq.payload, "deployID"),
		)
		obj.ChangeID = strPtr(changeID)
		version := firstNonEmpty(
			payloadString(rq.payload, "release_id"),
			payloadString(rq.payload, "releaseID"),
			payloadString(rq.payload, "version"),
		)
		obj.Version = strPtr(version)
	}

	if err := b.incidentStore.Create(ctx, obj); err != nil {
		return nil, errno.ErrIncidentCreateFailed
	}
	return obj, nil
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
	payload     map[string]any
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
	if businessKey == "" {
		businessKey = deriveBusinessKeyFromPayload(triggerType, rq.Payload)
	}
	incidentID := resolveIncidentID(rq, triggerType, businessKey)
	sessionID := resolveSessionID(rq)
	if !hasValidAnchor(triggerType, incidentID, sessionID, businessKey) {
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
		payload:     clonePayloadMap(rq.Payload),
	}, nil
}

func hasValidAnchor(triggerType string, incidentID string, sessionID string, businessKey string) bool {
	incidentID = strings.TrimSpace(incidentID)
	sessionID = strings.TrimSpace(sessionID)
	businessKey = strings.TrimSpace(businessKey)
	switch triggerType {
	case TriggerTypeReplay, TriggerTypeFollowUp:
		return incidentID != "" || sessionID != ""
	case TriggerTypeCron, TriggerTypeChange:
		return incidentID != "" || sessionID != "" || businessKey != ""
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
	if triggerType == TriggerTypeReplay || triggerType == TriggerTypeFollowUp || triggerType == TriggerTypeCron || triggerType == TriggerTypeChange {
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

func deriveBusinessKeyFromPayload(triggerType string, payload map[string]any) string {
	switch strings.ToLower(strings.TrimSpace(triggerType)) {
	case TriggerTypeCron:
		return buildCronBusinessKey(payload)
	case TriggerTypeChange:
		return buildChangeBusinessKey(payload)
	default:
		return ""
	}
}

func buildCronBusinessKey(payload map[string]any) string {
	service := payloadString(payload, "service")
	namespace := payloadString(payload, "namespace")
	environment := firstNonEmpty(payloadString(payload, "environment"), payloadString(payload, "env"))
	tenant := firstNonEmpty(payloadString(payload, "tenant"), payloadString(payload, "tenant_id"))

	if service != "" {
		parts := []string{"service:" + service}
		if environment != "" {
			parts = append(parts, "env:"+environment)
		}
		if namespace != "" {
			parts = append(parts, "ns:"+namespace)
		}
		if tenant != "" {
			parts = append(parts, "tenant:"+tenant)
		}
		return strings.Join(parts, ":")
	}
	if namespace != "" {
		parts := []string{"namespace:" + namespace}
		if environment != "" {
			parts = append(parts, "env:"+environment)
		}
		if tenant != "" {
			parts = append(parts, "tenant:"+tenant)
		}
		return strings.Join(parts, ":")
	}
	if tenant != "" {
		return "tenant:" + tenant
	}
	return ""
}

func buildChangeBusinessKey(payload map[string]any) string {
	if changeID := firstNonEmpty(payloadString(payload, "change_id"), payloadString(payload, "changeID")); changeID != "" {
		return "change:" + changeID
	}
	releaseID := firstNonEmpty(payloadString(payload, "release_id"), payloadString(payload, "releaseID"))
	deployID := firstNonEmpty(payloadString(payload, "deploy_id"), payloadString(payload, "deployID"))
	service := payloadString(payload, "service")
	environment := firstNonEmpty(payloadString(payload, "environment"), payloadString(payload, "env"))

	if releaseID != "" && service != "" {
		parts := []string{"deploy", service, releaseID}
		if environment != "" {
			parts = append(parts, environment)
		}
		return strings.Join(parts, ":")
	}
	if releaseID != "" {
		return "release:" + releaseID
	}
	if deployID != "" {
		return "deploy:" + deployID
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

func (b *triggerBiz) buildRunRequest(
	ctx context.Context,
	r *normalizedTriggerRequest,
	incidentID string,
) (*v1.RunAIJobRequest, error) {
	if r == nil {
		return nil, errorsx.ErrInvalidArgument
	}
	runReq := cloneRunRequest(r.runRequest)
	if runReq == nil {
		runReq = &v1.RunAIJobRequest{}
	}
	runReq.IncidentID = incidentID

	if pipeline := trimOptional(r.pipeline); pipeline != "" {
		runReq.Pipeline = strPtr(pipeline)
	}
	if trimOptional(runReq.Pipeline) == "" && b != nil && b.configBiz != nil {
		resolvedPipeline, _, _, err := b.configBiz.ResolveTriggerPipeline(ctx, r.triggerType)
		if err == nil && strings.TrimSpace(resolvedPipeline) != "" {
			runReq.Pipeline = strPtr(strings.TrimSpace(resolvedPipeline))
		}
	}
	if trimOptional(runReq.Pipeline) == "" && b != nil && b.configBiz != nil {
		pipelineCfg, err := b.configBiz.GetPipeline(ctx, &internalstrategyconfig.GetPipelineConfigRequest{
			AlertSource: r.triggerType,
			Service:     payloadString(r.payload, "service"),
			Namespace:   payloadString(r.payload, "namespace"),
		})
		if err == nil && pipelineCfg != nil && strings.TrimSpace(pipelineCfg.PipelineID) != "" {
			runReq.Pipeline = strPtr(strings.TrimSpace(pipelineCfg.PipelineID))
		}
	}
	if trimOptional(runReq.Pipeline) == "" {
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
	case TriggerTypeCron:
		return TriggerTypeCron
	case TriggerTypeChange:
		return TriggerTypeChange
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

func payloadString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	raw, ok := payload[key]
	if !ok {
		return ""
	}
	return strings.TrimSpace(anyToString(raw))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func clonePayloadMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		cleanKey := strings.TrimSpace(key)
		if cleanKey == "" {
			continue
		}
		out[cleanKey] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func serviceFromBusinessKey(businessKey string) string {
	businessKey = strings.TrimSpace(businessKey)
	if businessKey == "" {
		return ""
	}
	if strings.HasPrefix(businessKey, "service:") {
		trimmed := strings.TrimPrefix(businessKey, "service:")
		parts := strings.Split(trimmed, ":")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	return ""
}
