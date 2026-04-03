package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/onexstack/onexstack/pkg/cli/cli"
	"github.com/onexstack/onexstack/pkg/core"
	"github.com/onexstack/onexstack/pkg/version"
	"github.com/redis/go-redis/v9"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	genericapiserver "k8s.io/apiserver/pkg/server"

	"github.com/aiopsre/rca-api/cmd/rca-apiserver/app/options"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/metrics"
	noticepkg "github.com/aiopsre/rca-api/internal/apiserver/pkg/notice"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/orchestratorregistry"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/redisx"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
)

const (
	// defaultHomeDir defines the default directory to store configuration files
	// for the rca-apiserver service, typically within the user's home directory.
	defaultHomeDir = ".rca-api"

	// defaultConfigName specifies the default configuration file name
	// for the rca-apiserver service.
	defaultConfigName = "rca-apiserver.yaml"
)

// configFile stores the path to the configuration file, set via command-line flag.
var configFile string

// NewAPIServerCommand creates a new *cobra.Command object that represents
// the root command for the rca-apiserver application. It sets up command-line
// flags, configuration loading, and the main execution logic.
func NewAPIServerCommand() *cobra.Command {
	opts := options.NewServerOptions() // Create default application command-line options

	cmd := &cobra.Command{
		// Specify the name of the command, which will appear in the help information
		Use: "rca-apiserver",
		// A short description of the command.
		Short: "RCA platform API for K8s microservices",
		// A detailed description of the command.
		Long: "rca-api: Ingest alerts, collect evidence (K8s/Prom/ES), run diagnosis (rules -> multi-agent), and support approve-based remediation actions with audit.",
		// SilenceUsage ensures that the help message is not printed when an error occurs.
		SilenceUsage: true,
		// RunE defines the function to execute when cmd.Execute() is called.
		RunE: runWithPreparedOptions(opts, run),

		// Args ensures no command-line arguments are allowed, e.g., './rca-apiserver param1'.
		Args: cobra.NoArgs,
	}

	// Register the configuration initialization function, which runs before command execution.
	// It sets up Viper to search for configuration files in specified directories.
	cobra.OnInitialize(core.OnInitialize(&configFile, "RCA_API_APISERVER", cli.SearchDirs(defaultHomeDir), defaultConfigName))

	// Define persistent flags that apply to this command and its subcommands.
	cmd.PersistentFlags().StringVarP(
		&configFile,
		"config",
		"c",
		cli.FilePath(defaultHomeDir, defaultConfigName),
		"Path to the rca-apiserver configuration file.",
	)

	// Add server-specific options as command-line flags.
	opts.AddFlags(cmd.PersistentFlags())

	// Add the standard --version flag to the command.
	version.AddFlags(cmd.PersistentFlags())
	cmd.AddCommand(newNoticeWorkerCommand(opts))

	return cmd
}

// run contains the main logic for initializing and running the server.
func run(ctx context.Context, opts *options.ServerOptions) error {
	logAPIServerRedisProfile(opts)
	redisClient, err := ensureRedisStrongDependency(ctx, opts)
	if err != nil {
		return err
	}
	if err := orchestratorregistry.ConfigureRedisClient(redisClient); err != nil {
		_ = redisClient.Close()
		return fmt.Errorf("redis strong dependency check failed: %w", err)
	}

	// Retrieve the application configuration from the parsed options.
	cfg, err := opts.Config()
	if err != nil {
		_ = redisClient.Close()
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Create a new server instance based on the configuration.
	server, err := cfg.New(ctx)
	if err != nil {
		_ = redisClient.Close()
		return fmt.Errorf("failed to create server: %w", err)
	}

	// Run the server until the context is canceled or an error occurs.
	defer func() { _ = redisClient.Close() }()
	return server.Run(ctx)
}

func ensureRedisStrongDependency(ctx context.Context, opts *options.ServerOptions) (*redis.Client, error) {
	if opts == nil {
		return nil, fmt.Errorf("redis strong dependency check failed: nil options")
	}

	redisOpts := opts.RedisOptions
	redisOpts.ApplyDefaults()
	if !redisOpts.Enabled {
		return nil, fmt.Errorf("redis strong dependency check failed: redis.enabled must be true")
	}

	redisClient, err := redisx.NewClient(ctx, redisOpts)
	if err != nil {
		return nil, fmt.Errorf("redis strong dependency check failed: %w", err)
	}
	return redisClient, nil
}

func newNoticeWorkerCommand(opts *options.ServerOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:          "notice-worker",
		Short:        "Run notice delivery worker (DB outbox retry)",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE:         runWithPreparedOptions(opts, runNoticeWorker),
	}
	return cmd
}

