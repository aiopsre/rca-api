package auth

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/aiopsre/rca-api/internal/apiserver/pkg/authz"
	"github.com/aiopsre/rca-api/internal/pkg/contextx"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
)

const (
	authorizationHeader = "Authorization"
)

// RequireOperatorToken validates Bearer token and injects operator identity into request context.
func RequireOperatorToken() gin.HandlerFunc {
	return func(c *gin.Context) {
		rawHeader := strings.TrimSpace(c.GetHeader(authorizationHeader))
		token, err := ExtractBearerToken(rawHeader)
		if err != nil {
			core.WriteResponse(c, nil, errno.ErrUnauthenticated)
			c.Abort()
			return
		}
		claims, err := ParseToken(token)
		if err != nil {
			core.WriteResponse(c, nil, errno.ErrTokenInvalid)
			c.Abort()
			return
		}

		ctx := c.Request.Context()
		ctx = contextx.WithUserID(ctx, claims.OperatorID)
		if claims.Username != "" {
			ctx = contextx.WithUsername(ctx, claims.Username)
		}
		ctx = contextx.WithAccessToken(ctx, token)
		ctx = contextx.WithOperatorTeams(ctx, claims.TeamIDs)
		ctx = contextx.WithOperatorScopes(ctx, claims.Scopes)
		c.Request = c.Request.WithContext(ctx)

		if strings.TrimSpace(c.GetHeader("X-Scopes")) == "" && len(claims.Scopes) > 0 {
			c.Request.Header.Set("X-Scopes", strings.Join(claims.Scopes, " "))
		}
		if strings.TrimSpace(c.GetHeader("X-Scopes")) == "" {
			// Keep compatibility with existing scope checks for operator endpoints.
			c.Request.Header.Set("X-Scopes", strings.Join([]string{authz.ScopeAIRead, authz.ScopeAIRun}, " "))
		}

		c.Next()
	}
}
