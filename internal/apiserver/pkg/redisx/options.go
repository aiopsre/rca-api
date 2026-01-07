package redisx

import (
	"errors"
	"strings"
)

const (
	// DefaultAddr is the default redis address for apiserver wakeup signaling.
	DefaultAddr = "192.168.39.2:6379"
	// DefaultDB is the default redis db index.
	DefaultDB = 0
	// DefaultPassword is the default redis password.
	DefaultPassword = "Az123456_"
	// DefaultAIJobQueueSignalTopic is the default redis pub/sub topic for ai job queue wakeup.
	DefaultAIJobQueueSignalTopic = "rca:ai_job_queue_signal"
	// DefaultNoticeDeliveryStreamKey is the default redis stream key for notice delivery dispatch.
	DefaultNoticeDeliveryStreamKey = "rca:notice:delivery_stream"
	// DefaultNoticeDeliveryStreamGroup is the default redis stream consumer group for notice workers.
	DefaultNoticeDeliveryStreamGroup = "notice_delivery_workers"
	// DefaultNoticeDeliveryReclaimIdleSeconds is the default idle seconds for pending message reclaim.
	DefaultNoticeDeliveryReclaimIdleSeconds = 60
	// DefaultLimiterMode is the default redis limiter mode.
	DefaultLimiterMode = "both"
	// DefaultLimiterGlobalQPS is the default global notice limiter qps.
	DefaultLimiterGlobalQPS = 20.0
	// DefaultLimiterChannelQPS is the default per-channel notice limiter qps.
	DefaultLimiterChannelQPS = 0.0
	// DefaultLimiterBurst is the default limiter burst factor.
	DefaultLimiterBurst = 20
)

var errInvalidRedisAddr = errors.New("redis.addr must not be empty when redis.enabled=true")

// TopicOptions defines redis pub/sub topics.
type TopicOptions struct {
	AIJobQueueSignal string `json:"ai_job_queue_signal" mapstructure:"ai_job_queue_signal"`
}

// NoticeDeliveryStreamOptions defines redis stream options for notice delivery dispatch.
type NoticeDeliveryStreamOptions struct {
	Enabled            bool   `json:"enabled" mapstructure:"enabled"`
	Key                string `json:"key" mapstructure:"key"`
	Group              string `json:"group" mapstructure:"group"`
	ReclaimIdleSeconds int    `json:"reclaim_idle_seconds" mapstructure:"reclaim_idle_seconds"`
}

// PubSubOptions defines redis pub/sub release-profile controls.
type PubSubOptions struct {
	Enabled          bool   `json:"enabled" mapstructure:"enabled"`
	TopicAIJobSignal string `json:"topic_ai_job_signal" mapstructure:"topic_ai_job_signal"`
}

// LimiterOptions defines redis notice-worker limiter release-profile controls.
type LimiterOptions struct {
	Enabled    bool    `json:"enabled" mapstructure:"enabled"`
	Mode       string  `json:"mode" mapstructure:"mode"`
	GlobalQPS  float64 `json:"global_qps" mapstructure:"global_qps"`
	ChannelQPS float64 `json:"channel_qps" mapstructure:"channel_qps"`
	Burst      int     `json:"burst" mapstructure:"burst"`
}

// StreamsOptions defines redis stream release-profile controls.
type StreamsOptions struct {
	Enabled              bool                        `json:"enabled" mapstructure:"enabled"`
	NoticeDeliveryStream string                      `json:"notice_delivery_stream" mapstructure:"notice_delivery_stream"`
	ConsumerGroup        string                      `json:"consumer_group" mapstructure:"consumer_group"`
	ReclaimIdleSeconds   int                         `json:"reclaim_idle_seconds" mapstructure:"reclaim_idle_seconds"`
	NoticeDelivery       NoticeDeliveryStreamOptions `json:"notice_delivery" mapstructure:"notice_delivery"`
}

// AlertingOptions defines redis alerting short-state release-profile controls.
type AlertingOptions struct {
	Enabled bool `json:"enabled" mapstructure:"enabled"`
}

// RedisOptions defines redis connection and topic settings.
type RedisOptions struct {
	Enabled  bool   `json:"enabled" mapstructure:"enabled"`
	Addr     string `json:"addr" mapstructure:"addr"`
	DB       int    `json:"db" mapstructure:"db"`
	Password string `json:"password" mapstructure:"password"`
	FailOpen bool   `json:"fail_open" mapstructure:"fail_open"`

	// Legacy topic namespace (R1/R2 compatibility).
	Topic TopicOptions `json:"topic" mapstructure:"topic"`

	// O1 release profile namespaces.
	PubSub   PubSubOptions   `json:"pubsub" mapstructure:"pubsub"`
	Limiter  LimiterOptions  `json:"limiter" mapstructure:"limiter"`
	Streams  StreamsOptions  `json:"streams" mapstructure:"streams"`
	Alerting AlertingOptions `json:"alerting" mapstructure:"alerting"`
}

