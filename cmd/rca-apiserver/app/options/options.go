package options

import (
	"errors"
	"net/url"
	"strings"
	"time"

	genericoptions "github.com/aiopsre/rca-api/pkg/options"
	"github.com/spf13/pflag"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"

	"github.com/aiopsre/rca-api/internal/apiserver"
	alertingingest "github.com/aiopsre/rca-api/internal/apiserver/pkg/alerting/ingest"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/policy"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/queue"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/redisx"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/skillartifact"
)

var (
	errInvalidNoticeWorkerPollInterval = errors.New("noticeWorkerPollInterval must be > 0")
	errInvalidNoticeWorkerBatchSize    = errors.New("noticeWorkerBatchSize must be > 0")
	errInvalidNoticeWorkerLockTimeout  = errors.New("noticeWorkerLockTimeout must be > 0")
	errInvalidNoticeWorkerChannelConc  = errors.New("noticeWorkerChannelConcurrency must be > 0")
	errInvalidNoticeWorkerGlobalQPS    = errors.New("noticeWorkerGlobalQPS must be > 0")
	errInvalidNoticeWorkerChannelQPS   = errors.New("noticeWorkerChannelQPS must be >= 0")
	errInvalidNoticeWorkerRedisPrefix  = errors.New("noticeWorkerRedisRLKeyPrefix must not be empty")
	errInvalidNoticeWorkerRedisConcTTL = errors.New("noticeWorkerRedisConcTTL must be > 0")
	errInvalidNoticeWorkerRedisWinTTL  = errors.New("noticeWorkerRedisWindowTTL must be > 0")
	errInvalidNoticeBaseURL            = errors.New("noticeBaseURL must be valid http(s) url")
)

const (
	defaultNoticeWorkerGlobalQPS  = 20.0
	defaultNoticeWorkerChannelQPS = 0.0
)

// ServerOptions contains the configuration options for the server.
type ServerOptions struct {
	// TLSOptions contains the TLS configuration options.
	TLSOptions *genericoptions.TLSOptions `json:"tls" mapstructure:"tls"`
	// HTTPOptions contains the HTTP configuration options.
	HTTPOptions *genericoptions.HTTPOptions `json:"http" mapstructure:"http"`
	// MySQLOptions contains the MySQL configuration options.
	MySQLOptions *genericoptions.MySQLOptions `json:"coredb" mapstructure:"coredb"`
	// OTelOptions used to specify the otel options.
	OTelOptions *genericoptions.OTelOptions `json:"otel" mapstructure:"otel"`
	// RedisOptions configures redis pub/sub wakeup for long poll.
	RedisOptions redisx.RedisOptions `json:"redis" mapstructure:"redis"`
	// Alerting groups alerting-related runtime policy options.
	Alerting AlertingOptions `json:"alerting" mapstructure:"alerting"`
	// AIJobLongPoll configures adaptive long-poll waiter options for /v1/ai/jobs.
	AIJobLongPoll AIJobLongPollOptions `json:"ai_job_longpoll" mapstructure:"ai_job_longpoll"`
	// NoticeWorker configures notice-worker polling, limits, and redis limiter internals.
	NoticeWorker NoticeWorkerOptions `json:"notice_worker" mapstructure:"notice_worker"`
	// NoticeBaseURL is default links base_url when channel.baseURL is unset.
	NoticeBaseURL string `json:"noticeBaseURL" mapstructure:"noticeBaseURL"`
	// MCPPolicy configures per-tool MCP governance limits and enable switches.
	MCPPolicy policy.MCPPolicyConfig `json:"mcp" mapstructure:"mcp"`
	// SkillArtifact configures skill bundle upload and download artifact storage.
	SkillArtifact skillartifact.RuntimeConfig `json:"skill_artifact" mapstructure:"skill_artifact"`

	aiJobLongPollYAMLSet queue.AdaptiveWaiterOptionSet `json:"-" mapstructure:"-"`
	aiJobLongPollCLISet  queue.AdaptiveWaiterOptionSet `json:"-" mapstructure:"-"`
	resolvedLongPollOpts queue.AdaptiveWaiterOptions   `json:"-" mapstructure:"-"`
}

