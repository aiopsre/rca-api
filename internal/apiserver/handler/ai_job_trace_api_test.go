package handler

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aiopsre/rca-api/internal/apiserver/biz"
	aijobbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/ai_job"
	sessionbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/session"
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
	latestCompare := extractMap(data, "latest_compare", "latestCompare", "LatestCompare")
	require.NotNil(t, latestCompare)
	drilldown := extractMap(data, "drilldown", "Drilldown")
	require.NotNil(t, drilldown)
	require.Equal(t, fmt.Sprintf("/v1/ai/jobs/%s/trace", rightJobID), extractString(drilldown, "latest_trace_path", "latestTracePath", "LatestTracePath"))
	require.Equal(
		t,
		fmt.Sprintf("/v1/ai/jobs:trace-compare?left_job_id=%s&right_job_id=%s", extractString(latestCompare, "left_job_id", "leftJobID", "LeftJobID"), rightJobID),
		extractString(drilldown, "latest_compare_path", "latestComparePath", "LatestComparePath"),
	)
	require.Equal(t, fmt.Sprintf("/v1/sessions/%s/history", sessionID), extractString(drilldown, "history_path", "historyPath", "HistoryPath"))
	require.Equal(
		t,
		fmt.Sprintf("/v1/operator/assignment_history?session_id=%s", sessionID),
		extractString(drilldown, "assignment_history_path", "assignmentHistoryPath", "AssignmentHistoryPath"),
	)
	latestDecisionDrill := extractMap(drilldown, "latest_decision", "latestDecision", "LatestDecision")
	require.NotNil(t, latestDecisionDrill)
	require.Equal(t, rightJobID, extractString(latestDecisionDrill, "job_id", "jobID", "JobID"))
	require.Equal(t, true, latestDecisionDrill["decision_detail_available"])
	latestAssignmentDrill := extractMap(drilldown, "latest_assignment", "latestAssignment", "LatestAssignment")
	require.NotNil(t, latestAssignmentDrill)
	require.Equal(t, "", extractString(latestAssignmentDrill, "assignee", "Assignee"))
	pinnedEvidenceDrill := extractMap(drilldown, "pinned_evidence", "pinnedEvidence", "PinnedEvidence")
	require.NotNil(t, pinnedEvidenceDrill)
	require.Equal(t, fmt.Sprintf("/v1/incidents/%s/evidence", incident.IncidentID), extractString(pinnedEvidenceDrill, "incident_evidence_path", "incidentEvidencePath", "IncidentEvidencePath"))
	historyDrill := extractMap(drilldown, "history", "History")
	require.NotNil(t, historyDrill)
	require.Equal(t, fmt.Sprintf("/v1/sessions/%s/history?order=desc&offset=0&limit=5", sessionID), extractString(historyDrill, "recent_path", "recentPath", "RecentPath"))
	nextViews := extractStringArray(drilldown["recommended_next_view"])
	require.Contains(t, nextViews, fmt.Sprintf("/v1/ai/jobs/%s/trace", rightJobID))
	require.Contains(t, nextViews, fmt.Sprintf("/v1/sessions/%s/history", sessionID))
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
	require.Equal(t, "user:test-user", extractString(startData, "reviewed_by", "reviewedBy", "ReviewedBy"))
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
	require.Equal(t, "user:test-user", extractString(confirmedSession, "reviewed_by", "reviewedBy", "ReviewedBy"))
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
	require.Equal(t, "user:test-user", extractString(assignData, "assigned_by", "assignedBy", "AssignedBy"))
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
	require.Equal(t, "user:test-user", extractString(sessionObj, "assigned_by", "assignedBy", "AssignedBy"))
	require.Equal(t, "handoff to oncall-a", extractString(sessionObj, "assign_note", "assignNote", "AssignNote"))
	require.Equal(t, "none", extractString(sessionObj, "escalation_state", "escalationState", "EscalationState"))
	require.NotEmpty(t, extractString(sessionObj, "sla_due_at", "slaDueAt", "SlaDueAt"))

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
	require.Equal(t, "none", extractString(sessionObj, "escalation_state", "escalationState", "EscalationState"))
}

