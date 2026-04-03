package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/aiopsre/rca-api/internal/apiserver/pkg/authz"
)

// CreateMcpServer handles the HTTP request to create a new MCP server.
func (h *Handler) CreateMcpServer(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleJSONRequest(c, h.biz.McpServerV1().Create, h.val.ValidateCreateMcpServerRequest)
}

// GetMcpServer handles the HTTP request to get an MCP server by ID.
func (h *Handler) GetMcpServer(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleUriRequest(c, h.biz.McpServerV1().Get, h.val.ValidateGetMcpServerRequest)
}

// ListMcpServers handles the HTTP request to list MCP servers.
func (h *Handler) ListMcpServers(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleQueryRequest(c, h.biz.McpServerV1().List, h.val.ValidateListMcpServersRequest)
}

// UpdateMcpServer handles the HTTP request to update an MCP server.
func (h *Handler) UpdateMcpServer(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleAllRequest(c, h.biz.McpServerV1().Update, h.val.ValidateUpdateMcpServerRequest)
}

// DeleteMcpServer handles the HTTP request to delete an MCP server.
func (h *Handler) DeleteMcpServer(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleUriRequest(c, h.biz.McpServerV1().Delete, h.val.ValidateDeleteMcpServerRequest)
}

//nolint:gochecknoinits // Route registration follows repository-wide registrar pattern.
func init() {
	Register(func(v1 *gin.RouterGroup, handler *Handler, mws ...gin.HandlerFunc) {
		rg := v1.Group("/mcp-servers", mws...)
		rg.POST("", handler.CreateMcpServer)
		rg.GET("/:mcpServerID", handler.GetMcpServer)
		rg.GET("", handler.ListMcpServers)
		rg.PUT("/:mcpServerID", handler.UpdateMcpServer)
		rg.DELETE("/:mcpServerID", handler.DeleteMcpServer)
	})
}