package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aiopsre/rca-api/internal/apiserver/biz"
	aijobbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/ai_job"
	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	"github.com/aiopsre/rca-api/internal/pkg/contextx"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

func TestAIJobTraceAPI_GetAndList(t *testing.T) {
	baseURL, cleanup, s, client := newTestServer(t)
	defer cleanup()

	incident := createAIJobLongPollTestIncident(t, s)
	aiBiz := biz.NewBiz(s).AIJobV1()

	leftJobID := createFinalizedTraceJob(t, aiBiz, incident.IncidentID, "replay", "replay_api", "user:replay", buildDiagnosisJSON(
		"database pool saturation confirmed",
		"db_pool_exhausted",
		"db",
		0.82,
		"ev-left-1",
		"ev-left-2",
	))
	rightJobID := createFinalizedTraceJob(t, aiBiz, incident.IncidentID, "follow_up", "follow_up_api", "user:follow-up", buildDiagnosisJSON(
		"upstream timeout dominates latency",
		"dependency_timeout",
		"dependency",
		0.67,
		"ev-right-1",
		"ev-right-2",
	))
	sessionID := "session-trace-api-1"
	require.NoError(t, s.DB(context.Background()).
		Model(&model.AIJobM{}).
		Where("job_id IN ?", []string{leftJobID, rightJobID}).
		Update("session_id", sessionID).Error)

	traceStatus, traceBody, err := doJSONRequest(client, http.MethodGet, fmt.Sprintf("%s/v1/ai/jobs/%s/trace", baseURL, leftJobID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, traceStatus)
	traceData := extractDataContainer(traceBody)
	require.Equal(t, leftJobID, extractString(traceData, "job_id", "jobID", "JobID"))
	runTrace := extractMap(traceData, "run_trace", "runTrace", "RunTrace")
	require.NotNil(t, runTrace)
	require.Equal(t, "replay", extractString(runTrace, "trigger_type", "triggerType", "TriggerType"))
	decisionTrace := extractMap(traceData, "decision_trace", "decisionTrace", "DecisionTrace")
	require.NotNil(t, decisionTrace)
	require.Equal(t, "database pool saturation confirmed", extractString(decisionTrace, "root_cause_summary", "rootCauseSummary", "RootCauseSummary"))

	incidentListStatus, incidentListBody, err := doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/incidents/%s/ai/traces?limit=10", baseURL, incident.IncidentID),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, incidentListStatus)
	incidentListData := extractDataContainer(incidentListBody)
	incidentSummaries := extractSummaryList(incidentListData)
	require.GreaterOrEqual(t, len(incidentSummaries), 2)

	sessionListStatus, sessionListBody, err := doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/sessions/%s/ai/traces?limit=10", baseURL, sessionID),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, sessionListStatus)
	sessionListData := extractDataContainer(sessionListBody)
	sessionSummaries := extractSummaryList(sessionListData)
	require.GreaterOrEqual(t, len(sessionSummaries), 2)
}

func TestAIJobTraceAPI_Compare(t *testing.T) {
	baseURL, cleanup, s, client := newTestServer(t)
	defer cleanup()

	incident := createAIJobLongPollTestIncident(t, s)
	aiBiz := biz.NewBiz(s).AIJobV1()

	leftJobID := createFinalizedTraceJob(t, aiBiz, incident.IncidentID, "replay", "replay_api", "user:replay", buildDiagnosisJSON(
		"database pool saturation confirmed",
		"db_pool_exhausted",
		"db",
		0.82,
		"ev-left-1",
		"ev-left-2",
	))
	rightJobID := createFinalizedTraceJob(t, aiBiz, incident.IncidentID, "follow_up", "follow_up_api", "user:follow-up", buildDiagnosisJSON(
		"upstream timeout dominates latency",
		"dependency_timeout",
		"dependency",
		0.67,
		"ev-right-1",
		"ev-right-2",
	))

	compareStatus, compareBody, err := doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/ai/jobs:trace-compare?left_job_id=%s&right_job_id=%s", baseURL, leftJobID, rightJobID),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, compareStatus)
	compareData := extractDataContainer(compareBody)
	require.Equal(t, true, compareData["same_incident"])
	require.Equal(t, true, compareData["changed_root_cause"])
	require.Equal(t, true, compareData["changed_confidence"])
	leftSide := extractMap(compareData, "left", "Left")
	rightSide := extractMap(compareData, "right", "Right")
	require.NotNil(t, leftSide)
	require.NotNil(t, rightSide)
	require.Equal(t, "replay", extractString(leftSide, "trigger_type", "triggerType", "TriggerType"))
	require.Equal(t, "follow_up", extractString(rightSide, "trigger_type", "triggerType", "TriggerType"))
}

func TestAIJobTraceAPI_CompareRejectsUnrelatedJobs(t *testing.T) {
	baseURL, cleanup, s, client := newTestServer(t)
	defer cleanup()

	incidentA := createAIJobLongPollTestIncident(t, s)
	incidentB := createAIJobLongPollTestIncident(t, s)
	aiBiz := biz.NewBiz(s).AIJobV1()

	leftJobID := createFinalizedTraceJob(t, aiBiz, incidentA.IncidentID, "manual", "manual_api", "user:a", buildDiagnosisJSON(
		"database pool saturation confirmed",
		"db_pool_exhausted",
		"db",
		0.82,
		"ev-a-1",
		"ev-a-2",
	))
	rightJobID := createFinalizedTraceJob(t, aiBiz, incidentB.IncidentID, "manual", "manual_api", "user:b", buildDiagnosisJSON(
		"upstream timeout dominates latency",
		"dependency_timeout",
		"dependency",
		0.67,
		"ev-b-1",
		"ev-b-2",
	))

	compareStatus, _, err := doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/ai/jobs:trace-compare?left_job_id=%s&right_job_id=%s", baseURL, leftJobID, rightJobID),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, compareStatus)
}

func TestSessionWorkbenchAPI_Get(t *testing.T) {
	baseURL, cleanup, s, client := newTestServer(t)
	defer cleanup()
	require.NoError(t, s.DB(context.Background()).AutoMigrate(&model.SessionContextM{}))

	incident := createAIJobLongPollTestIncident(t, s)
	aiBiz := biz.NewBiz(s).AIJobV1()

	_ = createFinalizedTraceJob(t, aiBiz, incident.IncidentID, "manual", "manual_api", "user:manual", buildDiagnosisJSON(
		"database pool saturation confirmed",
		"db_pool_exhausted",
		"db",
		0.82,
		"ev-manual-1",
		"ev-manual-2",
	))
	rightJobID := createFinalizedTraceJob(t, aiBiz, incident.IncidentID, "replay", "replay_api", "user:replay", buildDiagnosisJSON(
		"upstream timeout dominates latency",
		"dependency_timeout",
		"dependency",
		0.67,
		"ev-replay-1",
		"ev-replay-2",
	))
	job, err := s.AIJob().Get(context.Background(), where.T(context.Background()).F("job_id", rightJobID))
	require.NoError(t, err)
	require.NotNil(t, job.SessionID)
	sessionID := *job.SessionID

	status, body, err := doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/sessions/%s/workbench?limit=10", baseURL, sessionID),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)

	data := extractDataContainer(body)
	sessionObj := extractMap(data, "session", "Session")
	require.NotNil(t, sessionObj)
	require.Equal(t, sessionID, extractString(sessionObj, "session_id", "sessionID", "SessionID"))
	latestRun := extractMap(data, "latest_run", "latestRun", "LatestRun")
	require.NotNil(t, latestRun)
	require.Equal(t, "replay", extractString(latestRun, "trigger_type", "triggerType", "TriggerType"))
	reviewFlags := extractMap(data, "review_flags", "reviewFlags", "ReviewFlags")
	require.NotNil(t, reviewFlags)
	require.Equal(t, false, reviewFlags["human_review_required"])
	hints := extractStringArray(data["next_action_hints"])
	require.Contains(t, hints, "review_compare")
}

func TestSessionWorkbenchActionAPI_ReplayAndFollowUp(t *testing.T) {
	baseURL, cleanup, s, client := newTestServer(t)
	defer cleanup()
	require.NoError(t, s.DB(context.Background()).AutoMigrate(&model.SessionContextM{}))

	incident := createAIJobLongPollTestIncident(t, s)
	aiBiz := biz.NewBiz(s).AIJobV1()

	seedJobID := createFinalizedTraceJob(t, aiBiz, incident.IncidentID, "manual", "manual_api", "user:manual", buildDiagnosisJSON(
		"database pool saturation confirmed",
		"db_pool_exhausted",
		"db",
		0.82,
		"ev-seed-1",
		"ev-seed-2",
	))
	seedJob, err := s.AIJob().Get(context.Background(), where.T(context.Background()).F("job_id", seedJobID))
	require.NoError(t, err)
	require.NotNil(t, seedJob.SessionID)
	sessionID := strings.TrimSpace(*seedJob.SessionID)
	require.NotEmpty(t, sessionID)

	replayStatus, replayBody, err := doJSONRequest(
		client,
		http.MethodPost,
		fmt.Sprintf("%s/v1/sessions/%s/actions/replay", baseURL, sessionID),
		[]byte(`{"reason":"operator replay","operator_note":"rerun with same context"}`),
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, replayStatus)
	replayData := extractDataContainer(replayBody)
	require.Equal(t, sessionID, extractString(replayData, "session_id", "sessionID", "SessionID"))
	require.Equal(t, "replay", extractString(replayData, "trigger_type", "triggerType", "TriggerType"))
	require.Equal(t, "accepted", extractString(replayData, "status", "Status"))
	replayJobID := extractString(replayData, "job_id", "jobID", "JobID")
	require.NotEmpty(t, replayJobID)

	replayTraceStatus, replayTraceBody, err := doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/ai/jobs/%s/trace", baseURL, replayJobID),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, replayTraceStatus)
	replayTraceData := extractDataContainer(replayTraceBody)
	replayRunTrace := extractMap(replayTraceData, "run_trace", "runTrace", "RunTrace")
	require.NotNil(t, replayRunTrace)
	require.Equal(t, "replay", extractString(replayRunTrace, "trigger_type", "triggerType", "TriggerType"))
	require.Equal(
		t,
		sessionReplayActionSource,
		extractString(replayRunTrace, "trigger_source", "triggerSource", "TriggerSource"),
	)

	orcCtx := contextx.WithOrchestratorInstanceID(context.Background(), "orc-session-action-test")
	_, err = aiBiz.Start(orcCtx, &v1.StartAIJobRequest{JobID: replayJobID})
	require.NoError(t, err)
	_, err = aiBiz.Finalize(orcCtx, &v1.FinalizeAIJobRequest{
		JobID:  replayJobID,
		Status: "succeeded",
		DiagnosisJSON: strPtr(buildDiagnosisJSON(
			"replay completed for follow_up setup",
			"db_pool_exhausted",
			"db",
			0.71,
			"ev-replay-finish-1",
			"ev-replay-finish-2",
		)),
		EvidenceIDs: []string{"ev-replay-finish-1", "ev-replay-finish-2"},
	})
	require.NoError(t, err)

	followUpStatus, followUpBody, err := doJSONRequest(
		client,
		http.MethodPost,
		fmt.Sprintf("%s/v1/sessions/%s/actions/follow-up", baseURL, sessionID),
		[]byte(`{"pipeline":"basic_rca","source":"operator_review_panel","initiator":"user:alice","reason":"collect additional evidence"}`),
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, followUpStatus)
	followUpData := extractDataContainer(followUpBody)
	require.Equal(t, sessionID, extractString(followUpData, "session_id", "sessionID", "SessionID"))
	require.Equal(t, "follow_up", extractString(followUpData, "trigger_type", "triggerType", "TriggerType"))
	require.Equal(t, "accepted", extractString(followUpData, "status", "Status"))
	followUpJobID := extractString(followUpData, "job_id", "jobID", "JobID")
	require.NotEmpty(t, followUpJobID)

	followUpTraceStatus, followUpTraceBody, err := doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/ai/jobs/%s/trace", baseURL, followUpJobID),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, followUpTraceStatus)
	followUpTraceData := extractDataContainer(followUpTraceBody)
	followUpRunTrace := extractMap(followUpTraceData, "run_trace", "runTrace", "RunTrace")
	require.NotNil(t, followUpRunTrace)
	require.Equal(t, "follow_up", extractString(followUpRunTrace, "trigger_type", "triggerType", "TriggerType"))
	require.Equal(
		t,
		"operator_review_panel",
		extractString(followUpRunTrace, "trigger_source", "triggerSource", "TriggerSource"),
	)
}

func TestSessionWorkbenchActionAPI_ValidationAndSessionNotFound(t *testing.T) {
	baseURL, cleanup, s, client := newTestServer(t)
	defer cleanup()
	require.NoError(t, s.DB(context.Background()).AutoMigrate(&model.SessionContextM{}))

	incident := createAIJobLongPollTestIncident(t, s)
	aiBiz := biz.NewBiz(s).AIJobV1()
	seedJobID := createFinalizedTraceJob(t, aiBiz, incident.IncidentID, "manual", "manual_api", "user:manual", buildDiagnosisJSON(
		"database pool saturation confirmed",
		"db_pool_exhausted",
		"db",
		0.82,
		"ev-seed-a",
		"ev-seed-b",
	))
	seedJob, err := s.AIJob().Get(context.Background(), where.T(context.Background()).F("job_id", seedJobID))
	require.NoError(t, err)
	require.NotNil(t, seedJob.SessionID)
	sessionID := strings.TrimSpace(*seedJob.SessionID)

	invalidPipeline := strings.Repeat("x", 65)
	invalidStatus, _, err := doJSONRequest(
		client,
		http.MethodPost,
		fmt.Sprintf("%s/v1/sessions/%s/actions/replay", baseURL, sessionID),
		[]byte(fmt.Sprintf(`{"pipeline":%q}`, invalidPipeline)),
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, invalidStatus)

	notFoundStatus, _, err := doJSONRequest(
		client,
		http.MethodPost,
		fmt.Sprintf("%s/v1/sessions/%s/actions/follow-up", baseURL, "session-not-exists"),
		[]byte(`{"reason":"test"}`),
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, notFoundStatus)

	invalidAssignStatus, _, err := doJSONRequest(
		client,
		http.MethodPost,
		fmt.Sprintf("%s/v1/sessions/%s/actions/assign", baseURL, sessionID),
		[]byte(`{"assignee":" "}`),
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, invalidAssignStatus)
}

func TestSessionWorkbenchReviewActionAPI_StateTransitions(t *testing.T) {
	baseURL, cleanup, s, client := newTestServer(t)
	defer cleanup()
	require.NoError(t, s.DB(context.Background()).AutoMigrate(&model.SessionContextM{}))

	incident := createAIJobLongPollTestIncident(t, s)
	aiBiz := biz.NewBiz(s).AIJobV1()

	failedJobID := createFailedTraceJob(t, aiBiz, incident.IncidentID, "manual", "manual_api", "user:manual", "review required")
	job, err := s.AIJob().Get(context.Background(), where.T(context.Background()).F("job_id", failedJobID))
	require.NoError(t, err)
	require.NotNil(t, job.SessionID)
	sessionID := strings.TrimSpace(*job.SessionID)
	require.NotEmpty(t, sessionID)

	startStatus, startBody, err := doJSONRequest(
		client,
		http.MethodPost,
		fmt.Sprintf("%s/v1/sessions/%s/actions/review-start", baseURL, sessionID),
		[]byte(`{"note":"taking ownership","reviewed_by":"user:reviewer","reason_code":"manual_takeover"}`),
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, startStatus)
	startData := extractDataContainer(startBody)
	require.Equal(t, sessionID, extractString(startData, "session_id", "sessionID", "SessionID"))
	require.Equal(t, "in_review", extractString(startData, "review_state", "reviewState", "ReviewState"))
	require.Equal(t, "user:reviewer", extractString(startData, "reviewed_by", "reviewedBy", "ReviewedBy"))
	require.NotEmpty(t, extractString(startData, "reviewed_at", "reviewedAt", "ReviewedAt"))

	confirmStatus, confirmBody, err := doJSONRequest(
		client,
		http.MethodPost,
		fmt.Sprintf("%s/v1/sessions/%s/actions/review-confirm", baseURL, sessionID),
		[]byte(`{"note":"confirmed diagnosis"}`),
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, confirmStatus)
	confirmData := extractDataContainer(confirmBody)
	require.Equal(t, "confirmed", extractString(confirmData, "review_state", "reviewState", "ReviewState"))
	require.NotEmpty(t, extractString(confirmData, "reviewed_by", "reviewedBy", "ReviewedBy"))

	rejectStatus, rejectBody, err := doJSONRequest(
		client,
		http.MethodPost,
		fmt.Sprintf("%s/v1/sessions/%s/actions/review-reject", baseURL, sessionID),
		[]byte(`{"note":"reject and require another pass","reviewed_by":"user:reviewer","reason_code":"insufficient_evidence"}`),
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rejectStatus)
	rejectData := extractDataContainer(rejectBody)
	require.Equal(t, "rejected", extractString(rejectData, "review_state", "reviewState", "ReviewState"))
	require.Equal(t, "insufficient_evidence", extractString(rejectData, "reason_code", "reasonCode", "ReasonCode"))
}

func TestSessionWorkbenchReviewActionAPI_WorkbenchReflectsStateAndHints(t *testing.T) {
	baseURL, cleanup, s, client := newTestServer(t)
	defer cleanup()
	require.NoError(t, s.DB(context.Background()).AutoMigrate(&model.SessionContextM{}))

	incident := createAIJobLongPollTestIncident(t, s)
	aiBiz := biz.NewBiz(s).AIJobV1()

	failedJobID := createFailedTraceJob(t, aiBiz, incident.IncidentID, "manual", "manual_api", "user:manual", "review required")
	job, err := s.AIJob().Get(context.Background(), where.T(context.Background()).F("job_id", failedJobID))
	require.NoError(t, err)
	require.NotNil(t, job.SessionID)
	sessionID := strings.TrimSpace(*job.SessionID)
	require.NotEmpty(t, sessionID)

	initialStatus, initialBody, err := doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/sessions/%s/workbench?limit=10", baseURL, sessionID),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, initialStatus)
	initialData := extractDataContainer(initialBody)
	initialHints := extractStringArray(initialData["next_action_hints"])
	require.Contains(t, initialHints, "need_human_review")
	initialSession := extractMap(initialData, "session", "Session")
	require.NotNil(t, initialSession)
	require.Equal(t, "pending", extractString(initialSession, "review_state", "reviewState", "ReviewState"))

	confirmStatus, _, err := doJSONRequest(
		client,
		http.MethodPost,
		fmt.Sprintf("%s/v1/sessions/%s/actions/review-confirm", baseURL, sessionID),
		[]byte(`{"note":"human confirmed","reviewed_by":"user:alice"}`),
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, confirmStatus)

	confirmedWorkbenchStatus, confirmedWorkbenchBody, err := doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/sessions/%s/workbench?limit=10", baseURL, sessionID),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, confirmedWorkbenchStatus)
	confirmedData := extractDataContainer(confirmedWorkbenchBody)
	confirmedSession := extractMap(confirmedData, "session", "Session")
	require.NotNil(t, confirmedSession)
	require.Equal(t, "confirmed", extractString(confirmedSession, "review_state", "reviewState", "ReviewState"))
	require.Equal(t, "user:alice", extractString(confirmedSession, "reviewed_by", "reviewedBy", "ReviewedBy"))
	confirmedHints := extractStringArray(confirmedData["next_action_hints"])
	require.NotContains(t, confirmedHints, "need_human_review")

	startStatus, _, err := doJSONRequest(
		client,
		http.MethodPost,
		fmt.Sprintf("%s/v1/sessions/%s/actions/review-start", baseURL, sessionID),
		[]byte(`{"note":"back to investigation","reviewed_by":"user:alice"}`),
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, startStatus)

	inReviewStatus, inReviewBody, err := doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/sessions/%s/workbench?limit=10", baseURL, sessionID),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, inReviewStatus)
	inReviewData := extractDataContainer(inReviewBody)
	inReviewHints := extractStringArray(inReviewData["next_action_hints"])
	require.Contains(t, inReviewHints, "review_in_progress")

	rejectStatus, _, err := doJSONRequest(
		client,
		http.MethodPost,
		fmt.Sprintf("%s/v1/sessions/%s/actions/review-reject", baseURL, sessionID),
		[]byte(`{"note":"reject diagnosis","reviewed_by":"user:alice"}`),
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rejectStatus)

	rejectedWorkbenchStatus, rejectedWorkbenchBody, err := doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/sessions/%s/workbench?limit=10", baseURL, sessionID),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rejectedWorkbenchStatus)
	rejectedData := extractDataContainer(rejectedWorkbenchBody)
	rejectedHints := extractStringArray(rejectedData["next_action_hints"])
	require.Contains(t, rejectedHints, "consider_follow_up")
	require.Contains(t, rejectedHints, "consider_replay")
}

func TestSessionWorkbenchAssignmentActionAPI_AssignReassignAndWorkbench(t *testing.T) {
	baseURL, cleanup, s, client := newTestServer(t)
	defer cleanup()
	require.NoError(t, s.DB(context.Background()).AutoMigrate(&model.SessionContextM{}))

	incident := createAIJobLongPollTestIncident(t, s)
	aiBiz := biz.NewBiz(s).AIJobV1()
	jobID := createFailedTraceJob(t, aiBiz, incident.IncidentID, "manual", "manual_api", "user:manual", "needs assignment")
	sessionID := mustHandlerSessionIDByJob(t, s, jobID)

	assignStatus, assignBody, err := doJSONRequest(
		client,
		http.MethodPost,
		fmt.Sprintf("%s/v1/sessions/%s/actions/assign", baseURL, sessionID),
		[]byte(`{"assignee":"user:oncall-a","assigned_by":"user:lead-a","note":"handoff to oncall-a"}`),
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, assignStatus)
	assignData := extractDataContainer(assignBody)
	require.Equal(t, sessionID, extractString(assignData, "session_id", "sessionID", "SessionID"))
	require.Equal(t, "user:oncall-a", extractString(assignData, "assignee", "Assignee"))
	require.Equal(t, "user:lead-a", extractString(assignData, "assigned_by", "assignedBy", "AssignedBy"))
	require.NotEmpty(t, extractString(assignData, "assigned_at", "assignedAt", "AssignedAt"))
	require.Equal(t, "handoff to oncall-a", extractString(assignData, "assign_note", "assignNote", "AssignNote"))

	workbenchStatus, workbenchBody, err := doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/sessions/%s/workbench?limit=10", baseURL, sessionID),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, workbenchStatus)
	workbenchData := extractDataContainer(workbenchBody)
	sessionObj := extractMap(workbenchData, "session", "Session")
	require.NotNil(t, sessionObj)
	require.Equal(t, "user:oncall-a", extractString(sessionObj, "assignee", "Assignee"))
	require.Equal(t, "user:lead-a", extractString(sessionObj, "assigned_by", "assignedBy", "AssignedBy"))
	require.Equal(t, "handoff to oncall-a", extractString(sessionObj, "assign_note", "assignNote", "AssignNote"))

	reassignStatus, reassignBody, err := doJSONRequest(
		client,
		http.MethodPost,
		fmt.Sprintf("%s/v1/sessions/%s/actions/reassign", baseURL, sessionID),
		[]byte(`{"assignee":"user:oncall-b","note":"shift changed"}`),
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, reassignStatus)
	reassignData := extractDataContainer(reassignBody)
	require.Equal(t, "user:oncall-b", extractString(reassignData, "assignee", "Assignee"))
	require.NotEmpty(t, extractString(reassignData, "assigned_by", "assignedBy", "AssignedBy"))
	require.Equal(t, "shift changed", extractString(reassignData, "assign_note", "assignNote", "AssignNote"))

	workbenchStatus, workbenchBody, err = doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/sessions/%s/workbench?limit=10", baseURL, sessionID),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, workbenchStatus)
	workbenchData = extractDataContainer(workbenchBody)
	sessionObj = extractMap(workbenchData, "session", "Session")
	require.NotNil(t, sessionObj)
	require.Equal(t, "user:oncall-b", extractString(sessionObj, "assignee", "Assignee"))
	require.Equal(t, "shift changed", extractString(sessionObj, "assign_note", "assignNote", "AssignNote"))
}

func TestOperatorInboxAPI_ListAndFilters(t *testing.T) {
	baseURL, cleanup, s, client := newTestServer(t)
	defer cleanup()
	require.NoError(t, s.DB(context.Background()).AutoMigrate(&model.SessionContextM{}))

	incidentA := createAIJobLongPollTestIncident(t, s)
	incidentB := createAIJobLongPollTestIncident(t, s)
	incidentC := createAIJobLongPollTestIncident(t, s)
	aiBiz := biz.NewBiz(s).AIJobV1()

	jobA := createFailedTraceJob(t, aiBiz, incidentA.IncidentID, "manual", "manual_api", "user:a", "needs manual review")
	jobB := createFailedTraceJob(t, aiBiz, incidentB.IncidentID, "follow_up", "follow_up_api", "user:b", "follow-up failed")
	jobC := createFinalizedTraceJob(t, aiBiz, incidentC.IncidentID, "manual", "manual_api", "user:c", buildDiagnosisJSON(
		"healthy after mitigation",
		"dependency_timeout",
		"dependency",
		0.92,
		"ev-c-1",
		"ev-c-2",
	))

	sessionA := mustHandlerSessionIDByJob(t, s, jobA)
	sessionB := mustHandlerSessionIDByJob(t, s, jobB)
	sessionC := mustHandlerSessionIDByJob(t, s, jobC)

	startStatus, _, err := doJSONRequest(
		client,
		http.MethodPost,
		fmt.Sprintf("%s/v1/sessions/%s/actions/review-start", baseURL, sessionA),
		[]byte(`{"note":"taking over","reviewed_by":"user:reviewer-a"}`),
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, startStatus)

	rejectStatus, _, err := doJSONRequest(
		client,
		http.MethodPost,
		fmt.Sprintf("%s/v1/sessions/%s/actions/review-reject", baseURL, sessionB),
		[]byte(`{"note":"reject current diagnosis","reviewed_by":"user:reviewer-b"}`),
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rejectStatus)

	confirmStatus, _, err := doJSONRequest(
		client,
		http.MethodPost,
		fmt.Sprintf("%s/v1/sessions/%s/actions/review-confirm", baseURL, sessionC),
		[]byte(`{"note":"confirmed","reviewed_by":"user:reviewer-c"}`),
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, confirmStatus)

	assignStatus, _, err := doJSONRequest(
		client,
		http.MethodPost,
		fmt.Sprintf("%s/v1/sessions/%s/actions/assign", baseURL, sessionA),
		[]byte(`{"assignee":"user:oncall-a","assigned_by":"user:lead-a","note":"handoff"}`),
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, assignStatus)

	inboxStatus, inboxBody, err := doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/operator/inbox?limit=10", baseURL),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, inboxStatus)
	inboxData := extractDataContainer(inboxBody)
	items := extractInboxItems(inboxData)
	require.GreaterOrEqual(t, len(items), 3)
	require.Equal(t, sessionA, extractString(items[0], "session_id", "sessionID", "SessionID"))
	require.Equal(t, "in_review", extractString(items[0], "review_state", "reviewState", "ReviewState"))
	require.Equal(t, "user:oncall-a", extractString(items[0], "assignee", "Assignee"))
	require.NotEmpty(t, extractString(items[0], "workbench_path", "workbenchPath", "WorkbenchPath"))
	require.NotEmpty(t, extractString(items[0], "last_activity_at", "lastActivityAt", "LastActivityAt"))

	reviewStateStatus, reviewStateBody, err := doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/operator/inbox?review_state=confirmed", baseURL),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, reviewStateStatus)
	reviewStateData := extractDataContainer(reviewStateBody)
	confirmedItems := extractInboxItems(reviewStateData)
	require.Equal(t, 1, len(confirmedItems))
	require.Equal(t, sessionC, extractString(confirmedItems[0], "session_id", "sessionID", "SessionID"))
	require.Equal(t, "confirmed", extractString(confirmedItems[0], "review_state", "reviewState", "ReviewState"))

	needsReviewStatus, needsReviewBody, err := doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/operator/inbox?needs_review=true", baseURL),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, needsReviewStatus)
	needsReviewData := extractDataContainer(needsReviewBody)
	needsReviewItems := extractInboxItems(needsReviewData)
	require.Equal(t, 2, len(needsReviewItems))
	ids := []string{
		extractString(needsReviewItems[0], "session_id", "sessionID", "SessionID"),
		extractString(needsReviewItems[1], "session_id", "sessionID", "SessionID"),
	}
	require.Contains(t, ids, sessionA)
	require.Contains(t, ids, sessionB)

	assigneeStatus, assigneeBody, err := doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/operator/inbox?assignee=user:oncall-a", baseURL),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, assigneeStatus)
	assigneeData := extractDataContainer(assigneeBody)
	assigneeItems := extractInboxItems(assigneeData)
	require.Equal(t, 1, len(assigneeItems))
	require.Equal(t, sessionA, extractString(assigneeItems[0], "session_id", "sessionID", "SessionID"))
	require.Equal(t, "user:oncall-a", extractString(assigneeItems[0], "assignee", "Assignee"))
}

func TestOperatorInboxAPI_InvalidQuery(t *testing.T) {
	baseURL, cleanup, _, client := newTestServer(t)
	defer cleanup()

	status, _, err := doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/operator/inbox?needs_review=bad-bool", baseURL),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status)
}

func createFinalizedTraceJob(
	t *testing.T,
	aiBiz aijobbiz.AIJobBiz,
	incidentID string,
	triggerType string,
	triggerSource string,
	initiator string,
	diagnosisJSON string,
) string {
	t.Helper()

	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-20 * time.Minute)
	runCtx := contextx.WithTriggerType(context.Background(), triggerType)
	runCtx = contextx.WithTriggerSource(runCtx, triggerSource)
	runCtx = contextx.WithTriggerInitiator(runCtx, initiator)
	runResp, err := aiBiz.Run(runCtx, &v1.RunAIJobRequest{
		IncidentID:     incidentID,
		Trigger:        strPtr(triggerType),
		CreatedBy:      strPtr(initiator),
		TimeRangeStart: timestamppb.New(start),
		TimeRangeEnd:   timestamppb.New(end),
	})
	require.NoError(t, err)
	require.NotEmpty(t, runResp.GetJobID())

	orcCtx := contextx.WithOrchestratorInstanceID(context.Background(), "orc-trace-api-test")
	_, err = aiBiz.Start(orcCtx, &v1.StartAIJobRequest{JobID: runResp.GetJobID()})
	require.NoError(t, err)
	_, err = aiBiz.Finalize(orcCtx, &v1.FinalizeAIJobRequest{
		JobID:         runResp.GetJobID(),
		Status:        "succeeded",
		DiagnosisJSON: strPtr(diagnosisJSON),
		EvidenceIDs:   []string{"ev-1", "ev-2"},
	})
	require.NoError(t, err)
	return runResp.GetJobID()
}

