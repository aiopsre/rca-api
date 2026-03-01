package handler

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	authpkg "github.com/aiopsre/rca-api/internal/apiserver/pkg/auth"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/authz"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
)

type operatorLoginRequest struct {
	OperatorID        *string  `json:"operator_id,omitempty"`
	Username          *string  `json:"username,omitempty"`
	Password          *string  `json:"password,omitempty"`
	OIDCIDToken       *string  `json:"oidc_id_token,omitempty"`
	TeamIDs           []string `json:"team_ids,omitempty"`
	Scopes            []string `json:"scopes,omitempty"`
	TTLSeconds        *int64   `json:"ttl_seconds,omitempty"`
	RefreshTTLSeconds *int64   `json:"refresh_ttl_seconds,omitempty"`
}

type operatorRefreshRequest struct {
	RefreshToken      *string `json:"refresh_token,omitempty"`
	TTLSeconds        *int64  `json:"ttl_seconds,omitempty"`
	RefreshTTLSeconds *int64  `json:"refresh_ttl_seconds,omitempty"`
}

type operatorLoginResponse struct {
	Token            string                   `json:"token"`
	AccessToken      string                   `json:"access_token,omitempty"`
	RefreshToken     string                   `json:"refresh_token,omitempty"`
	TokenType        string                   `json:"token_type"`
	ExpiresAt        string                   `json:"expires_at"`
	AccessExpiresAt  string                   `json:"access_expires_at,omitempty"`
	RefreshExpiresAt string                   `json:"refresh_expires_at,omitempty"`
	Operator         *operatorIdentityPayload `json:"operator"`
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

	accessTTL, refreshTTL, err := parseTokenTTLs(req.TTLSeconds, req.RefreshTTLSeconds)
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	now := time.Now().UTC()

	oidcIdentity, err := resolveOIDCIdentityForLogin(req.OIDCIDToken)
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	usingOIDC := oidcIdentity != nil

	operatorID := strings.TrimSpace(normalizeOptionalText(req.OperatorID))
	username := strings.TrimSpace(normalizeOptionalText(req.Username))
	if operatorID == "" && oidcIdentity != nil {
		operatorID = strings.TrimSpace(oidcIdentity.Subject)
	}
	if username == "" && oidcIdentity != nil {
		username = firstNonEmpty(
			strings.TrimSpace(oidcIdentity.Username),
			strings.TrimSpace(oidcIdentity.Email),
		)
	}
	if operatorID == "" {
		operatorID = username
	}
	if operatorID == "" {
		core.WriteResponse(c, nil, errno.ErrInvalidArgument)
		return
	}

	if len(req.TeamIDs) == 0 && oidcIdentity != nil {
		req.TeamIDs = append([]string(nil), oidcIdentity.TeamIDs...)
	}
	if len(req.Scopes) == 0 && oidcIdentity != nil {
		req.Scopes = append([]string(nil), oidcIdentity.Scopes...)
	}

	password := strings.TrimSpace(normalizeOptionalText(req.Password))
	loginProfileResp, err := h.biz.RBACV1().ResolveLoginProfile(c.Request.Context(), &v1.ResolveLoginProfileRequest{
		UserId:            operatorID,
		Password:          password,
		SkipPasswordCheck: usingOIDC,
	})
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	loginProfile := loginProfileResp.GetLoginProfile()
	if username == "" && loginProfile != nil && loginProfile.User != nil {
		username = strings.TrimSpace(loginProfile.User.GetUsername())
	}
	if len(req.TeamIDs) == 0 && loginProfile != nil && len(loginProfile.GetEffectiveTeamIds()) > 0 {
		req.TeamIDs = append([]string(nil), loginProfile.GetEffectiveTeamIds()...)
	}
	scopes := req.Scopes
	if len(scopes) == 0 {
		if loginProfile != nil && len(loginProfile.GetEffectiveActions()) > 0 {
			scopes = append([]string(nil), loginProfile.GetEffectiveActions()...)
		} else {
			scopes = []string{authz.ScopeAIRead, authz.ScopeAIRun}
		}
	}

	pairResp, err := authpkg.IssueTokenPair(&authpkg.IssueTokenPairRequest{
		OperatorID: operatorID,
		Username:   username,
		TeamIDs:    req.TeamIDs,
		Scopes:     scopes,
		Now:        now,
		AccessTTL:  accessTTL,
		RefreshTTL: refreshTTL,
	})
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.WriteResponse(c, buildOperatorTokenResponse(pairResp, loginProfileResp), nil)
}