func TestSessionHistoryAPI_ListAndWorkbenchRecent(t *testing.T) {
	baseURL, cleanup, s, client := newTestServer(t)
	defer cleanup()
	require.NoError(t, s.DB(context.Background()).AutoMigrate(&model.SessionContextM{}, &model.SessionHistoryEventM{}))

	incident := createAIJobLongPollTestIncident(t, s)
	aiBiz := biz.NewBiz(s).AIJobV1()
	jobID := createFailedTraceJob(t, aiBiz, incident.IncidentID, "manual", "manual_api", "user:manual", "needs operator action")
	sessionID := mustHandlerSessionIDByJob(t, s, jobID)

	assignStatus, _, err := doJSONRequest(
		client,
		http.MethodPost,
		fmt.Sprintf("%s/v1/sessions/%s/actions/assign", baseURL, sessionID),
		[]byte(`{"assignee":"user:oncall-a","assigned_by":"user:lead-a","note":"handoff"}`),
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, assignStatus)
	reassignStatus, _, err := doJSONRequest(
		client,
		http.MethodPost,
		fmt.Sprintf("%s/v1/sessions/%s/actions/reassign", baseURL, sessionID),
		[]byte(`{"assignee":"user:oncall-b","assigned_by":"user:lead-b","note":"shift changed"}`),
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, reassignStatus)

	reviewStatus, _, err := doJSONRequest(
		client,
		http.MethodPost,
		fmt.Sprintf("%s/v1/sessions/%s/actions/review-start", baseURL, sessionID),
		[]byte(`{"note":"start review","reviewed_by":"user:reviewer-a","reason_code":"manual_takeover"}`),
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, reviewStatus)

	replayStatus, replayBody, err := doJSONRequest(
		client,
		http.MethodPost,
		fmt.Sprintf("%s/v1/sessions/%s/actions/replay", baseURL, sessionID),
		[]byte(`{"reason":"operator replay","operator_note":"rerun latest evidence"}`),
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, replayStatus)
	replayData := extractDataContainer(replayBody)
	replayJobID := extractString(replayData, "job_id", "jobID", "JobID")
	require.NotEmpty(t, replayJobID)

	orcCtx := contextx.WithOrchestratorInstanceID(context.Background(), "orc-session-history-test")
	_, err = aiBiz.Start(orcCtx, &v1.StartAIJobRequest{JobID: replayJobID})
	require.NoError(t, err)
	_, err = aiBiz.Finalize(orcCtx, &v1.FinalizeAIJobRequest{
		JobID:  replayJobID,
		Status: "succeeded",
		DiagnosisJSON: strPtr(buildDiagnosisJSON(
			"replay succeeded for follow-up",
			"db_pool_exhausted",
			"db",
			0.75,
			"ev-history-1",
			"ev-history-2",
		)),
		EvidenceIDs: []string{"ev-history-1", "ev-history-2"},
	})
	require.NoError(t, err)

	followUpStatus, _, err := doJSONRequest(
		client,
		http.MethodPost,
		fmt.Sprintf("%s/v1/sessions/%s/actions/follow-up", baseURL, sessionID),
		[]byte(`{"reason":"collect extra context","source":"operator_review_panel","initiator":"user:reviewer-a"}`),
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, followUpStatus)

	historyStatus, historyBody, err := doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/sessions/%s/history?offset=0&limit=20", baseURL, sessionID),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, historyStatus)
	historyData := extractDataContainer(historyBody)
	historyItems := extractHistoryItems(historyData)
	require.GreaterOrEqual(t, len(historyItems), 4)
	historyEventTypes := map[string]bool{}
	for _, item := range historyItems {
		historyEventTypes[extractString(item, "event_type", "eventType", "EventType")] = true
	}
	require.True(t, historyEventTypes[sessionbiz.SessionHistoryEventAssigned])
	require.True(t, historyEventTypes[sessionbiz.SessionHistoryEventReviewStarted])
	require.True(t, historyEventTypes[sessionbiz.SessionHistoryEventReplayRequested])
	require.True(t, historyEventTypes[sessionbiz.SessionHistoryEventFollowUpRequested])
	require.True(t, historyEventTypes[sessionbiz.SessionHistoryEventReassigned])

	assignmentHistoryStatus, assignmentHistoryBody, err := doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/sessions/%s/assignment_history?offset=0&limit=10", baseURL, sessionID),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, assignmentHistoryStatus)
	assignmentHistoryData := extractDataContainer(assignmentHistoryBody)
	assignmentItems := extractHistoryItems(assignmentHistoryData)
	require.Len(t, assignmentItems, 2)
	require.Equal(t, sessionbiz.SessionHistoryEventReassigned, extractString(assignmentItems[0], "event_type", "eventType", "EventType"))
	require.Equal(t, "user:oncall-b", extractString(assignmentItems[0], "assignee", "Assignee"))
	require.Equal(t, "user:test-user", extractString(assignmentItems[0], "assigned_by", "assignedBy", "AssignedBy"))
	require.Equal(t, "user:oncall-a", extractString(assignmentItems[0], "previous_assignee", "previousAssignee", "PreviousAssignee"))
	require.Equal(t, "shift changed", extractString(assignmentItems[0], "note", "Note"))
	require.Equal(t, sessionbiz.SessionHistoryEventAssigned, extractString(assignmentItems[1], "event_type", "eventType", "EventType"))
	require.Equal(t, "user:oncall-a", extractString(assignmentItems[1], "assignee", "Assignee"))

	assignmentOrder := "asc"
	assignmentAscStatus, assignmentAscBody, err := doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf(
			"%s/v1/sessions/%s/assignment_history?offset=0&limit=1&order=%s",
			baseURL,
			sessionID,
			assignmentOrder,
		),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, assignmentAscStatus)
	assignmentAscData := extractDataContainer(assignmentAscBody)
	assignmentAscItems := extractHistoryItems(assignmentAscData)
	require.Len(t, assignmentAscItems, 1)
	require.Equal(t, sessionbiz.SessionHistoryEventAssigned, extractString(assignmentAscItems[0], "event_type", "eventType", "EventType"))

	globalAssignmentStatus, globalAssignmentBody, err := doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/operator/assignment_history?session_id=%s&offset=0&limit=10", baseURL, sessionID),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, globalAssignmentStatus)
	globalAssignmentData := extractDataContainer(globalAssignmentBody)
	globalAssignmentItems := extractHistoryItems(globalAssignmentData)
	require.Len(t, globalAssignmentItems, 2)
	require.Equal(t, sessionID, extractString(globalAssignmentItems[0], "session_id", "sessionID", "SessionID"))
	require.Equal(t, sessionbiz.SessionHistoryEventReassigned, extractString(globalAssignmentItems[0], "event_type", "eventType", "EventType"))

	globalAssignmentAscStatus, globalAssignmentAscBody, err := doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/operator/assignment_history?session_id=%s&offset=0&limit=1&order=asc", baseURL, sessionID),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, globalAssignmentAscStatus)
	globalAssignmentAscData := extractDataContainer(globalAssignmentAscBody)
	globalAssignmentAscItems := extractHistoryItems(globalAssignmentAscData)
	require.Len(t, globalAssignmentAscItems, 1)
	require.Equal(t, sessionbiz.SessionHistoryEventAssigned, extractString(globalAssignmentAscItems[0], "event_type", "eventType", "EventType"))

	ascStatus, ascBody, err := doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/sessions/%s/history?order=asc&offset=0&limit=2", baseURL, sessionID),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, ascStatus)
	ascData := extractDataContainer(ascBody)
	ascItems := extractHistoryItems(ascData)
	require.Len(t, ascItems, 2)
	require.Equal(t, sessionbiz.SessionHistoryEventAssigned, extractString(ascItems[0], "event_type", "eventType", "EventType"))

	workbenchStatus, workbenchBody, err := doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/sessions/%s/workbench?limit=10", baseURL, sessionID),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, workbenchStatus)
	workbenchData := extractDataContainer(workbenchBody)
	require.Equal(t, fmt.Sprintf("/v1/sessions/%s/history", sessionID), extractString(workbenchData, "history_path", "historyPath", "HistoryPath"))
	recentHistory := extractHistoryItems(map[string]any{
		"events": workbenchData["recent_history"],
	})
	require.NotEmpty(t, recentHistory)
	drilldown := extractMap(workbenchData, "drilldown", "Drilldown")
	require.NotNil(t, drilldown)
	require.Equal(
		t,
		fmt.Sprintf("/v1/operator/assignment_history?session_id=%s", sessionID),
		extractString(drilldown, "assignment_history_path", "assignmentHistoryPath", "AssignmentHistoryPath"),
	)
	latestAssignment := extractMap(drilldown, "latest_assignment", "latestAssignment", "LatestAssignment")
	require.NotNil(t, latestAssignment)
	require.Equal(t, "user:oncall-b", extractString(latestAssignment, "assignee", "Assignee"))
	require.Equal(t, "user:test-user", extractString(latestAssignment, "assigned_by", "assignedBy", "AssignedBy"))
}

