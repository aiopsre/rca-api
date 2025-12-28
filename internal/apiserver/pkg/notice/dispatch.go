package notice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"time"

	"zk8s.com/rca-api/internal/apiserver/model"
	"zk8s.com/rca-api/internal/apiserver/pkg/metrics"
	"zk8s.com/rca-api/internal/apiserver/store"
)

// DispatchRequest describes one notice event to enqueue.
type DispatchRequest struct {
	EventType string
	Incident  *model.IncidentM
	JobID     string

	DiagnosisConfidence float64
	DiagnosisEvidenceID []string

	OccurredAt time.Time
}

type dispatchPlan struct {
	channels    []*model.NoticeChannelM
	eventType   string
	requestBody string
	occurredAt  time.Time
}

type eventContext struct {
	EventType     string
	Namespace     string
	Service       string
	Severity      string
	RootCauseType string
}

// DispatchBestEffort enqueues webhook deliveries into DB outbox (pending), without network IO.
func DispatchBestEffort(ctx context.Context, st store.IStore, rq DispatchRequest) {
	if st == nil || rq.Incident == nil {
		return
	}

	plan, ok := prepareDispatchPlan(ctx, st, rq)
	if !ok {
		return
	}

	for _, channel := range plan.channels {
		if channel == nil {
			continue
		}

		delivery := &model.NoticeDeliveryM{
			ChannelID:      channel.ChannelID,
			EventType:      plan.eventType,
			IncidentID:     strPtrOrNil(strings.TrimSpace(rq.Incident.IncidentID)),
			JobID:          strPtrOrNil(strings.TrimSpace(rq.JobID)),
			RequestBody:    plan.requestBody,
			Status:         DeliveryStatusPending,
			Attempts:       0,
			MaxAttempts:    deriveMaxAttempts(channel.MaxRetries),
			NextRetryAt:    time.Now().UTC(),
			IdempotencyKey: newDeliveryIdempotencyKey(channel.ChannelID, plan.eventType, rq.Incident.IncidentID, rq.JobID, plan.occurredAt),
		}
		if err := st.NoticeDelivery().Create(ctx, delivery); err != nil {
			slog.ErrorContext(ctx, "notice delivery enqueue failed",
				"error", err,
				"event_type", plan.eventType,
				"incident_id", rq.Incident.IncidentID,
				"channel_id", channel.ChannelID,
			)
			continue
		}
		if metrics.M != nil {
			metrics.M.RecordNoticeDeliveryDispatch(ctx, plan.eventType)
		}
	}
}

func prepareDispatchPlan(ctx context.Context, st store.IStore, rq DispatchRequest) (*dispatchPlan, bool) {
	channels, err := st.NoticeChannel().ListEnabledWebhook(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "notice list channels failed",
			"error", err,
			"event_type", rq.EventType,
			"incident_id", rq.Incident.IncidentID,
		)
		return nil, false
	}
	if len(channels) == 0 {
		return nil, false
	}

	occurredAt := rq.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	rq.OccurredAt = occurredAt
	eventType := strings.ToLower(strings.TrimSpace(rq.EventType))
	eventCtx := buildEventContext(eventType, rq.Incident)
	matchedChannels := selectMatchedChannels(channels, eventCtx)
	if len(matchedChannels) == 0 {
		return nil, false
	}

	payloadRaw, err := buildPayload(rq)
	if err != nil {
		slog.ErrorContext(ctx, "notice payload build failed",
			"error", err,
			"event_type", rq.EventType,
			"incident_id", rq.Incident.IncidentID,
		)
		return nil, false
	}

	return &dispatchPlan{
		channels:    matchedChannels,
		eventType:   eventType,
		requestBody: truncateString(string(payloadRaw), RequestBodyMaxBytes),
		occurredAt:  occurredAt,
	}, true
}

func buildPayload(rq DispatchRequest) ([]byte, error) {
	incident := rq.Incident
	payload := map[string]any{
		"event_type":  strings.ToLower(strings.TrimSpace(rq.EventType)),
		"occurred_at": rq.OccurredAt.UTC().Format(time.RFC3339),
		"incident": map[string]any{
			"incident_id":        incident.IncidentID,
			"namespace":          incident.Namespace,
			"service":            incident.Service,
			"severity":           incident.Severity,
			"rca_status":         incident.RCAStatus,
			"root_cause_type":    derefString(incident.RootCauseType),
			"root_cause_summary": derefString(incident.RootCauseSummary),
		},
		"links": map[string]any{
			"incident": "/v1/incidents/" + incident.IncidentID,
		},
	}

	if strings.EqualFold(strings.TrimSpace(rq.EventType), EventTypeDiagnosisWritten) {
		payload["job"] = map[string]any{
			"job_id": strings.TrimSpace(rq.JobID),
		}
		payload["diagnosis"] = map[string]any{
			"confidence":   rq.DiagnosisConfidence,
			"evidence_ids": normalizeStringSlice(rq.DiagnosisEvidenceID),
		}
		payload["links"] = map[string]any{
			"incident": "/v1/incidents/" + incident.IncidentID,
			"job":      "/v1/ai/jobs/" + strings.TrimSpace(rq.JobID),
		}
	}

	return json.Marshal(payload)
}

