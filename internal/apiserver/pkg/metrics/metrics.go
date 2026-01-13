package metrics

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Metrics holds the OpenTelemetry instruments for capturing application metrics.
type Metrics struct {
	Meter                               metric.Meter
	RESTResourceCreateCounter           metric.Int64Counter
	RESTResourceGetCounter              metric.Int64Counter
	EvidenceQueryCounter                metric.Int64Counter
	EvidenceQueryLatency                metric.Float64Histogram
	AlertEventIngestCounter             metric.Int64Counter
	AlertEventIngestLatency             metric.Float64Histogram
	AlertIngestTotal                    *prometheus.CounterVec
	AlertIngestAllowedTotal             *prometheus.CounterVec
	AlertIngestProgressedTotal          *prometheus.CounterVec
	AlertIngestDroppedTotal             *prometheus.CounterVec
	AlertIngestSilencedTotal            *prometheus.CounterVec
	AlertIngestMergedTotal              *prometheus.CounterVec
	AlertIngestNewIncidentTotal         *prometheus.CounterVec
	AlertIngestPolicyDecisionTotal      *prometheus.CounterVec
	AlertIngestPolicyBackendErrorTotal  *prometheus.CounterVec
	AIJobQueuePullCounter               metric.Int64Counter
	AIJobQueuePullLatency               metric.Float64Histogram
	NoticeDeliveryDispatchTotal         metric.Int64Counter
	NoticeDeliverySendTotal             metric.Int64Counter
	NoticeDeliverySendLatencyMS         metric.Float64Histogram
	NoticeDeliveryFailedTotal           metric.Int64Counter
	NoticeDeliverySnapshotMismatchTotal metric.Int64Counter
	NoticeDeliveryReplayTotal           metric.Int64Counter
	NoticeDeliveryCancelTotal           metric.Int64Counter
	NoticeRateLimitAcquireTotal         *prometheus.CounterVec
	NoticeLimiterAllowTotal             *prometheus.CounterVec
	NoticeLimiterDenyTotal              *prometheus.CounterVec
	NoticeLimiterFallbackTotal          *prometheus.CounterVec
	NoticeStreamReadTotal               *prometheus.CounterVec
	NoticeStreamMessagesTotal           *prometheus.CounterVec
	RedisStreamConsumeTotal             *prometheus.CounterVec
	NoticeWorkerClaimSourceTotal        *prometheus.CounterVec
	RedisPubSubPublishTotal             *prometheus.CounterVec
	RedisPubSubSubscribeState           *prometheus.GaugeVec
	RedisPubSubSubscribeReady           *prometheus.GaugeVec
	AIJobLongPollWakeupTotal            *prometheus.CounterVec
	AIJobLongPollFallbackTotal          *prometheus.CounterVec
	MCPCallsTotal                       *prometheus.CounterVec
	MCPCallLatencyMS                    *prometheus.HistogramVec
	MCPTruncatedTotal                   *prometheus.CounterVec
	MCPScopeDeniedTotal                 *prometheus.CounterVec
	MCPRateLimitedTotal                 *prometheus.CounterVec
}

var (
	// M is the global metrics instance.
	M *Metrics
	// once ensures the initialization logic runs only once.
	once sync.Once
)

