package metrics

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Metrics holds the OpenTelemetry instruments for capturing application metrics.
type Metrics struct {
	Meter                       metric.Meter
	RESTResourceCreateCounter   metric.Int64Counter
	RESTResourceGetCounter      metric.Int64Counter
	EvidenceQueryCounter        metric.Int64Counter
	EvidenceQueryLatency        metric.Float64Histogram
	AlertEventIngestCounter     metric.Int64Counter
	AlertEventIngestLatency     metric.Float64Histogram
	AIJobQueuePullCounter       metric.Int64Counter
	AIJobQueuePullLatency       metric.Float64Histogram
	NoticeDeliveryDispatchTotal metric.Int64Counter
	NoticeDeliverySendTotal     metric.Int64Counter
	NoticeDeliverySendLatencyMS metric.Float64Histogram
	NoticeDeliveryFailedTotal   metric.Int64Counter
	NoticeDeliveryReplayTotal   metric.Int64Counter
	NoticeDeliveryCancelTotal   metric.Int64Counter
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

		noticeReplayTotal, _ := meter.Int64Counter(
			"notice_delivery_replay_total",
			metric.WithDescription("Total number of notice delivery replay operations"),
		)

		noticeCancelTotal, _ := meter.Int64Counter(
			"notice_delivery_cancel_total",
			metric.WithDescription("Total number of notice delivery cancel operations"),
		)

		// Assign the global singleton.
		M = &Metrics{
			Meter:                       meter,
			RESTResourceCreateCounter:   createCounter,
			RESTResourceGetCounter:      getCount,
			EvidenceQueryCounter:        evidenceQueryCounter,
			EvidenceQueryLatency:        evidenceQueryLatency,
			AlertEventIngestCounter:     alertEventIngestCounter,
			AlertEventIngestLatency:     alertEventIngestLatency,
			AIJobQueuePullCounter:       aiJobQueuePullCounter,
			AIJobQueuePullLatency:       aiJobQueuePullLatency,
			NoticeDeliveryDispatchTotal: noticeDispatchTotal,
			NoticeDeliverySendTotal:     noticeSendTotal,
			NoticeDeliverySendLatencyMS: noticeSendLatency,
			NoticeDeliveryFailedTotal:   noticeFailedTotal,
			NoticeDeliveryReplayTotal:   noticeReplayTotal,
			NoticeDeliveryCancelTotal:   noticeCancelTotal,
		}
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
