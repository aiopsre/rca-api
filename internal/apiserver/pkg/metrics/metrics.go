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
	Meter                     metric.Meter
	RESTResourceCreateCounter metric.Int64Counter
	RESTResourceGetCounter    metric.Int64Counter
	EvidenceQueryCounter      metric.Int64Counter
	EvidenceQueryLatency      metric.Float64Histogram
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

		// Assign the global singleton.
		M = &Metrics{
			Meter:                     meter,
			RESTResourceCreateCounter: createCounter,
			RESTResourceGetCounter:    getCount,
			EvidenceQueryCounter:      evidenceQueryCounter,
			EvidenceQueryLatency:      evidenceQueryLatency,
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
