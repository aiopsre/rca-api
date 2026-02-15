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
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
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

	ListUsers(ctx context.Context, rq *ListUsersRequest) (*ListUsersResponse, error)
	GetUser(ctx context.Context, rq *GetUserRequest) (*UserView, error)
	UpsertUser(ctx context.Context, rq *UpsertUserRequest) (*UserView, error)
	DeleteUser(ctx context.Context, rq *DeleteUserRequest) error

	ListRoles(ctx context.Context, rq *ListRolesRequest) (*ListRolesResponse, error)
	GetRole(ctx context.Context, rq *GetRoleRequest) (*RoleView, error)
	UpsertRole(ctx context.Context, rq *UpsertRoleRequest) (*RoleView, error)
	DeleteRole(ctx context.Context, rq *DeleteRoleRequest) error

	ListPermissions(ctx context.Context, rq *ListPermissionsRequest) (*ListPermissionsResponse, error)
	GetPermission(ctx context.Context, rq *GetPermissionRequest) (*PermissionView, error)
	UpsertPermission(ctx context.Context, rq *UpsertPermissionRequest) (*PermissionView, error)
	DeletePermission(ctx context.Context, rq *DeletePermissionRequest) error

	AssignUserRoles(ctx context.Context, rq *AssignUserRolesRequest) (*UserAccessView, error)
	AssignRolePermissions(ctx context.Context, rq *AssignRolePermissionsRequest) (*RoleAccessView, error)
	GetUserAccess(ctx context.Context, rq *GetUserAccessRequest) (*UserAccessView, error)
	ResolveLoginProfile(ctx context.Context, rq *ResolveLoginProfileRequest) (*LoginProfile, error)

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

type ListUsersRequest struct {
	Offset int
	Limit  int
}

type ListUsersResponse struct {
	Total int64       `json:"total"`
	Items []*UserView `json:"items"`
}

type GetUserRequest struct {
	UserID string
}

type UpsertUserRequest struct {
	UserID   string
	Username string
	Password *string
	TeamID   string
	Status   string
}

type DeleteUserRequest struct {
	UserID string
}

type UserView struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username,omitempty"`
	TeamID    string `json:"team_id,omitempty"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type ListRolesRequest struct {
	Offset int
	Limit  int
}

type ListRolesResponse struct {
	Total int64       `json:"total"`
	Items []*RoleView `json:"items"`
}

type GetRoleRequest struct {
	RoleID string
}

type UpsertRoleRequest struct {
	RoleID      string
	DisplayName string
	Description string
	Status      string
}

type DeleteRoleRequest struct {
	RoleID string
}

type RoleView struct {
	RoleID      string `json:"role_id"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type ListPermissionsRequest struct {
	Offset int
	Limit  int
}

type ListPermissionsResponse struct {
	Total int64             `json:"total"`
	Items []*PermissionView `json:"items"`
}

type GetPermissionRequest struct {
	PermissionID string
}

type UpsertPermissionRequest struct {
	PermissionID string
	Resource     string
	Action       string
	Description  string
	Status       string
}

type DeletePermissionRequest struct {
	PermissionID string
}

