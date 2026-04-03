package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	options := defaultRuntimeOptions()
	flag.StringVar(&options.ConfigPath, "config", options.ConfigPath, "Path to log alert rule config file")
	flag.BoolVar(&options.Once, "once", false, "Run a single tick and exit")
	flag.IntVar(&options.MaxTicks, "max-ticks", 0, "Run N ticks and exit (0 means keep running)")
	flag.IntVar(&options.TickSeconds, "tick-seconds", 0, "Override job tick interval seconds")
	flag.StringVar(&options.MetricsAddr, "metrics-addr", "", "Override metrics listen address")
	flag.Parse()

	cfg, err := loadConfig(options.ConfigPath)
	if err != nil {
		logger.Error("load config failed", slog.String("error", err.Error()))
		os.Exit(1)
	}

	if options.TickSeconds > 0 {
		cfg.Job.TickSeconds = options.TickSeconds
	}
	if metricsAddr := options.MetricsAddr; metricsAddr != "" {
		cfg.Job.MetricsAddr = metricsAddr
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	registry := prometheus.NewRegistry()
	metrics, err := newJobMetrics(registry)
	if err != nil {
		logger.Error("init metrics failed", slog.String("error", err.Error()))
		os.Exit(1)
	}

	metricsServer := startMetricsServer(ctx, cfg.Job.MetricsAddr, registry, logger)
	if metricsServer != nil {
		defer func() {
			shutdownContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			defer cancel()
			if shutdownErr := metricsServer.Shutdown(shutdownContext); shutdownErr != nil && !errors.Is(shutdownErr, http.ErrServerClosed) {
				logger.Warn("shutdown metrics server failed", slog.String("error", shutdownErr.Error()))
			}
		}()
	}

	runner := newJobRunner(
		cfg,
		newESClient(cfg.ES, metrics, logger),
		newWebhookClient(cfg.RCA),
		metrics,
		logger,
	)
	if err = runner.run(ctx, options); err != nil {
		logger.Error("run job failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func startMetricsServer(
	ctx context.Context,
	addr string,
	registry *prometheus.Registry,
	logger *slog.Logger,
) *http.Server {

	if addr == "" {
		return nil
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("ok"))
	})

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 2 * time.Second,
	}

	go func() {
		logger.Info("metrics server started", slog.String("addr", addr))
		if serveErr := server.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			logger.Error("metrics server failed", slog.String("error", serveErr.Error()))
		}
	}()

	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		if shutdownErr := server.Shutdown(shutdownContext); shutdownErr != nil && !errors.Is(shutdownErr, http.ErrServerClosed) {
			logger.Warn("shutdown metrics server failed", slog.String("error", shutdownErr.Error()))
		}
	}()

	return server
}
