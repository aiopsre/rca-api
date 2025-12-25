package notice

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"zk8s.com/rca-api/internal/apiserver/model"
	"zk8s.com/rca-api/internal/apiserver/store"
)

const (
	// EventTypeIncidentCreated is emitted after incident creation commit.
	EventTypeIncidentCreated = "incident_created"
	// EventTypeDiagnosisWritten is emitted after finalize writeback commit.
	EventTypeDiagnosisWritten = "diagnosis_written"

	DeliveryStatusSucceeded = "succeeded"
	DeliveryStatusFailed    = "failed"

	RequestBodyMaxBytes  = 8 * 1024
	ResponseBodyMaxBytes = 8 * 1024
	ErrorBodyMaxBytes    = 2 * 1024

	timeoutMsMin     = int64(500)
	timeoutMsMax     = int64(10000)
	defaultTimeoutMs = int64(3000)
	maxHTTPReadBytes = int64(64 * 1024)
)

var errEmptyEndpointURL = errors.New("empty endpoint_url")

// DispatchRequest describes one notice event to dispatch.
type DispatchRequest struct {
	EventType string
	Incident  *model.IncidentM
	JobID     string

	DiagnosisConfidence float64
	DiagnosisEvidenceID []string

	OccurredAt time.Time
}

// DispatchBestEffort sends webhook notifications to enabled channels and always best-effort persists deliveries.
func DispatchBestEffort(ctx context.Context, st store.IStore, rq DispatchRequest) {
	if st == nil || rq.Incident == nil {
		return
	}

	channels, err := st.NoticeChannel().ListEnabledWebhook(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "notice list channels failed", "error", err, "event_type", rq.EventType, "incident_id", rq.Incident.IncidentID)
		return
	}
	if len(channels) == 0 {
		return
	}

	if rq.OccurredAt.IsZero() {
		rq.OccurredAt = time.Now().UTC()
	}

	for _, channel := range channels {
		if channel == nil {
			continue
		}
		dispatchToChannel(ctx, st, channel, rq)
	}
}

//nolint:gocognit // Delivery flow keeps explicit best-effort branches for auditability.
func dispatchToChannel(ctx context.Context, st store.IStore, channel *model.NoticeChannelM, rq DispatchRequest) {
	payloadRaw, err := buildPayload(rq)
	if err != nil {
		slog.ErrorContext(ctx, "notice payload build failed",
			"error", err,
			"event_type", rq.EventType,
			"incident_id", rq.Incident.IncidentID,
			"channel_id", channel.ChannelID,
		)
		return
	}

	attempts := 1
	if channel.MaxRetries > 0 {
		attempts = 2
	}

	for attempt := 1; attempt <= attempts; attempt++ {
		started := time.Now()
		respCode, respBody, callErr := postWebhook(ctx, channel, strings.ToLower(strings.TrimSpace(rq.EventType)), payloadRaw)
		latency := time.Since(started).Milliseconds()

		status := DeliveryStatusSucceeded
		if callErr != nil || (respCode != nil && *respCode >= 400) {
			status = DeliveryStatusFailed
		}

		var errText *string
		if callErr != nil {
			v := truncateString(callErr.Error(), ErrorBodyMaxBytes)
			errText = &v
		}

		respBodyPtr := strPtrOrNil(truncateString(respBody, ResponseBodyMaxBytes))
		reqBody := truncateString(string(payloadRaw), RequestBodyMaxBytes)

		delivery := &model.NoticeDeliveryM{
			ChannelID:    channel.ChannelID,
			EventType:    strings.ToLower(strings.TrimSpace(rq.EventType)),
			IncidentID:   strPtrOrNil(strings.TrimSpace(rq.Incident.IncidentID)),
			JobID:        strPtrOrNil(strings.TrimSpace(rq.JobID)),
			RequestBody:  reqBody,
			ResponseCode: respCode,
			ResponseBody: respBodyPtr,
			LatencyMs:    latency,
			Status:       status,
			Error:        errText,
		}
		if err := st.NoticeDelivery().Create(ctx, delivery); err != nil {
			slog.ErrorContext(ctx, "notice delivery audit create failed",
				"error", err,
				"event_type", rq.EventType,
				"incident_id", rq.Incident.IncidentID,
				"channel_id", channel.ChannelID,
			)
		}

		if status == DeliveryStatusSucceeded {
			return
		}
	}
}

func postWebhook(ctx context.Context, channel *model.NoticeChannelM, eventType string, payloadRaw []byte) (*int32, string, error) {
	endpoint := strings.TrimSpace(channel.EndpointURL)
	if endpoint == "" {
		return nil, "", errEmptyEndpointURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payloadRaw))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Rca-Event", eventType)
	req.Header.Set("X-Rca-Notice-Channel", strings.TrimSpace(channel.ChannelID))

	for key, value := range parseHeaders(channel.HeadersJSON) {
		req.Header.Set(key, value)
	}
	if secret := strings.TrimSpace(derefString(channel.Secret)); secret != "" {
		req.Header.Set("X-Rca-Signature", signPayload(secret, payloadRaw))
	}

	client := &http.Client{Timeout: time.Duration(clampTimeoutMs(channel.TimeoutMs)) * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	bodyRaw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxHTTPReadBytes))
	code := int32(resp.StatusCode)
	if readErr != nil {
		return nil, "", readErr
	}
	return &code, string(bodyRaw), nil
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

func parseHeaders(raw *string) map[string]string {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return map[string]string{}
	}
	out := make(map[string]string)
	if err := json.Unmarshal([]byte(*raw), &out); err != nil {
		return map[string]string{}
	}
	return out
}

func signPayload(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func clampTimeoutMs(in int64) int64 {
	switch {
	case in == 0:
		return defaultTimeoutMs
	case in < timeoutMsMin:
		return timeoutMsMin
	case in > timeoutMsMax:
		return timeoutMsMax
	default:
		return in
	}
}

func truncateString(in string, maxBytes int) string {
	if len(in) <= maxBytes {
		return in
	}
	return in[:maxBytes]
}

func strPtrOrNil(v string) *string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	return &v
}

func derefString(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

func normalizeStringSlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, item := range in {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}