// AlertingOptions contains alert ingest policy controls.
type AlertingOptions struct {
	IngestPolicy alertingingest.PolicyConfig  `json:"ingest_policy" mapstructure:"ingest_policy"`
	Rollout      alertingingest.RolloutConfig `json:"rollout" mapstructure:"rollout"`
}

// AIJobLongPollOptions maps yaml/cli long-poll knobs to AdaptiveWaiterOptions.
type AIJobLongPollOptions struct {
	PollInterval         time.Duration `json:"poll_interval" mapstructure:"poll_interval"`
	WatermarkCacheTTL    time.Duration `json:"watermark_cache_ttl" mapstructure:"watermark_cache_ttl"`
	MaxPollingWaiters    int64         `json:"max_polling_waiters" mapstructure:"max_polling_waiters"`
	DBErrorWindow        int           `json:"db_error_window" mapstructure:"db_error_window"`
	DBErrorRateThreshold float64       `json:"db_error_rate_threshold" mapstructure:"db_error_rate_threshold"`
	DBErrorMinSamples    int           `json:"db_error_min_samples" mapstructure:"db_error_min_samples"`
}

func newAIJobLongPollOptions() AIJobLongPollOptions {
	defaults := queue.DefaultAdaptiveWaiterOptions()
	return AIJobLongPollOptions{
		PollInterval:         defaults.PollInterval,
		WatermarkCacheTTL:    defaults.WatermarkCacheTTL,
		MaxPollingWaiters:    defaults.MaxPollingWaiters,
		DBErrorWindow:        defaults.DBErrorWindow,
		DBErrorRateThreshold: defaults.DBErrorRateThreshold,
		DBErrorMinSamples:    defaults.DBErrorMinSamples,
	}
}

func (o AIJobLongPollOptions) toAdaptiveWaiterOptions() queue.AdaptiveWaiterOptions {
	return queue.AdaptiveWaiterOptions{
		PollInterval:         o.PollInterval,
		WatermarkCacheTTL:    o.WatermarkCacheTTL,
		MaxPollingWaiters:    o.MaxPollingWaiters,
		DBErrorWindow:        o.DBErrorWindow,
		DBErrorRateThreshold: o.DBErrorRateThreshold,
		DBErrorMinSamples:    o.DBErrorMinSamples,
	}
}

// NoticeWorkerRedisOptions configures redis-local knobs for notice-worker limiter.
type NoticeWorkerRedisOptions struct {
	KeyPrefix string        `json:"key_prefix" mapstructure:"key_prefix"`
	ConcTTL   time.Duration `json:"conc_ttl" mapstructure:"conc_ttl"`
	WindowTTL time.Duration `json:"window_ttl" mapstructure:"window_ttl"`
}

// NoticeWorkerOptions configures notice-worker runtime behavior.
type NoticeWorkerOptions struct {
	PollInterval       time.Duration            `json:"poll_interval" mapstructure:"poll_interval"`
	BatchSize          int                      `json:"batch_size" mapstructure:"batch_size"`
	LockTimeout        time.Duration            `json:"lock_timeout" mapstructure:"lock_timeout"`
	WorkerID           string                   `json:"worker_id" mapstructure:"worker_id"`
	ChannelConcurrency int                      `json:"channel_concurrency" mapstructure:"channel_concurrency"`
	GlobalQPS          float64                  `json:"global_qps" mapstructure:"global_qps"`
	ChannelQPS         float64                  `json:"channel_qps" mapstructure:"channel_qps"`
	Redis              NoticeWorkerRedisOptions `json:"redis" mapstructure:"redis"`
}

