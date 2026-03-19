package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/aiopsre/rca-api/internal/apiserver/pkg/authz"
)

// CreateVerificationTemplate handles the HTTP request to create a new verification template.
func (h *Handler) CreateVerificationTemplate(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleJSONRequest(c, h.biz.VerificationTemplateV1().Create, h.val.ValidateCreateVerificationTemplateRequest)
}

// GetVerificationTemplate handles the HTTP request to get a verification template by ID.
func (h *Handler) GetVerificationTemplate(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleUriRequest(c, h.biz.VerificationTemplateV1().Get, h.val.ValidateGetVerificationTemplateRequest)
}

// ListVerificationTemplates handles the HTTP request to list verification templates.
func (h *Handler) ListVerificationTemplates(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleQueryRequest(c, h.biz.VerificationTemplateV1().List, h.val.ValidateListVerificationTemplatesRequest)
}

// UpdateVerificationTemplate handles the HTTP request to update a verification template.
func (h *Handler) UpdateVerificationTemplate(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleAllRequest(c, h.biz.VerificationTemplateV1().Update, h.val.ValidateUpdateVerificationTemplateRequest)
}

// DeleteVerificationTemplate handles the HTTP request to delete a verification template.
func (h *Handler) DeleteVerificationTemplate(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleUriRequest(c, h.biz.VerificationTemplateV1().Delete, h.val.ValidateDeleteVerificationTemplateRequest)
}

// ActivateVerificationTemplate handles the HTTP request to activate a verification template.
func (h *Handler) ActivateVerificationTemplate(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleAllRequest(c, h.biz.VerificationTemplateV1().Activate, h.val.ValidateActivateVerificationTemplateRequest)
}

// DeactivateVerificationTemplate handles the HTTP request to deactivate a verification template.
func (h *Handler) DeactivateVerificationTemplate(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleUriRequest(c, h.biz.VerificationTemplateV1().Deactivate, h.val.ValidateDeactivateVerificationTemplateRequest)
}

// GetActiveVerificationTemplate handles the HTTP request to get the currently active verification template.
func (h *Handler) GetActiveVerificationTemplate(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleJSONRequest(c, h.biz.VerificationTemplateV1().GetActive, h.val.ValidateGetActiveVerificationTemplateRequest)
}

//nolint:gochecknoinits // Route registration follows repository-wide init registrar pattern.
func init() {
	Register(func(v1 *gin.RouterGroup, handler *Handler, mws ...gin.HandlerFunc) {
		templates := v1.Group("/verification-templates", mws...)
		templates.POST("", handler.CreateVerificationTemplate)
		templates.GET("", handler.ListVerificationTemplates)
		templates.GET("/active", handler.GetActiveVerificationTemplate)
		templates.GET("/:id", handler.GetVerificationTemplate)
		templates.PUT("/:id", handler.UpdateVerificationTemplate)
		templates.DELETE("/:id", handler.DeleteVerificationTemplate)
		templates.POST("/:id/activate", handler.ActivateVerificationTemplate)
		templates.POST("/:id/deactivate", handler.DeactivateVerificationTemplate)
	})
}