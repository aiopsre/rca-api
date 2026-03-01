package rbac

//go:generate mockgen -destination mock_rbac.go -package rbac github.com/aiopsre/rca-api/internal/apiserver/biz/v1/rbac RBACBiz

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/casbin/casbin/v2"
	casbinmodel "github.com/casbin/casbin/v2/model"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
)

const (
	envRBACModelPath     = "RCA_API_RBAC_MODEL_PATH"
	defaultRBACModelPath = "configs/auth/model.conf"
	rbacStatusActive     = "active"
	rbacStatusDisabled   = "disabled"
)

const fallbackRBACModelConf = `
[request_definition]
r = sub, obj, act

[policy_definition]
p = sub, obj, act

[role_definition]
g = _, _

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = g(r.sub, p.sub) && keyMatch2(r.obj, p.obj) && (p.act == "*" || r.act == p.act)
`

type RBACBiz interface {
	Enforce(ctx context.Context, userID string, resource string, action string) (bool, error)
	Reload(ctx context.Context) error

	ListUsers(ctx context.Context, rq *v1.ListUsersRequest) (*v1.ListUsersResponse, error)
	GetUser(ctx context.Context, rq *v1.GetUserRequest) (*v1.GetUserResponse, error)
	UpsertUser(ctx context.Context, rq *v1.UpsertUserRequest) (*v1.UpsertUserResponse, error)
	DeleteUser(ctx context.Context, rq *v1.DeleteUserRequest) (*v1.DeleteUserResponse, error)

	ListRoles(ctx context.Context, rq *v1.ListRolesRequest) (*v1.ListRolesResponse, error)
	GetRole(ctx context.Context, rq *v1.GetRoleRequest) (*v1.GetRoleResponse, error)
	UpsertRole(ctx context.Context, rq *v1.UpsertRoleRequest) (*v1.UpsertRoleResponse, error)
	DeleteRole(ctx context.Context, rq *v1.DeleteRoleRequest) (*v1.DeleteRoleResponse, error)

	ListPermissions(ctx context.Context, rq *v1.ListPermissionsRequest) (*v1.ListPermissionsResponse, error)
	GetPermission(ctx context.Context, rq *v1.GetPermissionRequest) (*v1.GetPermissionResponse, error)
	UpsertPermission(ctx context.Context, rq *v1.UpsertPermissionRequest) (*v1.UpsertPermissionResponse, error)
	DeletePermission(ctx context.Context, rq *v1.DeletePermissionRequest) (*v1.DeletePermissionResponse, error)

	AssignUserRoles(ctx context.Context, rq *v1.AssignUserRolesRequest) (*v1.AssignUserRolesResponse, error)
	AssignRolePermissions(ctx context.Context, rq *v1.AssignRolePermissionsRequest) (*v1.AssignRolePermissionsResponse, error)
	GetUserAccess(ctx context.Context, rq *v1.GetUserAccessRequest) (*v1.GetUserAccessResponse, error)
	ResolveLoginProfile(ctx context.Context, rq *v1.ResolveLoginProfileRequest) (*v1.ResolveLoginProfileResponse, error)

	RBACExpansion
}

//nolint:modernize // Keep explicit placeholder for future extensions.
type RBACExpansion interface{}

type rbacBiz struct {
	store     store.IStore
	modelPath string

	enforcerMu    sync.RWMutex
	enforcer      *casbin.Enforcer
	policyEnabled bool
}

var _ RBACBiz = (*rbacBiz)(nil)

func New(s store.IStore) *rbacBiz {
	return &rbacBiz{
		store:     s,
		modelPath: resolveRBACModelPath(),
	}
}

