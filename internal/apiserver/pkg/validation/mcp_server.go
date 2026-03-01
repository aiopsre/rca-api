package validation

import (
	"context"
	"strings"

	"github.com/onexstack/onexstack/pkg/errorsx"
	genericvalidation "github.com/onexstack/onexstack/pkg/validation"

	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

const (
	maxMcpServerNameLen        = 128
	maxMcpServerDisplayNameLen = 256
	maxMcpServerDescriptionLen = 1024
	maxMcpServerBaseURLLen     = 512
	maxMcpServerAuthSecretLen  = 256
	maxMcpServerScopesLen      = 256
	maxMcpServerStatusLen      = 32
	defaultMcpServerListLimit  = int64(20)
	maxMcpServerListLimit      = int64(200)
)

// ValidateMcpServerRules returns a set of validation rules for mcp server-related requests.
func (v *Validator) ValidateMcpServerRules() genericvalidation.Rules {
	return genericvalidation.Rules{}
}

// ValidateCreateMcpServerRequest validates the fields of a CreateMcpServerRequest.
func (v *Validator) ValidateCreateMcpServerRequest(ctx context.Context, rq *v1.CreateMcpServerRequest) error {
	_ = ctx
	if !validateRequiredTrimmedMaxLen(rq.GetName(), maxMcpServerNameLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateRequiredTrimmedMaxLen(rq.GetBaseURL(), maxMcpServerBaseURLLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.DisplayName, maxMcpServerDisplayNameLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.Description, maxMcpServerDescriptionLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.AuthSecretRef, maxMcpServerAuthSecretLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.Scopes, maxMcpServerScopesLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateGetMcpServerRequest validates the fields of a GetMcpServerRequest.
func (v *Validator) ValidateGetMcpServerRequest(ctx context.Context, rq *v1.GetMcpServerRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateMcpServerRules())
}

// ValidateListMcpServersRequest validates the fields of a ListMcpServersRequest.
func (v *Validator) ValidateListMcpServersRequest(ctx context.Context, rq *v1.ListMcpServersRequest) error {
	_ = ctx
	if rq.GetLimit() <= 0 {
		rq.Limit = defaultMcpServerListLimit
	}
	if rq.GetLimit() > maxMcpServerListLimit {
		rq.Limit = maxMcpServerListLimit
	}
	if !validateOptionalTrimmedMaxLen(rq.Name, maxMcpServerNameLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.Status, maxMcpServerStatusLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateUpdateMcpServerRequest validates the fields of an UpdateMcpServerRequest.
func (v *Validator) ValidateUpdateMcpServerRequest(ctx context.Context, rq *v1.UpdateMcpServerRequest) error {
	_ = ctx
	if strings.TrimSpace(rq.GetMcpServerID()) == "" {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.DisplayName, maxMcpServerDisplayNameLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.Description, maxMcpServerDescriptionLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.BaseURL, maxMcpServerBaseURLLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.AuthSecretRef, maxMcpServerAuthSecretLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.Scopes, maxMcpServerScopesLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.Status, maxMcpServerStatusLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateDeleteMcpServerRequest validates the fields of a DeleteMcpServerRequest.
func (v *Validator) ValidateDeleteMcpServerRequest(ctx context.Context, rq *v1.DeleteMcpServerRequest) error {
	return genericvalidation.ValidateAllFields(rq, v.ValidateMcpServerRules())
}