func deriveMaxAttempts(channelMaxRetries int64) int64 {
	if channelMaxRetries <= 0 {
		return defaultDeliveryMaxAttempts
	}
	if channelMaxRetries > maxDeliveryMaxAttempts {
		return maxDeliveryMaxAttempts
	}
	return channelMaxRetries
}

func newDeliveryIdempotencyKey(channelID string, eventType string, incidentID string, jobID string, occurredAt time.Time) string {
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	base := strings.Join([]string{
		strings.TrimSpace(channelID),
		strings.ToLower(strings.TrimSpace(eventType)),
		strings.TrimSpace(incidentID),
		strings.TrimSpace(jobID),
		occurredAt.UTC().Format(time.RFC3339Nano),
	}, "|")
	sum := sha256.Sum256([]byte(base))
	return "notice-" + hex.EncodeToString(sum[:16])
}

func buildEventContext(eventType string, incident *model.IncidentM) eventContext {
	if incident == nil {
		return eventContext{EventType: strings.ToLower(strings.TrimSpace(eventType))}
	}
	return eventContext{
		EventType:     strings.ToLower(strings.TrimSpace(eventType)),
		Namespace:     strings.ToLower(strings.TrimSpace(incident.Namespace)),
		Service:       strings.ToLower(strings.TrimSpace(incident.Service)),
		Severity:      normalizeSelectorSeverity(strings.TrimSpace(incident.Severity)),
		RootCauseType: strings.ToLower(strings.TrimSpace(derefString(incident.RootCauseType))),
	}
}

func selectMatchedChannels(channels []*model.NoticeChannelM, ctx eventContext) []*model.NoticeChannelM {
	if len(channels) == 0 {
		return nil
	}
	out := make([]*model.NoticeChannelM, 0, len(channels))
	for _, ch := range channels {
		if ch == nil {
			continue
		}
		if !matchChannelSelectors(ch.SelectorsJSON, ctx) {
			continue
		}
		out = append(out, ch)
	}
	return out
}

func matchChannelSelectors(raw *string, ctx eventContext) bool {
	selectors := decodeSelectors(raw)
	if selectors == nil {
		return true
	}

	if !matchSelectorDimension(selectors.EventTypes, ctx.EventType) {
		return false
	}
	if !matchSelectorDimension(selectors.Namespaces, ctx.Namespace) {
		return false
	}
	if !matchSelectorDimension(selectors.Services, ctx.Service) {
		return false
	}
	if !matchSelectorDimension(selectors.Severities, ctx.Severity) {
		return false
	}

	if ctx.EventType == EventTypeDiagnosisWritten {
		if !matchSelectorDimension(selectors.RootCauseTypes, ctx.RootCauseType) {
			return false
		}
	}
	return true
}

func decodeSelectors(raw *string) *model.NoticeSelectors {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil
	}

	var selectors model.NoticeSelectors
	if err := json.Unmarshal([]byte(*raw), &selectors); err != nil {
		return nil
	}
	normalizeSelectors(&selectors)
	if isEmptySelectors(&selectors) {
		return nil
	}
	return &selectors
}

func normalizeSelectors(selectors *model.NoticeSelectors) {
	if selectors == nil {
		return
	}
	selectors.EventTypes = normalizeSelectorList(selectors.EventTypes, normalizeSelectorEventType)
	selectors.Namespaces = normalizeSelectorList(selectors.Namespaces, normalizeSelectorIdentity)
	selectors.Services = normalizeSelectorList(selectors.Services, normalizeSelectorIdentity)
	selectors.Severities = normalizeSelectorList(selectors.Severities, normalizeSelectorSeverity)
	selectors.RootCauseTypes = normalizeSelectorList(selectors.RootCauseTypes, normalizeSelectorIdentity)
}

func normalizeSelectorList(items []string, normalize func(string) string) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		v := normalize(item)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func normalizeSelectorEventType(raw string) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	switch v {
	case EventTypeIncidentCreated, EventTypeDiagnosisWritten:
		return v
	default:
		return ""
	}
}

func normalizeSelectorIdentity(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

func normalizeSelectorSeverity(raw string) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	switch v {
	case "critical", "p0", "high":
		return "critical"
	case "warning", "warn", "p1", "medium":
		return "warning"
	case "info", "p2", "p3", "low":
		return "info"
	default:
		return v
	}
}

func matchSelectorDimension(allowList []string, value string) bool {
	if len(allowList) == 0 {
		return true
	}
	if strings.TrimSpace(value) == "" {
		return false
	}
	return slices.Contains(allowList, value)
}

func isEmptySelectors(selectors *model.NoticeSelectors) bool {
	if selectors == nil {
		return true
	}
	return len(selectors.EventTypes) == 0 &&
		len(selectors.Namespaces) == 0 &&
		len(selectors.Services) == 0 &&
		len(selectors.Severities) == 0 &&
		len(selectors.RootCauseTypes) == 0
}
