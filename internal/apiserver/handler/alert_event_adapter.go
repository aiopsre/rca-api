package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"
	"github.com/onexstack/onexstack/pkg/errorsx"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aiopsre/rca-api/internal/apiserver/pkg/authz"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

const (
	adapterGenericV1              = "generic_v1"
	adapterPrometheusAlertmanager = "prometheus_alertmanager"
	adapterAlertmanagerAlias      = "alertmanager"

	adapterDefaultNamespace = "default"
	adapterDefaultService   = "unknown"
	adapterDefaultSeverity  = "warning"

	adapterMaxScopeLen       = 128
	adapterMaxAlertNameLen   = 128
	adapterMaxSummaryLen     = 256
	adapterMaxMapKeyLen      = 64
	adapterMaxMapValueLen    = 256
	adapterMaxMapEntries     = 32
	adapterMaxMapJSONBytes   = 4096
	adapterFingerprintPrefix = "adapter-fp:"
)

var adapterFingerprintLabelAllowList = map[string]struct{}{
	"alertname":   {},
	"severity":    {},
	"cluster":     {},
	"namespace":   {},
	"service":     {},
	"workload":    {},
	"job":         {},
	"environment": {},
	"team":        {},
	"region":      {},
}

func (h *Handler) IngestAlertEventByAdapter(c *gin.Context) {
	if err := authz.RequireAnyScope(c, authz.ScopeAlertIngest); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	adapter := normalizeAdapterName(c.Param("adapter"))
	if adapter == "" {
		core.WriteResponse(c, nil, errorsx.ErrInvalidArgument)
		return
	}

	var payload map[string]any
	if err := c.ShouldBindJSON(&payload); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	req, err := buildAdapterIngestRequest(adapter, payload, time.Now().UTC())
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	if req.IdempotencyKey == nil {
		if key := strings.TrimSpace(c.GetHeader("Idempotency-Key")); key != "" {
			req.IdempotencyKey = &key
		}
	}
	if err := h.val.ValidateIngestAlertEventRequest(c.Request.Context(), req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	resp, err := h.biz.AlertEventV1().IngestByAdapter(c.Request.Context(), adapter, req)
	core.WriteResponse(c, resp, err)
}

func buildAdapterIngestRequest(adapter string, payload map[string]any, now time.Time) (*v1.IngestAlertEventRequest, error) {
	adapter = normalizeAdapterName(adapter)
	switch adapter {
	case adapterGenericV1:
		return buildGenericV1IngestRequest(payload, now), nil
	case adapterPrometheusAlertmanager:
		return buildAlertmanagerIngestRequest(payload, now)
	default:
		return nil, errorsx.ErrInvalidArgument
	}
}

func buildGenericV1IngestRequest(payload map[string]any, now time.Time) *v1.IngestAlertEventRequest {
	labels := sanitizeAdapterMap(toStringMap(payload["labels"]))
	annotations := sanitizeAdapterMap(toStringMap(payload["annotations"]))

	alertName := sanitizeAdapterText(adapterFirstNonEmpty(adapterString(payload["alertname"]), labels["alertname"]), adapterMaxAlertNameLen)
	namespace := sanitizeAdapterText(adapterFirstNonEmpty(adapterString(payload["namespace"]), labels["namespace"], adapterDefaultNamespace), adapterMaxScopeLen)
	service := sanitizeAdapterText(adapterFirstNonEmpty(adapterString(payload["service"]), labels["service"], adapterDefaultService), adapterMaxScopeLen)
	severity := sanitizeAdapterText(adapterFirstNonEmpty(adapterString(payload["severity"]), labels["severity"], adapterDefaultSeverity), adapterMaxMapValueLen)
	status := normalizeAdapterStatus(adapterString(payload["status"]))
	eventTime := parseAdapterTimestamp(adapterString(payload["event_time"]), now)

	summary := sanitizeAdapterText(adapterFirstNonEmpty(adapterString(payload["summary"]), annotations["summary"]), adapterMaxSummaryLen)
	if summary != "" {
		annotations["summary"] = summary
	}

	fingerprint := sanitizeAdapterText(adapterString(payload["fingerprint"]), 128)
	if fingerprint == "" {
		fingerprint = buildAdapterFingerprint(namespace, service, alertName, labels)
	}

	return buildAdapterRequestCommon(adapterGenericV1, namespace, service, severity, status, alertName, eventTime, fingerprint, labels, annotations)
}

func buildAlertmanagerIngestRequest(payload map[string]any, now time.Time) (*v1.IngestAlertEventRequest, error) {
	alerts := toAnySlice(payload["alerts"])
	if len(alerts) == 0 {
		return nil, errorsx.ErrInvalidArgument
	}

	selected := toAnyMap(alerts[0])
	for _, item := range alerts {
		candidate := toAnyMap(item)
		status := normalizeAdapterStatus(adapterString(candidate["status"]))
		if status == "firing" || status == "resolved" || status == "suppressed" {
			selected = candidate
			break
		}
	}

	commonLabels := sanitizeAdapterMap(toStringMap(payload["commonLabels"]))
	commonAnnotations := sanitizeAdapterMap(toStringMap(payload["commonAnnotations"]))
	labels := mergeStringMap(commonLabels, sanitizeAdapterMap(toStringMap(selected["labels"])))
	annotations := mergeStringMap(commonAnnotations, sanitizeAdapterMap(toStringMap(selected["annotations"])))

	alertName := sanitizeAdapterText(adapterFirstNonEmpty(labels["alertname"], adapterString(payload["alertname"])), adapterMaxAlertNameLen)
	namespace := sanitizeAdapterText(adapterFirstNonEmpty(labels["namespace"], adapterDefaultNamespace), adapterMaxScopeLen)
	service := sanitizeAdapterText(adapterFirstNonEmpty(labels["service"], adapterDefaultService), adapterMaxScopeLen)
	severity := sanitizeAdapterText(adapterFirstNonEmpty(labels["severity"], adapterDefaultSeverity), adapterMaxMapValueLen)
	status := normalizeAdapterStatus(adapterFirstNonEmpty(adapterString(selected["status"]), adapterString(payload["status"])))

	eventTime := parseAdapterTimestamp(adapterString(selected["startsAt"]), now)
	if status == "resolved" {
		eventTime = parseAdapterTimestamp(adapterString(selected["endsAt"]), parseAdapterTimestamp(adapterString(selected["startsAt"]), now))
	}

	summary := sanitizeAdapterText(adapterFirstNonEmpty(annotations["summary"], annotations["description"]), adapterMaxSummaryLen)
	if summary != "" {
		annotations["summary"] = summary
	}

	fingerprint := sanitizeAdapterText(adapterString(selected["fingerprint"]), 128)
	if fingerprint == "" {
		fingerprint = buildAdapterFingerprint(namespace, service, alertName, labels)
	}

	return buildAdapterRequestCommon(
		adapterPrometheusAlertmanager,
		namespace,
		service,
		severity,
		status,
		alertName,
		eventTime,
		fingerprint,
		labels,
		annotations,
	), nil
}

func buildAdapterRequestCommon(
	adapter string,
	namespace string,
	service string,
	severity string,
	status string,
	alertName string,
	eventTime time.Time,
	fingerprint string,
	labels map[string]string,
	annotations map[string]string,
) *v1.IngestAlertEventRequest {

	status = normalizeAdapterStatus(status)
	if status == "" {
		status = "firing"
	}
	if severity == "" {
		severity = adapterDefaultSeverity
	}
	if namespace == "" {
		namespace = adapterDefaultNamespace
	}
	if service == "" {
		service = adapterDefaultService
	}

	req := &v1.IngestAlertEventRequest{
		Fingerprint: strPtr(fingerprint),
		Source:      strPtr(adapter),
		Status:      status,
		Severity:    severity,
		AlertName:   strPtr(alertName),
		Service:     strPtr(service),
		Namespace:   strPtr(namespace),
		LastSeenAt:  timestamppb.New(eventTime.UTC()),
		LabelsJSON:  encodeAdapterMapJSON(labels),
	}
	if len(annotations) > 0 {
		req.AnnotationsJSON = encodeAdapterMapJSON(annotations)
	}
	return req
}

func encodeAdapterMapJSON(data map[string]string) *string {
	if len(data) == 0 {
		return nil
	}
	clamped := clampAdapterJSONMap(data, adapterMaxMapJSONBytes)
	if len(clamped) == 0 {
		return nil
	}
	raw, _ := json.Marshal(clamped)
	if len(raw) == 0 {
		return nil
	}
	value := string(raw)
	return &value
}

func clampAdapterJSONMap(in map[string]string, maxBytes int) map[string]string {
	if len(in) == 0 || maxBytes <= 0 {
		return map[string]string{}
	}
	keys := sortedKeys(in)
	out := make(map[string]string, len(in))
	for _, key := range keys {
		out[key] = in[key]
		raw, _ := json.Marshal(out)
		if len(raw) > maxBytes {
			delete(out, key)
			break
		}
	}
	return out
}

func sanitizeAdapterMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	keys := sortedKeys(in)
	out := make(map[string]string, minInt(len(keys), adapterMaxMapEntries))
	for _, rawKey := range keys {
		if len(out) >= adapterMaxMapEntries {
			break
		}
		key := strings.ToLower(sanitizeAdapterText(rawKey, adapterMaxMapKeyLen))
		if key == "" || isSensitiveWord(key) {
			continue
		}
		value := sanitizeAdapterText(in[rawKey], adapterMaxMapValueLen)
		if value == "" {
			continue
		}
		out[key] = value
	}
	return out
}

