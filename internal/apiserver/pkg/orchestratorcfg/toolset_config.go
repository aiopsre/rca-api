// Package orchestratorcfg provides orchestrator configuration utilities.
//
// Deprecated: The environment-based toolset configuration (RCA_TOOLSET_CONFIG_JSON/PATH)
// has been deprecated. Use the toolset_config_dynamics table instead, managed via
// /v1/internal-strategy-config/toolsets API. This package is kept for backward
// compatibility and may be removed in a future version.
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
	defaultProviderFunction = "call"
)

var (
	// ErrInvalidConfig indicates resolve failed because server toolset config is malformed/unavailable.
	ErrInvalidConfig = errors.New("invalid orchestrator toolset config")
	// ErrToolsetNotFound indicates normalized pipeline has no mapped toolset in config.
	ErrToolsetNotFound = errors.New("orchestrator toolset not found")
)

type toolsetConfig struct {
	Pipelines map[string][]string
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
	toolsets, err := ResolveChain(pipeline)
	if err != nil {
		return nil, err
	}
	if len(toolsets) == 0 {
		return nil, fmt.Errorf("%w: pipeline=%s", ErrToolsetNotFound, NormalizePipeline(pipeline))
	}
	return toolsets[0], nil
}

// ResolveChain returns resolved toolset chain (ordered) for the input pipeline from server-side config.
func ResolveChain(pipeline string) ([]*v1.OrchestratorToolset, error) {
	cfg, err := loadConfigFromEnv()
	if err != nil {
		return nil, err
	}

	normalizedPipeline := NormalizePipeline(pipeline)
	toolsetIDs, ok := cfg.Pipelines[normalizedPipeline]
	if !ok || len(toolsetIDs) == 0 {
		return nil, fmt.Errorf("%w: pipeline=%s", ErrToolsetNotFound, normalizedPipeline)
	}
	return resolveToolsetChainByIDs(toolsetIDs, cfg.Toolsets, normalizedPipeline)
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

	rawCfg := struct {
		Pipelines map[string]any      `json:"pipelines"`
		Toolsets  map[string]toolsetV `json:"toolsets"`
	}{}
	if err := json.Unmarshal(rawBytes, &rawCfg); err != nil {
		return nil, invalidConfigf("json decode failed: %v", err)
	}
	if rawCfg.Pipelines == nil || len(rawCfg.Pipelines) == 0 {
		return nil, invalidConfigf("pipelines is empty")
	}
	if rawCfg.Toolsets == nil || len(rawCfg.Toolsets) == 0 {
		return nil, invalidConfigf("toolsets is empty")
	}

	normalizedPipelines := make(map[string][]string, len(rawCfg.Pipelines))
	for pipeline, mapping := range rawCfg.Pipelines {
		normalizedPipeline := NormalizePipeline(pipeline)
		normalizedChain, normalizeErr := normalizePipelineToolsetChain(normalizedPipeline, mapping)
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		normalizedPipelines[normalizedPipeline] = normalizedChain
	}

	cfg := &toolsetConfig{
		Pipelines: normalizedPipelines,
		Toolsets:  rawCfg.Toolsets,
	}
	return cfg, nil
}

func toolsetConfigSourceConfigured() bool {
	return strings.TrimSpace(os.Getenv(envToolsetConfigJSON)) != "" ||
		strings.TrimSpace(os.Getenv(envToolsetConfigPath)) != ""
}

func resolveToolsetChainByIDs(
	toolsetIDs []string,
	toolsetDefinitions map[string]toolsetV,
	normalizedPipeline string,
) ([]*v1.OrchestratorToolset, error) {
	if len(toolsetIDs) == 0 {
		return nil, invalidConfigf("pipeline=%s mapped toolset chain is empty", normalizedPipeline)
	}
	if len(toolsetDefinitions) == 0 {
		return nil, invalidConfigf("pipeline=%s has no toolset definitions", normalizedPipeline)
	}

	out := make([]*v1.OrchestratorToolset, 0, len(toolsetIDs))
	for chainIndex, rawToolsetID := range toolsetIDs {
		toolsetID := strings.TrimSpace(rawToolsetID)
		if toolsetID == "" {
			return nil, invalidConfigf("pipeline=%s has empty toolset_id at index=%d", normalizedPipeline, chainIndex+1)
		}
		toolsetPayload, exists := toolsetDefinitions[toolsetID]
		if !exists {
			return nil, invalidConfigf("pipeline=%s references missing toolset_id=%s", normalizedPipeline, toolsetID)
		}
		if len(toolsetPayload.Providers) == 0 {
			return nil, invalidConfigf("pipeline=%s toolset=%s has empty providers", normalizedPipeline, toolsetID)
		}

		resolved := &v1.OrchestratorToolset{
			ToolsetID: toolsetID,
			Providers: make([]*v1.OrchestratorToolsetProvider, 0, len(toolsetPayload.Providers)),
		}
		for index, provider := range toolsetPayload.Providers {
			normalizedProvider, normalizeErr := normalizeProvider(provider, toolsetID, index+1)
			if normalizeErr != nil {
				return nil, normalizeErr
			}
			resolved.Providers = append(resolved.Providers, normalizedProvider)
		}
		out = append(out, resolved)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: pipeline=%s", ErrToolsetNotFound, normalizedPipeline)
	}
	return out, nil
}

func normalizeProvider(provider providerV, toolsetID string, index int) (*v1.OrchestratorToolsetProvider, error) {
	providerType := strings.ToLower(strings.TrimSpace(provider.Type))
	if providerType == "" {
		return nil, invalidConfigf("toolset=%s provider[%d] type is empty", toolsetID, index)
	}
	if providerType != providerTypeMCPHTTP {
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

func normalizePipelineToolsetChain(pipeline string, raw any) ([]string, error) {
	switch value := raw.(type) {
	case string:
		toolsetID := strings.TrimSpace(value)
		if toolsetID == "" {
			return nil, invalidConfigf("pipeline=%s mapped toolset_id is empty", pipeline)
		}
		return []string{toolsetID}, nil
	case []any:
		if len(value) == 0 {
			return nil, invalidConfigf("pipeline=%s mapped toolset chain is empty", pipeline)
		}
		chain := make([]string, 0, len(value))
		for index, item := range value {
			toolsetID, ok := item.(string)
			if !ok {
				return nil, invalidConfigf(
					"pipeline=%s mapped toolset_id at index=%d must be string",
					pipeline,
					index+1,
				)
			}
			normalizedToolsetID := strings.TrimSpace(toolsetID)
			if normalizedToolsetID == "" {
				return nil, invalidConfigf("pipeline=%s mapped toolset_id is empty at index=%d", pipeline, index+1)
			}
			chain = append(chain, normalizedToolsetID)
		}
		return chain, nil
	default:
		return nil, invalidConfigf("pipeline=%s mapped value has invalid type=%T", pipeline, raw)
	}
}

func ptrString(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