func (h *Handler) RefreshOperatorToken(c *gin.Context) {
	var req operatorRefreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	accessTTL, refreshTTL, err := parseTokenTTLs(req.TTLSeconds, req.RefreshTTLSeconds)
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	refreshToken := strings.TrimSpace(normalizeOptionalText(req.RefreshToken))
	if refreshToken == "" {
		if bearerToken, tokenErr := authpkg.ExtractBearerToken(strings.TrimSpace(c.GetHeader("Authorization"))); tokenErr == nil {
			refreshToken = bearerToken
		}
	}
	if refreshToken == "" {
		core.WriteResponse(c, nil, errno.ErrUnauthenticated)
		return
	}
	refreshClaims, err := authpkg.ParseRefreshToken(refreshToken)
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	loginProfileResp, err := h.biz.RBACV1().ResolveLoginProfile(c.Request.Context(), &v1.ResolveLoginProfileRequest{
		UserId:            strings.TrimSpace(refreshClaims.OperatorID),
		SkipPasswordCheck: true,
	})
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}

	operatorID := strings.TrimSpace(refreshClaims.OperatorID)
	username := strings.TrimSpace(refreshClaims.Username)
	teamIDs := append([]string(nil), refreshClaims.TeamIDs...)
	scopes := append([]string(nil), refreshClaims.Scopes...)
	if loginProfileResp != nil {
		lp := loginProfileResp.GetLoginProfile()
		if lp != nil {
			if lp.User != nil {
				if u := strings.TrimSpace(lp.User.GetUserId()); u != "" {
					operatorID = u
				}
				if uname := strings.TrimSpace(lp.User.GetUsername()); uname != "" {
					username = uname
				}
			}
			if len(lp.GetEffectiveTeamIds()) > 0 {
				teamIDs = append([]string(nil), lp.GetEffectiveTeamIds()...)
			}
			if len(lp.GetEffectiveActions()) > 0 {
				scopes = append([]string(nil), lp.GetEffectiveActions()...)
			}
		}
	}
	if operatorID == "" {
		core.WriteResponse(c, nil, errno.ErrUnauthenticated)
		return
	}
	if len(scopes) == 0 {
		scopes = []string{authz.ScopeAIRead, authz.ScopeAIRun}
	}

	pairResp, err := authpkg.IssueTokenPair(&authpkg.IssueTokenPairRequest{
		OperatorID: operatorID,
		Username:   username,
		TeamIDs:    teamIDs,
		Scopes:     scopes,
		Now:        time.Now().UTC(),
		AccessTTL:  accessTTL,
		RefreshTTL: refreshTTL,
	})
	if err != nil {
		core.WriteResponse(c, nil, err)
		return
	}
	core.WriteResponse(c, buildOperatorTokenResponse(pairResp, loginProfileResp), nil)
}

func buildOperatorTokenResponse(pairResp *authpkg.IssueTokenPairResponse, profile *v1.ResolveLoginProfileResponse) *operatorLoginResponse {
	if pairResp == nil || pairResp.Claims == nil {
		return &operatorLoginResponse{}
	}
	accessToken := strings.TrimSpace(pairResp.AccessToken)
	resp := &operatorLoginResponse{
		Token:            accessToken,
		AccessToken:      accessToken,
		RefreshToken:     strings.TrimSpace(pairResp.RefreshToken),
		TokenType:        "Bearer",
		ExpiresAt:        pairResp.AccessExpiresAt.UTC().Format(time.RFC3339Nano),
		AccessExpiresAt:  pairResp.AccessExpiresAt.UTC().Format(time.RFC3339Nano),
		RefreshExpiresAt: pairResp.RefreshExpiresAt.UTC().Format(time.RFC3339Nano),
		Operator: &operatorIdentityPayload{
			OperatorID: pairResp.Claims.OperatorID,
			Username:   pairResp.Claims.Username,
			TeamIDs:    append([]string(nil), pairResp.Claims.TeamIDs...),
			Scopes:     append([]string(nil), pairResp.Claims.Scopes...),
			RBAC:       mapLoginRBAC(profile),
		},
	}
	return resp
}

func parseTokenTTLs(accessTTLSeconds *int64, refreshTTLSeconds *int64) (time.Duration, time.Duration, error) {
	accessTTL := time.Duration(0)
	if accessTTLSeconds != nil {
		if *accessTTLSeconds <= 0 {
			return 0, 0, errno.ErrInvalidArgument
		}
		accessTTL = time.Duration(*accessTTLSeconds) * time.Second
	}
	refreshTTL := time.Duration(0)
	if refreshTTLSeconds != nil {
		if *refreshTTLSeconds <= 0 {
			return 0, 0, errno.ErrInvalidArgument
		}
		refreshTTL = time.Duration(*refreshTTLSeconds) * time.Second
	}
	return accessTTL, refreshTTL, nil
}

func resolveOIDCIdentityForLogin(rawOIDCIDToken *string) (*authpkg.OIDCIdentity, error) {
	oidcToken := strings.TrimSpace(normalizeOptionalText(rawOIDCIDToken))
	if oidcToken == "" {
		return nil, nil
	}
	identity, err := authpkg.VerifyOIDCIDToken(oidcToken)
	if err != nil {
		return nil, err
	}
	return identity, nil
}

func mapLoginRBAC(profile *v1.ResolveLoginProfileResponse) *operatorRBACPayload {
	if profile == nil {
		return nil
	}
	lp := profile.GetLoginProfile()
	if lp == nil {
		return nil
	}
	out := &operatorRBACPayload{
		RoleIDs: append([]string(nil), lp.GetRoleIds()...),
	}
	if len(lp.GetPermissions()) == 0 {
		return out
	}
	items := make([]*operatorPermissionPayload, 0, len(lp.GetPermissions()))
	for _, item := range lp.GetPermissions() {
		if item == nil {
			continue
		}
		items = append(items, &operatorPermissionPayload{
			PermissionID: item.GetPermissionId(),
			Resource:     item.GetResource(),
			Action:       item.GetAction(),
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
		authGroup.POST("/refresh", handler.RefreshOperatorToken)
	})
}
