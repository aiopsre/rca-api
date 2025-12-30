package conversion

import (
	"encoding/json"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"zk8s.com/rca-api/internal/apiserver/model"
	v1 "zk8s.com/rca-api/pkg/api/apiserver/v1"
)

// NoticeChannelMToNoticeChannelV1 converts model notice channel to API notice channel.
func NoticeChannelMToNoticeChannelV1(m *model.NoticeChannelM) *v1.NoticeChannel {
	if m == nil {
		return nil
	}

	return &v1.NoticeChannel{
		ChannelID:          m.ChannelID,
		Name:               m.Name,
		Type:               m.Type,
		Enabled:            m.Enabled,
		EndpointURL:        m.EndpointURL,
		Secret:             cloneOptionalString(m.Secret),
		Headers:            decodeStringMap(m.HeadersJSON),
		Selectors:          DecodeNoticeSelectors(m.SelectorsJSON),
		TimeoutMs:          m.TimeoutMs,
		MaxRetries:         m.MaxRetries,
		PayloadMode:        noticePayloadModeModelToV1(m.PayloadMode),
		IncludeDiagnosis:   m.IncludeDiagnosis,
		IncludeEvidenceIds: m.IncludeEvidenceIDs,
		IncludeRootCause:   m.IncludeRootCause,
		IncludeLinks:       m.IncludeLinks,
		BaseURL:            cloneOptionalString(m.BaseURL),
		SummaryTemplate:    cloneOptionalString(m.SummaryTemplate),
		CreatedAt:          timestamppb.New(m.CreatedAt.UTC()),
		UpdatedAt:          timestamppb.New(m.UpdatedAt.UTC()),
	}
}

// NoticeDeliveryMToNoticeDeliveryV1 converts model notice delivery to API notice delivery.
func NoticeDeliveryMToNoticeDeliveryV1(m *model.NoticeDeliveryM) *v1.NoticeDelivery {
	if m == nil {
		return nil
	}

	return &v1.NoticeDelivery{
		DeliveryID:     m.DeliveryID,
		ChannelID:      m.ChannelID,
		EventType:      m.EventType,
		IncidentID:     cloneOptionalString(m.IncidentID),
		JobID:          cloneOptionalString(m.JobID),
		RequestBody:    m.RequestBody,
		ResponseCode:   int32ToInt64Ptr(m.ResponseCode),
		ResponseBody:   cloneOptionalString(m.ResponseBody),
		LatencyMs:      m.LatencyMs,
		Status:         m.Status,
		Error:          cloneOptionalString(m.Error),
		CreatedAt:      timestamppb.New(m.CreatedAt.UTC()),
		Attempts:       m.Attempts,
		MaxAttempts:    m.MaxAttempts,
		NextRetryAt:    timestamppb.New(m.NextRetryAt.UTC()),
		LockedBy:       cloneOptionalString(m.LockedBy),
		LockedAt:       timePtrToTimestampPtr(m.LockedAt),
		IdempotencyKey: m.IdempotencyKey,
		Snapshot:       noticeDeliverySnapshotMToV1(m),
	}
}

// EncodeStringMap encodes headers to JSON text.
func EncodeStringMap(headers map[string]string) *string {
	if len(headers) == 0 {
		return nil
	}
	normalized := make(map[string]string, len(headers))
	for key, value := range headers {
		k := strings.TrimSpace(key)
		if k == "" {
			continue
		}
		normalized[k] = value
	}
	if len(normalized) == 0 {
		return nil
	}
	raw, _ := json.Marshal(normalized)
	out := string(raw)
	return &out
}

// EncodeNoticeSelectors encodes selectors to JSON text.
func EncodeNoticeSelectors(selectors *v1.NoticeSelectors) *string {
	if selectors == nil {
		return nil
	}
	modelSelectors := selectorsV1ToModel(selectors)
	if isEmptyNoticeSelectors(modelSelectors) {
		return nil
	}

	raw, _ := json.Marshal(modelSelectors)
	out := string(raw)
	return &out
}

