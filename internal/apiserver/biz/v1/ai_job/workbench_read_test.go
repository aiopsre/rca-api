package ai_job

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	sessionbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/session"
	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	"github.com/aiopsre/rca-api/internal/pkg/contextx"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

func TestGetSessionWorkbench_AggregatesAndSuggestsReviewHints(t *testing.T) {
	db := newAIJobTestDB(t)
	s := store.NewStore(db)
	biz := New(s)
	incident := createTestIncident(t, s)

	manualJobID := runAndFinalizeWorkbenchJob(t, biz, incident.IncidentID, workbenchRunSpec{
		Status:        "succeeded",
		TriggerType:   "manual",
		TriggerSource: "manual_api",
		Initiator:     "user:manual",
		DiagnosisJSON: `{
			"summary":"database pool saturation confirmed",
			"root_cause":{
				"type":"db_pool_exhausted",
				"category":"db",
				"summary":"database pool saturation confirmed",
				"statement":"database pool saturation confirmed",
				"confidence":0.91,
				"evidence_ids":["ev-manual-1","ev-manual-2"]
			},
			"hypotheses":[{"statement":"pool limit reached","confidence":0.91,"supporting_evidence_ids":["ev-manual-1","ev-manual-2"],"missing_evidence":[]}]
		}`,
		EvidenceIDs: []string{"ev-manual-1", "ev-manual-2"},
	})
	followJobID := runAndFinalizeWorkbenchJob(t, biz, incident.IncidentID, workbenchRunSpec{
		Status:        "failed",
		TriggerType:   "replay",
		TriggerSource: "replay_api",
		Initiator:     "user:replay",
		ErrorMessage:  "worker timeout while replaying evidence collection",
	})
	require.NotEqual(t, manualJobID, followJobID)

	job, err := s.AIJob().Get(context.Background(), where.T(context.Background()).F("job_id", followJobID))
	require.NoError(t, err)
	require.NotNil(t, job.SessionID)
	sessionID := strings.TrimSpace(*job.SessionID)
	require.NotEmpty(t, sessionID)

	resp, err := biz.GetSessionWorkbench(context.Background(), &GetSessionWorkbenchRequest{
		SessionID:   sessionID,
		RecentLimit: 10,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, resp.Session)
	require.Equal(t, sessionID, resp.Session.SessionID)
	require.NotNil(t, resp.Incident)
	require.Equal(t, incident.IncidentID, resp.Incident.IncidentID)

	require.NotNil(t, resp.LatestRun)
	require.Equal(t, followJobID, resp.LatestRun.JobID)
	require.Equal(t, "replay", resp.LatestRun.TriggerType)
	require.GreaterOrEqual(t, len(resp.RecentRuns), 2)
	require.GreaterOrEqual(t, resp.RecentTotalCount, int64(2))

	require.NotNil(t, resp.LatestDecision)
	require.Equal(t, "failed", resp.LatestDecision.Status)
	require.NotNil(t, resp.LatestCompare)
	require.Equal(t, manualJobID, resp.LatestCompare.LeftJobID)
	require.Equal(t, followJobID, resp.LatestCompare.RightJobID)
	require.True(t, resp.LatestCompare.ChangedRootCause)

	require.NotNil(t, resp.ReviewFlags)
	require.True(t, resp.ReviewFlags.HumanReviewRequired)
	require.True(t, resp.ReviewFlags.HasPinnedEvidence)
	require.GreaterOrEqual(t, resp.ReviewFlags.PinnedEvidenceCount, int64(2))

	require.Contains(t, resp.NextActionHints, workbenchHintNeedHumanReview)
	require.Contains(t, resp.NextActionHints, workbenchHintReviewCompare)
}

func TestGetSessionWorkbench_InProgressHint(t *testing.T) {
	db := newAIJobTestDB(t)
	s := store.NewStore(db)
	biz := New(s)
	incident := createTestIncident(t, s)

	now := time.Now().UTC().Truncate(time.Second)
	runCtx := contextx.WithTriggerType(context.Background(), "manual")
	runCtx = contextx.WithTriggerSource(runCtx, "manual_api")
	runCtx = contextx.WithTriggerInitiator(runCtx, "user:manual")
	runResp, err := biz.Run(runCtx, &v1.RunAIJobRequest{
		IncidentID:     incident.IncidentID,
		Trigger:        ptrAIString("manual"),
		CreatedBy:      ptrAIString("user:manual"),
		TimeRangeStart: timestamppb.New(now.Add(-10 * time.Minute)),
		TimeRangeEnd:   timestamppb.New(now),
	})
	require.NoError(t, err)
	require.NotEmpty(t, runResp.GetJobID())

	job, err := s.AIJob().Get(context.Background(), where.T(context.Background()).F("job_id", runResp.GetJobID()))
	require.NoError(t, err)
	require.NotNil(t, job.SessionID)
	sessionID := strings.TrimSpace(*job.SessionID)
	require.NotEmpty(t, sessionID)

	resp, err := biz.GetSessionWorkbench(context.Background(), &GetSessionWorkbenchRequest{SessionID: sessionID})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, resp.LatestRun)
	require.Equal(t, jobStatusQueued, resp.LatestRun.Status)
	require.Contains(t, resp.NextActionHints, workbenchHintRunInProgress)
}