// NewRedisOptions returns redis options with repository defaults.
func NewRedisOptions() RedisOptions {
	return RedisOptions{
		Enabled:  false,
		Addr:     DefaultAddr,
		DB:       DefaultDB,
		Password: DefaultPassword,
		Topic: TopicOptions{
			AIJobQueueSignal: DefaultAIJobQueueSignalTopic,
		},
		PubSub: PubSubOptions{
			Enabled:          true,
			TopicAIJobSignal: DefaultAIJobQueueSignalTopic,
		},
		Limiter: LimiterOptions{
			Enabled:    true,
			Mode:       DefaultLimiterMode,
			GlobalQPS:  DefaultLimiterGlobalQPS,
			ChannelQPS: DefaultLimiterChannelQPS,
			Burst:      DefaultLimiterBurst,
		},
		Streams: StreamsOptions{
			Enabled:              false,
			NoticeDeliveryStream: DefaultNoticeDeliveryStreamKey,
			ConsumerGroup:        DefaultNoticeDeliveryStreamGroup,
			ReclaimIdleSeconds:   DefaultNoticeDeliveryReclaimIdleSeconds,
			NoticeDelivery: NoticeDeliveryStreamOptions{
				Enabled:            false,
				Key:                DefaultNoticeDeliveryStreamKey,
				Group:              DefaultNoticeDeliveryStreamGroup,
				ReclaimIdleSeconds: DefaultNoticeDeliveryReclaimIdleSeconds,
			},
		},
		Alerting: AlertingOptions{
			Enabled: true,
		},
		FailOpen: true,
	}
}

// ApplyDefaults fills zero-value fields with default values.
//
//nolint:gocognit,gocyclo // Legacy-to-profile compatibility normalization is intentionally explicit.
func (o *RedisOptions) ApplyDefaults() {
	if o == nil {
		return
	}
	o.Addr = strings.TrimSpace(o.Addr)
	if o.Addr == "" {
		o.Addr = DefaultAddr
	}

	legacyTopic := strings.TrimSpace(o.Topic.AIJobQueueSignal)
	o.PubSub.TopicAIJobSignal = strings.TrimSpace(o.PubSub.TopicAIJobSignal)
	if o.PubSub.TopicAIJobSignal == "" {
		if legacyTopic != "" {
			o.PubSub.TopicAIJobSignal = legacyTopic
		} else {
			o.PubSub.TopicAIJobSignal = DefaultAIJobQueueSignalTopic
		}
	}
	o.Topic.AIJobQueueSignal = o.PubSub.TopicAIJobSignal

	o.Limiter.Mode = normalizeLimiterMode(o.Limiter.Mode)
	if o.Limiter.GlobalQPS <= 0 {
		o.Limiter.GlobalQPS = DefaultLimiterGlobalQPS
	}
	if o.Limiter.ChannelQPS < 0 {
		o.Limiter.ChannelQPS = DefaultLimiterChannelQPS
	}
	if o.Limiter.Burst <= 0 {
		o.Limiter.Burst = DefaultLimiterBurst
	}

	legacyStreamEnabled := o.Streams.NoticeDelivery.Enabled
	legacyStreamKey := strings.TrimSpace(o.Streams.NoticeDelivery.Key)
	legacyStreamGroup := strings.TrimSpace(o.Streams.NoticeDelivery.Group)
	legacyReclaimIdleSeconds := o.Streams.NoticeDelivery.ReclaimIdleSeconds

	if !o.Streams.Enabled && legacyStreamEnabled {
		o.Streams.Enabled = true
	}

	o.Streams.NoticeDeliveryStream = strings.TrimSpace(o.Streams.NoticeDeliveryStream)
	if o.Streams.NoticeDeliveryStream == "" {
		if legacyStreamKey != "" {
			o.Streams.NoticeDeliveryStream = legacyStreamKey
		} else {
			o.Streams.NoticeDeliveryStream = DefaultNoticeDeliveryStreamKey
		}
	}

	o.Streams.ConsumerGroup = strings.TrimSpace(o.Streams.ConsumerGroup)
	if o.Streams.ConsumerGroup == "" {
		if legacyStreamGroup != "" {
			o.Streams.ConsumerGroup = legacyStreamGroup
		} else {
			o.Streams.ConsumerGroup = DefaultNoticeDeliveryStreamGroup
		}
	}

	if o.Streams.ReclaimIdleSeconds <= 0 {
		if legacyReclaimIdleSeconds > 0 {
			o.Streams.ReclaimIdleSeconds = legacyReclaimIdleSeconds
		} else {
			o.Streams.ReclaimIdleSeconds = DefaultNoticeDeliveryReclaimIdleSeconds
		}
	}

	o.Streams.NoticeDelivery.Enabled = o.Streams.Enabled
	o.Streams.NoticeDelivery.Key = o.Streams.NoticeDeliveryStream
	o.Streams.NoticeDelivery.Group = o.Streams.ConsumerGroup
	o.Streams.NoticeDelivery.ReclaimIdleSeconds = o.Streams.ReclaimIdleSeconds
}

// Validate checks redis options sanity.
func (o *RedisOptions) Validate() error {
	if o == nil {
		return nil
	}
	o.ApplyDefaults()
	if o.Enabled && strings.TrimSpace(o.Addr) == "" {
		return errInvalidRedisAddr
	}
	if o.DB < 0 {
		o.DB = DefaultDB
	}
	return nil
}

// PubSubEnabled reports whether redis pub/sub signal path is enabled.
func (o *RedisOptions) PubSubEnabled() bool {
	return o != nil && o.Enabled && o.PubSub.Enabled
}

// LimiterEnabled reports whether redis limiter path is enabled.
func (o *RedisOptions) LimiterEnabled() bool {
	return o != nil && o.Enabled && o.Limiter.Enabled
}

// StreamsEnabled reports whether redis streams dispatch/consume path is enabled.
func (o *RedisOptions) StreamsEnabled() bool {
	return o != nil && o.Enabled && o.Streams.Enabled
}

// AlertingEnabled reports whether redis alerting short-state path is enabled.
func (o *RedisOptions) AlertingEnabled() bool {
	return o != nil && o.Enabled && o.Alerting.Enabled
}

func normalizeLimiterMode(raw string) string {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "global":
		return "global"
	case "per_channel":
		return "per_channel"
	default:
		return DefaultLimiterMode
	}
}
