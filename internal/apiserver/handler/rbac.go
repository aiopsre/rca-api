package handler

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	rbacbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/rbac"
	authpkg "github.com/aiopsre/rca-api/internal/apiserver/pkg/auth"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/authz"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
)

type upsertUserRequest struct {
	UserID   string  `json:"user_id"`
	Username string  `json:"username,omitempty"`
	Password *string `json:"password,omitempty"`
	TeamID   string  `json:"team_id,omitempty"`
	Status   string  `json:"status,omitempty"`
}

type upsertRoleRequest struct {
	RoleID      string `json:"role_id"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status,omitempty"`
}

type upsertPermissionRequest struct {
	PermissionID string `json:"permission_id"`
	Resource     string `json:"resource"`
	Action       string `json:"action"`
	Description  string `json:"description,omitempty"`
	Status       string `json:"status,omitempty"`
}

type assignUserRolesRequest struct {
	RoleIDs []string `json:"role_ids"`
}

type assignRolePermissionsRequest struct {
	PermissionIDs []string `json:"permission_ids"`
}

func (h *Handler) ListUsers(c *gin.Context) {
	if err := requireRBACAdminScope(c); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	resp, err := h.biz.RBACV1().ListUsers(c.Request.Context(), &rbacbiz.ListUsersRequest{
		Offset: parsePositiveInt(c.Query("offset"), 0),
		Limit:  parsePositiveInt(c.Query("limit"), 50),
	})
	core.WriteResponse(c, resp, err)
}

func (h *Handler) GetUser(c *gin.Context) {
	if err := requireRBACAdminScope(c); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	userID := strings.TrimSpace(c.Param("id"))
	if userID == "" {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument)
		return
	}
	resp, err := h.biz.RBACV1().GetUser(c.Request.Context(), &rbacbiz.GetUserRequest{UserID: userID})
	core.WriteResponse(c, resp, err)
}

func (h *Handler) UpsertUser(c *gin.Context) {
	if err := requireRBACAdminScope(c); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	var req upsertUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	if pathUserID := strings.TrimSpace(c.Param("id")); pathUserID != "" {
		req.UserID = pathUserID
	}
	resp, err := h.biz.RBACV1().UpsertUser(c.Request.Context(), &rbacbiz.UpsertUserRequest{
		UserID:   strings.TrimSpace(req.UserID),
		Username: strings.TrimSpace(req.Username),
		Password: req.Password,
		TeamID:   strings.TrimSpace(req.TeamID),
		Status:   strings.TrimSpace(req.Status),
	})
	core.WriteResponse(c, resp, err)
}

func (h *Handler) DeleteUser(c *gin.Context) {
	if err := requireRBACAdminScope(c); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	userID := strings.TrimSpace(c.Param("id"))
	if userID == "" {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument)
		return
	}
	err := h.biz.RBACV1().DeleteUser(c.Request.Context(), &rbacbiz.DeleteUserRequest{UserID: userID})
	core.WriteResponse(c, gin.H{"deleted": true}, err)
}

func (h *Handler) AssignUserRoles(c *gin.Context) {
	if err := requireRBACAdminScope(c); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	userID := strings.TrimSpace(c.Param("id"))
	if userID == "" {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument)
		return
	}
	var req assignUserRolesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	resp, err := h.biz.RBACV1().AssignUserRoles(c.Request.Context(), &rbacbiz.AssignUserRolesRequest{
		UserID:  userID,
		RoleIDs: req.RoleIDs,
	})
	core.WriteResponse(c, resp, err)
}

func (h *Handler) ListRoles(c *gin.Context) {
	if err := requireRBACAdminScope(c); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	resp, err := h.biz.RBACV1().ListRoles(c.Request.Context(), &rbacbiz.ListRolesRequest{
		Offset: parsePositiveInt(c.Query("offset"), 0),
		Limit:  parsePositiveInt(c.Query("limit"), 50),
	})
	core.WriteResponse(c, resp, err)
}

func (h *Handler) GetRole(c *gin.Context) {
	if err := requireRBACAdminScope(c); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	roleID := strings.TrimSpace(c.Param("id"))
	if roleID == "" {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument)
		return
	}
	resp, err := h.biz.RBACV1().GetRole(c.Request.Context(), &rbacbiz.GetRoleRequest{RoleID: roleID})
	core.WriteResponse(c, resp, err)
}

func (h *Handler) UpsertRole(c *gin.Context) {
	if err := requireRBACAdminScope(c); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	var req upsertRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	if pathRoleID := strings.TrimSpace(c.Param("id")); pathRoleID != "" {
		req.RoleID = pathRoleID
	}
	resp, err := h.biz.RBACV1().UpsertRole(c.Request.Context(), &rbacbiz.UpsertRoleRequest{
		RoleID:      strings.TrimSpace(req.RoleID),
		DisplayName: strings.TrimSpace(req.DisplayName),
		Description: strings.TrimSpace(req.Description),
		Status:      strings.TrimSpace(req.Status),
	})
	core.WriteResponse(c, resp, err)
}

func (h *Handler) DeleteRole(c *gin.Context) {
	if err := requireRBACAdminScope(c); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	roleID := strings.TrimSpace(c.Param("id"))
	if roleID == "" {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument)
		return
	}
	err := h.biz.RBACV1().DeleteRole(c.Request.Context(), &rbacbiz.DeleteRoleRequest{RoleID: roleID})
	core.WriteResponse(c, gin.H{"deleted": true}, err)
}

