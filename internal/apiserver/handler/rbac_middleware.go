package handler

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/aiopsre/rca-api/internal/pkg/contextx"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
)

// RequireRBAC checks casbin rbac policy for one action on the current API resource.
// It remains fail-open when no policy is configured to preserve backward compatibility.
func (h *Handler) RequireRBAC(action string) gin.HandlerFunc {
	action = strings.TrimSpace(action)
	return func(c *gin.Context) {
		if strings.TrimSpace(action) == "" || h == nil || h.biz == nil || h.biz.RBACV1() == nil {
			c.Next()
			return
		}
		userID := strings.TrimSpace(contextx.UserID(c.Request.Context()))
		if userID == "" {
			core.WriteResponse(c, nil, errno.ErrUnauthenticated)
			c.Abort()
			return
		}
		resource := strings.TrimSpace(c.FullPath())
		if resource == "" {
			resource = strings.TrimSpace(c.Request.URL.Path)
		}
		if resource == "" {
			core.WriteResponse(c, nil, errno.ErrInvalidArgument)
			c.Abort()
			return
		}
		allowed, err := h.biz.RBACV1().Enforce(c.Request.Context(), userID, resource, action)
		if err != nil {
			core.WriteResponse(c, nil, err)
			c.Abort()
			return
		}
		if !allowed {
			core.WriteResponse(c, nil, errno.ErrPermissionDenied)
			c.Abort()
			return
		}
		c.Next()
	}
}
