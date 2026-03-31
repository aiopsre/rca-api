package validation

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/onexstack/onexstack/pkg/errorsx"
	genericvalidation "github.com/onexstack/onexstack/pkg/validation"

	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

const (
	maxToolsetProviderBindingToolsetNameLen      = 128
	maxToolsetProviderBindingMcpServerIDLen      = 64
	maxToolsetProviderBindingAllowedToolsJSONLen = 4096
	defaultToolsetProviderBindingListLimit       = int64(20)
	maxToolsetProviderBindingListLimit           = int64(200)
)

func (v *Validator) ValidateToolsetProviderBindingRules() genericvalidation.Rules {
	return genericvalidation.Rules{}
}

func (v *Validator) ValidateCreateToolsetProviderBindingRequest(ctx context.Context, rq *v1.CreateToolsetProviderBindingRequest) error {
	_ = ctx
	if rq == nil {
		return errorsx.ErrInvalidArgument
	}
	if !validateRequiredTrimmedMaxLen(rq.GetToolsetName(), maxToolsetProviderBindingToolsetNameLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateRequiredTrimmedMaxLen(rq.GetMcpServerID(), maxToolsetProviderBindingMcpServerIDLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.AllowedToolsJSON, maxToolsetProviderBindingAllowedToolsJSONLen) {
		return errorsx.ErrInvalidArgument
	}
	if rq.AllowedToolsJSON != nil {
		if err := validateJSONArrayString(*rq.AllowedToolsJSON); err != nil {
			return err
		}
	}
	return nil
}

func (v *Validator) ValidateGetToolsetProviderBindingRequest(ctx context.Context, rq *v1.GetToolsetProviderBindingRequest) error {
	_ = ctx
	if rq == nil {
		return errorsx.ErrInvalidArgument
	}
	if !validateRequiredTrimmedMaxLen(rq.GetToolsetName(), maxToolsetProviderBindingToolsetNameLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateRequiredTrimmedMaxLen(rq.GetMcpServerID(), maxToolsetProviderBindingMcpServerIDLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func (v *Validator) ValidateListToolsetProviderBindingsRequest(ctx context.Context, rq *v1.ListToolsetProviderBindingsRequest) error {
	_ = ctx
	if rq == nil {
		return errorsx.ErrInvalidArgument
	}
	if rq.GetOffset() < 0 {
		return errorsx.ErrInvalidArgument
	}
	if rq.GetLimit() <= 0 {
		rq.Limit = defaultToolsetProviderBindingListLimit
	}
	if rq.GetLimit() > maxToolsetProviderBindingListLimit {
		rq.Limit = maxToolsetProviderBindingListLimit
	}
	if !validateOptionalTrimmedMaxLen(rq.ToolsetName, maxToolsetProviderBindingToolsetNameLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.McpServerID, maxToolsetProviderBindingMcpServerIDLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func (v *Validator) ValidateUpdateToolsetProviderBindingRequest(ctx context.Context, rq *v1.UpdateToolsetProviderBindingRequest) error {
	_ = ctx
	if rq == nil {
		return errorsx.ErrInvalidArgument
	}
	if !validateRequiredTrimmedMaxLen(rq.GetToolsetName(), maxToolsetProviderBindingToolsetNameLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateRequiredTrimmedMaxLen(rq.GetMcpServerID(), maxToolsetProviderBindingMcpServerIDLen) {
		return errorsx.ErrInvalidArgument
	}
	if rq.AllowedToolsJSON == nil && rq.Priority == nil && rq.Enabled == nil {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalTrimmedMaxLen(rq.AllowedToolsJSON, maxToolsetProviderBindingAllowedToolsJSONLen) {
		return errorsx.ErrInvalidArgument
	}
	if rq.AllowedToolsJSON != nil {
		if err := validateJSONArrayString(*rq.AllowedToolsJSON); err != nil {
			return err
		}
	}
	return nil
}

func (v *Validator) ValidateDeleteToolsetProviderBindingRequest(ctx context.Context, rq *v1.DeleteToolsetProviderBindingRequest) error {
	_ = ctx
	if rq == nil {
		return errorsx.ErrInvalidArgument
	}
	if !validateRequiredTrimmedMaxLen(rq.GetToolsetName(), maxToolsetProviderBindingToolsetNameLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateRequiredTrimmedMaxLen(rq.GetMcpServerID(), maxToolsetProviderBindingMcpServerIDLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

func validateJSONArrayString(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return errorsx.ErrInvalidArgument
	}
	var values []string
	if err := json.Unmarshal([]byte(trimmed), &values); err != nil {
		return errorsx.ErrInvalidArgument
	}
	for _, v := range values {
		if strings.TrimSpace(v) == "" {
			return errorsx.ErrInvalidArgument
		}
	}
	return nil
}
