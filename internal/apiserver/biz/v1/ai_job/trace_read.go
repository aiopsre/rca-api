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
	JobID         string
	RunTrace      *RunTraceReadModel
	DecisionTrace *DecisionTraceReadModel
}

type ListTraceReadModelsRequest struct {
	IncidentID *string
	SessionID  *string
	Offset     int64
	Limit      int64
}

type ListTraceReadModelsResponse struct {
	TotalCount int64
	Summaries  []*TraceReadSummary
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
	JobID               string
	SessionID           string
	IncidentID          string
	TriggerType         string
	TriggerSource       string
	Initiator           string
	Pipeline            string
	Status              string
	StartedAt           *string
	FinishedAt          *string
	ToolCallCount       int64
	EvidenceCount       int64
	VerificationCount   int64
	RootCauseType       string
	RootCauseSummary    string
	Confidence          float64
	HumanReviewRequired bool
	VerificationRefs    []string
	RunUpdatedAt        string
	DecisionUpdatedAt   string
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
