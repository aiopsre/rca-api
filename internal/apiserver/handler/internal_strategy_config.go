package handler

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	internalstrategyconfig "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/internal_strategy_config"
	authpkg "github.com/aiopsre/rca-api/internal/apiserver/pkg/auth"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/authz"
	"github.com/aiopsre/rca-api/internal/pkg/contextx"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
)

type upsertPipelineConfigRequest struct {
	AlertSource string  `json:"alert_source"`
	Service     string  `json:"service,omitempty"`
	Namespace   string  `json:"namespace,omitempty"`
	PipelineID  string  `json:"pipeline_id"`
	GraphID     *string `json:"graph_id,omitempty"`
}

type upsertTriggerConfigRequest struct {
	TriggerType string `json:"trigger_type"`
	PipelineID  string `json:"pipeline_id"`
	SessionType string `json:"session_type,omitempty"`
	Fallback    bool   `json:"fallback"`
}

type upsertToolsetConfigRequest struct {
	PipelineID   string   `json:"pipeline_id"`
	ToolsetName  string   `json:"toolset_name"`
	AllowedTools []string `json:"allowed_tools"`
}

type upsertSLAConfigRequest struct {
	SessionType          string  `json:"session_type"`
	DueSeconds           int64   `json:"due_seconds"`
	EscalationThresholds []int64 `json:"escalation_thresholds,omitempty"`
}

type assignSessionRequest struct {
	Assignee   string  `json:"assignee"`
	AssignedBy *string `json:"assigned_by,omitempty"`
	Note       *string `json:"note,omitempty"`
	AssignedAt *string `json:"assigned_at,omitempty"`
}

func (h *Handler) GetPipelineConfig(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	alertSource := strings.TrimSpace(c.Param("alert_source"))
	if alertSource == "" {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument)
		return
	}
	resp, err := h.biz.InternalStrategyConfigV1().GetPipeline(c.Request.Context(), &internalstrategyconfig.GetPipelineConfigRequest{
		AlertSource: alertSource,
		Service:     strings.TrimSpace(c.Query("service")),
		Namespace:   strings.TrimSpace(c.Query("namespace")),
	})
	core.WriteResponse(c, resp, err)
}

func (h *Handler) UpsertPipelineConfig(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	var req upsertPipelineConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	resp, err := h.biz.InternalStrategyConfigV1().UpsertPipeline(c.Request.Context(), &internalstrategyconfig.UpsertPipelineConfigRequest{
		AlertSource: req.AlertSource,
		Service:     req.Service,
		Namespace:   req.Namespace,
		PipelineID:  req.PipelineID,
		GraphID:     req.GraphID,
	})
	core.WriteResponse(c, resp, err)
}

func (h *Handler) GetTriggerConfig(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	triggerType := strings.TrimSpace(c.Param("trigger_type"))
	if triggerType == "" {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument)
		return
	}
	resp, err := h.biz.InternalStrategyConfigV1().GetTrigger(c.Request.Context(), &internalstrategyconfig.GetTriggerConfigRequest{TriggerType: triggerType})
	core.WriteResponse(c, resp, err)
}

func (h *Handler) UpsertTriggerConfig(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	var req upsertTriggerConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	resp, err := h.biz.InternalStrategyConfigV1().UpsertTrigger(c.Request.Context(), &internalstrategyconfig.UpsertTriggerConfigRequest{
		TriggerType: req.TriggerType,
		PipelineID:  req.PipelineID,
		SessionType: req.SessionType,
		Fallback:    req.Fallback,
	})
	core.WriteResponse(c, resp, err)
}

func (h *Handler) GetToolsetConfig(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	pipelineID := strings.TrimSpace(c.Param("pipeline_id"))
	if pipelineID == "" {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument)
		return
	}
	resp, err := h.biz.InternalStrategyConfigV1().GetToolsets(c.Request.Context(), &internalstrategyconfig.GetToolsetConfigRequest{PipelineID: pipelineID})
	core.WriteResponse(c, resp, err)
}

func (h *Handler) UpsertToolsetConfig(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	var req upsertToolsetConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	resp, err := h.biz.InternalStrategyConfigV1().UpsertToolset(c.Request.Context(), &internalstrategyconfig.UpsertToolsetConfigRequest{
		PipelineID:   req.PipelineID,
		ToolsetName:  req.ToolsetName,
		AllowedTools: req.AllowedTools,
	})
	core.WriteResponse(c, resp, err)
}