func TestOperatorInboxAPI_ListAndFilters(t *testing.T) {
	baseURL, cleanup, s, client := newTestServer(t)
	defer cleanup()
	require.NoError(t, s.DB(context.Background()).AutoMigrate(&model.SessionContextM{}))

	incidentA := createAIJobLongPollTestIncident(t, s)
	incidentB := createAIJobLongPollTestIncident(t, s)
	incidentC := createAIJobLongPollTestIncident(t, s)
	aiBiz := biz.NewBiz(s).AIJobV1()
	sessionSvc := biz.NewBiz(s).SessionV1()

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

	assignedAtPending := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	_, err = sessionSvc.UpdateAssignment(context.Background(), &sessionbiz.UpdateAssignmentRequest{
		SessionID:  sessionA,
		Assignee:   "user:oncall-a",
		AssignedBy: strPtr("user:lead-a"),
		AssignNote: strPtr("handoff"),
		AssignedAt: &assignedAtPending,
	})
	require.NoError(t, err)
	assignedAtEscalated := time.Now().UTC().Add(-5 * time.Hour).Truncate(time.Second)
	_, err = sessionSvc.UpdateAssignment(context.Background(), &sessionbiz.UpdateAssignmentRequest{
		SessionID:  sessionB,
		Assignee:   "user:oncall-b",
		AssignedBy: strPtr("user:lead-b"),
		AssignedAt: &assignedAtEscalated,
	})
	require.NoError(t, err)

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
	require.Equal(t, "pending", extractString(items[0], "escalation_state", "escalationState", "EscalationState"))
	require.NotEmpty(t, extractString(items[0], "sla_due_at", "slaDueAt", "SlaDueAt"))
	require.NotEmpty(t, extractString(items[0], "workbench_path", "workbenchPath", "WorkbenchPath"))
	require.NotEmpty(t, extractString(items[0], "last_activity_at", "lastActivityAt", "LastActivityAt"))
	require.True(t, extractBool(items[0], "long_unhandled", "longUnhandled", "LongUnhandled"))
	require.True(t, extractBool(items[0], "high_risk", "highRisk", "HighRisk"))

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

	escalationPendingStatus, escalationPendingBody, err := doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/operator/inbox?escalation_state=pending", baseURL),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, escalationPendingStatus)
	escalationPendingData := extractDataContainer(escalationPendingBody)
	pendingItems := extractInboxItems(escalationPendingData)
	require.Equal(t, 1, len(pendingItems))
	require.Equal(t, sessionA, extractString(pendingItems[0], "session_id", "sessionID", "SessionID"))

	escalationEscalatedStatus, escalationEscalatedBody, err := doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/operator/inbox?escalation_state=escalated", baseURL),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, escalationEscalatedStatus)
	escalationEscalatedData := extractDataContainer(escalationEscalatedBody)
	escalatedItems := extractInboxItems(escalationEscalatedData)
	require.Equal(t, 1, len(escalatedItems))
	require.Equal(t, sessionB, extractString(escalatedItems[0], "session_id", "sessionID", "SessionID"))
	require.True(t, extractBool(escalatedItems[0], "high_risk", "highRisk", "HighRisk"))
}