func TestGetSessionWorkbench_ReviewStateAffectsHints(t *testing.T) {
	db := newAIJobTestDB(t)
	s := store.NewStore(db)
	biz := New(s)
	sessionSvc := sessionbiz.New(s)
	incident := createTestIncident(t, s)

	failedJobID := runAndFinalizeWorkbenchJob(t, biz, incident.IncidentID, workbenchRunSpec{
		Status:        "failed",
		TriggerType:   "manual",
		TriggerSource: "manual_api",
		Initiator:     "user:manual",
		ErrorMessage:  "diagnosis confidence too low",
	})
	job, err := s.AIJob().Get(context.Background(), where.T(context.Background()).F("job_id", failedJobID))
	require.NoError(t, err)
	require.NotNil(t, job.SessionID)
	sessionID := strings.TrimSpace(*job.SessionID)
	require.NotEmpty(t, sessionID)

	initial, err := biz.GetSessionWorkbench(context.Background(), &GetSessionWorkbenchRequest{SessionID: sessionID})
	require.NoError(t, err)
	require.Contains(t, initial.NextActionHints, workbenchHintNeedHumanReview)

	_, err = sessionSvc.UpdateReviewState(context.Background(), &sessionbiz.UpdateReviewStateRequest{
		SessionID:   sessionID,
		ReviewState: sessionbiz.SessionReviewStateConfirmed,
		ReviewNote:  ptrAIString("human confirmed"),
		ReviewedBy:  ptrAIString("user:alice"),
	})
	require.NoError(t, err)
	_, err = sessionSvc.UpdateAssignment(context.Background(), &sessionbiz.UpdateAssignmentRequest{
		SessionID:  sessionID,
		Assignee:   "user:oncall-a",
		AssignedBy: ptrAIString("user:lead-a"),
		AssignNote: ptrAIString("manual handoff"),
	})
	require.NoError(t, err)

	confirmed, err := biz.GetSessionWorkbench(context.Background(), &GetSessionWorkbenchRequest{SessionID: sessionID})
	require.NoError(t, err)
	require.Equal(t, sessionbiz.SessionReviewStateConfirmed, confirmed.Session.ReviewState)
	require.Equal(t, "user:alice", confirmed.Session.ReviewedBy)
	require.Equal(t, "user:oncall-a", confirmed.Session.Assignee)
	require.Equal(t, "user:lead-a", confirmed.Session.AssignedBy)
	require.Equal(t, "manual handoff", confirmed.Session.AssignNote)
	require.NotContains(t, confirmed.NextActionHints, workbenchHintNeedHumanReview)

	_, err = sessionSvc.UpdateReviewState(context.Background(), &sessionbiz.UpdateReviewStateRequest{
		SessionID:   sessionID,
		ReviewState: sessionbiz.SessionReviewStateRejected,
		ReviewNote:  ptrAIString("reject current diagnosis"),
		ReviewedBy:  ptrAIString("user:alice"),
	})
	require.NoError(t, err)

	rejected, err := biz.GetSessionWorkbench(context.Background(), &GetSessionWorkbenchRequest{SessionID: sessionID})
	require.NoError(t, err)
	require.Equal(t, sessionbiz.SessionReviewStateRejected, rejected.Session.ReviewState)
	require.Contains(t, rejected.NextActionHints, workbenchHintConsiderFollowUp)
	require.Contains(t, rejected.NextActionHints, workbenchHintConsiderReplay)
}

