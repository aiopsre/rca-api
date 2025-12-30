package validation

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "zk8s.com/rca-api/pkg/api/apiserver/v1"
)

func TestValidateCreateNoticeChannelRequest_Guardrails(t *testing.T) {
	val := &Validator{}

	err := val.ValidateCreateNoticeChannelRequest(context.Background(), &v1.CreateNoticeChannelRequest{
		Name:        "webhook-1",
		Type:        strPtrValidationNotice("email"),
		EndpointURL: "https://example.com/hook",
	})
	require.Error(t, err)

	err = val.ValidateCreateNoticeChannelRequest(context.Background(), &v1.CreateNoticeChannelRequest{
		Name:        "webhook-1",
		EndpointURL: "ftp://example.com/hook",
	})
	require.Error(t, err)

	err = val.ValidateCreateNoticeChannelRequest(context.Background(), &v1.CreateNoticeChannelRequest{
		Name:        "webhook-1",
		EndpointURL: "https://example.com/hook",
		PayloadMode: v1.NoticePayloadMode(99),
	})
	require.Error(t, err)

	err = val.ValidateCreateNoticeChannelRequest(context.Background(), &v1.CreateNoticeChannelRequest{
		Name:        "webhook-1",
		EndpointURL: "https://example.com/hook",
		BaseURL:     strPtrValidationNotice("javascript:alert(1)"),
	})
	require.Error(t, err)

	err = val.ValidateCreateNoticeChannelRequest(context.Background(), &v1.CreateNoticeChannelRequest{
		Name:            "webhook-1",
		EndpointURL:     "https://example.com/hook",
		SummaryTemplate: strPtrValidationNotice(strings.Repeat("x", 513)),
	})
	require.Error(t, err)

	req := &v1.CreateNoticeChannelRequest{
		Name:        "webhook-1",
		EndpointURL: "https://example.com/hook",
		BaseURL:     strPtrValidationNotice(" https://rca.example.test "),
		SummaryTemplate: strPtrValidationNotice(
			" [${severity}] ${service} ${event_type} incident=${incident_id} ",
		),
		TimeoutMs: int64PtrValidationNotice(100),
		Headers: map[string]string{
			"Authorization": "Bearer token",
		},
		Selectors: &v1.NoticeSelectors{
			EventTypes: []string{"Incident_Created", "diagnosis_written"},
			Severities: []string{"P1", "critical"},
			Namespaces: []string{"Default"},
			Services:   []string{"Checkout"},
		},
	}
	err = val.ValidateCreateNoticeChannelRequest(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, int64(500), req.GetTimeoutMs())
	require.Equal(t, []string{"incident_created", "diagnosis_written"}, req.GetSelectors().GetEventTypes())
	require.Equal(t, []string{"warning", "critical"}, req.GetSelectors().GetSeverities())
	require.Equal(t, []string{"default"}, req.GetSelectors().GetNamespaces())
	require.Equal(t, []string{"checkout"}, req.GetSelectors().GetServices())
	require.Equal(t, "https://rca.example.test", req.GetBaseURL())
	require.Equal(t, "[${severity}] ${service} ${event_type} incident=${incident_id}", req.GetSummaryTemplate())

	req = &v1.CreateNoticeChannelRequest{
		Name:        "webhook-full",
		EndpointURL: "https://example.com/hook",
		PayloadMode: v1.NoticePayloadMode_NOTICE_PAYLOAD_MODE_FULL,
	}
	err = val.ValidateCreateNoticeChannelRequest(context.Background(), req)
	require.NoError(t, err)
}

