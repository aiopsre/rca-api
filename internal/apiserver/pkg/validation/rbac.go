package validation

import (
	"context"
	"strings"

	"github.com/onexstack/onexstack/pkg/errorsx"
	genericvalidation "github.com/onexstack/onexstack/pkg/validation"

	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

const (
	maxUserIDLen       = 64
	maxUsernameLen     = 128
	maxTeamIDLen       = 64
	maxPasswordLen     = 256
	maxStatusLen       = 32

	maxRoleIDLen         = 64
	maxRoleDisplayNameLen = 128
	maxRoleDescriptionLen = 1024

	maxPermissionIDLen   = 96
	maxResourceLen       = 255
	maxActionLen         = 64
	maxPermissionDescLen = 1024

	defaultRBACListLimit = int64(50)
	maxRBACListLimit     = int64(200)
)

// ValidateRBACRules returns a set of validation rules for RBAC-related requests.
func (v *Validator) ValidateRBACRules() genericvalidation.Rules {
	return genericvalidation.Rules{}
}

// validateRequiredString validates that a string is required and trimmed.
func validateRequiredString(value string) bool {
	return strings.TrimSpace(value) != ""
}

// validateOptionalStringMaxLen validates an optional string (as plain string) with max length.
func validateOptionalStringMaxLen(value string, maxLen int) bool {
	if value == "" {
		return true
	}
	return len(strings.TrimSpace(value)) <= maxLen
}

// validateRequiredStringMaxLen validates a required string with max length.
func validateRequiredStringMaxLen(value string, maxLen int) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed != "" && len(trimmed) <= maxLen
}

// ==================== User Validation ====================

// ValidateListUsersRequest validates the fields of a ListUsersRequest.
func (v *Validator) ValidateListUsersRequest(ctx context.Context, rq *v1.ListUsersRequest) error {
	_ = ctx
	if rq.GetLimit() <= 0 {
		rq.Limit = defaultRBACListLimit
	}
	if rq.GetLimit() > maxRBACListLimit {
		rq.Limit = maxRBACListLimit
	}
	return nil
}

