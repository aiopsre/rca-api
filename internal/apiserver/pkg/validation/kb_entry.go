package validation

import (
	"context"
	"strings"

	"github.com/onexstack/onexstack/pkg/errorsx"
	genericvalidation "github.com/onexstack/onexstack/pkg/validation"

	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

const (
	maxKBIDLen              = 64
	maxNamespaceLen         = 128
	maxServiceLen           = 256
	maxRootCauseTypeLen     = 64
	maxRootCauseSummaryLen  = 512
	maxPatternsJSONLen      = 4096
	maxPatternsHashLen      = 64
	maxEvidenceSignatureLen = 4096

	defaultKBEntryListLimit = int64(20)
	maxKBEntryListLimit     = int64(200)
)

// ValidateKBEntryRules returns a set of validation rules for KB entry-related requests.
func (v *Validator) ValidateKBEntryRules() genericvalidation.Rules {
	return genericvalidation.Rules{}
}

// ValidateCreateKBEntryRequest validates the fields of a CreateKBEntryRequest.
func (v *Validator) ValidateCreateKBEntryRequest(ctx context.Context, rq *v1.CreateKBEntryRequest) error {
	_ = ctx
	if !validateRequiredStringMaxLen(rq.GetNamespace(), maxNamespaceLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateRequiredStringMaxLen(rq.GetService(), maxServiceLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateRequiredStringMaxLen(rq.GetRootCauseType(), maxRootCauseTypeLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateRequiredStringMaxLen(rq.GetRootCauseSummary(), maxRootCauseSummaryLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateRequiredStringMaxLen(rq.GetPatternsJSON(), maxPatternsJSONLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalStringMaxLen(rq.GetKbID(), maxKBIDLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalStringMaxLen(rq.GetPatternsHash(), maxPatternsHashLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalStringMaxLen(rq.GetEvidenceSignatureJSON(), maxEvidenceSignatureLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateGetKBEntryRequest validates the fields of a GetKBEntryRequest.
func (v *Validator) ValidateGetKBEntryRequest(ctx context.Context, rq *v1.GetKBEntryRequest) error {
	_ = ctx
	if !validateRequiredStringMaxLen(rq.GetKbID(), maxKBIDLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateUpdateKBEntryRequest validates the fields of an UpdateKBEntryRequest.
func (v *Validator) ValidateUpdateKBEntryRequest(ctx context.Context, rq *v1.UpdateKBEntryRequest) error {
	_ = ctx
	if !validateRequiredStringMaxLen(rq.GetKbID(), maxKBIDLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalStringMaxLen(rq.GetRootCauseSummary(), maxRootCauseSummaryLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalStringMaxLen(rq.GetPatternsJSON(), maxPatternsJSONLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalStringMaxLen(rq.GetPatternsHash(), maxPatternsHashLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalStringMaxLen(rq.GetEvidenceSignatureJSON(), maxEvidenceSignatureLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateDeleteKBEntryRequest validates the fields of a DeleteKBEntryRequest.
func (v *Validator) ValidateDeleteKBEntryRequest(ctx context.Context, rq *v1.DeleteKBEntryRequest) error {
	_ = ctx
	if !validateRequiredStringMaxLen(rq.GetKbID(), maxKBIDLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateListKBEntriesRequest validates the fields of a ListKBEntriesRequest.
func (v *Validator) ValidateListKBEntriesRequest(ctx context.Context, rq *v1.ListKBEntriesRequest) error {
	_ = ctx
	if rq.GetLimit() <= 0 {
		rq.Limit = defaultKBEntryListLimit
	}
	if rq.GetLimit() > maxKBEntryListLimit {
		rq.Limit = maxKBEntryListLimit
	}
	if !validateOptionalStringMaxLen(rq.GetNamespace(), maxNamespaceLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalStringMaxLen(rq.GetService(), maxServiceLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalStringMaxLen(rq.GetRootCauseType(), maxRootCauseTypeLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// normalizeKBEntryString trims whitespace from a string.
func normalizeKBEntryString(s string) string {
	return strings.TrimSpace(s)
}