func TestValidatePatchNoticeChannelRequest_RequireFieldsAndClamp(t *testing.T) {
	val := &Validator{}

	err := val.ValidatePatchNoticeChannelRequest(context.Background(), &v1.PatchNoticeChannelRequest{
		ChannelID: "notice-channel-1",
	})
	require.Error(t, err)

	req := &v1.PatchNoticeChannelRequest{
		ChannelID: "notice-channel-1",
		TimeoutMs: int64PtrValidationNotice(30000),
		Selectors: &v1.NoticeSelectors{
			EventTypes: []string{"Diagnosis_Written"},
			Severities: []string{"P2"},
		},
	}
	err = val.ValidatePatchNoticeChannelRequest(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, int64(10000), req.GetTimeoutMs())
	require.Equal(t, []string{"diagnosis_written"}, req.GetSelectors().GetEventTypes())
	require.Equal(t, []string{"info"}, req.GetSelectors().GetSeverities())

	req = &v1.PatchNoticeChannelRequest{
		ChannelID:   "notice-channel-1",
		PayloadMode: noticePayloadModePtrValidation(v1.NoticePayloadMode_NOTICE_PAYLOAD_MODE_FULL),
	}
	err = val.ValidatePatchNoticeChannelRequest(context.Background(), req)
	require.NoError(t, err)

	req = &v1.PatchNoticeChannelRequest{
		ChannelID:       "notice-channel-1",
		BaseURL:         strPtrValidationNotice(" https://rca.example.test/v2 "),
		SummaryTemplate: strPtrValidationNotice(" [${severity}] ${service} "),
	}
	err = val.ValidatePatchNoticeChannelRequest(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, "https://rca.example.test/v2", req.GetBaseURL())
	require.Equal(t, "[${severity}] ${service}", req.GetSummaryTemplate())

	req = &v1.PatchNoticeChannelRequest{
		ChannelID:   "notice-channel-1",
		PayloadMode: noticePayloadModePtrValidation(v1.NoticePayloadMode_NOTICE_PAYLOAD_MODE_UNSPECIFIED),
	}
	err = val.ValidatePatchNoticeChannelRequest(context.Background(), req)
	require.Error(t, err)

	err = val.ValidatePatchNoticeChannelRequest(context.Background(), &v1.PatchNoticeChannelRequest{
		ChannelID: "notice-channel-1",
		Selectors: &v1.NoticeSelectors{
			EventTypes: []string{"unknown_event"},
		},
	})
	require.Error(t, err)

	tooMany := make([]string, 101)
	for i := range tooMany {
		tooMany[i] = "default"
	}
	err = val.ValidatePatchNoticeChannelRequest(context.Background(), &v1.PatchNoticeChannelRequest{
		ChannelID: "notice-channel-1",
		Selectors: &v1.NoticeSelectors{
			Namespaces: tooMany,
		},
	})
	require.Error(t, err)
}

func TestValidateListNoticeDeliveriesRequest_DefaultLimitAndFilters(t *testing.T) {
	val := &Validator{}

	req := &v1.ListNoticeDeliveriesRequest{}
	err := val.ValidateListNoticeDeliveriesRequest(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, int64(20), req.GetLimit())

	err = val.ValidateListNoticeDeliveriesRequest(context.Background(), &v1.ListNoticeDeliveriesRequest{
		IncidentID: strPtrValidationNotice(""),
	})
	require.Error(t, err)

	req = &v1.ListNoticeDeliveriesRequest{
		IncidentID: strPtrValidationNotice("incident-1"),
		EventType:  strPtrValidationNotice("Incident_Created"),
		Status:     strPtrValidationNotice("Succeeded"),
	}
	err = val.ValidateListNoticeDeliveriesRequest(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, "incident_created", req.GetEventType())
	require.Equal(t, "succeeded", req.GetStatus())
}

func TestValidateReplayAndCancelNoticeDeliveryRequest(t *testing.T) {
	val := &Validator{}

	require.Error(t, val.ValidateReplayNoticeDeliveryRequest(context.Background(), &v1.ReplayNoticeDeliveryRequest{
		DeliveryID: "",
	}))
	require.NoError(t, val.ValidateReplayNoticeDeliveryRequest(context.Background(), &v1.ReplayNoticeDeliveryRequest{
		DeliveryID: "notice-delivery-1",
	}))

	require.Error(t, val.ValidateCancelNoticeDeliveryRequest(context.Background(), &v1.CancelNoticeDeliveryRequest{
		DeliveryID: "",
	}))
	require.NoError(t, val.ValidateCancelNoticeDeliveryRequest(context.Background(), &v1.CancelNoticeDeliveryRequest{
		DeliveryID: "notice-delivery-1",
	}))
}

func int64PtrValidationNotice(v int64) *int64 { return &v }

func strPtrValidationNotice(v string) *string { return &v }

func noticePayloadModePtrValidation(v v1.NoticePayloadMode) *v1.NoticePayloadMode { return &v }
