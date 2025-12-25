package notice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
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
		channels:    channels,
		eventType:   strings.ToLower(strings.TrimSpace(rq.EventType)),
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
