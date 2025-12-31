package notice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/metrics"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
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
	channels   []*model.NoticeChannelM
	eventType  string
	occurredAt time.Time
}

type eventContext struct {
	EventType     string
	Namespace     string
	Service       string
	Severity      string
	RootCauseType string
}

var errNoticeSnapshotInvalid = errors.New("invalid notice delivery snapshot")

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
		enqueueDeliveryForChannel(ctx, st, plan, rq, channel)
	}
}

func enqueueDeliveryForChannel(ctx context.Context, st store.IStore, plan *dispatchPlan, rq DispatchRequest, channel *model.NoticeChannelM) {
	payloadRaw, err := buildPayloadForChannel(rq, channel)
	if err != nil {
		slog.ErrorContext(ctx, "notice payload build failed",
			"error", err,
			"event_type", plan.eventType,
			"incident_id", rq.Incident.IncidentID,
			"channel_id", channel.ChannelID,
		)
		return
	}

	snapshot, err := BuildDeliverySnapshotFromChannel(channel)
	if err != nil {
		slog.WarnContext(ctx, "notice delivery snapshot build failed",
			"error", err,
			"event_type", plan.eventType,
			"incident_id", rq.Incident.IncidentID,
			"channel_id", channel.ChannelID,
		)
		return
	}

	delivery := &model.NoticeDeliveryM{
		ChannelID:                 channel.ChannelID,
		EventType:                 plan.eventType,
		IncidentID:                strPtrOrNil(strings.TrimSpace(rq.Incident.IncidentID)),
		JobID:                     strPtrOrNil(strings.TrimSpace(rq.JobID)),
		RequestBody:               truncateString(string(payloadRaw), RequestBodyMaxBytes),
		Status:                    DeliveryStatusPending,
		Attempts:                  0,
		MaxAttempts:               deriveMaxAttempts(channel.MaxRetries),
		NextRetryAt:               time.Now().UTC(),
		IdempotencyKey:            newDeliveryIdempotencyKey(channel.ChannelID, plan.eventType, rq.Incident.IncidentID, rq.JobID, plan.occurredAt),
		SnapshotEndpointURL:       snapshot.EndpointURL,
		SnapshotTimeoutMs:         snapshot.TimeoutMs,
		SnapshotHeadersJSON:       encodeSnapshotHeaders(snapshot.Headers),
		SnapshotSecretFingerprint: snapshot.SecretFingerprint,
		SnapshotChannelVersion:    snapshot.ChannelVersion,
	}
	if err := st.NoticeDelivery().Create(ctx, delivery); err != nil {
		slog.ErrorContext(ctx, "notice delivery enqueue failed",
			"error", err,
			"event_type", plan.eventType,
			"incident_id", rq.Incident.IncidentID,
			"channel_id", channel.ChannelID,
		)
		return
	}

	rebuildDeliveryPayloadWithID(ctx, st, rq, channel, delivery)

	if metrics.M != nil {
		metrics.M.RecordNoticeDeliveryDispatch(ctx, plan.eventType)
	}
}

