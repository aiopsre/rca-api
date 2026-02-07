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

	confirmed, err := biz.GetSessionWorkbench(context.Background(), &GetSessionWorkbenchRequest{SessionID: sessionID})
	require.NoError(t, err)
	require.Equal(t, sessionbiz.SessionReviewStateConfirmed, confirmed.Session.ReviewState)
	require.Equal(t, "user:alice", confirmed.Session.ReviewedBy)
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