func TestGetSessionWorkbench_SLAEscalationState(t *testing.T) {
	db := newAIJobTestDB(t)
	s := store.NewStore(db)
	biz := New(s)
	sessionSvc := sessionbiz.New(s)
	incident := createTestIncident(t, s)

	failedJobID := runAndFinalizeWorkbenchJob(t, biz, incident.IncidentID, workbenchRunSpec{
		Status:        "failed",
		TriggerType:   "manual",
		TriggerSource: "manual_api",
		Initiator:     "user:manual",
		ErrorMessage:  "needs assignment follow-up",
	})
	sessionID := mustSessionIDByJob(t, s, failedJobID)

	oldAssignedAt := time.Now().UTC().Add(-5 * time.Hour).Truncate(time.Second)
	_, err := sessionSvc.UpdateAssignment(context.Background(), &sessionbiz.UpdateAssignmentRequest{
		SessionID:  sessionID,
		Assignee:   "user:oncall-a",
		AssignedBy: ptrAIString("user:lead-a"),
		AssignedAt: &oldAssignedAt,
	})
	require.NoError(t, err)

	escalated, err := biz.GetSessionWorkbench(context.Background(), &GetSessionWorkbenchRequest{SessionID: sessionID})
	require.NoError(t, err)
	require.Equal(t, "escalated", escalated.Session.EscalationState)
	require.EqualValues(t, 2, escalated.Session.EscalationLevel)
	require.NotEmpty(t, escalated.Session.SlaDueAt)

	_, err = sessionSvc.UpdateReviewState(context.Background(), &sessionbiz.UpdateReviewStateRequest{
		SessionID:   sessionID,
		ReviewState: sessionbiz.SessionReviewStateConfirmed,
		ReviewNote:  ptrAIString("handled manually"),
		ReviewedBy:  ptrAIString("user:reviewer"),
	})
	require.NoError(t, err)

	cleared, err := biz.GetSessionWorkbench(context.Background(), &GetSessionWorkbenchRequest{SessionID: sessionID})
	require.NoError(t, err)
	require.Equal(t, "none", cleared.Session.EscalationState)
	require.EqualValues(t, 0, cleared.Session.EscalationLevel)
}

