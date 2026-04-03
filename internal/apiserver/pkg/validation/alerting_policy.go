package validation

import (
	"context"

	"github.com/onexstack/onexstack/pkg/errorsx"
	genericvalidation "github.com/onexstack/onexstack/pkg/validation"

	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

const (
	maxAlertingPolicyNameLen        = 128
	maxAlertingPolicyDescriptionLen = 1024
	maxAlertingPolicyConfigLen      = 64 * 1024 // 64KB for config JSON
	maxAlertingPolicyOperatorLen    = 128

	defaultAlertingPolicyListLimit = int64(20)
	maxAlertingPolicyListLimit     = int64(200)
)

// ValidateAlertingPolicyRules returns a set of validation rules for alerting policy-related requests.
func (v *Validator) ValidateAlertingPolicyRules() genericvalidation.Rules {
	return genericvalidation.Rules{}
}

// ValidateCreateAlertingPolicyRequest validates the fields of a CreateAlertingPolicyRequest.
func (v *Validator) ValidateCreateAlertingPolicyRequest(ctx context.Context, rq *v1.CreateAlertingPolicyRequest) error {
	_ = ctx
	if !validateRequiredTrimmedMaxLen(rq.GetName(), maxAlertingPolicyNameLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.Description, maxAlertingPolicyDescriptionLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateRequiredTrimmedMaxLen(rq.GetConfigJSON(), maxAlertingPolicyConfigLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateGetAlertingPolicyRequest validates the fields of a GetAlertingPolicyRequest.
func (v *Validator) ValidateGetAlertingPolicyRequest(ctx context.Context, rq *v1.GetAlertingPolicyRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateAlertingPolicyRules())
}

// ValidateListAlertingPoliciesRequest validates the fields of a ListAlertingPoliciesRequest.
func (v *Validator) ValidateListAlertingPoliciesRequest(ctx context.Context, rq *v1.ListAlertingPoliciesRequest) error {
	_ = ctx
	if rq.GetLimit() <= 0 {
		rq.Limit = defaultAlertingPolicyListLimit
	}
	if rq.GetLimit() > maxAlertingPolicyListLimit {
		rq.Limit = maxAlertingPolicyListLimit
	}
	if !validateOptionalTrimmedMaxLen(rq.Name, maxAlertingPolicyNameLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateUpdateAlertingPolicyRequest validates the fields of an UpdateAlertingPolicyRequest.
func (v *Validator) ValidateUpdateAlertingPolicyRequest(ctx context.Context, rq *v1.UpdateAlertingPolicyRequest) error {
	_ = ctx
	if rq.GetId() <= 0 {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.Name, maxAlertingPolicyNameLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.Description, maxAlertingPolicyDescriptionLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.ConfigJSON, maxAlertingPolicyConfigLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateDeleteAlertingPolicyRequest validates the fields of a DeleteAlertingPolicyRequest.
func (v *Validator) ValidateDeleteAlertingPolicyRequest(ctx context.Context, rq *v1.DeleteAlertingPolicyRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateAlertingPolicyRules())
}

// ValidateActivateAlertingPolicyRequest validates the fields of an ActivateAlertingPolicyRequest.
func (v *Validator) ValidateActivateAlertingPolicyRequest(ctx context.Context, rq *v1.ActivateAlertingPolicyRequest) error {
	_ = ctx
	if rq.GetId() <= 0 {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.Operator, maxAlertingPolicyOperatorLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateDeactivateAlertingPolicyRequest validates the fields of a DeactivateAlertingPolicyRequest.
func (v *Validator) ValidateDeactivateAlertingPolicyRequest(ctx context.Context, rq *v1.DeactivateAlertingPolicyRequest) error {
	_ = ctx
	if rq.GetId() <= 0 {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateRollbackAlertingPolicyRequest validates the fields of a RollbackAlertingPolicyRequest.
func (v *Validator) ValidateRollbackAlertingPolicyRequest(ctx context.Context, rq *v1.RollbackAlertingPolicyRequest) error {
	_ = ctx
	if rq.GetId() <= 0 {
		return errorsx.ErrInvalidArgument
	}
	if rq.GetVersion() <= 0 {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.Operator, maxAlertingPolicyOperatorLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateGetActiveAlertingPolicyRequest validates the fields of a GetActiveAlertingPolicyRequest.
func (v *Validator) ValidateGetActiveAlertingPolicyRequest(ctx context.Context, rq *v1.GetActiveAlertingPolicyRequest) error {
	return nil
}