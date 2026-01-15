package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const (
	maxPayloadBytes           = 16 * 1024
	maxMapValueLength         = 512
	maxSummaryLength          = 256
	maxPayloadFieldShort      = 160
	maxPayloadFieldExtraShort = 96
)

type genericWebhookPayload struct {
	Namespace   string            `json:"namespace,omitempty"`
	Service     string            `json:"service,omitempty"`
	Severity    string            `json:"severity,omitempty"`
	Status      string            `json:"status,omitempty"`
	AlertName   string            `json:"alertname,omitempty"`
	Summary     string            `json:"summary,omitempty"`
	EventTime   string            `json:"event_time,omitempty"`
	Fingerprint string            `json:"fingerprint,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

func buildWebhookPayload(cfg config, rule ruleConfig, cluster alertCluster, indexPattern string) genericWebhookPayload {
	labels := map[string]string{
		"source":  "log-alert-job",
		"adapter": "es_v1",
		"kind":    rule.Kind,
		"rule_id": rule.ID,
	}

	annotations := map[string]string{
		"rule_id":          rule.ID,
		"window_seconds":   strconv.Itoa(rule.WindowSeconds),
		"count":            strconv.Itoa(cluster.Count),
		"first_seen_at":    cluster.FirstSeen,
		"last_seen_at":     cluster.LastSeen,
		"sample_trace_ids": strings.Join(cluster.TraceIDs, ","),
		"group_by_key":     cluster.Key,
		"es_index":         indexPattern,
	}

	if rule.RCAEvent.Hints.IncludeESQuery {
		annotations["es_query_string"] = rule.Selector.QueryString
	}

	service := "unknown"
	namespace := "default"
	summaryContext := map[string]string{
		"rule_id":  rule.ID,
		"kind":     rule.Kind,
		"count":    strconv.Itoa(cluster.Count),
		"severity": rule.RCAEvent.Severity,
	}

	switch rule.Kind {
	case ruleKindIngress5XX, ruleKindIngressSlow:
		labels["ingress_env"] = "prod"
		domain := cluster.GroupValues["destination.domain"]
		path := cluster.GroupValues["http.request.uri_path"]
		upstream := cluster.GroupValues["nginx.upstream.address"]
		annotations["domain"] = domain
		annotations["path"] = path
		annotations["upstream"] = upstream
		annotations["sample_request_ids"] = strings.Join(cluster.RequestIDs, ",")
		service = firstNonEmpty(domain, "ingress")
		namespace = "prod"
		summaryContext["domain"] = domain
		summaryContext["path"] = path
		summaryContext["upstream"] = upstream

	case ruleKindMicrosvcError:
		namespaceValue := cluster.GroupValues["k8s.ns"]
		serviceValue := cluster.GroupValues["event.dataset"]
		annotations["namespace"] = namespaceValue
		annotations["service"] = serviceValue
		annotations["msg_examples"] = strings.Join(cluster.MsgExamples, " || ")
		service = firstNonEmpty(serviceValue, "microsvc")
		namespace = firstNonEmpty(namespaceValue, "default")
		summaryContext["namespace"] = namespaceValue
		summaryContext["service"] = serviceValue
	}

	summary := renderSummary(rule.RCAEvent.SummaryTemplate, summaryContext)
	if strings.TrimSpace(summary) == "" {
		summary = fmt.Sprintf("%s detected %d logs in %ds window", rule.ID, cluster.Count, rule.WindowSeconds)
	}

	payload := genericWebhookPayload{
		Namespace:   namespace,
		Service:     service,
		Severity:    firstNonEmpty(rule.RCAEvent.Severity, "P2"),
		Status:      "firing",
		AlertName:   rule.ID,
		Summary:     summary,
		EventTime:   firstNonEmpty(cluster.LastSeen, cluster.FirstSeen),
		Fingerprint: buildFingerprint(rule, cluster),
		Labels:      labels,
		Annotations: annotations,
	}

	return sanitizeAndClampPayload(payload)
}

func buildFingerprint(rule ruleConfig, cluster alertCluster) string {
	kind := strings.TrimSpace(rule.Kind)
	if kind == "" {
		kind = "unknown"
	}

	switch rule.Kind {
	case ruleKindIngress5XX, ruleKindIngressSlow:
		domain := firstNonEmpty(cluster.GroupValues["destination.domain"], "unknown")
		pathHash := stableShortHash(cluster.GroupValues["http.request.uri_path"])
		upstreamHash := stableShortHash(cluster.GroupValues["nginx.upstream.address"])
		return fmt.Sprintf("es:%s:%s:%s:%s", kind, domain, pathHash, upstreamHash)

	case ruleKindMicrosvcError:
		ns := firstNonEmpty(cluster.GroupValues["k8s.ns"], "unknown")
		svc := firstNonEmpty(cluster.GroupValues["event.dataset"], "unknown")
		msgHash := firstNonEmpty(cluster.GroupValues["msg_template_hash"], stableShortHash(cluster.Key))
		return fmt.Sprintf("es:%s:%s:%s:%s", kind, ns, svc, msgHash)

	default:
		return fmt.Sprintf("es:%s:%s", kind, stableShortHash(cluster.Key))
	}
}

func renderSummary(template string, values map[string]string) string {
	trimmed := strings.TrimSpace(template)
	if trimmed == "" {
		return ""
	}

	pairs := make([]string, 0, len(values)*2)
	for key, value := range values {
		pairs = append(pairs, fmt.Sprintf("{{%s}}", key), value)
	}

	replacer := strings.NewReplacer(pairs...)
	return strings.TrimSpace(replacer.Replace(trimmed))
}

func sanitizeAndClampPayload(payload genericWebhookPayload) genericWebhookPayload {
	warnings := make(map[string]struct{})

	payload.Namespace = sanitizePayloadText(payload.Namespace, maxMapValueLength, warnings)
	payload.Service = sanitizePayloadText(payload.Service, maxMapValueLength, warnings)
	payload.AlertName = sanitizePayloadText(payload.AlertName, maxMapValueLength, warnings)
	payload.Severity = sanitizePayloadText(payload.Severity, maxMapValueLength, warnings)
	payload.Summary = sanitizePayloadText(payload.Summary, maxSummaryLength, warnings)
	payload.Fingerprint = sanitizePayloadText(payload.Fingerprint, maxMapValueLength, warnings)
	payload.Labels = sanitizePayloadMap(payload.Labels, warnings)
	payload.Annotations = sanitizePayloadMap(payload.Annotations, warnings)

	payload = clampPayload(payload, warnings)
	addWarnings(&payload, warnings)
	payload = clampPayload(payload, warnings)
	addWarnings(&payload, warnings)
	return payload
}

func clampPayload(payload genericWebhookPayload, warnings map[string]struct{}) genericWebhookPayload {
	if payloadWithinLimit(payload) {
		return payload
	}
	warnings["TRUNCATED"] = struct{}{}

	trimSteps := []func(*genericWebhookPayload){
		func(item *genericWebhookPayload) {
			shrinkAnnotation(item.Annotations, "msg_examples", maxPayloadFieldShort)
		},
		func(item *genericWebhookPayload) {
			shrinkAnnotation(item.Annotations, "sample_request_ids", maxPayloadFieldShort)
		},
		func(item *genericWebhookPayload) {
			shrinkAnnotation(item.Annotations, "sample_trace_ids", maxPayloadFieldShort)
		},
		func(item *genericWebhookPayload) {
			delete(item.Annotations, "es_query_string")
		},
		func(item *genericWebhookPayload) {
			shrinkAnnotation(item.Annotations, "group_by_key", maxPayloadFieldShort)
		},
		func(item *genericWebhookPayload) {
			item.Summary = truncateString(item.Summary, maxPayloadFieldShort)
		},
		func(item *genericWebhookPayload) {
			for key, value := range item.Annotations {
				item.Annotations[key] = truncateString(value, maxPayloadFieldExtraShort)
			}
		},
		func(item *genericWebhookPayload) {
			item.Labels = keepEssentialLabels(item.Labels)
		},
		func(item *genericWebhookPayload) {
			item.Annotations = keepEssentialAnnotations(item.Annotations)
		},
	}

	for _, step := range trimSteps {
		if payloadWithinLimit(payload) {
			return payload
		}
		step(&payload)
		addWarnings(&payload, warnings)
	}

	if !payloadWithinLimit(payload) {
		payload.Summary = truncateString(payload.Summary, maxPayloadFieldExtraShort)
		payload.Annotations = keepEssentialAnnotations(payload.Annotations)
		addWarnings(&payload, warnings)
	}

	return payload
}

func keepEssentialLabels(labels map[string]string) map[string]string {
	if len(labels) == 0 {
		return map[string]string{}
	}

	essential := map[string]struct{}{
		"source":      {},
		"adapter":     {},
		"kind":        {},
		"rule_id":     {},
		"ingress_env": {},
	}
	out := make(map[string]string)
	for key, value := range labels {
		if _, ok := essential[key]; ok {
			out[key] = value
		}
	}
	return out
}

func keepEssentialAnnotations(annotations map[string]string) map[string]string {
	if len(annotations) == 0 {
		return map[string]string{}
	}

	essential := []string{
		"rule_id",
		"count",
		"sample_trace_ids",
		"warnings",
	}
	out := make(map[string]string, len(essential))
	for _, key := range essential {
		if value, ok := annotations[key]; ok {
			out[key] = value
		}
	}
	return out
}

func addWarnings(payload *genericWebhookPayload, warnings map[string]struct{}) {
	if len(warnings) == 0 {
		return
	}
	if payload.Annotations == nil {
		payload.Annotations = map[string]string{}
	}

	warningValues := make([]string, 0, len(warnings))
	for warning := range warnings {
		warningValues = append(warningValues, warning)
	}
	sort.Strings(warningValues)
	payload.Annotations["warnings"] = strings.Join(warningValues, ",")
}

func payloadWithinLimit(payload genericWebhookPayload) bool {
	raw, _ := json.Marshal(payload)
	return len(raw) <= maxPayloadBytes
}

func sanitizePayloadMap(input map[string]string, warnings map[string]struct{}) map[string]string {
	if len(input) == 0 {
		return map[string]string{}
	}

	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	output := make(map[string]string, len(input))
	for _, key := range keys {
		normalizedKey := strings.TrimSpace(strings.ToLower(key))
		if normalizedKey == "" {
			continue
		}
		if containsSensitiveWord(normalizedKey) {
			warnings["REDACTION_APPLIED"] = struct{}{}
			continue
		}

		sanitized := sanitizePayloadText(input[key], maxMapValueLength, warnings)
		if sanitized == "" {
			continue
		}
		output[normalizedKey] = sanitized
	}
	return output
}

func sanitizePayloadText(raw string, maxLength int, warnings map[string]struct{}) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if containsSensitiveWord(trimmed) {
		warnings["REDACTION_APPLIED"] = struct{}{}
		return "[redacted]"
	}
	if len(trimmed) > maxLength {
		warnings["TRUNCATED"] = struct{}{}
		return trimmed[:maxLength]
	}
	return trimmed
}

func containsSensitiveWord(raw string) bool {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return false
	}
	for _, token := range []string{"secret", "token", "authorization", "header", "headers"} {
		if strings.Contains(value, token) {
			return true
		}
	}
	return false
}

func shrinkAnnotation(annotations map[string]string, key string, targetLen int) {
	if len(annotations) == 0 {
		return
	}
	current, ok := annotations[key]
	if !ok {
		return
	}
	annotations[key] = truncateString(current, targetLen)
}

func truncateString(value string, targetLen int) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if targetLen <= 0 {
		return ""
	}
	if len(trimmed) <= targetLen {
		return trimmed
	}
	if targetLen <= 3 {
		return trimmed[:targetLen]
	}
	return trimmed[:targetLen-3] + "..."
}

func stableShortHash(raw string) string {
	normalized := strings.TrimSpace(raw)
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:6])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}
