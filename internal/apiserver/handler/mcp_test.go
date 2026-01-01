package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/apiserver/biz"
	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/validation"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

func TestMCPListTools(t *testing.T) {
	baseURL, cleanup, _, client := newMCPTestServer(t)
	defer cleanup()

	status, body, err := doScopedJSONRequest(client, http.MethodGet, baseURL+"/v1/mcp/tools", nil, "")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload))
	rawTools, ok := payload["tools"].([]any)
	require.True(t, ok)
	require.Len(t, rawTools, 6)

	names := make(map[string]struct{}, len(rawTools))
	for _, item := range rawTools {
		obj, ok := item.(map[string]any)
		require.True(t, ok)
		name, _ := obj["name"].(string)
		names[name] = struct{}{}
	}
	require.Contains(t, names, "get_incident")
	require.Contains(t, names, "list_alert_events_current")
	require.Contains(t, names, "get_evidence")
	require.Contains(t, names, "list_incident_evidence")
	require.Contains(t, names, "query_metrics")
	require.Contains(t, names, "query_logs")
}

func TestMCPCallGetIncident_AllowAndAudit(t *testing.T) {
	baseURL, cleanup, s, client := newMCPTestServer(t)
	defer cleanup()

	incident := createAIJobLongPollTestIncident(t, s)

	body := map[string]any{
		"tool": "get_incident",
		"input": map[string]any{
			"incident_id": incident.IncidentID,
		},
	}
	rawBody, err := json.Marshal(body)
	require.NoError(t, err)

	status, respBody, err := doScopedJSONRequest(client, http.MethodPost, baseURL+"/v1/mcp/tools/call", rawBody, "incident.read")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(respBody, &resp))
	require.Equal(t, "get_incident", asString(resp["tool"]))
	output, ok := resp["output"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, incident.IncidentID, asString(output["incidentID"]))

	toolCallID := asString(resp["tool_call_id"])
	require.NotEmpty(t, toolCallID)

	stored, err := s.AIToolCall().Get(context.Background(), where.T(context.Background()).F("tool_call_id", toolCallID))
	require.NoError(t, err)
	require.Equal(t, mcpToolCallAuditJobID, stored.JobID)
	require.Equal(t, mcpToolCallToolPrefix+"get_incident", stored.ToolName)
	require.Equal(t, "ok", stored.Status)
	require.NotEmpty(t, stored.RequestJSON)
}

func TestMCPCallScopeDenied_FixedError(t *testing.T) {
	baseURL, cleanup, s, client := newMCPTestServer(t)
	defer cleanup()

	incident := createAIJobLongPollTestIncident(t, s)

	body := map[string]any{
		"tool": "get_incident",
		"input": map[string]any{
			"incident_id": incident.IncidentID,
		},
	}
	rawBody, err := json.Marshal(body)
	require.NoError(t, err)

	status, respBody, err := doScopedJSONRequest(client, http.MethodPost, baseURL+"/v1/mcp/tools/call", rawBody, "evidence.read")
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, status)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(respBody, &payload))
	errorObj, ok := payload["error"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "SCOPE_DENIED", asString(errorObj["code"]))

	details, ok := errorObj["details"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, mcpCallStep, asString(details["step"]))
	require.Equal(t, "get_incident", asString(details["tool"]))

	total, list, err := s.AIToolCall().List(context.Background(), where.T(context.Background()).P(0, 20).F("job_id", mcpToolCallAuditJobID))
	require.NoError(t, err)
	require.GreaterOrEqual(t, total, int64(1))
	require.NotEmpty(t, list)
	require.Equal(t, mcpToolCallToolPrefix+"get_incident", list[len(list)-1].ToolName)
}

func TestMCPCallQueryLogs_GuardrailInvalidArgument(t *testing.T) {
	baseURL, cleanup, s, client := newMCPTestServer(t)
	defer cleanup()

	ds := &model.DatasourceM{
		Type:      "loki",
		Name:      "mcp-logs-ds",
		BaseURL:   "http://127.0.0.1:9090",
		AuthType:  "none",
		TimeoutMs: 1000,
		IsEnabled: true,
	}
	require.NoError(t, s.Datasource().Create(context.Background(), ds))
	require.NotEmpty(t, ds.DatasourceID)

	now := time.Now().UTC().Unix()
	body := map[string]any{
		"tool": "query_logs",
		"input": map[string]any{
			"datasource_id":    ds.DatasourceID,
			"query":            "{app=\"demo\"}",
			"time_range_start": map[string]any{"seconds": now - 300, "nanos": 0},
			"time_range_end":   map[string]any{"seconds": now, "nanos": 0},
			"limit":            999,
		},
	}
	rawBody, err := json.Marshal(body)
	require.NoError(t, err)

	status, respBody, err := doScopedJSONRequest(client, http.MethodPost, baseURL+"/v1/mcp/tools/call", rawBody, "evidence.query")
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(respBody, &payload))
	errorObj, ok := payload["error"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "INVALID_ARGUMENT", asString(errorObj["code"]))
}

