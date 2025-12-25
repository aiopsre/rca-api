package validation

import (
	"context"
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

	req := &v1.CreateNoticeChannelRequest{
		Name:        "webhook-1",
		EndpointURL: "https://example.com/hook",
		TimeoutMs:   int64PtrValidationNotice(100),
		Headers: map[string]string{
			"Authorization": "Bearer token",
		},
	}
	err = val.ValidateCreateNoticeChannelRequest(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, int64(500), req.GetTimeoutMs())
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
	}
	err = val.ValidatePatchNoticeChannelRequest(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, int64(10000), req.GetTimeoutMs())
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

func int64PtrValidationNotice(v int64) *int64 { return &v }

func strPtrValidationNotice(v string) *string { return &v }
