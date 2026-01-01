package ai_job

//go:generate mockgen -destination mock_ai_job.go -package ai_job github.com/aiopsre/rca-api/internal/apiserver/biz/v1/ai_job AIJobBiz

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/onexstack/onexstack/pkg/errorsx"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/audit"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/conversion"
	noticepkg "github.com/aiopsre/rca-api/internal/apiserver/pkg/notice"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	"github.com/aiopsre/rca-api/internal/pkg/contextx"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

const (
	jobStatusQueued    = "queued"
	jobStatusRunning   = "running"
	jobStatusSucceeded = "succeeded"
	jobStatusFailed    = "failed"
	jobStatusCanceled  = "canceled"

	incidentRCAStatusRunning = "running"
	incidentRCAStatusDone    = "done"
	incidentRCAStatusFailed  = "failed"

	defaultPipeline  = "basic_rca"
	defaultTrigger   = "manual"
	defaultCreatedBy = "system"

	defaultListLimit = int64(20)
	defaultToolLimit = int64(50)

	maxToolCallResponseBytes = 256 * 1024
	toolCallPreviewBytes     = 4096

	rootCauseTypeMissingEvidence  = "missing_evidence"
	rootCauseTypeConflictEvidence = "conflict_evidence"
	maxMissingEvidenceItems       = 20
)

var (
	allowedRootCauseCategory = map[string]struct{}{
		"k8s":        {},
		"db":         {},
		"network":    {},
		"app":        {},
		"dependency": {},
		"unknown":    {},
	}
)

// AIJobBiz defines AI job and tool call use-cases.
//
//nolint:interfacebloat // P0 keeps the full AIJob surface in a single biz entrypoint.
type AIJobBiz interface {
	Run(ctx context.Context, rq *v1.RunAIJobRequest) (*v1.RunAIJobResponse, error)
	Get(ctx context.Context, rq *v1.GetAIJobRequest) (*v1.GetAIJobResponse, error)
	List(ctx context.Context, rq *v1.ListAIJobsRequest) (*v1.ListAIJobsResponse, error)
	ListByIncident(ctx context.Context, rq *v1.ListIncidentAIJobsRequest) (*v1.ListIncidentAIJobsResponse, error)
	Start(ctx context.Context, rq *v1.StartAIJobRequest) (*v1.StartAIJobResponse, error)
	Cancel(ctx context.Context, rq *v1.CancelAIJobRequest) (*v1.CancelAIJobResponse, error)
	Finalize(ctx context.Context, rq *v1.FinalizeAIJobRequest) (*v1.FinalizeAIJobResponse, error)
	CreateToolCall(ctx context.Context, rq *v1.CreateAIToolCallRequest) (*v1.CreateAIToolCallResponse, error)
	ListToolCalls(ctx context.Context, rq *v1.ListAIToolCallsRequest) (*v1.ListAIToolCallsResponse, error)
	RecordToolCallAudit(ctx context.Context, rq *RecordToolCallAuditRequest) (string, error)

	AIJobExpansion
}

//nolint:modernize // Keep explicit empty interface as a placeholder expansion point.
type AIJobExpansion interface{}

type aiJobBiz struct {
	store store.IStore
}

// RecordToolCallAuditRequest writes one audit row to ai_tool_calls without AI job status gating.
// It is used by MCP read-only tool shim to reuse ToolCall audit storage.
type RecordToolCallAuditRequest struct {
	JobID             string
	Seq               int64
	NodeName          string
	ToolName          string
	RequestJSON       string
	ResponseJSON      *string
	ResponseSizeBytes int64
	Status            string
	LatencyMs         int64
	ErrorMessage      *string
}

var _ AIJobBiz = (*aiJobBiz)(nil)

// New creates ai job biz.
func New(store store.IStore) *aiJobBiz {
	return &aiJobBiz{store: store}
}