// NewServerOptions creates a ServerOptions instance with default values.
func NewServerOptions() *ServerOptions {
	opts := &ServerOptions{
		TLSOptions:   genericoptions.NewTLSOptions(),
		HTTPOptions:  genericoptions.NewHTTPOptions(),
		MySQLOptions: genericoptions.NewMySQLOptions(),
		OTelOptions:  genericoptions.NewOTelOptions(),
		RedisOptions: redisx.NewRedisOptions(),
		Alerting: AlertingOptions{
			IngestPolicy: alertingingest.DefaultPolicyConfig(),
			Rollout:      alertingingest.DefaultRolloutConfig(),
		},
		AIJobLongPoll: newAIJobLongPollOptions(),
		SkillArtifact: skillartifact.DefaultRuntimeConfig(),
		NoticeWorker: NoticeWorkerOptions{
			PollInterval:       1 * time.Second,
			BatchSize:          16,
			LockTimeout:        60 * time.Second,
			ChannelConcurrency: 2,
			GlobalQPS:          20,
			ChannelQPS:         0,
			Redis: NoticeWorkerRedisOptions{
				KeyPrefix: "rca:notice",
				ConcTTL:   60 * time.Second,
				WindowTTL: 2 * time.Second,
			},
		},
	}
	opts.HTTPOptions.Addr = ":5555"

	return opts
}

