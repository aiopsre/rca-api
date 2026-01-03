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
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/policy"
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
	require.Equal(t, mcpToolRegistryVersion, asString(payload["version"]))
	rawTools, ok := payload["tools"].([]any)
	require.True(t, ok)
	require.Len(t, rawTools, 22)

	names := make(map[string]struct{}, len(rawTools))
	for _, item := range rawTools {
		obj, ok := item.(map[string]any)
		require.True(t, ok)
		name, _ := obj["name"].(string)
		names[name] = struct{}{}
	}
	require.Contains(t, names, "search_incidents")
	require.Contains(t, names, "list_incidents")
	require.Contains(t, names, "get_incident")
	require.Contains(t, names, "get_incident_timeline")
	require.Contains(t, names, "list_alert_events_current")
	require.Contains(t, names, "list_alert_events_history")
	require.Contains(t, names, "list_datasources")
	require.Contains(t, names, "get_datasource")
	require.Contains(t, names, "get_evidence")
	require.Contains(t, names, "list_incident_evidence")
	require.Contains(t, names, "search_evidence")
	require.Contains(t, names, "query_metrics")
	require.Contains(t, names, "query_logs")
	require.Contains(t, names, "get_ai_job")
	require.Contains(t, names, "list_ai_jobs")
	require.Contains(t, names, "list_tool_calls")
	require.Contains(t, names, "search_tool_calls")
	require.Contains(t, names, "list_silences")
	require.Contains(t, names, "list_notice_deliveries")
	require.Contains(t, names, "get_notice_deliveries_by_incident")
	require.Contains(t, names, "list_notice_deliveries_by_time")
	require.Contains(t, names, "get_notice_delivery")
}

func TestMCPListTools_ContainsPolicyMetadata(t *testing.T) {
	baseURL, cleanup, _, client := newMCPTestServerWithPolicy(t, policy.MCPPolicyConfig{
		Tools: map[string]policy.MCPToolPolicy{
			"search_incidents": {
				Enabled:   ptrBoolValue(false),
				RiskLevel: "readonly",
				Limits: policy.MCPToolPolicyLimits{
					MaxLimit:            ptrInt64Value(5),
					MaxTimeRangeSeconds: ptrInt64Value(300),
					MaxResponseBytes:    ptrIntValue(4096),
				},
			},
		},
	})
	defer cleanup()

	status, body, err := doScopedJSONRequest(client, http.MethodGet, baseURL+"/v1/mcp/tools", nil, "")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload))
	tools, ok := payload["tools"].([]any)
	require.True(t, ok)

	found := false
	for _, item := range tools {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if asString(obj["name"]) != "search_incidents" {
			continue
		}
		metadata, ok := obj["metadata"].(map[string]any)
		require.True(t, ok)
		policyObj, ok := metadata["policy"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, false, policyObj["enabled"])
		require.Equal(t, "readonly", asString(policyObj["risk_level"]))
		isolationObj, ok := metadata["isolation"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "filter", asString(isolationObj["mode"]))
		limits, ok := policyObj["limits"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "5", asString(limits["max_limit"]))
		require.Equal(t, "300", asString(limits["max_time_range_seconds"]))
		require.Equal(t, "4096", asString(limits["max_response_bytes"]))
		found = true
		break
	}
	require.True(t, found)
}

func TestMCPCallPolicyDisabled_ReturnsScopeDenied(t *testing.T) {
	baseURL, cleanup, _, client := newMCPTestServerWithPolicy(t, policy.MCPPolicyConfig{
		Tools: map[string]policy.MCPToolPolicy{
			"search_incidents": {
				Enabled: ptrBoolValue(false),
			},
		},
	})
	defer cleanup()

	rawBody, err := json.Marshal(map[string]any{
		"tool": "search_incidents",
		"input": map[string]any{
			"limit": 1,
		},
	})
	require.NoError(t, err)

	status, respBody, err := doScopedJSONRequest(client, http.MethodPost, baseURL+"/v1/mcp/tools/call", rawBody, "incident.read")
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, status)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(respBody, &payload))
	errorObj, ok := payload["error"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, string(MCPErrorCodeScopeDenied), asString(errorObj["code"]))
}

