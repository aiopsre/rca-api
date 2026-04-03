package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/aiopsre/rca-api/internal/apiserver/pkg/authz"
)

// CreateToolMetadata handles the HTTP request to create a new tool metadata.
func (h *Handler) CreateToolMetadata(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleJSONRequest(c, h.biz.ToolMetadataV1().Create, h.val.ValidateCreateToolMetadataRequest)
}

// GetToolMetadata handles the HTTP request to get a tool metadata by tool name.
func (h *Handler) GetToolMetadata(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleUriRequest(c, h.biz.ToolMetadataV1().Get, h.val.ValidateGetToolMetadataRequest)
}

// ListToolMetadata handles the HTTP request to list tool metadata.
func (h *Handler) ListToolMetadata(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleQueryRequest(c, h.biz.ToolMetadataV1().List, h.val.ValidateListToolMetadataRequest)
}

// UpdateToolMetadata handles the HTTP request to update a tool metadata.
func (h *Handler) UpdateToolMetadata(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleAllRequest(c, h.biz.ToolMetadataV1().Update, h.val.ValidateUpdateToolMetadataRequest)
}

// DeleteToolMetadata handles the HTTP request to delete a tool metadata.
func (h *Handler) DeleteToolMetadata(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleUriRequest(c, h.biz.ToolMetadataV1().Delete, h.val.ValidateDeleteToolMetadataRequest)
}

//nolint:gochecknoinits // Route registration follows repository-wide registrar pattern.
func init() {
	Register(func(v1 *gin.RouterGroup, handler *Handler, mws ...gin.HandlerFunc) {
		rg := v1.Group("/tool-metadata", mws...)
		rg.POST("", handler.CreateToolMetadata)
		rg.GET("/:toolName", handler.GetToolMetadata)
		rg.GET("", handler.ListToolMetadata)
		rg.PUT("/:toolName", handler.UpdateToolMetadata)
		rg.DELETE("/:toolName", handler.DeleteToolMetadata)
	})
}