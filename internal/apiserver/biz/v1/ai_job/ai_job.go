package ai_job

//go:generate mockgen -destination mock_ai_job.go -package ai_job github.com/aiopsre/rca-api/internal/apiserver/biz/v1/ai_job AIJobBiz

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/onexstack/onexstack/pkg/errorsx"
	"gorm.io/gorm"

	kbbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/kb"
	playbookbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/playbook"
	sessionbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/session"
	verificationbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/verification"
	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/audit"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/conversion"
	noticepkg "github.com/aiopsre/rca-api/internal/apiserver/pkg/notice"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/runtimecontract"
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

	defaultListLimit        = int64(20)
	defaultToolLimit        = int64(50)
	defaultAIJobLeaseTTL    = 30 * time.Second
	defaultAIJobReclaimScan = 100

	maxToolCallResponseBytes = 256 * 1024
	toolCallPreviewBytes     = 4096

	rootCauseTypeMissingEvidence  = "missing_evidence"
	rootCauseTypeConflictEvidence = "conflict_evidence"
	maxMissingEvidenceItems       = 20

	defaultRunTraceTriggerSource = "legacy_direct"
	defaultRunTraceSchemaVersion = "v1"
	humanReviewConfidenceGate    = 0.6
	maxVerificationTraceRefs     = 20
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
	QueueSignalVersion(ctx context.Context) (int64, error)
	ListByIncident(ctx context.Context, rq *v1.ListIncidentAIJobsRequest) (*v1.ListIncidentAIJobsResponse, error)
	Start(ctx context.Context, rq *v1.StartAIJobRequest) (*v1.StartAIJobResponse, error)
	Renew(ctx context.Context, rq *v1.StartAIJobRequest) (*v1.StartAIJobResponse, error)
	Cancel(ctx context.Context, rq *v1.CancelAIJobRequest) (*v1.CancelAIJobResponse, error)
	Finalize(ctx context.Context, rq *v1.FinalizeAIJobRequest) (*v1.FinalizeAIJobResponse, error)
	CreateToolCall(ctx context.Context, rq *v1.CreateAIToolCallRequest) (*v1.CreateAIToolCallResponse, error)
	ListToolCalls(ctx context.Context, rq *v1.ListAIToolCallsRequest) (*v1.ListAIToolCallsResponse, error)
	SearchToolCalls(ctx context.Context, rq *SearchToolCallsRequest) (*SearchToolCallsResponse, error)
	RecordToolCallAudit(ctx context.Context, rq *RecordToolCallAuditRequest) (string, error)

	AIJobExpansion
}

type AIJobExpansion interface {
	GetTraceReadModel(ctx context.Context, rq *GetTraceReadModelRequest) (*GetTraceReadModelResponse, error)
	ListTraceReadModels(ctx context.Context, rq *ListTraceReadModelsRequest) (*ListTraceReadModelsResponse, error)
	CompareTraceReadModels(ctx context.Context, rq *CompareTraceReadModelsRequest) (*CompareTraceReadModelsResponse, error)
	GetSessionWorkbench(ctx context.Context, rq *GetSessionWorkbenchRequest) (*GetSessionWorkbenchResponse, error)
}

