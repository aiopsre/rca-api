package handler

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"
	"github.com/onexstack/onexstack/pkg/errorsx"

	"github.com/aiopsre/rca-api/internal/apiserver/pkg/authz"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/metrics"
	"github.com/aiopsre/rca-api/internal/pkg/contextx"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

const (
	orchestratorInstanceIDHeader = "X-Orchestrator-Instance-ID"
	longPollDBCheckInterval      = 500 * time.Millisecond
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

	resp, err := h.biz.AIJobV1().Run(c.Request.Context(), &req)
	if err == nil {
		h.jobQueueNotifier.Notify()
	}
	core.WriteResponse(c, resp, err)
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

	baselineVersion, err := h.biz.AIJobV1().QueueSignalVersion(c.Request.Context())
	if err != nil {
		outcome = "error"
		core.WriteResponse(c, nil, err)
		return
	}

	// Close list->baseline race: if queue changed before baseline read, this second list catches it.
	resp, err = h.biz.AIJobV1().List(c.Request.Context(), req)
	if err != nil {
		outcome = "error"
		core.WriteResponse(c, nil, err)
		return
	}
	if len(resp.GetJobs()) > 0 {
		core.WriteResponse(c, resp, nil)
		return
	}

	deadline := time.Now().Add(time.Duration(waitSeconds) * time.Second)
	notifierVersion := h.jobQueueNotifier.Version()
	for c.Request.Context().Err() == nil {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		waitDur := longPollDBCheckInterval
		waitDur = min(waitDur, remaining)
		h.jobQueueNotifier.Wait(c.Request.Context(), notifierVersion, waitDur)
		notifierVersion = h.jobQueueNotifier.Version()

		currentVersion, verErr := h.biz.AIJobV1().QueueSignalVersion(c.Request.Context())
		if verErr != nil {
			outcome = "error"
			core.WriteResponse(c, nil, verErr)
			return
		}
		if currentVersion != baselineVersion {
			resp, err = h.biz.AIJobV1().List(c.Request.Context(), req)
			if err != nil {
				outcome = "error"
			}
			core.WriteResponse(c, resp, err)
			return
		}
	}

	core.WriteResponse(c, &v1.ListAIJobsResponse{
		TotalCount: 0,
		Jobs:       []*v1.AIJob{},
	}, nil)
}

func (h *Handler) StartAIJob(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRun); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	req := &v1.StartAIJobRequest{
		JobID: strings.TrimSpace(c.Param("jobID")),
	}
	if err := h.val.ValidateStartAIJobRequest(c.Request.Context(), req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	ctx := withOrchestratorInstanceID(c)
	resp, err := h.biz.AIJobV1().Start(ctx, req)
	core.WriteResponse(c, resp, err)
}

func (h *Handler) RenewAIJobLease(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRun); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	req := &v1.StartAIJobRequest{
		JobID: strings.TrimSpace(c.Param("jobID")),
	}
	if err := h.val.ValidateStartAIJobRequest(c.Request.Context(), req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	if strings.TrimSpace(c.GetHeader(orchestratorInstanceIDHeader)) == "" {
		core.WriteResponse(c, nil, errorsx.ErrInvalidArgument)
		return
	}

	ctx := withOrchestratorInstanceID(c)
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

	resp, err := h.biz.AIJobV1().Cancel(c.Request.Context(), &req)
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
	if err := h.val.ValidateFinalizeAIJobRequest(c.Request.Context(), &req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	ctx := withOrchestratorInstanceID(c)
	resp, err := h.biz.AIJobV1().Finalize(ctx, &req)
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
	if err := h.val.ValidateCreateAIToolCallRequest(c.Request.Context(), &req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	ctx := withOrchestratorInstanceID(c)
	resp, err := h.biz.AIJobV1().CreateToolCall(ctx, &req)
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

		jobGroup := v1.Group("/ai/jobs", mws...)
		jobGroup.GET("", handler.ListAIJobs)
		jobGroup.GET("/:jobID", handler.GetAIJob)
		jobGroup.POST("/:jobID/start", handler.StartAIJob)
		jobGroup.POST("/:jobID/heartbeat", handler.RenewAIJobLease)
		jobGroup.POST("/:jobID/cancel", handler.CancelAIJob)
		jobGroup.POST("/:jobID/finalize", handler.FinalizeAIJob)
		jobGroup.POST("/:jobID/tool-calls", handler.CreateAIToolCall)
		jobGroup.GET("/:jobID/tool-calls", handler.ListAIToolCalls)
	})
}

func withOrchestratorInstanceID(c *gin.Context) context.Context {
	ctx := c.Request.Context()
	instanceID := strings.TrimSpace(c.GetHeader(orchestratorInstanceIDHeader))
	if instanceID == "" {
		return ctx
	}
	return contextx.WithOrchestratorInstanceID(ctx, instanceID)
}
