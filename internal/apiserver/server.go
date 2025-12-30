package apiserver

import (
	"context"
	"log/slog"
	"time"

	genericoptions "github.com/onexstack/onexstack/pkg/options"
	"github.com/onexstack/onexstack/pkg/server"
	"github.com/onexstack/onexstack/pkg/store/registry"
	"gorm.io/gorm"
	"zk8s.com/rca-api/internal/apiserver/handler"
	"zk8s.com/rca-api/internal/apiserver/pkg/metrics"
	noticepkg "zk8s.com/rca-api/internal/apiserver/pkg/notice"
)

const serviceName = "rca-apiserver"

// Dependencies collects all components that need initialization but are not directly used
// by the main server struct during runtime (e.g., sidecar processes, cache warmers).
type Dependencies struct{}

// Config contains application-related configurations.
type Config struct {
	TLSOptions    *genericoptions.TLSOptions
	HTTPOptions   *genericoptions.HTTPOptions
	MySQLOptions  *genericoptions.MySQLOptions
	NoticeBaseURL string
}

// Server represents the web server and its background workers.
type Server struct {
	cfg *ServerConfig
	srv server.Server
}

// ServerConfig contains the core dependencies and configurations of the server.
type ServerConfig struct {
	*Config

	Dependencies *Dependencies
	Handler      *handler.Handler
}

// New creates and returns a new Server instance.
func (cfg *Config) New(ctx context.Context) (*Server, error) {
	noticepkg.SetConfiguredNoticeBaseURL(cfg.NoticeBaseURL)

	// Create the core server instance using dependency injection.
	// This relies on the wire-generated NewServer function.
	s, err := NewServer(ctx, cfg)
	if err != nil {
		return nil, err
	}

	return s.Prepare(ctx)
}

// Prepare performs post-initialization tasks such as registering subscribers.
func (s *Server) Prepare(ctx context.Context) (*Server, error) {
	_ = metrics.Init(serviceName)
	return s, nil
}

// Run starts the server and listens for termination signals.
// It gracefully shuts down the server upon receiving a termination signal from the context.
func (s *Server) Run(ctx context.Context) error {
	// Start the HTTP/gRPC server in a background goroutine.
	go s.srv.RunOrDie(ctx)

	// Block until the context is canceled (e.g., via SIGINT/SIGTERM).
	<-ctx.Done()

	slog.Info("shutting down server...")

	// Create a new context with a timeout to ensure graceful shutdown doesn't hang indefinitely.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	// Trigger graceful shutdown for all components.
	s.srv.GracefulStop(shutdownCtx)

	slog.Info("server exited successfully")

	return nil
}

// NewDB creates and returns a *gorm.DB instance for database operations.
func (cfg *Config) NewDB() (*gorm.DB, error) {
	slog.Info("initializing database connection", "type", "mariadb")
	dbInstance, err := cfg.MySQLOptions.NewDB()
	if err != nil {
		slog.Error("failed to create database connection", "error", err)
		return nil, err
	}

	// Automatically migrate database schema
	if err := registry.Migrate(dbInstance); err != nil {
		slog.Error("failed to migrate database schema", "error", err)
		return nil, err
	}

	return dbInstance, nil
}

// ProvideDB provides a database instance based on the configuration.
func ProvideDB(cfg *Config) (*gorm.DB, error) {
	return cfg.NewDB()
}

// NewDependencies initializes all components that need to be started but are not directly stored.
// This is typically used for side-effects or warming up caches.
func NewDependencies(ctx context.Context) *Dependencies {
	return &Dependencies{}
}

// NewWebServer creates and returns a new web server instance using the provided server configuration.
func NewWebServer(serverConfig *ServerConfig) (server.Server, error) {
	return serverConfig.NewGinServer()
}
