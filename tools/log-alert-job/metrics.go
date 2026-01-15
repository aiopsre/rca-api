package main

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type jobMetrics struct {
	ticksTotal              *prometheus.CounterVec
	ruleQueriesTotal        *prometheus.CounterVec
	clustersTotal           *prometheus.CounterVec
	firesTotal              *prometheus.CounterVec
	webhookTotal            *prometheus.CounterVec
	webhookLatencyMS        *prometheus.HistogramVec
	cooldownSuppressedTotal *prometheus.CounterVec
	esFailoverTotal         prometheus.Counter
	esRequestTotal          *prometheus.CounterVec
}

func newJobMetrics(registry *prometheus.Registry) (*jobMetrics, error) {
	ticksTotal := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "log_alert_ticks_total",
			Help: "Total number of log-alert-job tick runs.",
		},
		[]string{"result"},
	)
	ruleQueriesTotal := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "log_alert_rule_queries_total",
			Help: "Total number of ES queries per rule.",
		},
		[]string{"rule", "code"},
	)
	clustersTotal := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "log_alert_clusters_total",
			Help: "Total number of clusters observed per rule.",
		},
		[]string{"rule"},
	)
	firesTotal := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "log_alert_fires_total",
			Help: "Total number of fired webhook alerts per rule.",
		},
		[]string{"rule"},
	)
	webhookTotal := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "log_alert_webhook_total",
			Help: "Total number of webhook calls per rule and status code.",
		},
		[]string{"rule", "code"},
	)
	//nolint:promlinter // Keep *_ms suffix to align with T8 acceptance contract.
	webhookLatencyMS := prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "log_alert_webhook_latency_ms",
			Help:    "Webhook latency in milliseconds.",
			Buckets: []float64{10, 25, 50, 100, 200, 500, 1000, 2000, 5000},
		},
		[]string{"rule"},
	)
	cooldownSuppressedTotal := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "log_alert_cooldown_suppressed_total",
			Help: "Total number of clusters suppressed by cooldown.",
		},
		[]string{"rule"},
	)
	esFailoverTotal := prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "log_alert_es_failover_total",
			Help: "Total number of ES request failovers.",
		},
	)
	esRequestTotal := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "log_alert_es_request_total",
			Help: "Total number of ES requests by HTTP code.",
		},
		[]string{"code"},
	)

	metricSet := []prometheus.Collector{
		ticksTotal,
		ruleQueriesTotal,
		clustersTotal,
		firesTotal,
		webhookTotal,
		webhookLatencyMS,
		cooldownSuppressedTotal,
		esFailoverTotal,
		esRequestTotal,
	}
	for _, collector := range metricSet {
		if err := registry.Register(collector); err != nil {
			return nil, err
		}
	}

	return &jobMetrics{
		ticksTotal:              ticksTotal,
		ruleQueriesTotal:        ruleQueriesTotal,
		clustersTotal:           clustersTotal,
		firesTotal:              firesTotal,
		webhookTotal:            webhookTotal,
		webhookLatencyMS:        webhookLatencyMS,
		cooldownSuppressedTotal: cooldownSuppressedTotal,
		esFailoverTotal:         esFailoverTotal,
		esRequestTotal:          esRequestTotal,
	}, nil
}

func (m *jobMetrics) recordTick(result string) {
	m.ticksTotal.WithLabelValues(result).Inc()
}

func (m *jobMetrics) recordRuleQuery(ruleID string, code string) {
	m.ruleQueriesTotal.WithLabelValues(ruleID, code).Inc()
}

func (m *jobMetrics) recordClusters(ruleID string, count int) {
	m.clustersTotal.WithLabelValues(ruleID).Add(float64(count))
}

func (m *jobMetrics) recordFire(ruleID string) {
	m.firesTotal.WithLabelValues(ruleID).Inc()
}

func (m *jobMetrics) recordWebhook(ruleID string, code string, latency time.Duration) {
	m.webhookTotal.WithLabelValues(ruleID, code).Inc()
	m.webhookLatencyMS.WithLabelValues(ruleID).Observe(float64(latency.Milliseconds()))
}

func (m *jobMetrics) recordCooldownSuppressed(ruleID string) {
	m.cooldownSuppressedTotal.WithLabelValues(ruleID).Inc()
}

func (m *jobMetrics) recordESFailover() {
	m.esFailoverTotal.Inc()
}

func (m *jobMetrics) recordESRequest(code int) {
	m.esRequestTotal.WithLabelValues(strconv.Itoa(code)).Inc()
}

func (m *jobMetrics) recordESRequestCode(code string) {
	m.esRequestTotal.WithLabelValues(code).Inc()
}
