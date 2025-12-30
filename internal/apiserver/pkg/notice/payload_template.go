package notice

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"zk8s.com/rca-api/internal/apiserver/model"
)

type payloadTemplateConfig struct {
	mode               string
	includeDiagnosis   bool
	includeEvidenceIDs bool
	includeRootCause   bool
	includeLinks       bool
}

type payloadRenderMetadata struct {
	deliveryID string
}

type diagnosisSnapshot struct {
	confidence       float64
	rootCauseType    string
	rootCauseSummary string
	evidenceIDs      []string
	missingEvidence  []string
}

var errNoticePayloadInvalid = errors.New("invalid notice payload input")

func buildPayloadForChannel(rq DispatchRequest, channel *model.NoticeChannelM) ([]byte, error) {
	return buildPayloadForChannelWithMetadata(rq, channel, payloadRenderMetadata{})
}

//nolint:gocognit // Payload assembly branches stay explicit for policy auditability.
func buildPayloadForChannelWithMetadata(
	rq DispatchRequest,
	channel *model.NoticeChannelM,
	metadata payloadRenderMetadata,
) ([]byte, error) {

	if rq.Incident == nil {
		return nil, fmt.Errorf("%w: nil incident", errNoticePayloadInvalid)
	}
	if channel == nil {
		return nil, fmt.Errorf("%w: nil channel", errNoticePayloadInvalid)
	}

	eventType := strings.ToLower(strings.TrimSpace(rq.EventType))
	occurredAt := rq.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}

	incident := rq.Incident
	diagnosis := buildDiagnosisSnapshot(rq)
	payload := map[string]any{
		"event_type":  eventType,
		"timestamp":   occurredAt.UTC().Format(time.RFC3339),
		"occurred_at": occurredAt.UTC().Format(time.RFC3339),
		"incident": map[string]any{
			"incident_id": incident.IncidentID,
			"namespace":   incident.Namespace,
			"service":     incident.Service,
			"severity":    incident.Severity,
			"rca_status":  incident.RCAStatus,
		},
		"notice": map[string]any{
			"channel_id":  strings.TrimSpace(channel.ChannelID),
			"delivery_id": strings.TrimSpace(metadata.deliveryID),
			"attempt":     0,
			"status":      DeliveryStatusPending,
		},
		"summary": buildPayloadSummaryWithTemplate(eventType, incident, channel, rq, diagnosis, metadata),
	}

	if eventType == EventTypeDiagnosisWritten {
		jobID := strings.TrimSpace(rq.JobID)
		if jobID != "" {
			payload["job"] = map[string]any{
				"job_id": jobID,
			}
		}
	}

	template := buildPayloadTemplateConfig(channel)
	applyPayloadTemplate(payload, rq, template, diagnosis)
	if template.includeLinks {
		links, linksOmitted := buildPayloadLinks(rq, channel, metadata)
		if len(links) > 0 {
			payload["links"] = links
		}
		if linksOmitted {
			payload["links_omitted"] = true
		}
	}

	return marshalPayloadWithGuardrails(payload)
}

func buildPayloadTemplateConfig(channel *model.NoticeChannelM) payloadTemplateConfig {
	if channel == nil {
		return payloadTemplateConfig{mode: NoticePayloadModeCompact}
	}
	return payloadTemplateConfig{
		mode:               normalizePayloadMode(channel.PayloadMode),
		includeDiagnosis:   channel.IncludeDiagnosis,
		includeEvidenceIDs: channel.IncludeEvidenceIDs,
		includeRootCause:   channel.IncludeRootCause,
		includeLinks:       channel.IncludeLinks,
	}
}

func normalizePayloadMode(mode string) string {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case NoticePayloadModeFull:
		return NoticePayloadModeFull
	case NoticePayloadModeCompact:
		return NoticePayloadModeCompact
	default:
		return NoticePayloadModeCompact
	}
}

func buildPayloadSummary(eventType string, incident *model.IncidentM) string {
	if incident == nil {
		return truncateString(strings.TrimSpace(eventType), NoticePayloadStringMax)
	}
	summary := fmt.Sprintf(
		"%s incident=%s severity=%s service=%s",
		eventType,
		strings.TrimSpace(incident.IncidentID),
		strings.TrimSpace(incident.Severity),
		strings.TrimSpace(incident.Service),
	)
	return truncateString(strings.TrimSpace(summary), NoticePayloadStringMax)
}