//nolint:gocognit,gocyclo,nestif // Existing transactional flow is kept for P0 compatibility.
func (b *aiJobBiz) Run(ctx context.Context, rq *v1.RunAIJobRequest) (*v1.RunAIJobResponse, error) {
	jobID := ""
	incidentID := strings.TrimSpace(rq.GetIncidentID())
	idempotencyKey := trimOptional(rq.IdempotencyKey)

	err := b.store.TX(ctx, func(txCtx context.Context) error {
		if _, err := b.getIncident(txCtx, incidentID); err != nil {
			return err
		}

		if idempotencyKey != "" {
			existing, err := b.store.AIJob().Get(txCtx, where.T(txCtx).F("idempotency_key", idempotencyKey))
			if err == nil {
				if existing.IncidentID != incidentID {
					return errno.ErrAIJobIdempotencyConflict
				}
				jobID = existing.JobID
				return nil
			}
			if err != nil && !errorsx.Is(err, gorm.ErrRecordNotFound) {
				return errno.ErrAIJobGetFailed
			}
		}

		activeCount, _, err := b.store.AIJob().List(txCtx,
			where.T(txCtx).P(0, 1).F("incident_id", incidentID).Q("status IN ?", []string{jobStatusQueued, jobStatusRunning}))
		if err != nil {
			return errno.ErrAIJobListFailed
		}
		if activeCount > 0 {
			return errno.ErrAIJobAlreadyRunning
		}

		start := rq.GetTimeRangeStart().AsTime().UTC()
		end := rq.GetTimeRangeEnd().AsTime().UTC()
		createdBy := normalizeCreatedBy(ctx, rq.CreatedBy)
		pipeline := normalizePipeline(rq.Pipeline)
		trigger := normalizeTrigger(rq.Trigger)

		job := &model.AIJobM{
			IncidentID:     incidentID,
			Pipeline:       pipeline,
			Trigger:        trigger,
			Status:         jobStatusQueued,
			TimeRangeStart: start,
			TimeRangeEnd:   end,
			CreatedBy:      createdBy,
		}
		if rq.InputHintsJSON != nil {
			v := strings.TrimSpace(rq.GetInputHintsJSON())
			job.InputHintsJSON = &v
		}
		if idempotencyKey != "" {
			job.IdempotencyKey = &idempotencyKey
		}

		if err := b.store.AIJob().Create(txCtx, job); err != nil {
			if idempotencyKey != "" && isDuplicateKeyError(err) {
				existing, getErr := b.store.AIJob().Get(txCtx, where.T(txCtx).F("idempotency_key", idempotencyKey))
				if getErr == nil && existing.IncidentID == incidentID {
					jobID = existing.JobID
					return nil
				}
				return errno.ErrAIJobIdempotencyConflict
			}
			return errno.ErrAIJobCreateFailed
		}

		incident, err := b.getIncident(txCtx, incidentID)
		if err != nil {
			return err
		}
		incident.RCAStatus = incidentRCAStatusRunning
		if err := b.store.Incident().Update(txCtx, incident); err != nil {
			return errno.ErrIncidentUpdateFailed
		}

		jobID = job.JobID
		audit.AppendIncidentTimelineIfExists(txCtx, b.store.DB(txCtx), incidentID, "ai_job_queued", jobID, map[string]any{
			"status":  jobStatusQueued,
			"trigger": trigger,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	return &v1.RunAIJobResponse{JobID: jobID}, nil
}

func (b *aiJobBiz) Get(ctx context.Context, rq *v1.GetAIJobRequest) (*v1.GetAIJobResponse, error) {
	job, err := b.store.AIJob().Get(ctx, where.T(ctx).F("job_id", strings.TrimSpace(rq.GetJobID())))
	if err != nil {
		return nil, toAIJobGetError(err)
	}
	return &v1.GetAIJobResponse{Job: conversion.AIJobMToAIJobV1(job)}, nil
}

func (b *aiJobBiz) List(ctx context.Context, rq *v1.ListAIJobsRequest) (*v1.ListAIJobsResponse, error) {
	limit := rq.GetLimit()
	if limit <= 0 {
		limit = 10
	}
	status := strings.ToLower(strings.TrimSpace(rq.GetStatus()))
	if status == "" {
		status = jobStatusQueued
	}

	total, list, err := b.store.AIJob().ListByStatus(ctx, status, int(rq.GetOffset()), int(limit), true)
	if err != nil {
		return nil, errno.ErrAIJobListFailed
	}
	out := make([]*v1.AIJob, 0, len(list))
	for _, item := range list {
		out = append(out, conversion.AIJobMToAIJobV1(item))
	}
	return &v1.ListAIJobsResponse{
		TotalCount: total,
		Jobs:       out,
	}, nil
}

func (b *aiJobBiz) ListByIncident(ctx context.Context, rq *v1.ListIncidentAIJobsRequest) (*v1.ListIncidentAIJobsResponse, error) {
	limit := rq.GetLimit()
	if limit <= 0 {
		limit = defaultListLimit
	}
	total, list, err := b.store.AIJob().List(ctx, where.T(ctx).P(int(rq.GetOffset()), int(limit)).F("incident_id", strings.TrimSpace(rq.GetIncidentID())))
	if err != nil {
		return nil, errno.ErrAIJobListFailed
	}
	out := make([]*v1.AIJob, 0, len(list))
	for _, item := range list {
		out = append(out, conversion.AIJobMToAIJobV1(item))
	}
	return &v1.ListIncidentAIJobsResponse{
		TotalCount: total,
		Jobs:       out,
	}, nil
}

//nolint:gocognit // Existing state transition logic is intentionally explicit.
func (b *aiJobBiz) Start(ctx context.Context, rq *v1.StartAIJobRequest) (*v1.StartAIJobResponse, error) {
	jobID := strings.TrimSpace(rq.GetJobID())
	err := b.store.TX(ctx, func(txCtx context.Context) error {
		job, err := b.store.AIJob().Get(txCtx, where.T(txCtx).F("job_id", jobID))
		if err != nil {
			return toAIJobGetError(err)
		}
		if job.Status == jobStatusRunning {
			return nil
		}
		if job.Status != jobStatusQueued {
			return errno.ErrAIJobInvalidTransition
		}

		now := time.Now().UTC()
		rows, err := b.store.AIJob().UpdateStatus(txCtx, jobID, []string{jobStatusQueued}, map[string]any{
			"status":     jobStatusRunning,
			"started_at": now,
		})
		if err != nil {
			return errno.ErrAIJobStartFailed
		}
		if rows == 0 {
			return errno.ErrAIJobInvalidTransition
		}

		incident, err := b.getIncident(txCtx, job.IncidentID)
		if err != nil {
			return err
		}
		incident.RCAStatus = incidentRCAStatusRunning
		if err := b.store.Incident().Update(txCtx, incident); err != nil {
			return errno.ErrIncidentUpdateFailed
		}

		audit.AppendIncidentTimelineIfExists(txCtx, b.store.DB(txCtx), job.IncidentID, "ai_job_running", jobID, map[string]any{
			"status": jobStatusRunning,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &v1.StartAIJobResponse{}, nil
}

//nolint:gocognit,gocyclo,nestif // Existing state transition logic is intentionally explicit.
func (b *aiJobBiz) Cancel(ctx context.Context, rq *v1.CancelAIJobRequest) (*v1.CancelAIJobResponse, error) {
	jobID := strings.TrimSpace(rq.GetJobID())
	reason := trimOptional(rq.Reason)

	err := b.store.TX(ctx, func(txCtx context.Context) error {
		job, err := b.store.AIJob().Get(txCtx, where.T(txCtx).F("job_id", jobID))
		if err != nil {
			return toAIJobGetError(err)
		}

		if job.Status == jobStatusCanceled {
			return nil
		}
		if job.Status != jobStatusQueued && job.Status != jobStatusRunning {
			return errno.ErrAIJobInvalidTransition
		}

		now := time.Now().UTC()
		updates := map[string]any{
			"status":      jobStatusCanceled,
			"finished_at": now,
		}
		if reason != "" {
			updates["error_message"] = reason
		}
		rows, err := b.store.AIJob().UpdateStatus(txCtx, jobID, []string{jobStatusQueued, jobStatusRunning}, updates)
		if err != nil {
			return errno.ErrAIJobCancelFailed
		}
		if rows == 0 {
			return errno.ErrAIJobInvalidTransition
		}

		incident, err := b.getIncident(txCtx, job.IncidentID)
		if err != nil {
			return err
		}
		incident.RCAStatus = incidentRCAStatusFailed
		if err := b.store.Incident().Update(txCtx, incident); err != nil {
			return errno.ErrIncidentUpdateFailed
		}

		audit.AppendIncidentTimelineIfExists(txCtx, b.store.DB(txCtx), job.IncidentID, "ai_job_canceled", jobID, map[string]any{
			"status": jobStatusCanceled,
			"reason": reason,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	return &v1.CancelAIJobResponse{}, nil
}

//nolint:gocognit,gocyclo,nestif // Existing finalize path is intentionally explicit for auditability.
func (b *aiJobBiz) Finalize(ctx context.Context, rq *v1.FinalizeAIJobRequest) (*v1.FinalizeAIJobResponse, error) {
	jobID := strings.TrimSpace(rq.GetJobID())
	targetStatus := strings.ToLower(strings.TrimSpace(rq.GetStatus()))
	var noticeReq *noticepkg.DispatchRequest

	err := b.store.TX(ctx, func(txCtx context.Context) error {
		job, err := b.store.AIJob().Get(txCtx, where.T(txCtx).F("job_id", jobID))
		if err != nil {
			return toAIJobGetError(err)
		}

		if isTerminalStatus(job.Status) {
			if job.Status == targetStatus {
				return nil
			}
			return errno.ErrAIJobInvalidTransition
		}

		fromStatuses := []string{jobStatusRunning}
		if targetStatus == jobStatusCanceled {
			fromStatuses = []string{jobStatusQueued, jobStatusRunning}
		}
		if targetStatus != jobStatusSucceeded && targetStatus != jobStatusFailed && targetStatus != jobStatusCanceled {
			return errno.ErrAIJobInvalidTransition
		}

		now := time.Now().UTC()
		updates := map[string]any{
			"status":      targetStatus,
			"finished_at": now,
		}
		if rq.OutputSummary != nil {
			updates["output_summary"] = strings.TrimSpace(rq.GetOutputSummary())
		}
		if rq.ErrorMessage != nil {
			updates["error_message"] = strings.TrimSpace(rq.GetErrorMessage())
		}

		incident, err := b.getIncident(txCtx, job.IncidentID)
		if err != nil {
			return err
		}

		incidentRCAStatus := incidentRCAStatusFailed
		evidenceIDs := normalizeStringSlice(rq.GetEvidenceIDs())

		if targetStatus == jobStatusSucceeded {
			diagnosis, diagnosisJSON, err := validateAndNormalizeDiagnosisJSON(rq.GetDiagnosisJSON())
			if err != nil {
				return err
			}

			derivedEvidenceIDs := collectDiagnosisEvidenceIDs(diagnosis)
			evidenceIDs = mergeStringSlices(evidenceIDs, derivedEvidenceIDs)
			if diagnosis.RootCause != nil && diagnosis.RootCause.Type == rootCauseTypeConflictEvidence {
				if err := b.ensureIncidentEvidenceExists(txCtx, incident.IncidentID, evidenceIDs); err != nil {
					return err
				}
			}

			updates["output_json"] = diagnosisJSON
			if rq.OutputSummary == nil {
				if summary := strings.TrimSpace(diagnosis.Summary); summary != "" {
					updates["output_summary"] = summary
				}
			}

			incidentRCAStatus = incidentRCAStatusDone
			incident.DiagnosisJSON = &diagnosisJSON
			if summary := strings.TrimSpace(diagnosis.Summary); summary != "" {
				incident.RootCauseSummary = &summary
			}
			if diagnosis.RootCause != nil {
				rootType := strings.TrimSpace(diagnosis.RootCause.Type)
				if rootType != "" {
					incident.RootCauseType = &rootType
				} else {
					category := strings.TrimSpace(diagnosis.RootCause.Category)
					if category != "" {
						incident.RootCauseType = &category
					}
				}

				rootSummary := strings.TrimSpace(diagnosis.RootCause.Summary)
				if rootSummary != "" && incident.RootCauseSummary == nil {
					incident.RootCauseSummary = &rootSummary
				}
				if strings.TrimSpace(diagnosis.RootCause.Statement) != "" && incident.RootCauseSummary == nil {
					s := strings.TrimSpace(diagnosis.RootCause.Statement)
					incident.RootCauseSummary = &s
				}
			}
			refsJSON := buildEvidenceRefsJSON(jobID, evidenceIDs)
			incident.EvidenceRefsJSON = &refsJSON

			diagnosisConfidence := 0.0
			if diagnosis.RootCause != nil {
				diagnosisConfidence = diagnosis.RootCause.Confidence
			}
			noticeReq = &noticepkg.DispatchRequest{
				EventType:           noticepkg.EventTypeDiagnosisWritten,
				JobID:               jobID,
				DiagnosisConfidence: diagnosisConfidence,
				DiagnosisEvidenceID: append([]string(nil), evidenceIDs...),
				OccurredAt:          now,
			}
		}

		if len(evidenceIDs) > 0 {
			evidenceIDsJSON := mustMarshalStringSlice(evidenceIDs)
			updates["evidence_ids_json"] = evidenceIDsJSON
		}

		rows, err := b.store.AIJob().UpdateStatus(txCtx, jobID, fromStatuses, updates)
		if err != nil {
			return errno.ErrAIJobFinalizeFailed
		}
		if rows == 0 {
			return errno.ErrAIJobInvalidTransition
		}

		incident.RCAStatus = incidentRCAStatus
		if err := b.store.Incident().Update(txCtx, incident); err != nil {
			return errno.ErrIncidentUpdateFailed
		}
		if noticeReq != nil {
			incidentCopy := *incident
			noticeReq.Incident = &incidentCopy
		}

		audit.AppendIncidentTimelineIfExists(txCtx, b.store.DB(txCtx), job.IncidentID, "ai_job_finalized", jobID, map[string]any{
			"status":       targetStatus,
			"evidence_ids": evidenceIDs,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	if noticeReq != nil {
		noticepkg.DispatchBestEffort(ctx, b.store, *noticeReq)
	}

	return &v1.FinalizeAIJobResponse{}, nil
}

//nolint:gocognit,gocyclo,nestif // Existing tool-call write path is intentionally explicit for P0.
func (b *aiJobBiz) CreateToolCall(ctx context.Context, rq *v1.CreateAIToolCallRequest) (*v1.CreateAIToolCallResponse, error) {
	jobID := strings.TrimSpace(rq.GetJobID())
	outID := ""

	err := b.store.TX(ctx, func(txCtx context.Context) error {
		job, err := b.store.AIJob().Get(txCtx, where.T(txCtx).F("job_id", jobID))
		if err != nil {
			return toAIJobGetError(err)
		}
		if isTerminalStatus(job.Status) {
			return errno.ErrAIToolCallStatusConflict
		}
		if job.Status == jobStatusQueued {
			now := time.Now().UTC()
			rows, err := b.store.AIJob().UpdateStatus(txCtx, jobID, []string{jobStatusQueued}, map[string]any{
				"status":     jobStatusRunning,
				"started_at": now,
			})
			if err != nil {
				return errno.ErrAIJobStartFailed
			}
			if rows == 0 {
				return errno.ErrAIJobInvalidTransition
			}

			incident, err := b.getIncident(txCtx, job.IncidentID)
			if err != nil {
				return err
			}
			incident.RCAStatus = incidentRCAStatusRunning
			if err := b.store.Incident().Update(txCtx, incident); err != nil {
				return errno.ErrIncidentUpdateFailed
			}
		}

		existing, err := b.store.AIToolCall().Get(txCtx, where.T(txCtx).F("job_id", jobID, "seq", rq.GetSeq()))
		if err == nil {
			outID = existing.ToolCallID
			return nil
		}
		if err != nil && !errorsx.Is(err, gorm.ErrRecordNotFound) {
			return errno.ErrAIToolCallCreateFailed
		}

		responseJSON, responseSizeBytes := normalizeToolCallResponse(trimOptional(rq.ResponseJSON), trimOptional(rq.ResponseRef))
		evidenceIDs := normalizeStringSlice(rq.GetEvidenceIDs())
		evidenceIDsJSON := mustMarshalStringSlice(evidenceIDs)

		call := &model.AIToolCallM{
			JobID:             jobID,
			Seq:               rq.GetSeq(),
			NodeName:          strings.TrimSpace(rq.GetNodeName()),
			ToolName:          strings.TrimSpace(rq.GetToolName()),
			RequestJSON:       strings.TrimSpace(rq.GetRequestJSON()),
			ResponseJSON:      responseJSON,
			ResponseSizeBytes: responseSizeBytes,
			Status:            strings.ToLower(strings.TrimSpace(rq.GetStatus())),
			LatencyMs:         rq.GetLatencyMs(),
		}
		if rq.ResponseRef != nil {
			v := strings.TrimSpace(rq.GetResponseRef())
			call.ResponseRef = &v
		}
		if rq.ErrorMessage != nil {
			v := strings.TrimSpace(rq.GetErrorMessage())
			call.ErrorMessage = &v
		}
		if len(evidenceIDs) > 0 {
			call.EvidenceIDsJSON = &evidenceIDsJSON
		}

		if err := b.store.AIToolCall().Create(txCtx, call); err != nil {
			if isDuplicateKeyError(err) {
				existing, getErr := b.store.AIToolCall().Get(txCtx, where.T(txCtx).F("job_id", jobID, "seq", rq.GetSeq()))
				if getErr == nil {
					outID = existing.ToolCallID
					return nil
				}
			}
			return errno.ErrAIToolCallCreateFailed
		}
		outID = call.ToolCallID
		return nil
	})
	if err != nil {
		return nil, err
	}

	return &v1.CreateAIToolCallResponse{ToolCallID: outID}, nil
}

//nolint:gocognit,gocyclo // Validation+persist path is intentionally explicit for audit safety.
func (b *aiJobBiz) RecordToolCallAudit(ctx context.Context, rq *RecordToolCallAuditRequest) (string, error) {
	if rq == nil {
		return "", errno.ErrAIToolCallCreateFailed
	}

	jobID := strings.TrimSpace(rq.JobID)
	nodeName := strings.TrimSpace(rq.NodeName)
	toolName := strings.TrimSpace(rq.ToolName)
	requestJSON := strings.TrimSpace(rq.RequestJSON)
	status := strings.ToLower(strings.TrimSpace(rq.Status))
	if jobID == "" || rq.Seq <= 0 || nodeName == "" || toolName == "" || requestJSON == "" {
		return "", errno.ErrAIToolCallCreateFailed
	}
	if status != "ok" && status != "error" && status != "timeout" && status != "canceled" {
		return "", errno.ErrAIToolCallInvalidStatus
	}
	if rq.LatencyMs < 0 {
		return "", errno.ErrAIToolCallCreateFailed
	}

	call := &model.AIToolCallM{
		JobID:             jobID,
		Seq:               rq.Seq,
		NodeName:          nodeName,
		ToolName:          toolName,
		RequestJSON:       requestJSON,
		ResponseSizeBytes: rq.ResponseSizeBytes,
		Status:            status,
		LatencyMs:         rq.LatencyMs,
	}
	if rq.ResponseJSON != nil {
		v := strings.TrimSpace(*rq.ResponseJSON)
		call.ResponseJSON = &v
	}
	if rq.ErrorMessage != nil {
		v := strings.TrimSpace(*rq.ErrorMessage)
		call.ErrorMessage = &v
	}

	if err := b.store.AIToolCall().Create(ctx, call); err != nil {
		if isDuplicateKeyError(err) {
			existing, getErr := b.store.AIToolCall().Get(ctx, where.T(ctx).F("job_id", jobID, "seq", rq.Seq))
			if getErr == nil {
				return existing.ToolCallID, nil
			}
		}
		return "", errno.ErrAIToolCallCreateFailed
	}
	return call.ToolCallID, nil
}

func (b *aiJobBiz) ListToolCalls(ctx context.Context, rq *v1.ListAIToolCallsRequest) (*v1.ListAIToolCallsResponse, error) {
	limit := rq.GetLimit()
	if limit <= 0 {
		limit = defaultToolLimit
	}
	whr := where.T(ctx).P(int(rq.GetOffset()), int(limit)).F("job_id", strings.TrimSpace(rq.GetJobID()))
	if rq.Seq != nil {
		whr = whr.F("seq", rq.GetSeq())
	}
	total, list, err := b.store.AIToolCall().List(ctx, whr)
	if err != nil {
		return nil, errno.ErrAIToolCallListFailed
	}
	out := make([]*v1.AIToolCall, 0, len(list))
	for _, item := range list {
		out = append(out, conversion.AIToolCallMToAIToolCallV1(item))
	}
	return &v1.ListAIToolCallsResponse{
		TotalCount: total,
		ToolCalls:  out,
	}, nil
}

func (b *aiJobBiz) getIncident(ctx context.Context, incidentID string) (*model.IncidentM, error) {
	incident, err := b.store.Incident().Get(ctx, where.T(ctx).F("incident_id", strings.TrimSpace(incidentID)))
	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrIncidentNotFound
		}
		return nil, errno.ErrIncidentGetFailed
	}
	return incident, nil
}

func normalizeCreatedBy(ctx context.Context, createdBy *string) string {
	if createdBy != nil {
		if v := strings.TrimSpace(*createdBy); v != "" {
			return v
		}
	}
	if u := strings.TrimSpace(contextx.Username(ctx)); u != "" {
		return "user:" + u
	}
	if uid := strings.TrimSpace(contextx.UserID(ctx)); uid != "" {
		return "user:" + uid
	}
	return defaultCreatedBy
}

func normalizePipeline(v *string) string {
	if v == nil {
		return defaultPipeline
	}
	if trimmed := strings.TrimSpace(*v); trimmed != "" {
		return strings.ToLower(trimmed)
	}
	return defaultPipeline
}

func normalizeTrigger(v *string) string {
	if v == nil {
		return defaultTrigger
	}
	if trimmed := strings.TrimSpace(*v); trimmed != "" {
		return strings.ToLower(trimmed)
	}
	return defaultTrigger
}

func trimOptional(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

func toAIJobGetError(err error) error {
	if errorsx.Is(err, gorm.ErrRecordNotFound) {
		return errno.ErrAIJobNotFound
	}
	return errno.ErrAIJobGetFailed
}

func isDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "duplicate") || strings.Contains(lower, "unique constraint")
}

func normalizeStringSlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, item := range in {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func mergeStringSlices(left, right []string) []string {
	return normalizeStringSlice(append(left, right...))
}

func mustMarshalStringSlice(in []string) string {
	raw, _ := json.Marshal(normalizeStringSlice(in))
	return string(raw)
}

func (b *aiJobBiz) ensureIncidentEvidenceExists(ctx context.Context, incidentID string, evidenceIDs []string) error {
	normalized := normalizeStringSlice(evidenceIDs)
	if len(normalized) == 0 {
		return nil
	}

	total, _, err := b.store.Evidence().List(ctx, where.T(ctx).
		O(0).
		L(len(normalized)).
		F("incident_id", incidentID).
		Q("evidence_id IN ?", normalized))
	if err != nil {
		return errno.ErrAIJobInvalidDiagnosis
	}
	if total != int64(len(normalized)) {
		return errno.ErrAIJobInvalidDiagnosis
	}
	return nil
}

func normalizeToolCallResponse(raw string, responseRef string) (*string, int64) {
	if raw == "" {
		return nil, 0
	}
	size := int64(len(raw))
	if len(raw) <= maxToolCallResponseBytes {
		v := raw
		return &v, size
	}

	preview := raw
	if len(preview) > toolCallPreviewBytes {
		preview = preview[:toolCallPreviewBytes]
	}

	payload := map[string]any{
		"truncated": true,
		"reason":    "max_response_bytes_exceeded",
		"preview":   preview,
	}
	if responseRef != "" {
		payload["reason"] = "stored_in_response_ref"
		payload["response_ref"] = responseRef
	}

	normalized, err := json.Marshal(payload)
	if err != nil {
		fallback := `{"truncated":true,"reason":"marshal_failed"}`
		return &fallback, size
	}

	v := string(normalized)
	return &v, size
}

func isTerminalStatus(status string) bool {
	switch status {
	case jobStatusSucceeded, jobStatusFailed, jobStatusCanceled:
		return true
	default:
		return false
	}
}

type diagnosisRootCause struct {
	Type       string   `json:"type,omitempty"`
	Category   string   `json:"category"`
	Summary    string   `json:"summary,omitempty"`
	Statement  string   `json:"statement"`
	Confidence float64  `json:"confidence"`
	EvidenceID []string `json:"evidence_ids"`
}

type diagnosisHypothesis struct {
	Statement            string   `json:"statement"`
	Confidence           float64  `json:"confidence"`
	SupportingEvidenceID []string `json:"supporting_evidence_ids"`
	MissingEvidence      []string `json:"missing_evidence"`
}

type diagnosisPayload struct {
	SchemaVersion   string                `json:"schema_version,omitempty"`
	GeneratedAt     string                `json:"generated_at,omitempty"`
	IncidentID      string                `json:"incident_id,omitempty"`
	Summary         string                `json:"summary"`
	RootCause       *diagnosisRootCause   `json:"root_cause"`
	MissingEvidence []string              `json:"missing_evidence,omitempty"`
	Timeline        []map[string]any      `json:"timeline"`
	Hypotheses      []diagnosisHypothesis `json:"hypotheses"`
	Observations    []map[string]any      `json:"observations,omitempty"`
	Recommendations []map[string]any      `json:"recommendations"`
	Unknowns        []string              `json:"unknowns"`
	NextSteps       []string              `json:"next_steps"`
}

//nolint:gocognit,gocyclo,nestif,wsl_v5 // Validation mirrors Appendix J rules with explicit branch checks.
func validateAndNormalizeDiagnosisJSON(raw string) (*diagnosisPayload, string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, "", errno.ErrAIJobInvalidDiagnosis
	}

	var diagnosis diagnosisPayload
	if err := json.Unmarshal([]byte(trimmed), &diagnosis); err != nil {
		return nil, "", errno.ErrAIJobInvalidDiagnosis
	}

	if strings.TrimSpace(diagnosis.Summary) == "" && diagnosis.RootCause == nil && len(diagnosis.Hypotheses) == 0 {
		return nil, "", errno.ErrAIJobInvalidDiagnosis
	}

	diagnosis.MissingEvidence = normalizeStringSlice(diagnosis.MissingEvidence)

	if diagnosis.RootCause != nil {
		diagnosis.RootCause.Type = strings.ToLower(strings.TrimSpace(diagnosis.RootCause.Type))
		diagnosis.RootCause.Summary = strings.TrimSpace(diagnosis.RootCause.Summary)

		category := strings.ToLower(strings.TrimSpace(diagnosis.RootCause.Category))
		if category == "" {
			category = "unknown"
		}
		if _, ok := allowedRootCauseCategory[category]; !ok {
			return nil, "", errno.ErrAIJobInvalidDiagnosis
		}
		diagnosis.RootCause.Category = category

		if diagnosis.RootCause.Confidence < 0 || diagnosis.RootCause.Confidence > 1 {
			return nil, "", errno.ErrAIJobInvalidDiagnosis
		}
		rootEvidenceIDs := normalizeStringSlice(diagnosis.RootCause.EvidenceID)
		diagnosis.RootCause.EvidenceID = rootEvidenceIDs

		switch {
		case diagnosis.RootCause.Confidence >= 0.6:
			if len(rootEvidenceIDs) < 2 {
				return nil, "", errno.ErrAIJobInvalidDiagnosis
			}
		case diagnosis.RootCause.Confidence >= 0.3:
			if len(rootEvidenceIDs) < 1 {
				return nil, "", errno.ErrAIJobInvalidDiagnosis
			}
		default:
			if strings.TrimSpace(diagnosis.RootCause.Statement) != "" {
				return nil, "", errno.ErrAIJobInvalidDiagnosis
			}
		}
		if diagnosis.RootCause.Type == rootCauseTypeMissingEvidence || diagnosis.RootCause.Type == rootCauseTypeConflictEvidence {
			if diagnosis.RootCause.Confidence > 0.3 {
				return nil, "", errno.ErrAIJobInvalidDiagnosis
			}
			if len(diagnosis.MissingEvidence) == 0 || len(diagnosis.MissingEvidence) > maxMissingEvidenceItems {
				return nil, "", errno.ErrAIJobInvalidDiagnosis
			}
		}
	}

	for i := range diagnosis.Hypotheses {
		h := &diagnosis.Hypotheses[i]
		if h.Confidence < 0 || h.Confidence > 1 {
			return nil, "", errno.ErrAIJobInvalidDiagnosis
		}
		h.SupportingEvidenceID = normalizeStringSlice(h.SupportingEvidenceID)
		h.MissingEvidence = normalizeStringSlice(h.MissingEvidence)
		if len(h.SupportingEvidenceID) == 0 && len(h.MissingEvidence) == 0 {
			return nil, "", errno.ErrAIJobInvalidDiagnosis
		}
		switch {
		case h.Confidence >= 0.6:
			if len(h.SupportingEvidenceID) < 2 {
				return nil, "", errno.ErrAIJobInvalidDiagnosis
			}
		case h.Confidence >= 0.3:
			if len(h.SupportingEvidenceID) < 1 || len(h.MissingEvidence) < 1 {
				return nil, "", errno.ErrAIJobInvalidDiagnosis
			}
		}
	}
	normalized, err := json.Marshal(diagnosis)
	if err != nil {
		return nil, "", errno.ErrAIJobInvalidDiagnosis
	}
	return &diagnosis, string(normalized), nil
}

func collectDiagnosisEvidenceIDs(diagnosis *diagnosisPayload) []string {
	if diagnosis == nil {
		return nil
	}
	out := make([]string, 0, 8)
	if diagnosis.RootCause != nil {
		out = append(out, diagnosis.RootCause.EvidenceID...)
	}
	for _, h := range diagnosis.Hypotheses {
		out = append(out, h.SupportingEvidenceID...)
	}
	return normalizeStringSlice(out)
}

func buildEvidenceRefsJSON(jobID string, evidenceIDs []string) string {
	payload := map[string]any{
		"job_id":       jobID,
		"evidence_ids": normalizeStringSlice(evidenceIDs),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return `{"job_id":"","evidence_ids":[]}`
	}
	return string(raw)
}