func (b *rbacBiz) Enforce(ctx context.Context, userID string, resource string, action string) (bool, error) {
	if b == nil || b.store == nil {
		return true, nil
	}
	subject := strings.TrimSpace(userID)
	if subject == "" {
		return false, errno.ErrPermissionDenied
	}
	obj := strings.TrimSpace(resource)
	if obj == "" {
		return false, errno.ErrInvalidArgument
	}
	act := strings.TrimSpace(action)
	if act == "" {
		return false, errno.ErrInvalidArgument
	}
	if err := b.ensureEnforcer(ctx); err != nil {
		return false, err
	}

	b.enforcerMu.RLock()
	e := b.enforcer
	enabled := b.policyEnabled
	b.enforcerMu.RUnlock()
	if e == nil {
		return true, nil
	}
	if !enabled {
		// Keep backward compatibility when RBAC policy has not been configured.
		return true, nil
	}
	allowed, err := e.Enforce(subject, obj, act)
	if err != nil {
		return false, errno.ErrInternal
	}
	return allowed, nil
}

func (b *rbacBiz) Reload(ctx context.Context) error {
	if b == nil || b.store == nil {
		return nil
	}
	if err := b.ensureEnforcer(ctx); err != nil {
		return err
	}
	b.enforcerMu.Lock()
	defer b.enforcerMu.Unlock()
	return b.reloadLocked(ctx)
}

func (b *rbacBiz) ensureEnforcer(ctx context.Context) error {
	b.enforcerMu.RLock()
	if b.enforcer != nil {
		b.enforcerMu.RUnlock()
		return nil
	}
	b.enforcerMu.RUnlock()

	b.enforcerMu.Lock()
	defer b.enforcerMu.Unlock()
	if b.enforcer != nil {
		return nil
	}
	m, err := loadRBACModel(b.modelPath)
	if err != nil {
		return errno.ErrInternal
	}
	e, err := casbin.NewEnforcer(m)
	if err != nil {
		return errno.ErrInternal
	}
	b.enforcer = e
	if err := b.reloadLocked(ctx); err != nil {
		return err
	}
	return nil
}

func (b *rbacBiz) reloadLocked(ctx context.Context) error {
	if b.enforcer == nil {
		return errno.ErrInternal
	}
	b.enforcer.ClearPolicy()

	policyRows, err := b.store.RBAC().ListPolicyRows(ctx)
	if err != nil {
		if isTableMissingError(err) {
			b.policyEnabled = false
			return nil
		}
		return errno.ErrInternal
	}
	groupingRows, err := b.store.RBAC().ListGroupingRows(ctx)
	if err != nil {
		if isTableMissingError(err) {
			b.policyEnabled = false
			return nil
		}
		return errno.ErrInternal
	}

	for _, item := range policyRows {
		if item == nil {
			continue
		}
		if _, addErr := b.enforcer.AddPolicy(
			strings.TrimSpace(item.RoleID),
			strings.TrimSpace(item.Resource),
			strings.TrimSpace(item.Action),
		); addErr != nil {
			return errno.ErrInternal
		}
	}
	for _, item := range groupingRows {
		if item == nil {
			continue
		}
		if _, addErr := b.enforcer.AddGroupingPolicy(
			strings.TrimSpace(item.UserID),
			strings.TrimSpace(item.RoleID),
		); addErr != nil {
			return errno.ErrInternal
		}
	}
	b.policyEnabled = len(policyRows) > 0 || len(groupingRows) > 0
	return nil
}

func (b *rbacBiz) ListUsers(ctx context.Context, rq *v1.ListUsersRequest) (*v1.ListUsersResponse, error) {
	if b == nil || b.store == nil {
		return nil, errno.ErrInvalidArgument
	}
	if rq == nil {
		rq = &v1.ListUsersRequest{}
	}
	total, list, err := b.store.RBAC().ListUsers(ctx, int(rq.GetOffset()), int(rq.GetLimit()))
	if err != nil {
		if isTableMissingError(err) {
			return &v1.ListUsersResponse{Total: 0, Items: []*v1.User{}}, nil
		}
		return nil, errno.ErrInternal
	}
	items := make([]*v1.User, 0, len(list))
	for _, item := range list {
		items = append(items, modelToProtoUser(item))
	}
	return &v1.ListUsersResponse{Total: total, Items: items}, nil
}

