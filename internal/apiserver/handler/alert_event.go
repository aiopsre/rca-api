package handler

import (
	"errors"
	"io"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/aiopsre/rca-api/internal/apiserver/pkg/authz"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

func (h *Handler) IngestAlertEvent(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAlertIngest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	var req v1.IngestAlertEventRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	if req.IdempotencyKey == nil {
		if key := strings.TrimSpace(c.GetHeader("Idempotency-Key")); key != "" {
			req.IdempotencyKey = &key
		}
	}
	if err := h.val.ValidateIngestAlertEventRequest(c.Request.Context(), &req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	resp, err := h.biz.AlertEventV1().Ingest(c.Request.Context(), &req)
	core.WriteResponse(c, resp, err)
}

func (h *Handler) ListCurrentAlertEvents(c *gin.Context) {
	h.listAlertEvents(c, true)
}

func (h *Handler) ListHistoryAlertEvents(c *gin.Context) {
	h.listAlertEvents(c, false)
}

func (h *Handler) listAlertEvents(c *gin.Context, current bool) {
	if err := authz.RequireAnyScope(c, authz.ScopeAlertRead); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	if current {
		var req v1.ListCurrentAlertEventsRequest
		if err := c.ShouldBindQuery(&req); err != nil {
			core.WriteResponse(c, nil, err)
			return
		}
		if err := h.val.ValidateListCurrentAlertEventsRequest(c.Request.Context(), &req); err != nil {
			core.WriteResponse(c, nil, err)
			return
		}

		resp, err := h.biz.AlertEventV1().ListCurrent(c.Request.Context(), &req)
		core.WriteResponse(c, resp, err)
		return
	}

	var req v1.ListHistoryAlertEventsRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	if err := h.val.ValidateListHistoryAlertEventsRequest(c.Request.Context(), &req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	resp, err := h.biz.AlertEventV1().ListHistory(c.Request.Context(), &req)
	core.WriteResponse(c, resp, err)
}

func (h *Handler) AckAlertEvent(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAlertAck); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	var req v1.AckAlertEventRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		core.WriteResponse(c, nil, err)
		return
	}
	req.EventID = strings.TrimSpace(c.Param("eventID"))
	if err := h.val.ValidateAckAlertEventRequest(c.Request.Context(), &req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	resp, err := h.biz.AlertEventV1().Ack(c.Request.Context(), &req)
	core.WriteResponse(c, resp, err)
}

func (h *Handler) AlertEventsPostAction(c *gin.Context) {
	switch normalizeColonAction(c.Param("action")) {
	case "ingest":
		h.IngestAlertEvent(c)
	default:
		core.WriteResponse(c, nil, errno.ErrPageNotFound)
	}
}

func (h *Handler) AlertEventsGetAction(c *gin.Context) {
	switch normalizeColonAction(c.Param("action")) {
	case "current":
		h.ListCurrentAlertEvents(c)
	case "history":
		h.ListHistoryAlertEvents(c)
	default:
		core.WriteResponse(c, nil, errno.ErrPageNotFound)
	}
}

func normalizeColonAction(action string) string {
	action = strings.TrimSpace(action)
	return strings.TrimPrefix(action, ":")
}

func init() {
	Register(func(v1 *gin.RouterGroup, handler *Handler, mws ...gin.HandlerFunc) {
		root := v1.Group("", mws...)
		root.POST("/alert-events:action", handler.AlertEventsPostAction)
		root.GET("/alert-events:action", handler.AlertEventsGetAction)
		root.POST("/alert-events/:eventID/ack", handler.AckAlertEvent)
	})
}