func TestMCPCallPolicyLimitExceeded_ReturnsInvalidArgument(t *testing.T) {
	baseURL, cleanup, _, client := newMCPTestServerWithPolicy(t, policy.MCPPolicyConfig{
		Tools: map[string]policy.MCPToolPolicy{
			"search_incidents": {
				Limits: policy.MCPToolPolicyLimits{
					MaxLimit: ptrInt64Value(5),
				},
			},
		},
	})
	defer cleanup()

	rawBody, err := json.Marshal(map[string]any{
		"tool": "search_incidents",
		"input": map[string]any{
			"limit": 50,
			"page":  1,
		},
	})
	require.NoError(t, err)

	status, respBody, err := doScopedJSONRequest(client, http.MethodPost, baseURL+"/v1/mcp/tools/call", rawBody, "incident.read")
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(respBody, &payload))
	errorObj, ok := payload["error"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, string(MCPErrorCodeInvalidArg), asString(errorObj["code"]))
}

func TestMCPCallIsolation_ListFilteredAndGetDenied(t *testing.T) {
	baseURL, cleanup, s, client := newMCPTestServer(t)
	defer cleanup()

	incidentAllowed := createAIJobLongPollTestIncident(t, s)
	incidentAllowed.Namespace = "ns-allowed"
	incidentAllowed.Service = "svc-allowed"
	require.NoError(t, s.Incident().Update(context.Background(), incidentAllowed))

	incidentDenied := createAIJobLongPollTestIncident(t, s)
	incidentDenied.Namespace = "ns-denied"
	incidentDenied.Service = "svc-denied"
	require.NoError(t, s.Incident().Update(context.Background(), incidentDenied))

	listBody, err := json.Marshal(map[string]any{
		"tool": "list_incidents",
		"input": map[string]any{
			"limit": 20,
			"page":  1,
		},
	})
	require.NoError(t, err)

	listHeaders := map[string]string{
		mcpHeaderAllowedNamespaces: "ns-allowed",
		mcpHeaderAllowedServices:   "svc-allowed",
	}
	status, respBody, err := doScopedJSONRequestWithHeaders(
		client,
		http.MethodPost,
		baseURL+"/v1/mcp/tools/call",
		listBody,
		"incident.read",
		listHeaders,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)

	var listPayload map[string]any
	require.NoError(t, json.Unmarshal(respBody, &listPayload))
	output, ok := listPayload["output"].(map[string]any)
	require.True(t, ok)
	incidents, ok := output["incidents"].([]any)
	require.True(t, ok)
	require.Len(t, incidents, 1)
	item, ok := incidents[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "ns-allowed", asString(item["namespace"]))
	require.Equal(t, "svc-allowed", asString(item["service"]))

	getBody, err := json.Marshal(map[string]any{
		"tool": "get_incident",
		"input": map[string]any{
			"incident_id": incidentDenied.IncidentID,
		},
	})
	require.NoError(t, err)

	status, respBody, err = doScopedJSONRequestWithHeaders(
		client,
		http.MethodPost,
		baseURL+"/v1/mcp/tools/call",
		getBody,
		"incident.read",
		listHeaders,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, status)

	var getPayload map[string]any
	require.NoError(t, json.Unmarshal(respBody, &getPayload))
	errorObj, ok := getPayload["error"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, string(MCPErrorCodeScopeDenied), asString(errorObj["code"]))
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
	require.Equal(t, string(MCPErrorCodeScopeDenied), asString(errorObj["code"]))

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
	require.Equal(t, string(MCPErrorCodeInvalidArg), asString(errorObj["code"]))
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

func TestMCPCallGetDatasource_MetadataOnly(t *testing.T) {
	baseURL, cleanup, s, client := newMCPTestServer(t)
	defer cleanup()

	ds := &model.DatasourceM{
		Type:               "prometheus",
		Name:               "mcp-ds-meta",
		BaseURL:            "http://127.0.0.1:19090",
		AuthType:           "bearer",
		AuthSecretRef:      ptrStringValue("vault://prod/metrics"),
		TimeoutMs:          3000,
		IsEnabled:          true,
		DefaultHeadersJSON: ptrStringValue(`{"Authorization":"Bearer top-secret-token","X-Test":"demo"}`),
		TLSConfigJSON:      ptrStringValue(`{"insecure_skip_verify":true}`),
	}
	require.NoError(t, s.Datasource().Create(context.Background(), ds))

	rawBody, err := json.Marshal(map[string]any{
		"tool": "get_datasource",
		"input": map[string]any{
			"datasource_id": ds.DatasourceID,
		},
	})
	require.NoError(t, err)

	status, respBody, err := doScopedJSONRequest(client, http.MethodPost, baseURL+"/v1/mcp/tools/call", rawBody, "datasource.read")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	assertNoSensitiveLeak(t, respBody)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(respBody, &payload))
	output, ok := payload["output"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, ds.DatasourceID, asString(output["datasourceID"]))
	require.NotContains(t, output, "authSecretRef")
	require.NotContains(t, output, "defaultHeadersJSON")
	require.NotContains(t, output, "tlsConfigJSON")
}

