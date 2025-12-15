//go:build wireinject
// +build wireinject

package apiserver

import (
	"context"

	"github.com/google/wire"

	"zk8s.com/rca-api/internal/apiserver/biz"
	"zk8s.com/rca-api/internal/apiserver/handler"
	"zk8s.com/rca-api/internal/apiserver/pkg/validation"
	"zk8s.com/rca-api/internal/apiserver/store"
)

// infrastructureSet groups all infrastructure-related providers.
// This keeps the main wire.Build call clean.
var infrastructureSet = wire.NewSet(
	ProvideDB,
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
		handler.NewHandler,

		// Infrastructure dependencies
		infrastructureSet,
	)
	return nil, nil
}
