//nolint:dupl
package handler

import (
	"errors"
	"io"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"zk8s.com/rca-api/internal/apiserver/pkg/authz"
	v1 "zk8s.com/rca-api/pkg/api/apiserver/v1"
)

func (h *Handler) CreateNoticeChannel(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeNoticeAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	var req v1.CreateNoticeChannelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	if err := h.val.ValidateCreateNoticeChannelRequest(c.Request.Context(), &req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	resp, err := h.biz.NoticeV1().CreateChannel(c.Request.Context(), &req)
	core.WriteResponse(c, resp, err)
}

func (h *Handler) GetNoticeChannel(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeNoticeRead); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	req := &v1.GetNoticeChannelRequest{ChannelID: strings.TrimSpace(c.Param("channelID"))}
	if err := h.val.ValidateGetNoticeChannelRequest(c.Request.Context(), req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	resp, err := h.biz.NoticeV1().GetChannel(c.Request.Context(), req)
	core.WriteResponse(c, resp, err)
}

func (h *Handler) ListNoticeChannels(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeNoticeRead); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	var req v1.ListNoticeChannelsRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	if err := h.val.ValidateListNoticeChannelsRequest(c.Request.Context(), &req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	resp, err := h.biz.NoticeV1().ListChannels(c.Request.Context(), &req)
	core.WriteResponse(c, resp, err)
}

func (h *Handler) PatchNoticeChannel(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeNoticeAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	var req v1.PatchNoticeChannelRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		core.WriteResponse(c, nil, err)
		return
	}
	req.ChannelID = strings.TrimSpace(c.Param("channelID"))
	if err := h.val.ValidatePatchNoticeChannelRequest(c.Request.Context(), &req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	resp, err := h.biz.NoticeV1().PatchChannel(c.Request.Context(), &req)
	core.WriteResponse(c, resp, err)
}

func (h *Handler) DeleteNoticeChannel(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeNoticeAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	req := &v1.DeleteNoticeChannelRequest{ChannelID: strings.TrimSpace(c.Param("channelID"))}
	if err := h.val.ValidateDeleteNoticeChannelRequest(c.Request.Context(), req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	resp, err := h.biz.NoticeV1().DeleteChannel(c.Request.Context(), req)
	core.WriteResponse(c, resp, err)
}

func (h *Handler) GetNoticeDelivery(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeNoticeRead); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	req := &v1.GetNoticeDeliveryRequest{DeliveryID: strings.TrimSpace(c.Param("deliveryID"))}
	if err := h.val.ValidateGetNoticeDeliveryRequest(c.Request.Context(), req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	resp, err := h.biz.NoticeV1().GetDelivery(c.Request.Context(), req)
	core.WriteResponse(c, resp, err)
}

func (h *Handler) ListNoticeDeliveries(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeNoticeRead); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	var req v1.ListNoticeDeliveriesRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	if err := h.val.ValidateListNoticeDeliveriesRequest(c.Request.Context(), &req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	resp, err := h.biz.NoticeV1().ListDeliveries(c.Request.Context(), &req)
	core.WriteResponse(c, resp, err)
}

//nolint:gochecknoinits // Route registration is intentionally init-based in this codebase.
func init() {
	Register(func(v1 *gin.RouterGroup, handler *Handler, mws ...gin.HandlerFunc) {
		channelRG := v1.Group("/notice-channels", mws...)
		channelRG.POST("", handler.CreateNoticeChannel)
		channelRG.GET("", handler.ListNoticeChannels)
		channelRG.GET("/:channelID", handler.GetNoticeChannel)
		channelRG.PATCH("/:channelID", handler.PatchNoticeChannel)
		channelRG.DELETE("/:channelID", handler.DeleteNoticeChannel)

		deliveryRG := v1.Group("/notice-deliveries", mws...)
		deliveryRG.GET("", handler.ListNoticeDeliveries)
		deliveryRG.GET("/:deliveryID", handler.GetNoticeDelivery)
	})
}