// Init initializes the global metrics instance using the singleton pattern.
func Init(scope string) error {
	once.Do(func() {
		meter := otel.Meter(scope)

		// Define custom metrics.
		createCounter, _ := meter.Int64Counter(
			"rca_api_apiserver_resources_created_total",
			metric.WithDescription("Total number of REST resource create requests"),
		)

		getCount, _ := meter.Int64Counter(
			"rca_api_apiserver_resources_retrieved_total",
			metric.WithDescription("Total number of REST resource get requests"),
		)

		evidenceQueryCounter, _ := meter.Int64Counter(
			"rca_api_apiserver_evidence_query_total",
			metric.WithDescription("Total number of evidence query requests by type/outcome"),
		)

		evidenceQueryLatency, _ := meter.Float64Histogram(
			"rca_api_apiserver_evidence_query_latency_ms",
			metric.WithDescription("Latency in milliseconds for evidence queries"),
		)

		alertEventIngestCounter, _ := meter.Int64Counter(
			"rca_api_apiserver_alert_event_ingest_total",
			metric.WithDescription("Total number of alert ingest requests by merge_result/outcome"),
		)

		alertEventIngestLatency, _ := meter.Float64Histogram(
			"rca_api_apiserver_alert_event_ingest_latency_ms",
			metric.WithDescription("Latency in milliseconds for alert ingest requests"),
		)

		alertIngestPolicyDecisionTotal := promauto.With(prometheus.DefaultRegisterer).NewCounterVec(prometheus.CounterOpts{
			Name: "alert_ingest_policy_decision_total",
			Help: "Total alert ingest policy decisions by decision/backend.",
		}, []string{"decision", "backend"})
		alertIngestTotal := promauto.With(prometheus.DefaultRegisterer).NewCounterVec(prometheus.CounterOpts{
			Name: "alert_ingest_total",
			Help: "Total adapter alert ingest requests by adapter.",
		}, []string{"adapter"})
		alertIngestAllowedTotal := promauto.With(prometheus.DefaultRegisterer).NewCounterVec(prometheus.CounterOpts{
			Name: "alert_ingest_allowed_total",
			Help: "Total adapter alert ingest requests allowed by rollout policy.",
		}, []string{"adapter"})
		alertIngestProgressedTotal := promauto.With(prometheus.DefaultRegisterer).NewCounterVec(prometheus.CounterOpts{
			Name: "alert_ingest_progressed_total",
			Help: "Total adapter alert ingest requests progressed to incident pipeline.",
		}, []string{"adapter"})
		alertIngestDroppedTotal := promauto.With(prometheus.DefaultRegisterer).NewCounterVec(prometheus.CounterOpts{
			Name: "alert_ingest_dropped_total",
			Help: "Total adapter alert ingest requests dropped from incident progression by reason.",
		}, []string{"adapter", "reason"})
		alertIngestSilencedTotal := promauto.With(prometheus.DefaultRegisterer).NewCounterVec(prometheus.CounterOpts{
			Name: "alert_ingest_silenced_total",
			Help: "Total adapter alert ingest requests silenced by silence policy.",
		}, []string{"adapter"})
		alertIngestMergedTotal := promauto.With(prometheus.DefaultRegisterer).NewCounterVec(prometheus.CounterOpts{
			Name: "alert_ingest_merged_total",
			Help: "Total adapter alert ingest requests merged into current alert view by merge_result.",
		}, []string{"adapter", "merge_result"})
		alertIngestNewIncidentTotal := promauto.With(prometheus.DefaultRegisterer).NewCounterVec(prometheus.CounterOpts{
			Name: "alert_ingest_new_incident_total",
			Help: "Total adapter alert ingest requests that created new incidents.",
		}, []string{"adapter"})

		alertIngestPolicyBackendErrorTotal := promauto.With(prometheus.DefaultRegisterer).NewCounterVec(prometheus.CounterOpts{
			Name: "alert_ingest_policy_backend_error_total",
			Help: "Total alert ingest policy backend operation errors by backend/op.",
		}, []string{"backend", "op"})

		aiJobQueuePullCounter, _ := meter.Int64Counter(
			"rca_api_apiserver_ai_job_queue_pull_total",
			metric.WithDescription("Total number of AI job queue pull requests by status/outcome"),
		)

		aiJobQueuePullLatency, _ := meter.Float64Histogram(
			"rca_api_apiserver_ai_job_queue_pull_latency_ms",
			metric.WithDescription("Latency in milliseconds for AI job queue pull requests"),
		)

		noticeDispatchTotal, _ := meter.Int64Counter(
			"notice_delivery_dispatch_total",
			metric.WithDescription("Total number of notice deliveries enqueued to outbox"),
		)

		noticeSendTotal, _ := meter.Int64Counter(
			"notice_delivery_send_total",
			metric.WithDescription("Total number of notice webhook send attempts by status"),
		)

		noticeSendLatency, _ := meter.Float64Histogram(
			"notice_delivery_send_latency_ms",
			metric.WithDescription("Latency in milliseconds for notice webhook send attempts"),
		)

		noticeFailedTotal, _ := meter.Int64Counter(
			"notice_delivery_failed_total",
			metric.WithDescription("Total number of notice deliveries ended in failed status"),
		)

		noticeSnapshotMismatchTotal, _ := meter.Int64Counter(
			"notice_delivery_snapshot_mismatch_total",
			metric.WithDescription("Total number of notice deliveries failed by snapshot secret fingerprint mismatch"),
		)

		noticeReplayTotal, _ := meter.Int64Counter(
			"notice_delivery_replay_total",
			metric.WithDescription("Total number of notice delivery replay operations"),
		)

		noticeCancelTotal, _ := meter.Int64Counter(
			"notice_delivery_cancel_total",
			metric.WithDescription("Total number of notice delivery cancel operations"),
		)

		noticeRateLimitAcquireTotal := promauto.With(prometheus.DefaultRegisterer).NewCounterVec(prometheus.CounterOpts{
			Name: "notice_rate_limit_acquire_total",
			Help: "Total notice rate limiter acquire outcomes by mode/result/reason.",
		}, []string{"mode", "result", "reason"})
		noticeLimiterAllowTotal := promauto.With(prometheus.DefaultRegisterer).NewCounterVec(prometheus.CounterOpts{
			Name: "notice_limiter_allow_total",
			Help: "Total notice limiter allow decisions by mode.",
		}, []string{"mode"})
		noticeLimiterDenyTotal := promauto.With(prometheus.DefaultRegisterer).NewCounterVec(prometheus.CounterOpts{
			Name: "notice_limiter_deny_total",
			Help: "Total notice limiter deny decisions by mode/reason.",
		}, []string{"mode", "reason"})
		noticeLimiterFallbackTotal := promauto.With(prometheus.DefaultRegisterer).NewCounterVec(prometheus.CounterOpts{
			Name: "notice_limiter_fallback_total",
			Help: "Total notice limiter fallback events by reason.",
		}, []string{"reason"})
		noticeStreamReadTotal := promauto.With(prometheus.DefaultRegisterer).NewCounterVec(prometheus.CounterOpts{
			Name: "notice_stream_read_total",
			Help: "Total redis streams read attempts for notice delivery dispatch by result.",
		}, []string{"result"})
		noticeStreamMessagesTotal := promauto.With(prometheus.DefaultRegisterer).NewCounterVec(prometheus.CounterOpts{
			Name: "notice_stream_messages_total",
			Help: "Total notice stream message actions by type.",
		}, []string{"action"})
		redisStreamConsumeTotal := promauto.With(prometheus.DefaultRegisterer).NewCounterVec(prometheus.CounterOpts{
			Name: "redis_stream_consume_total",
			Help: "Total redis stream consume attempts by stream/result.",
		}, []string{"stream", "result"})
		noticeWorkerClaimSourceTotal := promauto.With(prometheus.DefaultRegisterer).NewCounterVec(prometheus.CounterOpts{
			Name: "notice_worker_claim_source_total",
			Help: "Total notice worker claimed deliveries by source.",
		}, []string{"source"})

		mcpCallsTotal := promauto.With(prometheus.DefaultRegisterer).NewCounterVec(prometheus.CounterOpts{
			Name: "mcp_calls_total",
			Help: "Total MCP tool calls by tool/error_code.",
		}, []string{"tool", "code"})

		//nolint:promlinter // Name is fixed by MCP C5 contract.
		mcpCallLatencyMS := promauto.With(prometheus.DefaultRegisterer).NewHistogramVec(prometheus.HistogramOpts{
			Name:    "mcp_call_latency_ms",
			Help:    "MCP tool call latency in milliseconds by tool.",
			Buckets: []float64{5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000},
		}, []string{"tool"})

		mcpTruncatedTotal := promauto.With(prometheus.DefaultRegisterer).NewCounterVec(prometheus.CounterOpts{
			Name: "mcp_truncated_total",
			Help: "Total truncated MCP responses by tool.",
		}, []string{"tool"})

		mcpScopeDeniedTotal := promauto.With(prometheus.DefaultRegisterer).NewCounterVec(prometheus.CounterOpts{
			Name: "mcp_scope_denied_total",
			Help: "Total MCP scope denied errors by tool.",
		}, []string{"tool"})

		mcpRateLimitedTotal := promauto.With(prometheus.DefaultRegisterer).NewCounterVec(prometheus.CounterOpts{
			Name: "mcp_rate_limited_total",
			Help: "Total MCP rate-limited errors by tool.",
		}, []string{"tool"})

		redisPubSubPublishTotal := promauto.With(prometheus.DefaultRegisterer).NewCounterVec(prometheus.CounterOpts{
			Name: "redis_pubsub_publish_total",
			Help: "Total redis pubsub publish attempts for ai job queue wakeup by topic/result.",
		}, []string{"topic", "result"})

		redisPubSubSubscribeState := promauto.With(prometheus.DefaultRegisterer).NewGaugeVec(prometheus.GaugeOpts{
			Name: "redis_pubsub_subscribe_state",
			Help: "Current redis pubsub subscribe readiness for ai job queue wakeup by topic (0/1).",
		}, []string{"topic"})
		redisPubSubSubscribeReady := promauto.With(prometheus.DefaultRegisterer).NewGaugeVec(prometheus.GaugeOpts{
			Name: "redis_pubsub_subscribe_ready",
			Help: "Current redis pubsub subscribe readiness for ai job queue wakeup by topic (0/1).",
		}, []string{"topic"})

		aiJobLongPollWakeupTotal := promauto.With(prometheus.DefaultRegisterer).NewCounterVec(prometheus.CounterOpts{
			Name: "ai_job_longpoll_wakeup_total",
			Help: "Total ai job longpoll wakeup outcomes by source.",
		}, []string{"source"})
		aiJobLongPollFallbackTotal := promauto.With(prometheus.DefaultRegisterer).NewCounterVec(prometheus.CounterOpts{
			Name: "ai_job_longpoll_fallback_total",
			Help: "Total ai job longpoll fallback events by reason.",
		}, []string{"reason"})

		// Assign the global singleton.
		M = &Metrics{
			Meter:                               meter,
			RESTResourceCreateCounter:           createCounter,
			RESTResourceGetCounter:              getCount,
			EvidenceQueryCounter:                evidenceQueryCounter,
			EvidenceQueryLatency:                evidenceQueryLatency,
			AlertEventIngestCounter:             alertEventIngestCounter,
			AlertEventIngestLatency:             alertEventIngestLatency,
			AlertIngestTotal:                    alertIngestTotal,
			AlertIngestAllowedTotal:             alertIngestAllowedTotal,
			AlertIngestProgressedTotal:          alertIngestProgressedTotal,
			AlertIngestDroppedTotal:             alertIngestDroppedTotal,
			AlertIngestSilencedTotal:            alertIngestSilencedTotal,
			AlertIngestMergedTotal:              alertIngestMergedTotal,
			AlertIngestNewIncidentTotal:         alertIngestNewIncidentTotal,
			AlertIngestPolicyDecisionTotal:      alertIngestPolicyDecisionTotal,
			AlertIngestPolicyBackendErrorTotal:  alertIngestPolicyBackendErrorTotal,
			AIJobQueuePullCounter:               aiJobQueuePullCounter,
			AIJobQueuePullLatency:               aiJobQueuePullLatency,
			NoticeDeliveryDispatchTotal:         noticeDispatchTotal,
			NoticeDeliverySendTotal:             noticeSendTotal,
			NoticeDeliverySendLatencyMS:         noticeSendLatency,
			NoticeDeliveryFailedTotal:           noticeFailedTotal,
			NoticeDeliverySnapshotMismatchTotal: noticeSnapshotMismatchTotal,
			NoticeDeliveryReplayTotal:           noticeReplayTotal,
			NoticeDeliveryCancelTotal:           noticeCancelTotal,
			NoticeRateLimitAcquireTotal:         noticeRateLimitAcquireTotal,
			NoticeLimiterAllowTotal:             noticeLimiterAllowTotal,
			NoticeLimiterDenyTotal:              noticeLimiterDenyTotal,
			NoticeLimiterFallbackTotal:          noticeLimiterFallbackTotal,
			NoticeStreamReadTotal:               noticeStreamReadTotal,
			NoticeStreamMessagesTotal:           noticeStreamMessagesTotal,
			RedisStreamConsumeTotal:             redisStreamConsumeTotal,
			NoticeWorkerClaimSourceTotal:        noticeWorkerClaimSourceTotal,
			RedisPubSubPublishTotal:             redisPubSubPublishTotal,
			RedisPubSubSubscribeState:           redisPubSubSubscribeState,
			RedisPubSubSubscribeReady:           redisPubSubSubscribeReady,
			AIJobLongPollWakeupTotal:            aiJobLongPollWakeupTotal,
			AIJobLongPollFallbackTotal:          aiJobLongPollFallbackTotal,
			MCPCallsTotal:                       mcpCallsTotal,
			MCPCallLatencyMS:                    mcpCallLatencyMS,
			MCPTruncatedTotal:                   mcpTruncatedTotal,
			MCPScopeDeniedTotal:                 mcpScopeDeniedTotal,
			MCPRateLimitedTotal:                 mcpRateLimitedTotal,
		}

		// Pre-create baseline series so /metrics always exposes MCP metric families.
		mcpCallsTotal.WithLabelValues("unknown", "OK").Add(0)
		mcpCallLatencyMS.WithLabelValues("unknown").Observe(0)
		mcpTruncatedTotal.WithLabelValues("unknown").Add(0)
		mcpScopeDeniedTotal.WithLabelValues("unknown").Add(0)
		mcpRateLimitedTotal.WithLabelValues("unknown").Add(0)
		noticeRateLimitAcquireTotal.WithLabelValues("redis", "ok", "ok").Add(0)
		noticeRateLimitAcquireTotal.WithLabelValues("redis", "deny", "global_qps").Add(0)
		noticeRateLimitAcquireTotal.WithLabelValues("redis", "error", "redis_error").Add(0)
		noticeRateLimitAcquireTotal.WithLabelValues("local", "ok", "local").Add(0)
		noticeLimiterAllowTotal.WithLabelValues("redis").Add(0)
		noticeLimiterAllowTotal.WithLabelValues("local").Add(0)
		noticeLimiterDenyTotal.WithLabelValues("redis", "global_qps").Add(0)
		noticeLimiterFallbackTotal.WithLabelValues("redis_error").Add(0)
		alertIngestPolicyDecisionTotal.WithLabelValues("normal", "mysql").Add(0)
		alertIngestPolicyDecisionTotal.WithLabelValues("merged", "mysql").Add(0)
		alertIngestPolicyDecisionTotal.WithLabelValues("silenced", "mysql").Add(0)
		alertIngestPolicyDecisionTotal.WithLabelValues("deduped", "mysql").Add(0)
		alertIngestPolicyDecisionTotal.WithLabelValues("deduped", "redis").Add(0)
		alertIngestTotal.WithLabelValues("unknown").Add(0)
		alertIngestAllowedTotal.WithLabelValues("unknown").Add(0)
		alertIngestProgressedTotal.WithLabelValues("unknown").Add(0)
		alertIngestDroppedTotal.WithLabelValues("unknown", "unknown").Add(0)
		alertIngestSilencedTotal.WithLabelValues("unknown").Add(0)
		alertIngestMergedTotal.WithLabelValues("unknown", "unknown").Add(0)
		alertIngestNewIncidentTotal.WithLabelValues("unknown").Add(0)
		alertIngestPolicyBackendErrorTotal.WithLabelValues("redis", "dedup").Add(0)
		alertIngestPolicyBackendErrorTotal.WithLabelValues("redis", "burst").Add(0)
		noticeStreamReadTotal.WithLabelValues("ok").Add(0)
		noticeStreamReadTotal.WithLabelValues("error").Add(0)
		noticeStreamMessagesTotal.WithLabelValues("xadd").Add(0)
		noticeStreamMessagesTotal.WithLabelValues("reclaim").Add(0)
		noticeStreamMessagesTotal.WithLabelValues("claim_ok").Add(0)
		noticeStreamMessagesTotal.WithLabelValues("claim_skip").Add(0)
		noticeStreamMessagesTotal.WithLabelValues("claim_error").Add(0)
		noticeStreamMessagesTotal.WithLabelValues("process_error").Add(0)
		noticeStreamMessagesTotal.WithLabelValues("ack").Add(0)
		redisStreamConsumeTotal.WithLabelValues("unknown", "ok").Add(0)
		redisStreamConsumeTotal.WithLabelValues("unknown", "error").Add(0)
		noticeWorkerClaimSourceTotal.WithLabelValues("stream").Add(0)
		noticeWorkerClaimSourceTotal.WithLabelValues("db_fallback").Add(0)
		redisPubSubPublishTotal.WithLabelValues("unknown", "ok").Add(0)
		redisPubSubPublishTotal.WithLabelValues("unknown", "error").Add(0)
		redisPubSubSubscribeState.WithLabelValues("unknown").Set(0)
		redisPubSubSubscribeReady.WithLabelValues("unknown").Set(0)
		aiJobLongPollWakeupTotal.WithLabelValues("redis").Add(0)
		aiJobLongPollWakeupTotal.WithLabelValues("db_watermark").Add(0)
		aiJobLongPollWakeupTotal.WithLabelValues("timeout").Add(0)
		aiJobLongPollFallbackTotal.WithLabelValues("redis_unavailable").Add(0)
		aiJobLongPollFallbackTotal.WithLabelValues("db_watermark").Add(0)
		aiJobLongPollFallbackTotal.WithLabelValues("timeout").Add(0)
	})

	return nil
}

