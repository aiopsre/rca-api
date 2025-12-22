package handler

import (
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"
	"github.com/onexstack/onexstack/pkg/errorsx"

	"zk8s.com/rca-api/internal/apiserver/pkg/authz"
	"zk8s.com/rca-api/internal/apiserver/pkg/metrics"
	v1 "zk8s.com/rca-api/pkg/api/apiserver/v1"
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

	version := h.jobQueueNotifier.Version()
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

	h.jobQueueNotifier.Wait(c.Request.Context(), version, time.Duration(waitSeconds)*time.Second)
	if c.Request.Context().Err() != nil {
		core.WriteResponse(c, &v1.ListAIJobsResponse{
			TotalCount: 0,
			Jobs:       []*v1.AIJob{},
		}, nil)
		return
	}

	resp, err = h.biz.AIJobV1().List(c.Request.Context(), req)
	if err != nil {
		outcome = "error"
	}
	core.WriteResponse(c, resp, err)
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

	resp, err := h.biz.AIJobV1().Start(c.Request.Context(), req)
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

	resp, err := h.biz.AIJobV1().Finalize(c.Request.Context(), &req)
	core.WriteResponse(c, resp, err)
}

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

	resp, err := h.biz.AIJobV1().CreateToolCall(c.Request.Context(), &req)
	core.WriteResponse(c, resp, err)
}

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

func init() {
	Register(func(v1 *gin.RouterGroup, handler *Handler, mws ...gin.HandlerFunc) {
		incidentGroup := v1.Group("/incidents", mws...)
		incidentGroup.POST("/:incidentID/ai:run", handler.RunIncidentAIJob)
		incidentGroup.GET("/:incidentID/ai", handler.ListIncidentAIJobs)

		jobGroup := v1.Group("/ai/jobs", mws...)
		jobGroup.GET("", handler.ListAIJobs)
		jobGroup.GET("/:jobID", handler.GetAIJob)
		jobGroup.POST("/:jobID/start", handler.StartAIJob)
		jobGroup.POST("/:jobID/cancel", handler.CancelAIJob)
		jobGroup.POST("/:jobID/finalize", handler.FinalizeAIJob)
		jobGroup.POST("/:jobID/tool-calls", handler.CreateAIToolCall)
		jobGroup.GET("/:jobID/tool-calls", handler.ListAIToolCalls)
	})
}
