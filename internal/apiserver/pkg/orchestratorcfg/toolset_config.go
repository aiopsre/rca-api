package orchestratorcfg

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

const (
	defaultPipeline         = "basic_rca"
	envToolsetConfigJSON    = "RCA_TOOLSET_CONFIG_JSON"
	envToolsetConfigPath    = "RCA_TOOLSET_CONFIG_PATH"
	providerTypeMCPHTTP     = "mcp_http"
	providerTypeSkills      = "skills"
	defaultProviderFunction = "call"
)

var (
	// ErrInvalidConfig indicates resolve failed because server toolset config is malformed/unavailable.
	ErrInvalidConfig = errors.New("invalid orchestrator toolset config")
	// ErrToolsetNotFound indicates normalized pipeline has no mapped toolset in config.
	ErrToolsetNotFound = errors.New("orchestrator toolset not found")
)

type toolsetConfig struct {
	Pipelines map[string]string   `json:"pipelines"`
	Toolsets  map[string]toolsetV `json:"toolsets"`
}

type toolsetV struct {
	Providers []providerV `json:"providers"`
}

type providerV struct {
	Type       string   `json:"type"`
	AllowTools []string `json:"allow_tools"`
	Name       string   `json:"name"`
	BaseURL    string   `json:"base_url"`
	Scopes     string   `json:"scopes"`
	TimeoutS   *float64 `json:"timeout_s"`
	Module     string   `json:"module"`
	Function   string   `json:"function"`
}

// NormalizePipeline applies pipeline-only normalize semantics used by orchestrator:
// trim+lower, and empty pipeline resolves to basic_rca.
func NormalizePipeline(pipeline string) string {
	normalized := strings.ToLower(strings.TrimSpace(pipeline))
	if normalized == "" {
		return defaultPipeline
	}
	return normalized
}

// Resolve returns one resolved toolset for the input pipeline from server-side config.
func Resolve(pipeline string) (*v1.OrchestratorToolset, error) {
	cfg, err := loadConfigFromEnv()
	if err != nil {
		return nil, err
	}

	normalizedPipeline := NormalizePipeline(pipeline)
	toolsetID := strings.TrimSpace(cfg.Pipelines[normalizedPipeline])
	if toolsetID == "" {
		return nil, fmt.Errorf("%w: pipeline=%s", ErrToolsetNotFound, normalizedPipeline)
	}

	toolsetPayload, ok := cfg.Toolsets[toolsetID]
	if !ok {
		return nil, fmt.Errorf("%w: toolset=%s", ErrToolsetNotFound, toolsetID)
	}
	if len(toolsetPayload.Providers) == 0 {
		return nil, invalidConfigf("toolset=%s has empty providers", toolsetID)
	}

	out := &v1.OrchestratorToolset{
		ToolsetID: toolsetID,
		Providers: make([]*v1.OrchestratorToolsetProvider, 0, len(toolsetPayload.Providers)),
	}
	for index, provider := range toolsetPayload.Providers {
		normalizedProvider, normalizeErr := normalizeProvider(provider, toolsetID, index+1)
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		out.Providers = append(out.Providers, normalizedProvider)
	}
	return out, nil
}

func loadConfigFromEnv() (*toolsetConfig, error) {
	rawJSON := strings.TrimSpace(os.Getenv(envToolsetConfigJSON))
	path := strings.TrimSpace(os.Getenv(envToolsetConfigPath))

	var rawBytes []byte
	switch {
	case rawJSON != "":
		rawBytes = []byte(rawJSON)
	case path != "":
		fileBytes, err := os.ReadFile(path)
		if err != nil {
			return nil, invalidConfigf("failed to read %s: %v", envToolsetConfigPath, err)
		}
		rawBytes = fileBytes
	default:
		return nil, invalidConfigf("both %s and %s are empty", envToolsetConfigJSON, envToolsetConfigPath)
	}

	cfg := &toolsetConfig{}
	if err := json.Unmarshal(rawBytes, cfg); err != nil {
		return nil, invalidConfigf("json decode failed: %v", err)
	}
	if cfg.Pipelines == nil || len(cfg.Pipelines) == 0 {
		return nil, invalidConfigf("pipelines is empty")
	}
	if cfg.Toolsets == nil || len(cfg.Toolsets) == 0 {
		return nil, invalidConfigf("toolsets is empty")
	}

	normalizedPipelines := make(map[string]string, len(cfg.Pipelines))
	for pipeline, toolsetID := range cfg.Pipelines {
		normalizedPipeline := NormalizePipeline(pipeline)
		normalizedToolsetID := strings.TrimSpace(toolsetID)
		if normalizedToolsetID == "" {
			return nil, invalidConfigf("pipeline=%s mapped toolset_id is empty", normalizedPipeline)
		}
		normalizedPipelines[normalizedPipeline] = normalizedToolsetID
	}
	cfg.Pipelines = normalizedPipelines

	return cfg, nil
}

func normalizeProvider(provider providerV, toolsetID string, index int) (*v1.OrchestratorToolsetProvider, error) {
	providerType := strings.ToLower(strings.TrimSpace(provider.Type))
	if providerType == "" {
		return nil, invalidConfigf("toolset=%s provider[%d] type is empty", toolsetID, index)
	}
	if providerType != providerTypeMCPHTTP && providerType != providerTypeSkills {
		return nil, invalidConfigf("toolset=%s provider[%d] unsupported type=%s", toolsetID, index, providerType)
	}

	allowTools := make([]string, 0, len(provider.AllowTools))
	seen := make(map[string]struct{}, len(provider.AllowTools))
	for _, tool := range provider.AllowTools {
		normalizedTool := strings.TrimSpace(tool)
		if normalizedTool == "" {
			return nil, invalidConfigf("toolset=%s provider[%d] allow_tools contains empty", toolsetID, index)
		}
		if _, ok := seen[normalizedTool]; ok {
			continue
		}
		seen[normalizedTool] = struct{}{}
		allowTools = append(allowTools, normalizedTool)
	}
	if len(allowTools) == 0 {
		return nil, invalidConfigf("toolset=%s provider[%d] allow_tools is empty", toolsetID, index)
	}

	name := strings.TrimSpace(provider.Name)
	if name == "" {
		name = fmt.Sprintf("%s-%d", providerType, index)
	}
	baseURL := strings.TrimSpace(provider.BaseURL)
	module := strings.TrimSpace(provider.Module)
	function := strings.TrimSpace(provider.Function)
	if function == "" {
		function = defaultProviderFunction
	}

	if providerType == providerTypeMCPHTTP && baseURL == "" {
		return nil, invalidConfigf("toolset=%s provider[%d] base_url is required", toolsetID, index)
	}
	if providerType == providerTypeSkills && module == "" {
		return nil, invalidConfigf("toolset=%s provider[%d] module is required", toolsetID, index)
	}

	out := &v1.OrchestratorToolsetProvider{
		Type:       providerType,
		AllowTools: allowTools,
		Name:       &name,
		BaseURL:    ptrString(baseURL),
		Scopes:     ptrString(strings.TrimSpace(provider.Scopes)),
		Module:     ptrString(module),
		Function:   ptrString(function),
	}
	if provider.TimeoutS != nil && *provider.TimeoutS > 0 {
		timeout := *provider.TimeoutS
		out.TimeoutSeconds = &timeout
	}
	return out, nil
}

func invalidConfigf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidConfig, fmt.Sprintf(format, args...))
}

func ptrString(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
