package handler

import (
	"context"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/aiopsre/rca-api/internal/apiserver/pkg/authz"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

func (h *Handler) ResolveOrchestratorToolset(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleQueryRequest(c, h.resolveOrchestratorToolsetQuery, h.val.ValidateResolveToolsetRequest)
}

func (h *Handler) resolveOrchestratorToolsetQuery(
	ctx context.Context,
	req *v1.ResolveToolsetRequest,
) (*v1.ResolveToolsetResponse, error) {
	resp, err := h.biz.OrchestratorToolsetV1().Resolve(ctx, req)
	if err != nil || resp == nil {
		return resp, err
	}

	if len(resp.Toolsets) > 0 {
		if resp.Toolset == nil {
			resp.Toolset = resp.Toolsets[0]
		}
		return resp, nil
	}

	if resp.Toolset != nil {
		resp.Toolsets = []*v1.OrchestratorToolset{resp.Toolset}
	}
	return resp, nil
}

func init() {
	Register(func(v1 *gin.RouterGroup, handler *Handler, mws ...gin.HandlerFunc) {
		group := v1.Group("/orchestrator/toolsets", mws...)
		group.GET("/resolve", handler.ResolveOrchestratorToolset)
	})
}
