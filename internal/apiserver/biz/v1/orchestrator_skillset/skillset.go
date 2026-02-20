package orchestrator_skillset

//go:generate mockgen -destination mock_skillset.go -package orchestrator_skillset github.com/aiopsre/rca-api/internal/apiserver/biz/v1/orchestrator_skillset SkillsetBiz

import (
	"context"
	"errors"
	"strings"

	internalstrategyconfig "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/internal_strategy_config"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/orchestratorcfg"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/skillartifact"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
	"gorm.io/gorm"
)

// SkillsetBiz defines read-only orchestrator skillset resolve use-case.
type SkillsetBiz interface {
	Resolve(ctx context.Context, req *v1.ResolveOrchestratorSkillsetsRequest) (*v1.ResolveOrchestratorSkillsetsResponse, error)

	SkillsetExpansion
}

//nolint:modernize // Keep explicit placeholder for future extensions.
type SkillsetExpansion interface{}

type skillsetBiz struct {
	store     store.IStore
	configBiz internalstrategyconfig.ConfigBiz
}

var _ SkillsetBiz = (*skillsetBiz)(nil)

// New creates orchestrator skillset biz.
func New(store store.IStore) *skillsetBiz {
	return &skillsetBiz{
		store:     store,
		configBiz: internalstrategyconfig.New(store),
	}
}

func (b *skillsetBiz) Resolve(
	ctx context.Context,
	req *v1.ResolveOrchestratorSkillsetsRequest,
) (*v1.ResolveOrchestratorSkillsetsResponse, error) {
	if req == nil {
		return nil, errno.ErrInvalidArgument
	}
	normalizedPipeline := orchestratorcfg.NormalizePipeline(req.GetPipeline())
	items, _, err := b.configBiz.ResolveSkillsetByPipeline(ctx, normalizedPipeline)
	if err != nil {
		if errors.Is(err, errno.ErrNotFound) {
			return &v1.ResolveOrchestratorSkillsetsResponse{
				Pipeline:  normalizedPipeline,
				Skillsets: []*v1.OrchestratorSkillset{},
			}, nil
		}
		return nil, errno.ErrOrchestratorSkillsetConfigInvalid
	}
	out := make([]*v1.OrchestratorSkillset, 0, len(items))
	for _, item := range items {
		if item == nil || strings.TrimSpace(item.SkillsetName) == "" {
			continue
		}
		resolvedSkills := make([]*v1.OrchestratorSkillRelease, 0, len(item.Skills))
		for _, ref := range item.Skills {
			if ref == nil {
				continue
			}
			release, getErr := b.store.InternalStrategyConfig().GetSkillRelease(ctx, ref.SkillID, ref.Version)
			if getErr != nil {
				if errors.Is(getErr, gorm.ErrRecordNotFound) {
					return nil, errno.ErrOrchestratorSkillsetConfigInvalid
				}
				return nil, errno.ErrInternal
			}
			downloadURL, resolveErr := skillartifact.ResolveDownloadURL(ctx, strings.TrimSpace(release.ArtifactURL))
			if resolveErr != nil {
				return nil, errno.ErrInternal
			}
			resolvedSkills = append(resolvedSkills, &v1.OrchestratorSkillRelease{
				SkillID:      strings.TrimSpace(release.SkillID),
				Version:      strings.TrimSpace(release.Version),
				BundleDigest: strings.TrimSpace(release.BundleDigest),
				ArtifactURL:  downloadURL,
				ManifestJSON: cloneOptionalTrimmedString(release.ManifestJSON),
				Status:       strings.TrimSpace(release.Status),
			})
		}
		out = append(out, &v1.OrchestratorSkillset{
			SkillsetID: strings.TrimSpace(item.SkillsetName),
			Skills:     resolvedSkills,
		})
	}
	return &v1.ResolveOrchestratorSkillsetsResponse{
		Pipeline:  normalizedPipeline,
		Skillsets: out,
	}, nil
}

func cloneOptionalTrimmedString(in *string) *string {
	if in == nil {
		return nil
	}
	value := strings.TrimSpace(*in)
	return &value
}