func (b *rbacBiz) GetUser(ctx context.Context, rq *v1.GetUserRequest) (*v1.GetUserResponse, error) {
	if b == nil || b.store == nil || rq == nil {
		return nil, errno.ErrInvalidArgument
	}
	obj, err := b.store.RBAC().GetUser(ctx, strings.TrimSpace(rq.GetUserId()))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrNotFound
		}
		if isTableMissingError(err) {
			return nil, errno.ErrNotFound
		}
		return nil, errno.ErrInternal
	}
	return &v1.GetUserResponse{User: modelToProtoUser(obj)}, nil
}

func (b *rbacBiz) UpsertUser(ctx context.Context, rq *v1.UpsertUserRequest) (*v1.UpsertUserResponse, error) {
	if b == nil || b.store == nil || rq == nil {
		return nil, errno.ErrInvalidArgument
	}
	userID := strings.TrimSpace(rq.GetUserId())
	if userID == "" {
		return nil, errno.ErrInvalidArgument
	}
	status := normalizeStatus(rq.GetStatus())
	obj := &model.RBACUserM{
		UserID:   userID,
		Username: strings.TrimSpace(rq.GetUsername()),
		TeamID:   strings.TrimSpace(rq.GetTeamId()),
		Status:   status,
	}
	if rq.Password != nil {
		rawPassword := strings.TrimSpace(*rq.Password)
		if rawPassword != "" {
			hash, hashErr := bcrypt.GenerateFromPassword([]byte(rawPassword), bcrypt.DefaultCost)
			if hashErr != nil {
				return nil, errno.ErrInternal
			}
			hashString := string(hash)
			obj.PasswordHash = &hashString
		}
	}
	if obj.PasswordHash == nil {
		existing, getErr := b.store.RBAC().GetUser(ctx, userID)
		switch {
		case getErr == nil && existing != nil:
			obj.PasswordHash = existing.PasswordHash
		case getErr != nil && !errors.Is(getErr, gorm.ErrRecordNotFound) && !isTableMissingError(getErr):
			return nil, errno.ErrInternal
		}
	}
	if err := b.store.RBAC().UpsertUser(ctx, obj); err != nil {
		return nil, errno.ErrInternal
	}
	out, err := b.store.RBAC().GetUser(ctx, userID)
	if err != nil {
		return nil, errno.ErrInternal
	}
	if reloadErr := b.Reload(ctx); reloadErr != nil {
		return nil, reloadErr
	}
	return &v1.UpsertUserResponse{User: modelToProtoUser(out)}, nil
}

func (b *rbacBiz) DeleteUser(ctx context.Context, rq *v1.DeleteUserRequest) (*v1.DeleteUserResponse, error) {
	if b == nil || b.store == nil || rq == nil {
		return nil, errno.ErrInvalidArgument
	}
	if err := b.store.RBAC().DeleteUser(ctx, strings.TrimSpace(rq.GetUserId())); err != nil {
		return nil, errno.ErrInternal
	}
	if err := b.Reload(ctx); err != nil {
		return nil, err
	}
	return &v1.DeleteUserResponse{}, nil
}

func (b *rbacBiz) ListRoles(ctx context.Context, rq *v1.ListRolesRequest) (*v1.ListRolesResponse, error) {
	if b == nil || b.store == nil {
		return nil, errno.ErrInvalidArgument
	}
	if rq == nil {
		rq = &v1.ListRolesRequest{}
	}
	total, list, err := b.store.RBAC().ListRoles(ctx, int(rq.GetOffset()), int(rq.GetLimit()))
	if err != nil {
		if isTableMissingError(err) {
			return &v1.ListRolesResponse{Total: 0, Items: []*v1.Role{}}, nil
		}
		return nil, errno.ErrInternal
	}
	items := make([]*v1.Role, 0, len(list))
	for _, item := range list {
		items = append(items, modelToProtoRole(item))
	}
	return &v1.ListRolesResponse{Total: total, Items: items}, nil
}

