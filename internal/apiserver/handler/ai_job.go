package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"
	"github.com/onexstack/onexstack/pkg/errorsx"

	aijobbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/ai_job"
	sessionbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/session"
	triggerbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/trigger"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/authz"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/metrics"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/queue"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/runtimecontract"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/validation"
	"github.com/aiopsre/rca-api/internal/pkg/contextx"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

const (
	orchestratorInstanceIDHeader = "X-Orchestrator-Instance-ID"
	sessionActionDefaultWindow   = 30 * time.Minute
	sessionActionStatusAccepted  = "accepted"
	sessionActionRefreshHint     = "refresh_session_workbench"

	sessionReplayActionSource   = "session_workbench_replay_api"
	sessionFollowUpActionSource = "session_workbench_follow_up_api"
)

type sessionActionRequest struct {
	Pipeline     *string `json:"pipeline,omitempty"`
	Reason       *string `json:"reason,omitempty"`
	OperatorNote *string `json:"operator_note,omitempty"`
	Source       *string `json:"source,omitempty"`
	Initiator    *string `json:"initiator,omitempty"`
}

type sessionActionResponse struct {
	SessionID            string `json:"session_id"`
	IncidentID           string `json:"incident_id"`
	JobID                string `json:"job_id"`
	TriggerType          string `json:"trigger_type"`
	Pipeline             string `json:"pipeline"`
	Created              bool   `json:"created"`
	Status               string `json:"status"`
	Message              string `json:"message,omitempty"`
	WorkbenchRefreshHint string `json:"workbench_refresh_hint,omitempty"`
}

type sessionReviewActionRequest struct {
	Note       *string `json:"note,omitempty"`
	ReviewedBy *string `json:"reviewed_by,omitempty"`
	ReasonCode *string `json:"reason_code,omitempty"`
}

type sessionAssignActionRequest struct {
	Assignee   *string `json:"assignee,omitempty"`
	AssignedBy *string `json:"assigned_by,omitempty"`
	Note       *string `json:"note,omitempty"`
}

type sessionReviewActionResponse struct {
	SessionID            string `json:"session_id"`
	ReviewState          string `json:"review_state"`
	ReviewNote           string `json:"review_note,omitempty"`
	ReviewedBy           string `json:"reviewed_by,omitempty"`
	ReviewedAt           string `json:"reviewed_at,omitempty"`
	ReasonCode           string `json:"reason_code,omitempty"`
	Status               string `json:"status"`
	Message              string `json:"message,omitempty"`
	WorkbenchRefreshHint string `json:"workbench_refresh_hint,omitempty"`
}

type sessionAssignActionResponse struct {
	SessionID            string `json:"session_id"`
	Assignee             string `json:"assignee"`
	AssignedBy           string `json:"assigned_by,omitempty"`
	AssignedAt           string `json:"assigned_at,omitempty"`
	AssignNote           string `json:"assign_note,omitempty"`
	Status               string `json:"status"`
	Message              string `json:"message,omitempty"`
	WorkbenchRefreshHint string `json:"workbench_refresh_hint,omitempty"`
}

func (h *Handler) RunIncidentAIJob(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRun); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	var req v1.RunAIJobRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	req.IncidentID = strings.TrimSpace(c.Param("incidentID"))
	if req.IdempotencyKey == nil {
		headerKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
		if headerKey != "" {
			req.IdempotencyKey = &headerKey
		}
	}
	if err := h.val.ValidateRunAIJobRequest(c.Request.Context(), &req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	triggerType, triggerSource := resolveManualTrigger(req.Trigger)

	triggerResp, err := h.biz.TriggerV1().Dispatch(c.Request.Context(), &triggerbiz.TriggerRequest{
		TriggerType: triggerType,
		Source:      triggerSource,
		BusinessKey: req.GetIncidentID(),
		Initiator:   req.CreatedBy,
		IncidentHint: &triggerbiz.IncidentHint{
			IncidentID: req.GetIncidentID(),
		},
		DesiredPipeline: req.Pipeline,
		TimeRange: &triggerbiz.TriggerTimeRange{
			Start: req.GetTimeRangeStart().AsTime(),
			End:   req.GetTimeRangeEnd().AsTime(),
		},
		RunRequest: &req,
	})
	resp := &v1.RunAIJobResponse{}
	if triggerResp != nil {
		resp.JobID = triggerResp.JobID
	}
	if err == nil {
		h.jobQueueNotifier.Notify()
		if h.jobQueueWakeup != nil {
			_ = h.jobQueueWakeup.PublishAIJobQueueSignal(c.Request.Context())
		}
	}
	core.WriteResponse(c, resp, err)
}

