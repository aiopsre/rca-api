package handler

import (
	"io"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	playbookbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/playbook"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/authz"
	"github.com/aiopsre/rca-api/internal/pkg/contextx"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
)

type createPlaybookRequest struct {
	Name        string                      `json:"name"`
	Description *string                     `json:"description,omitempty"`
	Config      *playbookbiz.PlaybookConfig `json:"config"`
}

type updatePlaybookRequest struct {
	Name            *string                     `json:"name,omitempty"`
	Description     *string                     `json:"description,omitempty"`
	Config          *playbookbiz.PlaybookConfig `json:"config,omitempty"`
	ExpectedVersion *int                        `json:"expected_version,omitempty"`
}

type activatePlaybookRequest struct {
	Operator *string `json:"operator,omitempty"`
}

type rollbackPlaybookRequest struct {
	Version  int     `json:"version"`
	Operator *string `json:"operator,omitempty"`
}

func (h *Handler) CreatePlaybook(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	var req createPlaybookRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	if strings.TrimSpace(req.Name) == "" {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument)
		return
	}

	if req.Config == nil {
		core.WriteResponse(c, nil, errno.ErrPlaybookInvalidConfig)
		return
	}

	ctx := c.Request.Context()
	resp, err := h.biz.PlaybookV1().Create(ctx, &playbookbiz.CreateRequest{
		Name:        strings.TrimSpace(req.Name),
		Description: req.Description,
		Config:      req.Config,
		CreatedBy:   contextx.Username(ctx),
	})
	core.WriteResponse(c, resp, err)
}

func (h *Handler) GetPlaybook(c *gin.Context) {
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

	resp, err := h.biz.PlaybookV1().Get(c.Request.Context(), id)
	core.WriteResponse(c, resp, err)
}

func (h *Handler) ListPlaybooks(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	var req playbookbiz.ListRequest

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

	resp, err := h.biz.PlaybookV1().List(c.Request.Context(), &req)
	core.WriteResponse(c, resp, err)
}

func (h *Handler) UpdatePlaybook(c *gin.Context) {
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

	var req updatePlaybookRequest
	if err := c.ShouldBindJSON(&req); err != nil && err != io.EOF {
		core.WriteResponse(c, nil, err)
		return
	}

	ctx := c.Request.Context()
	resp, err := h.biz.PlaybookV1().Update(ctx, id, &playbookbiz.UpdateRequest{
		Name:            req.Name,
		Description:     req.Description,
		Config:          req.Config,
		ExpectedVersion: req.ExpectedVersion,
		UpdatedBy:       contextx.Username(ctx),
	})
	core.WriteResponse(c, resp, err)
}

func (h *Handler) DeletePlaybook(c *gin.Context) {
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

	err = h.biz.PlaybookV1().Delete(c.Request.Context(), id)
	core.WriteResponse(c, gin.H{"deleted": true}, err)
}

func (h *Handler) ActivatePlaybook(c *gin.Context) {
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

	var req activatePlaybookRequest
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

	err = h.biz.PlaybookV1().Activate(ctx, id, operator)
	core.WriteResponse(c, gin.H{"activated": true}, err)
}

func (h *Handler) DeactivatePlaybook(c *gin.Context) {
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

	err = h.biz.PlaybookV1().Deactivate(c.Request.Context(), id)
	core.WriteResponse(c, gin.H{"deactivated": true}, err)
}

func (h *Handler) RollbackPlaybook(c *gin.Context) {
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

	var req rollbackPlaybookRequest
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

	err = h.biz.PlaybookV1().Rollback(ctx, id, req.Version, operator)
	core.WriteResponse(c, gin.H{"rollbacked": true}, err)
}

func (h *Handler) GetActivePlaybook(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	resp, err := h.biz.PlaybookV1().GetActive(c.Request.Context())
	core.WriteResponse(c, resp, err)
}

//nolint:gochecknoinits // Route registration follows repository-wide init registrar pattern.
func init() {
	Register(func(v1 *gin.RouterGroup, handler *Handler, mws ...gin.HandlerFunc) {
		playbooks := v1.Group("/playbooks", mws...)
		playbooks.POST("", handler.CreatePlaybook)
		playbooks.GET("", handler.ListPlaybooks)
		playbooks.GET("/active", handler.GetActivePlaybook)
		playbooks.GET("/:id", handler.GetPlaybook)
		playbooks.PUT("/:id", handler.UpdatePlaybook)
		playbooks.DELETE("/:id", handler.DeletePlaybook)
		playbooks.POST("/:id/activate", handler.ActivatePlaybook)
		playbooks.POST("/:id/deactivate", handler.DeactivatePlaybook)
		playbooks.POST("/:id/rollback", handler.RollbackPlaybook)
	})
}