// AddFlags binds the options in ServerOptions to command-line flags.
func (o *ServerOptions) AddFlags(fs *pflag.FlagSet) {
	// Add command-line flags for sub-options.
	o.TLSOptions.AddFlags(fs, "tls")
	o.HTTPOptions.AddFlags(fs, "http")
	o.MySQLOptions.AddFlags(fs, "coredb")
	o.OTelOptions.AddFlags(fs, "otel")
	fs.BoolVar(&o.RedisOptions.Enabled, "redis.enabled", o.RedisOptions.Enabled, "Enable redis pub/sub wakeup bridge for ai job long polling.")
	fs.StringVar(&o.RedisOptions.Addr, "redis.addr", o.RedisOptions.Addr, "Redis address used by ai job wakeup bridge.")
	fs.IntVar(&o.RedisOptions.DB, "redis.db", o.RedisOptions.DB, "Redis db index used by ai job wakeup bridge.")
	fs.StringVar(&o.RedisOptions.Password, "redis.password", o.RedisOptions.Password, "Redis password used by ai job wakeup bridge.")
	fs.BoolVar(&o.RedisOptions.FailOpen, "redis.fail_open", o.RedisOptions.FailOpen, "Whether redis failures should fall back to mysql/local paths.")
	fs.BoolVar(&o.RedisOptions.PubSub.Enabled, "redis.pubsub.enabled", o.RedisOptions.PubSub.Enabled, "Enable redis pub/sub wakeup bridge for ai job long polling.")
	fs.StringVar(&o.RedisOptions.PubSub.TopicAIJobSignal, "redis.pubsub.topic_ai_job_signal", o.RedisOptions.PubSub.TopicAIJobSignal, "Redis pub/sub topic for ai job queue wakeup.")
	fs.BoolVar(&o.RedisOptions.Limiter.Enabled, "redis.limiter.enabled", o.RedisOptions.Limiter.Enabled, "Enable redis-backed notice worker limiter.")
	fs.StringVar(&o.RedisOptions.Limiter.Mode, "redis.limiter.mode", o.RedisOptions.Limiter.Mode, "Redis limiter mode: global|per_channel|both.")
	fs.Float64Var(&o.RedisOptions.Limiter.GlobalQPS, "redis.limiter.global_qps", o.RedisOptions.Limiter.GlobalQPS, "Redis limiter global qps profile.")
	fs.Float64Var(&o.RedisOptions.Limiter.ChannelQPS, "redis.limiter.channel_qps", o.RedisOptions.Limiter.ChannelQPS, "Redis limiter per-channel qps profile.")
	fs.IntVar(&o.RedisOptions.Limiter.Burst, "redis.limiter.burst", o.RedisOptions.Limiter.Burst, "Redis limiter burst profile.")
	fs.BoolVar(&o.RedisOptions.Streams.Enabled, "redis.streams.enabled", o.RedisOptions.Streams.Enabled, "Enable redis streams dispatch/consume for notice delivery.")
	fs.StringVar(&o.RedisOptions.Streams.NoticeDeliveryStream, "redis.streams.notice_delivery_stream", o.RedisOptions.Streams.NoticeDeliveryStream, "Redis stream key for notice delivery dispatch.")
	fs.StringVar(&o.RedisOptions.Streams.ConsumerGroup, "redis.streams.consumer_group", o.RedisOptions.Streams.ConsumerGroup, "Redis stream consumer group for notice workers.")
	fs.IntVar(&o.RedisOptions.Streams.ReclaimIdleSeconds, "redis.streams.reclaim_idle_seconds", o.RedisOptions.Streams.ReclaimIdleSeconds, "Idle seconds before reclaiming pending notice stream messages.")
	fs.BoolVar(&o.RedisOptions.Alerting.Enabled, "redis.alerting.enabled", o.RedisOptions.Alerting.Enabled, "Enable redis short-state backend for alerting ingest policy.")

	// Backward-compatible flags (R1/R2/R3 layout).
	fs.StringVar(&o.RedisOptions.Topic.AIJobQueueSignal, "redis.topic.ai_job_queue_signal", o.RedisOptions.Topic.AIJobQueueSignal, "Redis pub/sub topic for ai job queue wakeup.")
	fs.BoolVar(&o.RedisOptions.Streams.NoticeDelivery.Enabled, "redis.streams.notice_delivery.enabled", o.RedisOptions.Streams.NoticeDelivery.Enabled, "Enable redis streams dispatch for notice deliveries.")
	fs.StringVar(&o.RedisOptions.Streams.NoticeDelivery.Key, "redis.streams.notice_delivery.key", o.RedisOptions.Streams.NoticeDelivery.Key, "Redis stream key for notice delivery dispatch.")
	fs.StringVar(&o.RedisOptions.Streams.NoticeDelivery.Group, "redis.streams.notice_delivery.group", o.RedisOptions.Streams.NoticeDelivery.Group, "Redis stream consumer group for notice workers.")
	fs.IntVar(&o.RedisOptions.Streams.NoticeDelivery.ReclaimIdleSeconds, "redis.streams.notice_delivery.reclaim_idle_seconds", o.RedisOptions.Streams.NoticeDelivery.ReclaimIdleSeconds, "Idle seconds before reclaiming pending notice stream messages.")
	fs.IntVar(&o.Alerting.IngestPolicy.DedupWindowSeconds, "alerting.ingest_policy.dedup_window_seconds", o.Alerting.IngestPolicy.DedupWindowSeconds, "Dedup suppression window in seconds for alert ingest. 0 disables.")
	fs.IntVar(&o.Alerting.IngestPolicy.Burst.WindowSeconds, "alerting.ingest_policy.burst.window_seconds", o.Alerting.IngestPolicy.Burst.WindowSeconds, "Burst counter window in seconds for alert ingest. 0 disables.")
	fs.IntVar(&o.Alerting.IngestPolicy.Burst.Threshold, "alerting.ingest_policy.burst.threshold", o.Alerting.IngestPolicy.Burst.Threshold, "Burst threshold in one window for alert ingest. 0 disables.")
	fs.BoolVar(&o.Alerting.IngestPolicy.RedisBackend.Enabled, "alerting.ingest_policy.redis_backend.enabled", o.Alerting.IngestPolicy.RedisBackend.Enabled, "Enable redis backend for alert ingest dedup/burst short-state.")
	fs.StringVar(&o.Alerting.IngestPolicy.RedisBackend.KeyPrefix, "alerting.ingest_policy.redis_backend.key_prefix", o.Alerting.IngestPolicy.RedisBackend.KeyPrefix, "Redis key prefix for alert ingest policy state.")
	fs.BoolVar(&o.Alerting.Rollout.Enabled, "alerting.rollout.enabled", o.Alerting.Rollout.Enabled, "Enable adapter ingest rollout gating.")
	fs.StringSliceVar(&o.Alerting.Rollout.AllowedNamespaces, "alerting.rollout.allowed_namespaces", o.Alerting.Rollout.AllowedNamespaces, "Allowed namespaces for adapter ingest progression.")
	fs.StringSliceVar(&o.Alerting.Rollout.AllowedServices, "alerting.rollout.allowed_services", o.Alerting.Rollout.AllowedServices, "Allowed services for adapter ingest progression.")
	fs.StringVar(&o.Alerting.Rollout.Mode, "alerting.rollout.mode", o.Alerting.Rollout.Mode, "Adapter ingest rollout mode: observe|enforce.")
	fs.DurationVar(&o.AIJobLongPoll.PollInterval, "ai-job-longpoll-poll-interval", o.AIJobLongPoll.PollInterval, "Adaptive long-poll Level2 polling interval.")
	fs.DurationVar(&o.AIJobLongPoll.WatermarkCacheTTL, "ai-job-longpoll-watermark-cache-ttl", o.AIJobLongPoll.WatermarkCacheTTL, "Adaptive long-poll Level2 watermark cache ttl.")
	fs.Int64Var(&o.AIJobLongPoll.MaxPollingWaiters, "ai-job-longpoll-max-polling-waiters", o.AIJobLongPoll.MaxPollingWaiters, "Adaptive long-poll Level3 max waiters allowed for DB polling.")
	fs.IntVar(&o.AIJobLongPoll.DBErrorWindow, "ai-job-longpoll-db-error-window", o.AIJobLongPoll.DBErrorWindow, "Adaptive long-poll DB polling error-rate sliding window size.")
	fs.Float64Var(&o.AIJobLongPoll.DBErrorRateThreshold, "ai-job-longpoll-db-error-rate-threshold", o.AIJobLongPoll.DBErrorRateThreshold, "Adaptive long-poll DB polling error-rate threshold for Level3 self-protect.")
	fs.IntVar(&o.AIJobLongPoll.DBErrorMinSamples, "ai-job-longpoll-db-error-min-samples", o.AIJobLongPoll.DBErrorMinSamples, "Adaptive long-poll minimum DB polling samples before error-rate self-protect.")
	fs.DurationVar(&o.NoticeWorker.PollInterval, "notice-worker-poll-interval", o.NoticeWorker.PollInterval, "Polling interval for notice worker.")
	fs.IntVar(&o.NoticeWorker.BatchSize, "notice-worker-batch-size", o.NoticeWorker.BatchSize, "Batch size per notice worker claim.")
	fs.DurationVar(&o.NoticeWorker.LockTimeout, "notice-worker-lock-timeout", o.NoticeWorker.LockTimeout, "Lock timeout before notice worker can reclaim deliveries.")
	fs.StringVar(&o.NoticeWorker.WorkerID, "notice-worker-id", o.NoticeWorker.WorkerID, "Worker instance id for notice worker lock ownership.")
	fs.IntVar(&o.NoticeWorker.ChannelConcurrency, "notice-worker-channel-concurrency", o.NoticeWorker.ChannelConcurrency, "Per-channel max in-flight sends.")
	fs.Float64Var(&o.NoticeWorker.GlobalQPS, "notice-worker-global-qps", o.NoticeWorker.GlobalQPS, "Global send QPS token bucket for one worker process.")
	fs.Float64Var(&o.NoticeWorker.ChannelQPS, "notice-worker-channel-qps", o.NoticeWorker.ChannelQPS, "Per-channel send QPS limit (0 disables).")
	fs.StringVar(&o.NoticeWorker.Redis.KeyPrefix, "notice-worker-redis-rl-key-prefix", o.NoticeWorker.Redis.KeyPrefix, "Redis key prefix for notice global limiter.")
	fs.DurationVar(&o.NoticeWorker.Redis.ConcTTL, "notice-worker-redis-conc-ttl", o.NoticeWorker.Redis.ConcTTL, "Redis TTL for per-channel concurrency key.")
	fs.DurationVar(&o.NoticeWorker.Redis.WindowTTL, "notice-worker-redis-window-ttl", o.NoticeWorker.Redis.WindowTTL, "Redis TTL for per-second QPS window keys.")
	fs.StringVar(&o.NoticeBaseURL, "notice-base-url", o.NoticeBaseURL, "Default base URL for notice links when channel.baseURL is empty.")
	fs.StringVar(&o.SkillArtifact.Endpoint, "skill-artifact.endpoint", o.SkillArtifact.Endpoint, "S3-compatible endpoint for skill bundle storage.")
	fs.StringVar(&o.SkillArtifact.Bucket, "skill-artifact.bucket", o.SkillArtifact.Bucket, "Bucket name for skill bundle storage.")
	fs.StringVar(&o.SkillArtifact.AccessKey, "skill-artifact.access_key", o.SkillArtifact.AccessKey, "Access key for skill bundle storage.")
	fs.StringVar(&o.SkillArtifact.SecretKey, "skill-artifact.secret_key", o.SkillArtifact.SecretKey, "Secret key for skill bundle storage.")
	fs.StringVar(&o.SkillArtifact.Region, "skill-artifact.region", o.SkillArtifact.Region, "Region for skill bundle storage.")
	fs.BoolVar(&o.SkillArtifact.PathStyle, "skill-artifact.path_style", o.SkillArtifact.PathStyle, "Use path-style bucket lookup for skill bundle storage.")
	fs.BoolVar(&o.SkillArtifact.TLS, "skill-artifact.tls", o.SkillArtifact.TLS, "Use TLS when the skill artifact endpoint omits an explicit scheme.")
	fs.BoolVar(&o.SkillArtifact.SkipTLSVerify, "skill-artifact.skip_tls_verify", o.SkillArtifact.SkipTLSVerify, "Skip TLS certificate verification for skill artifact storage.")
	fs.BoolVar(&o.SkillArtifact.PrivateBucket, "skill-artifact.private_bucket", o.SkillArtifact.PrivateBucket, "Treat the skill artifact bucket as private and prefer presigned download URLs.")
	fs.StringVar(&o.SkillArtifact.ObjectKeyPattern, "skill-artifact.object_key_pattern", o.SkillArtifact.ObjectKeyPattern, "Object key pattern for uploaded skill bundles.")
	fs.StringVar(&o.SkillArtifact.UploadMode, "skill-artifact.upload_mode", o.SkillArtifact.UploadMode, "Skill artifact upload mode: manual_register|api_upload.")
	fs.StringVar(&o.SkillArtifact.DownloadMode, "skill-artifact.download_mode", o.SkillArtifact.DownloadMode, "Skill artifact download mode: direct_url|presigned_url.")
	fs.DurationVar(&o.SkillArtifact.PresignTTL, "skill-artifact.presign_ttl", o.SkillArtifact.PresignTTL, "Presigned URL ttl for private skill bundle downloads.")
}

