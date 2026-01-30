package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/aiopsre/rca-api/internal/apiserver/pkg/authz"
)

func (h *Handler) ResolveOrchestratorStrategy(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAIRead); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleQueryRequest(c, h.biz.OrchestratorStrategyV1().Resolve, h.val.ValidateResolveOrchestratorStrategyRequest)
}

func init() {
	Register(func(v1 *gin.RouterGroup, handler *Handler, mws ...gin.HandlerFunc) {
		group := v1.Group("/orchestrator/strategies", mws...)
		group.GET("/resolve", handler.ResolveOrchestratorStrategy)
	})
}
