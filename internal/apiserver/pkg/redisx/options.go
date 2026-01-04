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
)

var errInvalidRedisAddr = errors.New("redis.addr must not be empty when redis.enabled=true")

// TopicOptions defines redis pub/sub topics.
type TopicOptions struct {
	AIJobQueueSignal string `json:"ai_job_queue_signal" mapstructure:"ai_job_queue_signal"`
}

// RedisOptions defines redis connection and topic settings.
type RedisOptions struct {
	Enabled  bool         `json:"enabled" mapstructure:"enabled"`
	Addr     string       `json:"addr" mapstructure:"addr"`
	DB       int          `json:"db" mapstructure:"db"`
	Password string       `json:"password" mapstructure:"password"`
	Topic    TopicOptions `json:"topic" mapstructure:"topic"`
	FailOpen bool         `json:"fail_open" mapstructure:"fail_open"`
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
