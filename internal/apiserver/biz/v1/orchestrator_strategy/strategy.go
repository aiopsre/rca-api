package orchestrator_strategy

//go:generate mockgen -destination mock_strategy.go -package orchestrator_strategy github.com/aiopsre/rca-api/internal/apiserver/biz/v1/orchestrator_strategy StrategyBiz

import (
	"context"
	"errors"

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
	store store.IStore
}

var _ StrategyBiz = (*strategyBiz)(nil)

// New creates orchestrator strategy biz.
func New(store store.IStore) *strategyBiz {
	return &strategyBiz{store: store}
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

	return &v1.ResolveOrchestratorStrategyResponse{Strategy: strategy}, nil
}
