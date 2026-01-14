package validation

import (
	"context"
	"strings"

	"github.com/onexstack/onexstack/pkg/errorsx"
	genericvalidation "github.com/onexstack/onexstack/pkg/validation"

	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

const (
	defaultOperatorListPage  = int64(1)
	defaultOperatorListLimit = int64(20)
	maxOperatorListLimit     = int64(200)

	maxOperatorActorLen      = 128
	maxOperatorActionTypeLen = 64
	maxOperatorSummaryLen    = 256
	maxOperatorSourceLen     = 64
	maxOperatorToolLen       = 128
	maxOperatorObservedLen   = 512
	maxOperatorPayloadInput  = 512 * 1024
)

// ValidateIncidentRules returns a set of validation rules for incident-related requests.
func (v *Validator) ValidateIncidentRules() genericvalidation.Rules {
	return genericvalidation.Rules{}
}

// ValidateCreateIncidentRequest validates the fields of a CreateIncidentRequest.
func (v *Validator) ValidateCreateIncidentRequest(ctx context.Context, rq *v1.CreateIncidentRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateIncidentRules())
}

// ValidateUpdateIncidentRequest validates the fields of an UpdateIncidentRequest.
func (v *Validator) ValidateUpdateIncidentRequest(ctx context.Context, rq *v1.UpdateIncidentRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateIncidentRules())
}

// ValidateDeleteIncidentRequest validates the fields of a DeleteIncidentRequest.
func (v *Validator) ValidateDeleteIncidentRequest(ctx context.Context, rq *v1.DeleteIncidentRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateIncidentRules())
}

//// ValidateDeleteIncidentsRequest validates the fields of a DeleteIncidentsRequest.
//func (v *Validator) ValidateDeleteIncidentsRequest(ctx context.Context, rq *v1.DeleteIncidentsRequest) error {
//	return genericvalidation.ValidateAllFields(rq, v.ValidateIncidentRules())
//}

// ValidateGetIncidentRequest validates the fields of a GetIncidentRequest.
func (v *Validator) ValidateGetIncidentRequest(ctx context.Context, rq *v1.GetIncidentRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateIncidentRules())
}

// ValidateListIncidentRequest validates the fields of a ListIncidentRequest, focusing on selected fields ("Offset" and "Limit").
func (v *Validator) ValidateListIncidentRequest(ctx context.Context, rq *v1.ListIncidentRequest) error {
	return genericvalidation.ValidateSelectedFields(rq, v.ValidateIncidentRules(), "Offset", "Limit")
}

func (v *Validator) ValidateCreateIncidentActionRequest(ctx context.Context, rq *v1.CreateIncidentActionRequest) error {
	_ = ctx
	if strings.TrimSpace(rq.GetIncidentID()) == "" {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.Actor, maxOperatorActorLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateRequiredTrimmedMaxLen(rq.GetActionType(), maxOperatorActionTypeLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateRequiredTrimmedMaxLen(rq.GetSummary(), maxOperatorSummaryLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.DetailsJSON, maxOperatorPayloadInput) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func (v *Validator) ValidateListIncidentActionsRequest(ctx context.Context, rq *v1.ListIncidentActionsRequest) error {
	_ = ctx
	if strings.TrimSpace(rq.GetIncidentID()) == "" {
		return errorsx.ErrInvalidArgument
	}
	if rq.GetPage() <= 0 {
		rq.Page = defaultOperatorListPage
	}
	if rq.GetLimit() <= 0 {
		rq.Limit = defaultOperatorListLimit
	}
	if rq.GetLimit() > maxOperatorListLimit {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func (v *Validator) ValidateCreateIncidentVerificationRunRequest(
	ctx context.Context,
	rq *v1.CreateIncidentVerificationRunRequest,
) error {

	_ = ctx

	validations := []bool{
		strings.TrimSpace(rq.GetIncidentID()) != "",
		validateOptionalTrimmedMaxLen(rq.Actor, maxOperatorActorLen),
		validateRequiredTrimmedMaxLen(rq.GetSource(), maxOperatorSourceLen),
		rq.GetStepIndex() >= 0,
		validateRequiredTrimmedMaxLen(rq.GetTool(), maxOperatorToolLen),
		validateRequiredTrimmedMaxLen(rq.GetObserved(), maxOperatorObservedLen),
		validateOptionalTrimmedMaxLen(rq.ParamsJSON, maxOperatorPayloadInput),
	}
	for _, ok := range validations {
		if ok {
			continue
		}
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func (v *Validator) ValidateListIncidentVerificationRunsRequest(
	ctx context.Context,
	rq *v1.ListIncidentVerificationRunsRequest,
) error {

	_ = ctx
	if strings.TrimSpace(rq.GetIncidentID()) == "" {
		return errorsx.ErrInvalidArgument
	}
	if rq.GetPage() <= 0 {
		rq.Page = defaultOperatorListPage
	}
	if rq.GetLimit() <= 0 {
		rq.Limit = defaultOperatorListLimit
	}
	if rq.GetLimit() > maxOperatorListLimit {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func validateOptionalTrimmedMaxLen(value *string, maxLen int) bool {
	if value == nil {
		return true
	}
	return len(strings.TrimSpace(*value)) <= maxLen
}

func validateRequiredTrimmedMaxLen(value string, maxLen int) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed != "" && len(trimmed) <= maxLen
}