// DecodeNoticeSelectors decodes selectors JSON text.
func DecodeNoticeSelectors(raw *string) *v1.NoticeSelectors {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil
	}

	var out model.NoticeSelectors
	if err := json.Unmarshal([]byte(*raw), &out); err != nil {
		return nil
	}
	if isEmptyNoticeSelectors(&out) {
		return nil
	}
	return selectorsModelToV1(&out)
}

func decodeStringMap(raw *string) map[string]string {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return map[string]string{}
	}
	out := map[string]string{}
	if err := json.Unmarshal([]byte(*raw), &out); err != nil {
		return map[string]string{}
	}
	return out
}

func int32ToInt64Ptr(v *int32) *int64 {
	if v == nil {
		return nil
	}
	out := int64(*v)
	return &out
}

func timePtrToTimestampPtr(v *time.Time) *timestamppb.Timestamp {
	if v == nil {
		return nil
	}
	return timestamppb.New(v.UTC())
}

func selectorsV1ToModel(in *v1.NoticeSelectors) *model.NoticeSelectors {
	if in == nil {
		return nil
	}
	return &model.NoticeSelectors{
		EventTypes:     append([]string(nil), in.GetEventTypes()...),
		Namespaces:     append([]string(nil), in.GetNamespaces()...),
		Services:       append([]string(nil), in.GetServices()...),
		Severities:     append([]string(nil), in.GetSeverities()...),
		RootCauseTypes: append([]string(nil), in.GetRootCauseTypes()...),
	}
}

func selectorsModelToV1(in *model.NoticeSelectors) *v1.NoticeSelectors {
	if in == nil {
		return nil
	}
	return &v1.NoticeSelectors{
		EventTypes:     append([]string(nil), in.EventTypes...),
		Namespaces:     append([]string(nil), in.Namespaces...),
		Services:       append([]string(nil), in.Services...),
		Severities:     append([]string(nil), in.Severities...),
		RootCauseTypes: append([]string(nil), in.RootCauseTypes...),
	}
}

func isEmptyNoticeSelectors(in *model.NoticeSelectors) bool {
	if in == nil {
		return true
	}
	return len(in.EventTypes) == 0 &&
		len(in.Namespaces) == 0 &&
		len(in.Services) == 0 &&
		len(in.Severities) == 0 &&
		len(in.RootCauseTypes) == 0
}

func noticeDeliverySnapshotMToV1(m *model.NoticeDeliveryM) *v1.NoticeDeliverySnapshot {
	if m == nil {
		return nil
	}
	if m.SnapshotEndpointURL == nil &&
		m.SnapshotTimeoutMs == nil &&
		m.SnapshotHeadersJSON == nil &&
		m.SnapshotSecretFingerprint == nil &&
		m.SnapshotChannelVersion == nil {

		return nil
	}

	return &v1.NoticeDeliverySnapshot{
		EndpointURL:       cloneOptionalString(m.SnapshotEndpointURL),
		TimeoutMs:         cloneOptionalInt64(m.SnapshotTimeoutMs),
		Headers:           decodeStringMap(m.SnapshotHeadersJSON),
		SecretFingerprint: cloneOptionalString(m.SnapshotSecretFingerprint),
		ChannelVersion:    cloneOptionalInt64(m.SnapshotChannelVersion),
	}
}

func cloneOptionalInt64(v *int64) *int64 {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}

func noticePayloadModeModelToV1(mode string) v1.NoticePayloadMode {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case "FULL":
		return v1.NoticePayloadMode_NOTICE_PAYLOAD_MODE_FULL
	case "COMPACT":
		return v1.NoticePayloadMode_NOTICE_PAYLOAD_MODE_COMPACT
	default:
		return v1.NoticePayloadMode_NOTICE_PAYLOAD_MODE_COMPACT
	}
}