func rebuildDeliveryPayloadWithID(
	ctx context.Context,
	st store.IStore,
	rq DispatchRequest,
	channel *model.NoticeChannelM,
	delivery *model.NoticeDeliveryM,
) {

	if st == nil || channel == nil || delivery == nil {
		return
	}
	deliveryID := strings.TrimSpace(delivery.DeliveryID)
	if deliveryID == "" {
		return
	}

	payloadRaw, err := buildPayloadForChannelWithMetadata(rq, channel, payloadRenderMetadata{deliveryID: deliveryID})
	if err != nil {
		slog.WarnContext(ctx, "notice payload rebuild with delivery_id failed",
			"error", err,
			"event_type", rq.EventType,
			"incident_id", rq.Incident.IncidentID,
			"channel_id", channel.ChannelID,
			"delivery_id", deliveryID,
		)
		return
	}
	requestBody := truncateString(string(payloadRaw), RequestBodyMaxBytes)
	if requestBody == delivery.RequestBody {
		return
	}
	if err := st.NoticeDelivery().UpdateRequestBody(ctx, deliveryID, requestBody); err != nil {
		slog.WarnContext(ctx, "notice payload update with delivery_id failed",
			"error", err,
			"event_type", rq.EventType,
			"incident_id", rq.Incident.IncidentID,
			"channel_id", channel.ChannelID,
			"delivery_id", deliveryID,
		)
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

	return &dispatchPlan{
		channels:   matchedChannels,
		eventType:  eventType,
		occurredAt: occurredAt,
	}, true
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

// BuildDeliverySnapshotFromChannel builds an immutable delivery snapshot from channel config with guardrails.
func BuildDeliverySnapshotFromChannel(channel *model.NoticeChannelM) (*model.NoticeDeliverySnapshot, error) {
	if channel == nil {
		return nil, fmt.Errorf("%w: nil channel", errNoticeSnapshotInvalid)
	}

	endpoint := strings.TrimSpace(channel.EndpointURL)
	if endpoint == "" {
		return nil, fmt.Errorf("%w: empty endpoint_url", errNoticeSnapshotInvalid)
	}

	headers, err := normalizeSnapshotHeaders(channel.HeadersJSON)
	if err != nil {
		return nil, err
	}

	timeout := clampTimeoutMs(channel.TimeoutMs)
	snapshot := &model.NoticeDeliverySnapshot{
		EndpointURL:       strPtrOrNil(endpoint),
		TimeoutMs:         int64Ptr(timeout),
		Headers:           headers,
		SecretFingerprint: buildSecretFingerprint(channel.Secret),
		ChannelVersion:    channelVersion(channel.UpdatedAt),
	}
	if err := validateSnapshotSize(snapshot); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func normalizeSnapshotHeaders(raw *string) (map[string]string, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return map[string]string{}, nil
	}

	decoded, err := parseSnapshotHeaders(raw)
	if err != nil {
		return nil, err
	}
	if err := validateSnapshotHeaderCount(decoded); err != nil {
		return nil, err
	}

	out := make(map[string]string, len(decoded))
	for key, value := range decoded {
		normalizedKey, normalizedValue, err := normalizeSnapshotHeader(key, value)
		if err != nil {
			return nil, err
		}
		out[normalizedKey] = normalizedValue
	}
	return out, nil
}

func parseSnapshotHeaders(raw *string) (map[string]string, error) {
	decoded := map[string]string{}
	if err := json.Unmarshal([]byte(*raw), &decoded); err != nil {
		return nil, fmt.Errorf("%w: invalid headers_json", errNoticeSnapshotInvalid)
	}
	return decoded, nil
}

func validateSnapshotHeaderCount(headers map[string]string) error {
	if len(headers) > SnapshotHeaderMax {
		return fmt.Errorf("%w: headers count exceeds %d", errNoticeSnapshotInvalid, SnapshotHeaderMax)
	}
	return nil
}

func normalizeSnapshotHeader(key string, value string) (string, string, error) {
	trimmedKey := strings.TrimSpace(key)
	if trimmedKey == "" || len(trimmedKey) > SnapshotHeaderKeyMax {
		return "", "", fmt.Errorf("%w: invalid header key", errNoticeSnapshotInvalid)
	}
	if len(value) > SnapshotHeaderValMax {
		return "", "", fmt.Errorf("%w: invalid header value length", errNoticeSnapshotInvalid)
	}
	return trimmedKey, value, nil
}

func validateSnapshotSize(snapshot *model.NoticeDeliverySnapshot) error {
	raw, _ := json.Marshal(snapshot)
	if len(raw) > SnapshotMaxBytes {
		return fmt.Errorf("%w: snapshot too large", errNoticeSnapshotInvalid)
	}
	return nil
}

func encodeSnapshotHeaders(headers map[string]string) *string {
	if len(headers) == 0 {
		return nil
	}
	raw, _ := json.Marshal(headers)
	out := string(raw)
	return &out
}

func buildSecretFingerprint(secret *string) *string {
	if secret == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*secret)
	if trimmed == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(trimmed))
	fingerprint := "sha256:" + hex.EncodeToString(sum[:])
	return &fingerprint
}

func channelVersion(t time.Time) *int64 {
	if t.IsZero() {
		return nil
	}
	v := t.UTC().UnixMilli()
	return &v
}

func int64Ptr(v int64) *int64 { return &v }
