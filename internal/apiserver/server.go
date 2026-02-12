package apiserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/aiopsre/rca-api/internal/apiserver/handler"
	alertingingest "github.com/aiopsre/rca-api/internal/apiserver/pkg/alerting/ingest"
	alertingpolicy "github.com/aiopsre/rca-api/internal/apiserver/pkg/alerting/policy"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/cachex"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/metrics"
	noticepkg "github.com/aiopsre/rca-api/internal/apiserver/pkg/notice"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/policy"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/queue"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/redisx"
	genericoptions "github.com/onexstack/onexstack/pkg/options"
	"github.com/onexstack/onexstack/pkg/server"
	"github.com/onexstack/onexstack/pkg/store/registry"
	"gorm.io/gorm"
)

const serviceName = "rca-apiserver"

var errServerNotInitialized = errors.New("server is not initialized")

// Dependencies collects all components that need initialization but are not directly used
// by the main server struct during runtime (e.g., sidecar processes, cache warmers).
type Dependencies struct{}

// Config contains application-related configurations.
type Config struct {
	TLSOptions           *genericoptions.TLSOptions
	HTTPOptions          *genericoptions.HTTPOptions
	MySQLOptions         *genericoptions.MySQLOptions
	RedisOptions         redisx.RedisOptions
	AlertingIngestPolicy alertingingest.PolicyConfig
	AlertingRollout      alertingingest.RolloutConfig
	AlertingPolicy       alertingpolicy.ExternalPolicyOptions
	AIJobLongPoll        queue.AdaptiveWaiterOptions
	NoticeBaseURL        string
	MCPPolicy            policy.MCPPolicyConfig
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
	_ = metrics.Init(serviceName)
	if err := cfg.loadAlertingPolicy(); err != nil {
		return nil, err
	}

	alertingingest.SetRuntimeConfig(alertingingest.RuntimeConfig{
		Policy:  cfg.AlertingIngestPolicy,
		Rollout: cfg.AlertingRollout,
		Redis:   cfg.RedisOptions,
	})
	noticepkg.SetConfiguredNoticeBaseURL(cfg.NoticeBaseURL)
	noticepkg.SetNoticeDeliverySignalPublisher(nil)
	if err := cfg.configureNoticeDeliverySignalPublisher(ctx); err != nil {
		return nil, err
	}
	if err := cfg.configureOperatorReadCacheClient(ctx); err != nil {
		return nil, err
	}

	// Create the core server instance using dependency injection.
	// This relies on the wire-generated NewServer function.
	s, err := NewServer(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if s != nil && s.cfg != nil && s.cfg.Handler != nil {
		s.cfg.Handler.ConfigureMCPPolicy(cfg.MCPPolicy)
		s.cfg.Handler.ConfigureAdaptiveLongPollOptions(cfg.AIJobLongPoll)
	}

	return s.Prepare(ctx)
}

func (cfg *Config) loadAlertingPolicy() error {
	if cfg == nil {
		alertingpolicy.SetRuntimeConfig(alertingpolicy.DefaultRuntimeConfig())
		return nil
	}

	loadInput := alertingpolicy.ResolveLoadInput(cfg.AlertingPolicy)
	policyCfg, activeSource, loadErr := alertingpolicy.Load(loadInput.Path, loadInput.Strict)
	if loadErr != nil && loadInput.Strict {
		recordAlertingPolicyLoadMetric("error", loadInput.Source)
		slog.Error("alerting policy load failed",
			"policy_path", loadInput.Path,
			"strict", loadInput.Strict,
			"source", loadInput.Source,
			"err", loadErr,
		)
		return fmt.Errorf("strict alerting policy load failed: %w", loadErr)
	}

	result := "ok"
	if loadErr != nil {
		result = "error"
	}
	recordAlertingPolicyLoadMetric(result, loadInput.Source)
	if loadErr != nil {
		slog.Warn("alerting policy load fallback to default",
			"policy_path", loadInput.Path,
			"strict", loadInput.Strict,
			"source", loadInput.Source,
			"err", loadErr,
		)
	} else {
		slog.Info("alerting policy loaded",
			"policy_path", loadInput.Path,
			"strict", loadInput.Strict,
			"source", loadInput.Source,
			"err", "",
		)
	}

	alertingpolicy.SetRuntimeConfig(alertingpolicy.RuntimeConfig{
		Policy:       policyCfg,
		PolicyPath:   loadInput.Path,
		Strict:       loadInput.Strict,
		Source:       loadInput.Source,
		ActiveSource: activeSource,
	})
	return nil
}

func recordAlertingPolicyLoadMetric(result string, source string) {
	if metrics.M == nil {
		return
	}
	metrics.M.RecordAlertingPolicyLoad(result, source)
}

func (cfg *Config) configureNoticeDeliverySignalPublisher(ctx context.Context) error {
	if cfg == nil {
		return nil
	}
	opts := cfg.RedisOptions
	opts.ApplyDefaults()
	streamOpts := opts.Streams
	if !opts.StreamsEnabled() {
		return nil
	}

	client, err := redisx.NewClient(ctx, opts)
	if err != nil {
		if opts.FailOpen {
			slog.Error("notice delivery stream publisher init failed, fallback to db-only dispatch",
				"addr", opts.Addr,
				"capability", "streams",
				"fallback", true,
				"error", err,
			)
			return nil
		}
		return err
	}
	// Reuse the same redis client for read-side cache whenever notice stream client is available.
	cachex.ConfigureRedisClient(client)
	noticepkg.SetNoticeDeliverySignalPublisher(noticepkg.NewRedisNoticeDeliveryStream(
		client,
		noticepkg.RedisNoticeDeliveryStreamOptions{
			Enabled: true,
			Key:     streamOpts.NoticeDeliveryStream,
			Group:   streamOpts.ConsumerGroup,
		},
	))
	return nil
}

func (cfg *Config) configureOperatorReadCacheClient(ctx context.Context) error {
	if cfg == nil {
		cachex.ConfigureRedisClient(nil)
		return nil
	}
	if cachex.Enabled() {
		return nil
	}
	opts := cfg.RedisOptions
	opts.ApplyDefaults()
	if !opts.Enabled {
		cachex.ConfigureRedisClient(nil)
		return nil
	}
	client, err := redisx.NewClient(ctx, opts)
	if err != nil {
		if opts.FailOpen {
			slog.Error("operator read cache init failed, fallback to db-only read aggregation",
				"addr", opts.Addr,
				"capability", "read_cache",
				"fallback", true,
				"error", err,
			)
			cachex.ConfigureRedisClient(nil)
			return nil
		}
		return err
	}
	cachex.ConfigureRedisClient(client)
	return nil
}

// Prepare performs post-initialization tasks such as registering subscribers.
func (s *Server) Prepare(ctx context.Context) (*Server, error) {
	_ = metrics.Init(serviceName)
	return s, nil
}

// Run starts the server and listens for termination signals.
// It gracefully shuts down the server upon receiving a termination signal from the context.
func (s *Server) Run(ctx context.Context) error {
	if s == nil || s.srv == nil {
		return errServerNotInitialized
	}

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
	if s.cfg != nil && s.cfg.Handler != nil {
		if err := s.cfg.Handler.Close(); err != nil {
			slog.Warn("server component close failed", "component", "handler.biz", "error", err)
		}
	}
	if err := cachex.Close(); err != nil {
		slog.Warn("server component close failed", "component", "redis.read_cache", "error", err)
	}

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