func TestOperatorDashboardAPI_Get(t *testing.T) {
	baseURL, cleanup, s, client := newTestServer(t)
	defer cleanup()
	require.NoError(t, s.DB(context.Background()).AutoMigrate(&model.SessionContextM{}))

	incidentA := createAIJobLongPollTestIncident(t, s)
	incidentB := createAIJobLongPollTestIncident(t, s)
	incidentC := createAIJobLongPollTestIncident(t, s)
	aiBiz := biz.NewBiz(s).AIJobV1()
	sessionSvc := biz.NewBiz(s).SessionV1()

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

	assignedAtPending := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	_, err = sessionSvc.UpdateAssignment(context.Background(), &sessionbiz.UpdateAssignmentRequest{
		SessionID:  sessionA,
		Assignee:   "user:oncall-a",
		AssignedBy: strPtr("user:lead-a"),
		AssignedAt: &assignedAtPending,
	})
	require.NoError(t, err)
	assignedAtEscalated := time.Now().UTC().Add(-5 * time.Hour).Truncate(time.Second)
	_, err = sessionSvc.UpdateAssignment(context.Background(), &sessionbiz.UpdateAssignmentRequest{
		SessionID:  sessionB,
		Assignee:   "user:oncall-b",
		AssignedBy: strPtr("user:lead-b"),
		AssignedAt: &assignedAtEscalated,
	})
	require.NoError(t, err)

	status, body, err := doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/operator/dashboard", baseURL),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)

	data := extractDataContainer(body)
	require.NotEmpty(t, extractString(data, "as_of", "asOf", "AsOf"))

	overview := extractMap(data, "overview", "Overview")
	require.NotNil(t, overview)
	require.EqualValues(t, 3, extractInt64(overview, "total_sessions", "totalSessions", "TotalSessions"))
	require.EqualValues(t, 2, extractInt64(overview, "needs_review_count", "needsReviewCount", "NeedsReviewCount"))
	require.EqualValues(t, 1, extractInt64(overview, "in_review_count", "inReviewCount", "InReviewCount"))
	require.EqualValues(t, 1, extractInt64(overview, "confirmed_count", "confirmedCount", "ConfirmedCount"))
	require.EqualValues(t, 1, extractInt64(overview, "rejected_count", "rejectedCount", "RejectedCount"))
	require.EqualValues(t, 2, extractInt64(overview, "assigned_count", "assignedCount", "AssignedCount"))
	require.EqualValues(t, 1, extractInt64(overview, "unassigned_count", "unassignedCount", "UnassignedCount"))
	require.EqualValues(t, 0, extractInt64(overview, "my_queue_count", "myQueueCount", "MyQueueCount"))
	require.EqualValues(t, 2, extractInt64(overview, "long_unhandled_count", "longUnhandledCount", "LongUnhandledCount"))
	require.EqualValues(t, 2, extractInt64(overview, "high_risk_count", "highRiskCount", "HighRiskCount"))

	escalation := extractMap(data, "escalation", "Escalation")
	require.NotNil(t, escalation)
	require.EqualValues(t, 1, extractInt64(escalation, "pending_escalation_count", "pendingEscalationCount", "PendingEscalationCount"))
	require.EqualValues(t, 1, extractInt64(escalation, "escalated_count", "escalatedCount", "EscalatedCount"))
	require.EqualValues(t, 1, extractInt64(escalation, "normal_count", "normalCount", "NormalCount"))

	distribution := extractMap(data, "distribution", "Distribution")
	require.NotNil(t, distribution)
	bySessionType := extractMap(distribution, "by_session_type", "bySessionType", "BySessionType")
	require.EqualValues(t, 3, extractInt64(bySessionType, "incident"))
	byTriggerType := extractMap(distribution, "by_latest_trigger_type", "byLatestTriggerType", "ByLatestTriggerType")
	require.EqualValues(t, 2, extractInt64(byTriggerType, "manual"))
	require.EqualValues(t, 1, extractInt64(byTriggerType, "follow_up"))

	queuePreview := extractMap(data, "queue_preview", "queuePreview", "QueuePreview")
	require.NotNil(t, queuePreview)
	inReviewItems := extractInboxItems(map[string]any{"items": queuePreview["in_review"]})
	require.NotEmpty(t, inReviewItems)
	require.Equal(t, sessionA, extractString(inReviewItems[0], "session_id", "sessionID", "SessionID"))
	escalatedItems := extractInboxItems(map[string]any{"items": queuePreview["escalated"]})
	require.NotEmpty(t, escalatedItems)
	require.Equal(t, sessionB, extractString(escalatedItems[0], "session_id", "sessionID", "SessionID"))

	navigation := extractMap(data, "navigation", "Navigation")
	require.NotNil(t, navigation)
	require.Equal(t, "/v1/operator/inbox", extractString(navigation, "inbox_path", "inboxPath", "InboxPath"))
	recommendedFilters := extractMap(navigation, "recommended_filters", "recommendedFilters", "RecommendedFilters")
	require.Equal(t, "/v1/operator/inbox?review_state=in_review", extractString(recommendedFilters, "in_review"))
	require.Equal(t, "/v1/operator/inbox?escalation_state=escalated", extractString(recommendedFilters, "escalated"))
}

