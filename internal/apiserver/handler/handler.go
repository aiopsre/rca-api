package handler

import (
	"log/slog"
	"sync"

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
	jobQueueWakeup   queue.AIJobQueueWakeup
	longPollWaiter   *queue.AdaptiveWaiter
	longPollOnce     sync.Once
	mcpPolicies      mcpPolicyRegistry
}

// Registrar defines a function signature for registering HTTP routes.
type Registrar func(v1 *gin.RouterGroup, h *Handler, mws ...gin.HandlerFunc)

var registrars []Registrar

// NewHandler creates a new instance of Handler.
func NewHandler(biz biz.IBiz, val *validation.Validator) *Handler {
	return NewHandlerWithQueueDeps(biz, val, queue.NewNotifier(), queue.NewNoopWakeup())
}

// NewHandlerWithQueueDeps creates a handler with externally provided queue notifier/wakeup bridge.
func NewHandlerWithQueueDeps(
	biz biz.IBiz,
	val *validation.Validator,
	jobQueueNotifier *queue.Notifier,
	jobQueueWakeup queue.AIJobQueueWakeup,
) *Handler {

	if jobQueueNotifier == nil {
		jobQueueNotifier = queue.NewNotifier()
	}
	if jobQueueWakeup == nil {
		jobQueueWakeup = queue.NewNoopWakeup()
	}

	return &Handler{
		biz:              biz,
		val:              val,
		jobQueueNotifier: jobQueueNotifier,
		jobQueueWakeup:   jobQueueWakeup,
		mcpPolicies:      newMCPPolicyRegistry(),
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

// Close releases resources held by downstream biz components.
func (h *Handler) Close() error {
	if h == nil || h.biz == nil {
		return nil
	}
	return h.biz.Close()
}