func createFailedTraceJob(
	t *testing.T,
	aiBiz aijobbiz.AIJobBiz,
	incidentID string,
	triggerType string,
	triggerSource string,
	initiator string,
	errorMessage string,
) string {
	t.Helper()
	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-20 * time.Minute)
	runCtx := contextx.WithTriggerType(context.Background(), triggerType)
	runCtx = contextx.WithTriggerSource(runCtx, triggerSource)
	runCtx = contextx.WithTriggerInitiator(runCtx, initiator)
	runResp, err := aiBiz.Run(runCtx, &v1.RunAIJobRequest{
		IncidentID:     incidentID,
		Trigger:        strPtr(triggerType),
		CreatedBy:      strPtr(initiator),
		TimeRangeStart: timestamppb.New(start),
		TimeRangeEnd:   timestamppb.New(end),
	})
	require.NoError(t, err)
	require.NotEmpty(t, runResp.GetJobID())

	orcCtx := contextx.WithOrchestratorInstanceID(context.Background(), "orc-trace-api-failed-test")
	_, err = aiBiz.Start(orcCtx, &v1.StartAIJobRequest{JobID: runResp.GetJobID()})
	require.NoError(t, err)
	_, err = aiBiz.Finalize(orcCtx, &v1.FinalizeAIJobRequest{
		JobID:        runResp.GetJobID(),
		Status:       "failed",
		ErrorMessage: strPtr(errorMessage),
	})
	require.NoError(t, err)
	return runResp.GetJobID()
}

