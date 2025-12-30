package options

import (
	"errors"
	"net/url"
	"strings"
	"time"

	genericoptions "github.com/onexstack/onexstack/pkg/options"
	"github.com/spf13/pflag"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"

	"zk8s.com/rca-api/internal/apiserver"
)

var (
	errInvalidNoticeWorkerPollInterval = errors.New("noticeWorkerPollInterval must be > 0")
	errInvalidNoticeWorkerBatchSize    = errors.New("noticeWorkerBatchSize must be > 0")
	errInvalidNoticeWorkerLockTimeout  = errors.New("noticeWorkerLockTimeout must be > 0")
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
	// NoticeWorkerPollInterval controls worker polling interval.
	NoticeWorkerPollInterval time.Duration `json:"noticeWorkerPollInterval" mapstructure:"noticeWorkerPollInterval"`
	// NoticeWorkerBatchSize controls max deliveries claimed each run.
	NoticeWorkerBatchSize int `json:"noticeWorkerBatchSize" mapstructure:"noticeWorkerBatchSize"`
	// NoticeWorkerLockTimeout controls lock timeout recovery for claims.
	NoticeWorkerLockTimeout time.Duration `json:"noticeWorkerLockTimeout" mapstructure:"noticeWorkerLockTimeout"`
	// NoticeWorkerID identifies the worker instance in lock records.
	NoticeWorkerID string `json:"noticeWorkerID" mapstructure:"noticeWorkerID"`
	// NoticeBaseURL is default links base_url when channel.baseURL is unset.
	NoticeBaseURL string `json:"noticeBaseURL" mapstructure:"noticeBaseURL"`
}

// NewServerOptions creates a ServerOptions instance with default values.
func NewServerOptions() *ServerOptions {
	opts := &ServerOptions{
		TLSOptions:               genericoptions.NewTLSOptions(),
		HTTPOptions:              genericoptions.NewHTTPOptions(),
		MySQLOptions:             genericoptions.NewMySQLOptions(),
		OTelOptions:              genericoptions.NewOTelOptions(),
		NoticeWorkerPollInterval: 1 * time.Second,
		NoticeWorkerBatchSize:    16,
		NoticeWorkerLockTimeout:  60 * time.Second,
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
	fs.DurationVar(&o.NoticeWorkerPollInterval, "notice-worker-poll-interval", o.NoticeWorkerPollInterval, "Polling interval for notice worker.")
	fs.IntVar(&o.NoticeWorkerBatchSize, "notice-worker-batch-size", o.NoticeWorkerBatchSize, "Batch size per notice worker claim.")
	fs.DurationVar(&o.NoticeWorkerLockTimeout, "notice-worker-lock-timeout", o.NoticeWorkerLockTimeout, "Lock timeout before notice worker can reclaim deliveries.")
	fs.StringVar(&o.NoticeWorkerID, "notice-worker-id", o.NoticeWorkerID, "Worker instance id for notice worker lock ownership.")
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
	if o.NoticeWorkerPollInterval <= 0 {
		errs = append(errs, errInvalidNoticeWorkerPollInterval)
	}
	if o.NoticeWorkerBatchSize <= 0 {
		errs = append(errs, errInvalidNoticeWorkerBatchSize)
	}
	if o.NoticeWorkerLockTimeout <= 0 {
		errs = append(errs, errInvalidNoticeWorkerLockTimeout)
	}
	if !isValidNoticeBaseURL(o.NoticeBaseURL) {
		errs = append(errs, errInvalidNoticeBaseURL)
	}

	// Aggregate all errors and return them.
	return utilerrors.NewAggregate(errs)
}

// Config builds an apiserver.Config based on ServerOptions.
func (o *ServerOptions) Config() (*apiserver.Config, error) {
	return &apiserver.Config{
		TLSOptions:    o.TLSOptions,
		HTTPOptions:   o.HTTPOptions,
		MySQLOptions:  o.MySQLOptions,
		NoticeBaseURL: strings.TrimSpace(o.NoticeBaseURL),
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
