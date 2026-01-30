package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/aiopsre/rca-api/internal/apiserver/pkg/authz"
)

func (h *Handler) RegisterOrchestratorTemplates(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRun); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleJSONRequest(c, h.biz.OrchestratorTemplateV1().Register, h.val.ValidateRegisterOrchestratorTemplatesRequest)
}

func (h *Handler) ListOrchestratorTemplates(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleQueryRequest(c, h.biz.OrchestratorTemplateV1().List, h.val.ValidateListOrchestratorTemplatesRequest)
}

func init() {
	Register(func(v1 *gin.RouterGroup, handler *Handler, mws ...gin.HandlerFunc) {
		group := v1.Group("/orchestrator/templates", mws...)
		group.POST("/register", handler.RegisterOrchestratorTemplates)
		group.GET("", handler.ListOrchestratorTemplates)
	})
}