// Complete completes all the required options.
func (o *ServerOptions) Complete() error {
	o.RedisOptions.ApplyDefaults()
	o.resolveAdaptiveLongPollOptions()

	// Keep notice-worker as source-of-truth for runtime limiter values while allowing
	// redis.limiter profile to override default notice-worker values.
	if o.NoticeWorker.GlobalQPS == defaultNoticeWorkerGlobalQPS && o.RedisOptions.Limiter.GlobalQPS > 0 {
		o.NoticeWorker.GlobalQPS = o.RedisOptions.Limiter.GlobalQPS
	}
	if o.NoticeWorker.ChannelQPS == defaultNoticeWorkerChannelQPS && o.RedisOptions.Limiter.ChannelQPS >= 0 {
		o.NoticeWorker.ChannelQPS = o.RedisOptions.Limiter.ChannelQPS
	}

	return nil
}

// Validate checks whether the options in ServerOptions are valid.
func (o *ServerOptions) Validate() error {
	errs := []error{}

	// Validate sub-options.
	errs = append(errs, o.TLSOptions.Validate()...)
	errs = append(errs, o.HTTPOptions.Validate()...)
	errs = append(errs, o.MySQLOptions.Validate()...)
	errs = append(errs, o.OTelOptions.Validate()...)
	if err := o.RedisOptions.Validate(); err != nil {
		errs = append(errs, err)
	}
	o.Alerting.IngestPolicy.ApplyDefaults()
	o.Alerting.Rollout.ApplyDefaults()
	errs = append(errs, o.validateNoticeWorkerOptions()...)
	if !isValidNoticeBaseURL(o.NoticeBaseURL) {
		errs = append(errs, errInvalidNoticeBaseURL)
	}
	if err := validateSkillArtifactOptions(o.SkillArtifact); err != nil {
		errs = append(errs, err)
	}

	// Aggregate all errors and return them.
	return utilerrors.NewAggregate(errs)
}

