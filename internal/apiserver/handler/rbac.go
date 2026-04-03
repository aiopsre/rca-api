package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	authpkg "github.com/aiopsre/rca-api/internal/apiserver/pkg/auth"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/authz"
)

// ListUsers handles the HTTP request to list users.
func (h *Handler) ListUsers(c *gin.Context) {
	if err := requireRBACAdminScope(c); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleQueryRequest(c, h.biz.RBACV1().ListUsers, h.val.ValidateListUsersRequest)
}

// GetUser handles the HTTP request to get a user by ID.
func (h *Handler) GetUser(c *gin.Context) {
	if err := requireRBACAdminScope(c); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleUriRequest(c, h.biz.RBACV1().GetUser, h.val.ValidateGetUserRequest)
}

// UpsertUser handles the HTTP request to create or update a user.
func (h *Handler) UpsertUser(c *gin.Context) {
	if err := requireRBACAdminScope(c); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleAllRequest(c, h.biz.RBACV1().UpsertUser, h.val.ValidateUpsertUserRequest)
}

// DeleteUser handles the HTTP request to delete a user.
func (h *Handler) DeleteUser(c *gin.Context) {
	if err := requireRBACAdminScope(c); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleUriRequest(c, h.biz.RBACV1().DeleteUser, h.val.ValidateDeleteUserRequest)
}

// AssignUserRoles handles the HTTP request to assign roles to a user.
func (h *Handler) AssignUserRoles(c *gin.Context) {
	if err := requireRBACAdminScope(c); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleAllRequest(c, h.biz.RBACV1().AssignUserRoles, h.val.ValidateAssignUserRolesRequest)
}

// ListRoles handles the HTTP request to list roles.
func (h *Handler) ListRoles(c *gin.Context) {
	if err := requireRBACAdminScope(c); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleQueryRequest(c, h.biz.RBACV1().ListRoles, h.val.ValidateListRolesRequest)
}

// GetRole handles the HTTP request to get a role by ID.
func (h *Handler) GetRole(c *gin.Context) {
	if err := requireRBACAdminScope(c); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleUriRequest(c, h.biz.RBACV1().GetRole, h.val.ValidateGetRoleRequest)
}

// UpsertRole handles the HTTP request to create or update a role.
func (h *Handler) UpsertRole(c *gin.Context) {
	if err := requireRBACAdminScope(c); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleAllRequest(c, h.biz.RBACV1().UpsertRole, h.val.ValidateUpsertRoleRequest)
}

// DeleteRole handles the HTTP request to delete a role.
func (h *Handler) DeleteRole(c *gin.Context) {
	if err := requireRBACAdminScope(c); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleUriRequest(c, h.biz.RBACV1().DeleteRole, h.val.ValidateDeleteRoleRequest)
}

// AssignRolePermissions handles the HTTP request to assign permissions to a role.
func (h *Handler) AssignRolePermissions(c *gin.Context) {
	if err := requireRBACAdminScope(c); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleAllRequest(c, h.biz.RBACV1().AssignRolePermissions, h.val.ValidateAssignRolePermissionsRequest)
}

// ListPermissions handles the HTTP request to list permissions.
func (h *Handler) ListPermissions(c *gin.Context) {
	if err := requireRBACAdminScope(c); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleQueryRequest(c, h.biz.RBACV1().ListPermissions, h.val.ValidateListPermissionsRequest)
}

// GetPermission handles the HTTP request to get a permission by ID.
func (h *Handler) GetPermission(c *gin.Context) {
	if err := requireRBACAdminScope(c); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleUriRequest(c, h.biz.RBACV1().GetPermission, h.val.ValidateGetPermissionRequest)
}

// UpsertPermission handles the HTTP request to create or update a permission.
func (h *Handler) UpsertPermission(c *gin.Context) {
	if err := requireRBACAdminScope(c); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleAllRequest(c, h.biz.RBACV1().UpsertPermission, h.val.ValidateUpsertPermissionRequest)
}

// DeletePermission handles the HTTP request to delete a permission.
func (h *Handler) DeletePermission(c *gin.Context) {
	if err := requireRBACAdminScope(c); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.HandleUriRequest(c, h.biz.RBACV1().DeletePermission, h.val.ValidateDeletePermissionRequest)
}

func requireRBACAdminScope(c *gin.Context) error {
	return authz.RequireAnyScope(c, authz.ScopeRBACAdmin, authz.ScopeConfigAdmin)
}

//nolint:gochecknoinits // Route registration follows repository-wide init registrar pattern.
func init() {
	Register(func(v1Group *gin.RouterGroup, handler *Handler, mws ...gin.HandlerFunc) {
		tokenMW := authpkg.RequireOperatorToken()
		swaggerRBACMW := handler.RequireSwaggerRBAC()
		sensitiveAuditMW := handler.AuditSensitiveOperatorAction()
		rbacMW := handler.RequireRBAC(authz.ScopeRBACAdmin)

		users := v1Group.Group("/users", append(mws, tokenMW, swaggerRBACMW, sensitiveAuditMW)...)
		users.GET("", rbacMW, handler.ListUsers)
		users.POST("", rbacMW, handler.UpsertUser)
		users.GET("/:id", rbacMW, handler.GetUser)
		users.PUT("/:id", rbacMW, handler.UpsertUser)
		users.DELETE("/:id", rbacMW, handler.DeleteUser)
		users.POST("/:id/roles", rbacMW, handler.AssignUserRoles)

		roles := v1Group.Group("/roles", append(mws, tokenMW, swaggerRBACMW, sensitiveAuditMW)...)
		roles.GET("", rbacMW, handler.ListRoles)
		roles.POST("", rbacMW, handler.UpsertRole)
		roles.GET("/:id", rbacMW, handler.GetRole)
		roles.PUT("/:id", rbacMW, handler.UpsertRole)
		roles.DELETE("/:id", rbacMW, handler.DeleteRole)
		roles.POST("/:id/permissions", rbacMW, handler.AssignRolePermissions)

		permissions := v1Group.Group("/permissions", append(mws, tokenMW, swaggerRBACMW, sensitiveAuditMW)...)
		permissions.GET("", rbacMW, handler.ListPermissions)
		permissions.POST("", rbacMW, handler.UpsertPermission)
		permissions.GET("/:id", rbacMW, handler.GetPermission)
		permissions.PUT("/:id", rbacMW, handler.UpsertPermission)
		permissions.DELETE("/:id", rbacMW, handler.DeletePermission)
	})
}