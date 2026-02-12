package validation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
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

func TestValidateRunAIJobRequest_ReplayFollowUpCronChange(t *testing.T) {
	val := &Validator{}
	now := time.Now().UTC()

	err := val.ValidateRunAIJobRequest(context.Background(), &v1.RunAIJobRequest{
		IncidentID:     "incident-1",
		Trigger:        ptrValidationString("replay"),
		TimeRangeStart: timestamppb.New(now.Add(-10 * time.Minute)),
		TimeRangeEnd:   timestamppb.New(now),
	})
	require.NoError(t, err)

	err = val.ValidateRunAIJobRequest(context.Background(), &v1.RunAIJobRequest{
		IncidentID:     "incident-1",
		Trigger:        ptrValidationString("follow_up"),
		TimeRangeStart: timestamppb.New(now.Add(-10 * time.Minute)),
		TimeRangeEnd:   timestamppb.New(now),
	})
	require.NoError(t, err)

	err = val.ValidateRunAIJobRequest(context.Background(), &v1.RunAIJobRequest{
		IncidentID:     "incident-1",
		Trigger:        ptrValidationString("cron"),
		TimeRangeStart: timestamppb.New(now.Add(-10 * time.Minute)),
		TimeRangeEnd:   timestamppb.New(now),
	})
	require.NoError(t, err)

	err = val.ValidateRunAIJobRequest(context.Background(), &v1.RunAIJobRequest{
		IncidentID:     "incident-1",
		Trigger:        ptrValidationString("change"),
		TimeRangeStart: timestamppb.New(now.Add(-10 * time.Minute)),
		TimeRangeEnd:   timestamppb.New(now),
	})
	require.NoError(t, err)
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

func TestValidateListAIJobsRequest_Defaults(t *testing.T) {
	val := &Validator{}
	rq := &v1.ListAIJobsRequest{}

	err := val.ValidateListAIJobsRequest(context.Background(), rq)
	require.NoError(t, err)
	require.Equal(t, "queued", rq.GetStatus())
	require.Equal(t, int64(10), rq.GetLimit())
}

func TestValidateListAIJobsRequest_Guardrails(t *testing.T) {
	val := &Validator{}

	err := val.ValidateListAIJobsRequest(context.Background(), &v1.ListAIJobsRequest{
		Status: "running",
		Limit:  10,
	})
	require.Error(t, err)

	err = val.ValidateListAIJobsRequest(context.Background(), &v1.ListAIJobsRequest{
		Status: "queued",
		Limit:  51,
	})
	require.Error(t, err)

	err = val.ValidateListAIJobsRequest(context.Background(), &v1.ListAIJobsRequest{
		Status: "queued",
		Offset: -1,
		Limit:  10,
	})
	require.Error(t, err)
}

func TestValidateAIJobQueueWaitSeconds_Guardrails(t *testing.T) {
	val := &Validator{}

	err := val.ValidateAIJobQueueWaitSeconds(context.Background(), -1)
	require.Error(t, err)

	err = val.ValidateAIJobQueueWaitSeconds(context.Background(), 0)
	require.NoError(t, err)

	err = val.ValidateAIJobQueueWaitSeconds(context.Background(), 30)
	require.NoError(t, err)

	err = val.ValidateAIJobQueueWaitSeconds(context.Background(), 31)
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

func TestValidateSessionOperatorActionRequest(t *testing.T) {
	val := &Validator{}
	req := &SessionOperatorActionRequest{
		SessionID:    "session-1",
		TriggerType:  "replay",
		Pipeline:     ptrValidationString("basic_rca"),
		Reason:       ptrValidationString("operator replay"),
		OperatorNote: ptrValidationString("focus on db pressure"),
		Source:       ptrValidationString("session_workbench_replay_api"),
		Initiator:    ptrValidationString("user:alice"),
	}
	require.NoError(t, val.ValidateSessionOperatorActionRequest(context.Background(), req))
}

func TestValidateSessionOperatorActionRequest_Invalid(t *testing.T) {
	val := &Validator{}
	require.Error(t, val.ValidateSessionOperatorActionRequest(context.Background(), nil))

	req := &SessionOperatorActionRequest{
		SessionID:   "session-1",
		TriggerType: "manual",
	}
	require.Error(t, val.ValidateSessionOperatorActionRequest(context.Background(), req))

	req.TriggerType = "follow_up"
	req.SessionID = " "
	require.Error(t, val.ValidateSessionOperatorActionRequest(context.Background(), req))
}

func TestValidateSessionReviewActionRequest(t *testing.T) {
	val := &Validator{}
	req := &SessionReviewActionRequest{
		SessionID:   "session-1",
		ReviewState: "confirmed",
		Note:        ptrValidationString("manual verification completed"),
		ReviewedBy:  ptrValidationString("user:alice"),
		ReasonCode:  ptrValidationString("human_validated"),
	}
	require.NoError(t, val.ValidateSessionReviewActionRequest(context.Background(), req))
}

func TestValidateSessionReviewActionRequest_Invalid(t *testing.T) {
	val := &Validator{}
	require.Error(t, val.ValidateSessionReviewActionRequest(context.Background(), nil))

	req := &SessionReviewActionRequest{
		SessionID:   "session-1",
		ReviewState: "unknown",
	}
	require.Error(t, val.ValidateSessionReviewActionRequest(context.Background(), req))

	req.ReviewState = "rejected"
	req.SessionID = " "
	require.Error(t, val.ValidateSessionReviewActionRequest(context.Background(), req))
}

func TestValidateSessionAssignmentActionRequest(t *testing.T) {
	val := &Validator{}
	req := &SessionAssignmentActionRequest{
		SessionID:  "session-1",
		Assignee:   ptrValidationString(" user:oncall-a "),
		AssignedBy: ptrValidationString("user:lead-a"),
		Note:       ptrValidationString("handoff to oncall shift"),
	}
	require.NoError(t, val.ValidateSessionAssignmentActionRequest(context.Background(), req))
	require.Equal(t, "user:oncall-a", *req.Assignee)
}

func TestValidateSessionAssignmentActionRequest_Invalid(t *testing.T) {
	val := &Validator{}
	require.Error(t, val.ValidateSessionAssignmentActionRequest(context.Background(), nil))

	req := &SessionAssignmentActionRequest{
		SessionID: "session-1",
	}
	require.Error(t, val.ValidateSessionAssignmentActionRequest(context.Background(), req))

	req.Assignee = ptrValidationString(" ")
	require.Error(t, val.ValidateSessionAssignmentActionRequest(context.Background(), req))
}

func TestValidateSessionOperatorInboxRequest(t *testing.T) {
	val := &Validator{}
	reviewState := "in_review"
	sessionType := "service"
	assignee := "user:oncall-a"
	teamID := "namespace:payments"
	escalationState := "pending"
	needsReview := true
	scanLimit := int64(500)
	shard := int64(0)
	shardCount := int64(2)
	asyncRefresh := true
	req := &SessionOperatorInboxRequest{
		ReviewState:     &reviewState,
		NeedsReview:     &needsReview,
		SessionType:     &sessionType,
		Assignee:        &assignee,
		TeamID:          &teamID,
		EscalationState: &escalationState,
		Offset:          0,
		Limit:           10,
		ScanLimit:       scanLimit,
		Shard:           &shard,
		ShardCount:      &shardCount,
		AsyncRefresh:    &asyncRefresh,
	}
	require.NoError(t, val.ValidateSessionOperatorInboxRequest(context.Background(), req))
	require.Equal(t, "in_review", *req.ReviewState)
	require.Equal(t, "service", *req.SessionType)
	require.Equal(t, "user:oncall-a", *req.Assignee)
	require.Equal(t, "namespace:payments", *req.TeamID)
	require.Equal(t, "pending", *req.EscalationState)
}

func TestValidateSessionOperatorInboxRequest_Invalid(t *testing.T) {
	val := &Validator{}
	require.Error(t, val.ValidateSessionOperatorInboxRequest(context.Background(), nil))

	reviewState := "invalid"
	req := &SessionOperatorInboxRequest{ReviewState: &reviewState}
	require.Error(t, val.ValidateSessionOperatorInboxRequest(context.Background(), req))

	sessionType := "invalid"
	req = &SessionOperatorInboxRequest{SessionType: &sessionType}
	require.Error(t, val.ValidateSessionOperatorInboxRequest(context.Background(), req))

	req = &SessionOperatorInboxRequest{Offset: -1}
	require.Error(t, val.ValidateSessionOperatorInboxRequest(context.Background(), req))

	assignee := " "
	req = &SessionOperatorInboxRequest{Assignee: &assignee}
	require.Error(t, val.ValidateSessionOperatorInboxRequest(context.Background(), req))

	teamID := " "
	req = &SessionOperatorInboxRequest{TeamID: &teamID}
	require.Error(t, val.ValidateSessionOperatorInboxRequest(context.Background(), req))

	escalationState := "bad"
	req = &SessionOperatorInboxRequest{EscalationState: &escalationState}
	require.Error(t, val.ValidateSessionOperatorInboxRequest(context.Background(), req))

	badScanLimit := int64(6000)
	req = &SessionOperatorInboxRequest{ScanLimit: badScanLimit}
	require.Error(t, val.ValidateSessionOperatorInboxRequest(context.Background(), req))

	shard := int64(2)
	shardCount := int64(2)
	req = &SessionOperatorInboxRequest{Shard: &shard, ShardCount: &shardCount}
	require.Error(t, val.ValidateSessionOperatorInboxRequest(context.Background(), req))
}

func ptrValidationString(v string) *string { return &v }