func TestOperatorDashboardTrendsAPI_Get(t *testing.T) {
	baseURL, cleanup, s, client := newTestServer(t)
	defer cleanup()
	require.NoError(t, s.DB(context.Background()).AutoMigrate(
		&model.SessionContextM{},
		&model.SessionHistoryEventM{},
	))

	incidentA := createAIJobLongPollTestIncident(t, s)
	incidentB := createAIJobLongPollTestIncident(t, s)
	incidentA.Namespace = "payments"
	incidentB.Namespace = "checkout"
	require.NoError(t, s.Incident().Update(context.Background(), incidentA))
	require.NoError(t, s.Incident().Update(context.Background(), incidentB))

	aiBiz := biz.NewBiz(s).AIJobV1()
	sessionSvc := biz.NewBiz(s).SessionV1()
	jobA := createFailedTraceJob(t, aiBiz, incidentA.IncidentID, "manual", "manual_api", "user:a", "trend replay")
	jobB := createFailedTraceJob(t, aiBiz, incidentB.IncidentID, "manual", "manual_api", "user:b", "trend follow-up")
	sessionA := mustHandlerSessionIDByJob(t, s, jobA)
	sessionB := mustHandlerSessionIDByJob(t, s, jobB)

	now := time.Now().UTC().Truncate(time.Second)
	events := []struct {
		sessionID string
		eventType string
		actor     string
		createdAt time.Time
	}{
		{sessionA, sessionbiz.SessionHistoryEventReplayRequested, "user:alice", now.Add(-2 * 24 * time.Hour)},
		{sessionA, sessionbiz.SessionHistoryEventReviewStarted, "user:alice", now.Add(-24 * time.Hour)},
		{sessionB, sessionbiz.SessionHistoryEventFollowUpRequested, "user:bob", now.Add(-23 * time.Hour)},
		{sessionB, sessionbiz.SessionHistoryEventEscalationEscalated, "system:sla", now.Add(-23 * time.Hour)},
	}
	for _, item := range events {
		item := item
		_, err := sessionSvc.AppendHistoryEvent(context.Background(), &sessionbiz.AppendSessionHistoryEventRequest{
			SessionID: item.sessionID,
			EventType: item.eventType,
			Actor:     strPtr(item.actor),
			CreatedAt: &item.createdAt,
		})
		require.NoError(t, err)
	}

	status, body, err := doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/operator/dashboard/trends?window=7d&group_by=operator", baseURL),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	data := extractDataContainer(body)
	require.Equal(t, "7d", extractString(data, "window", "Window"))
	require.Equal(t, "operator", extractString(data, "group_by", "groupBy", "GroupBy"))
	summary := extractMap(data, "summary", "Summary")
	require.EqualValues(t, 1, extractInt64(summary, "replay_count", "replayCount", "ReplayCount"))
	require.EqualValues(t, 1, extractInt64(summary, "follow_up_count", "followUpCount", "FollowUpCount"))
	require.EqualValues(t, 1, extractInt64(summary, "review_started_count", "reviewStartedCount", "ReviewStartedCount"))
	require.EqualValues(t, 1, extractInt64(summary, "sla_escalated_count", "slaEscalatedCount", "SLAEscalatedCount"))
	require.EqualValues(t, 4, extractInt64(summary, "total_count", "totalCount", "TotalCount"))
	byDay := extractInboxItems(map[string]any{"items": data["by_day"]})
	require.Len(t, byDay, 7)
	grouped := extractInboxItems(map[string]any{"items": data["grouped"]})
	require.NotEmpty(t, grouped)
	navigation := extractMap(data, "navigation", "Navigation")
	require.Equal(t, "/v1/operator/dashboard", extractString(navigation, "dashboard_path", "dashboardPath", "DashboardPath"))

	teamToken := mustLoginOperatorToken(t, client, baseURL, map[string]any{
		"operator_id": "alice",
		"team_ids":    []string{"namespace:payments"},
		"scopes":      []string{"ai.read"},
	})
	filteredStatus, filteredBody, err := doJSONRequestWithHeaders(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/operator/dashboard/trends?window=7d&group_by=team&team_id=namespace:payments&operator=alice", baseURL),
		nil,
		map[string]string{"Authorization": "Bearer " + teamToken},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, filteredStatus)
	filteredData := extractDataContainer(filteredBody)
	filteredSummary := extractMap(filteredData, "summary", "Summary")
	require.EqualValues(t, 1, extractInt64(filteredSummary, "replay_count", "replayCount", "ReplayCount"))
	require.EqualValues(t, 1, extractInt64(filteredSummary, "review_started_count", "reviewStartedCount", "ReviewStartedCount"))
	require.EqualValues(t, 0, extractInt64(filteredSummary, "follow_up_count", "followUpCount", "FollowUpCount"))
	applied := extractMap(filteredData, "applied", "Applied")
	require.Equal(t, "alice", extractString(applied, "operator", "Operator"))
	require.Equal(t, "namespace:payments", extractString(applied, "team_id", "teamID", "TeamID"))
}