func (b *rbacBiz) GetRole(ctx context.Context, rq *v1.GetRoleRequest) (*v1.GetRoleResponse, error) {
	if b == nil || b.store == nil || rq == nil {
		return nil, errno.ErrInvalidArgument
	}
	obj, err := b.store.RBAC().GetRole(ctx, strings.TrimSpace(rq.GetRoleId()))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrNotFound
		}
		if isTableMissingError(err) {
			return nil, errno.ErrNotFound
		}
		return nil, errno.ErrInternal
	}
	return &v1.GetRoleResponse{Role: modelToProtoRole(obj)}, nil
}

func (b *rbacBiz) UpsertRole(ctx context.Context, rq *v1.UpsertRoleRequest) (*v1.UpsertRoleResponse, error) {
	if b == nil || b.store == nil || rq == nil {
		return nil, errno.ErrInvalidArgument
	}
	roleID := strings.TrimSpace(rq.GetRoleId())
	if roleID == "" {
		return nil, errno.ErrInvalidArgument
	}
	obj := &model.RBACRoleM{
		RoleID:      roleID,
		DisplayName: strings.TrimSpace(rq.GetDisplayName()),
		Description: strings.TrimSpace(rq.GetDescription()),
		Status:      normalizeStatus(rq.GetStatus()),
	}
	if err := b.store.RBAC().UpsertRole(ctx, obj); err != nil {
		return nil, errno.ErrInternal
	}
	out, err := b.store.RBAC().GetRole(ctx, roleID)
	if err != nil {
		return nil, errno.ErrInternal
	}
	if reloadErr := b.Reload(ctx); reloadErr != nil {
		return nil, reloadErr
	}
	return &v1.UpsertRoleResponse{Role: modelToProtoRole(out)}, nil
}

func (b *rbacBiz) DeleteRole(ctx context.Context, rq *v1.DeleteRoleRequest) (*v1.DeleteRoleResponse, error) {
	if b == nil || b.store == nil || rq == nil {
		return nil, errno.ErrInvalidArgument
	}
	if err := b.store.RBAC().DeleteRole(ctx, strings.TrimSpace(rq.GetRoleId())); err != nil {
		return nil, errno.ErrInternal
	}
	if err := b.Reload(ctx); err != nil {
		return nil, err
	}
	return &v1.DeleteRoleResponse{}, nil
}

func (b *rbacBiz) ListPermissions(ctx context.Context, rq *v1.ListPermissionsRequest) (*v1.ListPermissionsResponse, error) {
	if b == nil || b.store == nil {
		return nil, errno.ErrInvalidArgument
	}
	if rq == nil {
		rq = &v1.ListPermissionsRequest{}
	}
	total, list, err := b.store.RBAC().ListPermissions(ctx, int(rq.GetOffset()), int(rq.GetLimit()))
	if err != nil {
		if isTableMissingError(err) {
			return &v1.ListPermissionsResponse{Total: 0, Items: []*v1.Permission{}}, nil
		}
		return nil, errno.ErrInternal
	}
	items := make([]*v1.Permission, 0, len(list))
	for _, item := range list {
		items = append(items, modelToProtoPermission(item))
	}
	return &v1.ListPermissionsResponse{Total: total, Items: items}, nil
}

func (b *rbacBiz) GetPermission(ctx context.Context, rq *v1.GetPermissionRequest) (*v1.GetPermissionResponse, error) {
	if b == nil || b.store == nil || rq == nil {
		return nil, errno.ErrInvalidArgument
	}
	obj, err := b.store.RBAC().GetPermission(ctx, strings.TrimSpace(rq.GetPermissionId()))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrNotFound
		}
		if isTableMissingError(err) {
			return nil, errno.ErrNotFound
		}
		return nil, errno.ErrInternal
	}
	return &v1.GetPermissionResponse{Permission: modelToProtoPermission(obj)}, nil
}