// RecordResourceCreate increments the counter for a resource creation operation.
func (m *Metrics) RecordResourceCreate(ctx context.Context, resource string) {
	attrs := []attribute.KeyValue{attribute.String("resource", resource)}

	m.RESTResourceCreateCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordResourceGet increments the counter for a resource retrieval operation.
func (m *Metrics) RecordResourceGet(ctx context.Context, resource string) {
	attrs := []attribute.KeyValue{attribute.String("resource", resource)}

	m.RESTResourceGetCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordEvidenceQuery records evidence query metrics for observability.
func (m *Metrics) RecordEvidenceQuery(ctx context.Context, queryType string, datasourceType string, outcome string, duration time.Duration) {
	attrs := []attribute.KeyValue{
		attribute.String("query_type", queryType),
		attribute.String("datasource_type", datasourceType),
		attribute.String("outcome", outcome),
	}
	m.EvidenceQueryCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
	m.EvidenceQueryLatency.Record(ctx, float64(duration.Milliseconds()), metric.WithAttributes(attrs...))
}

// RecordAlertEventIngest records ingest/merge metrics for alert events.
func (m *Metrics) RecordAlertEventIngest(ctx context.Context, mergeResult string, outcome string, duration time.Duration) {
	if mergeResult == "" {
		mergeResult = "unknown"
	}
	if outcome == "" {
		outcome = "unknown"
	}
	attrs := []attribute.KeyValue{
		attribute.String("merge_result", mergeResult),
		attribute.String("outcome", outcome),
	}
	m.AlertEventIngestCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
	m.AlertEventIngestLatency.Record(ctx, float64(duration.Milliseconds()), metric.WithAttributes(attrs...))
}

// RecordAlertIngestTotal records total adapter ingest count.
func (m *Metrics) RecordAlertIngestTotal(adapter string) {
	if m == nil || m.AlertIngestTotal == nil {
		return
	}
	m.AlertIngestTotal.WithLabelValues(normalizeAlertAdapterLabel(adapter)).Inc()
}

// RecordAlertIngestAllowed records rollout allow-list matched count.
func (m *Metrics) RecordAlertIngestAllowed(adapter string) {
	if m == nil || m.AlertIngestAllowedTotal == nil {
		return
	}
	m.AlertIngestAllowedTotal.WithLabelValues(normalizeAlertAdapterLabel(adapter)).Inc()
}

// RecordAlertIngestProgressed records incident progression count.
func (m *Metrics) RecordAlertIngestProgressed(adapter string) {
	if m == nil || m.AlertIngestProgressedTotal == nil {
		return
	}
	m.AlertIngestProgressedTotal.WithLabelValues(normalizeAlertAdapterLabel(adapter)).Inc()
}

// RecordAlertIngestDropped records rollout drop count by reason.
func (m *Metrics) RecordAlertIngestDropped(adapter string, reason string) {
	if m == nil || m.AlertIngestDroppedTotal == nil {
		return
	}
	m.AlertIngestDroppedTotal.WithLabelValues(
		normalizeAlertAdapterLabel(adapter),
		normalizeAlertReasonLabel(reason),
	).Inc()
}

// RecordAlertIngestSilenced records silenced ingest count.
func (m *Metrics) RecordAlertIngestSilenced(adapter string) {
	if m == nil || m.AlertIngestSilencedTotal == nil {
		return
	}
	m.AlertIngestSilencedTotal.WithLabelValues(normalizeAlertAdapterLabel(adapter)).Inc()
}

// RecordAlertIngestMerged records merged ingest count by merge result.
func (m *Metrics) RecordAlertIngestMerged(adapter string, mergeResult string) {
	if m == nil || m.AlertIngestMergedTotal == nil {
		return
	}
	m.AlertIngestMergedTotal.WithLabelValues(
		normalizeAlertAdapterLabel(adapter),
		normalizeAlertReasonLabel(mergeResult),
	).Inc()
}

// RecordAlertIngestNewIncident records new-incident count.
func (m *Metrics) RecordAlertIngestNewIncident(adapter string) {
	if m == nil || m.AlertIngestNewIncidentTotal == nil {
		return
	}
	m.AlertIngestNewIncidentTotal.WithLabelValues(normalizeAlertAdapterLabel(adapter)).Inc()
}

// RecordAlertIngestPolicyDecision records one alert ingest policy decision.
func (m *Metrics) RecordAlertIngestPolicyDecision(decision string, backend string) {
	if m == nil || m.AlertIngestPolicyDecisionTotal == nil {
		return
	}
	decision = strings.TrimSpace(strings.ToLower(decision))
	if decision == "" {
		decision = "unknown"
	}
	backend = strings.TrimSpace(strings.ToLower(backend))
	if backend == "" {
		backend = "unknown"
	}
	m.AlertIngestPolicyDecisionTotal.WithLabelValues(decision, backend).Inc()
}

// RecordAlertIngestPolicyBackendError records one policy backend operation error.
func (m *Metrics) RecordAlertIngestPolicyBackendError(backend string, op string) {
	if m == nil || m.AlertIngestPolicyBackendErrorTotal == nil {
		return
	}
	backend = strings.TrimSpace(strings.ToLower(backend))
	if backend == "" {
		backend = "unknown"
	}
	op = strings.TrimSpace(strings.ToLower(op))
	if op == "" {
		op = "unknown"
	}
	m.AlertIngestPolicyBackendErrorTotal.WithLabelValues(backend, op).Inc()
}

// RecordAIJobQueuePull records queue pull metrics for orchestrator polling.
func (m *Metrics) RecordAIJobQueuePull(ctx context.Context, status string, outcome string, duration time.Duration) {
	if status == "" {
		status = "unknown"
	}
	if outcome == "" {
		outcome = "unknown"
	}
	attrs := []attribute.KeyValue{
		attribute.String("status", status),
		attribute.String("outcome", outcome),
	}
	m.AIJobQueuePullCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
	m.AIJobQueuePullLatency.Record(ctx, float64(duration.Milliseconds()), metric.WithAttributes(attrs...))
}

// RecordRedisPubSubPublish records one redis pubsub publish attempt.
func (m *Metrics) RecordRedisPubSubPublish(topic string, result string) {
	if m == nil || m.RedisPubSubPublishTotal == nil {
		return
	}
	topic = strings.TrimSpace(topic)
	if topic == "" {
		topic = "unknown"
	}
	result = strings.TrimSpace(strings.ToLower(result))
	if result == "" {
		result = "unknown"
	}
	m.RedisPubSubPublishTotal.WithLabelValues(topic, result).Inc()
}

// SetRedisPubSubSubscribeState updates redis subscribe readiness gauge.
func (m *Metrics) SetRedisPubSubSubscribeState(topic string, ready bool) {
	if m == nil {
		return
	}
	topic = strings.TrimSpace(topic)
	if topic == "" {
		topic = "unknown"
	}
	value := 0.0
	if ready {
		value = 1
	}
	if m.RedisPubSubSubscribeState != nil {
		m.RedisPubSubSubscribeState.WithLabelValues(topic).Set(value)
	}
	if m.RedisPubSubSubscribeReady != nil {
		m.RedisPubSubSubscribeReady.WithLabelValues(topic).Set(value)
	}
}

// RecordAIJobLongPollWakeup records one longpoll wakeup source.
func (m *Metrics) RecordAIJobLongPollWakeup(source string) {
	if m == nil || m.AIJobLongPollWakeupTotal == nil {
		return
	}
	source = strings.TrimSpace(strings.ToLower(source))
	if source == "" {
		source = "unknown"
	}
	m.AIJobLongPollWakeupTotal.WithLabelValues(source).Inc()
}

// RecordAIJobLongPollFallback records one longpoll fallback reason.
func (m *Metrics) RecordAIJobLongPollFallback(reason string) {
	if m == nil || m.AIJobLongPollFallbackTotal == nil {
		return
	}
	reason = strings.TrimSpace(strings.ToLower(reason))
	if reason == "" {
		reason = "unknown"
	}
	m.AIJobLongPollFallbackTotal.WithLabelValues(reason).Inc()
}

// RecordNoticeRateLimitAcquire records one limiter acquire outcome.
//
//nolint:gocognit,gocyclo // Counter fan-out is intentionally explicit for ops visibility.
func (m *Metrics) RecordNoticeRateLimitAcquire(mode string, result string, reason string) {
	if m == nil || m.NoticeRateLimitAcquireTotal == nil {
		return
	}
	mode = strings.TrimSpace(strings.ToLower(mode))
	if mode == "" {
		mode = "unknown"
	}
	result = strings.TrimSpace(strings.ToLower(result))
	if result == "" {
		result = "unknown"
	}
	reason = strings.TrimSpace(strings.ToLower(reason))
	if reason == "" {
		reason = "unknown"
	}
	m.NoticeRateLimitAcquireTotal.WithLabelValues(mode, result, reason).Inc()
	switch result {
	case "ok":
		if m.NoticeLimiterAllowTotal != nil {
			m.NoticeLimiterAllowTotal.WithLabelValues(mode).Inc()
		}

	case "deny":
		if m.NoticeLimiterDenyTotal != nil {
			m.NoticeLimiterDenyTotal.WithLabelValues(mode, reason).Inc()
		}

	case "error":
		if m.NoticeLimiterFallbackTotal != nil && reason == "redis_error" {
			m.NoticeLimiterFallbackTotal.WithLabelValues(reason).Inc()
		}
	}
}

// RecordNoticeStreamRead records redis notice stream read result.
func (m *Metrics) RecordNoticeStreamRead(result string) {
	if m == nil || m.NoticeStreamReadTotal == nil {
		return
	}
	result = strings.TrimSpace(strings.ToLower(result))
	if result == "" {
		result = "unknown"
	}
	m.NoticeStreamReadTotal.WithLabelValues(result).Inc()
}

// RecordRedisStreamConsume records one redis stream consume result.
func (m *Metrics) RecordRedisStreamConsume(stream string, result string) {
	if m == nil || m.RedisStreamConsumeTotal == nil {
		return
	}
	stream = strings.TrimSpace(stream)
	if stream == "" {
		stream = "unknown"
	}
	result = strings.TrimSpace(strings.ToLower(result))
	if result == "" {
		result = "unknown"
	}
	m.RedisStreamConsumeTotal.WithLabelValues(stream, result).Inc()
}

// RecordNoticeWorkerClaimSource records notice worker claim source count.
func (m *Metrics) RecordNoticeWorkerClaimSource(source string, count int) {
	if m == nil || m.NoticeWorkerClaimSourceTotal == nil || count <= 0 {
		return
	}
	source = strings.TrimSpace(strings.ToLower(source))
	if source == "" {
		source = "unknown"
	}
	m.NoticeWorkerClaimSourceTotal.WithLabelValues(source).Add(float64(count))
}

// RecordNoticeStreamMessage records one notice stream message action.
func (m *Metrics) RecordNoticeStreamMessage(action string) {
	if m == nil || m.NoticeStreamMessagesTotal == nil {
		return
	}
	action = strings.TrimSpace(strings.ToLower(action))
	if action == "" {
		action = "unknown"
	}
	m.NoticeStreamMessagesTotal.WithLabelValues(action).Inc()
}

// RecordNoticeDeliveryDispatch records one notice delivery enqueue event.
func (m *Metrics) RecordNoticeDeliveryDispatch(ctx context.Context, eventType string) {
	if eventType == "" {
		eventType = "unknown"
	}
	attrs := []attribute.KeyValue{
		attribute.String("event_type", eventType),
	}
	m.NoticeDeliveryDispatchTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordNoticeDeliverySend records one notice webhook send attempt.
func (m *Metrics) RecordNoticeDeliverySend(ctx context.Context, eventType string, status string, duration time.Duration) {
	if eventType == "" {
		eventType = "unknown"
	}
	if status == "" {
		status = "unknown"
	}
	attrs := []attribute.KeyValue{
		attribute.String("event_type", eventType),
		attribute.String("status", status),
	}
	m.NoticeDeliverySendTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
	m.NoticeDeliverySendLatencyMS.Record(ctx, float64(duration.Milliseconds()), metric.WithAttributes(attrs...))
}

// RecordNoticeDeliveryFailed records one notice delivery terminal failure.
func (m *Metrics) RecordNoticeDeliveryFailed(ctx context.Context, eventType string) {
	if eventType == "" {
		eventType = "unknown"
	}
	attrs := []attribute.KeyValue{
		attribute.String("event_type", eventType),
	}
	m.NoticeDeliveryFailedTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordNoticeDeliverySnapshotMismatch records one snapshot secret fingerprint mismatch failure.
func (m *Metrics) RecordNoticeDeliverySnapshotMismatch(ctx context.Context, eventType string) {
	if eventType == "" {
		eventType = "unknown"
	}
	attrs := []attribute.KeyValue{
		attribute.String("event_type", eventType),
	}
	m.NoticeDeliverySnapshotMismatchTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordNoticeDeliveryReplay records one replay operation.
func (m *Metrics) RecordNoticeDeliveryReplay(ctx context.Context, status string, mode string) {
	if status == "" {
		status = "unknown"
	}
	if mode == "" {
		mode = "unknown"
	}
	attrs := []attribute.KeyValue{
		attribute.String("status", status),
		attribute.String("mode", mode),
	}
	m.NoticeDeliveryReplayTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordNoticeDeliveryCancel records one cancel operation.
func (m *Metrics) RecordNoticeDeliveryCancel(ctx context.Context, status string) {
	if status == "" {
		status = "unknown"
	}
	attrs := []attribute.KeyValue{
		attribute.String("status", status),
	}
	m.NoticeDeliveryCancelTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordMCPToolCall records MCP call totals grouped by tool and MCP error code.
func (m *Metrics) RecordMCPToolCall(_ context.Context, tool string, code string) {
	if m == nil || m.MCPCallsTotal == nil {
		return
	}
	m.MCPCallsTotal.WithLabelValues(normalizeMCPToolLabel(tool), normalizeMCPCodeLabel(code)).Inc()
}

// RecordMCPToolLatency records MCP call latency histogram by tool.
func (m *Metrics) RecordMCPToolLatency(_ context.Context, tool string, duration time.Duration) {
	if m == nil || m.MCPCallLatencyMS == nil {
		return
	}
	m.MCPCallLatencyMS.WithLabelValues(normalizeMCPToolLabel(tool)).Observe(float64(duration.Milliseconds()))
}

// RecordMCPTruncated records one truncated MCP response event.
func (m *Metrics) RecordMCPTruncated(_ context.Context, tool string) {
	if m == nil || m.MCPTruncatedTotal == nil {
		return
	}
	m.MCPTruncatedTotal.WithLabelValues(normalizeMCPToolLabel(tool)).Inc()
}

// RecordMCPScopeDenied records one MCP scope-denied event.
func (m *Metrics) RecordMCPScopeDenied(_ context.Context, tool string) {
	if m == nil || m.MCPScopeDeniedTotal == nil {
		return
	}
	m.MCPScopeDeniedTotal.WithLabelValues(normalizeMCPToolLabel(tool)).Inc()
}

// RecordMCPRateLimited records one MCP rate-limited event.
func (m *Metrics) RecordMCPRateLimited(_ context.Context, tool string) {
	if m == nil || m.MCPRateLimitedTotal == nil {
		return
	}
	m.MCPRateLimitedTotal.WithLabelValues(normalizeMCPToolLabel(tool)).Inc()
}

func normalizeMCPToolLabel(tool string) string {
	if normalized := strings.ToLower(strings.TrimSpace(tool)); normalized != "" {
		return normalized
	}
	return "unknown"
}

func normalizeMCPCodeLabel(code string) string {
	if normalized := strings.ToUpper(strings.TrimSpace(code)); normalized != "" {
		return normalized
	}
	return "OK"
}

func normalizeAlertAdapterLabel(adapter string) string {
	if normalized := strings.ToLower(strings.TrimSpace(adapter)); normalized != "" {
		return normalized
	}
	return "unknown"
}

func normalizeAlertReasonLabel(reason string) string {
	if normalized := strings.ToLower(strings.TrimSpace(reason)); normalized != "" {
		return normalized
	}
	return "unknown"
}
