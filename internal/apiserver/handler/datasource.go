package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/aiopsre/rca-api/internal/apiserver/pkg/authz"
)

func (h *Handler) CreateDatasource(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeDatasourceAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleJSONRequest(c, h.biz.DatasourceV1().Create, h.val.ValidateCreateDatasourceRequest)
}

func (h *Handler) UpdateDatasource(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeDatasourceAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleAllRequest(c, h.biz.DatasourceV1().Update, h.val.ValidateUpdateDatasourceRequest)
}

func (h *Handler) DeleteDatasource(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeDatasourceAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleUriRequest(c, h.biz.DatasourceV1().Delete, h.val.ValidateDeleteDatasourceRequest)
}

func (h *Handler) GetDatasource(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeDatasourceRead, authz.ScopeDatasourceAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleUriRequest(c, h.biz.DatasourceV1().Get, h.val.ValidateGetDatasourceRequest)
}

func (h *Handler) ListDatasource(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeDatasourceRead, authz.ScopeDatasourceAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleQueryRequest(c, h.biz.DatasourceV1().List, h.val.ValidateListDatasourceRequest)
}

func init() {
	Register(func(v1 *gin.RouterGroup, handler *Handler, mws ...gin.HandlerFunc) {
		rg := v1.Group("/datasources", mws...)
		rg.POST("", handler.CreateDatasource)
		rg.PATCH("/:datasourceID", handler.UpdateDatasource)
		rg.DELETE("/:datasourceID", handler.DeleteDatasource)
		rg.GET("/:datasourceID", handler.GetDatasource)
		rg.GET("", handler.ListDatasource)
	})
}