func resolveManualTrigger(trigger *string) (string, string) {
	triggerValue := ""
	if trigger != nil {
		triggerValue = strings.TrimSpace(*trigger)
	}
	switch strings.ToLower(triggerValue) {
	case triggerbiz.TriggerTypeReplay:
		return triggerbiz.TriggerTypeReplay, "manual_replay_api"
	case triggerbiz.TriggerTypeFollowUp:
		return triggerbiz.TriggerTypeFollowUp, "manual_follow_up_api"
	case triggerbiz.TriggerTypeCron:
		return triggerbiz.TriggerTypeCron, "manual_cron_api"
	case triggerbiz.TriggerTypeChange:
		return triggerbiz.TriggerTypeChange, "manual_change_api"
	default:
		return triggerbiz.TriggerTypeManual, "manual_api"
	}
}

func (h *Handler) ListIncidentAIJobs(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	req := &v1.ListIncidentAIJobsRequest{
		IncidentID: strings.TrimSpace(c.Param("incidentID")),
	}
	if offset := strings.TrimSpace(c.Query("offset")); offset != "" {
		if v, err := strconv.ParseInt(offset, 10, 64); err == nil {
			req.Offset = v
		}
	}
	if limit := strings.TrimSpace(c.Query("limit")); limit != "" {
		if v, err := strconv.ParseInt(limit, 10, 64); err == nil {
			req.Limit = v
		}
	}
	if err := h.val.ValidateListIncidentAIJobsRequest(c.Request.Context(), req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	resp, err := h.biz.AIJobV1().ListByIncident(c.Request.Context(), req)
	core.WriteResponse(c, resp, err)
}

func (h *Handler) GetAIJobTrace(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	req := &aijobbiz.GetTraceReadModelRequest{
		JobID: strings.TrimSpace(c.Param("jobID")),
	}
	resp, err := h.biz.AIJobV1().GetTraceReadModel(c.Request.Context(), req)
	core.WriteResponse(c, resp, err)
}

func (h *Handler) ListIncidentAIJobTraces(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	req := &aijobbiz.ListTraceReadModelsRequest{
		IncidentID: strPtr(strings.TrimSpace(c.Param("incidentID"))),
	}
	if offset := strings.TrimSpace(c.Query("offset")); offset != "" {
		if v, err := strconv.ParseInt(offset, 10, 64); err == nil {
			req.Offset = v
		}
	}
	if limit := strings.TrimSpace(c.Query("limit")); limit != "" {
		if v, err := strconv.ParseInt(limit, 10, 64); err == nil {
			req.Limit = v
		}
	}
	resp, err := h.biz.AIJobV1().ListTraceReadModels(c.Request.Context(), req)
	core.WriteResponse(c, resp, err)
}

func (h *Handler) ListSessionAIJobTraces(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	req := &aijobbiz.ListTraceReadModelsRequest{
		SessionID: strPtr(strings.TrimSpace(c.Param("sessionID"))),
	}
	if offset := strings.TrimSpace(c.Query("offset")); offset != "" {
		if v, err := strconv.ParseInt(offset, 10, 64); err == nil {
			req.Offset = v
		}
	}
	if limit := strings.TrimSpace(c.Query("limit")); limit != "" {
		if v, err := strconv.ParseInt(limit, 10, 64); err == nil {
			req.Limit = v
		}
	}
	resp, err := h.biz.AIJobV1().ListTraceReadModels(c.Request.Context(), req)
	core.WriteResponse(c, resp, err)
}

func (h *Handler) ListSessionHistory(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	req := &sessionbiz.ListSessionHistoryRequest{
		SessionID: strings.TrimSpace(c.Param("sessionID")),
	}
	if offset := strings.TrimSpace(c.Query("offset")); offset != "" {
		if v, err := strconv.ParseInt(offset, 10, 64); err == nil {
			req.Offset = v
		}
	}
	if limit := strings.TrimSpace(c.Query("limit")); limit != "" {
		if v, err := strconv.ParseInt(limit, 10, 64); err == nil {
			req.Limit = v
		}
	}
	if order := strings.TrimSpace(c.Query("order")); order != "" {
		req.Order = strPtr(order)
	}
	resp, err := h.biz.SessionV1().ListHistory(c.Request.Context(), req)
	core.WriteResponse(c, resp, err)
}

func (h *Handler) GetSessionAIWorkbench(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	req := &aijobbiz.GetSessionWorkbenchRequest{
		SessionID: strings.TrimSpace(c.Param("sessionID")),
	}
	if limit := strings.TrimSpace(c.Query("limit")); limit != "" {
		if v, err := strconv.ParseInt(limit, 10, 64); err == nil {
			req.RecentLimit = v
		}
	}
	resp, err := h.biz.AIJobV1().GetSessionWorkbench(c.Request.Context(), req)
	core.WriteResponse(c, resp, err)
}

func (h *Handler) ListOperatorInbox(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	req := &aijobbiz.ListOperatorInboxRequest{}
	if reviewState := strings.TrimSpace(c.Query("review_state")); reviewState != "" {
		req.ReviewState = strPtr(reviewState)
	}
	if sessionType := strings.TrimSpace(c.Query("session_type")); sessionType != "" {
		req.SessionType = strPtr(sessionType)
	}
	if assignee := strings.TrimSpace(c.Query("assignee")); assignee != "" {
		req.Assignee = strPtr(assignee)
	}
	if escalationState := strings.TrimSpace(c.Query("escalation_state")); escalationState != "" {
		req.EscalationState = strPtr(escalationState)
	}
	if needsReviewRaw := strings.TrimSpace(c.Query("needs_review")); needsReviewRaw != "" {
		needsReview, err := strconv.ParseBool(needsReviewRaw)
		if err != nil {
			core.WriteResponse(c, nil, errorsx.ErrInvalidArgument)
			return
		}
		req.NeedsReview = &needsReview
	}
	if offset := strings.TrimSpace(c.Query("offset")); offset != "" {
		if v, err := strconv.ParseInt(offset, 10, 64); err == nil {
			req.Offset = v
		}
	}
	if limit := strings.TrimSpace(c.Query("limit")); limit != "" {
		if v, err := strconv.ParseInt(limit, 10, 64); err == nil {
			req.Limit = v
		}
	}
	validateReq := &validation.SessionOperatorInboxRequest{
		ReviewState:     req.ReviewState,
		NeedsReview:     req.NeedsReview,
		SessionType:     req.SessionType,
		Assignee:        req.Assignee,
		EscalationState: req.EscalationState,
		Offset:          req.Offset,
		Limit:           req.Limit,
	}
	if err := h.val.ValidateSessionOperatorInboxRequest(c.Request.Context(), validateReq); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	req.ReviewState = validateReq.ReviewState
	req.SessionType = validateReq.SessionType
	req.Assignee = validateReq.Assignee
	req.EscalationState = validateReq.EscalationState
	req.Offset = validateReq.Offset
	req.Limit = validateReq.Limit

	resp, err := h.biz.AIJobV1().ListOperatorInbox(c.Request.Context(), req)
	core.WriteResponse(c, resp, err)
}

func (h *Handler) GetOperatorDashboard(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	resp, err := h.biz.AIJobV1().GetOperatorDashboard(c.Request.Context(), &aijobbiz.GetOperatorDashboardRequest{})
	core.WriteResponse(c, resp, err)
}

func (h *Handler) CompareAIJobTrace(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	leftJobID := firstNonEmptyTrimmedQuery(c, "left_job_id", "leftJobID")
	rightJobID := firstNonEmptyTrimmedQuery(c, "right_job_id", "rightJobID")
	req := &aijobbiz.CompareTraceReadModelsRequest{
		LeftJobID:  leftJobID,
		RightJobID: rightJobID,
	}
	resp, err := h.biz.AIJobV1().CompareTraceReadModels(c.Request.Context(), req)
	core.WriteResponse(c, resp, err)
}

func (h *Handler) ReplaySessionAI(c *gin.Context) {
	h.dispatchSessionAIAction(c, triggerbiz.TriggerTypeReplay, sessionReplayActionSource)
}

func (h *Handler) FollowUpSessionAI(c *gin.Context) {
	h.dispatchSessionAIAction(c, triggerbiz.TriggerTypeFollowUp, sessionFollowUpActionSource)
}

func (h *Handler) StartSessionReview(c *gin.Context) {
	h.dispatchSessionReviewAction(c, sessionbiz.SessionReviewStateInReview, "review_started")
}

func (h *Handler) ConfirmSessionReview(c *gin.Context) {
	h.dispatchSessionReviewAction(c, sessionbiz.SessionReviewStateConfirmed, "review_confirmed")
}

func (h *Handler) RejectSessionReview(c *gin.Context) {
	h.dispatchSessionReviewAction(c, sessionbiz.SessionReviewStateRejected, "review_rejected")
}

func (h *Handler) AssignSessionOwner(c *gin.Context) {
	h.dispatchSessionAssignAction(c, "assigned")
}

func (h *Handler) ReassignSessionOwner(c *gin.Context) {
	h.dispatchSessionAssignAction(c, "reassigned")
}

//nolint:dupl // Keep handler pattern aligned with other resources.
func (h *Handler) GetAIJob(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	req := &v1.GetAIJobRequest{
		JobID: strings.TrimSpace(c.Param("jobID")),
	}
	if err := h.val.ValidateGetAIJobRequest(c.Request.Context(), req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	resp, err := h.biz.AIJobV1().Get(c.Request.Context(), req)
	core.WriteResponse(c, resp, err)
}

//nolint:gocognit,gocyclo // Long-poll flow keeps explicit control branches for API semantics.
func (h *Handler) ListAIJobs(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	req := &v1.ListAIJobsRequest{
		Status: strings.TrimSpace(c.Query("status")),
	}
	if offset := strings.TrimSpace(c.Query("offset")); offset != "" {
		if v, err := strconv.ParseInt(offset, 10, 64); err == nil {
			req.Offset = v
		}
	}
	if limit := strings.TrimSpace(c.Query("limit")); limit != "" {
		if v, err := strconv.ParseInt(limit, 10, 64); err == nil {
			req.Limit = v
		}
	}

	startedAt := time.Now()
	outcome := "success"
	queueStatus := req.GetStatus()
	defer func() {
		if metrics.M != nil {
			metrics.M.RecordAIJobQueuePull(c.Request.Context(), queueStatus, outcome, time.Since(startedAt))
		}
	}()

	waitSeconds := int64(0)
	if wait := strings.TrimSpace(c.Query("wait_seconds")); wait != "" {
		v, err := strconv.ParseInt(wait, 10, 64)
		if err != nil {
			outcome = "error"
			core.WriteResponse(c, nil, errorsx.ErrInvalidArgument)
			return
		}
		waitSeconds = v
	}

	if err := h.val.ValidateListAIJobsRequest(c.Request.Context(), req); err != nil {
		outcome = "error"
		core.WriteResponse(c, nil, err)
		return
	}
	if err := h.val.ValidateAIJobQueueWaitSeconds(c.Request.Context(), waitSeconds); err != nil {
		outcome = "error"
		core.WriteResponse(c, nil, err)
		return
	}
	queueStatus = req.GetStatus()

	resp, err := h.biz.AIJobV1().List(c.Request.Context(), req)
	if err != nil {
		outcome = "error"
		core.WriteResponse(c, resp, err)
		return
	}
	if len(resp.GetJobs()) > 0 || waitSeconds <= 0 {
		core.WriteResponse(c, resp, err)
		return
	}

	waiter := h.adaptiveWaiter()
	if waiter == nil {
		outcome = "error"
		core.WriteResponse(c, nil, errorsx.ErrInvalidArgument)
		return
	}

	waitResult, waitErr := waiter.Wait(c.Request.Context(), time.Duration(waitSeconds)*time.Second)
	if waitErr != nil {
		outcome = "error"
		core.WriteResponse(c, nil, waitErr)
		return
	}
	if reason := strings.TrimSpace(waitResult.FallbackReason); reason != "" && metrics.M != nil {
		metrics.M.RecordAIJobLongPollFallback(reason)
	}

	resp, err = h.biz.AIJobV1().List(c.Request.Context(), req)
	if err != nil {
		outcome = "error"
		core.WriteResponse(c, nil, err)
		return
	}
	h.recordLongPollWake(c.Request.Context(), waitResult.WakeupSource, waitSeconds)
	core.WriteResponse(c, resp, nil)
}

func (h *Handler) adaptiveWaiter() *queue.AdaptiveWaiter {
	if h == nil || h.biz == nil {
		return nil
	}
	h.longPollOnce.Do(func() {
		opts := h.longPollOpts
		if !h.longPollOptsSet {
			opts = queue.ApplyAdaptiveWaiterEnvOverrides(queue.DefaultAdaptiveWaiterOptions())
		}
		h.longPollWaiter = queue.NewAdaptiveWaiter(
			h.jobQueueNotifier,
			h.jobQueueWakeup,
			h.biz.AIJobV1().QueueSignalVersion,
			opts,
		)
	})
	return h.longPollWaiter
}

func (h *Handler) recordLongPollWake(ctx context.Context, source string, waitSeconds int64) {
	source = strings.TrimSpace(source)
	if source == "" {
		source = queue.LongPollWakeupSourceTimeout
	}
	if metrics.M != nil {
		metrics.M.RecordAIJobLongPollWakeup(source)
	}
	slog.Info("ai job long poll wake", "wakeup_source", source, "wait_seconds", waitSeconds)
}

func (h *Handler) StartAIJob(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRun); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	contractReq := runtimecontract.NewClaimStartRequest(
		c.Param("jobID"),
		c.GetHeader(orchestratorInstanceIDHeader),
	)
	req := contractReq.ToAPIRequest()
	if err := h.val.ValidateStartAIJobRequest(c.Request.Context(), req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	if !requireOrchestratorInstanceIDValue(c, contractReq.OrchestratorInstanceID) {
		return
	}

	ctx := withOrchestratorInstanceIDValue(c.Request.Context(), contractReq.OrchestratorInstanceID)
	resp, err := h.biz.AIJobV1().Start(ctx, req)
	core.WriteResponse(c, resp, err)
}

func (h *Handler) RenewAIJobLease(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRun); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	contractReq := runtimecontract.NewRenewHeartbeatRequest(
		c.Param("jobID"),
		c.GetHeader(orchestratorInstanceIDHeader),
	)
	req := contractReq.ToAPIRequest()
	if err := h.val.ValidateStartAIJobRequest(c.Request.Context(), req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	if !requireOrchestratorInstanceIDValue(c, contractReq.OrchestratorInstanceID) {
		return
	}

	ctx := withOrchestratorInstanceIDValue(c.Request.Context(), contractReq.OrchestratorInstanceID)
	resp, err := h.biz.AIJobV1().Renew(ctx, req)
	core.WriteResponse(c, resp, err)
}

func (h *Handler) CancelAIJob(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAICancel, authz.ScopeAIRun); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	var req v1.CancelAIJobRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		core.WriteResponse(c, nil, err)
		return
	}
	req.JobID = strings.TrimSpace(c.Param("jobID"))
	if err := h.val.ValidateCancelAIJobRequest(c.Request.Context(), &req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	if !requireOrchestratorInstanceID(c) {
		return
	}

	ctx := withOrchestratorInstanceID(c)
	resp, err := h.biz.AIJobV1().Cancel(ctx, &req)
	core.WriteResponse(c, resp, err)
}

//nolint:dupl // Keep request bind/validate/dispatch pattern explicit and consistent.
func (h *Handler) FinalizeAIJob(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRun); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	var req v1.FinalizeAIJobRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	req.JobID = strings.TrimSpace(c.Param("jobID"))
	contractReq := runtimecontract.FinalizeRequestFromAPI(&req, c.GetHeader(orchestratorInstanceIDHeader))
	apiReq := contractReq.ToAPIRequest()
	if err := h.val.ValidateFinalizeAIJobRequest(c.Request.Context(), apiReq); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	if !requireOrchestratorInstanceIDValue(c, contractReq.OrchestratorInstanceID) {
		return
	}

	ctx := withOrchestratorInstanceIDValue(c.Request.Context(), contractReq.OrchestratorInstanceID)
	resp, err := h.biz.AIJobV1().Finalize(ctx, apiReq)
	core.WriteResponse(c, resp, err)
}

