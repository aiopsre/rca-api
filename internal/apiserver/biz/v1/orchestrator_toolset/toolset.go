package orchestrator_toolset

//go:generate mockgen -destination mock_toolset.go -package orchestrator_toolset github.com/aiopsre/rca-api/internal/apiserver/biz/v1/orchestrator_toolset ToolsetBiz

import (
	"context"
	"strings"

	internalstrategyconfig "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/internal_strategy_config"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/orchestratorcfg"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
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

type toolsetBiz struct {
	configBiz internalstrategyconfig.ConfigBiz
}

var _ ToolsetBiz = (*toolsetBiz)(nil)

// New creates orchestrator toolset biz.
func New(store store.IStore) *toolsetBiz {
	return &toolsetBiz{
		configBiz: internalstrategyconfig.New(store),
	}
}

func (b *toolsetBiz) Resolve(ctx context.Context, req *v1.ResolveToolsetRequest) (*v1.ResolveToolsetResponse, error) {
	_ = ctx
	if req == nil {
		return nil, errno.ErrInvalidArgument
	}

	normalizedPipeline := orchestratorcfg.NormalizePipeline(req.GetPipeline())
	if b != nil && b.configBiz != nil {
		dynamicItems, _, dynamicErr := b.configBiz.ResolveToolsetByPipeline(ctx, normalizedPipeline)
		if dynamicErr == nil && len(dynamicItems) > 0 {
			resolved := mapDynamicToolsets(dynamicItems)
			if len(resolved) > 0 {
				return &v1.ResolveToolsetResponse{
					Pipeline: normalizedPipeline,
					Toolset:  resolved[0],
					Toolsets: resolved,
				}, nil
			}
		}
	}
	// Env-based fallback has been deprecated. Use toolset_config_dynamics table instead.
	// See docs/tooling/tool-registry.md for configuration guidance.
	return nil, errno.ErrOrchestratorToolsetNotFound
}

func mapDynamicToolsets(items []*internalstrategyconfig.ToolsetItem) []*v1.OrchestratorToolset {
	if len(items) == 0 {
		return nil
	}
	out := make([]*v1.OrchestratorToolset, 0, len(items))
	for _, item := range items {
		if item == nil || strings.TrimSpace(item.ToolsetName) == "" || len(item.AllowedTools) == 0 {
			continue
		}
		name := strings.TrimSpace(item.ToolsetName)
		out = append(out, &v1.OrchestratorToolset{
			ToolsetID: name,
			Providers: []*v1.OrchestratorToolsetProvider{{
				Type:       "mcp_http",
				Name:       &name,
				AllowTools: append([]string(nil), item.AllowedTools...),
			}},
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
