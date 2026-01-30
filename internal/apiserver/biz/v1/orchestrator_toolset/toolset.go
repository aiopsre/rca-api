package orchestrator_toolset

//go:generate mockgen -destination mock_toolset.go -package orchestrator_toolset github.com/aiopsre/rca-api/internal/apiserver/biz/v1/orchestrator_toolset ToolsetBiz

import (
	"context"
	"errors"

	"github.com/aiopsre/rca-api/internal/apiserver/pkg/orchestratorcfg"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

// ToolsetBiz defines read-only orchestrator toolset resolve use-case.
type ToolsetBiz interface {
	Resolve(ctx context.Context, req *v1.ResolveToolsetRequest) (*v1.ResolveToolsetResponse, error)

	ToolsetExpansion
}

//nolint:modernize // Keep explicit placeholder for future extensions.
type ToolsetExpansion interface{}

type toolsetBiz struct{}

var _ ToolsetBiz = (*toolsetBiz)(nil)

// New creates orchestrator toolset biz.
func New() *toolsetBiz {
	return &toolsetBiz{}
}

func (b *toolsetBiz) Resolve(ctx context.Context, req *v1.ResolveToolsetRequest) (*v1.ResolveToolsetResponse, error) {
	_ = ctx
	if req == nil {
		return nil, errno.ErrInvalidArgument
	}

	normalizedPipeline := orchestratorcfg.NormalizePipeline(req.GetPipeline())
	toolsets, err := orchestratorcfg.ResolveChain(normalizedPipeline)
	if err != nil {
		switch {
		case errors.Is(err, orchestratorcfg.ErrToolsetNotFound):
			return nil, errno.ErrOrchestratorToolsetNotFound
		case errors.Is(err, orchestratorcfg.ErrInvalidConfig):
			return nil, errno.ErrOrchestratorToolsetConfigInvalid
		default:
			return nil, errno.ErrInternal
		}
	}
	if len(toolsets) == 0 {
		return nil, errno.ErrOrchestratorToolsetNotFound
	}

	return &v1.ResolveToolsetResponse{
		Pipeline: normalizedPipeline,
		Toolset:  toolsets[0],
		Toolsets: toolsets,
	}, nil
}
