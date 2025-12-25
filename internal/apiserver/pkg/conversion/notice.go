package conversion

import (
	"encoding/json"
	"strings"

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
		ChannelID:   m.ChannelID,
		Name:        m.Name,
		Type:        m.Type,
		Enabled:     m.Enabled,
		EndpointURL: m.EndpointURL,
		Secret:      cloneOptionalString(m.Secret),
		Headers:     decodeStringMap(m.HeadersJSON),
		TimeoutMs:   m.TimeoutMs,
		MaxRetries:  m.MaxRetries,
		CreatedAt:   timestamppb.New(m.CreatedAt.UTC()),
		UpdatedAt:   timestamppb.New(m.UpdatedAt.UTC()),
	}
}

// NoticeDeliveryMToNoticeDeliveryV1 converts model notice delivery to API notice delivery.
func NoticeDeliveryMToNoticeDeliveryV1(m *model.NoticeDeliveryM) *v1.NoticeDelivery {
	if m == nil {
		return nil
	}

	return &v1.NoticeDelivery{
		DeliveryID:   m.DeliveryID,
		ChannelID:    m.ChannelID,
		EventType:    m.EventType,
		IncidentID:   cloneOptionalString(m.IncidentID),
		JobID:        cloneOptionalString(m.JobID),
		RequestBody:  m.RequestBody,
		ResponseCode: int32ToInt64Ptr(m.ResponseCode),
		ResponseBody: cloneOptionalString(m.ResponseBody),
		LatencyMs:    m.LatencyMs,
		Status:       m.Status,
		Error:        cloneOptionalString(m.Error),
		CreatedAt:    timestamppb.New(m.CreatedAt.UTC()),
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
