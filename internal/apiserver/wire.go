//go:build wireinject

package apiserver

import (
	"context"

	"github.com/aiopsre/rca-api/internal/apiserver/biz"
	"github.com/aiopsre/rca-api/internal/apiserver/handler"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/validation"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	"github.com/google/wire"
)

// infrastructureSet groups all infrastructure-related providers.
// This keeps the main wire.Build call clean.
var infrastructureSet = wire.NewSet(
	ProvideDB,
	ProvideJobQueueNotifier,
	ProvideAIJobQueueWakeup,
)

// NewServer initializes and creates the web server with all necessary dependencies using Wire.
func NewServer(context.Context, *Config) (*Server, error) {
	wire.Build(
		// Server infrastructure
		NewWebServer,
		NewDependencies,
		wire.Struct(new(ServerConfig), "*"), // Inject all fields
		wire.Struct(new(Server), "*"),

		// Domain layers
		store.ProviderSet,
		biz.ProviderSet,
		validation.ProviderSet,
		handler.NewHandlerWithQueueDeps,

		// Infrastructure dependencies
		infrastructureSet,
	)
	return nil, nil
}
