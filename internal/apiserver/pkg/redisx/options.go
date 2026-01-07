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

// StreamsOptions defines redis stream families.
type StreamsOptions struct {
	NoticeDelivery NoticeDeliveryStreamOptions `json:"notice_delivery" mapstructure:"notice_delivery"`
}

// RedisOptions defines redis connection and topic settings.
type RedisOptions struct {
	Enabled  bool           `json:"enabled" mapstructure:"enabled"`
	Addr     string         `json:"addr" mapstructure:"addr"`
	DB       int            `json:"db" mapstructure:"db"`
	Password string         `json:"password" mapstructure:"password"`
	Topic    TopicOptions   `json:"topic" mapstructure:"topic"`
	Streams  StreamsOptions `json:"streams" mapstructure:"streams"`
	FailOpen bool           `json:"fail_open" mapstructure:"fail_open"`
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
		Streams: StreamsOptions{
			NoticeDelivery: NoticeDeliveryStreamOptions{
				Enabled:            false,
				Key:                DefaultNoticeDeliveryStreamKey,
				Group:              DefaultNoticeDeliveryStreamGroup,
				ReclaimIdleSeconds: DefaultNoticeDeliveryReclaimIdleSeconds,
			},
		},
		FailOpen: true,
	}
}

// ApplyDefaults fills zero-value fields with default values.
func (o *RedisOptions) ApplyDefaults() {
	if o == nil {
		return
	}
	o.Addr = strings.TrimSpace(o.Addr)
	if o.Addr == "" {
		o.Addr = DefaultAddr
	}
	if o.Topic.AIJobQueueSignal == "" {
		o.Topic.AIJobQueueSignal = DefaultAIJobQueueSignalTopic
	}
	o.Topic.AIJobQueueSignal = strings.TrimSpace(o.Topic.AIJobQueueSignal)
	if o.Topic.AIJobQueueSignal == "" {
		o.Topic.AIJobQueueSignal = DefaultAIJobQueueSignalTopic
	}
	o.Streams.NoticeDelivery.Key = strings.TrimSpace(o.Streams.NoticeDelivery.Key)
	if o.Streams.NoticeDelivery.Key == "" {
		o.Streams.NoticeDelivery.Key = DefaultNoticeDeliveryStreamKey
	}
	o.Streams.NoticeDelivery.Group = strings.TrimSpace(o.Streams.NoticeDelivery.Group)
	if o.Streams.NoticeDelivery.Group == "" {
		o.Streams.NoticeDelivery.Group = DefaultNoticeDeliveryStreamGroup
	}
	if o.Streams.NoticeDelivery.ReclaimIdleSeconds <= 0 {
		o.Streams.NoticeDelivery.ReclaimIdleSeconds = DefaultNoticeDeliveryReclaimIdleSeconds
	}
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
