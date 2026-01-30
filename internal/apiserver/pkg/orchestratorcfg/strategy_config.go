package orchestratorcfg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/aiopsre/rca-api/internal/apiserver/pkg/orchestratorregistry"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

const (
	envStrategyConfigJSON = "RCA_STRATEGY_CONFIG_JSON"
	envStrategyConfigPath = "RCA_STRATEGY_CONFIG_PATH"
)

var (
	// ErrStrategyInvalidConfig indicates resolve failed because strategy config is malformed/unavailable.
	ErrStrategyInvalidConfig = errors.New("invalid orchestrator strategy config")
	// ErrStrategyNotFound indicates normalized pipeline has no mapped strategy.
	ErrStrategyNotFound = errors.New("orchestrator strategy not found")
	// ErrStrategyTemplateNotRegistered indicates strategy template is not registered in template registry.
	ErrStrategyTemplateNotRegistered = errors.New("orchestrator strategy template not registered")
)

type strategyConfig struct {
	Pipelines map[string]strategyPipelineBinding
	Toolsets  map[string]toolsetV
}

type strategyPipelineBinding struct {
	TemplateID string
	ToolsetIDs []string
}

// ResolveStrategy resolves one orchestrator strategy by pipeline.
func ResolveStrategy(pipeline string) (*v1.OrchestratorStrategy, error) {
	cfg, err := loadStrategyConfigFromEnv()
	if err != nil {
		return nil, err
	}

	normalizedPipeline := NormalizePipeline(pipeline)
	binding, ok := cfg.Pipelines[normalizedPipeline]
	if !ok {
		return nil, fmt.Errorf("%w: pipeline=%s", ErrStrategyNotFound, normalizedPipeline)
	}
	if strings.TrimSpace(binding.TemplateID) == "" {
		return nil, strategyInvalidConfigf("pipeline=%s template_id is empty", normalizedPipeline)
	}
	if len(binding.ToolsetIDs) == 0 {
		return nil, strategyInvalidConfigf("pipeline=%s mapped toolset chain is empty", normalizedPipeline)
	}

	strategy := &v1.OrchestratorStrategy{
		Pipeline:   normalizedPipeline,
		TemplateID: binding.TemplateID,
		Toolsets:   make([]*v1.OrchestratorToolset, 0, len(binding.ToolsetIDs)),
	}

	for index, rawToolsetID := range binding.ToolsetIDs {
		toolsetID := strings.TrimSpace(rawToolsetID)
		if toolsetID == "" {
			return nil, strategyInvalidConfigf("pipeline=%s has empty toolset_id at index=%d", normalizedPipeline, index+1)
		}
		toolsetPayload, exists := cfg.Toolsets[toolsetID]
		if !exists {
			return nil, strategyInvalidConfigf("pipeline=%s references missing toolset_id=%s", normalizedPipeline, toolsetID)
		}
		if len(toolsetPayload.Providers) == 0 {
			return nil, strategyInvalidConfigf("pipeline=%s toolset=%s has empty providers", normalizedPipeline, toolsetID)
		}

		resolvedToolset := &v1.OrchestratorToolset{
			ToolsetID: toolsetID,
			Providers: make([]*v1.OrchestratorToolsetProvider, 0, len(toolsetPayload.Providers)),
		}
		for providerIndex, provider := range toolsetPayload.Providers {
			normalizedProvider, normalizeErr := normalizeStrategyProvider(provider, toolsetID, providerIndex+1)
			if normalizeErr != nil {
				return nil, normalizeErr
			}
			resolvedToolset.Providers = append(resolvedToolset.Providers, normalizedProvider)
		}
		strategy.Toolsets = append(strategy.Toolsets, resolvedToolset)
	}

	if err := ensureTemplateRegistered(strategy.TemplateID); err != nil {
		return nil, err
	}
	return strategy, nil
}