//nolint:gocognit // Startup wiring intentionally keeps validation/setup order explicit.
func runWithPreparedOptions(
	opts *options.ServerOptions,
	runner func(context.Context, *options.ServerOptions) error,
) func(*cobra.Command, []string) error {

	return func(cmd *cobra.Command, args []string) error {
		// Setup a context that listens for OS signals (e.g., Ctrl+C) for graceful shutdown.
		ctx := genericapiserver.SetupSignalContext()

		// Check if the --version flag was requested. If so, print version info and exit.
		version.PrintAndExitIfRequested()

		// Bind parsed CLI flags into viper so command-line values override config defaults.
		if err := viper.BindPFlags(cmd.Flags()); err != nil {
			return fmt.Errorf("failed to bind command flags: %w", err)
		}
		cliOverrides := captureSetFlags(cmd.Flags())

		// Unmarshal the configuration from Viper into the ServerOptions struct.
		if err := viper.Unmarshal(opts); err != nil {
			return fmt.Errorf("failed to unmarshal configuration: %w", err)
		}
		opts.MarkConfigFileOverrides(func(key string) bool {
			return viper.InConfig(key)
		})
		if err := applyFlagOverrides(cmd.Flags(), cliOverrides); err != nil {
			return err
		}
		opts.MarkCLIFlagOverrides(cliOverrides)

		// Complete the options by setting default values and deriving configurations.
		if err := opts.Complete(); err != nil {
			return fmt.Errorf("failed to complete options: %w", err)
		}

		// Validate the command-line options to ensure they are valid.
		if err := opts.Validate(); err != nil {
			return fmt.Errorf("invalid options: %w", err)
		}

		// Initialize and configure OpenTelemetry providers based on enabled signals.
		if err := opts.OTelOptions.Apply(); err != nil {
			return err
		}
		// Ensure OpenTelemetry resources are properly cleaned up on application shutdown.
		defer func() { _ = opts.OTelOptions.Shutdown(ctx) }()

		return runner(ctx, opts)
	}
}

func captureSetFlags(flags *pflag.FlagSet) map[string]string {
	overrides := make(map[string]string)
	if flags == nil {
		return overrides
	}
	flags.Visit(func(flag *pflag.Flag) {
		overrides[flag.Name] = flag.Value.String()
	})
	return overrides
}

func applyFlagOverrides(flags *pflag.FlagSet, overrides map[string]string) error {
	if flags == nil || len(overrides) == 0 {
		return nil
	}
	for name, value := range overrides {
		if err := flags.Set(name, value); err != nil {
			return fmt.Errorf("failed to re-apply command flag %s: %w", name, err)
		}
	}
	return nil
}

