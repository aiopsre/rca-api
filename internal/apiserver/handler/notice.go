//nolint:dupl
package handler

import (
	"errors"
	"io"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"
	"github.com/onexstack/onexstack/pkg/errorsx"

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

func (h *Handler) ReplayNoticeDelivery(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeNoticeAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	useLatestChannel, err := parseUseLatestChannelFromQuery(c)
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	req := &v1.ReplayNoticeDeliveryRequest{
		DeliveryID:       strings.TrimSpace(c.Param("deliveryID")),
		UseLatestChannel: &useLatestChannel,
	}
	if err := h.val.ValidateReplayNoticeDeliveryRequest(c.Request.Context(), req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	resp, err := h.biz.NoticeV1().ReplayDelivery(c.Request.Context(), req)
	core.WriteResponse(c, resp, err)
}

func (h *Handler) CancelNoticeDelivery(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeNoticeAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	req := &v1.CancelNoticeDeliveryRequest{DeliveryID: strings.TrimSpace(c.Param("deliveryID"))}
	if err := h.val.ValidateCancelNoticeDeliveryRequest(c.Request.Context(), req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	resp, err := h.biz.NoticeV1().CancelDelivery(c.Request.Context(), req)
	core.WriteResponse(c, resp, err)
}

func (h *Handler) OperateNoticeDelivery(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeNoticeAdmin); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	deliveryID, op, ok := parseNoticeDeliveryAction(c.Param("deliveryID"))
	if !ok {
		core.WriteResponse(c, nil, errorsx.ErrInvalidArgument)
		return
	}

	switch op {
	case "replay":
		useLatestChannel, err := parseUseLatestChannelFromQuery(c)
		if err != nil {
			core.WriteResponse(c, nil, err)
			return
		}
		req := &v1.ReplayNoticeDeliveryRequest{
			DeliveryID:       deliveryID,
			UseLatestChannel: &useLatestChannel,
		}
		if err := h.val.ValidateReplayNoticeDeliveryRequest(c.Request.Context(), req); err != nil {
			core.WriteResponse(c, nil, err)
			return
		}
		resp, err := h.biz.NoticeV1().ReplayDelivery(c.Request.Context(), req)
		core.WriteResponse(c, resp, err)

	case "cancel":
		req := &v1.CancelNoticeDeliveryRequest{DeliveryID: deliveryID}
		if err := h.val.ValidateCancelNoticeDeliveryRequest(c.Request.Context(), req); err != nil {
			core.WriteResponse(c, nil, err)
			return
		}
		resp, err := h.biz.NoticeV1().CancelDelivery(c.Request.Context(), req)
		core.WriteResponse(c, resp, err)

	default:
		core.WriteResponse(c, nil, errorsx.ErrInvalidArgument)
	}
}

func parseNoticeDeliveryAction(raw string) (string, string, bool) {
	action := strings.TrimSpace(strings.TrimPrefix(raw, "/"))
	if action == "" {
		return "", "", false
	}
	if strings.Contains(action, "/") {
		return parseSlashNoticeDeliveryAction(action)
	}
	return parseColonNoticeDeliveryAction(action)
}

func parseSlashNoticeDeliveryAction(action string) (string, string, bool) {
	parts := strings.Split(action, "/")
	if len(parts) != 2 {
		return "", "", false
	}
	deliveryID := strings.TrimSpace(parts[0])
	op := strings.ToLower(strings.TrimSpace(parts[1]))
	return deliveryID, op, isSupportedNoticeDeliveryAction(deliveryID, op)
}

func parseColonNoticeDeliveryAction(action string) (string, string, bool) {
	idx := strings.LastIndex(action, ":")
	if idx <= 0 || idx >= len(action)-1 {
		return "", "", false
	}
	deliveryID := strings.TrimSpace(action[:idx])
	op := strings.ToLower(strings.TrimSpace(action[idx+1:]))
	return deliveryID, op, isSupportedNoticeDeliveryAction(deliveryID, op)
}

func isSupportedNoticeDeliveryAction(deliveryID string, op string) bool {
	return deliveryID != "" && (op == "replay" || op == "cancel")
}

func parseUseLatestChannel(raw string) (bool, error) {
	switch strings.TrimSpace(raw) {
	case "":
		return false, nil
	case "0":
		return false, nil

	case "1":
		return true, nil

	default:
		return false, errorsx.ErrInvalidArgument
	}
}

func parseUseLatestChannelFromQuery(c *gin.Context) (bool, error) {
	if c == nil {
		return false, nil
	}
	raw := c.Query("use_latest_channel")
	if strings.TrimSpace(raw) == "" {
		raw = c.Query("useLatestChannel")
	}
	return parseUseLatestChannel(raw)
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
		deliveryRG.POST("/:deliveryID/replay", handler.ReplayNoticeDelivery)
		deliveryRG.POST("/:deliveryID/cancel", handler.CancelNoticeDelivery)
		deliveryRG.POST("/:deliveryID", handler.OperateNoticeDelivery)
	})
}