type aiJobBiz struct {
	store      store.IStore
	sessionBiz sessionbiz.SessionBiz
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

// SearchToolCallsRequest filters MCP tool-call audits by optional dimensions.
type SearchToolCallsRequest struct {
	ToolPrefix string
	ToolName   string
	JobID      string
	IncidentID string
	RequestID  string
	TimeFrom   *time.Time
	TimeTo     *time.Time
	Offset     int64
	Limit      int64
}

// SearchToolCallsResponse returns paginated tool-call audits.
type SearchToolCallsResponse struct {
	TotalCount int64
	ToolCalls  []*v1.AIToolCall
}

var _ AIJobBiz = (*aiJobBiz)(nil)

// New creates ai job biz.
func New(store store.IStore) *aiJobBiz {
	return &aiJobBiz{
		store:      store,
		sessionBiz: sessionbiz.New(store),
	}
}

//nolint:gocognit,gocyclo,nestif // Existing transactional flow is kept for P0 compatibility.
func (b *aiJobBiz) Run(ctx context.Context, rq *v1.RunAIJobRequest) (*v1.RunAIJobResponse, error) {
	jobID := ""
	runSessionID := ""
	createdNew := false
	incidentID := strings.TrimSpace(rq.GetIncidentID())
	idempotencyKey := trimOptional(rq.IdempotencyKey)

	err := b.store.TX(ctx, func(txCtx context.Context) error {
		incident, err := b.getIncident(txCtx, incidentID)
		if err != nil {
			return err
		}

		if idempotencyKey != "" {
			existing, err := b.store.AIJob().Get(txCtx, where.T(txCtx).F("idempotency_key", idempotencyKey))
			if err == nil {
				if existing.IncidentID != incidentID {
					return errno.ErrAIJobIdempotencyConflict
				}
				jobID = existing.JobID
				runSessionID = trimOptional(existing.SessionID)
				if runSessionID == "" {
					runSessionID = b.ensureIncidentSessionIDBestEffort(txCtx, incident)
					if runSessionID != "" {
						existing.SessionID = &runSessionID
						if updateErr := b.store.AIJob().Update(txCtx, existing); updateErr != nil {
							slog.WarnContext(txCtx, "ai job idempotent session backfill skipped",
								"job_id", jobID,
								"incident_id", incidentID,
								"error", updateErr,
							)
							runSessionID = ""
						}
					}
				}
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
		sessionID := b.ensureIncidentSessionIDBestEffort(txCtx, incident)
		runSessionID = sessionID

		job := &model.AIJobM{
			IncidentID:     incidentID,
			SessionID:      nil,
			Pipeline:       pipeline,
			Trigger:        trigger,
			Status:         jobStatusQueued,
			TimeRangeStart: start,
			TimeRangeEnd:   end,
			CreatedBy:      createdBy,
		}
		if sessionID != "" {
			job.SessionID = &sessionID
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
		createdNew = true
		if runTraceJSON := b.buildRunTraceJSON(ctx, job, nil, nil); runTraceJSON != "" {
			rows, updateErr := b.store.AIJob().UpdateStatus(txCtx, job.JobID, []string{jobStatusQueued}, map[string]any{
				"run_trace_json": runTraceJSON,
			})
			if updateErr != nil || rows == 0 {
				slog.WarnContext(txCtx, "ai job run trace init skipped",
					"job_id", job.JobID,
					"incident_id", incidentID,
					"error", updateErr,
				)
			} else {
				job.RunTraceJSON = &runTraceJSON
			}
		}

		incident.RCAStatus = incidentRCAStatusRunning
		if err := b.store.Incident().Update(txCtx, incident); err != nil {
			return errno.ErrIncidentUpdateFailed
		}

		jobID = job.JobID
		if _, err := b.store.AIJobQueueSignal().Bump(txCtx, time.Now().UTC()); err != nil {
			return errno.ErrAIJobCreateFailed
		}
		audit.AppendIncidentTimelineIfExists(txCtx, b.store.DB(txCtx), incidentID, "ai_job_queued", jobID, map[string]any{
			"status":  jobStatusQueued,
			"trigger": trigger,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !createdNew && jobID == "" {
		return nil, errno.ErrAIJobCreateFailed
	}
	if runSessionID != "" && jobID != "" {
		b.setSessionActiveRunBestEffort(ctx, runSessionID, jobID, "ai_job_run")
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

//nolint:gocognit,nestif // List includes reclaim + queue-read flow and keeps explicit branches.
func (b *aiJobBiz) List(ctx context.Context, rq *v1.ListAIJobsRequest) (*v1.ListAIJobsResponse, error) {
	limit := rq.GetLimit()
	if limit <= 0 {
		limit = 10
	}
	status := strings.ToLower(strings.TrimSpace(rq.GetStatus()))
	if status == "" {
		status = jobStatusQueued
	}
	if status == jobStatusQueued {
		now := time.Now().UTC()
		reclaimed, err := b.store.AIJob().ReclaimExpiredRunning(ctx, now, defaultAIJobReclaimScan)
		if err != nil {
			return nil, errno.ErrAIJobListFailed
		}
		if reclaimed > 0 {
			if _, err := b.store.AIJobQueueSignal().Bump(ctx, now); err != nil {
				return nil, errno.ErrAIJobListFailed
			}
		}
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

func (b *aiJobBiz) QueueSignalVersion(ctx context.Context) (int64, error) {
	version, err := b.store.AIJobQueueSignal().GetVersion(ctx)
	if err != nil {
		return 0, errno.ErrAIJobListFailed
	}
	return version, nil
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

//nolint:gocognit,gocyclo,nestif // Existing state transition logic is intentionally explicit.
func (b *aiJobBiz) Start(ctx context.Context, rq *v1.StartAIJobRequest) (*v1.StartAIJobResponse, error) {
	jobID := strings.TrimSpace(rq.GetJobID())
	leaseOwner, err := leaseOwnerFromContext(ctx)
	if err != nil {
		return nil, err
	}
	err = b.store.TX(ctx, func(txCtx context.Context) error {
		job, err := b.store.AIJob().Get(txCtx, where.T(txCtx).F("job_id", jobID))
		if err != nil {
			return toAIJobGetError(err)
		}
		if isTerminalStatus(job.Status) {
			return errno.ErrAIJobInvalidTransition
		}

		now := time.Now().UTC()
		rows, err := b.store.AIJob().ClaimQueued(txCtx, jobID, leaseOwner, now, defaultAIJobLeaseTTL)
		if err != nil {
			return errno.ErrAIJobStartFailed
		}
		if rows == 0 {
			renewRows, renewErr := b.store.AIJob().RenewLease(txCtx, jobID, leaseOwner, now, defaultAIJobLeaseTTL)
			if renewErr != nil {
				return errno.ErrAIJobStartFailed
			}
			if renewRows == 1 {
				b.refreshRunTraceOnStartBestEffort(txCtx, jobID, leaseOwner, now)
				return nil
			}
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
		if _, err := b.store.AIJobQueueSignal().Bump(txCtx, now); err != nil {
			return errno.ErrAIJobStartFailed
		}
		b.refreshRunTraceOnStartBestEffort(txCtx, jobID, leaseOwner, now)

		audit.AppendIncidentTimelineIfExists(txCtx, b.store.DB(txCtx), job.IncidentID, "ai_job_running", jobID, map[string]any{
			"status":      jobStatusRunning,
			"lease_owner": leaseOwner,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &v1.StartAIJobResponse{}, nil
}

func (b *aiJobBiz) Renew(ctx context.Context, rq *v1.StartAIJobRequest) (*v1.StartAIJobResponse, error) {
	jobID := strings.TrimSpace(rq.GetJobID())
	leaseOwner, err := leaseOwnerFromContext(ctx)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	rows, err := b.store.AIJob().RenewLease(ctx, jobID, leaseOwner, now, defaultAIJobLeaseTTL)
	if err != nil {
		return nil, errno.ErrAIJobStartFailed
	}
	if rows == 0 {
		return nil, errno.ErrAIJobInvalidTransition
	}
	return &v1.StartAIJobResponse{}, nil
}

//nolint:gocognit,gocyclo,nestif // Existing state transition logic is intentionally explicit.
func (b *aiJobBiz) Cancel(ctx context.Context, rq *v1.CancelAIJobRequest) (*v1.CancelAIJobResponse, error) {
	jobID := strings.TrimSpace(rq.GetJobID())
	reason := trimOptional(rq.Reason)
	leaseOwner, err := leaseOwnerFromContext(ctx)
	if err != nil {
		return nil, err
	}

	err = b.store.TX(ctx, func(txCtx context.Context) error {
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
			"status":           jobStatusCanceled,
			"finished_at":      now,
			"lease_owner":      nil,
			"lease_expires_at": nil,
			"heartbeat_at":     nil,
		}
		if reason != "" {
			updates["error_message"] = reason
		}
		leaseOwnerFilter := leaseOwner
		rows, err := b.store.AIJob().UpdateStatusWithLeaseOwner(
			txCtx,
			jobID,
			[]string{jobStatusQueued, jobStatusRunning},
			&leaseOwnerFilter,
			updates,
		)
		if err != nil {
			return errno.ErrAIJobCancelFailed
		}
		if rows == 0 {
			return errno.ErrAIJobInvalidTransition
		}
		if job.Status == jobStatusQueued {
			if _, err := b.store.AIJobQueueSignal().Bump(txCtx, now); err != nil {
				return errno.ErrAIJobCancelFailed
			}
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
	leaseOwner, err := leaseOwnerFromContext(ctx)
	if err != nil {
		return nil, err
	}
	var noticeReq *noticepkg.DispatchRequest
	var sessionPatch *sessionFinalizePatch

	err = b.store.TX(ctx, func(txCtx context.Context) error {
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
			"status":           targetStatus,
			"finished_at":      now,
			"lease_owner":      nil,
			"lease_expires_at": nil,
			"heartbeat_at":     nil,
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
		var diagnosis *diagnosisPayload
		diagnosisJSON := ""
		sessionID := trimOptional(job.SessionID)
		if sessionID == "" {
			sessionID = b.ensureIncidentSessionIDBestEffort(txCtx, incident)
			if sessionID != "" {
				updates["session_id"] = sessionID
			}
		}

		if targetStatus == jobStatusSucceeded {
			diagnosis, diagnosisJSON, err = validateAndNormalizeDiagnosisJSON(rq.GetDiagnosisJSON())
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

			var jobToolCalls []*model.AIToolCallM
			if _, jobToolCalls, err = b.store.AIToolCall().List(txCtx, where.T(txCtx).P(0, 200).F("job_id", jobID)); err != nil {
				slog.Warn("verification plan skipped: list tool calls failed", "job_id", jobID, "incident_id", incident.IncidentID, "error", err)
			} else {
				verificationPlan := b.buildVerificationPlan(txCtx, jobToolCalls)
				if len(verificationPlan) > 0 {
					if patchedDiagnosisJSON, patchErr := injectVerificationPlanIntoDiagnosis(diagnosisJSON, verificationPlan); patchErr != nil {
						slog.Warn("verification plan skipped: inject diagnosis failed", "job_id", jobID, "incident_id", incident.IncidentID, "error", patchErr)
					} else {
						diagnosisJSON = patchedDiagnosisJSON
						if mirrorErr := b.mirrorVerificationPlanToToolCall(txCtx, jobToolCalls, verificationPlan); mirrorErr != nil {
							slog.Warn("verification plan mirror failed", "job_id", jobID, "incident_id", incident.IncidentID, "error", mirrorErr)
						}
					}
				}

				playbookPayload, generatedPlaybook, playbookErr := playbookbiz.Build(playbookbiz.BuildInput{
					DiagnosisJSON: diagnosisJSON,
					RootCauseType: playbookRootCauseTypeFromDiagnosis(diagnosis),
				})
				if playbookErr != nil {
					slog.Warn("playbook generation skipped", "job_id", jobID, "incident_id", incident.IncidentID, "error", playbookErr)
				} else if generatedPlaybook {
					if patchedDiagnosisJSON, patchErr := injectPlaybookIntoDiagnosis(diagnosisJSON, playbookPayload); patchErr != nil {
						slog.Warn("playbook injection skipped: inject diagnosis failed", "job_id", jobID, "incident_id", incident.IncidentID, "error", patchErr)
					} else {
						diagnosisJSON = patchedDiagnosisJSON
						if mirrorErr := b.mirrorPlaybookToToolCall(txCtx, jobToolCalls, playbookPayload); mirrorErr != nil {
							slog.Warn("playbook mirror failed", "job_id", jobID, "incident_id", incident.IncidentID, "error", mirrorErr)
						}
					}
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
		outputSummary := trimOptional(rq.OutputSummary)
		if summaryValue, ok := updates["output_summary"].(string); ok {
			outputSummary = strings.TrimSpace(summaryValue)
		}

		runWindowStart := job.CreatedAt.UTC()
		if job.StartedAt != nil && !job.StartedAt.IsZero() {
			runWindowStart = job.StartedAt.UTC()
		}
		runWindowStart = runWindowStart.Add(-1 * time.Second)
		verificationRefs, verificationCount := b.collectVerificationTraceRefs(txCtx, jobID, job.IncidentID, runWindowStart, now)
		decisionTraceJSON := buildDecisionTraceJSON(
			targetStatus,
			outputSummary,
			diagnosis,
			evidenceIDs,
			verificationRefs,
			trimOptional(rq.ErrorMessage),
		)
		if decisionTraceJSON != "" {
			updates["decision_trace_json"] = decisionTraceJSON
		}

		toolCallCount, err := b.countToolCallsByJob(txCtx, jobID)
		if err != nil {
			slog.WarnContext(txCtx, "run trace tool call count skipped",
				"job_id", jobID,
				"incident_id", job.IncidentID,
				"error", err,
			)
		}
		evidenceCount := int64(len(normalizeStringSlice(evidenceIDs)))
		runTraceJSON := b.buildRunTraceJSON(ctx, job, job.RunTraceJSON, &runTraceOverrides{
			Status:            strPtr(targetStatus),
			FinishedAt:        &now,
			WorkerID:          strPtr(leaseOwner),
			ToolCallCount:     &toolCallCount,
			EvidenceCount:     &evidenceCount,
			VerificationCount: &verificationCount,
			ErrorSummary:      strPtr(trimOptional(rq.ErrorMessage)),
		})
		if runTraceJSON != "" {
			updates["run_trace_json"] = runTraceJSON
		}

		sessionPatch = b.buildSessionFinalizePatch(
			sessionID,
			jobID,
			job.IncidentID,
			targetStatus,
			now,
			outputSummary,
			diagnosis,
			evidenceIDs,
		)

		leaseOwnerFilter := leaseOwner
		rows, err := b.store.AIJob().UpdateStatusWithLeaseOwner(txCtx, jobID, fromStatuses, &leaseOwnerFilter, updates)
		if err != nil {
			return errno.ErrAIJobFinalizeFailed
		}
		if rows == 0 {
			return errno.ErrAIJobInvalidTransition
		}
		if targetStatus == jobStatusCanceled && job.Status == jobStatusQueued {
			if _, err := b.store.AIJobQueueSignal().Bump(txCtx, now); err != nil {
				return errno.ErrAIJobFinalizeFailed
			}
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
	if sessionPatch != nil {
		b.applySessionFinalizePatchBestEffort(ctx, sessionPatch, jobID, targetStatus)
	}

	if noticeReq != nil {
		noticepkg.DispatchBestEffort(ctx, b.store, *noticeReq)
	}
	if targetStatus == jobStatusSucceeded {
		b.runKBBestEffort(ctx, jobID)
	}

	return &v1.FinalizeAIJobResponse{}, nil
}

func (b *aiJobBiz) buildVerificationPlan(ctx context.Context, toolCalls []*model.AIToolCallM) map[string]any {
	executed := extractVerificationExecuted(toolCalls)
	kbPatterns := extractVerificationKBPatterns(toolCalls)

	prometheusID, logsID, defaultID := b.resolveVerificationDatasourceIDs(ctx, toolCalls)
	if prometheusID == "" && logsID == "" && defaultID == "" {
		return nil
	}

	plan := verificationbiz.BuildPlan(verificationbiz.BuildInput{
		Executed:     executed,
		KBPatterns:   kbPatterns,
		Now:          time.Now().UTC(),
		PrometheusID: prometheusID,
		LogsID:       logsID,
		DefaultID:    defaultID,
	})
	if plan == nil {
		return nil
	}

	raw, err := json.Marshal(plan)
	if err != nil {
		return nil
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

//nolint:gocognit,gocyclo,wsl_v5 // Datasource resolution keeps explicit precedence for deterministic fallback.
func (b *aiJobBiz) resolveVerificationDatasourceIDs(ctx context.Context, toolCalls []*model.AIToolCallM) (string, string, string) {
	// Prefer datasource_id captured in tool-call requests when available.
	for _, toolCall := range toolCalls {
		payload := parseJSONObject(toolCall.RequestJSON)
		if payload == nil {
			continue
		}
		datasourceID := strings.TrimSpace(anyToString(payload["datasource_id"]))
		if datasourceID == "" {
			datasourceID = strings.TrimSpace(anyToString(payload["datasourceID"]))
		}
		if datasourceID != "" {
			ds, err := b.store.Datasource().Get(ctx, where.T(ctx).F("datasource_id", datasourceID))
			if err == nil && ds != nil && ds.IsEnabled {
				dsType := strings.ToLower(strings.TrimSpace(ds.Type))
				switch dsType {
				case "prometheus":
					return ds.DatasourceID, "", ds.DatasourceID
				case "loki", "elasticsearch":
					return "", ds.DatasourceID, ds.DatasourceID
				default:
					return "", "", ds.DatasourceID
				}
			}
		}
	}

	// Fallback to latest enabled datasources.
	_, list, err := b.store.Datasource().List(ctx, where.T(ctx).P(0, 20).F("is_enabled", true))
	if err != nil {
		return "", "", ""
	}

	prometheusID := ""
	logsID := ""
	defaultID := ""
	for _, item := range list {
		if item == nil || !item.IsEnabled {
			continue
		}
		if defaultID == "" {
			defaultID = item.DatasourceID
		}
		dsType := strings.ToLower(strings.TrimSpace(item.Type))
		switch dsType {
		case "prometheus":
			if prometheusID == "" {
				prometheusID = item.DatasourceID
			}
		case "loki", "elasticsearch":
			if logsID == "" {
				logsID = item.DatasourceID
			}
		}
	}
	return prometheusID, logsID, defaultID
}

func (b *aiJobBiz) mirrorVerificationPlanToToolCall(ctx context.Context, toolCalls []*model.AIToolCallM, plan map[string]any) error {
	if len(plan) == 0 {
		return nil
	}
	target := selectDiagnosisRelatedToolCall(toolCalls)
	if target == nil {
		return nil
	}

	payload := map[string]any{}
	if target.ResponseJSON != nil {
		payload = parseJSONObject(*target.ResponseJSON)
	}
	if payload == nil {
		payload = make(map[string]any)
	}
	payload["verification_plan"] = plan

	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	value := string(raw)
	target.ResponseJSON = &value
	target.ResponseSizeBytes = int64(len(raw))
	return b.store.AIToolCall().Update(ctx, target)
}

func selectDiagnosisRelatedToolCall(toolCalls []*model.AIToolCallM) *model.AIToolCallM {
	if len(toolCalls) == 0 {
		return nil
	}
	candidates := make([]*model.AIToolCallM, 0, len(toolCalls))
	for _, toolCall := range toolCalls {
		toolName := strings.ToLower(strings.TrimSpace(toolCall.ToolName))
		nodeName := strings.ToLower(strings.TrimSpace(toolCall.NodeName))
		if strings.Contains(toolName, "diagnosis") || strings.Contains(nodeName, "synthesize") {
			candidates = append(candidates, toolCall)
		}
	}
	if len(candidates) == 0 {
		candidates = toolCalls
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Seq != candidates[j].Seq {
			return candidates[i].Seq < candidates[j].Seq
		}
		return candidates[i].ID < candidates[j].ID
	})
	return candidates[len(candidates)-1]
}

//nolint:gocognit // Extraction keeps explicit guards for heterogeneous historical payloads.
func extractVerificationExecuted(toolCalls []*model.AIToolCallM) []string {
	if len(toolCalls) == 0 {
		return nil
	}
	for _, toolCall := range toolCalls {
		payload := parseToolCallResponse(toolCall)
		if payload == nil {
			continue
		}
		evidencePlan, _ := payload["evidence_plan"].(map[string]any)
		if evidencePlan == nil {
			continue
		}
		executedAny, _ := evidencePlan["executed"].([]any)
		if len(executedAny) == 0 {
			continue
		}
		out := make([]string, 0, len(executedAny))
		for _, item := range executedAny {
			value := strings.TrimSpace(anyToString(item))
			if value == "" {
				continue
			}
			out = append(out, value)
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

//nolint:gocognit // Pattern extraction keeps explicit nested parsing for stable ordering/dedup.
func extractVerificationKBPatterns(toolCalls []*model.AIToolCallM) []verificationbiz.KBPattern {
	out := make([]verificationbiz.KBPattern, 0, 8)
	seen := map[string]struct{}{}

	for _, toolCall := range toolCalls {
		payload := parseToolCallResponse(toolCall)
		if payload == nil {
			continue
		}
		refs, _ := payload["kb_refs"].([]any)
		for _, ref := range refs {
			refObj, _ := ref.(map[string]any)
			if refObj == nil {
				continue
			}
			patterns, _ := refObj["patterns"].([]any)
			for _, pattern := range patterns {
				patternObj, _ := pattern.(map[string]any)
				if patternObj == nil {
					continue
				}
				pType := strings.TrimSpace(anyToString(patternObj["type"]))
				pValue := strings.TrimSpace(anyToString(patternObj["value"]))
				if pType == "" || pValue == "" {
					continue
				}
				key := strings.ToLower(pType) + ":" + strings.ToLower(pValue)
				if _, ok := seen[key]; ok {
					continue
				}
				seen[key] = struct{}{}
				out = append(out, verificationbiz.KBPattern{
					Type:  pType,
					Value: pValue,
				})
			}
		}
	}
	return out
}

func injectVerificationPlanIntoDiagnosis(diagnosisJSON string, plan map[string]any) (string, error) {
	payload := parseJSONObject(diagnosisJSON)
	if payload == nil {
		return "", errno.ErrAIJobInvalidDiagnosis
	}
	payload["verification_plan"] = plan
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func injectPlaybookIntoDiagnosis(diagnosisJSON string, playbook *playbookbiz.Playbook) (string, error) {
	if playbook == nil {
		return diagnosisJSON, nil
	}
	payload := parseJSONObject(diagnosisJSON)
	if payload == nil {
		return "", errno.ErrAIJobInvalidDiagnosis
	}
	payload["playbook"] = playbook
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func playbookRootCauseTypeFromDiagnosis(diagnosis *diagnosisPayload) string {
	if diagnosis == nil || diagnosis.RootCause == nil {
		return ""
	}
	if value := strings.ToLower(strings.TrimSpace(diagnosis.RootCause.Type)); value != "" {
		return value
	}
	return strings.ToLower(strings.TrimSpace(diagnosis.RootCause.Category))
}

func (b *aiJobBiz) mirrorPlaybookToToolCall(ctx context.Context, toolCalls []*model.AIToolCallM, playbook *playbookbiz.Playbook) error {
	if playbook == nil {
		return nil
	}
	target := selectDiagnosisRelatedToolCall(toolCalls)
	if target == nil {
		return nil
	}

	payload := map[string]any{}
	if target.ResponseJSON != nil {
		payload = parseJSONObject(*target.ResponseJSON)
	}
	if payload == nil {
		payload = make(map[string]any)
	}
	payload["playbook"] = playbook

	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	value := string(raw)
	target.ResponseJSON = &value
	target.ResponseSizeBytes = int64(len(raw))
	return b.store.AIToolCall().Update(ctx, target)
}

//nolint:gocognit,gocyclo // Best-effort path keeps explicit early-return branches for safety.
func (b *aiJobBiz) runKBBestEffort(ctx context.Context, jobID string) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return
	}

	job, err := b.store.AIJob().Get(ctx, where.T(ctx).F("job_id", jobID))
	if err != nil {
		slog.Warn("kb best-effort skipped: get ai job failed", "job_id", jobID, "error", err)
		return
	}
	if strings.ToLower(strings.TrimSpace(job.Status)) != jobStatusSucceeded {
		return
	}

	incident, err := b.store.Incident().Get(ctx, where.T(ctx).F("incident_id", job.IncidentID))
	if err != nil {
		slog.Warn("kb best-effort skipped: get incident failed", "job_id", jobID, "incident_id", job.IncidentID, "error", err)
		return
	}
	if incident.DiagnosisJSON == nil || strings.TrimSpace(*incident.DiagnosisJSON) == "" {
		return
	}

	_, toolCalls, err := b.store.AIToolCall().List(ctx, where.T(ctx).P(0, 200).F("job_id", jobID))
	if err != nil {
		slog.Warn("kb best-effort skipped: list tool calls failed", "job_id", jobID, "error", err)
		return
	}
	if len(toolCalls) == 0 {
		return
	}

	qualityDecision := kbbiz.ExtractQualityGateDecision(toolCalls)
	if qualityDecision != "pass" {
		return
	}

	rootCauseType := ""
	if incident.RootCauseType != nil {
		rootCauseType = strings.TrimSpace(*incident.RootCauseType)
	}
	rootCauseSummary := ""
	if incident.RootCauseSummary != nil {
		rootCauseSummary = strings.TrimSpace(*incident.RootCauseSummary)
	}
	if rootCauseType == "" || rootCauseSummary == "" {
		return
	}

	patterns := kbbiz.ExtractPatternsFromDiagnosis(*incident.DiagnosisJSON)
	if len(patterns) == 0 {
		patterns = kbbiz.ExtractPatternsFromToolCalls(toolCalls)
	}
	if len(patterns) == 0 {
		return
	}

	kb := kbbiz.New(b.store)
	_, err = kb.Writeback(ctx, kbbiz.WritebackInput{
		Namespace:         incident.Namespace,
		Service:           incident.Service,
		RootCauseType:     rootCauseType,
		RootCauseSummary:  rootCauseSummary,
		Patterns:          patterns,
		EvidenceSignature: kbbiz.BuildEvidenceSignature(*incident.DiagnosisJSON, toolCalls),
		Confidence:        extractDiagnosisRootCauseConfidence(*incident.DiagnosisJSON),
	})
	if err != nil {
		slog.Warn("kb writeback failed (best-effort)", "job_id", jobID, "incident_id", incident.IncidentID, "error", err)
		return
	}

	refs, err := kb.Search(ctx, kbbiz.SearchInput{
		Namespace:     incident.Namespace,
		Service:       incident.Service,
		RootCauseType: rootCauseType,
		Patterns:      patterns,
		Limit:         3,
	})
	if err != nil {
		slog.Warn("kb retrieval failed (best-effort)", "job_id", jobID, "incident_id", incident.IncidentID, "error", err)
		return
	}
	if len(refs) == 0 {
		return
	}

	targetToolCall := kbbiz.SelectPrimaryToolCall(toolCalls)
	if targetToolCall == nil {
		return
	}
	if err := kb.InjectRefsToToolCall(ctx, targetToolCall, refs); err != nil {
		slog.Warn("kb refs injection failed (best-effort)", "job_id", jobID, "incident_id", incident.IncidentID, "tool_call_id", targetToolCall.ToolCallID, "error", err)
	}
}

//nolint:gocognit,gocyclo,nestif // Existing tool-call write path is intentionally explicit for P0.
func (b *aiJobBiz) CreateToolCall(ctx context.Context, rq *v1.CreateAIToolCallRequest) (*v1.CreateAIToolCallResponse, error) {
	jobID := strings.TrimSpace(rq.GetJobID())
	leaseOwner, err := leaseOwnerFromContext(ctx)
	if err != nil {
		return nil, err
	}
	outID := ""

	err = b.store.TX(ctx, func(txCtx context.Context) error {
		job, err := b.store.AIJob().Get(txCtx, where.T(txCtx).F("job_id", jobID))
		if err != nil {
			return toAIJobGetError(err)
		}
		if isTerminalStatus(job.Status) {
			return errno.ErrAIToolCallStatusConflict
		}

		now := time.Now().UTC()
		if job.Status == jobStatusQueued {
			rows, err := b.store.AIJob().ClaimQueued(txCtx, jobID, leaseOwner, now, defaultAIJobLeaseTTL)
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
			if _, err := b.store.AIJobQueueSignal().Bump(txCtx, now); err != nil {
				return errno.ErrAIJobStartFailed
			}
		} else if job.Status == jobStatusRunning {
			renewRows, renewErr := b.store.AIJob().RenewLease(txCtx, jobID, leaseOwner, now, defaultAIJobLeaseTTL)
			if renewErr != nil {
				return errno.ErrAIJobStartFailed
			}
			if renewRows == 0 {
				return errno.ErrAIToolCallStatusConflict
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

//nolint:gocognit,gocyclo // Search filters are intentionally explicit and composable.
func (b *aiJobBiz) SearchToolCalls(
	ctx context.Context,
	rq *SearchToolCallsRequest,
) (*SearchToolCallsResponse, error) {

	if rq == nil {
		return nil, errno.ErrAIToolCallListFailed
	}

	limit := rq.Limit
	if limit <= 0 {
		limit = defaultToolLimit
	}

	whr := where.T(ctx).O(int(rq.Offset)).L(int(limit))
	if jobID := strings.TrimSpace(rq.JobID); jobID != "" {
		whr = whr.F("job_id", jobID)
	}

	if toolName := strings.TrimSpace(rq.ToolName); toolName != "" {
		whr = whr.F("tool_name", toolName)
	} else if toolPrefix := strings.TrimSpace(rq.ToolPrefix); toolPrefix != "" {
		whr = whr.Q("tool_name LIKE ?", toolPrefix+"%")
	}

	if incidentID := strings.TrimSpace(rq.IncidentID); incidentID != "" {
		whr = whr.Q("request_json LIKE ?", `%"incident_id":"`+incidentID+`"%`)
	}
	if requestID := strings.TrimSpace(rq.RequestID); requestID != "" {
		whr = whr.Q("request_json LIKE ?", `%"request_id":"`+requestID+`"%`)
	}

	if rq.TimeFrom != nil {
		whr = whr.Q("created_at >= ?", rq.TimeFrom.UTC())
	}
	if rq.TimeTo != nil {
		whr = whr.Q("created_at <= ?", rq.TimeTo.UTC())
	}

	total, list, err := b.store.AIToolCall().List(ctx, whr)
	if err != nil {
		return nil, errno.ErrAIToolCallListFailed
	}

	out := make([]*v1.AIToolCall, 0, len(list))
	for _, item := range list {
		out = append(out, conversion.AIToolCallMToAIToolCallV1(item))
	}
	return &SearchToolCallsResponse{
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

func leaseOwnerFromContext(ctx context.Context) (string, error) {
	owner := strings.TrimSpace(contextx.OrchestratorInstanceID(ctx))
	if owner == "" {
		return "", errorsx.ErrInvalidArgument
	}
	return owner, nil
}

type sessionFinalizePatch struct {
	SessionID          string
	LatestSummaryJSON  *string
	PinnedEvidenceJSON *string
	ActiveRunID        *string
}

func (b *aiJobBiz) ensureIncidentSessionIDBestEffort(ctx context.Context, incident *model.IncidentM) string {
	if b == nil || b.sessionBiz == nil || incident == nil {
		return ""
	}
	incidentID := strings.TrimSpace(incident.IncidentID)
	if incidentID == "" {
		return ""
	}

	title := sessionTitleFromIncident(incident)
	resp, err := b.sessionBiz.EnsureIncidentSession(ctx, &sessionbiz.EnsureIncidentSessionRequest{
		IncidentID: incidentID,
		Title:      title,
	})
	if err != nil {
		slog.WarnContext(ctx, "ai job session ensure skipped",
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

func (b *aiJobBiz) setSessionActiveRunBestEffort(ctx context.Context, sessionID string, jobID string, phase string) {
	if b == nil || b.sessionBiz == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	jobID = strings.TrimSpace(jobID)
	if sessionID == "" || jobID == "" {
		return
	}

	if _, err := b.sessionBiz.Update(ctx, &sessionbiz.UpdateSessionContextRequest{
		SessionID:   sessionID,
		ActiveRunID: &jobID,
	}); err != nil {
		slog.WarnContext(ctx, "ai job session active_run update skipped",
			"phase", phase,
			"session_id", sessionID,
			"job_id", jobID,
			"error", err,
		)
	}
}

func (b *aiJobBiz) buildSessionFinalizePatch(
	sessionID string,
	jobID string,
	incidentID string,
	targetStatus string,
	now time.Time,
	outputSummary string,
	diagnosis *diagnosisPayload,
	evidenceIDs []string,
) *sessionFinalizePatch {

	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}

	patch := &sessionFinalizePatch{
		SessionID: sessionID,
	}
	empty := ""
	patch.ActiveRunID = &empty
	if targetStatus != jobStatusSucceeded {
		return patch
	}

	latestSummaryJSON := buildSessionLatestSummaryJSON(now, jobID, incidentID, outputSummary, diagnosis, evidenceIDs)
	if latestSummaryJSON != "" {
		patch.LatestSummaryJSON = &latestSummaryJSON
	}
	if len(evidenceIDs) > 0 {
		pinnedEvidenceJSON := buildSessionPinnedEvidenceJSON(now, jobID, evidenceIDs)
		if pinnedEvidenceJSON != "" {
			patch.PinnedEvidenceJSON = &pinnedEvidenceJSON
		}
	}
	return patch
}

func (b *aiJobBiz) applySessionFinalizePatchBestEffort(
	ctx context.Context,
	patch *sessionFinalizePatch,
	jobID string,
	targetStatus string,
) {
	if b == nil || b.sessionBiz == nil || patch == nil {
		return
	}
	if strings.TrimSpace(patch.SessionID) == "" {
		return
	}

	if _, err := b.sessionBiz.Update(ctx, &sessionbiz.UpdateSessionContextRequest{
		SessionID:          patch.SessionID,
		LatestSummaryJSON:  patch.LatestSummaryJSON,
		PinnedEvidenceJSON: patch.PinnedEvidenceJSON,
		ActiveRunID:        patch.ActiveRunID,
	}); err != nil {
		slog.WarnContext(ctx, "ai job finalize session patch skipped",
			"session_id", patch.SessionID,
			"job_id", strings.TrimSpace(jobID),
			"status", targetStatus,
			"error", err,
		)
	}
}

func sessionTitleFromIncident(incident *model.IncidentM) *string {
	if incident == nil {
		return nil
	}
	if v := strings.TrimSpace(incident.Service); v != "" {
		return &v
	}
	if v := strings.TrimSpace(incident.WorkloadName); v != "" {
		return &v
	}
	return nil
}

func buildSessionLatestSummaryJSON(
	now time.Time,
	jobID string,
	incidentID string,
	outputSummary string,
	diagnosis *diagnosisPayload,
	evidenceIDs []string,
) string {
	rootCauseType := ""
	confidence := 0.0
	derivedSummary := strings.TrimSpace(outputSummary)
	if diagnosis != nil {
		if diagnosis.RootCause != nil {
			rootCauseType = strings.TrimSpace(diagnosis.RootCause.Type)
			if rootCauseType == "" {
				rootCauseType = strings.TrimSpace(diagnosis.RootCause.Category)
			}
			confidence = diagnosis.RootCause.Confidence
			if derivedSummary == "" {
				derivedSummary = strings.TrimSpace(diagnosis.RootCause.Summary)
			}
			if derivedSummary == "" {
				derivedSummary = strings.TrimSpace(diagnosis.RootCause.Statement)
			}
		}
		if derivedSummary == "" {
			derivedSummary = strings.TrimSpace(diagnosis.Summary)
		}
	}

	payload := map[string]any{
		"job_id":          strings.TrimSpace(jobID),
		"incident_id":     strings.TrimSpace(incidentID),
		"root_cause_type": rootCauseType,
		"summary":         derivedSummary,
		"confidence":      confidence,
		"updated_at":      now.UTC().Format(time.RFC3339Nano),
		"evidence_refs":   normalizeStringSlice(evidenceIDs),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(raw)
}

func buildSessionPinnedEvidenceJSON(now time.Time, jobID string, evidenceIDs []string) string {
	normalizedEvidenceIDs := normalizeStringSlice(evidenceIDs)
	if len(normalizedEvidenceIDs) == 0 {
		return ""
	}

	payload := map[string]any{
		"refs":       normalizedEvidenceIDs,
		"source":     "ai_job_finalize",
		"job_id":     strings.TrimSpace(jobID),
		"updated_at": now.UTC().Format(time.RFC3339Nano),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(raw)
}

type runTracePayload struct {
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

type runTraceOverrides struct {
	Status            *string
	StartedAt         *time.Time
	FinishedAt        *time.Time
	WorkerID          *string
	WorkerVersion     *string
	ToolCallCount     *int64
	EvidenceCount     *int64
	VerificationCount *int64
	ErrorSummary      *string
	TriggerType       *string
	TriggerSource     *string
	Initiator         *string
}

type decisionTracePayload struct {
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

func (b *aiJobBiz) refreshRunTraceOnStartBestEffort(
	ctx context.Context,
	jobID string,
	leaseOwner string,
	now time.Time,
) {
	if b == nil {
		return
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return
	}
	job, err := b.store.AIJob().Get(ctx, where.T(ctx).F("job_id", jobID))
	if err != nil {
		slog.WarnContext(ctx, "ai job run trace start refresh skipped",
			"job_id", jobID,
			"error", err,
		)
		return
	}

	startedAt := now.UTC()
	if job.StartedAt != nil && !job.StartedAt.IsZero() {
		startedAt = job.StartedAt.UTC()
	}
	runTraceJSON := b.buildRunTraceJSON(ctx, job, job.RunTraceJSON, &runTraceOverrides{
		Status:    strPtr(jobStatusRunning),
		StartedAt: &startedAt,
		WorkerID:  strPtr(leaseOwner),
	})
	if runTraceJSON == "" {
		return
	}
	rows, updateErr := b.store.AIJob().UpdateStatus(ctx, jobID, []string{jobStatusRunning}, map[string]any{
		"run_trace_json": runTraceJSON,
	})
	if updateErr != nil || rows == 0 {
		slog.WarnContext(ctx, "ai job run trace start persist skipped",
			"job_id", jobID,
			"rows", rows,
			"error", updateErr,
		)
	}
}

func (b *aiJobBiz) buildRunTraceJSON(
	ctx context.Context,
	job *model.AIJobM,
	existingRaw *string,
	overrides *runTraceOverrides,
) string {
	if job == nil {
		return ""
	}

	trace := parseRunTraceJSON(existingRaw)
	if trace == nil {
		trace = &runTracePayload{
			SchemaVersion: defaultRunTraceSchemaVersion,
		}
	}
	trace.RunID = strings.TrimSpace(job.JobID)
	trace.JobID = strings.TrimSpace(job.JobID)
	trace.IncidentID = strings.TrimSpace(job.IncidentID)
	trace.SessionID = trimOptional(job.SessionID)
	trace.Pipeline = strings.TrimSpace(job.Pipeline)
	if trace.Pipeline == "" {
		trace.Pipeline = defaultPipeline
	}

	if trace.TriggerType == "" {
		trace.TriggerType = inferRunTraceTriggerType(job.Trigger)
	}
	if ctxTriggerType := normalizeRunTraceTriggerType(contextx.TriggerType(ctx)); ctxTriggerType != "" {
		trace.TriggerType = ctxTriggerType
	}
	if trace.TriggerSource == "" {
		trace.TriggerSource = inferRunTraceTriggerSource(trace.TriggerType, job.Trigger)
	}
	if ctxTriggerSource := strings.TrimSpace(contextx.TriggerSource(ctx)); ctxTriggerSource != "" {
		trace.TriggerSource = ctxTriggerSource
	}
	if trace.Initiator == "" {
		trace.Initiator = strings.TrimSpace(job.CreatedBy)
	}
	if ctxTriggerInitiator := strings.TrimSpace(contextx.TriggerInitiator(ctx)); ctxTriggerInitiator != "" {
		trace.Initiator = ctxTriggerInitiator
	}

	trace.Status = strings.ToLower(strings.TrimSpace(job.Status))
	if trace.Status == "" {
		trace.Status = jobStatusQueued
	}
	trace.StartedAt = timeToRFC3339Ptr(job.StartedAt)
	trace.FinishedAt = timeToRFC3339Ptr(job.FinishedAt)
	trace.WorkerID = trimOptional(job.LeaseOwner)
	trace.ErrorSummary = trimOptional(job.ErrorMessage)
	if trace.ToolCallCount < 0 {
		trace.ToolCallCount = 0
	}
	if trace.EvidenceCount < 0 {
		trace.EvidenceCount = 0
	}
	if trace.VerificationCount < 0 {
		trace.VerificationCount = 0
	}

	updatedAt := time.Now().UTC()
	if overrides != nil {
		if overrides.Status != nil {
			trace.Status = strings.ToLower(strings.TrimSpace(*overrides.Status))
		}
		if overrides.StartedAt != nil {
			trace.StartedAt = timeToRFC3339Ptr(overrides.StartedAt)
			updatedAt = overrides.StartedAt.UTC()
		}
		if overrides.FinishedAt != nil {
			trace.FinishedAt = timeToRFC3339Ptr(overrides.FinishedAt)
			updatedAt = overrides.FinishedAt.UTC()
		}
		if overrides.WorkerID != nil {
			trace.WorkerID = strings.TrimSpace(*overrides.WorkerID)
		}
		if overrides.WorkerVersion != nil {
			trace.WorkerVersion = strings.TrimSpace(*overrides.WorkerVersion)
		}
		if overrides.ToolCallCount != nil && *overrides.ToolCallCount >= 0 {
			trace.ToolCallCount = *overrides.ToolCallCount
		}
		if overrides.EvidenceCount != nil && *overrides.EvidenceCount >= 0 {
			trace.EvidenceCount = *overrides.EvidenceCount
		}
		if overrides.VerificationCount != nil && *overrides.VerificationCount >= 0 {
			trace.VerificationCount = *overrides.VerificationCount
		}
		if overrides.ErrorSummary != nil {
			trace.ErrorSummary = strings.TrimSpace(*overrides.ErrorSummary)
		}
		if overrides.TriggerType != nil {
			trace.TriggerType = normalizeRunTraceTriggerType(*overrides.TriggerType)
		}
		if overrides.TriggerSource != nil {
			trace.TriggerSource = strings.TrimSpace(*overrides.TriggerSource)
		}
		if overrides.Initiator != nil {
			trace.Initiator = strings.TrimSpace(*overrides.Initiator)
		}
	}
	trace.UpdatedAt = updatedAt.Format(time.RFC3339Nano)

	raw, err := json.Marshal(trace)
	if err != nil {
		return ""
	}
	return string(raw)
}

func parseRunTraceJSON(raw *string) *runTracePayload {
	trimmed := trimOptional(raw)
	if trimmed == "" {
		return nil
	}
	var payload runTracePayload
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return nil
	}
	return &payload
}

func parseDecisionTraceJSON(raw *string) *decisionTracePayload {
	trimmed := trimOptional(raw)
	if trimmed == "" {
		return nil
	}
	var payload decisionTracePayload
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return nil
	}
	return &payload
}

func inferRunTraceTriggerType(jobTrigger string) string {
	switch strings.ToLower(strings.TrimSpace(jobTrigger)) {
	case "manual":
		return "manual"
	case "on_ingest":
		return "alert"
	case "on_escalation":
		return "incident"
	case "scheduled":
		return "scheduled"
	case "cron":
		return "cron"
	case "change":
		return "change"
	default:
		return strings.ToLower(strings.TrimSpace(jobTrigger))
	}
}

func normalizeRunTraceTriggerType(triggerType string) string {
	triggerType = strings.ToLower(strings.TrimSpace(triggerType))
	switch triggerType {
	case "manual", "alert", "incident", "scheduled", "replay", "follow_up":
		return triggerType
	default:
		return triggerType
	}
}

func inferRunTraceTriggerSource(triggerType string, jobTrigger string) string {
	if triggerType == "" {
		triggerType = inferRunTraceTriggerType(jobTrigger)
	}
	switch triggerType {
	case "manual":
		return "manual_api"
	case "alert":
		return "alert_ingest"
	case "incident":
		return "incident_update"
	case "scheduled":
		return "incident_scheduler"
	case "replay":
		return "replay_router"
	case "follow_up":
		return "follow_up_router"
	case "cron":
		return "cron_router"
	case "change":
		return "change_router"
	default:
		return defaultRunTraceTriggerSource
	}
}

func timeToRFC3339Ptr(ts *time.Time) *string {
	if ts == nil || ts.IsZero() {
		return nil
	}
	value := ts.UTC().Format(time.RFC3339Nano)
	return &value
}

func (b *aiJobBiz) countToolCallsByJob(ctx context.Context, jobID string) (int64, error) {
	total, _, err := b.store.AIToolCall().List(ctx, where.T(ctx).O(0).L(1).F("job_id", strings.TrimSpace(jobID)))
	if err != nil {
		return 0, err
	}
	return total, nil
}

func (b *aiJobBiz) collectVerificationTraceRefs(
	ctx context.Context,
	jobID string,
	incidentID string,
	from time.Time,
	to time.Time,
) ([]string, int64) {
	jobID = strings.TrimSpace(jobID)
	incidentID = strings.TrimSpace(incidentID)
	if jobID == "" && incidentID == "" {
		return nil, 0
	}

	buildTimeRangeFilters := func(whr *where.Options) *where.Options {
		if whr == nil {
			whr = where.T(ctx)
		}
		if !from.IsZero() {
			whr = whr.Q("created_at >= ?", from.UTC())
		}
		if !to.IsZero() {
			whr = whr.Q("created_at <= ?", to.UTC())
		}
		return whr
	}
	listRefs := func(whr *where.Options) ([]string, int64, error) {
		total, list, err := b.store.IncidentVerificationRun().List(ctx, whr)
		if err != nil {
			return nil, 0, err
		}
		refs := make([]string, 0, len(list))
		for _, item := range list {
			if item == nil {
				continue
			}
			runID := strings.TrimSpace(item.RunID)
			if runID == "" {
				continue
			}
			refs = append(refs, runID)
		}
		return normalizeStringSlice(refs), total, nil
	}

	if jobID != "" {
		jobWhr := buildTimeRangeFilters(where.T(ctx).O(0).L(maxVerificationTraceRefs).F("job_id", jobID))
		if refs, total, err := listRefs(jobWhr); err == nil {
			if total > 0 {
				return refs, total
			}
		} else {
			slog.WarnContext(ctx, "verification trace refs by job skipped",
				"job_id", jobID,
				"incident_id", incidentID,
				"error", err,
			)
		}
	}

	if incidentID == "" {
		return nil, 0
	}
	incidentWhr := buildTimeRangeFilters(where.T(ctx).O(0).L(maxVerificationTraceRefs).F("incident_id", incidentID))
	refs, total, err := listRefs(incidentWhr)
	if err != nil {
		slog.WarnContext(ctx, "verification trace refs skipped",
			"job_id", jobID,
			"incident_id", incidentID,
			"error", err,
		)
		return nil, 0
	}
	return refs, total
}

func buildDecisionTraceJSON(
	status string,
	outputSummary string,
	diagnosis *diagnosisPayload,
	evidenceIDs []string,
	verificationRefs []string,
	errorSummary string,
) string {
	status = strings.ToLower(strings.TrimSpace(status))
	rootCauseType := ""
	rootCauseSummary := strings.TrimSpace(outputSummary)
	confidence := 0.0

	if diagnosis != nil {
		if diagnosis.RootCause != nil {
			rootCauseType = strings.TrimSpace(diagnosis.RootCause.Type)
			if rootCauseType == "" {
				rootCauseType = strings.TrimSpace(diagnosis.RootCause.Category)
			}
			confidence = normalizeDecisionTraceConfidence(diagnosis.RootCause.Confidence)
			if rootCauseSummary == "" {
				rootCauseSummary = strings.TrimSpace(diagnosis.RootCause.Summary)
			}
			if rootCauseSummary == "" {
				rootCauseSummary = strings.TrimSpace(diagnosis.RootCause.Statement)
			}
		}
		if rootCauseSummary == "" {
			rootCauseSummary = strings.TrimSpace(diagnosis.Summary)
		}
	}

	missingFacts := collectDecisionMissingFacts(diagnosis)
	conflicts := collectDecisionConflicts(diagnosis)
	errorSummary = strings.TrimSpace(errorSummary)

	humanReviewRequired := status != jobStatusSucceeded
	if !humanReviewRequired {
		if confidence < humanReviewConfidenceGate {
			humanReviewRequired = true
		}
		if len(missingFacts) > 0 || len(conflicts) > 0 {
			humanReviewRequired = true
		}
		if rootCauseType == rootCauseTypeMissingEvidence || rootCauseType == rootCauseTypeConflictEvidence {
			humanReviewRequired = true
		}
	}
	if errorSummary != "" {
		humanReviewRequired = true
	}

	payload := decisionTracePayload{
		SchemaVersion:       defaultRunTraceSchemaVersion,
		Status:              status,
		RootCauseType:       rootCauseType,
		RootCauseSummary:    rootCauseSummary,
		Confidence:          confidence,
		EvidenceRefs:        normalizeStringSlice(evidenceIDs),
		MissingFacts:        missingFacts,
		Conflicts:           conflicts,
		HumanReviewRequired: humanReviewRequired,
		VerificationRefs:    normalizeStringSlice(verificationRefs),
		ErrorSummary:        errorSummary,
		UpdatedAt:           time.Now().UTC().Format(time.RFC3339Nano),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(raw)
}

func collectDecisionMissingFacts(diagnosis *diagnosisPayload) []string {
	if diagnosis == nil {
		return nil
	}
	out := make([]string, 0, len(diagnosis.MissingEvidence)+len(diagnosis.Hypotheses))
	out = append(out, diagnosis.MissingEvidence...)
	for _, hypothesis := range diagnosis.Hypotheses {
		out = append(out, hypothesis.MissingEvidence...)
	}
	return normalizeStringSlice(out)
}

func collectDecisionConflicts(diagnosis *diagnosisPayload) []string {
	if diagnosis == nil || diagnosis.RootCause == nil {
		return nil
	}
	if strings.TrimSpace(diagnosis.RootCause.Type) != rootCauseTypeConflictEvidence {
		return nil
	}
	out := make([]string, 0, 2)
	if summary := strings.TrimSpace(diagnosis.RootCause.Summary); summary != "" {
		out = append(out, summary)
	}
	if statement := strings.TrimSpace(diagnosis.RootCause.Statement); statement != "" {
		out = append(out, statement)
	}
	if len(out) == 0 {
		out = append(out, "conflicting evidence detected")
	}
	return normalizeStringSlice(out)
}

func normalizeDecisionTraceConfidence(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
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

func strPtr(v string) *string {
	value := strings.TrimSpace(v)
	if value == "" {
		return nil
	}
	return &value
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
	return runtimecontract.NormalizeStringList(in)
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

func parseToolCallResponse(toolCall *model.AIToolCallM) map[string]any {
	if toolCall == nil || toolCall.ResponseJSON == nil {
		return nil
	}
	return parseJSONObject(*toolCall.ResponseJSON)
}

func parseJSONObject(raw string) map[string]any {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	out := map[string]any{}
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return nil
	}
	return out
}

func anyToString(in any) string {
	switch value := in.(type) {
	case string:
		return value
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			return ""
		}
		return strings.Trim(string(raw), `"`)
	}
}

func isTerminalStatus(status string) bool {
	switch status {
	case jobStatusSucceeded, jobStatusFailed, jobStatusCanceled:
		return true
	default:
		return false
	}
}

//nolint:gocyclo // Keep explicit type branches for robust parsing.
func extractDiagnosisRootCauseConfidence(raw string) float64 {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0.7
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return 0.7
	}
	rootCause, _ := payload["root_cause"].(map[string]any)
	if rootCause == nil {
		return 0.7
	}

	normalize := func(v float64) float64 {
		if v <= 0 {
			return 0.7
		}
		if v > 1 {
			return 1
		}
		return v
	}

	switch confidence := rootCause["confidence"].(type) {
	case float64:
		return normalize(confidence)
	case float32:
		return normalize(float64(confidence))
	case int:
		return normalize(float64(confidence))
	case int64:
		return normalize(float64(confidence))
	case string:
		var parsed float64
		if err := json.Unmarshal([]byte(confidence), &parsed); err == nil {
			return normalize(parsed)
		}
	}
	return 0.7
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

type diagnosisPattern struct {
	Type   string  `json:"type"`
	Value  string  `json:"value"`
	Weight float64 `json:"weight"`
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
	Patterns        []diagnosisPattern    `json:"patterns,omitempty"`
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