func (b *rbacBiz) UpsertPermission(ctx context.Context, rq *v1.UpsertPermissionRequest) (*v1.UpsertPermissionResponse, error) {
	if b == nil || b.store == nil || rq == nil {
		return nil, errno.ErrInvalidArgument
	}
	permissionID := strings.TrimSpace(rq.GetPermissionId())
	resource := strings.TrimSpace(rq.GetResource())
	action := strings.TrimSpace(rq.GetAction())
	if permissionID == "" || resource == "" || action == "" {
		return nil, errno.ErrInvalidArgument
	}
	obj := &model.RBACPermissionM{
		PermissionID: permissionID,
		Resource:     resource,
		Action:       action,
		Description:  strings.TrimSpace(rq.GetDescription()),
		Status:       normalizeStatus(rq.GetStatus()),
	}
	if err := b.store.RBAC().UpsertPermission(ctx, obj); err != nil {
		return nil, errno.ErrInternal
	}
	out, err := b.store.RBAC().GetPermission(ctx, permissionID)
	if err != nil {
		return nil, errno.ErrInternal
	}
	if reloadErr := b.Reload(ctx); reloadErr != nil {
		return nil, reloadErr
	}
	return &v1.UpsertPermissionResponse{Permission: modelToProtoPermission(out)}, nil
}

func (b *rbacBiz) DeletePermission(ctx context.Context, rq *v1.DeletePermissionRequest) (*v1.DeletePermissionResponse, error) {
	if b == nil || b.store == nil || rq == nil {
		return nil, errno.ErrInvalidArgument
	}
	if err := b.store.RBAC().DeletePermission(ctx, strings.TrimSpace(rq.GetPermissionId())); err != nil {
		return nil, errno.ErrInternal
	}
	if err := b.Reload(ctx); err != nil {
		return nil, err
	}
	return &v1.DeletePermissionResponse{}, nil
}

func (b *rbacBiz) AssignUserRoles(ctx context.Context, rq *v1.AssignUserRolesRequest) (*v1.AssignUserRolesResponse, error) {
	if b == nil || b.store == nil || rq == nil {
		return nil, errno.ErrInvalidArgument
	}
	userID := strings.TrimSpace(rq.GetUserId())
	if userID == "" {
		return nil, errno.ErrInvalidArgument
	}
	if _, err := b.store.RBAC().GetUser(ctx, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrNotFound
		}
		return nil, errno.ErrInternal
	}
	roleIDs := uniqueStrings(rq.GetRoleIds())
	for _, roleID := range roleIDs {
		if _, err := b.store.RBAC().GetRole(ctx, roleID); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errno.ErrNotFound
			}
			return nil, errno.ErrInternal
		}
	}
	if err := b.store.RBAC().ReplaceUserRoles(ctx, userID, roleIDs); err != nil {
		return nil, errno.ErrInternal
	}
	if reloadErr := b.Reload(ctx); reloadErr != nil {
		return nil, reloadErr
	}
	access, err := b.GetUserAccess(ctx, &v1.GetUserAccessRequest{UserId: userID})
	if err != nil {
		return nil, err
	}
	return &v1.AssignUserRolesResponse{UserAccess: access.UserAccess}, nil
}

func (b *rbacBiz) AssignRolePermissions(ctx context.Context, rq *v1.AssignRolePermissionsRequest) (*v1.AssignRolePermissionsResponse, error) {
	if b == nil || b.store == nil || rq == nil {
		return nil, errno.ErrInvalidArgument
	}
	roleID := strings.TrimSpace(rq.GetRoleId())
	if roleID == "" {
		return nil, errno.ErrInvalidArgument
	}
	if _, err := b.store.RBAC().GetRole(ctx, roleID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrNotFound
		}
		return nil, errno.ErrInternal
	}
	permissionIDs := uniqueStrings(rq.GetPermissionIds())
	for _, permissionID := range permissionIDs {
		if _, err := b.store.RBAC().GetPermission(ctx, permissionID); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errno.ErrNotFound
			}
			return nil, errno.ErrInternal
		}
	}
	if err := b.store.RBAC().ReplaceRolePermissions(ctx, roleID, permissionIDs); err != nil {
		return nil, errno.ErrInternal
	}
	if reloadErr := b.Reload(ctx); reloadErr != nil {
		return nil, reloadErr
	}
	permissions, err := b.store.RBAC().ListPermissionsByRole(ctx, roleID)
	if err != nil {
		return nil, errno.ErrInternal
	}
	mapped := make([]*v1.Permission, 0, len(permissions))
	for _, item := range permissions {
		mapped = append(mapped, modelToProtoPermission(item))
	}
	return &v1.AssignRolePermissionsResponse{RoleAccess: &v1.RoleAccess{RoleId: roleID, Permissions: mapped}}, nil
}