func TestSessionWorkbenchViewerAPI_Get(t *testing.T) {
	baseURL, cleanup, s, client := newTestServer(t)
	defer cleanup()
	require.NoError(t, s.DB(context.Background()).AutoMigrate(
		&model.SessionContextM{},
		&model.SessionHistoryEventM{},
		&model.IncidentVerificationRunM{},
		&model.EvidenceM{},
	))

	incident := createAIJobLongPollTestIncident(t, s)
	aiBiz := biz.NewBiz(s).AIJobV1()
	sessionSvc := biz.NewBiz(s).SessionV1()

	leftJobID := createFinalizedTraceJob(t, aiBiz, incident.IncidentID, "manual", "manual_api", "user:manual", buildDiagnosisJSON(
		"database pool saturation confirmed",
		"db_pool_exhausted",
		"db",
		0.82,
		"ev-left-1",
		"ev-left-2",
	))
	rightJobID := createFailedTraceJob(t, aiBiz, incident.IncidentID, "replay", "replay_api", "user:replay", "viewer replay failed")
	sessionID := mustHandlerSessionIDByJob(t, s, rightJobID)

	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.IncidentVerificationRun().Create(context.Background(), &model.IncidentVerificationRunM{
		RunID:            "verify-api-viewer-1",
		IncidentID:       incident.IncidentID,
		JobID:            strPtr(rightJobID),
		Actor:            "user:verifier",
		Source:           "human",
		StepIndex:        1,
		Tool:             "kubectl",
		Observed:         "service recovered",
		MeetsExpectation: true,
		CreatedAt:        now,
		UpdatedAt:        now,
	}))
	_, err := sessionSvc.AppendHistoryEvent(context.Background(), &sessionbiz.AppendSessionHistoryEventRequest{
		SessionID: sessionID,
		EventType: sessionbiz.SessionHistoryEventReviewStarted,
		JobID:     strPtr(rightJobID),
		Actor:     strPtr("user:reviewer"),
	})
	require.NoError(t, err)

	status, body, err := doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf(
			"%s/v1/sessions/%s/workbench/viewer?view=compare&left_job_id=%s&right_job_id=%s&history_scope=job&job_id=%s&history_limit=10",
			baseURL,
			sessionID,
			leftJobID,
			rightJobID,
			rightJobID,
		),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)

	data := extractDataContainer(body)
	require.Equal(t, sessionID, extractString(data, "session_id", "sessionID", "SessionID"))
	require.Equal(t, "compare", extractString(data, "selected", "Selected"))

	evidence := extractMap(data, "evidence", "Evidence")
	require.NotNil(t, evidence)
	require.Equal(t, fmt.Sprintf("/v1/incidents/%s/evidence", incident.IncidentID), extractString(evidence, "evidence_path", "evidencePath", "EvidencePath"))

	verification := extractMap(data, "verification", "Verification")
	require.NotNil(t, verification)
	require.Equal(
		t,
		fmt.Sprintf("/v1/incidents/%s/verification-runs", incident.IncidentID),
		extractString(verification, "verification_path", "verificationPath", "VerificationPath"),
	)

	compare := extractMap(data, "compare", "Compare")
	require.NotNil(t, compare)
	latest := extractMap(compare, "latest", "Latest")
	require.True(t, extractBool(latest, "compare_available", "compareAvailable", "CompareAvailable"))
	selectedPair := extractMap(compare, "selected_pair", "selectedPair", "SelectedPair")
	require.Equal(t, leftJobID, extractString(selectedPair, "left_job_id", "leftJobID", "LeftJobID"))
	require.Equal(t, rightJobID, extractString(selectedPair, "right_job_id", "rightJobID", "RightJobID"))

	history := extractMap(data, "history", "History")
	require.NotNil(t, history)
	require.Equal(t, "job", extractString(history, "scope", "Scope"))
	require.EqualValues(t, 1, extractInt64(history, "total_count", "totalCount", "TotalCount"))
	events := extractInboxItems(map[string]any{"items": history["events"]})
	require.Len(t, events, 1)
	require.Equal(t, rightJobID, extractString(events[0], "job_id", "jobID", "JobID"))
}

