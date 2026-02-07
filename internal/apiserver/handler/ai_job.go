package handler

import (
	"context"
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
	triggerbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/trigger"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/authz"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/metrics"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/queue"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/runtimecontract"
	"github.com/aiopsre/rca-api/internal/pkg/contextx"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

const (
	orchestratorInstanceIDHeader = "X-Orchestrator-Instance-ID"
)

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