func TestMCPCallListToolCalls_SanitizedAndClamped(t *testing.T) {
	baseURL, cleanup, s, client := newMCPTestServer(t)
	defer cleanup()

	jobID := "job-mcp-list-tool-calls"
	huge := strings.Repeat("x", mcpMaxAuditInputBytes+1024)
	requestJSON := fmt.Sprintf(`{"headers":{"Authorization":"Bearer super-secret-token"},"token":"abc","payload":"%s"}`, huge)
	responseJSON := fmt.Sprintf(`{"secret":"sensitive","content":"%s"}`, huge)
	errorMessage := strings.Repeat("authorization token leaked; ", 300)

	row := &model.AIToolCallM{
		ToolCallID:        fmt.Sprintf("seed-tool-call-%d", time.Now().UTC().UnixNano()),
		JobID:             jobID,
		Seq:               1,
		NodeName:          "node-test",
		ToolName:          "mcp.query_logs",
		RequestJSON:       requestJSON,
		ResponseJSON:      &responseJSON,
		ResponseSizeBytes: int64(len(responseJSON)),
		Status:            "error",
		LatencyMs:         7,
		ErrorMessage:      &errorMessage,
	}
	err := s.AIToolCall().Create(context.Background(), row)
	require.NoError(t, err)
	toolCallID := row.ToolCallID

	rawBody, err := json.Marshal(map[string]any{
		"tool": "list_tool_calls",
		"input": map[string]any{
			"job_id": jobID,
			"limit":  20,
		},
	})
	require.NoError(t, err)

	status, respBody, err := doScopedJSONRequest(client, http.MethodPost, baseURL+"/v1/mcp/tools/call", rawBody, "toolcall.read")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	assertNoSensitiveLeak(t, respBody)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(respBody, &payload))
	output, ok := payload["output"].(map[string]any)
	require.True(t, ok)
	toolCalls, ok := output["toolCalls"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, toolCalls)
	var matched map[string]any
	for _, item := range toolCalls {
		call, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if asString(call["toolCallID"]) == toolCallID {
			matched = call
			break
		}
	}
	require.NotNil(t, matched)
	require.Equal(t, toolCallID, asString(matched["toolCallID"]))

	inputPayload, ok := matched["input"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, inputPayload["truncated"])
	responsePayload, ok := matched["output"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, responsePayload["truncated"])
	require.Equal(t, "[REDACTED]", asString(matched["error"]))
}

func TestMCPCallListToolCalls_ScopeDenied(t *testing.T) {
	baseURL, cleanup, _, client := newMCPTestServer(t)
	defer cleanup()

	rawBody, err := json.Marshal(map[string]any{
		"tool": "list_tool_calls",
		"input": map[string]any{
			"job_id": "job-for-scope-denied",
		},
	})
	require.NoError(t, err)

	status, respBody, err := doScopedJSONRequest(client, http.MethodPost, baseURL+"/v1/mcp/tools/call", rawBody, "incident.read")
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, status)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(respBody, &payload))
	errorObj, ok := payload["error"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, string(MCPErrorCodeScopeDenied), asString(errorObj["code"]))
}