func mustHandlerSessionIDByJob(t *testing.T, s store.IStore, jobID string) string {
	t.Helper()
	job, err := s.AIJob().Get(context.Background(), where.T(context.Background()).F("job_id", jobID))
	require.NoError(t, err)
	require.NotNil(t, job.SessionID)
	sessionID := strings.TrimSpace(*job.SessionID)
	require.NotEmpty(t, sessionID)
	return sessionID
}

func buildDiagnosisJSON(summary string, rootCauseType string, category string, confidence float64, ev1 string, ev2 string) string {
	return fmt.Sprintf(`{
		"summary":%q,
		"root_cause":{
			"type":%q,
			"category":%q,
			"summary":%q,
			"statement":"trace compare test statement",
			"confidence":%.2f,
			"evidence_ids":[%q,%q]
		},
		"hypotheses":[
			{
				"statement":"trace compare test hypothesis",
				"confidence":%.2f,
				"supporting_evidence_ids":[%q,%q],
				"missing_evidence":[]
			}
		]
	}`,
		summary,
		rootCauseType,
		category,
		summary,
		confidence,
		ev1,
		ev2,
		confidence,
		ev1,
		ev2,
	)
}

func extractMap(container map[string]any, keys ...string) map[string]any {
	for _, key := range keys {
		if out, ok := container[key].(map[string]any); ok {
			return out
		}
	}
	return nil
}

func extractSummaryList(container map[string]any) []map[string]any {
	raw := container["summaries"]
	if raw == nil {
		raw = container["Summaries"]
	}
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if obj, ok := item.(map[string]any); ok {
			out = append(out, obj)
		}
	}
	return out
}

func extractStringArray(raw any) []string {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		if value, ok := item.(string); ok {
			out = append(out, value)
		}
	}
	return out
}

func extractInboxItems(container map[string]any) []map[string]any {
	raw := container["items"]
	if raw == nil {
		raw = container["Items"]
	}
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if obj, ok := item.(map[string]any); ok {
			out = append(out, obj)
		}
	}
	return out
}
