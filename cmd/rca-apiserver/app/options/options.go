package options

import (
	"errors"
	"net/url"
	"strings"
	"time"

	genericoptions "github.com/onexstack/onexstack/pkg/options"
	"github.com/spf13/pflag"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"

	"github.com/aiopsre/rca-api/internal/apiserver"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/policy"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/redisx"
)

var (
	errInvalidNoticeWorkerPollInterval = errors.New("noticeWorkerPollInterval must be > 0")
	errInvalidNoticeWorkerBatchSize    = errors.New("noticeWorkerBatchSize must be > 0")
	errInvalidNoticeWorkerLockTimeout  = errors.New("noticeWorkerLockTimeout must be > 0")
	errInvalidNoticeWorkerChannelConc  = errors.New("noticeWorkerChannelConcurrency must be > 0")
	errInvalidNoticeWorkerGlobalQPS    = errors.New("noticeWorkerGlobalQPS must be > 0")
	errInvalidNoticeBaseURL            = errors.New("noticeBaseURL must be valid http(s) url")
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
	// NoticeWorkerPollInterval controls worker polling interval.
	NoticeWorkerPollInterval time.Duration `json:"noticeWorkerPollInterval" mapstructure:"noticeWorkerPollInterval"`
	// NoticeWorkerBatchSize controls max deliveries claimed each run.
	NoticeWorkerBatchSize int `json:"noticeWorkerBatchSize" mapstructure:"noticeWorkerBatchSize"`
	// NoticeWorkerLockTimeout controls lock timeout recovery for claims.
	NoticeWorkerLockTimeout time.Duration `json:"noticeWorkerLockTimeout" mapstructure:"noticeWorkerLockTimeout"`
	// NoticeWorkerID identifies the worker instance in lock records.
	NoticeWorkerID string `json:"noticeWorkerID" mapstructure:"noticeWorkerID"`
	// NoticeWorkerChannelConcurrency controls per-channel max in-flight send count.
	NoticeWorkerChannelConcurrency int `json:"noticeWorkerChannelConcurrency" mapstructure:"noticeWorkerChannelConcurrency"`
	// NoticeWorkerGlobalQPS controls global send QPS token bucket for one worker process.
	NoticeWorkerGlobalQPS float64 `json:"noticeWorkerGlobalQPS" mapstructure:"noticeWorkerGlobalQPS"`
	// NoticeBaseURL is default links base_url when channel.baseURL is unset.
	NoticeBaseURL string `json:"noticeBaseURL" mapstructure:"noticeBaseURL"`
	// MCPPolicy configures per-tool MCP governance limits and enable switches.
	MCPPolicy policy.MCPPolicyConfig `json:"mcp" mapstructure:"mcp"`
}

// NewServerOptions creates a ServerOptions instance with default values.
func NewServerOptions() *ServerOptions {
	opts := &ServerOptions{
		TLSOptions:                     genericoptions.NewTLSOptions(),
		HTTPOptions:                    genericoptions.NewHTTPOptions(),
		MySQLOptions:                   genericoptions.NewMySQLOptions(),
		OTelOptions:                    genericoptions.NewOTelOptions(),
		RedisOptions:                   redisx.NewRedisOptions(),
		NoticeWorkerPollInterval:       1 * time.Second,
		NoticeWorkerBatchSize:          16,
		NoticeWorkerLockTimeout:        60 * time.Second,
		NoticeWorkerChannelConcurrency: 2,
		NoticeWorkerGlobalQPS:          20,
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
	fs.StringVar(&o.RedisOptions.Topic.AIJobQueueSignal, "redis.topic.ai_job_queue_signal", o.RedisOptions.Topic.AIJobQueueSignal, "Redis pub/sub topic for ai job queue wakeup.")
	fs.BoolVar(&o.RedisOptions.FailOpen, "redis.fail_open", o.RedisOptions.FailOpen, "Whether redis failures should fall back to DB watermark long poll.")
	fs.DurationVar(&o.NoticeWorkerPollInterval, "notice-worker-poll-interval", o.NoticeWorkerPollInterval, "Polling interval for notice worker.")
	fs.IntVar(&o.NoticeWorkerBatchSize, "notice-worker-batch-size", o.NoticeWorkerBatchSize, "Batch size per notice worker claim.")
	fs.DurationVar(&o.NoticeWorkerLockTimeout, "notice-worker-lock-timeout", o.NoticeWorkerLockTimeout, "Lock timeout before notice worker can reclaim deliveries.")
	fs.StringVar(&o.NoticeWorkerID, "notice-worker-id", o.NoticeWorkerID, "Worker instance id for notice worker lock ownership.")
	fs.IntVar(&o.NoticeWorkerChannelConcurrency, "notice-worker-channel-concurrency", o.NoticeWorkerChannelConcurrency, "Per-channel max in-flight sends.")
	fs.Float64Var(&o.NoticeWorkerGlobalQPS, "notice-worker-global-qps", o.NoticeWorkerGlobalQPS, "Global send QPS token bucket for one worker process.")
	fs.StringVar(&o.NoticeBaseURL, "notice-base-url", o.NoticeBaseURL, "Default base URL for notice links when channel.baseURL is empty.")
}

// Complete completes all the required options.
func (o *ServerOptions) Complete() error {
	// TODO: Add the completion logic if needed.
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
	if o.NoticeWorkerPollInterval <= 0 {
		errs = append(errs, errInvalidNoticeWorkerPollInterval)
	}
	if o.NoticeWorkerBatchSize <= 0 {
		errs = append(errs, errInvalidNoticeWorkerBatchSize)
	}
	if o.NoticeWorkerLockTimeout <= 0 {
		errs = append(errs, errInvalidNoticeWorkerLockTimeout)
	}
	if o.NoticeWorkerChannelConcurrency <= 0 {
		errs = append(errs, errInvalidNoticeWorkerChannelConc)
	}
	if o.NoticeWorkerGlobalQPS <= 0 {
		errs = append(errs, errInvalidNoticeWorkerGlobalQPS)
	}
	if !isValidNoticeBaseURL(o.NoticeBaseURL) {
		errs = append(errs, errInvalidNoticeBaseURL)
	}

	// Aggregate all errors and return them.
	return utilerrors.NewAggregate(errs)
}

// Config builds an apiserver.Config based on ServerOptions.
func (o *ServerOptions) Config() (*apiserver.Config, error) {
	redisOpts := o.RedisOptions
	redisOpts.ApplyDefaults()
	return &apiserver.Config{
		TLSOptions:    o.TLSOptions,
		HTTPOptions:   o.HTTPOptions,
		MySQLOptions:  o.MySQLOptions,
		RedisOptions:  redisOpts,
		NoticeBaseURL: strings.TrimSpace(o.NoticeBaseURL),
		MCPPolicy:     o.MCPPolicy,
	}, nil
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