func runNoticeWorker(ctx context.Context, opts *options.ServerOptions) error {
	cfg, err := opts.Config()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}
	db, err := cfg.NewDB()
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	st := store.NewStore(db)
	_ = metrics.Init("notice-worker")

	redisOpts := opts.RedisOptions
	redisOpts.ApplyDefaults()
	limiterMode := effectiveLimiterMode(redisOpts.Limiter.Mode)
	limiterGlobalQPS, limiterChannelQPS := resolveNoticeLimiterQPS(opts)
	consumerID := workerIDForLog(opts.NoticeWorker.WorkerID)

	var redisClientClose func() error
	limiter, err := buildNoticeLimiter(ctx, opts, &redisClientClose)
	if err != nil {
		return fmt.Errorf("failed to initialize notice limiter: %w", err)
	}
	if redisClientClose != nil {
		defer func() { _ = redisClientClose() }()
	}

	var streamClientClose func() error
	streamConsumer, reclaimIdle, err := buildNoticeStreamConsumer(ctx, opts, &streamClientClose)
	if err != nil {
		return fmt.Errorf("failed to initialize notice stream consumer: %w", err)
	}
	if streamClientClose != nil {
		defer func() { _ = streamClientClose() }()
	}

	worker := noticepkg.NewWorker(st, noticepkg.WorkerOptions{
		WorkerID:              opts.NoticeWorker.WorkerID,
		BatchSize:             opts.NoticeWorker.BatchSize,
		PollInterval:          opts.NoticeWorker.PollInterval,
		LockTimeout:           opts.NoticeWorker.LockTimeout,
		PerChannelConcurrency: opts.NoticeWorker.ChannelConcurrency,
		GlobalQPS:             opts.NoticeWorker.GlobalQPS,
		Limiter:               limiter,
		StreamConsumer:        streamConsumer,
		StreamReclaimIdle:     reclaimIdle,
	})

	slog.Info("notice worker redis profile",
		"redis_enabled", redisOpts.Enabled,
		"redis_fail_open", redisOpts.FailOpen,
		"streams_enabled", redisOpts.StreamsEnabled(),
		"streams_group", strings.TrimSpace(redisOpts.Streams.ConsumerGroup),
		"streams_consumer_id", consumerID,
		"limiter_enabled", redisOpts.LimiterEnabled(),
		"limiter_mode", limiterMode,
		"limiter_global_qps", limiterGlobalQPS,
		"limiter_channel_qps", limiterChannelQPS,
		"fallback_strategy", "streams->db_claim,limiter->local",
	)
	slog.Info("notice worker started",
		"worker_id", workerIDForLog(opts.NoticeWorker.WorkerID),
		"batch_size", opts.NoticeWorker.BatchSize,
		"poll_interval", opts.NoticeWorker.PollInterval.String(),
		"lock_timeout", opts.NoticeWorker.LockTimeout.String(),
		"channel_concurrency", opts.NoticeWorker.ChannelConcurrency,
		"global_qps", opts.NoticeWorker.GlobalQPS,
		"channel_qps", opts.NoticeWorker.ChannelQPS,
		"redis_enabled", redisOpts.Enabled,
		"redis_fail_open", redisOpts.FailOpen,
		"redis_rl_prefix", strings.TrimSpace(opts.NoticeWorker.Redis.KeyPrefix),
		"redis_stream_enabled", redisOpts.StreamsEnabled(),
		"redis_stream_key", strings.TrimSpace(redisOpts.Streams.NoticeDeliveryStream),
		"redis_stream_group", strings.TrimSpace(redisOpts.Streams.ConsumerGroup),
		"redis_stream_reclaim_idle_seconds", redisOpts.Streams.ReclaimIdleSeconds,
	)
	defer slog.Info("notice worker exited")

	return worker.Run(ctx)
}

func buildNoticeLimiter(
	ctx context.Context,
	opts *options.ServerOptions,
	redisClientClose *func() error,
) (noticepkg.NoticeRateLimiter, error) {

	redisOpts := opts.RedisOptions
	redisOpts.ApplyDefaults()
	limiterMode := effectiveLimiterMode(redisOpts.Limiter.Mode)
	globalQPS, channelQPS := resolveNoticeLimiterQPS(opts)
	switch limiterMode {
	case "global":
		channelQPS = 0
	case "per_channel":
		globalQPS = 0
	}
	limiterOpts := noticepkg.RedisRateLimiterOptions{
		Enabled:               redisOpts.LimiterEnabled(),
		FailOpen:              redisOpts.FailOpen,
		KeyPrefix:             opts.NoticeWorker.Redis.KeyPrefix,
		GlobalQPS:             globalQPS,
		PerChannelQPS:         channelQPS,
		PerChannelConcurrency: opts.NoticeWorker.ChannelConcurrency,
		WindowTTL:             opts.NoticeWorker.Redis.WindowTTL,
		ConcurrencyTTL:        opts.NoticeWorker.Redis.ConcTTL,
	}

	if !redisOpts.LimiterEnabled() {
		return noticepkg.NewRedisRateLimiter(nil, limiterOpts), nil
	}

	client, err := redisx.NewClient(ctx, redisOpts)
	if err != nil {
		if redisOpts.FailOpen {
			slog.Error("notice worker redis init failed, fallback to local limiter",
				"addr", redisOpts.Addr,
				"capability", "limiter",
				"fallback", true,
				"error", err,
			)
			return noticepkg.NewRedisRateLimiter(nil, limiterOpts), nil
		}
		return nil, err
	}

	if redisClientClose != nil {
		*redisClientClose = client.Close
	}
	return noticepkg.NewRedisRateLimiter(client, limiterOpts), nil
}

