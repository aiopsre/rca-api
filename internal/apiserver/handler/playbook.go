package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/aiopsre/rca-api/internal/apiserver/pkg/authz"
)

// CreatePlaybook handles the HTTP request to create a new playbook.
func (h *Handler) CreatePlaybook(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleJSONRequest(c, h.biz.PlaybookV1().Create, h.val.ValidateCreatePlaybookRequest)
}

// GetPlaybook handles the HTTP request to get a playbook by ID.
func (h *Handler) GetPlaybook(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleUriRequest(c, h.biz.PlaybookV1().Get, h.val.ValidateGetPlaybookRequest)
}

// ListPlaybooks handles the HTTP request to list playbooks.
func (h *Handler) ListPlaybooks(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleQueryRequest(c, h.biz.PlaybookV1().List, h.val.ValidateListPlaybooksRequest)
}

// UpdatePlaybook handles the HTTP request to update a playbook.
func (h *Handler) UpdatePlaybook(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleAllRequest(c, h.biz.PlaybookV1().Update, h.val.ValidateUpdatePlaybookRequest)
}

// DeletePlaybook handles the HTTP request to delete a playbook.
func (h *Handler) DeletePlaybook(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleUriRequest(c, h.biz.PlaybookV1().Delete, h.val.ValidateDeletePlaybookRequest)
}

// ActivatePlaybook handles the HTTP request to activate a playbook.
func (h *Handler) ActivatePlaybook(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleAllRequest(c, h.biz.PlaybookV1().Activate, h.val.ValidateActivatePlaybookRequest)
}

// DeactivatePlaybook handles the HTTP request to deactivate a playbook.
func (h *Handler) DeactivatePlaybook(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleUriRequest(c, h.biz.PlaybookV1().Deactivate, h.val.ValidateDeactivatePlaybookRequest)
}

// RollbackPlaybook handles the HTTP request to rollback a playbook.
func (h *Handler) RollbackPlaybook(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleAllRequest(c, h.biz.PlaybookV1().Rollback, h.val.ValidateRollbackPlaybookRequest)
}

// GetActivePlaybook handles the HTTP request to get the currently active playbook.
func (h *Handler) GetActivePlaybook(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleJSONRequest(c, h.biz.PlaybookV1().GetActive, h.val.ValidateGetActivePlaybookRequest)
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