//nolint:dupl // Keep request bind/validate/dispatch pattern explicit and consistent.
func (h *Handler) CreateAIToolCall(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRun); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	var req v1.CreateAIToolCallRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	req.JobID = strings.TrimSpace(c.Param("jobID"))
	contractReq := runtimecontract.ToolCallReportRequestFromAPI(&req, c.GetHeader(orchestratorInstanceIDHeader))
	apiReq := contractReq.ToAPIRequest()
	if err := h.val.ValidateCreateAIToolCallRequest(c.Request.Context(), apiReq); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	if !requireOrchestratorInstanceIDValue(c, contractReq.OrchestratorInstanceID) {
		return
	}

	ctx := withOrchestratorInstanceIDValue(c.Request.Context(), contractReq.OrchestratorInstanceID)
	resp, err := h.biz.AIJobV1().CreateToolCall(ctx, apiReq)
	core.WriteResponse(c, resp, err)
}

//nolint:gocognit // Query parsing intentionally keeps explicit field handling for guardrails.
func (h *Handler) ListAIToolCalls(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	req := &v1.ListAIToolCallsRequest{
		JobID: strings.TrimSpace(c.Param("jobID")),
	}
	if offset := strings.TrimSpace(c.Query("offset")); offset != "" {
		if v, err := strconv.ParseInt(offset, 10, 64); err == nil {
			req.Offset = v
		}
	}
	if limit := strings.TrimSpace(c.Query("limit")); limit != "" {
		if v, err := strconv.ParseInt(limit, 10, 64); err == nil {
			req.Limit = v
		}
	}
	if seq := strings.TrimSpace(c.Query("seq")); seq != "" {
		if v, err := strconv.ParseInt(seq, 10, 64); err == nil {
			req.Seq = &v
		}
	}
	if err := h.val.ValidateListAIToolCallsRequest(c.Request.Context(), req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	resp, err := h.biz.AIJobV1().ListToolCalls(c.Request.Context(), req)
	core.WriteResponse(c, resp, err)
}

//nolint:gochecknoinits // Route registration follows repository-wide init registrar pattern.
func init() {
	Register(func(v1 *gin.RouterGroup, handler *Handler, mws ...gin.HandlerFunc) {
		incidentGroup := v1.Group("/incidents", mws...)
		incidentGroup.POST("/:incidentID/ai:run", handler.RunIncidentAIJob)
		incidentGroup.GET("/:incidentID/ai", handler.ListIncidentAIJobs)
		incidentGroup.GET("/:incidentID/ai/traces", handler.ListIncidentAIJobTraces)

		jobGroup := v1.Group("/ai/jobs", mws...)
		jobGroup.GET("", handler.ListAIJobs)
		jobGroup.GET("/:jobID", handler.GetAIJob)
		jobGroup.GET("/:jobID/trace", handler.GetAIJobTrace)
		jobGroup.POST("/:jobID/start", handler.StartAIJob)
		jobGroup.POST("/:jobID/heartbeat", handler.RenewAIJobLease)
		jobGroup.POST("/:jobID/cancel", handler.CancelAIJob)
		jobGroup.POST("/:jobID/finalize", handler.FinalizeAIJob)
		jobGroup.POST("/:jobID/tool-calls", handler.CreateAIToolCall)
		jobGroup.GET("/:jobID/tool-calls", handler.ListAIToolCalls)

		sessionGroup := v1.Group("/sessions", mws...)
		sessionGroup.GET("/:sessionID/ai/traces", handler.ListSessionAIJobTraces)
		sessionGroup.GET("/:sessionID/history", handler.ListSessionHistory)
		sessionGroup.GET("/:sessionID/workbench", handler.GetSessionAIWorkbench)
		sessionGroup.POST("/:sessionID/actions/replay", handler.ReplaySessionAI)
		sessionGroup.POST("/:sessionID/actions/follow-up", handler.FollowUpSessionAI)
		sessionGroup.POST("/:sessionID/actions/review-start", handler.StartSessionReview)
		sessionGroup.POST("/:sessionID/actions/review-confirm", handler.ConfirmSessionReview)
		sessionGroup.POST("/:sessionID/actions/review-reject", handler.RejectSessionReview)
		sessionGroup.POST("/:sessionID/actions/assign", handler.AssignSessionOwner)
		sessionGroup.POST("/:sessionID/actions/reassign", handler.ReassignSessionOwner)

		v1.GET("/operator/inbox", handler.ListOperatorInbox)
		v1.GET("/operator/dashboard", handler.GetOperatorDashboard)
		v1.GET("/ai/jobs:trace-compare", handler.CompareAIJobTrace)
	})
}

func withOrchestratorInstanceID(c *gin.Context) context.Context {
	ctx := c.Request.Context()
	instanceID := strings.TrimSpace(c.GetHeader(orchestratorInstanceIDHeader))
	return withOrchestratorInstanceIDValue(ctx, instanceID)
}

func withOrchestratorInstanceIDValue(ctx context.Context, instanceID string) context.Context {
	trimmed := strings.TrimSpace(instanceID)
	if trimmed == "" {
		return ctx
	}
	return contextx.WithOrchestratorInstanceID(ctx, trimmed)
}

func requireOrchestratorInstanceID(c *gin.Context) bool {
	return requireOrchestratorInstanceIDValue(c, c.GetHeader(orchestratorInstanceIDHeader))
}

func requireOrchestratorInstanceIDValue(c *gin.Context, instanceID string) bool {
	if strings.TrimSpace(instanceID) != "" {
		return true
	}
	core.WriteResponse(c, nil, errorsx.ErrInvalidArgument)
	return false
}

func firstNonEmptyTrimmedQuery(c *gin.Context, keys ...string) string {
	for _, key := range keys {
		value := strings.TrimSpace(c.Query(key))
		if value != "" {
			return value
		}
	}
	return ""
}

func (h *Handler) dispatchSessionAIAction(c *gin.Context, triggerType string, defaultSource string) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRun); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	sessionID := strings.TrimSpace(c.Param("sessionID"))
	var req sessionActionRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		core.WriteResponse(c, nil, err)
		return
	}

	validateReq := &validation.SessionOperatorActionRequest{
		SessionID:    sessionID,
		TriggerType:  triggerType,
		Pipeline:     req.Pipeline,
		Reason:       req.Reason,
		OperatorNote: req.OperatorNote,
		Source:       req.Source,
		Initiator:    req.Initiator,
	}
	if err := h.val.ValidateSessionOperatorActionRequest(c.Request.Context(), validateReq); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	source := normalizeOptionalText(req.Source)
	if source == "" {
		source = defaultSource
	}
	initiator := resolveSessionActionInitiator(c.Request.Context(), req.Initiator)
	pipeline := normalizeOptionalText(req.Pipeline)
	reason := normalizeOptionalText(req.Reason)
	operatorNote := normalizeOptionalText(req.OperatorNote)

	runReq := &v1.RunAIJobRequest{
		CreatedBy: strPtr(initiator),
	}
	if pipeline != "" {
		runReq.Pipeline = strPtr(pipeline)
	}
	if inputHints := buildSessionActionInputHints(triggerType, reason, operatorNote, source); inputHints != "" {
		runReq.InputHintsJSON = strPtr(inputHints)
	}

	triggerReq := &triggerbiz.TriggerRequest{
		TriggerType: triggerType,
		Source:      source,
		BusinessKey: sessionID,
		SessionHint: &triggerbiz.SessionHint{
			SessionID: sessionID,
		},
		Payload:   buildSessionActionPayload(reason, operatorNote),
		Initiator: strPtr(initiator),
		TimeRange: &triggerbiz.TriggerTimeRange{
			Start: time.Now().UTC().Add(-sessionActionDefaultWindow),
			End:   time.Now().UTC(),
		},
		DesiredPipeline: strPtr(pipeline),
		RunRequest:      runReq,
	}

	triggerResp, err := h.biz.TriggerV1().Dispatch(c.Request.Context(), triggerReq)
	resp := &sessionActionResponse{
		TriggerType:          triggerType,
		Status:               sessionActionStatusAccepted,
		WorkbenchRefreshHint: sessionActionRefreshHint,
	}
	if triggerResp != nil {
		resp.SessionID = strings.TrimSpace(triggerResp.SessionID)
		resp.IncidentID = strings.TrimSpace(triggerResp.IncidentID)
		resp.JobID = strings.TrimSpace(triggerResp.JobID)
		resp.Pipeline = strings.TrimSpace(triggerResp.Pipeline)
		resp.Created = triggerResp.Created
		resp.Message = strings.TrimSpace(triggerResp.Message)
	}

	if err == nil {
		historySessionID := strings.TrimSpace(resp.SessionID)
		if historySessionID == "" {
			historySessionID = sessionID
		}
		historyEventType := ""
		switch strings.ToLower(strings.TrimSpace(triggerType)) {
		case triggerbiz.TriggerTypeReplay:
			historyEventType = sessionbiz.SessionHistoryEventReplayRequested
		case triggerbiz.TriggerTypeFollowUp:
			historyEventType = sessionbiz.SessionHistoryEventFollowUpRequested
		}
		if historyEventType != "" && historySessionID != "" {
			historyNote := operatorNote
			if historyNote == "" {
				historyNote = reason
			}
			payload := map[string]any{
				"trigger_type": triggerType,
				"source":       source,
			}
			pipelineValue := strings.TrimSpace(resp.Pipeline)
			if pipelineValue == "" {
				pipelineValue = strings.TrimSpace(pipeline)
			}
			if pipelineValue != "" {
				payload["pipeline"] = pipelineValue
			}
			if reason != "" {
				payload["reason"] = reason
			}
			if operatorNote != "" {
				payload["operator_note"] = operatorNote
			}
			if resp.JobID != "" {
				payload["job_id"] = resp.JobID
			}
			if _, historyErr := h.biz.SessionV1().AppendHistoryEvent(c.Request.Context(), &sessionbiz.AppendSessionHistoryEventRequest{
				SessionID:      historySessionID,
				EventType:      historyEventType,
				IncidentID:     strPtr(resp.IncidentID),
				JobID:          strPtr(resp.JobID),
				Actor:          strPtr(initiator),
				Note:           strPtr(historyNote),
				PayloadSummary: payload,
			}); historyErr != nil {
				slog.WarnContext(c.Request.Context(), "append session replay/follow_up history skipped",
					"session_id", historySessionID,
					"event_type", historyEventType,
					"error", historyErr,
				)
			}
		}
		h.jobQueueNotifier.Notify()
		if h.jobQueueWakeup != nil {
			_ = h.jobQueueWakeup.PublishAIJobQueueSignal(c.Request.Context())
		}
	}
	core.WriteResponse(c, resp, err)
}