func TestListOperatorInbox_SortsAndFilters(t *testing.T) {
	db := newAIJobTestDB(t)
	s := store.NewStore(db)
	biz := New(s)
	sessionSvc := sessionbiz.New(s)

	incidentA := createTestIncident(t, s)
	incidentB := createTestIncident(t, s)
	incidentC := createTestIncident(t, s)

	jobA := runAndFinalizeWorkbenchJob(t, biz, incidentA.IncidentID, workbenchRunSpec{
		Status:        "failed",
		TriggerType:   "manual",
		TriggerSource: "manual_api",
		Initiator:     "user:a",
		ErrorMessage:  "requires manual investigation",
	})
	jobB := runAndFinalizeWorkbenchJob(t, biz, incidentB.IncidentID, workbenchRunSpec{
		Status:        "failed",
		TriggerType:   "follow_up",
		TriggerSource: "follow_up_api",
		Initiator:     "user:b",
		ErrorMessage:  "follow-up still inconclusive",
	})
	jobC := runAndFinalizeWorkbenchJob(t, biz, incidentC.IncidentID, workbenchRunSpec{
		Status:        "succeeded",
		TriggerType:   "manual",
		TriggerSource: "manual_api",
		Initiator:     "user:c",
		DiagnosisJSON: `{
			"summary":"healthy after mitigation",
			"root_cause":{
				"type":"dependency_timeout",
				"category":"dependency",
				"summary":"healthy after mitigation",
				"statement":"resolved",
				"confidence":0.92,
				"evidence_ids":["ev-c-1","ev-c-2"]
			},
			"hypotheses":[{"statement":"mitigated","confidence":0.92,"supporting_evidence_ids":["ev-c-1","ev-c-2"],"missing_evidence":[]}]
		}`,
		EvidenceIDs: []string{"ev-c-1", "ev-c-2"},
	})

	sessionA := mustSessionIDByJob(t, s, jobA)
	sessionB := mustSessionIDByJob(t, s, jobB)
	sessionC := mustSessionIDByJob(t, s, jobC)

	_, err := sessionSvc.UpdateReviewState(context.Background(), &sessionbiz.UpdateReviewStateRequest{
		SessionID:   sessionA,
		ReviewState: sessionbiz.SessionReviewStateInReview,
		ReviewedBy:  ptrAIString("user:reviewer-a"),
	})
	require.NoError(t, err)
	_, err = sessionSvc.UpdateReviewState(context.Background(), &sessionbiz.UpdateReviewStateRequest{
		SessionID:   sessionB,
		ReviewState: sessionbiz.SessionReviewStateRejected,
		ReviewedBy:  ptrAIString("user:reviewer-b"),
	})
	require.NoError(t, err)
	_, err = sessionSvc.UpdateReviewState(context.Background(), &sessionbiz.UpdateReviewStateRequest{
		SessionID:   sessionC,
		ReviewState: sessionbiz.SessionReviewStateConfirmed,
		ReviewedBy:  ptrAIString("user:reviewer-c"),
	})
	require.NoError(t, err)
	assignedAtPending := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	_, err = sessionSvc.UpdateAssignment(context.Background(), &sessionbiz.UpdateAssignmentRequest{
		SessionID:  sessionA,
		Assignee:   "user:oncall-a",
		AssignedBy: ptrAIString("user:lead-a"),
		AssignNote: ptrAIString("handoff to oncall-a"),
		AssignedAt: &assignedAtPending,
	})
	require.NoError(t, err)
	assignedAtEscalated := time.Now().UTC().Add(-5 * time.Hour).Truncate(time.Second)
	_, err = sessionSvc.UpdateAssignment(context.Background(), &sessionbiz.UpdateAssignmentRequest{
		SessionID:  sessionB,
		Assignee:   "user:oncall-b",
		AssignedBy: ptrAIString("user:lead-b"),
		AssignedAt: &assignedAtEscalated,
	})
	require.NoError(t, err)

	listResp, err := biz.ListOperatorInbox(context.Background(), &ListOperatorInboxRequest{
		Offset: 0,
		Limit:  20,
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, listResp.TotalCount, int64(3))
	require.GreaterOrEqual(t, len(listResp.Items), 3)
	require.Equal(t, sessionA, listResp.Items[0].SessionID)
	require.Equal(t, sessionbiz.SessionReviewStateInReview, listResp.Items[0].ReviewState)
	require.Equal(t, "user:oncall-a", listResp.Items[0].Assignee)
	require.Equal(t, "user:lead-a", listResp.Items[0].AssignedBy)
	require.Equal(t, "handoff to oncall-a", listResp.Items[0].AssignNote)
	require.Equal(t, "pending", listResp.Items[0].EscalationState)
	require.Equal(t, sessionbiz.SessionReviewStateRejected, listResp.Items[1].ReviewState)
	require.Equal(t, "escalated", listResp.Items[1].EscalationState)

	reviewStateConfirmed := sessionbiz.SessionReviewStateConfirmed
	confirmedResp, err := biz.ListOperatorInbox(context.Background(), &ListOperatorInboxRequest{
		ReviewState: &reviewStateConfirmed,
		Offset:      0,
		Limit:       20,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), confirmedResp.TotalCount)
	require.Equal(t, sessionC, confirmedResp.Items[0].SessionID)
	require.Equal(t, false, confirmedResp.Items[0].NeedsReview)

	needsReview := true
	needsReviewResp, err := biz.ListOperatorInbox(context.Background(), &ListOperatorInboxRequest{
		NeedsReview: &needsReview,
		Offset:      0,
		Limit:       20,
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), needsReviewResp.TotalCount)
	returnedIDs := []string{needsReviewResp.Items[0].SessionID, needsReviewResp.Items[1].SessionID}
	require.Contains(t, returnedIDs, sessionA)
	require.Contains(t, returnedIDs, sessionB)

	assignee := "user:oncall-a"
	assigneeResp, err := biz.ListOperatorInbox(context.Background(), &ListOperatorInboxRequest{
		Assignee: &assignee,
		Offset:   0,
		Limit:    20,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), assigneeResp.TotalCount)
	require.Equal(t, sessionA, assigneeResp.Items[0].SessionID)
	require.Equal(t, "user:oncall-a", assigneeResp.Items[0].Assignee)

	escalationPending := "pending"
	pendingResp, err := biz.ListOperatorInbox(context.Background(), &ListOperatorInboxRequest{
		EscalationState: &escalationPending,
		Offset:          0,
		Limit:           20,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), pendingResp.TotalCount)
	require.Equal(t, sessionA, pendingResp.Items[0].SessionID)

	escalationEscalated := "escalated"
	escalatedResp, err := biz.ListOperatorInbox(context.Background(), &ListOperatorInboxRequest{
		EscalationState: &escalationEscalated,
		Offset:          0,
		Limit:           20,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), escalatedResp.TotalCount)
	require.Equal(t, sessionB, escalatedResp.Items[0].SessionID)
}

type workbenchRunSpec struct {
	Status        string
	TriggerType   string
	TriggerSource string
	Initiator     string
	DiagnosisJSON string
	EvidenceIDs   []string
	ErrorMessage  string
}

func runAndFinalizeWorkbenchJob(
	t *testing.T,
	biz *aiJobBiz,
	incidentID string,
	spec workbenchRunSpec,
) string {
	t.Helper()
	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-15 * time.Minute)
	runCtx := contextx.WithTriggerType(context.Background(), spec.TriggerType)
	runCtx = contextx.WithTriggerSource(runCtx, spec.TriggerSource)
	runCtx = contextx.WithTriggerInitiator(runCtx, spec.Initiator)
	runResp, err := biz.Run(runCtx, &v1.RunAIJobRequest{
		IncidentID:     incidentID,
		Trigger:        ptrAIString(spec.TriggerType),
		CreatedBy:      ptrAIString(spec.Initiator),
		TimeRangeStart: timestamppb.New(start),
		TimeRangeEnd:   timestamppb.New(end),
	})
	require.NoError(t, err)
	require.NotEmpty(t, runResp.GetJobID())

	_, err = biz.Start(orchestratorCtx(), &v1.StartAIJobRequest{JobID: runResp.GetJobID()})
	require.NoError(t, err)
	status := strings.TrimSpace(spec.Status)
	if status == "" {
		status = "succeeded"
	}
	if status == "succeeded" {
		_, _, validateErr := validateAndNormalizeDiagnosisJSON(spec.DiagnosisJSON)
		require.NoError(t, validateErr)
	}
	for _, evidenceID := range spec.EvidenceIDs {
		ensureWorkbenchEvidence(t, biz, incidentID, evidenceID)
	}
	finalizeReq := &v1.FinalizeAIJobRequest{
		JobID:       runResp.GetJobID(),
		Status:      status,
		EvidenceIDs: spec.EvidenceIDs,
	}
	if diagnosis := strings.TrimSpace(spec.DiagnosisJSON); diagnosis != "" {
		finalizeReq.DiagnosisJSON = &diagnosis
	}
	if errorMessage := strings.TrimSpace(spec.ErrorMessage); errorMessage != "" {
		finalizeReq.ErrorMessage = &errorMessage
	}
	_, err = biz.Finalize(orchestratorCtx(), finalizeReq)
	require.NoError(t, err)
	return runResp.GetJobID()
}

