package validation

import (
	"context"
	"strings"

	"github.com/onexstack/onexstack/pkg/errorsx"

	"zk8s.com/rca-api/internal/apiserver/pkg/silenceutil"
	v1 "zk8s.com/rca-api/pkg/api/apiserver/v1"
)

const (
	defaultSilenceListLimit = int64(20)
	maxSilenceListLimit     = int64(200)
	maxSilenceReasonLength  = 1024
	maxSilenceCreatedBy     = 128
	maxSilenceNamespace     = 128
)

func (v *Validator) ValidateCreateSilenceRequest(ctx context.Context, rq *v1.CreateSilenceRequest) error {
	_ = ctx
	if rq == nil {
		return errorsx.ErrInvalidArgument
	}
	if rq.GetStartsAt() == nil || rq.GetEndsAt() == nil {
		return errorsx.ErrInvalidArgument
	}

	startsAt := rq.GetStartsAt().AsTime().UTC()
	endsAt := rq.GetEndsAt().AsTime().UTC()
	if !endsAt.After(startsAt) {
		return errorsx.ErrInvalidArgument
	}
	if len(strings.TrimSpace(rq.GetNamespace())) > maxSilenceNamespace {
		return errorsx.ErrInvalidArgument
	}
	if rq.Reason != nil && len(strings.TrimSpace(rq.GetReason())) > maxSilenceReasonLength {
		return errorsx.ErrInvalidArgument
	}
	if rq.CreatedBy != nil && len(strings.TrimSpace(rq.GetCreatedBy())) > maxSilenceCreatedBy {
		return errorsx.ErrInvalidArgument
	}
	return validateSilenceMatchers(rq.GetMatchers())
}

func (v *Validator) ValidateGetSilenceRequest(ctx context.Context, rq *v1.GetSilenceRequest) error {
	_ = ctx
	if rq == nil || strings.TrimSpace(rq.GetSilenceID()) == "" {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func (v *Validator) ValidateListSilencesRequest(ctx context.Context, rq *v1.ListSilencesRequest) error {
	_ = ctx
	if rq == nil {
		return errorsx.ErrInvalidArgument
	}
	if rq.GetOffset() < 0 {
		return errorsx.ErrInvalidArgument
	}
	if rq.GetLimit() <= 0 {
		rq.Limit = defaultSilenceListLimit
	}
	if rq.GetLimit() > maxSilenceListLimit {
		return errorsx.ErrInvalidArgument
	}
	if rq.Namespace != nil && len(strings.TrimSpace(rq.GetNamespace())) > maxSilenceNamespace {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func (v *Validator) ValidatePatchSilenceRequest(ctx context.Context, rq *v1.PatchSilenceRequest) error {
	_ = ctx
	if rq == nil || strings.TrimSpace(rq.GetSilenceID()) == "" {
		return errorsx.ErrInvalidArgument
	}
	if rq.Enabled == nil && rq.GetEndsAt() == nil && rq.Reason == nil {
		return errorsx.ErrInvalidArgument
	}
	if rq.Reason != nil && len(strings.TrimSpace(rq.GetReason())) > maxSilenceReasonLength {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func (v *Validator) ValidateDeleteSilenceRequest(ctx context.Context, rq *v1.DeleteSilenceRequest) error {
	_ = ctx
	if rq == nil || strings.TrimSpace(rq.GetSilenceID()) == "" {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func validateSilenceMatchers(matchers []*v1.SilenceMatcher) error {
	if len(matchers) == 0 {
		return errorsx.ErrInvalidArgument
	}
	for _, matcher := range matchers {
		if matcher == nil {
			return errorsx.ErrInvalidArgument
		}
		key := silenceutil.NormalizeMatcherKey(matcher.GetKey())
		op := silenceutil.NormalizeMatcherOp(matcher.GetOp())
		value := strings.TrimSpace(matcher.GetValue())
		if !silenceutil.IsAllowedMatcherKey(key) {
			return errorsx.ErrInvalidArgument
		}
		if op != silenceutil.MatcherOpEqual {
			return errorsx.ErrInvalidArgument
		}
		if value == "" {
			return errorsx.ErrInvalidArgument
		}
	}
	return nil
}
