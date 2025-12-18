package validation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "zk8s.com/rca-api/pkg/api/apiserver/v1"
)

func TestValidateRunAIJobRequest_InvalidTrigger(t *testing.T) {
	val := &Validator{}
	now := time.Now().UTC()

	err := val.ValidateRunAIJobRequest(context.Background(), &v1.RunAIJobRequest{
		IncidentID:     "incident-1",
		Trigger:        ptrValidationString("invalid"),
		TimeRangeStart: timestamppb.New(now.Add(-10 * time.Minute)),
		TimeRangeEnd:   timestamppb.New(now),
	})
	require.Error(t, err)
}

func TestValidateFinalizeAIJobRequest_SucceededRequiresDiagnosis(t *testing.T) {
	val := &Validator{}

	err := val.ValidateFinalizeAIJobRequest(context.Background(), &v1.FinalizeAIJobRequest{
		JobID:       "ai-job-1",
		Status:      "succeeded",
		EvidenceIDs: []string{"ev-1"},
	})
	require.Error(t, err)
}

func TestValidateCreateAIToolCallRequest_Guardrails(t *testing.T) {
	val := &Validator{}

	err := val.ValidateCreateAIToolCallRequest(context.Background(), &v1.CreateAIToolCallRequest{
		JobID:       "ai-job-1",
		Seq:         0,
		NodeName:    "metrics_specialist",
		ToolName:    "evidence.queryMetrics",
		RequestJSON: "{}",
		Status:      "ok",
		LatencyMs:   10,
	})
	require.Error(t, err)

	err = val.ValidateCreateAIToolCallRequest(context.Background(), &v1.CreateAIToolCallRequest{
		JobID:       "ai-job-1",
		Seq:         1,
		NodeName:    "metrics_specialist",
		ToolName:    "evidence.queryMetrics",
		RequestJSON: "{}",
		Status:      "bad-status",
		LatencyMs:   10,
	})
	require.Error(t, err)

	err = val.ValidateCreateAIToolCallRequest(context.Background(), &v1.CreateAIToolCallRequest{
		JobID:       "ai-job-1",
		Seq:         1,
		NodeName:    "metrics_specialist",
		ToolName:    "evidence.queryMetrics",
		RequestJSON: "{}",
		Status:      "ok",
		LatencyMs:   10,
	})
	require.NoError(t, err)
}

func ptrValidationString(v string) *string { return &v }
