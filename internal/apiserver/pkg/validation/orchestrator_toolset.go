package validation

import (
	"context"

	"github.com/onexstack/onexstack/pkg/errorsx"

	"github.com/aiopsre/rca-api/internal/apiserver/pkg/orchestratorcfg"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

func (v *Validator) ValidateResolveToolsetRequest(ctx context.Context, req *v1.ResolveToolsetRequest) error {
	_ = ctx
	if req == nil {
		return errorsx.ErrInvalidArgument
	}
	normalized := orchestratorcfg.NormalizePipeline(req.GetPipeline())
	req.Pipeline = &normalized
	return nil
}