// ValidateGetUserRequest validates the fields of a GetUserRequest.
func (v *Validator) ValidateGetUserRequest(ctx context.Context, rq *v1.GetUserRequest) error {
	_ = ctx
	if !validateRequiredStringMaxLen(rq.GetUserId(), maxUserIDLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateUpsertUserRequest validates the fields of an UpsertUserRequest.
func (v *Validator) ValidateUpsertUserRequest(ctx context.Context, rq *v1.UpsertUserRequest) error {
	_ = ctx
	if !validateRequiredStringMaxLen(rq.GetUserId(), maxUserIDLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalStringMaxLen(rq.Username, maxUsernameLen) {
		return errorsx.ErrInvalidArgument
	}
	if rq.Password != nil && !validateOptionalStringMaxLen(*rq.Password, maxPasswordLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalStringMaxLen(rq.TeamId, maxTeamIDLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalStringMaxLen(rq.Status, maxStatusLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateDeleteUserRequest validates the fields of a DeleteUserRequest.
func (v *Validator) ValidateDeleteUserRequest(ctx context.Context, rq *v1.DeleteUserRequest) error {
	_ = ctx
	if !validateRequiredStringMaxLen(rq.GetUserId(), maxUserIDLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateAssignUserRolesRequest validates the fields of an AssignUserRolesRequest.
func (v *Validator) ValidateAssignUserRolesRequest(ctx context.Context, rq *v1.AssignUserRolesRequest) error {
	_ = ctx
	if !validateRequiredStringMaxLen(rq.GetUserId(), maxUserIDLen) {
		return errorsx.ErrInvalidArgument
	}
	for _, roleID := range rq.GetRoleIds() {
		if !validateRequiredStringMaxLen(roleID, maxRoleIDLen) {
			return errorsx.ErrInvalidArgument
		}
	}
	return nil
}

// ValidateGetUserAccessRequest validates the fields of a GetUserAccessRequest.
func (v *Validator) ValidateGetUserAccessRequest(ctx context.Context, rq *v1.GetUserAccessRequest) error {
	_ = ctx
	if !validateRequiredStringMaxLen(rq.GetUserId(), maxUserIDLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ==================== Role Validation ====================

// ValidateListRolesRequest validates the fields of a ListRolesRequest.
func (v *Validator) ValidateListRolesRequest(ctx context.Context, rq *v1.ListRolesRequest) error {
	_ = ctx
	if rq.GetLimit() <= 0 {
		rq.Limit = defaultRBACListLimit
	}
	if rq.GetLimit() > maxRBACListLimit {
		rq.Limit = maxRBACListLimit
	}
	return nil
}

// ValidateGetRoleRequest validates the fields of a GetRoleRequest.
func (v *Validator) ValidateGetRoleRequest(ctx context.Context, rq *v1.GetRoleRequest) error {
	_ = ctx
	if !validateRequiredStringMaxLen(rq.GetRoleId(), maxRoleIDLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateUpsertRoleRequest validates the fields of an UpsertRoleRequest.
func (v *Validator) ValidateUpsertRoleRequest(ctx context.Context, rq *v1.UpsertRoleRequest) error {
	_ = ctx
	if !validateRequiredStringMaxLen(rq.GetRoleId(), maxRoleIDLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalStringMaxLen(rq.DisplayName, maxRoleDisplayNameLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalStringMaxLen(rq.Description, maxRoleDescriptionLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalStringMaxLen(rq.Status, maxStatusLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateDeleteRoleRequest validates the fields of a DeleteRoleRequest.
func (v *Validator) ValidateDeleteRoleRequest(ctx context.Context, rq *v1.DeleteRoleRequest) error {
	_ = ctx
	if !validateRequiredStringMaxLen(rq.GetRoleId(), maxRoleIDLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateAssignRolePermissionsRequest validates the fields of an AssignRolePermissionsRequest.
func (v *Validator) ValidateAssignRolePermissionsRequest(ctx context.Context, rq *v1.AssignRolePermissionsRequest) error {
	_ = ctx
	if !validateRequiredStringMaxLen(rq.GetRoleId(), maxRoleIDLen) {
		return errorsx.ErrInvalidArgument
	}
	for _, permissionID := range rq.GetPermissionIds() {
		if !validateRequiredStringMaxLen(permissionID, maxPermissionIDLen) {
			return errorsx.ErrInvalidArgument
		}
	}
	return nil
}

// ==================== Permission Validation ====================

// ValidateListPermissionsRequest validates the fields of a ListPermissionsRequest.
func (v *Validator) ValidateListPermissionsRequest(ctx context.Context, rq *v1.ListPermissionsRequest) error {
	_ = ctx
	if rq.GetLimit() <= 0 {
		rq.Limit = defaultRBACListLimit
	}
	if rq.GetLimit() > maxRBACListLimit {
		rq.Limit = maxRBACListLimit
	}
	return nil
}

// ValidateGetPermissionRequest validates the fields of a GetPermissionRequest.
func (v *Validator) ValidateGetPermissionRequest(ctx context.Context, rq *v1.GetPermissionRequest) error {
	_ = ctx
	if !validateRequiredStringMaxLen(rq.GetPermissionId(), maxPermissionIDLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateUpsertPermissionRequest validates the fields of an UpsertPermissionRequest.
func (v *Validator) ValidateUpsertPermissionRequest(ctx context.Context, rq *v1.UpsertPermissionRequest) error {
	_ = ctx
	if !validateRequiredStringMaxLen(rq.GetPermissionId(), maxPermissionIDLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateRequiredStringMaxLen(rq.GetResource(), maxResourceLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateRequiredStringMaxLen(rq.GetAction(), maxActionLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalStringMaxLen(rq.Description, maxPermissionDescLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalStringMaxLen(rq.Status, maxStatusLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ValidateDeletePermissionRequest validates the fields of a DeletePermissionRequest.
func (v *Validator) ValidateDeletePermissionRequest(ctx context.Context, rq *v1.DeletePermissionRequest) error {
	_ = ctx
	if !validateRequiredStringMaxLen(rq.GetPermissionId(), maxPermissionIDLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}

// ==================== Login Validation ====================

// ValidateResolveLoginProfileRequest validates the fields of a ResolveLoginProfileRequest.
func (v *Validator) ValidateResolveLoginProfileRequest(ctx context.Context, rq *v1.ResolveLoginProfileRequest) error {
	_ = ctx
	if !validateRequiredStringMaxLen(rq.GetUserId(), maxUserIDLen) {
		return errorsx.ErrInvalidArgument
	}
	if !validateOptionalStringMaxLen(rq.GetPassword(), maxPasswordLen) {
		return errorsx.ErrInvalidArgument
	}
	return nil
}