func TestMCPCallListAlertEventsCurrent_ResponseTruncated(t *testing.T) {
	baseURL, cleanup, s, client := newMCPTestServer(t)
	defer cleanup()

	now := time.Now().UTC()
	for i := 0; i < 180; i++ {
		service := fmt.Sprintf("svc-%03d-%s", i, strings.Repeat("x", 96))
		fingerprint := fmt.Sprintf("mcp-trunc-%03d", i)
		currentKey := fingerprint

		row := &model.AlertEventM{
			IncidentID:  nil,
			Fingerprint: fingerprint,
			DedupKey:    fingerprint,
			Source:      "alertmanager",
			Status:      "firing",
			Severity:    "warning",
			Service:     &service,
			Namespace:   ptrStringValue("default"),
			Cluster:     ptrStringValue("prod"),
			Workload:    ptrStringValue("demo"),
			LastSeenAt:  now.Add(-time.Duration(i) * time.Second),
			IsCurrent:   true,
			CurrentKey:  &currentKey,
		}
		require.NoError(t, s.AlertEvent().Create(context.Background(), row))
	}

	body := map[string]any{
		"tool": "list_alert_events_current",
		"input": map[string]any{
			"limit": 200,
			"page":  1,
		},
	}
	rawBody, err := json.Marshal(body)
	require.NoError(t, err)

	status, respBody, err := doScopedJSONRequest(client, http.MethodPost, baseURL+"/v1/mcp/tools/call", rawBody, "alert.read")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.LessOrEqual(t, len(respBody), mcpMaxResponseBytes)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(respBody, &payload))
	require.Equal(t, true, payload["truncated"])

	warnings, ok := payload["warnings"].([]any)
	require.True(t, ok)
	found := false
	for _, item := range warnings {
		if asString(item) == mcpWarningTruncated {
			found = true
			break
		}
	}
	require.True(t, found)
}

func TestMCPCallAudit_RequestAndResponseAreSanitizedAndClamped(t *testing.T) {
	baseURL, cleanup, s, client := newMCPTestServer(t)
	defer cleanup()

	now := time.Now().UTC()
	for i := 0; i < 180; i++ {
		service := fmt.Sprintf("svc-%03d-%s", i, strings.Repeat("x", 96))
		fingerprint := fmt.Sprintf("mcp-audit-%03d", i)
		currentKey := fingerprint

		row := &model.AlertEventM{
			IncidentID:  nil,
			Fingerprint: fingerprint,
			DedupKey:    fingerprint,
			Source:      "alertmanager",
			Status:      "firing",
			Severity:    "warning",
			Service:     &service,
			Namespace:   ptrStringValue("default"),
			Cluster:     ptrStringValue("prod"),
			Workload:    ptrStringValue("demo"),
			LastSeenAt:  now.Add(-time.Duration(i) * time.Second),
			IsCurrent:   true,
			CurrentKey:  &currentKey,
		}
		require.NoError(t, s.AlertEvent().Create(context.Background(), row))
	}

	body := map[string]any{
		"tool": "list_alert_events_current",
		"input": map[string]any{
			"limit":        200,
			"page":         1,
			"access_token": strings.Repeat("s", 64),
			"headers": map[string]any{
				"Authorization": "Bearer super-secret-token",
				"x-trace-id":    "trace-demo",
			},
		},
	}
	rawBody, err := json.Marshal(body)
	require.NoError(t, err)

	status, respBody, err := doScopedJSONRequest(client, http.MethodPost, baseURL+"/v1/mcp/tools/call", rawBody, "alert.read")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(respBody, &payload))
	toolCallID := asString(payload["tool_call_id"])
	require.NotEmpty(t, toolCallID)

	stored, err := s.AIToolCall().Get(context.Background(), where.T(context.Background()).F("tool_call_id", toolCallID))
	require.NoError(t, err)
	require.LessOrEqual(t, len(stored.RequestJSON), mcpMaxAuditInputBytes)

	require.NotNil(t, stored.ResponseJSON)
	require.LessOrEqual(t, len(strings.TrimSpace(*stored.ResponseJSON)), mcpMaxAuditOutputBytes)

	lowerRequest := strings.ToLower(stored.RequestJSON)
	require.NotContains(t, lowerRequest, "access_token")
	require.NotContains(t, lowerRequest, "authorization")
	require.NotContains(t, lowerRequest, "super-secret-token")
}

func newMCPTestServer(t *testing.T) (baseURL string, cleanup func(), s store.IStore, client *http.Client) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	store.ResetForTest()
	db := newMCPTestDB(t)
	s = store.NewStore(db)
	val := validation.New(s)
	h := NewHandler(biz.NewBiz(s), val)

	engine := gin.New()
	h.ApplyTo(engine.Group("/v1"))

	server := httptest.NewServer(engine)
	cleanup = func() {
		server.Close()
		store.ResetForTest()
	}
	return server.URL, cleanup, s, server.Client()
}

func newMCPTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newAIJobLongPollTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.AlertEventM{}, &model.DatasourceM{}, &model.EvidenceM{}))
	return db
}

func doScopedJSONRequest(client *http.Client, method string, url string, payload []byte, scopes string) (int, []byte, error) {
	var reqBody io.Reader
	if payload != nil {
		reqBody = bytes.NewReader(payload)
	}

	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	if strings.TrimSpace(scopes) != "" {
		req.Header.Set("X-Scopes", strings.TrimSpace(scopes))
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

func asString(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", typed))
	}
}

func ptrStringValue(value string) *string {
	v := value
	return &v
}