func (b *rbacBiz) GetUserAccess(ctx context.Context, rq *v1.GetUserAccessRequest) (*v1.GetUserAccessResponse, error) {
	if b == nil || b.store == nil || rq == nil {
		return nil, errno.ErrInvalidArgument
	}
	userID := strings.TrimSpace(rq.GetUserId())
	if userID == "" {
		return nil, errno.ErrInvalidArgument
	}
	if _, err := b.store.RBAC().GetUser(ctx, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrNotFound
		}
		if isTableMissingError(err) {
			return nil, errno.ErrNotFound
		}
		return nil, errno.ErrInternal
	}
	roleIDs, err := b.store.RBAC().ListRoleIDsByUser(ctx, userID)
	if err != nil {
		return nil, errno.ErrInternal
	}
	permissionMap := map[string]*v1.Permission{}
	for _, roleID := range roleIDs {
		permissions, permErr := b.store.RBAC().ListPermissionsByRole(ctx, roleID)
		if permErr != nil {
			return nil, errno.ErrInternal
		}
		for _, permission := range permissions {
			mapped := modelToProtoPermission(permission)
			permissionMap[mapped.GetPermissionId()] = mapped
		}
	}
	permissionIDs := make([]string, 0, len(permissionMap))
	for permissionID := range permissionMap {
		permissionIDs = append(permissionIDs, permissionID)
	}
	sort.Strings(permissionIDs)
	mappedPermissions := make([]*v1.Permission, 0, len(permissionIDs))
	for _, permissionID := range permissionIDs {
		mappedPermissions = append(mappedPermissions, permissionMap[permissionID])
	}
	return &v1.GetUserAccessResponse{UserAccess: &v1.UserAccess{UserId: userID, RoleIds: roleIDs, Permissions: mappedPermissions}}, nil
}

func (b *rbacBiz) ResolveLoginProfile(ctx context.Context, rq *v1.ResolveLoginProfileRequest) (*v1.ResolveLoginProfileResponse, error) {
	if b == nil || b.store == nil || rq == nil {
		return nil, errno.ErrInvalidArgument
	}
	userID := strings.TrimSpace(rq.GetUserId())
	if userID == "" {
		return nil, errno.ErrInvalidArgument
	}
	obj, err := b.store.RBAC().GetUser(ctx, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) || isTableMissingError(err) {
			return nil, nil
		}
		return nil, errno.ErrInternal
	}
	if normalizeStatus(obj.Status) != rbacStatusActive {
		return nil, errno.ErrPermissionDenied
	}
	profile := &v1.LoginProfile{
		User:             modelToProtoUser(obj),
		EffectiveTeamIds: uniqueStrings(splitCommaList(obj.TeamID)),
	}

	hash := strings.TrimSpace(ptrString(obj.PasswordHash))
	if hash != "" && !rq.GetSkipPasswordCheck() {
		if strings.TrimSpace(rq.GetPassword()) == "" {
			return nil, errno.ErrUnauthenticated
		}
		if compareErr := bcrypt.CompareHashAndPassword([]byte(hash), []byte(strings.TrimSpace(rq.GetPassword()))); compareErr != nil {
			return nil, errno.ErrUnauthenticated
		}
		profile.PasswordValidated = true
	} else if rq.GetSkipPasswordCheck() {
		profile.PasswordValidated = true
	}

	access, err := b.GetUserAccess(ctx, &v1.GetUserAccessRequest{UserId: userID})
	if err != nil {
		if errors.Is(err, errno.ErrNotFound) {
			return &v1.ResolveLoginProfileResponse{LoginProfile: profile}, nil
		}
		return nil, err
	}
	ua := access.GetUserAccess()
	profile.RoleIds = append([]string(nil), ua.GetRoleIds()...)
	profile.Permissions = append([]*v1.Permission(nil), ua.GetPermissions()...)
	actionSet := map[string]struct{}{}
	for _, permission := range ua.GetPermissions() {
		if permission == nil {
			continue
		}
		action := strings.TrimSpace(permission.GetAction())
		if action == "" {
			continue
		}
		actionSet[action] = struct{}{}
	}
	for action := range actionSet {
		profile.EffectiveActions = append(profile.EffectiveActions, action)
	}
	sort.Strings(profile.EffectiveActions)
	return &v1.ResolveLoginProfileResponse{LoginProfile: profile}, nil
}

