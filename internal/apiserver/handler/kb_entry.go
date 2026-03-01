package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/aiopsre/rca-api/internal/apiserver/pkg/authz"
)

// CreateKBEntry handles the HTTP request to create a new KB entry.
func (h *Handler) CreateKBEntry(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleJSONRequest(c, h.biz.KBEntryV1().Create, h.val.ValidateCreateKBEntryRequest)
}

// GetKBEntry handles the HTTP request to get a KB entry by kb_id.
func (h *Handler) GetKBEntry(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleUriRequest(c, h.biz.KBEntryV1().Get, h.val.ValidateGetKBEntryRequest)
}

// ListKBEntries handles the HTTP request to list KB entries.
func (h *Handler) ListKBEntries(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleQueryRequest(c, h.biz.KBEntryV1().List, h.val.ValidateListKBEntriesRequest)
}

// UpdateKBEntry handles the HTTP request to update a KB entry.
func (h *Handler) UpdateKBEntry(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleAllRequest(c, h.biz.KBEntryV1().Update, h.val.ValidateUpdateKBEntryRequest)
}

// DeleteKBEntry handles the HTTP request to delete a KB entry.
func (h *Handler) DeleteKBEntry(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeConfigAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleUriRequest(c, h.biz.KBEntryV1().Delete, h.val.ValidateDeleteKBEntryRequest)
}

//nolint:gochecknoinits // Route registration follows repository-wide init registrar pattern.
func init() {
	Register(func(v1 *gin.RouterGroup, handler *Handler, mws ...gin.HandlerFunc) {
		kbEntries := v1.Group("/kb-entries", mws...)
		kbEntries.POST("", handler.CreateKBEntry)
		kbEntries.GET("", handler.ListKBEntries)
		kbEntries.GET("/:kbID", handler.GetKBEntry)
		kbEntries.PUT("/:kbID", handler.UpdateKBEntry)
		kbEntries.DELETE("/:kbID", handler.DeleteKBEntry)
	})
}