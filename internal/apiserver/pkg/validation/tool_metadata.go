package validation

import (
	"context"

	"github.com/onexstack/onexstack/pkg/errorsx"
	genericvalidation "github.com/onexstack/onexstack/pkg/validation"

	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

const (
	maxToolMetadataNameLen        = 128
	maxToolMetadataDescriptionLen = 512
	maxToolMetadataTagsJSONLen    = 2048
	maxToolMetadataMcpServerIDLen = 64
	maxToolMetadataKindLen        = 32
	maxToolMetadataDomainLen      = 64
	maxToolMetadataRiskLevelLen   = 16
	maxToolMetadataLatencyTierLen = 16
	maxToolMetadataCostHintLen    = 16
	maxToolMetadataStatusLen      = 32

	defaultListLimit = int64(20)
	maxListLimit     = int64(200)
)

// ValidateToolMetadataRules returns a set of validation rules for tool metadata-related requests.
func (v *Validator) ValidateToolMetadataRules() genericvalidation.Rules {
	return genericvalidation.Rules{}
}

// ValidateCreateToolMetadataRequest validates the fields of a CreateToolMetadataRequest.
func (v *Validator) ValidateCreateToolMetadataRequest(ctx context.Context, rq *v1.CreateToolMetadataRequest) error {
	_ = ctx
	if !validateRequiredTrimmedMaxLen(rq.GetToolName(), maxToolMetadataNameLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.Kind, maxToolMetadataKindLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.Domain, maxToolMetadataDomainLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.RiskLevel, maxToolMetadataRiskLevelLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.LatencyTier, maxToolMetadataLatencyTierLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.CostHint, maxToolMetadataCostHintLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.TagsJSON, maxToolMetadataTagsJSONLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.Description, maxToolMetadataDescriptionLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.McpServerID, maxToolMetadataMcpServerIDLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateGetToolMetadataRequest validates the fields of a GetToolMetadataRequest.
func (v *Validator) ValidateGetToolMetadataRequest(ctx context.Context, rq *v1.GetToolMetadataRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateToolMetadataRules())
}

// ValidateListToolMetadataRequest validates the fields of a ListToolMetadataRequest.
func (v *Validator) ValidateListToolMetadataRequest(ctx context.Context, rq *v1.ListToolMetadataRequest) error {
	_ = ctx
	if rq.GetLimit() <= 0 {
		rq.Limit = defaultListLimit
	}
	if rq.GetLimit() > maxListLimit {
		rq.Limit = maxListLimit
	}
	if !validateOptionalTrimmedMaxLen(rq.Kind, maxToolMetadataKindLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.Domain, maxToolMetadataDomainLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.Status, maxToolMetadataStatusLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.McpServerID, maxToolMetadataMcpServerIDLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateUpdateToolMetadataRequest validates the fields of an UpdateToolMetadataRequest.
func (v *Validator) ValidateUpdateToolMetadataRequest(ctx context.Context, rq *v1.UpdateToolMetadataRequest) error {
	_ = ctx
	if !validateRequiredTrimmedMaxLen(rq.GetToolName(), maxToolMetadataNameLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.Kind, maxToolMetadataKindLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.Domain, maxToolMetadataDomainLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.RiskLevel, maxToolMetadataRiskLevelLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.LatencyTier, maxToolMetadataLatencyTierLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.CostHint, maxToolMetadataCostHintLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.TagsJSON, maxToolMetadataTagsJSONLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.Description, maxToolMetadataDescriptionLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.McpServerID, maxToolMetadataMcpServerIDLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.Status, maxToolMetadataStatusLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateDeleteToolMetadataRequest validates the fields of a DeleteToolMetadataRequest.
func (v *Validator) ValidateDeleteToolMetadataRequest(ctx context.Context, rq *v1.DeleteToolMetadataRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateToolMetadataRules())
}