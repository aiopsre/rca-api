package orchestratorcfg

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/aiopsre/rca-api/internal/apiserver/pkg/orchestratorregistry"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

func TestResolveStrategy_UsesDedicatedToolsetConfigWhenConfigured(t *testing.T) {
	setupStrategyTemplateRegistry(t, "basic_rca")

	t.Setenv("RCA_STRATEGY_CONFIG_PATH", "")
	t.Setenv("RCA_TOOLSET_CONFIG_PATH", "")
	t.Setenv("RCA_STRATEGY_CONFIG_JSON", `{
	  "pipelines": {
		"basic_rca": {
		  "template_id": "basic_rca",
		  "toolsets": ["legacy_inline_only"]
		}
	  },
		"toolsets": {
		  "legacy_inline_only": {
			"providers": [
			  {"type":"mcp_http","base_url":"http://127.0.0.1:5555","allow_tools":["query_logs"]}
			]
		  }
	  }
	}`)
	t.Setenv("RCA_TOOLSET_CONFIG_JSON", `{
	  "pipelines": {"basic_rca": ["canonical_primary"]},
	  "toolsets": {
		"canonical_primary": {
		  "providers": [
			{"type":"mcp_http","base_url":"http://127.0.0.1:5555","allow_tools":["query_metrics"]}
		  ]
		}
	  }
	}`)

	strategy, err := ResolveStrategy("basic_rca")
	require.NoError(t, err)
	require.NotNil(t, strategy)
	require.Equal(t, "basic_rca", strategy.GetTemplateID())
	require.Len(t, strategy.GetToolsets(), 1)
	require.Equal(t, "canonical_primary", strategy.GetToolsets()[0].GetToolsetID())
	require.Len(t, strategy.GetToolsets()[0].GetProviders(), 1)
	require.Equal(t, []string{"query_metrics"}, strategy.GetToolsets()[0].GetProviders()[0].GetAllowTools())
}

func TestResolveStrategy_FallbacksToInlineToolsetsWhenDedicatedToolsetConfigMissing(t *testing.T) {
	setupStrategyTemplateRegistry(t, "basic_rca")

	t.Setenv("RCA_STRATEGY_CONFIG_PATH", "")
	t.Setenv("RCA_TOOLSET_CONFIG_PATH", "")
	t.Setenv("RCA_TOOLSET_CONFIG_JSON", "")
	t.Setenv("RCA_STRATEGY_CONFIG_JSON", `{
	  "pipelines": {
		"basic_rca": {
		  "template_id": "basic_rca",
		  "toolsets": ["inline_default"]
		}
	  },
		"toolsets": {
		  "inline_default": {
			"providers": [
			  {"type":"mcp_http","base_url":"http://127.0.0.1:5555","allow_tools":["query_logs"]}
			]
		  }
	  }
	}`)

	strategy, err := ResolveStrategy("basic_rca")
	require.NoError(t, err)
	require.NotNil(t, strategy)
	require.Equal(t, "basic_rca", strategy.GetTemplateID())
	require.Len(t, strategy.GetToolsets(), 1)
	require.Equal(t, "inline_default", strategy.GetToolsets()[0].GetToolsetID())
	require.Equal(t, []string{"query_logs"}, strategy.GetToolsets()[0].GetProviders()[0].GetAllowTools())
}

func TestResolveStrategy_AllowsEmptyStrategyToolsetsWhenDedicatedToolsetConfigConfigured(t *testing.T) {
	setupStrategyTemplateRegistry(t, "basic_rca")

	t.Setenv("RCA_STRATEGY_CONFIG_PATH", "")
	t.Setenv("RCA_TOOLSET_CONFIG_PATH", "")
	t.Setenv("RCA_STRATEGY_CONFIG_JSON", `{
	  "pipelines": {
		"basic_rca": {
		  "template_id": "basic_rca"
		}
	  }
	}`)
	t.Setenv("RCA_TOOLSET_CONFIG_JSON", `{
	  "pipelines": {"basic_rca": ["canonical_default"]},
		"toolsets": {
		  "canonical_default": {
			"providers": [
			  {"type":"mcp_http","base_url":"http://127.0.0.1:5555","allow_tools":["query_logs"]}
			]
		  }
	  }
	}`)

	strategy, err := ResolveStrategy("basic_rca")
	require.NoError(t, err)
	require.NotNil(t, strategy)
	require.Equal(t, "basic_rca", strategy.GetTemplateID())
	require.Len(t, strategy.GetToolsets(), 1)
	require.Equal(t, "canonical_default", strategy.GetToolsets()[0].GetToolsetID())
}

func TestResolveStrategy_ReturnsInvalidConfigWhenNoToolsetSourceAvailable(t *testing.T) {
	setupStrategyTemplateRegistry(t, "basic_rca")

	t.Setenv("RCA_STRATEGY_CONFIG_PATH", "")
	t.Setenv("RCA_TOOLSET_CONFIG_PATH", "")
	t.Setenv("RCA_TOOLSET_CONFIG_JSON", "")
	t.Setenv("RCA_STRATEGY_CONFIG_JSON", `{
	  "pipelines": {
		"basic_rca": {
		  "template_id": "basic_rca"
		}
	  }
	}`)

	strategy, err := ResolveStrategy("basic_rca")
	require.Nil(t, strategy)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrStrategyInvalidConfig))
}

func setupStrategyTemplateRegistry(t *testing.T, templateIDs ...string) {
	t.Helper()

	backend := newStrategyTestTemplateRegistryBackend()
	restore := orchestratorregistry.SetBackendForTest(backend)
	t.Cleanup(restore)

	templates := make([]*v1.OrchestratorTemplate, 0, len(templateIDs))
	for _, rawTemplateID := range templateIDs {
		templateID := strings.TrimSpace(rawTemplateID)
		if templateID == "" {
			continue
		}
		templates = append(templates, &v1.OrchestratorTemplate{TemplateID: templateID, Version: "v1"})
	}
	require.NotEmpty(t, templates)
	require.NoError(t, orchestratorregistry.Register(context.Background(), "orc-strategy-test", templates))
}

func newStrategyTestTemplateRegistryBackend() *strategyTestTemplateRegistryBackend {
	return &strategyTestTemplateRegistryBackend{values: make(map[string]string)}
}

type strategyTestTemplateRegistryBackend struct {
	mu      sync.Mutex
	values  map[string]string
	setErr  error
	getErr  error
	scanErr error
}

func (f *strategyTestTemplateRegistryBackend) Set(_ context.Context, key string, value string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setErr != nil {
		return f.setErr
	}
	f.values[key] = value
	return nil
}

func (f *strategyTestTemplateRegistryBackend) Get(_ context.Context, key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return "", f.getErr
	}
	value, ok := f.values[key]
	if !ok {
		return "", redis.Nil
	}
	return value, nil
}

func (f *strategyTestTemplateRegistryBackend) Scan(_ context.Context, _ uint64, match string, _ int64) ([]string, uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.scanErr != nil {
		return nil, 0, f.scanErr
	}
	prefix := strings.TrimSuffix(match, "*")
	keys := make([]string, 0, len(f.values))
	for key := range f.values {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys, 0, nil
}

var _ orchestratorregistry.Backend = (*strategyTestTemplateRegistryBackend)(nil)