func normalizeOptionalText(raw *string) string {
	if raw == nil {
		return ""
	}
	return strings.TrimSpace(*raw)
}

func resolveSessionActionInitiator(ctx context.Context, reqInitiator *string) string {
	if value := normalizeOptionalText(reqInitiator); value != "" {
		return value
	}
	if user := strings.TrimSpace(contextx.Username(ctx)); user != "" {
		return "user:" + user
	}
	if userID := strings.TrimSpace(contextx.UserID(ctx)); userID != "" {
		return "user:" + userID
	}
	return "operator:session_action"
}

func buildSessionActionPayload(reason string, operatorNote string) map[string]any {
	payload := map[string]any{
		"action_origin": "session_workbench",
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		payload["reason"] = reason
	}
	if operatorNote = strings.TrimSpace(operatorNote); operatorNote != "" {
		payload["operator_note"] = operatorNote
	}
	return payload
}

func buildSessionActionInputHints(triggerType string, reason string, operatorNote string, source string) string {
	payload := map[string]any{
		"operator_action": strings.TrimSpace(triggerType),
		"source":          strings.TrimSpace(source),
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		payload["reason"] = reason
	}
	if operatorNote = strings.TrimSpace(operatorNote); operatorNote != "" {
		payload["operator_note"] = operatorNote
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(raw)
}

func (h *Handler) dispatchSessionReviewAction(c *gin.Context, reviewState string, message string) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRun); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	sessionID := strings.TrimSpace(c.Param("sessionID"))
	var req sessionReviewActionRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		core.WriteResponse(c, nil, err)
		return
	}
	validateReq := &validation.SessionReviewActionRequest{
		SessionID:   sessionID,
		ReviewState: reviewState,
		Note:        req.Note,
		ReviewedBy:  req.ReviewedBy,
		ReasonCode:  req.ReasonCode,
	}
	if err := h.val.ValidateSessionReviewActionRequest(c.Request.Context(), validateReq); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	reviewedBy := resolveSessionActionInitiator(c.Request.Context(), req.ReviewedBy)
	updateResp, err := h.biz.SessionV1().UpdateReviewState(c.Request.Context(), &sessionbiz.UpdateReviewStateRequest{
		SessionID:   sessionID,
		ReviewState: reviewState,
		ReviewNote:  req.Note,
		ReviewedBy:  strPtr(reviewedBy),
		ReasonCode:  req.ReasonCode,
	})
	resp := &sessionReviewActionResponse{
		SessionID:            sessionID,
		ReviewState:          reviewState,
		ReviewedBy:           reviewedBy,
		ReviewNote:           normalizeOptionalText(req.Note),
		ReasonCode:           normalizeOptionalText(req.ReasonCode),
		Status:               sessionActionStatusAccepted,
		Message:              strings.TrimSpace(message),
		WorkbenchRefreshHint: sessionActionRefreshHint,
	}
	if updateResp != nil && updateResp.Review != nil {
		resp.ReviewState = strings.TrimSpace(updateResp.Review.State)
		resp.ReviewNote = strings.TrimSpace(updateResp.Review.Note)
		resp.ReviewedBy = strings.TrimSpace(updateResp.Review.ReviewedBy)
		resp.ReviewedAt = strings.TrimSpace(updateResp.Review.ReviewedAt)
		resp.ReasonCode = strings.TrimSpace(updateResp.Review.ReasonCode)
	}
	core.WriteResponse(c, resp, err)
}

