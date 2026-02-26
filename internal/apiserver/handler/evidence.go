package handler

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/aiopsre/rca-api/internal/apiserver/pkg/authz"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/runtimecontract"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

func (h *Handler) SaveIncidentEvidence(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeEvidenceSave); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	var req v1.SaveEvidenceRequest
	if err := core.ShouldBindAll(c, &req, h.val.ValidateSaveEvidenceRequest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	if req.IdempotencyKey == nil {
		headerKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
		if headerKey != "" {
			req.IdempotencyKey = &headerKey
		}
	}
	req = *runtimecontract.EvidencePublishRequestFromAPI(&req).ToAPIRequest()

	resp, err := h.biz.EvidenceV1().Save(c.Request.Context(), &req)
	core.WriteResponse(c, resp, err)
}

func (h *Handler) ListIncidentEvidence(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeEvidenceRead); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	var req v1.ListIncidentEvidenceRequest
	if err := c.ShouldBindUri(&req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	if err := c.ShouldBindQuery(&req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	if err := h.val.ValidateListIncidentEvidenceRequest(c.Request.Context(), &req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	resp, err := h.biz.EvidenceV1().ListByIncident(c.Request.Context(), &req)
	core.WriteResponse(c, resp, err)
}

func (h *Handler) GetEvidence(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeEvidenceRead); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleUriRequest(c, h.biz.EvidenceV1().Get, h.val.ValidateGetEvidenceRequest)
}

func init() {
	Register(func(v1 *gin.RouterGroup, handler *Handler, mws ...gin.HandlerFunc) {
		root := v1.Group("", mws...)
		root.GET("/evidence/:evidenceID", handler.GetEvidence)

		rg := v1.Group("/incidents", mws...)
		rg.POST("/:incidentID/evidence", handler.SaveIncidentEvidence)
		rg.GET("/:incidentID/evidence", handler.ListIncidentEvidence)
	})
}