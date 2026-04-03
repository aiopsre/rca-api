package handler

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/aiopsre/rca-api/internal/apiserver/biz"
	sessionbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/session"
	"github.com/aiopsre/rca-api/internal/apiserver/model"
)

func TestOperatorSLAEscalationSyncAPI_AndReplayClearsEscalation(t *testing.T) {
	baseURL, cleanup, s, client := newTestServer(t)
	defer cleanup()
	require.NoError(t, s.DB(context.Background()).AutoMigrate(&model.SessionContextM{}))

	incident := createAIJobLongPollTestIncident(t, s)
	aiBiz := biz.NewBiz(s).AIJobV1()
	sessionSvc := biz.NewBiz(s).SessionV1()

	jobID := createFailedTraceJob(t, aiBiz, incident.IncidentID, "manual", "manual_api", "user:sla", "sla escalation test")
	sessionID := mustHandlerSessionIDByJob(t, s, jobID)

	oldAssignedAt := time.Now().UTC().Add(-5 * time.Hour).Truncate(time.Second)
	_, err := sessionSvc.UpdateAssignment(context.Background(), &sessionbiz.UpdateAssignmentRequest{
		SessionID:  sessionID,
		Assignee:   "user:oncall-a",
		AssignedBy: strPtr("user:lead-a"),
		AssignedAt: &oldAssignedAt,
	})
	require.NoError(t, err)

	status, body, err := doJSONRequest(
		client,
		http.MethodPost,
		fmt.Sprintf("%s/v1/operator/sla/escalation-sync?offset=0&limit=100", baseURL),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)

	data := extractDataContainer(body)
	require.Equal(t, true, extractBool(data, "accepted", "Accepted"))
	require.Equal(t, false, extractBool(data, "async", "Async"))
	require.GreaterOrEqual(t, extractInt64(data, "scanned_count", "scannedCount", "ScannedCount"), int64(1))
	require.GreaterOrEqual(t, extractInt64(data, "updated_count", "updatedCount", "UpdatedCount"), int64(1))
	require.GreaterOrEqual(t, extractInt64(data, "escalated_count", "escalatedCount", "EscalatedCount"), int64(1))

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
	require.Equal(t, "escalated", extractString(sessionObj, "escalation_state", "escalationState", "EscalationState"))

	replayStatus, _, err := doJSONRequest(
		client,
		http.MethodPost,
		fmt.Sprintf("%s/v1/sessions/%s/actions/replay", baseURL, sessionID),
		[]byte(`{"reason":"sla follow-up","operator_note":"reset escalation by replay"}`),
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, replayStatus)

	updatedWorkbenchStatus, updatedWorkbenchBody, err := doJSONRequest(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/sessions/%s/workbench?limit=10", baseURL, sessionID),
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, updatedWorkbenchStatus)
	updatedWorkbenchData := extractDataContainer(updatedWorkbenchBody)
	updatedSessionObj := extractMap(updatedWorkbenchData, "session", "Session")
	require.Equal(t, "none", extractString(updatedSessionObj, "escalation_state", "escalationState", "EscalationState"))
}