func buildPayloadSummaryWithTemplate(
	eventType string,
	incident *model.IncidentM,
	channel *model.NoticeChannelM,
	rq DispatchRequest,
	diagnosis diagnosisSnapshot,
	metadata payloadRenderMetadata,
) string {

	defaultSummary := buildPayloadSummary(eventType, incident)
	if channel == nil {
		return defaultSummary
	}
	template := strings.TrimSpace(derefString(channel.SummaryTemplate))
	if template == "" {
		return defaultSummary
	}
	template = truncateString(template, NoticePayloadStringMax)
	values := map[string]string{
		"event_type":         strings.ToLower(strings.TrimSpace(eventType)),
		"incident_id":        strings.TrimSpace(incident.IncidentID),
		"namespace":          strings.TrimSpace(incident.Namespace),
		"service":            strings.TrimSpace(incident.Service),
		"severity":           strings.TrimSpace(incident.Severity),
		"rca_status":         strings.TrimSpace(incident.RCAStatus),
		"root_cause_type":    strings.TrimSpace(diagnosis.rootCauseType),
		"root_cause_summary": strings.TrimSpace(diagnosis.rootCauseSummary),
		"confidence":         formatSummaryTemplateConfidence(diagnosis.confidence),
		"delivery_id":        strings.TrimSpace(metadata.deliveryID),
		"channel_id":         strings.TrimSpace(channel.ChannelID),
		"job_id":             strings.TrimSpace(rq.JobID),
	}

	out := replaceSummaryTemplateVars(template, values)
	return truncateString(strings.TrimSpace(out), NoticePayloadStringMax)
}

func formatSummaryTemplateConfidence(v float64) string {
	return strconv.FormatFloat(clampConfidence(v), 'f', -1, 64)
}

//nolint:gocognit,gocyclo // Placeholder parsing is intentionally explicit for guardrails.
func replaceSummaryTemplateVars(template string, variables map[string]string) string {
	if template == "" {
		return ""
	}

	var out strings.Builder
	out.Grow(len(template))
	replacements := 0

	for idx := 0; idx < len(template); {
		if template[idx] != '$' || idx+1 >= len(template) || template[idx+1] != '{' {
			out.WriteByte(template[idx])
			idx++
			continue
		}

		end := idx + 2
		for end < len(template) && template[end] != '}' {
			end++
		}
		if end >= len(template) {
			out.WriteByte(template[idx])
			idx++
			continue
		}

		key := template[idx+2 : end]
		token := template[idx : end+1]
		if !isSummaryTemplateVarName(key) {
			out.WriteString(token)
			idx = end + 1
			continue
		}

		if replacements >= NoticePayloadTemplateReplacementMax {
			out.WriteString(template[idx:])
			break
		}

		if value, ok := variables[key]; ok {
			out.WriteString(value)
		} else {
			// Keep unknown placeholder unchanged for easier template debugging.
			out.WriteString(token)
		}
		replacements++
		idx = end + 1
	}

	return out.String()
}

func isSummaryTemplateVarName(name string) bool {
	if name == "" {
		return false
	}
	for i := range len(name) {
		c := name[i]
		if isSummaryTemplateAlphaNumUnderscore(c) {
			continue
		}
		return false
	}
	return true
}