func TestMCPCallSearchToolCalls_SanitizedAndClamped(t *testing.T) {
	baseURL, cleanup, s, client := newMCPTestServer(t)
	defer cleanup()

	huge := strings.Repeat("x", mcpMaxAuditInputBytes+1024)
	requestJSON := fmt.Sprintf(`{"incident_id":"incident-search","request_id":"req-search","headers":{"Authorization":"Bearer super-secret-token"},"token":"abc","payload":"%s"}`, huge)
	responseJSON := fmt.Sprintf(`{"secret":"sensitive","content":"%s"}`, huge)
	errorMessage := strings.Repeat("authorization token leaked; ", 300)

	row := &model.AIToolCallM{
		ToolCallID:        fmt.Sprintf("seed-search-tool-call-%d", time.Now().UTC().UnixNano()),
		JobID:             mcpToolCallAuditJobID,
		Seq:               5,
		NodeName:          "node-test",
		ToolName:          "mcp.search_incidents",
		RequestJSON:       requestJSON,
		ResponseJSON:      &responseJSON,
		ResponseSizeBytes: int64(len(responseJSON)),
		Status:            "error",
		LatencyMs:         11,
		ErrorMessage:      &errorMessage,
	}
	require.NoError(t, s.AIToolCall().Create(context.Background(), row))

	rawBody, err := json.Marshal(map[string]any{
		"tool": "search_tool_calls",
		"input": map[string]any{
			"tool_prefix": "mcp.",
			"incident_id": "incident-search",
			"request_id":  "req-search",
			"limit":       20,
			"page":        1,
		},
	})
	require.NoError(t, err)

	status, respBody, err := doScopedJSONRequest(client, http.MethodPost, baseURL+"/v1/mcp/tools/call", rawBody, "toolcall.read")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	assertNoSensitiveLeak(t, respBody)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(respBody, &payload))
	output, ok := payload["output"].(map[string]any)
	require.True(t, ok)
	toolCalls, ok := output["toolCalls"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, toolCalls)

	var matched map[string]any
	for _, item := range toolCalls {
		call, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if asString(call["toolName"]) == "mcp.search_incidents" {
			matched = call
			break
		}
	}
	require.NotNil(t, matched)

	inputPayload, ok := matched["input"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, inputPayload["truncated"])
	responsePayload, ok := matched["output"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, responsePayload["truncated"])
	require.Equal(t, "[REDACTED]", asString(matched["error"]))
}

func TestMCPCallSearchToolCalls_ScopeDenied(t *testing.T) {
	baseURL, cleanup, _, client := newMCPTestServer(t)
	defer cleanup()

	rawBody, err := json.Marshal(map[string]any{
		"tool": "search_tool_calls",
		"input": map[string]any{
			"tool_prefix": "mcp.",
			"limit":       20,
			"page":        1,
		},
	})
	require.NoError(t, err)

	status, respBody, err := doScopedJSONRequest(client, http.MethodPost, baseURL+"/v1/mcp/tools/call", rawBody, "incident.read")
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, status)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(respBody, &payload))
	errorObj, ok := payload["error"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, string(MCPErrorCodeScopeDenied), asString(errorObj["code"]))
}

