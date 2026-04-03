//nolint:dupl
package handler

import (
	"errors"
	"io"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/aiopsre/rca-api/internal/apiserver/pkg/authz"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

func (h *Handler) CreateSilence(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeSilenceAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	var req v1.CreateSilenceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	if err := h.val.ValidateCreateSilenceRequest(c.Request.Context(), &req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	resp, err := h.biz.SilenceV1().Create(c.Request.Context(), &req)
	core.WriteResponse(c, resp, err)
}

func (h *Handler) GetSilence(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeSilenceRead); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	req := &v1.GetSilenceRequest{SilenceID: strings.TrimSpace(c.Param("silenceID"))}
	if err := h.val.ValidateGetSilenceRequest(c.Request.Context(), req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	resp, err := h.biz.SilenceV1().Get(c.Request.Context(), req)
	core.WriteResponse(c, resp, err)
}

func (h *Handler) ListSilences(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeSilenceRead); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	var req v1.ListSilencesRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	if err := h.val.ValidateListSilencesRequest(c.Request.Context(), &req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	resp, err := h.biz.SilenceV1().List(c.Request.Context(), &req)
	core.WriteResponse(c, resp, err)
}

func (h *Handler) PatchSilence(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeSilenceAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	var req v1.PatchSilenceRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		core.WriteResponse(c, nil, err)
		return
	}
	req.SilenceID = strings.TrimSpace(c.Param("silenceID"))
	if err := h.val.ValidatePatchSilenceRequest(c.Request.Context(), &req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	resp, err := h.biz.SilenceV1().Patch(c.Request.Context(), &req)
	core.WriteResponse(c, resp, err)
}

func (h *Handler) DeleteSilence(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeSilenceAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	req := &v1.DeleteSilenceRequest{SilenceID: strings.TrimSpace(c.Param("silenceID"))}
	if err := h.val.ValidateDeleteSilenceRequest(c.Request.Context(), req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	resp, err := h.biz.SilenceV1().Delete(c.Request.Context(), req)
	core.WriteResponse(c, resp, err)
}

//nolint:gochecknoinits // Route registration is intentionally init-based in this codebase.
func init() {
	Register(func(v1 *gin.RouterGroup, handler *Handler, mws ...gin.HandlerFunc) {
		rg := v1.Group("/silences", mws...)
		rg.POST("", handler.CreateSilence)
		rg.GET("", handler.ListSilences)
		rg.GET("/:silenceID", handler.GetSilence)
		rg.PATCH("/:silenceID", handler.PatchSilence)
		rg.DELETE("/:silenceID", handler.DeleteSilence)
	})
}
