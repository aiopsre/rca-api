package orchestrator_strategy

//go:generate mockgen -destination mock_strategy.go -package orchestrator_strategy github.com/aiopsre/rca-api/internal/apiserver/biz/v1/orchestrator_strategy StrategyBiz

import (
	"context"
	"errors"
	"strings"

	internalstrategyconfig "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/internal_strategy_config"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/orchestratorcfg"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

// StrategyBiz defines read-only orchestrator strategy resolve use-case.
type StrategyBiz interface {
	Resolve(ctx context.Context, req *v1.ResolveOrchestratorStrategyRequest) (*v1.ResolveOrchestratorStrategyResponse, error)

	StrategyExpansion
}

//nolint:modernize // Keep explicit placeholder for future extensions.
type StrategyExpansion interface{}

type strategyBiz struct {
	store     store.IStore
	configBiz internalstrategyconfig.ConfigBiz
}

var _ StrategyBiz = (*strategyBiz)(nil)

// New creates orchestrator strategy biz.
func New(store store.IStore) *strategyBiz {
	return &strategyBiz{
		store:     store,
		configBiz: internalstrategyconfig.New(store),
	}
}

func (b *strategyBiz) Resolve(
	ctx context.Context,
	req *v1.ResolveOrchestratorStrategyRequest,
) (*v1.ResolveOrchestratorStrategyResponse, error) {
	_ = ctx
	if req == nil {
		return nil, errno.ErrInvalidArgument
	}

	strategy, err := orchestratorcfg.ResolveStrategy(req.GetPipeline())
	if err != nil {
		switch {
		case errors.Is(err, orchestratorcfg.ErrStrategyNotFound):
			return nil, errno.ErrOrchestratorStrategyNotFound
		case errors.Is(err, orchestratorcfg.ErrStrategyTemplateNotRegistered):
			return nil, errno.ErrOrchestratorStrategyTemplateNotRegistered
		case errors.Is(err, orchestratorcfg.ErrStrategyInvalidConfig):
			return nil, errno.ErrOrchestratorStrategyConfigInvalid
		default:
			return nil, errno.ErrInternal
		}
	}
	if strategy == nil {
		return nil, errno.ErrOrchestratorStrategyNotFound
	}
	if b != nil && b.configBiz != nil {
		// Resolve skillsetIDs for the pipeline
		skillsetItems, _, skillsetErr := b.configBiz.ResolveSkillsetByPipeline(ctx, strategy.GetPipeline())
		if skillsetErr == nil && len(skillsetItems) > 0 {
			skillsetIDs := make([]string, 0, len(skillsetItems))
			for _, item := range skillsetItems {
				if item == nil {
					continue
				}
				if skillsetID := strings.TrimSpace(item.SkillsetName); skillsetID != "" {
					skillsetIDs = append(skillsetIDs, skillsetID)
				}
			}
			strategy.SkillsetIDs = skillsetIDs
		}

		// Resolve toolsetIDs for the pipeline (new canonical field)
		toolsetItems, _, toolsetErr := b.configBiz.ResolveToolsetByPipeline(ctx, strategy.GetPipeline())
		if toolsetErr == nil && len(toolsetItems) > 0 {
			toolsetIDs := make([]string, 0, len(toolsetItems))
			for _, item := range toolsetItems {
				if item == nil {
					continue
				}
				if toolsetID := strings.TrimSpace(item.ToolsetName); toolsetID != "" {
					toolsetIDs = append(toolsetIDs, toolsetID)
				}
			}
			strategy.ToolsetIDs = toolsetIDs
		}
	}

	return &v1.ResolveOrchestratorStrategyResponse{Strategy: strategy}, nil
}
