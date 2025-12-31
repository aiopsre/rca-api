package handler

import (
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"
	"go.opentelemetry.io/otel"

	"github.com/aiopsre/rca-api/internal/apiserver/pkg/metrics"
)

// CreateIncident handles the HTTP request to create a new incident.
func (h *Handler) CreateIncident(c *gin.Context) {
	ctx, span := otel.Tracer("handler").Start(c.Request.Context(), "Handler.CreateIncident")
	defer span.End()

	// Update the Gin request context so subsequent middleware/handlers use the traced context.
	c.Request = c.Request.WithContext(ctx)

	metrics.M.RecordResourceCreate(ctx, "incident")

	slog.InfoContext(ctx, "processing incident creation request")

	core.HandleJSONRequest(c, h.biz.IncidentV1().Create, h.val.ValidateCreateIncidentRequest)
}

// UpdateIncident handles the HTTP request to update an existing incident's details.
func (h *Handler) UpdateIncident(c *gin.Context) {
	core.HandleAllRequest(c, h.biz.IncidentV1().Update, h.val.ValidateUpdateIncidentRequest)
}

// DeleteIncident handles the HTTP request to delete a single incident specified by URI parameters.
func (h *Handler) DeleteIncident(c *gin.Context) {
	core.HandleJSONRequest(c, h.biz.IncidentV1().Delete, h.val.ValidateDeleteIncidentRequest)
}

// GetIncident retrieves details of a specific incident based on the request parameters.
func (h *Handler) GetIncident(c *gin.Context) {
	ctx, span := otel.Tracer("handler").Start(c.Request.Context(), "Handler.GetIncident")
	defer span.End()

	c.Request = c.Request.WithContext(ctx)

	metrics.M.RecordResourceGet(ctx, "incident")

	slog.InfoContext(ctx, "processing incident retrieve request")

	core.HandleUriRequest(c, h.biz.IncidentV1().Get, h.val.ValidateGetIncidentRequest)
}

// ListIncident retrieves a list of incidents based on query parameters.
func (h *Handler) ListIncident(c *gin.Context) {
	core.HandleQueryRequest(c, h.biz.IncidentV1().List, h.val.ValidateListIncidentRequest)
}

func init() {
	Register(func(v1 *gin.RouterGroup, handler *Handler, mws ...gin.HandlerFunc) {
		rg := v1.Group("/incidents", mws...)
		rg.POST("", handler.CreateIncident)
		rg.PUT("/:incidentID", handler.UpdateIncident)
		rg.DELETE("", handler.DeleteIncident)
		rg.GET("/:incidentID", handler.GetIncident)
		rg.GET("", handler.ListIncident)
	})
}