func isSummaryTemplateAlphaNumUnderscore(c byte) bool {
	return (c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') ||
		c == '_'
}

func buildDiagnosisSnapshot(rq DispatchRequest) diagnosisSnapshot {
	out := diagnosisSnapshot{
		confidence:       clampConfidence(rq.DiagnosisConfidence),
		rootCauseType:    truncateString(strings.ToLower(strings.TrimSpace(derefString(rq.Incident.RootCauseType))), NoticePayloadStringMax),
		rootCauseSummary: truncateString(strings.TrimSpace(derefString(rq.Incident.RootCauseSummary)), NoticePayloadStringMax),
		evidenceIDs:      limitStringSlice(normalizeStringSlice(rq.DiagnosisEvidenceID), NoticePayloadEvidenceIDsMax),
	}

	raw := strings.TrimSpace(derefString(rq.Incident.DiagnosisJSON))
	if raw == "" {
		return out
	}
	root, topMissing, hypothesisEvidence, hypothesisMissing := parseDiagnosisJSON(raw)
	if root.confidence >= 0 {
		out.confidence = root.confidence
	}
	if root.rootCauseType != "" {
		out.rootCauseType = root.rootCauseType
	}
	if root.rootCauseSummary != "" {
		out.rootCauseSummary = root.rootCauseSummary
	}

	out.evidenceIDs = limitStringSlice(normalizeStringSlice(append(out.evidenceIDs, append(root.evidenceIDs, hypothesisEvidence...)...)), NoticePayloadEvidenceIDsMax)
	out.missingEvidence = limitStringSlice(normalizeStringSlice(append(topMissing, hypothesisMissing...)), NoticePayloadMissingEvidenceMax)
	return out
}

func parseDiagnosisJSON(raw string) (diagnosisSnapshot, []string, []string, []string) {
	out := diagnosisSnapshot{confidence: -1}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return out, nil, nil, nil
	}

	root := asMap(payload["root_cause"])
	if root != nil {
		out.rootCauseType = truncateString(strings.ToLower(strings.TrimSpace(asString(root["type"]))), NoticePayloadStringMax)
		summary := strings.TrimSpace(asString(root["summary"]))
		if summary == "" {
			summary = strings.TrimSpace(asString(root["statement"]))
		}
		out.rootCauseSummary = truncateString(summary, NoticePayloadStringMax)
		if confidence, ok := asFloat64(root["confidence"]); ok {
			out.confidence = clampConfidence(confidence)
		}
		out.evidenceIDs = limitStringSlice(normalizeStringSlice(stringSliceFromAny(root["evidence_ids"])), NoticePayloadEvidenceIDsMax)
	}

	topMissing := limitStringSlice(normalizeStringSlice(stringSliceFromAny(payload["missing_evidence"])), NoticePayloadMissingEvidenceMax)
	var hypothesisEvidence []string
	var hypothesisMissing []string

	for _, rawHypothesis := range asSlice(payload["hypotheses"]) {
		hypothesis := asMap(rawHypothesis)
		if hypothesis == nil {
			continue
		}
		hypothesisEvidence = append(hypothesisEvidence, stringSliceFromAny(hypothesis["supporting_evidence_ids"])...)
		hypothesisMissing = append(hypothesisMissing, stringSliceFromAny(hypothesis["missing_evidence"])...)
	}

	return out,
		topMissing,
		limitStringSlice(normalizeStringSlice(hypothesisEvidence), NoticePayloadEvidenceIDsMax),
		limitStringSlice(normalizeStringSlice(hypothesisMissing), NoticePayloadMissingEvidenceMax)
}

func applyPayloadTemplate(
	payload map[string]any,
	rq DispatchRequest,
	template payloadTemplateConfig,
	diagnosis diagnosisSnapshot,
) {

	switch template.mode {
	case NoticePayloadModeFull:
		applyFullPayloadTemplate(payload, rq, template, diagnosis)
	default:
		applyCompactPayloadTemplate(payload, template, diagnosis)
	}
}

func applyCompactPayloadTemplate(payload map[string]any, template payloadTemplateConfig, diagnosis diagnosisSnapshot) {
	if template.includeRootCause && diagnosis.rootCauseSummary != "" {
		payload["root_cause_summary"] = diagnosis.rootCauseSummary
	}
	if !template.includeDiagnosis {
		return
	}

	diagnosisMin := map[string]any{
		"confidence": diagnosis.confidence,
	}
	rootCause := compactRootCause(diagnosis)
	if len(rootCause) > 0 {
		diagnosisMin["root_cause"] = rootCause
	}
	if len(diagnosis.missingEvidence) > 0 {
		diagnosisMin["missing_evidence"] = diagnosis.missingEvidence
	}
	payload["diagnosis_min"] = diagnosisMin
}