func loadStrategyConfigFromEnv() (*strategyConfig, error) {
	rawJSON := strings.TrimSpace(os.Getenv(envStrategyConfigJSON))
	path := strings.TrimSpace(os.Getenv(envStrategyConfigPath))

	var rawBytes []byte
	switch {
	case rawJSON != "":
		rawBytes = []byte(rawJSON)
	case path != "":
		fileBytes, err := os.ReadFile(path)
		if err != nil {
			return nil, strategyInvalidConfigf("failed to read %s: %v", envStrategyConfigPath, err)
		}
		rawBytes = fileBytes
	default:
		return nil, strategyInvalidConfigf("both %s and %s are empty", envStrategyConfigJSON, envStrategyConfigPath)
	}

	rawCfg := struct {
		Pipelines map[string]struct {
			TemplateID string `json:"template_id"`
			Toolsets   any    `json:"toolsets"`
		} `json:"pipelines"`
		Toolsets map[string]toolsetV `json:"toolsets"`
	}{}
	if err := json.Unmarshal(rawBytes, &rawCfg); err != nil {
		return nil, strategyInvalidConfigf("json decode failed: %v", err)
	}
	if rawCfg.Pipelines == nil || len(rawCfg.Pipelines) == 0 {
		return nil, strategyInvalidConfigf("pipelines is empty")
	}
	if rawCfg.Toolsets == nil || len(rawCfg.Toolsets) == 0 {
		return nil, strategyInvalidConfigf("toolsets is empty")
	}

	normalizedPipelines := make(map[string]strategyPipelineBinding, len(rawCfg.Pipelines))
	for pipeline, mapping := range rawCfg.Pipelines {
		normalizedPipeline := NormalizePipeline(pipeline)
		templateID := strings.TrimSpace(mapping.TemplateID)
		if templateID == "" {
			return nil, strategyInvalidConfigf("pipeline=%s template_id is empty", normalizedPipeline)
		}
		toolsetChain, normalizeErr := normalizeStrategyToolsetChain(normalizedPipeline, mapping.Toolsets)
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		normalizedPipelines[normalizedPipeline] = strategyPipelineBinding{
			TemplateID: templateID,
			ToolsetIDs: toolsetChain,
		}
	}

	return &strategyConfig{
		Pipelines: normalizedPipelines,
		Toolsets:  rawCfg.Toolsets,
	}, nil
}

func normalizeStrategyToolsetChain(pipeline string, raw any) ([]string, error) {
	list, ok := raw.([]any)
	if !ok {
		return nil, strategyInvalidConfigf("pipeline=%s toolsets must be list[string]", pipeline)
	}
	if len(list) == 0 {
		return nil, strategyInvalidConfigf("pipeline=%s mapped toolset chain is empty", pipeline)
	}

	chain := make([]string, 0, len(list))
	for index, item := range list {
		toolsetID, castOK := item.(string)
		if !castOK {
			return nil, strategyInvalidConfigf("pipeline=%s mapped toolset_id at index=%d must be string", pipeline, index+1)
		}
		normalized := strings.TrimSpace(toolsetID)
		if normalized == "" {
			return nil, strategyInvalidConfigf("pipeline=%s mapped toolset_id is empty at index=%d", pipeline, index+1)
		}
		chain = append(chain, normalized)
	}
	return chain, nil
}

func normalizeStrategyProvider(provider providerV, toolsetID string, index int) (*v1.OrchestratorToolsetProvider, error) {
	providerType := strings.ToLower(strings.TrimSpace(provider.Type))
	if providerType == "" {
		return nil, strategyInvalidConfigf("toolset=%s provider[%d] type is empty", toolsetID, index)
	}
	if providerType != providerTypeMCPHTTP && providerType != providerTypeSkills {
		return nil, strategyInvalidConfigf("toolset=%s provider[%d] unsupported type=%s", toolsetID, index, providerType)
	}

	allowTools := make([]string, 0, len(provider.AllowTools))
	seen := make(map[string]struct{}, len(provider.AllowTools))
	for _, tool := range provider.AllowTools {
		normalizedTool := strings.TrimSpace(tool)
		if normalizedTool == "" {
			return nil, strategyInvalidConfigf("toolset=%s provider[%d] allow_tools contains empty", toolsetID, index)
		}
		if _, exists := seen[normalizedTool]; exists {
			continue
		}
		seen[normalizedTool] = struct{}{}
		allowTools = append(allowTools, normalizedTool)
	}
	if len(allowTools) == 0 {
		return nil, strategyInvalidConfigf("toolset=%s provider[%d] allow_tools is empty", toolsetID, index)
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
		return nil, strategyInvalidConfigf("toolset=%s provider[%d] base_url is required", toolsetID, index)
	}
	if providerType == providerTypeSkills && module == "" {
		return nil, strategyInvalidConfigf("toolset=%s provider[%d] module is required", toolsetID, index)
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

func ensureTemplateRegistered(templateID string) error {
	registered, err := orchestratorregistry.List(context.Background())
	if err != nil {
		return strategyInvalidConfigf("template registry list failed: %v", err)
	}
	normalizedTemplateID := strings.TrimSpace(templateID)
	for _, item := range registered {
		if strings.TrimSpace(item.GetTemplateID()) == normalizedTemplateID {
			return nil
		}
	}
	return fmt.Errorf("%w: template_id=%s", ErrStrategyTemplateNotRegistered, normalizedTemplateID)
}

func strategyInvalidConfigf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrStrategyInvalidConfig, fmt.Sprintf(format, args...))
}
