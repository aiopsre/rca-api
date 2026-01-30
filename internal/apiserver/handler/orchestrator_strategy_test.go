package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aiopsre/rca-api/internal/apiserver/pkg/orchestratorregistry"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

func TestResolveOrchestratorStrategy_Success(t *testing.T) {
	backend := newFakeTemplateRegistryBackend()
	restore := orchestratorregistry.SetBackendForTest(backend)
	defer restore()

	require.NoError(t, orchestratorregistry.Register(context.Background(), "orc-l1", []*v1.OrchestratorTemplate{{
		TemplateID: "basic_rca",
		Version:    "v1",
	}}))

	baseURL, cleanup, _, client := newTestServer(t)
	defer cleanup()

	t.Setenv("RCA_STRATEGY_CONFIG_PATH", "")
	t.Setenv(
		"RCA_STRATEGY_CONFIG_JSON",
		`{
		  "pipelines": {
			"basic_rca": {
			  "template_id": "basic_rca",
			  "toolsets": ["default"]
			}
		  },
		  "toolsets": {
			"default": {
			  "providers": [
				{"type":"mcp_http","base_url":"http://127.0.0.1:5555","allow_tools":["query_metrics"]}
			  ]
			}
		  }
		}`,
	)

	status, body, err := doJSONRequest(client, http.MethodGet, baseURL+"/v1/orchestrator/strategies/resolve?pipeline=basic_rca", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)

	data := extractDataContainer(body)
	require.NotNil(t, data)
	strategyObj, ok := data["strategy"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "basic_rca", extractString(strategyObj, "pipeline"))
	require.Equal(t, "basic_rca", extractString(strategyObj, "templateID", "templateId", "template_id"))

	toolsetsObj, ok := strategyObj["toolsets"].([]any)
	require.True(t, ok)
	require.Len(t, toolsetsObj, 1)
	first, ok := toolsetsObj[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "default", extractString(first, "toolsetID", "toolsetId", "toolset_id"))
}

func TestResolveOrchestratorStrategy_PipelineMissingReturnsNotFound(t *testing.T) {
	backend := newFakeTemplateRegistryBackend()
	restore := orchestratorregistry.SetBackendForTest(backend)
	defer restore()

	require.NoError(t, orchestratorregistry.Register(context.Background(), "orc-l1", []*v1.OrchestratorTemplate{{
		TemplateID: "basic_rca",
	}}))

	baseURL, cleanup, _, client := newTestServer(t)
	defer cleanup()

	t.Setenv("RCA_STRATEGY_CONFIG_PATH", "")
	t.Setenv(
		"RCA_STRATEGY_CONFIG_JSON",
		`{
		  "pipelines": {
			"basic_rca": {
			  "template_id": "basic_rca",
			  "toolsets": ["default"]
			}
		  },
		  "toolsets": {
			"default": {
			  "providers": [
				{"type":"skills","module":"pkg.skills.demo","allow_tools":["query_logs"]}
			  ]
			}
		  }
		}`,
	)

	status, _, err := doJSONRequest(client, http.MethodGet, baseURL+"/v1/orchestrator/strategies/resolve?pipeline=custom_pipeline", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, status)
}

func TestResolveOrchestratorStrategy_TemplateNotRegisteredReturnsNotFound(t *testing.T) {
	backend := newFakeTemplateRegistryBackend()
	restore := orchestratorregistry.SetBackendForTest(backend)
	defer restore()

	require.NoError(t, orchestratorregistry.Register(context.Background(), "orc-l1", []*v1.OrchestratorTemplate{{
		TemplateID: "registered_template",
	}}))

	baseURL, cleanup, _, client := newTestServer(t)
	defer cleanup()

	t.Setenv("RCA_STRATEGY_CONFIG_PATH", "")
	t.Setenv(
		"RCA_STRATEGY_CONFIG_JSON",
		`{
		  "pipelines": {
			"basic_rca": {
			  "template_id": "missing_template",
			  "toolsets": ["default"]
			}
		  },
		  "toolsets": {
			"default": {
			  "providers": [
				{"type":"skills","module":"pkg.skills.demo","allow_tools":["query_logs"]}
			  ]
			}
		  }
		}`,
	)

	status, _, err := doJSONRequest(client, http.MethodGet, baseURL+"/v1/orchestrator/strategies/resolve?pipeline=basic_rca", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, status)
}

func TestResolveOrchestratorStrategy_InvalidConfigReturnsError(t *testing.T) {
	backend := newFakeTemplateRegistryBackend()
	restore := orchestratorregistry.SetBackendForTest(backend)
	defer restore()

	baseURL, cleanup, _, client := newTestServer(t)
	defer cleanup()

	t.Setenv("RCA_STRATEGY_CONFIG_PATH", "")
	t.Setenv("RCA_STRATEGY_CONFIG_JSON", "{")

	status, _, err := doJSONRequest(client, http.MethodGet, baseURL+"/v1/orchestrator/strategies/resolve", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusInternalServerError, status)
}