func TestOperatorTeamDashboardAPI_Get(t *testing.T) {
	baseURL, cleanup, s, client := newTestServer(t)
	defer cleanup()
	require.NoError(t, s.DB(context.Background()).AutoMigrate(&model.SessionContextM{}))

	incidentA := createAIJobLongPollTestIncident(t, s)
	incidentB := createAIJobLongPollTestIncident(t, s)
	incidentC := createAIJobLongPollTestIncident(t, s)
	incidentA.Namespace = "payments"
	incidentB.Namespace = "payments"
	incidentC.Namespace = "checkout"
	require.NoError(t, s.Incident().Update(context.Background(), incidentA))
	require.NoError(t, s.Incident().Update(context.Background(), incidentB))
	require.NoError(t, s.Incident().Update(context.Background(), incidentC))

	aiBiz := biz.NewBiz(s).AIJobV1()
	sessionSvc := biz.NewBiz(s).SessionV1()

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

	_, err := sessionSvc.UpdateReviewState(context.Background(), &sessionbiz.UpdateReviewStateRequest{
		SessionID:   sessionA,
		ReviewState: sessionbiz.SessionReviewStateInReview,
		ReviewedBy:  strPtr("user:reviewer-a"),
	})
	require.NoError(t, err)
	_, err = sessionSvc.UpdateReviewState(context.Background(), &sessionbiz.UpdateReviewStateRequest{
		SessionID:   sessionB,
		ReviewState: sessionbiz.SessionReviewStateRejected,
		ReviewedBy:  strPtr("user:reviewer-b"),
	})
	require.NoError(t, err)
	_, err = sessionSvc.UpdateReviewState(context.Background(), &sessionbiz.UpdateReviewStateRequest{
		SessionID:   sessionC,
		ReviewState: sessionbiz.SessionReviewStateConfirmed,
		ReviewedBy:  strPtr("user:reviewer-c"),
	})
	require.NoError(t, err)

	assignedAtPending := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	_, err = sessionSvc.UpdateAssignment(context.Background(), &sessionbiz.UpdateAssignmentRequest{
		SessionID:  sessionA,
		Assignee:   "user:oncall-a",
		AssignedBy: strPtr("user:lead-a"),
		AssignedAt: &assignedAtPending,
	})
	require.NoError(t, err)
	assignedAtEscalated := time.Now().UTC().Add(-5 * time.Hour).Truncate(time.Second)
	_, err = sessionSvc.UpdateAssignment(context.Background(), &sessionbiz.UpdateAssignmentRequest{
		SessionID:  sessionB,
		Assignee:   "user:oncall-b",
		AssignedBy: strPtr("user:lead-b"),
		AssignedAt: &assignedAtEscalated,
	})
	require.NoError(t, err)

	teamToken := mustLoginOperatorToken(t, client, baseURL, map[string]any{
		"operator_id": "oncall-a",
		"team_ids":    []string{"namespace:payments"},
		"scopes":      []string{"ai.read", "ai.run"},
	})
	status, body, err := doJSONRequestWithHeaders(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/operator/team_dashboard?team_id=namespace:payments&offset=0&limit=10&top_n=2&order=desc", baseURL),
		nil,
		map[string]string{"Authorization": "Bearer " + teamToken},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)

	data := extractDataContainer(body)
	require.Equal(t, "namespace:payments", extractString(data, "team_id", "teamId", "TeamID"))
	require.EqualValues(t, 2, extractInt64(data, "total_count", "totalCount", "TotalCount"))
	require.EqualValues(t, 0, extractInt64(data, "offset", "Offset"))
	require.EqualValues(t, 10, extractInt64(data, "limit", "Limit"))
	require.Equal(t, "desc", extractString(data, "sort_order", "sortOrder", "SortOrder"))

	overview := extractMap(data, "overview", "Overview")
	require.NotNil(t, overview)
	require.EqualValues(t, 2, extractInt64(overview, "total_sessions", "totalSessions", "TotalSessions"))
	require.EqualValues(t, 1, extractInt64(overview, "my_queue_count", "myQueueCount", "MyQueueCount"))
	require.EqualValues(t, 2, extractInt64(overview, "high_risk_count", "highRiskCount", "HighRiskCount"))
	require.EqualValues(t, 1, extractInt64(overview, "pending_escalation_count", "pendingEscalationCount", "PendingEscalationCount"))
	require.EqualValues(t, 1, extractInt64(overview, "escalated_count", "escalatedCount", "EscalatedCount"))

	distribution := extractMap(data, "distribution", "Distribution")
	require.NotNil(t, distribution)
	byAssignee := extractMap(distribution, "by_assignee", "byAssignee", "ByAssignee")
	require.EqualValues(t, 1, extractInt64(byAssignee, "user:oncall-a"))
	require.EqualValues(t, 1, extractInt64(byAssignee, "user:oncall-b"))

	topHighRisk := extractInboxItems(map[string]any{"items": data["top_high_risk"]})
	require.NotEmpty(t, topHighRisk)
	require.True(t, extractBool(topHighRisk[0], "high_risk", "highRisk", "HighRisk"))

	items := extractInboxItems(data)
	require.Len(t, items, 2)
	itemsBySession := map[string]map[string]any{}
	for _, item := range items {
		itemsBySession[extractString(item, "session_id", "sessionID", "SessionID")] = item
	}
	require.Contains(t, itemsBySession, sessionA)
	require.Contains(t, itemsBySession, sessionB)
	require.True(t, extractBool(itemsBySession[sessionA], "is_my_queue", "isMyQueue", "IsMyQueue"))
	require.True(t, extractBool(itemsBySession[sessionA], "long_unhandled", "longUnhandled", "LongUnhandled"))
	require.True(t, extractBool(itemsBySession[sessionA], "high_risk", "highRisk", "HighRisk"))
	require.Contains(
		t,
		extractString(itemsBySession[sessionA], "workbench_path", "workbenchPath", "WorkbenchPath"),
		"/v1/sessions/"+sessionA+"/workbench",
	)

	navigation := extractMap(data, "navigation", "Navigation")
	require.NotNil(t, navigation)
	require.Equal(t, "/v1/operator/inbox", extractString(navigation, "inbox_path", "inboxPath", "InboxPath"))
	require.Equal(
		t,
		"/v1/operator/inbox?team_id=namespace%3Apayments",
		extractString(navigation, "team_inbox_path", "teamInboxPath", "TeamInboxPath"),
	)

	pageStatus, pageBody, err := doJSONRequestWithHeaders(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/operator/team_dashboard?team_id=namespace:payments&offset=1&limit=1&order=asc", baseURL),
		nil,
		map[string]string{"Authorization": "Bearer " + teamToken},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, pageStatus)
	pageData := extractDataContainer(pageBody)
	pageItems := extractInboxItems(pageData)
	require.Len(t, pageItems, 1)

	teamInboxStatus, teamInboxBody, err := doJSONRequestWithHeaders(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/operator/inbox?team_id=namespace:payments&limit=10", baseURL),
		nil,
		map[string]string{"Authorization": "Bearer " + teamToken},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, teamInboxStatus)
	teamInboxData := extractDataContainer(teamInboxBody)
	teamInboxItems := extractInboxItems(teamInboxData)
	require.Len(t, teamInboxItems, 2)

	forbiddenInboxStatus, _, err := doJSONRequestWithHeaders(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/operator/inbox?team_id=namespace:checkout", baseURL),
		nil,
		map[string]string{"Authorization": "Bearer " + teamToken},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, forbiddenInboxStatus)

	forbiddenStatus, _, err := doJSONRequestWithHeaders(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/operator/team_dashboard?team_id=namespace:checkout", baseURL),
		nil,
		map[string]string{"Authorization": "Bearer " + teamToken},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, forbiddenStatus)
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

	status, _, err = doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/operator/inbox?escalation_state=bad", baseURL),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status)

	status, _, err = doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/operator/team_dashboard?order=bad", baseURL),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status)

	status, _, err = doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/operator/assignment_history?limit=bad", baseURL),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status)

	status, _, err = doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/operator/dashboard/trends?window=14d", baseURL),
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

func extractHistoryItems(container map[string]any) []map[string]any {
	raw := container["events"]
	if raw == nil {
		raw = container["Events"]
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

func extractInt64(container map[string]any, keys ...string) int64 {
	for _, key := range keys {
		value, ok := container[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case float64:
			return int64(v)
		case int64:
			return v
		case int:
			return int64(v)
		}
	}
	return 0
}

func extractBool(container map[string]any, keys ...string) bool {
	for _, key := range keys {
		value, ok := container[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case bool:
			return v
		case string:
			parsed, err := strconv.ParseBool(strings.TrimSpace(v))
			if err == nil {
				return parsed
			}
		}
	}
	return false
}