func ensureWorkbenchEvidence(
	t *testing.T,
	biz *aiJobBiz,
	incidentID string,
	evidenceID string,
) {
	t.Helper()
	evidenceID = strings.TrimSpace(evidenceID)
	if evidenceID == "" {
		return
	}
	now := time.Now().UTC()
	resultJSON := `{"source":"workbench_test"}`
	require.NoError(t, biz.store.Evidence().Create(context.Background(), &model.EvidenceM{
		EvidenceID:      evidenceID,
		IncidentID:      incidentID,
		Type:            "logs",
		QueryText:       "mock://workbench-test",
		QueryHash:       "workbench-test-query-hash",
		TimeRangeStart:  now.Add(-5 * time.Minute),
		TimeRangeEnd:    now,
		ResultJSON:      resultJSON,
		ResultSizeBytes: int64(len(resultJSON)),
		CreatedBy:       "system",
	}))
}

func mustSessionIDByJob(t *testing.T, s store.IStore, jobID string) string {
	t.Helper()
	job, err := s.AIJob().Get(context.Background(), where.T(context.Background()).F("job_id", jobID))
	require.NoError(t, err)
	require.NotNil(t, job.SessionID)
	sessionID := strings.TrimSpace(*job.SessionID)
	require.NotEmpty(t, sessionID)
	return sessionID
}