type PermissionView struct {
	PermissionID string `json:"permission_id"`
	Resource     string `json:"resource"`
	Action       string `json:"action"`
	Description  string `json:"description,omitempty"`
	Status       string `json:"status"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

type AssignUserRolesRequest struct {
	UserID  string
	RoleIDs []string
}

type AssignRolePermissionsRequest struct {
	RoleID        string
	PermissionIDs []string
}

type GetUserAccessRequest struct {
	UserID string
}

type UserAccessView struct {
	UserID      string            `json:"user_id"`
	RoleIDs     []string          `json:"role_ids"`
	Permissions []*PermissionView `json:"permissions"`
}

type RoleAccessView struct {
	RoleID      string            `json:"role_id"`
	Permissions []*PermissionView `json:"permissions"`
}

type ResolveLoginProfileRequest struct {
	UserID            string
	Password          string
	SkipPasswordCheck bool
}

type LoginProfile struct {
	User              *UserView         `json:"user,omitempty"`
	RoleIDs           []string          `json:"role_ids,omitempty"`
	Permissions       []*PermissionView `json:"permissions,omitempty"`
	EffectiveActions  []string          `json:"effective_actions,omitempty"`
	EffectiveTeamIDs  []string          `json:"effective_team_ids,omitempty"`
	PasswordValidated bool              `json:"password_validated"`
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

func (b *rbacBiz) ListUsers(ctx context.Context, rq *ListUsersRequest) (*ListUsersResponse, error) {
	if b == nil || b.store == nil {
		return nil, errno.ErrInvalidArgument
	}
	if rq == nil {
		rq = &ListUsersRequest{}
	}
	total, list, err := b.store.RBAC().ListUsers(ctx, rq.Offset, rq.Limit)
	if err != nil {
		if isTableMissingError(err) {
			return &ListUsersResponse{Total: 0, Items: []*UserView{}}, nil
		}
		return nil, errno.ErrInternal
	}
	items := make([]*UserView, 0, len(list))
	for _, item := range list {
		items = append(items, mapUser(item))
	}
	return &ListUsersResponse{Total: total, Items: items}, nil
}

func (b *rbacBiz) GetUser(ctx context.Context, rq *GetUserRequest) (*UserView, error) {
	if b == nil || b.store == nil || rq == nil {
		return nil, errno.ErrInvalidArgument
	}
	obj, err := b.store.RBAC().GetUser(ctx, strings.TrimSpace(rq.UserID))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrNotFound
		}
		if isTableMissingError(err) {
			return nil, errno.ErrNotFound
		}
		return nil, errno.ErrInternal
	}
	return mapUser(obj), nil
}

func (b *rbacBiz) UpsertUser(ctx context.Context, rq *UpsertUserRequest) (*UserView, error) {
	if b == nil || b.store == nil || rq == nil {
		return nil, errno.ErrInvalidArgument
	}
	userID := strings.TrimSpace(rq.UserID)
	if userID == "" {
		return nil, errno.ErrInvalidArgument
	}
	status := normalizeStatus(rq.Status)
	obj := &model.RBACUserM{
		UserID:   userID,
		Username: strings.TrimSpace(rq.Username),
		TeamID:   strings.TrimSpace(rq.TeamID),
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
	return mapUser(out), nil
}

func (b *rbacBiz) DeleteUser(ctx context.Context, rq *DeleteUserRequest) error {
	if b == nil || b.store == nil || rq == nil {
		return errno.ErrInvalidArgument
	}
	if err := b.store.RBAC().DeleteUser(ctx, strings.TrimSpace(rq.UserID)); err != nil {
		return errno.ErrInternal
	}
	return b.Reload(ctx)
}

func (b *rbacBiz) ListRoles(ctx context.Context, rq *ListRolesRequest) (*ListRolesResponse, error) {
	if b == nil || b.store == nil {
		return nil, errno.ErrInvalidArgument
	}
	if rq == nil {
		rq = &ListRolesRequest{}
	}
	total, list, err := b.store.RBAC().ListRoles(ctx, rq.Offset, rq.Limit)
	if err != nil {
		if isTableMissingError(err) {
			return &ListRolesResponse{Total: 0, Items: []*RoleView{}}, nil
		}
		return nil, errno.ErrInternal
	}
	items := make([]*RoleView, 0, len(list))
	for _, item := range list {
		items = append(items, mapRole(item))
	}
	return &ListRolesResponse{Total: total, Items: items}, nil
}

func (b *rbacBiz) GetRole(ctx context.Context, rq *GetRoleRequest) (*RoleView, error) {
	if b == nil || b.store == nil || rq == nil {
		return nil, errno.ErrInvalidArgument
	}
	obj, err := b.store.RBAC().GetRole(ctx, strings.TrimSpace(rq.RoleID))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrNotFound
		}
		if isTableMissingError(err) {
			return nil, errno.ErrNotFound
		}
		return nil, errno.ErrInternal
	}
	return mapRole(obj), nil
}

func (b *rbacBiz) UpsertRole(ctx context.Context, rq *UpsertRoleRequest) (*RoleView, error) {
	if b == nil || b.store == nil || rq == nil {
		return nil, errno.ErrInvalidArgument
	}
	roleID := strings.TrimSpace(rq.RoleID)
	if roleID == "" {
		return nil, errno.ErrInvalidArgument
	}
	obj := &model.RBACRoleM{
		RoleID:      roleID,
		DisplayName: strings.TrimSpace(rq.DisplayName),
		Description: strings.TrimSpace(rq.Description),
		Status:      normalizeStatus(rq.Status),
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
	return mapRole(out), nil
}

func (b *rbacBiz) DeleteRole(ctx context.Context, rq *DeleteRoleRequest) error {
	if b == nil || b.store == nil || rq == nil {
		return errno.ErrInvalidArgument
	}
	if err := b.store.RBAC().DeleteRole(ctx, strings.TrimSpace(rq.RoleID)); err != nil {
		return errno.ErrInternal
	}
	return b.Reload(ctx)
}

func (b *rbacBiz) ListPermissions(ctx context.Context, rq *ListPermissionsRequest) (*ListPermissionsResponse, error) {
	if b == nil || b.store == nil {
		return nil, errno.ErrInvalidArgument
	}
	if rq == nil {
		rq = &ListPermissionsRequest{}
	}
	total, list, err := b.store.RBAC().ListPermissions(ctx, rq.Offset, rq.Limit)
	if err != nil {
		if isTableMissingError(err) {
			return &ListPermissionsResponse{Total: 0, Items: []*PermissionView{}}, nil
		}
		return nil, errno.ErrInternal
	}
	items := make([]*PermissionView, 0, len(list))
	for _, item := range list {
		items = append(items, mapPermission(item))
	}
	return &ListPermissionsResponse{Total: total, Items: items}, nil
}

func (b *rbacBiz) GetPermission(ctx context.Context, rq *GetPermissionRequest) (*PermissionView, error) {
	if b == nil || b.store == nil || rq == nil {
		return nil, errno.ErrInvalidArgument
	}
	obj, err := b.store.RBAC().GetPermission(ctx, strings.TrimSpace(rq.PermissionID))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrNotFound
		}
		if isTableMissingError(err) {
			return nil, errno.ErrNotFound
		}
		return nil, errno.ErrInternal
	}
	return mapPermission(obj), nil
}

func (b *rbacBiz) UpsertPermission(ctx context.Context, rq *UpsertPermissionRequest) (*PermissionView, error) {
	if b == nil || b.store == nil || rq == nil {
		return nil, errno.ErrInvalidArgument
	}
	permissionID := strings.TrimSpace(rq.PermissionID)
	resource := strings.TrimSpace(rq.Resource)
	action := strings.TrimSpace(rq.Action)
	if permissionID == "" || resource == "" || action == "" {
		return nil, errno.ErrInvalidArgument
	}
	obj := &model.RBACPermissionM{
		PermissionID: permissionID,
		Resource:     resource,
		Action:       action,
		Description:  strings.TrimSpace(rq.Description),
		Status:       normalizeStatus(rq.Status),
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
	return mapPermission(out), nil
}

func (b *rbacBiz) DeletePermission(ctx context.Context, rq *DeletePermissionRequest) error {
	if b == nil || b.store == nil || rq == nil {
		return errno.ErrInvalidArgument
	}
	if err := b.store.RBAC().DeletePermission(ctx, strings.TrimSpace(rq.PermissionID)); err != nil {
		return errno.ErrInternal
	}
	return b.Reload(ctx)
}

func (b *rbacBiz) AssignUserRoles(ctx context.Context, rq *AssignUserRolesRequest) (*UserAccessView, error) {
	if b == nil || b.store == nil || rq == nil {
		return nil, errno.ErrInvalidArgument
	}
	userID := strings.TrimSpace(rq.UserID)
	if userID == "" {
		return nil, errno.ErrInvalidArgument
	}
	if _, err := b.store.RBAC().GetUser(ctx, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrNotFound
		}
		return nil, errno.ErrInternal
	}
	roleIDs := uniqueStrings(rq.RoleIDs)
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
	return b.GetUserAccess(ctx, &GetUserAccessRequest{UserID: userID})
}

func (b *rbacBiz) AssignRolePermissions(ctx context.Context, rq *AssignRolePermissionsRequest) (*RoleAccessView, error) {
	if b == nil || b.store == nil || rq == nil {
		return nil, errno.ErrInvalidArgument
	}
	roleID := strings.TrimSpace(rq.RoleID)
	if roleID == "" {
		return nil, errno.ErrInvalidArgument
	}
	if _, err := b.store.RBAC().GetRole(ctx, roleID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrNotFound
		}
		return nil, errno.ErrInternal
	}
	permissionIDs := uniqueStrings(rq.PermissionIDs)
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
	mapped := make([]*PermissionView, 0, len(permissions))
	for _, item := range permissions {
		mapped = append(mapped, mapPermission(item))
	}
	return &RoleAccessView{RoleID: roleID, Permissions: mapped}, nil
}

func (b *rbacBiz) GetUserAccess(ctx context.Context, rq *GetUserAccessRequest) (*UserAccessView, error) {
	if b == nil || b.store == nil || rq == nil {
		return nil, errno.ErrInvalidArgument
	}
	userID := strings.TrimSpace(rq.UserID)
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
	permissionMap := map[string]*PermissionView{}
	for _, roleID := range roleIDs {
		permissions, permErr := b.store.RBAC().ListPermissionsByRole(ctx, roleID)
		if permErr != nil {
			return nil, errno.ErrInternal
		}
		for _, permission := range permissions {
			mapped := mapPermission(permission)
			permissionMap[mapped.PermissionID] = mapped
		}
	}
	permissionIDs := make([]string, 0, len(permissionMap))
	for permissionID := range permissionMap {
		permissionIDs = append(permissionIDs, permissionID)
	}
	sort.Strings(permissionIDs)
	mappedPermissions := make([]*PermissionView, 0, len(permissionIDs))
	for _, permissionID := range permissionIDs {
		mappedPermissions = append(mappedPermissions, permissionMap[permissionID])
	}
	return &UserAccessView{UserID: userID, RoleIDs: roleIDs, Permissions: mappedPermissions}, nil
}

func (b *rbacBiz) ResolveLoginProfile(ctx context.Context, rq *ResolveLoginProfileRequest) (*LoginProfile, error) {
	if b == nil || b.store == nil || rq == nil {
		return nil, errno.ErrInvalidArgument
	}
	userID := strings.TrimSpace(rq.UserID)
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
	profile := &LoginProfile{
		User:             mapUser(obj),
		EffectiveTeamIDs: uniqueStrings(splitCommaList(obj.TeamID)),
	}

	hash := strings.TrimSpace(ptrString(obj.PasswordHash))
	if hash != "" && !rq.SkipPasswordCheck {
		if strings.TrimSpace(rq.Password) == "" {
			return nil, errno.ErrUnauthenticated
		}
		if compareErr := bcrypt.CompareHashAndPassword([]byte(hash), []byte(strings.TrimSpace(rq.Password))); compareErr != nil {
			return nil, errno.ErrUnauthenticated
		}
		profile.PasswordValidated = true
	} else if rq.SkipPasswordCheck {
		profile.PasswordValidated = true
	}

	access, err := b.GetUserAccess(ctx, &GetUserAccessRequest{UserID: userID})
	if err != nil {
		if errors.Is(err, errno.ErrNotFound) {
			return profile, nil
		}
		return nil, err
	}
	profile.RoleIDs = append([]string(nil), access.RoleIDs...)
	profile.Permissions = append([]*PermissionView(nil), access.Permissions...)
	actionSet := map[string]struct{}{}
	for _, permission := range access.Permissions {
		if permission == nil {
			continue
		}
		action := strings.TrimSpace(permission.Action)
		if action == "" {
			continue
		}
		actionSet[action] = struct{}{}
	}
	for action := range actionSet {
		profile.EffectiveActions = append(profile.EffectiveActions, action)
	}
	sort.Strings(profile.EffectiveActions)
	return profile, nil
}

func mapUser(obj *model.RBACUserM) *UserView {
	if obj == nil {
		return nil
	}
	return &UserView{
		UserID:    strings.TrimSpace(obj.UserID),
		Username:  strings.TrimSpace(obj.Username),
		TeamID:    strings.TrimSpace(obj.TeamID),
		Status:    normalizeStatus(obj.Status),
		CreatedAt: obj.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt: obj.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func mapRole(obj *model.RBACRoleM) *RoleView {
	if obj == nil {
		return nil
	}
	return &RoleView{
		RoleID:      strings.TrimSpace(obj.RoleID),
		DisplayName: strings.TrimSpace(obj.DisplayName),
		Description: strings.TrimSpace(obj.Description),
		Status:      normalizeStatus(obj.Status),
		CreatedAt:   obj.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:   obj.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func mapPermission(obj *model.RBACPermissionM) *PermissionView {
	if obj == nil {
		return nil
	}
	return &PermissionView{
		PermissionID: strings.TrimSpace(obj.PermissionID),
		Resource:     strings.TrimSpace(obj.Resource),
		Action:       strings.TrimSpace(obj.Action),
		Description:  strings.TrimSpace(obj.Description),
		Status:       normalizeStatus(obj.Status),
		CreatedAt:    obj.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:    obj.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

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