//nolint:gocognit // Template branches stay explicit for policy auditability.
func applyFullPayloadTemplate(payload map[string]any, rq DispatchRequest, template payloadTemplateConfig, diagnosis diagnosisSnapshot) {
	if template.includeRootCause {
		rootCause := compactRootCause(diagnosis)
		if len(rootCause) > 0 {
			payload["root_cause"] = rootCause
		}
	}
	if template.includeEvidenceIDs {
		payload["evidence_ids"] = diagnosis.evidenceIDs
	}
	if !template.includeDiagnosis {
		return
	}

	diagnosisPayload := map[string]any{
		"confidence": diagnosis.confidence,
	}
	rootCause := compactRootCause(diagnosis)
	if len(rootCause) > 0 {
		diagnosisPayload["root_cause"] = rootCause
	}
	if len(diagnosis.evidenceIDs) > 0 {
		diagnosisPayload["evidence_ids"] = diagnosis.evidenceIDs
	}
	if len(diagnosis.missingEvidence) > 0 {
		diagnosisPayload["missing_evidence"] = diagnosis.missingEvidence
	}
	payload["diagnosis"] = diagnosisPayload

	if strings.EqualFold(strings.TrimSpace(rq.EventType), EventTypeDiagnosisWritten) {
		jobID := strings.TrimSpace(rq.JobID)
		if jobID != "" {
			payload["job"] = map[string]any{
				"job_id": jobID,
			}
		}
	}
}

func compactRootCause(d diagnosisSnapshot) map[string]any {
	rootCause := map[string]any{}
	if d.rootCauseType != "" {
		rootCause["type"] = d.rootCauseType
	}
	if d.rootCauseSummary != "" {
		rootCause["summary"] = d.rootCauseSummary
	}
	return rootCause
}

func buildPayloadLinks(
	rq DispatchRequest,
	channel *model.NoticeChannelM,
	metadata payloadRenderMetadata,
) (map[string]any, bool) {

	baseURL := resolvePayloadLinksBaseURL(channel)
	if baseURL == "" {
		return nil, true
	}

	links := map[string]any{
		"version":  "v1",
		"base_url": baseURL,
	}

	incidentID := strings.TrimSpace(rq.Incident.IncidentID)
	if incidentID != "" {
		pathIncidentID := url.PathEscape(incidentID)
		links["incident_url"] = joinPayloadLinksURL(baseURL, "/v1/incidents/"+pathIncidentID)
		links["evidence_list_url"] = joinPayloadLinksURL(baseURL, "/v1/incidents/"+pathIncidentID+"/evidence")
	}
	if deliveryID := strings.TrimSpace(metadata.deliveryID); deliveryID != "" {
		links["delivery_url"] = joinPayloadLinksURL(baseURL, "/v1/notice-deliveries/"+url.PathEscape(deliveryID))
	}
	if channelID := strings.TrimSpace(channel.ChannelID); channelID != "" {
		links["channel_url"] = joinPayloadLinksURL(baseURL, "/v1/notice-channels/"+url.PathEscape(channelID))
	}
	if jobID := strings.TrimSpace(rq.JobID); jobID != "" {
		links["job_url"] = joinPayloadLinksURL(baseURL, "/v1/ai/jobs/"+url.PathEscape(jobID))
	}
	return links, false
}

func resolvePayloadLinksBaseURL(channel *model.NoticeChannelM) string {
	if channel != nil {
		if baseURL := normalizePayloadLinksBaseURL(derefString(channel.BaseURL)); baseURL != "" {
			return baseURL
		}
	}
	return normalizePayloadLinksBaseURL(configuredNoticeBaseURL())
}

func normalizePayloadLinksBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if (scheme != "http" && scheme != "https") || strings.TrimSpace(parsed.Host) == "" {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/")
}

func joinPayloadLinksURL(baseURL string, path string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	path = strings.TrimSpace(path)
	if baseURL == "" {
		return path
	}
	return baseURL + path
}

//nolint:gocognit // Guardrail fallback sequence is intentionally explicit.
func marshalPayloadWithGuardrails(payload map[string]any) ([]byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(raw) <= NoticePayloadMaxBytes {
		return raw, nil
	}

	payload["truncated"] = true
	raw, err = json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(raw) <= NoticePayloadMaxBytes {
		return raw, nil
	}

	for range 32 {
		if len(raw) <= NoticePayloadMaxBytes {
			break
		}
		if !shrinkPayload(payload) {
			payload = minimalTruncatedPayload(payload)
		}
		raw, err = json.Marshal(payload)
		if err != nil {
			return nil, err
		}
	}

	if len(raw) > NoticePayloadMaxBytes {
		raw = []byte(truncateString(string(raw), NoticePayloadMaxBytes))
	}
	return raw, nil
}