func (h *Handler) GetSLAConfig(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	sessionType := strings.TrimSpace(c.Param("session_type"))
	if sessionType == "" {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument)
		return
	}
	resp, err := h.biz.InternalStrategyConfigV1().GetSLA(c.Request.Context(), &internalstrategyconfig.GetSLAConfigRequest{SessionType: sessionType})
	core.WriteResponse(c, resp, err)
}

func (h *Handler) UpsertSLAConfig(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	var req upsertSLAConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	resp, err := h.biz.InternalStrategyConfigV1().UpsertSLA(c.Request.Context(), &internalstrategyconfig.UpsertSLAConfigRequest{
		SessionType:          req.SessionType,
		DueSeconds:           req.DueSeconds,
		EscalationThresholds: req.EscalationThresholds,
	})
	core.WriteResponse(c, resp, err)
}

func (h *Handler) GetSessionAssignmentConfig(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead, authz.ScopeAIRun, authz.ScopeSessionAssign, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	sessionID := strings.TrimSpace(c.Param("sessionID"))
	if sessionID == "" {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument)
		return
	}
	resp, err := h.biz.InternalStrategyConfigV1().GetSessionAssignment(c.Request.Context(), &internalstrategyconfig.GetSessionAssignmentRequest{SessionID: sessionID})
	core.WriteResponse(c, resp, err)
}

func (h *Handler) AssignSessionConfig(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRun, authz.ScopeSessionAssign, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	sessionID := strings.TrimSpace(c.Param("sessionID"))
	if sessionID == "" {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument)
		return
	}
	var req assignSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	assignedBy := strings.TrimSpace(normalizeOptionalText(req.AssignedBy))
	if assignedBy == "" {
		assignedBy = strings.TrimSpace(contextx.UserID(c.Request.Context()))
	}
	if assignedBy == "" {
		assignedBy = "operator:session_assign"
	}
	var assignedAt *time.Time
	if raw := strings.TrimSpace(normalizeOptionalText(req.AssignedAt)); raw != "" {
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			core.WriteResponse(c, nil, errno.ErrInvalidArgument)
			return
		}
		utc := parsed.UTC()
		assignedAt = &utc
	}
	resp, err := h.biz.InternalStrategyConfigV1().AssignSession(c.Request.Context(), &internalstrategyconfig.AssignSessionRequest{
		SessionID:  sessionID,
		Assignee:   strings.TrimSpace(req.Assignee),
		AssignedBy: assignedBy,
		Note:       req.Note,
		AssignedAt: assignedAt,
	})
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.WriteResponse(c, resp, nil)
}

//nolint:gochecknoinits // Route registration follows repository-wide init registrar pattern.
func init() {
	Register(func(v1 *gin.RouterGroup, handler *Handler, mws ...gin.HandlerFunc) {
		tokenMW := authpkg.RequireOperatorToken()
		configRBACMW := handler.RequireRBAC(authz.ScopeConfigAdmin)
		sessionAssignRBACMW := handler.RequireRBAC(authz.ScopeSessionAssign)
		configGroup := v1.Group("/config", append(mws, tokenMW)...)
		configGroup.GET("/pipeline/:alert_source", configRBACMW, handler.GetPipelineConfig)
		configGroup.POST("/pipeline/update", configRBACMW, handler.UpsertPipelineConfig)
		configGroup.GET("/trigger/:trigger_type", configRBACMW, handler.GetTriggerConfig)
		configGroup.POST("/trigger/update", configRBACMW, handler.UpsertTriggerConfig)
		configGroup.GET("/toolset/:pipeline_id", configRBACMW, handler.GetToolsetConfig)
		configGroup.POST("/toolset/update", configRBACMW, handler.UpsertToolsetConfig)
		configGroup.GET("/sla/:session_type", configRBACMW, handler.GetSLAConfig)
		configGroup.POST("/sla/update", configRBACMW, handler.UpsertSLAConfig)

		sessionGroup := v1.Group("/session", append(mws, tokenMW)...)
		sessionGroup.GET("/:sessionID/assignment", sessionAssignRBACMW, handler.GetSessionAssignmentConfig)
		sessionGroup.POST("/:sessionID/assign", sessionAssignRBACMW, handler.AssignSessionConfig)
	})
}
