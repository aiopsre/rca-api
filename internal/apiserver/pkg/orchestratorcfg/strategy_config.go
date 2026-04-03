package orchestratorcfg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	Pipelines      map[string]strategyPipelineBinding
	InlineToolsets map[string]toolsetV
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

	resolvedToolsets, err := resolveStrategyToolsets(normalizedPipeline, binding, cfg.InlineToolsets)
	if err != nil {
		return nil, err
	}

	strategy := &v1.OrchestratorStrategy{
		Pipeline:   normalizedPipeline,
		TemplateID: binding.TemplateID,
		Toolsets:   resolvedToolsets,
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
		Pipelines:      normalizedPipelines,
		InlineToolsets: rawCfg.Toolsets,
	}, nil
}

func normalizeStrategyToolsetChain(pipeline string, raw any) ([]string, error) {
	if raw == nil {
		return []string{}, nil
	}

	list, ok := raw.([]any)
	if !ok {
		return nil, strategyInvalidConfigf("pipeline=%s toolsets must be list[string]", pipeline)
	}
	if len(list) == 0 {
		return []string{}, nil
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

func resolveStrategyToolsets(
	normalizedPipeline string,
	binding strategyPipelineBinding,
	inlineToolsetDefinitions map[string]toolsetV,
) ([]*v1.OrchestratorToolset, error) {
	// Canonical source: dedicated toolset config (RCA_TOOLSET_CONFIG_JSON/PATH).
	if toolsetConfigSourceConfigured() {
		slog.Warn("DEPRECATED: environment-based toolset config is deprecated, migrate to DB config",
			"env_vars", fmt.Sprintf("%s/%s", envToolsetConfigJSON, envToolsetConfigPath),
			"migration_api", "/v1/internal-strategy-config/toolsets",
			"pipeline", normalizedPipeline,
		)
		resolvedToolsets, err := ResolveChain(normalizedPipeline)
		if err != nil {
			return nil, strategyInvalidConfigf("pipeline=%s resolve toolset config failed: %v", normalizedPipeline, err)
		}
		if len(resolvedToolsets) == 0 {
			return nil, strategyInvalidConfigf("pipeline=%s mapped toolset chain is empty", normalizedPipeline)
		}
		return resolvedToolsets, nil
	}

	// Transitional fallback: strategy inline toolsets for legacy deployments that only ship strategy config.
	if len(binding.ToolsetIDs) == 0 {
		return nil, strategyInvalidConfigf(
			"pipeline=%s mapped toolset chain is empty and %s/%s are not configured",
			normalizedPipeline,
			envToolsetConfigJSON,
			envToolsetConfigPath,
		)
	}
	resolvedToolsets, err := resolveToolsetChainByIDs(binding.ToolsetIDs, inlineToolsetDefinitions, normalizedPipeline)
	if err != nil {
		return nil, strategyInvalidConfigf("pipeline=%s inline toolset resolve failed: %v", normalizedPipeline, err)
	}
	return resolvedToolsets, nil
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
