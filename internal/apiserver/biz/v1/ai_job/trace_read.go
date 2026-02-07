package ai_job

import (
	"context"
	"strings"

	"github.com/onexstack/onexstack/pkg/errorsx"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

const (
	defaultTraceReadLimit = int64(20)
	maxTraceReadLimit     = int64(100)
)

type GetTraceReadModelRequest struct {
	JobID string
}

type GetTraceReadModelResponse struct {
	JobID         string                  `json:"job_id"`
	RunTrace      *RunTraceReadModel      `json:"run_trace,omitempty"`
	DecisionTrace *DecisionTraceReadModel `json:"decision_trace,omitempty"`
}

type ListTraceReadModelsRequest struct {
	IncidentID *string
	SessionID  *string
	Offset     int64
	Limit      int64
}

type ListTraceReadModelsResponse struct {
	TotalCount int64               `json:"total_count"`
	Summaries  []*TraceReadSummary `json:"summaries"`
}

type CompareTraceReadModelsRequest struct {
	LeftJobID  string
	RightJobID string
}

type CompareTraceReadModelsResponse struct {
	Left              *TraceCompareSide `json:"left,omitempty"`
	Right             *TraceCompareSide `json:"right,omitempty"`
	SameSession       bool              `json:"same_session"`
	SameIncident      bool              `json:"same_incident"`
	ChangedRootCause  bool              `json:"changed_root_cause"`
	ChangedConfidence bool              `json:"changed_confidence"`
}

type TraceCompareSide struct {
	JobID            string   `json:"job_id"`
	SessionID        string   `json:"session_id,omitempty"`
	IncidentID       string   `json:"incident_id,omitempty"`
	TriggerType      string   `json:"trigger_type,omitempty"`
	Pipeline         string   `json:"pipeline,omitempty"`
	RootCauseType    string   `json:"root_cause_type,omitempty"`
	RootCauseSummary string   `json:"root_cause_summary,omitempty"`
	Confidence       float64  `json:"confidence"`
	EvidenceRefs     []string `json:"evidence_refs"`
	VerificationRefs []string `json:"verification_refs"`
	MissingFacts     []string `json:"missing_facts"`
	Conflicts        []string `json:"conflicts"`
}

type RunTraceReadModel struct {
	SchemaVersion     string  `json:"schema_version,omitempty"`
	RunID             string  `json:"run_id"`
	JobID             string  `json:"job_id"`
	SessionID         string  `json:"session_id,omitempty"`
	IncidentID        string  `json:"incident_id"`
	TriggerType       string  `json:"trigger_type,omitempty"`
	TriggerSource     string  `json:"trigger_source,omitempty"`
	Initiator         string  `json:"initiator,omitempty"`
	Pipeline          string  `json:"pipeline"`
	WorkerID          string  `json:"worker_id,omitempty"`
	WorkerVersion     string  `json:"worker_version,omitempty"`
	Status            string  `json:"status"`
	StartedAt         *string `json:"started_at,omitempty"`
	FinishedAt        *string `json:"finished_at,omitempty"`
	ToolCallCount     int64   `json:"tool_call_count"`
	EvidenceCount     int64   `json:"evidence_count"`
	VerificationCount int64   `json:"verification_count"`
	ErrorSummary      string  `json:"error_summary,omitempty"`
	UpdatedAt         string  `json:"updated_at,omitempty"`
}

type DecisionTraceReadModel struct {
	SchemaVersion       string   `json:"schema_version,omitempty"`
	Status              string   `json:"status"`
	RootCauseType       string   `json:"root_cause_type,omitempty"`
	RootCauseSummary    string   `json:"root_cause_summary,omitempty"`
	Confidence          float64  `json:"confidence"`
	EvidenceRefs        []string `json:"evidence_refs"`
	MissingFacts        []string `json:"missing_facts"`
	Conflicts           []string `json:"conflicts"`
	HumanReviewRequired bool     `json:"human_review_required"`
	VerificationRefs    []string `json:"verification_refs"`
	ErrorSummary        string   `json:"error_summary,omitempty"`
	UpdatedAt           string   `json:"updated_at,omitempty"`
}

type TraceReadSummary struct {
	JobID               string   `json:"job_id"`
	SessionID           string   `json:"session_id,omitempty"`
	IncidentID          string   `json:"incident_id"`
	TriggerType         string   `json:"trigger_type,omitempty"`
	TriggerSource       string   `json:"trigger_source,omitempty"`
	Initiator           string   `json:"initiator,omitempty"`
	Pipeline            string   `json:"pipeline,omitempty"`
	Status              string   `json:"status,omitempty"`
	StartedAt           *string  `json:"started_at,omitempty"`
	FinishedAt          *string  `json:"finished_at,omitempty"`
	ToolCallCount       int64    `json:"tool_call_count"`
	EvidenceCount       int64    `json:"evidence_count"`
	VerificationCount   int64    `json:"verification_count"`
	RootCauseType       string   `json:"root_cause_type,omitempty"`
	RootCauseSummary    string   `json:"root_cause_summary,omitempty"`
	Confidence          float64  `json:"confidence"`
	HumanReviewRequired bool     `json:"human_review_required"`
	VerificationRefs    []string `json:"verification_refs"`
	RunUpdatedAt        string   `json:"run_updated_at,omitempty"`
	DecisionUpdatedAt   string   `json:"decision_updated_at,omitempty"`
}

func (b *aiJobBiz) GetTraceReadModel(
	ctx context.Context,
	rq *GetTraceReadModelRequest,
) (*GetTraceReadModelResponse, error) {
	if rq == nil {
		return nil, errorsx.ErrInvalidArgument
	}
	jobID := strings.TrimSpace(rq.JobID)
	if jobID == "" {
		return nil, errorsx.ErrInvalidArgument
	}
	job, err := b.store.AIJob().Get(ctx, where.T(ctx).F("job_id", jobID))
	if err != nil {
		return nil, toAIJobGetError(err)
	}

	runTrace := runTracePayloadToReadModel(parseRunTraceJSON(job.RunTraceJSON))
	if runTrace == nil {
		runTrace = fallbackRunTraceReadModel(job)
	}
	decisionTrace := decisionTracePayloadToReadModel(parseDecisionTraceJSON(job.DecisionTraceJSON))
	return &GetTraceReadModelResponse{
		JobID:         jobID,
		RunTrace:      runTrace,
		DecisionTrace: decisionTrace,
	}, nil
}

func (b *aiJobBiz) ListTraceReadModels(
	ctx context.Context,
	rq *ListTraceReadModelsRequest,
) (*ListTraceReadModelsResponse, error) {
	if rq == nil {
		return nil, errorsx.ErrInvalidArgument
	}
	incidentID := trimOptional(rq.IncidentID)
	sessionID := trimOptional(rq.SessionID)
	if incidentID == "" && sessionID == "" {
		return nil, errorsx.ErrInvalidArgument
	}
	offset := rq.Offset
	if offset < 0 {
		return nil, errorsx.ErrInvalidArgument
	}
	limit := rq.Limit
	if limit <= 0 {
		limit = defaultTraceReadLimit
	}
	if limit > maxTraceReadLimit {
		return nil, errorsx.ErrInvalidArgument
	}

	whr := where.T(ctx).O(int(offset)).L(int(limit))
	if incidentID != "" {
		whr = whr.F("incident_id", incidentID)
	}
	if sessionID != "" {
		whr = whr.F("session_id", sessionID)
	}
	total, list, err := b.store.AIJob().List(ctx, whr)
	if err != nil {
		return nil, errno.ErrAIJobListFailed
	}
	summaries := make([]*TraceReadSummary, 0, len(list))
	for _, item := range list {
		summaries = append(summaries, traceSummaryFromAIJob(item))
	}
	return &ListTraceReadModelsResponse{
		TotalCount: total,
		Summaries:  summaries,
	}, nil
}

func (b *aiJobBiz) CompareTraceReadModels(
	ctx context.Context,
	rq *CompareTraceReadModelsRequest,
) (*CompareTraceReadModelsResponse, error) {
	if rq == nil {
		return nil, errorsx.ErrInvalidArgument
	}
	leftJobID := strings.TrimSpace(rq.LeftJobID)
	rightJobID := strings.TrimSpace(rq.RightJobID)
	if leftJobID == "" || rightJobID == "" {
		return nil, errorsx.ErrInvalidArgument
	}
	if leftJobID == rightJobID {
		return nil, errorsx.ErrInvalidArgument
	}

	left, err := b.GetTraceReadModel(ctx, &GetTraceReadModelRequest{JobID: leftJobID})
	if err != nil {
		return nil, err
	}
	right, err := b.GetTraceReadModel(ctx, &GetTraceReadModelRequest{JobID: rightJobID})
	if err != nil {
		return nil, err
	}

	leftRun := left.RunTrace
	rightRun := right.RunTrace
	sameIncident := false
	sameSession := false
	if leftRun != nil && rightRun != nil {
		leftIncidentID := strings.TrimSpace(leftRun.IncidentID)
		rightIncidentID := strings.TrimSpace(rightRun.IncidentID)
		leftSessionID := strings.TrimSpace(leftRun.SessionID)
		rightSessionID := strings.TrimSpace(rightRun.SessionID)
		sameIncident = leftIncidentID != "" && leftIncidentID == rightIncidentID
		sameSession = leftSessionID != "" && leftSessionID == rightSessionID
	}
	if !sameIncident && !sameSession {
		return nil, errorsx.ErrInvalidArgument
	}

	leftSide := buildTraceCompareSide(left)
	rightSide := buildTraceCompareSide(right)
	return &CompareTraceReadModelsResponse{
		Left:              leftSide,
		Right:             rightSide,
		SameSession:       sameSession,
		SameIncident:      sameIncident,
		ChangedRootCause:  strings.TrimSpace(leftSide.RootCauseSummary) != strings.TrimSpace(rightSide.RootCauseSummary),
		ChangedConfidence: leftSide.Confidence != rightSide.Confidence,
	}, nil
}

func traceSummaryFromAIJob(job *model.AIJobM) *TraceReadSummary {
	if job == nil {
		return &TraceReadSummary{}
	}
	runTrace := runTracePayloadToReadModel(parseRunTraceJSON(job.RunTraceJSON))
	if runTrace == nil {
		runTrace = fallbackRunTraceReadModel(job)
	}
	decision := decisionTracePayloadToReadModel(parseDecisionTraceJSON(job.DecisionTraceJSON))
	out := &TraceReadSummary{
		JobID:             strings.TrimSpace(job.JobID),
		SessionID:         trimOptional(job.SessionID),
		IncidentID:        strings.TrimSpace(job.IncidentID),
		TriggerType:       strings.TrimSpace(runTrace.TriggerType),
		TriggerSource:     strings.TrimSpace(runTrace.TriggerSource),
		Initiator:         strings.TrimSpace(runTrace.Initiator),
		Pipeline:          strings.TrimSpace(runTrace.Pipeline),
		Status:            strings.TrimSpace(runTrace.Status),
		StartedAt:         cloneOptionalTrimmedString(runTrace.StartedAt),
		FinishedAt:        cloneOptionalTrimmedString(runTrace.FinishedAt),
		ToolCallCount:     runTrace.ToolCallCount,
		EvidenceCount:     runTrace.EvidenceCount,
		VerificationCount: runTrace.VerificationCount,
		RunUpdatedAt:      strings.TrimSpace(runTrace.UpdatedAt),
	}
	if decision != nil {
		out.RootCauseType = strings.TrimSpace(decision.RootCauseType)
		out.RootCauseSummary = strings.TrimSpace(decision.RootCauseSummary)
		out.Confidence = decision.Confidence
		out.HumanReviewRequired = decision.HumanReviewRequired
		out.VerificationRefs = append([]string(nil), decision.VerificationRefs...)
		out.DecisionUpdatedAt = strings.TrimSpace(decision.UpdatedAt)
	}
	return out
}

func runTracePayloadToReadModel(in *runTracePayload) *RunTraceReadModel {
	if in == nil {
		return nil
	}
	return &RunTraceReadModel{
		SchemaVersion:     strings.TrimSpace(in.SchemaVersion),
		RunID:             strings.TrimSpace(in.RunID),
		JobID:             strings.TrimSpace(in.JobID),
		SessionID:         strings.TrimSpace(in.SessionID),
		IncidentID:        strings.TrimSpace(in.IncidentID),
		TriggerType:       strings.TrimSpace(in.TriggerType),
		TriggerSource:     strings.TrimSpace(in.TriggerSource),
		Initiator:         strings.TrimSpace(in.Initiator),
		Pipeline:          strings.TrimSpace(in.Pipeline),
		WorkerID:          strings.TrimSpace(in.WorkerID),
		WorkerVersion:     strings.TrimSpace(in.WorkerVersion),
		Status:            strings.TrimSpace(in.Status),
		StartedAt:         cloneOptionalTrimmedString(in.StartedAt),
		FinishedAt:        cloneOptionalTrimmedString(in.FinishedAt),
		ToolCallCount:     in.ToolCallCount,
		EvidenceCount:     in.EvidenceCount,
		VerificationCount: in.VerificationCount,
		ErrorSummary:      strings.TrimSpace(in.ErrorSummary),
		UpdatedAt:         strings.TrimSpace(in.UpdatedAt),
	}
}

func decisionTracePayloadToReadModel(in *decisionTracePayload) *DecisionTraceReadModel {
	if in == nil {
		return nil
	}
	return &DecisionTraceReadModel{
		SchemaVersion:       strings.TrimSpace(in.SchemaVersion),
		Status:              strings.TrimSpace(in.Status),
		RootCauseType:       strings.TrimSpace(in.RootCauseType),
		RootCauseSummary:    strings.TrimSpace(in.RootCauseSummary),
		Confidence:          in.Confidence,
		EvidenceRefs:        append([]string(nil), in.EvidenceRefs...),
		MissingFacts:        append([]string(nil), in.MissingFacts...),
		Conflicts:           append([]string(nil), in.Conflicts...),
		HumanReviewRequired: in.HumanReviewRequired,
		VerificationRefs:    append([]string(nil), in.VerificationRefs...),
		ErrorSummary:        strings.TrimSpace(in.ErrorSummary),
		UpdatedAt:           strings.TrimSpace(in.UpdatedAt),
	}
}

func fallbackRunTraceReadModel(job *model.AIJobM) *RunTraceReadModel {
	if job == nil {
		return &RunTraceReadModel{}
	}
	return &RunTraceReadModel{
		SchemaVersion:     defaultRunTraceSchemaVersion,
		RunID:             strings.TrimSpace(job.JobID),
		JobID:             strings.TrimSpace(job.JobID),
		SessionID:         trimOptional(job.SessionID),
		IncidentID:        strings.TrimSpace(job.IncidentID),
		TriggerType:       inferRunTraceTriggerType(job.Trigger),
		TriggerSource:     inferRunTraceTriggerSource("", job.Trigger),
		Initiator:         strings.TrimSpace(job.CreatedBy),
		Pipeline:          strings.TrimSpace(job.Pipeline),
		WorkerID:          trimOptional(job.LeaseOwner),
		Status:            strings.TrimSpace(job.Status),
		StartedAt:         timeToRFC3339Ptr(job.StartedAt),
		FinishedAt:        timeToRFC3339Ptr(job.FinishedAt),
		ToolCallCount:     0,
		EvidenceCount:     0,
		VerificationCount: 0,
		ErrorSummary:      trimOptional(job.ErrorMessage),
	}
}

func buildTraceCompareSide(in *GetTraceReadModelResponse) *TraceCompareSide {
	if in == nil {
		return &TraceCompareSide{}
	}
	out := &TraceCompareSide{
		JobID: strings.TrimSpace(in.JobID),
	}
	if in.RunTrace != nil {
		out.JobID = firstTraceNonEmpty(out.JobID, in.RunTrace.JobID)
		out.SessionID = strings.TrimSpace(in.RunTrace.SessionID)
		out.IncidentID = strings.TrimSpace(in.RunTrace.IncidentID)
		out.TriggerType = strings.TrimSpace(in.RunTrace.TriggerType)
		out.Pipeline = strings.TrimSpace(in.RunTrace.Pipeline)
	}
	if in.DecisionTrace != nil {
		out.RootCauseType = strings.TrimSpace(in.DecisionTrace.RootCauseType)
		out.RootCauseSummary = strings.TrimSpace(in.DecisionTrace.RootCauseSummary)
		out.Confidence = in.DecisionTrace.Confidence
		out.EvidenceRefs = append([]string(nil), in.DecisionTrace.EvidenceRefs...)
		out.VerificationRefs = append([]string(nil), in.DecisionTrace.VerificationRefs...)
		out.MissingFacts = append([]string(nil), in.DecisionTrace.MissingFacts...)
		out.Conflicts = append([]string(nil), in.DecisionTrace.Conflicts...)
	}
	return out
}

func firstTraceNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func cloneOptionalTrimmedString(in *string) *string {
	if in == nil {
		return nil
	}
	value := strings.TrimSpace(*in)
	if value == "" {
		return nil
	}
	return &value
}
