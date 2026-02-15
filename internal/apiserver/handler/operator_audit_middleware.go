package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	sessionbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/session"
	"github.com/aiopsre/rca-api/internal/pkg/contextx"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

var sensitiveRBACActions = map[string]struct{}{
	"ai.run":             {},
	"session.review":     {},
	"session.assignment": {},
	"config.admin":       {},
	"rbac.admin":         {},
}

// AuditSensitiveOperatorAction appends one audit trail for sensitive operator API calls.
// It writes structured log always and incident action logs when incident context can be resolved.
func (h *Handler) AuditSensitiveOperatorAction() gin.HandlerFunc {
	return func(c *gin.Context) {
		startedAt := time.Now().UTC()
		c.Next()

		if h == nil || h.biz == nil {
			return
		}
		path := normalizeSwaggerRBACPath(c.FullPath())
		if path == "" {
			path = normalizeSwaggerRBACPath(c.Request.URL.Path)
		}
		if path == "" {
			return
		}
		method := strings.ToLower(strings.TrimSpace(c.Request.Method))
		actions := swaggerRbacActions(method, path)
		if !containsSensitiveAction(actions) {
			return
		}

		ctx := c.Request.Context()
		operatorID := strings.TrimSpace(contextx.UserID(ctx))
		operatorName := strings.TrimSpace(contextx.Username(ctx))
		actor := operatorID
		if actor == "" {
			actor = operatorName
		}
		if actor == "" {
			actor = "operator:unknown"
		}
		statusCode := c.Writer.Status()
		latencyMs := time.Since(startedAt).Milliseconds()
		sessionID := strings.TrimSpace(c.Param("sessionID"))
		incidentID := strings.TrimSpace(resolveAuditIncidentID(ctx, h, sessionID))

		payload := map[string]any{
			"path":        path,
			"method":      strings.ToUpper(method),
			"status_code": statusCode,
			"latency_ms":  latencyMs,
			"session_id":  sessionID,
			"incident_id": incidentID,
			"operator_id": operatorID,
			"username":    operatorName,
			"actions":     actions,
			"request_id":  strings.TrimSpace(contextx.RequestID(ctx)),
		}
		rawPayload, _ := json.Marshal(payload)
		slog.InfoContext(ctx, "operator sensitive api audit",
			"path", path,
			"method", strings.ToUpper(method),
			"status_code", statusCode,
			"actor", actor,
			"session_id", sessionID,
			"incident_id", incidentID,
			"actions", actions,
		)

		if incidentID == "" {
			return
		}
		if statusCode >= http.StatusInternalServerError {
			return
		}
		summary := strings.TrimSpace(strings.ToUpper(method) + " " + path + " status=" + strings.TrimSpace(intToString(statusCode)))
		detailsJSON := string(rawPayload)
		_, err := h.biz.IncidentV1().CreateAction(ctx, &v1.CreateIncidentActionRequest{
			IncidentID:  incidentID,
			Actor:       strPtr(actor),
			ActionType:  "operator_api_call",
			Summary:     summary,
			DetailsJSON: strPtr(detailsJSON),
		})
		if err != nil {
			slog.WarnContext(ctx, "incident action audit append skipped",
				"incident_id", incidentID,
				"path", path,
				"error", err,
			)
		}
	}
}

func containsSensitiveAction(actions []string) bool {
	for _, action := range actions {
		if _, ok := sensitiveRBACActions[strings.TrimSpace(action)]; ok {
			return true
		}
	}
	return false
}

func resolveAuditIncidentID(ctx context.Context, h *Handler, sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if h == nil || h.biz == nil || sessionID == "" {
		return ""
	}
	resp, err := h.biz.SessionV1().Get(ctx, &sessionbiz.GetSessionContextRequest{SessionID: &sessionID})
	if err != nil || resp == nil || resp.Session == nil || resp.Session.IncidentID == nil {
		return ""
	}
	return strings.TrimSpace(*resp.Session.IncidentID)
}

func intToString(value int) string {
	if value == 0 {
		return "0"
	}
	return strings.TrimSpace(strconv.Itoa(value))
}
