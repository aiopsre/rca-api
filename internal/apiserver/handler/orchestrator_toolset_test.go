package handler

import (
	"fmt"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
)

// Note: These tests validate the deprecated env-based toolset configuration.
// The env-based fallback has been removed from orchestrator_toolset/toolset.go.
// Tests should be updated to use DB-based configuration (toolset_config_dynamics table).
// See docs/tooling/tool-registry.md for the new configuration approach.

func TestResolveOrchestratorToolset_DBConfigSuccess(t *testing.T) {
	baseURL, cleanup, store, client := newTestServer(t)
	defer cleanup()

	// Insert toolset config directly into DB
	require.NoError(t, store.InternalStrategyConfig().UpsertToolsetConfig(t.Context(), &model.ToolsetConfigDynamicM{
		PipelineID:       "basic_rca",
		ToolsetName:      "default",
		AllowedToolsJSON: ptr(`["query_metrics","query_logs"]`),
	}))

	status, body, err := doJSONRequest(client, http.MethodGet, baseURL+"/v1/orchestrator/toolsets/resolve?pipeline=basic_rca", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)

	data := extractDataContainer(body)
	require.NotNil(t, data)
	require.Equal(t, "basic_rca", extractString(data, "pipeline"))

	toolsetObj, ok := data["toolset"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "default", extractString(toolsetObj, "toolsetID", "toolsetId", "toolset_id"))
	providers, ok := toolsetObj["providers"].([]any)
	require.True(t, ok)
	require.Len(t, providers, 1)
}

// TestResolveOrchestratorToolset_EnvConfig_StringMappingSuccess tests the deprecated env-based configuration.
// Deprecated: Use DB-based configuration instead.
func TestResolveOrchestratorToolset_EnvConfig_StringMappingSuccess(t *testing.T) {
	t.Skip("env-based toolset config is deprecated; use DB-based configuration")
	baseURL, cleanup, _, client := newTestServer(t)
	defer cleanup()

	t.Setenv("RCA_TOOLSET_CONFIG_PATH", "")
	t.Setenv(
		"RCA_TOOLSET_CONFIG_JSON",
		`{
		  "pipelines": {"basic_rca":"default"},
		  "toolsets": {
			"default": {
			  "providers": [
				{"type":"mcp_http","base_url":"http://127.0.0.1:5555","allow_tools":["query_metrics","query_logs"]}
			  ]
			}
		  }
		}`,
	)

	status, body, err := doJSONRequest(client, http.MethodGet, baseURL+"/v1/orchestrator/toolsets/resolve?pipeline=basic_rca", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)

	data := extractDataContainer(body)
	require.NotNil(t, data)
	require.Equal(t, "basic_rca", extractString(data, "pipeline"))

	toolsetObj, ok := data["toolset"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "default", extractString(toolsetObj, "toolsetID", "toolsetId", "toolset_id"))
	providers, ok := toolsetObj["providers"].([]any)
	require.True(t, ok)
	require.Len(t, providers, 1)

	toolsetsObj, ok := data["toolsets"].([]any)
	require.True(t, ok)
	require.Len(t, toolsetsObj, 1)
	first, ok := toolsetsObj[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "default", extractString(first, "toolsetID", "toolsetId", "toolset_id"))
}

func TestResolveOrchestratorToolset_EnvConfig_ListMappingSuccess(t *testing.T) {
	t.Skip("env-based toolset config is deprecated; use DB-based configuration")
	baseURL, cleanup, _, client := newTestServer(t)
	defer cleanup()

	t.Setenv("RCA_TOOLSET_CONFIG_PATH", "")
	t.Setenv(
		"RCA_TOOLSET_CONFIG_JSON",
		`{
		  "pipelines": {"basic_rca":["logs_only","metrics_only"]},
		  "toolsets": {
			"logs_only": {
			  "providers": [
				{"type":"mcp_http","base_url":"http://127.0.0.1:5555","allow_tools":["query_logs"]}
			  ]
			},
			"metrics_only": {
			  "providers": [
				{"type":"mcp_http","base_url":"http://127.0.0.1:5555","allow_tools":["query_metrics"]}
			  ]
			}
		  }
		}`,
	)

	status, body, err := doJSONRequest(client, http.MethodGet, baseURL+"/v1/orchestrator/toolsets/resolve?pipeline=basic_rca", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)

	data := extractDataContainer(body)
	require.NotNil(t, data)
	require.Equal(t, "basic_rca", extractString(data, "pipeline"))

	toolsetsObj, ok := data["toolsets"].([]any)
	require.True(t, ok)
	require.Len(t, toolsetsObj, 2)

	first, ok := toolsetsObj[0].(map[string]any)
	require.True(t, ok)
	second, ok := toolsetsObj[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "logs_only", extractString(first, "toolsetID", "toolsetId", "toolset_id"))
	require.Equal(t, "metrics_only", extractString(second, "toolsetID", "toolsetId", "toolset_id"))

	toolsetObj, ok := data["toolset"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "logs_only", extractString(toolsetObj, "toolsetID", "toolsetId", "toolset_id"))
}

func TestResolveOrchestratorToolset_MissingMappingReturnsNotFound(t *testing.T) {
	baseURL, cleanup, _, client := newTestServer(t)
	defer cleanup()

	// No toolset config in DB, should return not found
	status, _, err := doJSONRequest(
		client,
		http.MethodGet,
		baseURL+"/v1/orchestrator/toolsets/resolve?pipeline=custom_pipeline",
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, status)
}

func TestResolveOrchestratorToolset_EnvConfig_InvalidConfigReturnsError(t *testing.T) {
	t.Skip("env-based toolset config is deprecated; use DB-based configuration")
	baseURL, cleanup, _, client := newTestServer(t)
	defer cleanup()

	t.Setenv("RCA_TOOLSET_CONFIG_PATH", "")
	t.Setenv("RCA_TOOLSET_CONFIG_JSON", "{")

	status, body, err := doJSONRequest(
		client,
		http.MethodGet,
		baseURL+"/v1/orchestrator/toolsets/resolve",
		nil,
	)
	require.NoError(t, err)
	if status != http.StatusInternalServerError && status != http.StatusBadRequest {
		t.Fatalf("unexpected status=%d body=%s", status, string(body))
	}
}

func TestResolveOrchestratorToolset_EnvConfig_PathConfig(t *testing.T) {
	t.Skip("env-based toolset config is deprecated; use DB-based configuration")
	baseURL, cleanup, _, client := newTestServer(t)
	defer cleanup()

	t.Setenv("RCA_TOOLSET_CONFIG_JSON", "")
	configFile := fmt.Sprintf("%s/phaseh-toolset-config.json", t.TempDir())
	require.NoError(t, osWriteFile(configFile, []byte(
		`{"pipelines":{"basic_rca":"default"},"toolsets":{"default":{"providers":[{"type":"mcp_http","base_url":"http://127.0.0.1:5555","allow_tools":["query_logs"]}]}}}`,
	)))
	t.Setenv("RCA_TOOLSET_CONFIG_PATH", configFile)

	status, body, err := doJSONRequest(client, http.MethodGet, baseURL+"/v1/orchestrator/toolsets/resolve", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	data := extractDataContainer(body)
	require.NotNil(t, data)
	require.Equal(t, "basic_rca", extractString(data, "pipeline"))
}

func osWriteFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}

func ptr(s string) *string { return &s }
