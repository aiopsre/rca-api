package handler

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	rbac "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/rbac"
	authpkg "github.com/aiopsre/rca-api/internal/apiserver/pkg/auth"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/authz"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
)

type operatorLoginRequest struct {
	OperatorID *string  `json:"operator_id,omitempty"`
	Username   *string  `json:"username,omitempty"`
	Password   *string  `json:"password,omitempty"`
	TeamIDs    []string `json:"team_ids,omitempty"`
	Scopes     []string `json:"scopes,omitempty"`
	TTLSeconds *int64   `json:"ttl_seconds,omitempty"`
}

type operatorLoginResponse struct {
	Token     string                   `json:"token"`
	TokenType string                   `json:"token_type"`
	ExpiresAt string                   `json:"expires_at"`
	Operator  *operatorIdentityPayload `json:"operator"`
}

type operatorIdentityPayload struct {
	OperatorID string               `json:"operator_id"`
	Username   string               `json:"username,omitempty"`
	TeamIDs    []string             `json:"team_ids,omitempty"`
	Scopes     []string             `json:"scopes,omitempty"`
	RBAC       *operatorRBACPayload `json:"rbac,omitempty"`
}

type operatorRBACPayload struct {
	RoleIDs     []string                     `json:"role_ids,omitempty"`
	Permissions []*operatorPermissionPayload `json:"permissions,omitempty"`
}

type operatorPermissionPayload struct {
	PermissionID string `json:"permission_id"`
	Resource     string `json:"resource"`
	Action       string `json:"action"`
}

func (h *Handler) LoginOperator(c *gin.Context) {
	var req operatorLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	operatorID := strings.TrimSpace(normalizeOptionalText(req.OperatorID))
	username := strings.TrimSpace(normalizeOptionalText(req.Username))
	if operatorID == "" {
		operatorID = username
	}
	if operatorID == "" {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument)
		return
	}
	ttl := time.Duration(0)
	if req.TTLSeconds != nil {
		if *req.TTLSeconds <= 0 {
			core.WriteResponse(c, nil, errno.ErrInvalidArgument)
			return
		}
		ttl = time.Duration(*req.TTLSeconds) * time.Second
	}
	password := strings.TrimSpace(normalizeOptionalText(req.Password))
	loginProfile, err := h.biz.RBACV1().ResolveLoginProfile(c.Request.Context(), &rbac.ResolveLoginProfileRequest{
		UserID:   operatorID,
		Password: password,
	})
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	if username == "" && loginProfile != nil && loginProfile.User != nil {
		username = strings.TrimSpace(loginProfile.User.Username)
	}
	if len(req.TeamIDs) == 0 && loginProfile != nil && len(loginProfile.EffectiveTeamIDs) > 0 {
		req.TeamIDs = append([]string(nil), loginProfile.EffectiveTeamIDs...)
	}
	scopes := req.Scopes
	if len(scopes) == 0 {
		if loginProfile != nil && len(loginProfile.EffectiveActions) > 0 {
			scopes = append([]string(nil), loginProfile.EffectiveActions...)
		} else {
			scopes = []string{authz.ScopeAIRead, authz.ScopeAIRun}
		}
	}
	issueResp, err := authpkg.IssueToken(&authpkg.IssueTokenRequest{
		OperatorID: operatorID,
		Username:   username,
		TeamIDs:    req.TeamIDs,
		Scopes:     scopes,
		TTL:        ttl,
		Now:        time.Now().UTC(),
	})
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	resp := &operatorLoginResponse{
		Token:     issueResp.Token,
		TokenType: "Bearer",
		ExpiresAt: issueResp.ExpiresAt.UTC().Format(time.RFC3339Nano),
		Operator: &operatorIdentityPayload{
			OperatorID: issueResp.Claims.OperatorID,
			Username:   issueResp.Claims.Username,
			TeamIDs:    append([]string(nil), issueResp.Claims.TeamIDs...),
			Scopes:     append([]string(nil), issueResp.Claims.Scopes...),
			RBAC:       mapLoginRBAC(loginProfile),
		},
	}
	core.WriteResponse(c, resp, nil)
}

func mapLoginRBAC(profile *rbac.LoginProfile) *operatorRBACPayload {
	if profile == nil {
		return nil
	}
	out := &operatorRBACPayload{
		RoleIDs: append([]string(nil), profile.RoleIDs...),
	}
	if len(profile.Permissions) == 0 {
		return out
	}
	items := make([]*operatorPermissionPayload, 0, len(profile.Permissions))
	for _, item := range profile.Permissions {
		if item == nil {
			continue
		}
		items = append(items, &operatorPermissionPayload{
			PermissionID: item.PermissionID,
			Resource:     item.Resource,
			Action:       item.Action,
		})
	}
	out.Permissions = items
	return out
}

//nolint:gochecknoinits // Route registration follows repository-wide init registrar pattern.
func init() {
	Register(func(v1 *gin.RouterGroup, handler *Handler, mws ...gin.HandlerFunc) {
		authGroup := v1.Group("/auth", mws...)
		authGroup.POST("/login", handler.LoginOperator)
	})
}