func (h *Handler) dispatchSessionAssignAction(c *gin.Context, message string) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRun); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	sessionID := strings.TrimSpace(c.Param("sessionID"))
	var req sessionAssignActionRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		core.WriteResponse(c, nil, err)
		return
	}
	validateReq := &validation.SessionAssignmentActionRequest{
		SessionID:  sessionID,
		Assignee:   req.Assignee,
		AssignedBy: req.AssignedBy,
		Note:       req.Note,
	}
	if err := h.val.ValidateSessionAssignmentActionRequest(c.Request.Context(), validateReq); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	assignedBy := resolveSessionActionInitiator(c.Request.Context(), req.AssignedBy)
	updateResp, err := h.biz.SessionV1().UpdateAssignment(c.Request.Context(), &sessionbiz.UpdateAssignmentRequest{
		SessionID:  sessionID,
		Assignee:   strings.TrimSpace(*validateReq.Assignee),
		AssignedBy: strPtr(assignedBy),
		AssignNote: req.Note,
	})
	resp := &sessionAssignActionResponse{
		SessionID:            sessionID,
		Assignee:             strings.TrimSpace(*validateReq.Assignee),
		AssignedBy:           assignedBy,
		AssignNote:           normalizeOptionalText(req.Note),
		Status:               sessionActionStatusAccepted,
		Message:              strings.TrimSpace(message),
		WorkbenchRefreshHint: sessionActionRefreshHint,
	}
	if updateResp != nil && updateResp.Assignment != nil {
		resp.Assignee = strings.TrimSpace(updateResp.Assignment.Assignee)
		resp.AssignedBy = strings.TrimSpace(updateResp.Assignment.AssignedBy)
		resp.AssignedAt = strings.TrimSpace(updateResp.Assignment.AssignedAt)
		resp.AssignNote = strings.TrimSpace(updateResp.Assignment.Note)
	}
	core.WriteResponse(c, resp, err)
}
