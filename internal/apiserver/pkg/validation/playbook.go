package validation

import (
	"context"

	"github.com/onexstack/onexstack/pkg/errorsx"
	genericvalidation "github.com/onexstack/onexstack/pkg/validation"

	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

const (
	maxPlaybookNameLen        = 128
	maxPlaybookDescriptionLen = 1024
	maxPlaybookConfigLen      = 64 * 1024 // 64KB for config JSON
	maxPlaybookOperatorLen    = 128

	defaultPlaybookListLimit = int64(20)
	maxPlaybookListLimit     = int64(200)
)

// ValidatePlaybookRules returns a set of validation rules for playbook-related requests.
func (v *Validator) ValidatePlaybookRules() genericvalidation.Rules {
	return genericvalidation.Rules{}
}

// ValidateCreatePlaybookRequest validates the fields of a CreatePlaybookRequest.
func (v *Validator) ValidateCreatePlaybookRequest(ctx context.Context, rq *v1.CreatePlaybookRequest) error {
	_ = ctx
	if !validateRequiredTrimmedMaxLen(rq.GetName(), maxPlaybookNameLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.Description, maxPlaybookDescriptionLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateRequiredTrimmedMaxLen(rq.GetConfigJSON(), maxPlaybookConfigLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateGetPlaybookRequest validates the fields of a GetPlaybookRequest.
func (v *Validator) ValidateGetPlaybookRequest(ctx context.Context, rq *v1.GetPlaybookRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidatePlaybookRules())
}

// ValidateListPlaybooksRequest validates the fields of a ListPlaybooksRequest.
func (v *Validator) ValidateListPlaybooksRequest(ctx context.Context, rq *v1.ListPlaybooksRequest) error {
	_ = ctx
	if rq.GetLimit() <= 0 {
		rq.Limit = defaultPlaybookListLimit
	}
	if rq.GetLimit() > maxPlaybookListLimit {
		rq.Limit = maxPlaybookListLimit
	}
	if !validateOptionalTrimmedMaxLen(rq.Name, maxPlaybookNameLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateUpdatePlaybookRequest validates the fields of an UpdatePlaybookRequest.
func (v *Validator) ValidateUpdatePlaybookRequest(ctx context.Context, rq *v1.UpdatePlaybookRequest) error {
	_ = ctx
	if rq.GetId() <= 0 {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.Name, maxPlaybookNameLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.Description, maxPlaybookDescriptionLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.ConfigJSON, maxPlaybookConfigLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateDeletePlaybookRequest validates the fields of a DeletePlaybookRequest.
func (v *Validator) ValidateDeletePlaybookRequest(ctx context.Context, rq *v1.DeletePlaybookRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidatePlaybookRules())
}

// ValidateActivatePlaybookRequest validates the fields of an ActivatePlaybookRequest.
func (v *Validator) ValidateActivatePlaybookRequest(ctx context.Context, rq *v1.ActivatePlaybookRequest) error {
	_ = ctx
	if rq.GetId() <= 0 {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.Operator, maxPlaybookOperatorLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateDeactivatePlaybookRequest validates the fields of a DeactivatePlaybookRequest.
func (v *Validator) ValidateDeactivatePlaybookRequest(ctx context.Context, rq *v1.DeactivatePlaybookRequest) error {
	_ = ctx
	if rq.GetId() <= 0 {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateRollbackPlaybookRequest validates the fields of a RollbackPlaybookRequest.
func (v *Validator) ValidateRollbackPlaybookRequest(ctx context.Context, rq *v1.RollbackPlaybookRequest) error {
	_ = ctx
	if rq.GetId() <= 0 {
		return errorsx.ErrInvalidArgument
	}
	if rq.GetVersion() <= 0 {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.Operator, maxPlaybookOperatorLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateGetActivePlaybookRequest validates the fields of a GetActivePlaybookRequest.
func (v *Validator) ValidateGetActivePlaybookRequest(ctx context.Context, rq *v1.GetActivePlaybookRequest) error {
	return nil
}