func (h *Handler) AssignRolePermissions(c *gin.Context) {
	if err := requireRBACAdminScope(c); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	roleID := strings.TrimSpace(c.Param("id"))
	if roleID == "" {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument)
		return
	}
	var req assignRolePermissionsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	resp, err := h.biz.RBACV1().AssignRolePermissions(c.Request.Context(), &rbacbiz.AssignRolePermissionsRequest{
		RoleID:        roleID,
		PermissionIDs: req.PermissionIDs,
	})
	core.WriteResponse(c, resp, err)
}

func (h *Handler) ListPermissions(c *gin.Context) {
	if err := requireRBACAdminScope(c); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	resp, err := h.biz.RBACV1().ListPermissions(c.Request.Context(), &rbacbiz.ListPermissionsRequest{
		Offset: parsePositiveInt(c.Query("offset"), 0),
		Limit:  parsePositiveInt(c.Query("limit"), 50),
	})
	core.WriteResponse(c, resp, err)
}

func (h *Handler) GetPermission(c *gin.Context) {
	if err := requireRBACAdminScope(c); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	permissionID := strings.TrimSpace(c.Param("id"))
	if permissionID == "" {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument)
		return
	}
	resp, err := h.biz.RBACV1().GetPermission(c.Request.Context(), &rbacbiz.GetPermissionRequest{PermissionID: permissionID})
	core.WriteResponse(c, resp, err)
}

func (h *Handler) UpsertPermission(c *gin.Context) {
	if err := requireRBACAdminScope(c); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	var req upsertPermissionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	if pathPermissionID := strings.TrimSpace(c.Param("id")); pathPermissionID != "" {
		req.PermissionID = pathPermissionID
	}
	resp, err := h.biz.RBACV1().UpsertPermission(c.Request.Context(), &rbacbiz.UpsertPermissionRequest{
		PermissionID: strings.TrimSpace(req.PermissionID),
		Resource:     strings.TrimSpace(req.Resource),
		Action:       strings.TrimSpace(req.Action),
		Description:  strings.TrimSpace(req.Description),
		Status:       strings.TrimSpace(req.Status),
	})
	core.WriteResponse(c, resp, err)
}

func (h *Handler) DeletePermission(c *gin.Context) {
	if err := requireRBACAdminScope(c); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	permissionID := strings.TrimSpace(c.Param("id"))
	if permissionID == "" {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument)
		return
	}
	err := h.biz.RBACV1().DeletePermission(c.Request.Context(), &rbacbiz.DeletePermissionRequest{PermissionID: permissionID})
	core.WriteResponse(c, gin.H{"deleted": true}, err)
}

func requireRBACAdminScope(c *gin.Context) error {
	return authz.RequireAnyScope(c, authz.ScopeRBACAdmin, authz.ScopeConfigAdmin)
}

func parsePositiveInt(raw string, fallback int) int {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fallback
	}
	value, err := strconv.Atoi(trimmed)
	if err != nil || value < 0 {
		return fallback
	}
	return value
}

//nolint:gochecknoinits // Route registration follows repository-wide init registrar pattern.
func init() {
	Register(func(v1 *gin.RouterGroup, handler *Handler, mws ...gin.HandlerFunc) {
		tokenMW := authpkg.RequireOperatorToken()
		swaggerRBACMW := handler.RequireSwaggerRBAC()
		sensitiveAuditMW := handler.AuditSensitiveOperatorAction()
		rbacMW := handler.RequireRBAC(authz.ScopeRBACAdmin)

		users := v1.Group("/users", append(mws, tokenMW, swaggerRBACMW, sensitiveAuditMW)...)
		users.GET("", rbacMW, handler.ListUsers)
		users.POST("", rbacMW, handler.UpsertUser)
		users.GET("/:id", rbacMW, handler.GetUser)
		users.PUT("/:id", rbacMW, handler.UpsertUser)
		users.DELETE("/:id", rbacMW, handler.DeleteUser)
		users.POST("/:id/roles", rbacMW, handler.AssignUserRoles)

		roles := v1.Group("/roles", append(mws, tokenMW, swaggerRBACMW, sensitiveAuditMW)...)
		roles.GET("", rbacMW, handler.ListRoles)
		roles.POST("", rbacMW, handler.UpsertRole)
		roles.GET("/:id", rbacMW, handler.GetRole)
		roles.PUT("/:id", rbacMW, handler.UpsertRole)
		roles.DELETE("/:id", rbacMW, handler.DeleteRole)
		roles.POST("/:id/permissions", rbacMW, handler.AssignRolePermissions)

		permissions := v1.Group("/permissions", append(mws, tokenMW, swaggerRBACMW, sensitiveAuditMW)...)
		permissions.GET("", rbacMW, handler.ListPermissions)
		permissions.POST("", rbacMW, handler.UpsertPermission)
		permissions.GET("/:id", rbacMW, handler.GetPermission)
		permissions.PUT("/:id", rbacMW, handler.UpsertPermission)
		permissions.DELETE("/:id", rbacMW, handler.DeletePermission)
	})
}