func buildNoticeStreamConsumer(
	ctx context.Context,
	opts *options.ServerOptions,
	redisClientClose *func() error,
) (noticepkg.NoticeDeliveryStreamConsumer, time.Duration, error) {

	noopConsumer := noticepkg.NoopNoticeDeliveryStreamConsumer{}
	if opts == nil {
		return noopConsumer, 0, nil
	}
	redisOpts := opts.RedisOptions
	redisOpts.ApplyDefaults()
	streamOpts := redisOpts.Streams
	reclaimIdle := time.Duration(streamOpts.ReclaimIdleSeconds) * time.Second
	if reclaimIdle <= 0 {
		reclaimIdle = time.Duration(redisx.DefaultNoticeDeliveryReclaimIdleSeconds) * time.Second
	}
	if !redisOpts.StreamsEnabled() {
		return noopConsumer, reclaimIdle, nil
	}

	client, err := redisx.NewClient(ctx, redisOpts)
	if err != nil {
		if redisOpts.FailOpen {
			slog.Error("notice worker redis streams init failed, fallback to db claim mode",
				"addr", redisOpts.Addr,
				"capability", "streams",
				"fallback", true,
				"error", err,
			)
			return noopConsumer, reclaimIdle, nil
		}
		return nil, reclaimIdle, err
	}

	stream := noticepkg.NewRedisNoticeDeliveryStream(client, noticepkg.RedisNoticeDeliveryStreamOptions{
		Enabled: true,
		Key:     streamOpts.NoticeDeliveryStream,
		Group:   streamOpts.ConsumerGroup,
	})
	if redisClientClose != nil {
		*redisClientClose = stream.Close
	}
	return stream, reclaimIdle, nil
}

func workerIDForLog(id string) string {
	if id == "" {
		return "auto"
	}
	return id
}

func logAPIServerRedisProfile(opts *options.ServerOptions) {
	if opts == nil {
		return
	}
	redisOpts := opts.RedisOptions
	redisOpts.ApplyDefaults()
	limiterMode := effectiveLimiterMode(redisOpts.Limiter.Mode)
	limiterGlobalQPS, limiterChannelQPS := resolveNoticeLimiterQPS(opts)

	slog.Info("rca-apiserver redis profile",
		"enabled", redisOpts.Enabled,
		"fail_open", redisOpts.FailOpen,
		"pubsub_enabled", redisOpts.PubSubEnabled(),
		"pubsub_topic", strings.TrimSpace(redisOpts.PubSub.TopicAIJobSignal),
		"streams_enabled", redisOpts.StreamsEnabled(),
		"streams_stream", strings.TrimSpace(redisOpts.Streams.NoticeDeliveryStream),
		"limiter_enabled", redisOpts.LimiterEnabled(),
		"limiter_mode", limiterMode,
		"limiter_global_qps", limiterGlobalQPS,
		"limiter_channel_qps", limiterChannelQPS,
	)
}

func resolveNoticeLimiterQPS(opts *options.ServerOptions) (float64, float64) {
	return opts.NoticeWorker.GlobalQPS, opts.NoticeWorker.ChannelQPS
}

func effectiveLimiterMode(raw string) string {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "global":
		return "global"
	case "per_channel":
		return "per_channel"
	default:
		return "both"
	}
}