func (o *ServerOptions) validateNoticeWorkerOptions() []error {
	errs := make([]error, 0, 9)
	if o.NoticeWorker.PollInterval <= 0 {
		errs = append(errs, errInvalidNoticeWorkerPollInterval)
	}
	if o.NoticeWorker.BatchSize <= 0 {
		errs = append(errs, errInvalidNoticeWorkerBatchSize)
	}
	if o.NoticeWorker.LockTimeout <= 0 {
		errs = append(errs, errInvalidNoticeWorkerLockTimeout)
	}
	if o.NoticeWorker.ChannelConcurrency <= 0 {
		errs = append(errs, errInvalidNoticeWorkerChannelConc)
	}
	if o.NoticeWorker.GlobalQPS <= 0 {
		errs = append(errs, errInvalidNoticeWorkerGlobalQPS)
	}
	if o.NoticeWorker.ChannelQPS < 0 {
		errs = append(errs, errInvalidNoticeWorkerChannelQPS)
	}
	o.NoticeWorker.Redis.KeyPrefix = strings.TrimSpace(o.NoticeWorker.Redis.KeyPrefix)
	if o.NoticeWorker.Redis.KeyPrefix == "" {
		errs = append(errs, errInvalidNoticeWorkerRedisPrefix)
	}
	if o.NoticeWorker.Redis.ConcTTL <= 0 {
		errs = append(errs, errInvalidNoticeWorkerRedisConcTTL)
	}
	if o.NoticeWorker.Redis.WindowTTL <= 0 {
		errs = append(errs, errInvalidNoticeWorkerRedisWinTTL)
	}
	return errs
}

