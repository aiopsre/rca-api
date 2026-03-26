package apiserver

import (
	"context"
	"net/http"

	"github.com/aiopsre/rca-api/pkg/server"
	"github.com/gin-contrib/pprof"
	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"
	genericmw "github.com/onexstack/onexstack/pkg/middleware/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"

	"github.com/aiopsre/rca-api/internal/pkg/errno"
	mw "github.com/aiopsre/rca-api/internal/pkg/middleware/gin"
)

// ginServer implements the server.Server interface using the Gin framework.
type ginServer struct {
	srv server.Server
}

// Ensure *ginServer implements the server.Server interface.
var _ server.Server = (*ginServer)(nil)

// NewGinServer initializes and returns a new HTTP server based on Gin.
// It returns the server.Server interface to abstract implementation details.
func (c *ServerConfig) NewGinServer() (server.Server, error) {
	engine := gin.New()

	engine.Use(
		gin.Recovery(),
		mw.NoCache,
		mw.Cors(mw.DefaultCorsConfig()),
		mw.Secure,
		mw.RequestIDMiddleware(),
		otelgin.Middleware(serviceName, otelgin.WithFilter(shouldRecordTelemetry)),
		genericmw.Observability(),
		mw.Context(),
	)

	c.InstallRESTAPI(engine)

	httpsrv := server.NewHTTPServer(c.HTTPOptions, c.TLSOptions, engine)

	return &ginServer{srv: httpsrv}, nil
}

// InstallRESTAPI registers all RESTful API routes to the engine.
func (c *ServerConfig) InstallRESTAPI(engine *gin.Engine) {
	InstallGenericAPI(engine)

	engine.GET("/healthz", c.Handler.Healthz)

	v1 := engine.Group("/v1")
	c.Handler.ApplyTo(v1)
}

// InstallGenericAPI registers non-business logic routes such as pprof, metrics, and 404 handlers.
func InstallGenericAPI(engine *gin.Engine) {
	pprof.Register(engine)

	// Expose /metrics endpoint for Prometheus scraping.
	engine.GET("/metrics", gin.WrapH(promhttp.HandlerFor(
		prometheus.DefaultGatherer,
		promhttp.HandlerOpts{
			EnableOpenMetrics: true,
		},
	)))

	// Register 404 handler.
	engine.NoRoute(func(c *gin.Context) {
		core.WriteResponse(c, errno.ErrPageNotFound, nil)
	})
}

// RunOrDie starts the Gin server and panics if startup fails.
func (s *ginServer) RunOrDie(ctx context.Context) {
	s.srv.RunOrDie(ctx)
}

// GracefulStop gracefully shuts down the server.
func (s *ginServer) GracefulStop(ctx context.Context) {
	s.srv.GracefulStop(ctx)
}

// shouldRecordTelemetry filters out paths that shouldn't generate traces.
func shouldRecordTelemetry(r *http.Request) bool {
	return r.URL.Path != "/metrics"
}
