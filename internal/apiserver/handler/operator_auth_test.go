package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aiopsre/rca-api/internal/apiserver/biz"
	sessionbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/session"
	"github.com/aiopsre/rca-api/internal/apiserver/model"
)

func TestOperatorAuth_LoginAndTokenGuard(t *testing.T) {
	baseURL, cleanup, s, client := newTestServer(t)
	defer cleanup()
	require.NoError(t, s.DB(context.Background()).AutoMigrate(&model.SessionContextM{}, &model.SessionHistoryEventM{}))

	token := mustLoginOperatorToken(t, client, baseURL, map[string]any{
		"operator_id": "operator:test-a",
		"team_ids":    []string{"default"},
		"scopes":      []string{"ai.read", "ai.run"},
	})
	require.NotEmpty(t, token)

	status, _, err := doJSONRequestWithHeaders(client, http.MethodGet, fmt.Sprintf("%s/v1/operator/inbox", baseURL), nil, map[string]string{
		"Authorization": "Bearer " + token,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)

	status, _, err = doJSONRequestWithHeaders(client, http.MethodGet, fmt.Sprintf("%s/v1/operator/inbox", baseURL), nil, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, status)
}

func TestOperatorAuth_SessionAccessControlByTeamOrSelf(t *testing.T) {
	baseURL, cleanup, s, client := newTestServer(t)
	defer cleanup()

	require.NoError(t, s.DB(context.Background()).AutoMigrate(&model.SessionContextM{}, &model.SessionHistoryEventM{}))
	aiBiz := biz.NewBiz(s).AIJobV1()
	sessionBiz := biz.NewBiz(s).SessionV1()

	incidentA := createAIJobLongPollTestIncident(t, s)
	incidentB := createAIJobLongPollTestIncident(t, s)
	incidentA.Namespace = "payments"
	require.NoError(t, s.Incident().Update(context.Background(), incidentA))
	incidentB.Namespace = "checkout"
	require.NoError(t, s.Incident().Update(context.Background(), incidentB))

	jobA := createFinalizedTraceJob(t, aiBiz, incidentA.IncidentID, "manual", "manual_api", "user:a", buildDiagnosisJSON(
		"payments timeout",
		"dependency_timeout",
		"dependency",
		0.71,
		"ev-a-1",
		"ev-a-2",
	))
	jobB := createFinalizedTraceJob(t, aiBiz, incidentB.IncidentID, "manual", "manual_api", "user:b", buildDiagnosisJSON(
		"checkout memory pressure",
		"resource_pressure",
		"app",
		0.66,
		"ev-b-1",
		"ev-b-2",
	))
	sessionA := mustHandlerSessionIDByJob(t, s, jobA)
	sessionB := mustHandlerSessionIDByJob(t, s, jobB)

	_, err := sessionBiz.UpdateAssignment(context.Background(), &sessionbiz.UpdateAssignmentRequest{
		SessionID:  sessionB,
		Assignee:   "user:oncall-b",
		AssignedBy: strPtr("user:lead"),
	})
	require.NoError(t, err)

	teamToken := mustLoginOperatorToken(t, client, baseURL, map[string]any{
		"operator_id": "operator:team-payments",
		"team_ids":    []string{"namespace:payments"},
		"scopes":      []string{"ai.read", "ai.run"},
	})
	status, _, err := doJSONRequestWithHeaders(client, http.MethodGet, fmt.Sprintf("%s/v1/sessions/%s/workbench", baseURL, sessionA), nil, map[string]string{
		"Authorization": "Bearer " + teamToken,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)

	status, _, err = doJSONRequestWithHeaders(client, http.MethodGet, fmt.Sprintf("%s/v1/sessions/%s/workbench", baseURL, sessionB), nil, map[string]string{
		"Authorization": "Bearer " + teamToken,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, status)

	selfToken := mustLoginOperatorToken(t, client, baseURL, map[string]any{
		"operator_id": "oncall-b",
		"scopes":      []string{"ai.read", "ai.run"},
	})
	status, _, err = doJSONRequestWithHeaders(client, http.MethodGet, fmt.Sprintf("%s/v1/sessions/%s/workbench", baseURL, sessionB), nil, map[string]string{
		"Authorization": "Bearer " + selfToken,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
}

func mustLoginOperatorToken(t *testing.T, client *http.Client, baseURL string, body map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(body)
	require.NoError(t, err)
	status, respBody, err := doJSONRequestWithHeaders(client, http.MethodPost, fmt.Sprintf("%s/v1/auth/login", baseURL), payload, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	data := extractDataContainer(respBody)
	token := extractString(data, "token", "Token")
	require.NotEmpty(t, token)
	return token
}

func doJSONRequestWithHeaders(
	client *http.Client,
	method string,
	url string,
	payload []byte,
	headers map[string]string,
) (status int, body []byte, err error) {
	var reqBody io.Reader
	if payload != nil {
		reqBody = bytes.NewReader(payload)
	}
	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, data, nil
}