// Config builds an apiserver.Config based on ServerOptions.
func (o *ServerOptions) Config() (*apiserver.Config, error) {
	o.resolveAdaptiveLongPollOptions()
	redisOpts := o.RedisOptions
	redisOpts.ApplyDefaults()
	return &apiserver.Config{
		TLSOptions:           o.TLSOptions,
		HTTPOptions:          o.HTTPOptions,
		MySQLOptions:         o.MySQLOptions,
		RedisOptions:         redisOpts,
		AlertingIngestPolicy: o.Alerting.IngestPolicy,
		AlertingRollout:      o.Alerting.Rollout,
		AIJobLongPoll:        o.resolvedLongPollOpts,
		NoticeBaseURL:        strings.TrimSpace(o.NoticeBaseURL),
		MCPPolicy:            o.MCPPolicy,
		SkillArtifact:        o.SkillArtifact,
	}, nil
}

// MarkCLIFlagOverrides marks which options are explicitly set by CLI flags.
func (o *ServerOptions) MarkCLIFlagOverrides(overrides map[string]string) {
	if o == nil || len(overrides) == 0 {
		return
	}
	_, o.aiJobLongPollCLISet.PollInterval = overrides["ai-job-longpoll-poll-interval"]
	_, o.aiJobLongPollCLISet.WatermarkCacheTTL = overrides["ai-job-longpoll-watermark-cache-ttl"]
	_, o.aiJobLongPollCLISet.MaxPollingWaiters = overrides["ai-job-longpoll-max-polling-waiters"]
	_, o.aiJobLongPollCLISet.DBErrorWindow = overrides["ai-job-longpoll-db-error-window"]
	_, o.aiJobLongPollCLISet.DBErrorRateThreshold = overrides["ai-job-longpoll-db-error-rate-threshold"]
	_, o.aiJobLongPollCLISet.DBErrorMinSamples = overrides["ai-job-longpoll-db-error-min-samples"]
}

// MarkConfigFileOverrides marks adaptive long-poll keys explicitly configured in yaml.
func (o *ServerOptions) MarkConfigFileOverrides(inConfig func(string) bool) {
	if o == nil || inConfig == nil {
		return
	}
	o.aiJobLongPollYAMLSet.PollInterval = inConfig("ai_job_longpoll.poll_interval")
	o.aiJobLongPollYAMLSet.WatermarkCacheTTL = inConfig("ai_job_longpoll.watermark_cache_ttl")
	o.aiJobLongPollYAMLSet.MaxPollingWaiters = inConfig("ai_job_longpoll.max_polling_waiters")
	o.aiJobLongPollYAMLSet.DBErrorWindow = inConfig("ai_job_longpoll.db_error_window")
	o.aiJobLongPollYAMLSet.DBErrorRateThreshold = inConfig("ai_job_longpoll.db_error_rate_threshold")
	o.aiJobLongPollYAMLSet.DBErrorMinSamples = inConfig("ai_job_longpoll.db_error_min_samples")
}