func TestMCPCallIsolation_NotFoundMode_ExplicitMismatchAndGetOutOfScope(t *testing.T) {
	baseURL, cleanup, s, client := newMCPTestServerWithPolicy(t, policy.MCPPolicyConfig{
		Isolation: policy.MCPIsolationPolicy{Mode: "not_found"},
	})
	defer cleanup()

	incidentDenied := createAIJobLongPollTestIncident(t, s)
	incidentDenied.Namespace = "ns-denied"
	incidentDenied.Service = "svc-denied"
	require.NoError(t, s.Incident().Update(context.Background(), incidentDenied))

	headers := map[string]string{
		mcpHeaderAllowedNamespaces: "ns-allowed",
		mcpHeaderAllowedServices:   "svc-allowed",
	}

	searchBody, err := json.Marshal(map[string]any{
		"tool": "search_incidents",
		"input": map[string]any{
			"namespace": "ns-denied",
			"service":   "svc-denied",
			"limit":     20,
			"page":      1,
		},
	})
	require.NoError(t, err)

	status, respBody, err := doScopedJSONRequestWithHeaders(
		client,
		http.MethodPost,
		baseURL+"/v1/mcp/tools/call",
		searchBody,
		"incident.read",
		headers,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, status)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(respBody, &payload))
	errorObj, ok := payload["error"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, string(MCPErrorCodeNotFound), asString(errorObj["code"]))

	getBody, err := json.Marshal(map[string]any{
		"tool": "get_incident",
		"input": map[string]any{
			"incident_id": incidentDenied.IncidentID,
		},
	})
	require.NoError(t, err)

	status, respBody, err = doScopedJSONRequestWithHeaders(
		client,
		http.MethodPost,
		baseURL+"/v1/mcp/tools/call",
		getBody,
		"incident.read",
		headers,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, status)

	payload = map[string]any{}
	require.NoError(t, json.Unmarshal(respBody, &payload))
	errorObj, ok = payload["error"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, string(MCPErrorCodeNotFound), asString(errorObj["code"]))
}

func TestMCPCallGetIncidentTimeline_WhitelistOutput(t *testing.T) {
	baseURL, cleanup, s, client := newMCPTestServer(t)
	defer cleanup()

	incident := createAIJobLongPollTestIncident(t, s)
	require.NoError(t, s.DB(context.Background()).Exec(`
CREATE TABLE incident_timeline (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	incident_id TEXT NOT NULL,
	event_type TEXT,
	ref_id TEXT,
	payload_json TEXT,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
)`).Error)
	require.NoError(t, s.DB(context.Background()).Exec(
		`INSERT INTO incident_timeline (incident_id, event_type, ref_id, payload_json) VALUES (?, ?, ?, ?)`,
		incident.IncidentID,
		"alert_ingested",
		"event-1",
		`{"message":"demo timeline detail","headers":{"Authorization":"Bearer secret-token"},"token":"abc"}`,
	).Error)

	rawBody, err := json.Marshal(map[string]any{
		"tool": "get_incident_timeline",
		"input": map[string]any{
			"incident_id": incident.IncidentID,
			"limit":       20,
		},
	})
	require.NoError(t, err)

	status, respBody, err := doScopedJSONRequest(client, http.MethodPost, baseURL+"/v1/mcp/tools/call", rawBody, "incident.read")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	assertNoSensitiveLeak(t, respBody)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(respBody, &payload))
	output, ok := payload["output"].(map[string]any)
	require.True(t, ok)
	events, ok := output["events"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, events)

	firstEvent, ok := events[0].(map[string]any)
	require.True(t, ok)
	require.NotContains(t, firstEvent, "payloadJSON")
	require.NotContains(t, firstEvent, "detail")
}

func TestMCPCallGetNoticeDelivery_Sanitized(t *testing.T) {
	baseURL, cleanup, s, client := newMCPTestServer(t)
	defer cleanup()

	responseBody := `{"secret":"top-secret","ok":false}`
	errorMessage := "authorization failed with token"
	incidentID := "incident-notice-sanitized"
	jobID := "job-notice-sanitized"
	snapshotEndpoint := "http://notice.local/webhook"
	snapshotHeaders := `{"Authorization":"Bearer super-secret-token","X-Test":"mcp"}`
	snapshotSecret := "secret-fingerprint"
	snapshotTimeout := int64(3200)
	snapshotVersion := int64(7)

	delivery := &model.NoticeDeliveryM{
		ChannelID:                 "notice-channel-1",
		EventType:                 "incident_created",
		IncidentID:                &incidentID,
		JobID:                     &jobID,
		RequestBody:               `{"message":"hello","headers":{"Authorization":"Bearer top-secret"},"token":"abc"}`,
		ResponseBody:              &responseBody,
		LatencyMs:                 12,
		Status:                    "failed",
		Attempts:                  1,
		MaxAttempts:               3,
		NextRetryAt:               time.Now().UTC(),
		SnapshotEndpointURL:       &snapshotEndpoint,
		SnapshotTimeoutMs:         &snapshotTimeout,
		SnapshotHeadersJSON:       &snapshotHeaders,
		SnapshotSecretFingerprint: &snapshotSecret,
		SnapshotChannelVersion:    &snapshotVersion,
		IdempotencyKey:            "idem-notice-mcp",
		Error:                     &errorMessage,
	}
	require.NoError(t, s.NoticeDelivery().Create(context.Background(), delivery))
	require.NotEmpty(t, delivery.DeliveryID)

	rawBody, err := json.Marshal(map[string]any{
		"tool": "get_notice_delivery",
		"input": map[string]any{
			"delivery_id": delivery.DeliveryID,
		},
	})
	require.NoError(t, err)

	status, respBody, err := doScopedJSONRequest(client, http.MethodPost, baseURL+"/v1/mcp/tools/call", rawBody, "notice.read")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	assertNoSensitiveLeak(t, respBody)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(respBody, &payload))
	output, ok := payload["output"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, delivery.DeliveryID, asString(output["deliveryID"]))
	require.NotContains(t, output, "idempotencyKey")

	if snapshot, ok := output["snapshot"].(map[string]any); ok {
		require.NotContains(t, snapshot, "headers")
		require.NotContains(t, snapshot, "secret")
		require.NotContains(t, snapshot, "secretFingerprint")
	}
}

