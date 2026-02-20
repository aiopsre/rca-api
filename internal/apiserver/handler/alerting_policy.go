package handler

import (
	"io"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	alertingpolicybiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/alerting_policy"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/authz"
	"github.com/aiopsre/rca-api/internal/pkg/contextx"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
)

type createAlertingPolicyRequest struct {
	Name        string                                  `json:"name"`
	Description *string                                 `json:"description,omitempty"`
	Config      *alertingpolicybiz.AlertingPolicyConfig `json:"config"`
}

type updateAlertingPolicyRequest struct {
	Name            *string                                 `json:"name,omitempty"`
	Description     *string                                 `json:"description,omitempty"`
	Config          *alertingpolicybiz.AlertingPolicyConfig `json:"config,omitempty"`
	ExpectedVersion *int                                    `json:"expected_version,omitempty"`
}

type activateAlertingPolicyRequest struct {
	Operator *string `json:"operator,omitempty"`
}

type rollbackAlertingPolicyRequest struct {
	Version  int     `json:"version"`
	Operator *string `json:"operator,omitempty"`
}

func (h *Handler) CreateAlertingPolicy(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	var req createAlertingPolicyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	if strings.TrimSpace(req.Name) == "" {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument)
		return
	}

	if req.Config == nil {
		core.WriteResponse(c, nil, errno.ErrAlertingPolicyInvalidConfig)
		return
	}

	ctx := c.Request.Context()
	resp, err := h.biz.AlertingPolicyV1().Create(ctx, &alertingpolicybiz.CreateRequest{
		Name:        strings.TrimSpace(req.Name),
		Description: req.Description,
		Config:      req.Config,
		CreatedBy:   contextx.Username(ctx),
	})
	core.WriteResponse(c, resp, err)
}

func (h *Handler) GetAlertingPolicy(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	idStr := strings.TrimSpace(c.Param("id"))
	if idStr == "" {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument)
		return
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument)
		return
	}

	resp, err := h.biz.AlertingPolicyV1().Get(c.Request.Context(), id)
	core.WriteResponse(c, resp, err)
}

func (h *Handler) ListAlertingPolicies(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	var req alertingpolicybiz.ListRequest

	if name := strings.TrimSpace(c.Query("name")); name != "" {
		req.Name = &name
	}

	if activeStr := strings.TrimSpace(c.Query("active")); activeStr != "" {
		active, err := strconv.ParseBool(activeStr)
		if err != nil {
			core.WriteResponse(c, nil, errno.ErrInvalidArgument)
			return
		}
		req.Active = &active
	}

	if offsetStr := strings.TrimSpace(c.Query("offset")); offsetStr != "" {
		offset, err := strconv.ParseInt(offsetStr, 10, 64)
		if err != nil || offset < 0 {
			core.WriteResponse(c, nil, errno.ErrInvalidArgument)
			return
		}
		req.Offset = offset
	}

	if limitStr := strings.TrimSpace(c.Query("limit")); limitStr != "" {
		limit, err := strconv.ParseInt(limitStr, 10, 64)
		if err != nil || limit <= 0 {
			core.WriteResponse(c, nil, errno.ErrInvalidArgument)
			return
		}
		req.Limit = &limit
	}

	resp, err := h.biz.AlertingPolicyV1().List(c.Request.Context(), &req)
	core.WriteResponse(c, resp, err)
}

func (h *Handler) UpdateAlertingPolicy(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	idStr := strings.TrimSpace(c.Param("id"))
	if idStr == "" {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument)
		return
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument)
		return
	}

	var req updateAlertingPolicyRequest
	if err := c.ShouldBindJSON(&req); err != nil && err != io.EOF {
		core.WriteResponse(c, nil, err)
		return
	}

	ctx := c.Request.Context()
	resp, err := h.biz.AlertingPolicyV1().Update(ctx, id, &alertingpolicybiz.UpdateRequest{
		Name:            req.Name,
		Description:     req.Description,
		Config:          req.Config,
		ExpectedVersion: req.ExpectedVersion,
		UpdatedBy:       contextx.Username(ctx),
	})
	core.WriteResponse(c, resp, err)
}

func (h *Handler) DeleteAlertingPolicy(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	idStr := strings.TrimSpace(c.Param("id"))
	if idStr == "" {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument)
		return
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument)
		return
	}

	err = h.biz.AlertingPolicyV1().Delete(c.Request.Context(), id)
	core.WriteResponse(c, gin.H{"deleted": true}, err)
}

func (h *Handler) ActivateAlertingPolicy(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	idStr := strings.TrimSpace(c.Param("id"))
	if idStr == "" {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument)
		return
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument)
		return
	}

	var req activateAlertingPolicyRequest
	if c.Request.Body != nil && c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil && err != io.EOF {
			core.WriteResponse(c, nil, err)
			return
		}
	}

	ctx := c.Request.Context()
	operator := contextx.Username(ctx)
	if req.Operator != nil && strings.TrimSpace(*req.Operator) != "" {
		operator = strings.TrimSpace(*req.Operator)
	}

	err = h.biz.AlertingPolicyV1().Activate(ctx, id, operator)
	core.WriteResponse(c, gin.H{"activated": true}, err)
}

func (h *Handler) DeactivateAlertingPolicy(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	idStr := strings.TrimSpace(c.Param("id"))
	if idStr == "" {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument)
		return
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument)
		return
	}

	err = h.biz.AlertingPolicyV1().Deactivate(c.Request.Context(), id)
	core.WriteResponse(c, gin.H{"deactivated": true}, err)
}

func (h *Handler) RollbackAlertingPolicy(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	idStr := strings.TrimSpace(c.Param("id"))
	if idStr == "" {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument)
		return
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument)
		return
	}

	var req rollbackAlertingPolicyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	if req.Version <= 0 {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument)
		return
	}

	ctx := c.Request.Context()
	operator := contextx.Username(ctx)
	if req.Operator != nil && strings.TrimSpace(*req.Operator) != "" {
		operator = strings.TrimSpace(*req.Operator)
	}

	err = h.biz.AlertingPolicyV1().Rollback(ctx, id, req.Version, operator)
	core.WriteResponse(c, gin.H{"rollbacked": true}, err)
}

func (h *Handler) GetActiveAlertingPolicy(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	resp, err := h.biz.AlertingPolicyV1().GetActive(c.Request.Context())
	core.WriteResponse(c, resp, err)
}

//nolint:gochecknoinits // Route registration follows repository-wide init registrar pattern.
func init() {
	Register(func(v1 *gin.RouterGroup, handler *Handler, mws ...gin.HandlerFunc) {
		alertingPolicies := v1.Group("/alerting-policies", mws...)
		alertingPolicies.POST("", handler.CreateAlertingPolicy)
		alertingPolicies.GET("", handler.ListAlertingPolicies)
		alertingPolicies.GET("/active", handler.GetActiveAlertingPolicy)
		alertingPolicies.GET("/:id", handler.GetAlertingPolicy)
		alertingPolicies.PUT("/:id", handler.UpdateAlertingPolicy)
		alertingPolicies.DELETE("/:id", handler.DeleteAlertingPolicy)
		alertingPolicies.POST("/:id/activate", handler.ActivateAlertingPolicy)
		alertingPolicies.POST("/:id/deactivate", handler.DeactivateAlertingPolicy)
		alertingPolicies.POST("/:id/rollback", handler.RollbackAlertingPolicy)
	})
}