//nolint:gocognit,gocyclo // Ordered shrinking steps are explicit by design.
func shrinkPayload(payload map[string]any) bool {
	if diagnosis, ok := payload["diagnosis"].(map[string]any); ok {
		if shrinkStringSliceField(diagnosis, "missing_evidence") {
			return true
		}
		if shrinkStringSliceField(diagnosis, "evidence_ids") {
			return true
		}
		delete(payload, "diagnosis")
		return true
	}
	if diagnosisMin, ok := payload["diagnosis_min"].(map[string]any); ok {
		if shrinkStringSliceField(diagnosisMin, "missing_evidence") {
			return true
		}
		delete(payload, "diagnosis_min")
		return true
	}
	if shrinkStringSliceField(payload, "evidence_ids") {
		return true
	}
	if shrinkStringField(payload, "root_cause_summary") {
		return true
	}
	if _, ok := payload["root_cause"]; ok {
		delete(payload, "root_cause")
		return true
	}
	if _, ok := payload["links"]; ok {
		delete(payload, "links")
		return true
	}
	if shrinkStringField(payload, "summary") {
		return true
	}
	return false
}

func minimalTruncatedPayload(payload map[string]any) map[string]any {
	out := map[string]any{
		"truncated": true,
	}
	if eventType, ok := payload["event_type"]; ok {
		out["event_type"] = eventType
	}
	if timestamp, ok := payload["timestamp"]; ok {
		out["timestamp"] = timestamp
	}
	if incident, ok := payload["incident"]; ok {
		out["incident"] = incident
	}
	if notice, ok := payload["notice"]; ok {
		out["notice"] = notice
	}
	if summary, ok := payload["summary"].(string); ok {
		out["summary"] = truncateString(summary, 128)
	}
	return out
}

func shrinkStringSliceField(container map[string]any, key string) bool {
	raw, ok := container[key]
	if !ok {
		return false
	}
	items := stringSliceFromAny(raw)
	if len(items) == 0 {
		delete(container, key)
		return true
	}
	if len(items) == 1 {
		delete(container, key)
		return true
	}
	nextLen := len(items) / 2
	if nextLen <= 0 {
		delete(container, key)
		return true
	}
	container[key] = items[:nextLen]
	return true
}

func shrinkStringField(container map[string]any, key string) bool {
	raw, ok := container[key]
	if !ok {
		return false
	}
	value, ok := raw.(string)
	if !ok {
		delete(container, key)
		return true
	}
	value = strings.TrimSpace(value)
	if len(value) <= 64 {
		delete(container, key)
		return true
	}
	container[key] = truncateString(value, len(value)/2)
	return true
}

func clampConfidence(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func limitStringSlice(items []string, limit int) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		trimmed := truncateString(strings.TrimSpace(item), NoticePayloadStringMax)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func asMap(v any) map[string]any {
	out, _ := v.(map[string]any)
	return out
}

func asSlice(v any) []any {
	out, _ := v.([]any)
	return out
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	default:
		return ""
	}
}

//nolint:wsl_v5 // Type-switch returns are intentionally direct.
func asFloat64(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int64:
		return float64(t), true
	case int32:
		return float64(t), true
	case int:
		return float64(t), true
	case json.Number:
		out, err := t.Float64()

		return out, err == nil
	case string:
		out, err := json.Number(strings.TrimSpace(t)).Float64()

		return out, err == nil
	}
	return 0, false
}

//nolint:wsl_v5 // Type-switch returns are intentionally direct.
func stringSliceFromAny(v any) []string {
	switch raw := v.(type) {
	case []string:
		return normalizeStringSlice(raw)
	case []any:
		out := make([]string, 0, len(raw))
		for _, item := range raw {
			switch s := item.(type) {
			case string:
				out = append(out, s)
			case json.Number:
				out = append(out, s.String())
			}
		}

		return normalizeStringSlice(out)
	default:
		return nil
	}
}