// Model to Proto conversion functions

func modelToProtoUser(obj *model.RBACUserM) *v1.User {
	if obj == nil {
		return nil
	}
	return &v1.User{
		UserId:    strings.TrimSpace(obj.UserID),
		Username:  strings.TrimSpace(obj.Username),
		TeamId:    strings.TrimSpace(obj.TeamID),
		Status:    normalizeStatus(obj.Status),
		CreatedAt: timestamppb.New(obj.CreatedAt.UTC()),
		UpdatedAt: timestamppb.New(obj.UpdatedAt.UTC()),
	}
}

func modelToProtoRole(obj *model.RBACRoleM) *v1.Role {
	if obj == nil {
		return nil
	}
	return &v1.Role{
		RoleId:      strings.TrimSpace(obj.RoleID),
		DisplayName: strings.TrimSpace(obj.DisplayName),
		Description: strings.TrimSpace(obj.Description),
		Status:      normalizeStatus(obj.Status),
		CreatedAt:   timestamppb.New(obj.CreatedAt.UTC()),
		UpdatedAt:   timestamppb.New(obj.UpdatedAt.UTC()),
	}
}

func modelToProtoPermission(obj *model.RBACPermissionM) *v1.Permission {
	if obj == nil {
		return nil
	}
	return &v1.Permission{
		PermissionId: strings.TrimSpace(obj.PermissionID),
		Resource:     strings.TrimSpace(obj.Resource),
		Action:       strings.TrimSpace(obj.Action),
		Description:  strings.TrimSpace(obj.Description),
		Status:       normalizeStatus(obj.Status),
		CreatedAt:    timestamppb.New(obj.CreatedAt.UTC()),
		UpdatedAt:    timestamppb.New(obj.UpdatedAt.UTC()),
	}
}

// Helper functions

func normalizeStatus(status string) string {
	trimmed := strings.ToLower(strings.TrimSpace(status))
	switch trimmed {
	case "", rbacStatusActive:
		return rbacStatusActive
	case rbacStatusDisabled:
		return rbacStatusDisabled
	default:
		return trimmed
	}
}

func resolveRBACModelPath() string {
	path := strings.TrimSpace(os.Getenv(envRBACModelPath))
	if path != "" {
		return path
	}
	return defaultRBACModelPath
}

func loadRBACModel(path string) (casbinmodel.Model, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed != "" {
		if m, err := casbinmodel.NewModelFromFile(trimmed); err == nil {
			return m, nil
		}
		if absPath, err := filepath.Abs(trimmed); err == nil {
			if m, absErr := casbinmodel.NewModelFromFile(absPath); absErr == nil {
				return m, nil
			}
		}
	}
	return casbinmodel.NewModelFromString(fallbackRBACModelConf)
}

func uniqueStrings(input []string) []string {
	if len(input) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(input))
	out := make([]string, 0, len(input))
	for _, item := range input {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

func splitCommaList(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, ",")
}

func ptrString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func isTableMissingError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	if message == "" {
		return false
	}
	return strings.Contains(message, "no such table") || strings.Contains(message, "doesn't exist")
}

// Compatibility aliases for time formatting (used by tests)
var (
	// TimeNow returns current time for testing
	TimeNow = time.Now
)

// FormatTime formats time to RFC3339Nano string
func FormatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}