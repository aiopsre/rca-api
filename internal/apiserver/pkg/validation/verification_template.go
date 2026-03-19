package validation

import (
	"context"

	"github.com/onexstack/onexstack/pkg/errorsx"
	genericvalidation "github.com/onexstack/onexstack/pkg/validation"

	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

const (
	maxVerificationTemplateNameLen        = 128
	maxVerificationTemplateDescriptionLen = 1024
	maxVerificationTemplateMatchLen       = 16 * 1024  // 16KB for match JSON
	maxVerificationTemplateStepsLen       = 64 * 1024  // 64KB for steps JSON
	maxVerificationTemplateOperatorLen    = 128

	defaultVerificationTemplateListLimit = int64(20)
	maxVerificationTemplateListLimit     = int64(200)
)

// ValidateVerificationTemplateRules returns a set of validation rules for verification template-related requests.
func (v *Validator) ValidateVerificationTemplateRules() genericvalidation.Rules {
	return genericvalidation.Rules{}
}

// ValidateCreateVerificationTemplateRequest validates the fields of a CreateVerificationTemplateRequest.
func (v *Validator) ValidateCreateVerificationTemplateRequest(ctx context.Context, rq *v1.CreateVerificationTemplateRequest) error {
	_ = ctx
	if !validateRequiredTrimmedMaxLen(rq.GetName(), maxVerificationTemplateNameLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.Description, maxVerificationTemplateDescriptionLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateRequiredTrimmedMaxLen(rq.GetMatchJSON(), maxVerificationTemplateMatchLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateRequiredTrimmedMaxLen(rq.GetStepsJSON(), maxVerificationTemplateStepsLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateGetVerificationTemplateRequest validates the fields of a GetVerificationTemplateRequest.
func (v *Validator) ValidateGetVerificationTemplateRequest(ctx context.Context, rq *v1.GetVerificationTemplateRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateVerificationTemplateRules())
}

// ValidateListVerificationTemplatesRequest validates the fields of a ListVerificationTemplatesRequest.
func (v *Validator) ValidateListVerificationTemplatesRequest(ctx context.Context, rq *v1.ListVerificationTemplatesRequest) error {
	_ = ctx
	if rq.GetLimit() <= 0 {
		rq.Limit = defaultVerificationTemplateListLimit
	}
	if rq.GetLimit() > maxVerificationTemplateListLimit {
		rq.Limit = maxVerificationTemplateListLimit
	}
	if !validateOptionalTrimmedMaxLen(rq.Name, maxVerificationTemplateNameLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateUpdateVerificationTemplateRequest validates the fields of an UpdateVerificationTemplateRequest.
func (v *Validator) ValidateUpdateVerificationTemplateRequest(ctx context.Context, rq *v1.UpdateVerificationTemplateRequest) error {
	_ = ctx
	if rq.GetId() <= 0 {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.Name, maxVerificationTemplateNameLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.Description, maxVerificationTemplateDescriptionLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.MatchJSON, maxVerificationTemplateMatchLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.StepsJSON, maxVerificationTemplateStepsLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateDeleteVerificationTemplateRequest validates the fields of a DeleteVerificationTemplateRequest.
func (v *Validator) ValidateDeleteVerificationTemplateRequest(ctx context.Context, rq *v1.DeleteVerificationTemplateRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateVerificationTemplateRules())
}

// ValidateActivateVerificationTemplateRequest validates the fields of an ActivateVerificationTemplateRequest.
func (v *Validator) ValidateActivateVerificationTemplateRequest(ctx context.Context, rq *v1.ActivateVerificationTemplateRequest) error {
	_ = ctx
	if rq.GetId() <= 0 {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.Operator, maxVerificationTemplateOperatorLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateDeactivateVerificationTemplateRequest validates the fields of a DeactivateVerificationTemplateRequest.
func (v *Validator) ValidateDeactivateVerificationTemplateRequest(ctx context.Context, rq *v1.DeactivateVerificationTemplateRequest) error {
	_ = ctx
	if rq.GetId() <= 0 {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateGetActiveVerificationTemplateRequest validates the fields of a GetActiveVerificationTemplateRequest.
func (v *Validator) ValidateGetActiveVerificationTemplateRequest(ctx context.Context, rq *v1.GetActiveVerificationTemplateRequest) error {
	return nil
}