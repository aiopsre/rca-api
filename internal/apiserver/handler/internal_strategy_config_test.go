package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
)

func TestInternalStrategyConfigAPI_DynamicUpdateAndPermission(t *testing.T) {
	baseURL, cleanup, s, client := newTestServer(t)
	defer cleanup()
	require.NoError(t, s.DB(context.Background()).AutoMigrate(
		&model.SessionContextM{},
		&model.SessionHistoryEventM{},
		&model.PipelineConfigM{},
		&model.TriggerConfigM{},
		&model.ToolsetConfigDynamicM{},
		&model.SLAEscalationConfigM{},
		&model.SessionAssignmentM{},
	))

	readToken := loginOperatorForTest(t, client, baseURL, map[string]any{
		"operator_id": "operator:reader",
	})

	// Read is allowed with ai.read.
	readStatus, _, err := doJSONRequestWithToken(client, http.MethodGet, baseURL+"/v1/config/trigger/manual", nil, readToken)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, readStatus)

	// Update is forbidden without config.admin.
	forbiddenStatus, _, err := doJSONRequestWithToken(
		client,
		http.MethodPost,
		baseURL+"/v1/config/trigger/update",
		[]byte(`{"trigger_type":"manual","pipeline_id":"advanced_rca","session_type":"incident","fallback":false}`),
		readToken,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, forbiddenStatus)

	adminToken := loginOperatorForTest(t, client, baseURL, map[string]any{
		"operator_id": "operator:admin",
		"scopes":      []string{"config.admin", "ai.read", "ai.run"},
	})

	updateStatus, updateBody, err := doJSONRequestWithToken(
		client,
		http.MethodPost,
		baseURL+"/v1/config/trigger/update",
		[]byte(`{"trigger_type":"manual","pipeline_id":"advanced_rca","session_type":"incident","fallback":false}`),
		adminToken,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, updateStatus)
	updateData := extractDataContainer(updateBody)
	require.Equal(t, "advanced_rca", extractString(updateData, "pipeline_id", "pipelineID", "PipelineID"))
	require.Equal(t, "dynamic_db", extractString(updateData, "source", "Source"))

	getStatus, getBody, err := doJSONRequestWithToken(client, http.MethodGet, baseURL+"/v1/config/trigger/manual", nil, readToken)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, getStatus)
	getData := extractDataContainer(getBody)
	require.Equal(t, "advanced_rca", extractString(getData, "pipeline_id", "pipelineID", "PipelineID"))
	require.Equal(t, "dynamic_db", extractString(getData, "source", "Source"))
}

func TestInternalStrategyConfigAPI_SessionAssign(t *testing.T) {
	baseURL, cleanup, s, client := newTestServer(t)
	defer cleanup()
	require.NoError(t, s.DB(context.Background()).AutoMigrate(
		&model.SessionContextM{},
		&model.SessionHistoryEventM{},
		&model.SessionAssignmentM{},
		&model.TriggerConfigM{},
		&model.ToolsetConfigDynamicM{},
		&model.PipelineConfigM{},
		&model.SLAEscalationConfigM{},
	))

	session := &model.SessionContextM{
		SessionID:   "session-config-assign-1",
		SessionType: "incident",
		BusinessKey: "incident-config-assign-1",
		Status:      "active",
	}
	require.NoError(t, s.SessionContext().Create(context.Background(), session))
	sessionID := session.SessionID

	token := loginOperatorForTest(t, client, baseURL, map[string]any{
		"operator_id": "operator:oncall",
		"team_ids":    []string{"*"},
	})

	assignedAt := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	assignStatus, assignBody, err := doJSONRequestWithToken(
		client,
		http.MethodPost,
		fmt.Sprintf("%s/v1/session/%s/assign", baseURL, sessionID),
		[]byte(fmt.Sprintf(`{"assignee":"user:oncall-a","note":"config assign","assigned_at":"%s"}`, assignedAt)),
		token,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, assignStatus)
	assignData := extractDataContainer(assignBody)
	require.Equal(t, "user:oncall-a", extractString(assignData, "assignee", "Assignee"))

	getStatus, getBody, err := doJSONRequestWithToken(
		client,
		http.MethodGet,
		fmt.Sprintf("%s/v1/session/%s/assignment", baseURL, sessionID),
		nil,
		token,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, getStatus)
	getData := extractDataContainer(getBody)
	require.Equal(t, "user:oncall-a", extractString(getData, "assignee", "Assignee"))
	require.Equal(t, "dynamic_db", extractString(getData, "source", "Source"))
}

func loginOperatorForTest(t *testing.T, client *http.Client, baseURL string, payload map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	status, body, err := doJSONRequest(client, http.MethodPost, baseURL+"/v1/auth/login", raw)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	data := extractDataContainer(body)
	token := extractString(data, "token", "Token")
	require.NotEmpty(t, token)
	return token
}

func doJSONRequestWithToken(
	client *http.Client,
	method string,
	url string,
	payload []byte,
	token string,
) (int, []byte, error) {
	var bodyReader *bytes.Reader
	if payload == nil {
		bodyReader = bytes.NewReader(nil)
	} else {
		bodyReader = bytes.NewReader(payload)
	}
	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, body, nil
}