func TestMCPCallGetNoticeDelivery_ScopeDenied(t *testing.T) {
	baseURL, cleanup, _, client := newMCPTestServer(t)
	defer cleanup()

	rawBody, err := json.Marshal(map[string]any{
		"tool": "get_notice_delivery",
		"input": map[string]any{
			"delivery_id": "notice-delivery-scope-denied",
		},
	})
	require.NoError(t, err)

	status, respBody, err := doScopedJSONRequest(client, http.MethodPost, baseURL+"/v1/mcp/tools/call", rawBody, "incident.read")
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, status)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(respBody, &payload))
	errorObj, ok := payload["error"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, string(MCPErrorCodeScopeDenied), asString(errorObj["code"]))
}

func newMCPTestServer(t *testing.T) (baseURL string, cleanup func(), s store.IStore, client *http.Client) {
	return newMCPTestServerWithPolicy(t, policy.MCPPolicyConfig{})
}

func newMCPTestServerWithPolicy(
	t *testing.T,
	mcpPolicy policy.MCPPolicyConfig,
) (baseURL string, cleanup func(), s store.IStore, client *http.Client) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	store.ResetForTest()
	db := newMCPTestDB(t)
	s = store.NewStore(db)
	val := validation.New(s)
	h := NewHandler(biz.NewBiz(s), val)
	h.ConfigureMCPPolicy(mcpPolicy)

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
	require.NoError(t, db.AutoMigrate(
		&model.AlertEventM{},
		&model.DatasourceM{},
		&model.EvidenceM{},
		&model.NoticeChannelM{},
		&model.NoticeDeliveryM{},
	))
	return db
}

func doScopedJSONRequest(client *http.Client, method string, url string, payload []byte, scopes string) (int, []byte, error) {
	return doScopedJSONRequestWithHeaders(client, method, url, payload, scopes, nil)
}

func doScopedJSONRequestWithHeaders(
	client *http.Client,
	method string,
	url string,
	payload []byte,
	scopes string,
	headers map[string]string,
) (int, []byte, error) {
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
	for key, value := range headers {
		trimmedKey := strings.TrimSpace(key)
		trimmedValue := strings.TrimSpace(value)
		if trimmedKey == "" || trimmedValue == "" {
			continue
		}
		req.Header.Set(trimmedKey, trimmedValue)
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

func ptrBoolValue(value bool) *bool {
	v := value
	return &v
}

func ptrInt64Value(value int64) *int64 {
	v := value
	return &v
}

func ptrIntValue(value int) *int {
	v := value
	return &v
}

func assertNoSensitiveLeak(t *testing.T, body []byte) {
	t.Helper()
	lower := strings.ToLower(string(body))
	require.NotContains(t, lower, "\"secret\"")
	require.NotContains(t, lower, "\"authorization\"")
	require.NotContains(t, lower, "\"token\"")
	require.NotContains(t, lower, "\"headers\"")
}