func (o *ServerOptions) resolveAdaptiveLongPollOptions() {
	if o == nil {
		return
	}
	yamlOpts := o.AIJobLongPoll.toAdaptiveWaiterOptions()
	o.resolvedLongPollOpts = queue.ResolveAdaptiveWaiterOptions(
		yamlOpts,
		o.aiJobLongPollYAMLSet,
		yamlOpts,
		o.aiJobLongPollCLISet,
	)
}

func isValidNoticeBaseURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true
	}
	parsed, err := url.ParseRequestURI(raw)
	if err != nil {
		return false
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	return (scheme == "http" || scheme == "https") && strings.TrimSpace(parsed.Host) != ""
}

func validateSkillArtifactOptions(cfg skillartifact.RuntimeConfig) error {
	normalized := skillartifact.DefaultRuntimeConfig()
	if strings.TrimSpace(cfg.Endpoint) != "" {
		normalized.Endpoint = strings.TrimSpace(cfg.Endpoint)
	}
	if strings.TrimSpace(cfg.Bucket) != "" {
		normalized.Bucket = strings.TrimSpace(cfg.Bucket)
	}
	if strings.TrimSpace(cfg.AccessKey) != "" {
		normalized.AccessKey = strings.TrimSpace(cfg.AccessKey)
	}
	if strings.TrimSpace(cfg.SecretKey) != "" {
		normalized.SecretKey = strings.TrimSpace(cfg.SecretKey)
	}
	if strings.TrimSpace(cfg.Region) != "" {
		normalized.Region = strings.TrimSpace(cfg.Region)
	}
	if strings.TrimSpace(cfg.ObjectKeyPattern) != "" {
		normalized.ObjectKeyPattern = strings.TrimSpace(cfg.ObjectKeyPattern)
	}
	if strings.TrimSpace(cfg.UploadMode) != "" {
		normalized.UploadMode = strings.ToLower(strings.TrimSpace(cfg.UploadMode))
	}
	if strings.TrimSpace(cfg.DownloadMode) != "" {
		normalized.DownloadMode = strings.ToLower(strings.TrimSpace(cfg.DownloadMode))
	}
	if cfg.PresignTTL > 0 {
		normalized.PresignTTL = cfg.PresignTTL
	}
	normalized.PathStyle = cfg.PathStyle
	normalized.TLS = cfg.TLS
	normalized.SkipTLSVerify = cfg.SkipTLSVerify
	normalized.PrivateBucket = cfg.PrivateBucket

	hasAny := normalized.Endpoint != "" || normalized.Bucket != "" || normalized.AccessKey != "" || normalized.SecretKey != ""
	if !hasAny {
		return nil
	}
	if normalized.Endpoint == "" || normalized.Bucket == "" || normalized.AccessKey == "" || normalized.SecretKey == "" {
		return errors.New("skill_artifact requires endpoint, bucket, access_key, secret_key")
	}
	if !isValidHTTPBaseURLLike(normalized.Endpoint) {
		return errors.New("skill_artifact.endpoint must be valid http(s) url or host:port")
	}
	switch normalized.UploadMode {
	case "manual_register", "api_upload":
	default:
		return errors.New("skill_artifact.upload_mode must be manual_register or api_upload")
	}
	switch normalized.DownloadMode {
	case "direct_url", "presigned_url":
	default:
		return errors.New("skill_artifact.download_mode must be direct_url or presigned_url")
	}
	if strings.TrimSpace(normalized.ObjectKeyPattern) == "" {
		return errors.New("skill_artifact.object_key_pattern must not be empty")
	}
	if normalized.PresignTTL <= 0 {
		return errors.New("skill_artifact.presign_ttl must be > 0")
	}
	return nil
}

func isValidHTTPBaseURLLike(raw string) bool {
	value := strings.TrimSpace(raw)
	if value == "" {
		return false
	}
	if !strings.Contains(value, "://") {
		value = "http://" + value
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil {
		return false
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	return (scheme == "http" || scheme == "https") && strings.TrimSpace(parsed.Host) != ""
}
