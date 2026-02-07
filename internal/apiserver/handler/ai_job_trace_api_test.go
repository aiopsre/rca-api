package handler

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aiopsre/rca-api/internal/apiserver/biz"
	aijobbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/ai_job"
	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/pkg/contextx"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
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