func buildAdapterFingerprint(namespace string, service string, alertName string, labels map[string]string) string {
	stable := map[string]string{
		"namespace": sanitizeAdapterText(namespace, adapterMaxScopeLen),
		"service":   sanitizeAdapterText(service, adapterMaxScopeLen),
		"alertname": sanitizeAdapterText(alertName, adapterMaxAlertNameLen),
	}
	for key, value := range labels {
		k := strings.ToLower(strings.TrimSpace(key))
		if _, ok := adapterFingerprintLabelAllowList[k]; !ok {
			continue
		}
		if isSensitiveWord(k) {
			continue
		}
		stable[k] = sanitizeAdapterText(value, adapterMaxMapValueLen)
	}

	keys := sortedKeys(stable)
	var b strings.Builder
	for _, key := range keys {
		value := strings.TrimSpace(stable[key])
		if value == "" {
			continue
		}
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(value)
		b.WriteByte('\n')
	}

	sum := sha256.Sum256([]byte(b.String()))
	return adapterFingerprintPrefix + hex.EncodeToString(sum[:16])
}

func mergeStringMap(base map[string]string, override map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(override))
	maps.Copy(out, base)
	maps.Copy(out, override)
	return out
}

func normalizeAdapterName(adapter string) string {
	name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(adapter, ":")))
	switch name {
	case adapterAlertmanagerAlias:
		return adapterPrometheusAlertmanager
	case adapterGenericV1, adapterPrometheusAlertmanager:
		return name
	default:
		return ""
	}
}

func normalizeAdapterStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "firing":
		return "firing"
	case "resolved":
		return "resolved"
	case "suppressed":
		return "suppressed"
	default:
		return ""
	}
}

func parseAdapterTimestamp(raw string, fallback time.Time) time.Time {
	normalized := strings.TrimSpace(raw)
	if normalized == "" {
		return fallback.UTC()
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, normalized); err == nil {
			return parsed.UTC()
		}
	}
	return fallback.UTC()
}

func adapterString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []byte:
		return strings.TrimSpace(string(typed))
	default:
		return ""
	}
}

func toAnyMap(value any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	switch typed := value.(type) {
	case map[string]any:
		return typed

	default:
		return map[string]any{}
	}
}

func toAnySlice(value any) []any {
	if value == nil {
		return nil
	}
	if out, ok := value.([]any); ok {
		return out
	}
	return nil
}

func toStringMap(value any) map[string]string {
	out := map[string]string{}
	switch typed := value.(type) {
	case map[string]string:
		maps.Copy(out, typed)

	case map[string]any:
		maps.Copy(out, stringifyAnyMap(typed))
	}
	return out
}

func stringifyAnyMap(data map[string]any) map[string]string {
	out := make(map[string]string, len(data))
	for key, item := range data {
		if value := stringifyAny(item); value != "" {
			out[key] = value
		}
	}
	return out
}

func stringifyAny(value any) string {
	if value == nil {
		return ""
	}
	if casted, ok := value.(string); ok {
		return casted
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(raw)
}

func sortedKeys(data map[string]string) []string {
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func isSensitiveWord(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	if lower == "" {
		return false
	}
	return strings.Contains(lower, "secret") ||
		strings.Contains(lower, "token") ||
		strings.Contains(lower, "authorization") ||
		strings.Contains(lower, "header")
}

func sanitizeAdapterText(raw string, maxLen int) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if isSensitiveWord(trimmed) {
		return "[redacted]"
	}
	if maxLen > 0 && len(trimmed) > maxLen {
		return trimmed[:maxLen]
	}
	return trimmed
}

func adapterFirstNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func strPtr(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}
