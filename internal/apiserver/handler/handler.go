package handler

import (
	"log/slog"

	"github.com/gin-gonic/gin"

	"github.com/aiopsre/rca-api/internal/apiserver/biz"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/queue"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/validation"
)

// Handler manages the business logic for API requests and event processing.
type Handler struct {
	biz              biz.IBiz
	val              *validation.Validator
	jobQueueNotifier *queue.Notifier
}

// Registrar defines a function signature for registering HTTP routes.
type Registrar func(v1 *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc)

var registrars []Registrar

// NewHandler creates a new instance of Handler.
func NewHandler(biz biz.IBiz, val *validation.Validator) *Handler {
	return &Handler{
		biz:              biz,
		val:              val,
		jobQueueNotifier: queue.NewNotifier(),
	}
}

// Register adds a new REST route registrar to the global registry.
func Register(r Registrar) {
	registrars = append(registrars, r)
}

// ApplyTo applies the registered REST API registrars to the provided Gin router group.
func (h *Handler) ApplyTo(v1 *gin.RouterGroup, mws ...gin.HandlerFunc) {
	for _, r := range registrars {
		r(v1, h, mws...)
	}

	slog.Info("rest api routes installed", "count", len(registrars))
}
