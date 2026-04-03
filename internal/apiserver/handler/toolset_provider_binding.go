package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/aiopsre/rca-api/internal/apiserver/pkg/authz"
)

func (h *Handler) CreateToolsetProviderBinding(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleJSONRequest(c, h.biz.ToolsetProviderBindingV1().Create, h.val.ValidateCreateToolsetProviderBindingRequest)
}

func (h *Handler) GetToolsetProviderBinding(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleUriRequest(c, h.biz.ToolsetProviderBindingV1().Get, h.val.ValidateGetToolsetProviderBindingRequest)
}

func (h *Handler) ListToolsetProviderBindings(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleQueryRequest(c, h.biz.ToolsetProviderBindingV1().List, h.val.ValidateListToolsetProviderBindingsRequest)
}

func (h *Handler) UpdateToolsetProviderBinding(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleAllRequest(c, h.biz.ToolsetProviderBindingV1().Update, h.val.ValidateUpdateToolsetProviderBindingRequest)
}

func (h *Handler) DeleteToolsetProviderBinding(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleUriRequest(c, h.biz.ToolsetProviderBindingV1().Delete, h.val.ValidateDeleteToolsetProviderBindingRequest)
}

//nolint:gochecknoinits // Route registration follows repository-wide registrar pattern.
func init() {
	Register(func(v1 *gin.RouterGroup, handler *Handler, mws ...gin.HandlerFunc) {
		rg := v1.Group("/toolset-provider-bindings", mws...)
		rg.POST("", handler.CreateToolsetProviderBinding)
		rg.GET("/:toolsetName/:mcpServerID", handler.GetToolsetProviderBinding)
		rg.GET("", handler.ListToolsetProviderBindings)
		rg.PUT("/:toolsetName/:mcpServerID", handler.UpdateToolsetProviderBinding)
		rg.DELETE("/:toolsetName/:mcpServerID", handler.DeleteToolsetProviderBinding)
	})
}
