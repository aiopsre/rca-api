package validation

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

func TestValidateListIncidentActionsRequest_DefaultsAndLimit(t *testing.T) {
	val := New(nil)
	ctx := context.Background()

	req := &v1.ListIncidentActionsRequest{
		IncidentID: "incident-1",
	}
	require.NoError(t, val.ValidateListIncidentActionsRequest(ctx, req))
	require.Equal(t, int64(1), req.GetPage())
	require.Equal(t, int64(20), req.GetLimit())

	reqTooLarge := &v1.ListIncidentActionsRequest{
		IncidentID: "incident-1",
		Page:       1,
		Limit:      201,
	}
	require.Error(t, val.ValidateListIncidentActionsRequest(ctx, reqTooLarge))
}

func TestValidateCreateIncidentActionRequest(t *testing.T) {
	val := New(nil)
	ctx := context.Background()

	req := &v1.CreateIncidentActionRequest{
		IncidentID: "incident-1",
		ActionType: "rollback",
		Summary:    "manual rollback",
	}
	require.NoError(t, val.ValidateCreateIncidentActionRequest(ctx, req))

	reqTooLong := &v1.CreateIncidentActionRequest{
		IncidentID: "incident-1",
		ActionType: "rollback",
		Summary:    strings.Repeat("a", 257),
	}
	require.Error(t, val.ValidateCreateIncidentActionRequest(ctx, reqTooLong))
}

func TestValidateCreateIncidentVerificationRunRequest(t *testing.T) {
	val := New(nil)
	ctx := context.Background()

	req := &v1.CreateIncidentVerificationRunRequest{
		IncidentID:       "incident-1",
		Source:           "manual",
		StepIndex:        1,
		Tool:             "mcp.query_logs",
		Observed:         "result looks healthy",
		MeetsExpectation: true,
	}
	require.NoError(t, val.ValidateCreateIncidentVerificationRunRequest(ctx, req))

	reqBad := &v1.CreateIncidentVerificationRunRequest{
		IncidentID:       "incident-1",
		Source:           "manual",
		StepIndex:        -1,
		Tool:             "mcp.query_logs",
		Observed:         "result looks healthy",
		MeetsExpectation: true,
	}
	require.Error(t, val.ValidateCreateIncidentVerificationRunRequest(ctx, reqBad))
}
