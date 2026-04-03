package handler

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/core"

	"github.com/aiopsre/rca-api/internal/pkg/contextx"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
)

const (
	envSwaggerRBACPath         = "RCA_API_AUTH_SWAGGER_PATH"
	defaultSwaggerRBACPath     = "api/openapi/apiserver/v1/rca-complete.swagger.json"
	swaggerScopePublic         = "public"
	swaggerScopeSeparatorSpace = " "
)

var (
	swaggerPathParamPattern = regexp.MustCompile(`\{([^/}]+)\}`)
	swaggerRBACOnce         sync.Once
	swaggerRBACIndex        map[string][]string
)

type swaggerRBACOperation struct {
	Scope any `json:"x-rbac-scope"`
}

type swaggerRBACDocument struct {
	Paths map[string]map[string]swaggerRBACOperation `json:"paths"`
}

// RequireSwaggerRBAC enforces RBAC action by method/path mapping generated in OpenAPI x-rbac-scope.
// Missing mapping keeps compatibility by fail-open.
func (h *Handler) RequireSwaggerRBAC() gin.HandlerFunc {
	return func(c *gin.Context) {
		if h == nil || h.biz == nil || h.biz.RBACV1() == nil {
			c.Next()
			return
		}
		path := normalizeSwaggerRBACPath(c.FullPath())
		if path == "" {
			path = normalizeSwaggerRBACPath(c.Request.URL.Path)
		}
		if path == "" {
			c.Next()
			return
		}
		method := strings.ToLower(strings.TrimSpace(c.Request.Method))
		actions := swaggerRbacActions(method, path)
		if len(actions) == 0 {
			c.Next()
			return
		}

		userID := strings.TrimSpace(contextx.UserID(c.Request.Context()))
		if userID == "" {
			core.WriteResponse(c, nil, errno.ErrUnauthenticated)
			c.Abort()
			return
		}
		for _, action := range actions {
			allowed, err := h.biz.RBACV1().Enforce(c.Request.Context(), userID, path, action)
			if err != nil {
				core.WriteResponse(c, nil, err)
				c.Abort()
				return
			}
			if allowed {
				c.Next()
				return
			}
		}
		core.WriteResponse(c, nil, errno.ErrPermissionDenied)
		c.Abort()
	}
}

func swaggerRbacActions(method string, path string) []string {
	idx := loadSwaggerRBACIndex()
	if len(idx) == 0 {
		return nil
	}
	key := strings.ToLower(strings.TrimSpace(method)) + " " + normalizeSwaggerRBACPath(path)
	actions, ok := idx[key]
	if !ok || len(actions) == 0 {
		return nil
	}
	return append([]string(nil), actions...)
}

func loadSwaggerRBACIndex() map[string][]string {
	swaggerRBACOnce.Do(func() {
		swaggerRBACIndex = map[string][]string{}
		path := resolveSwaggerRBACPath()
		raw, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("swagger rbac mapping file not found, skip dynamic rbac mapping", "path", path, "error", err)
			return
		}
		var doc swaggerRBACDocument
		if err := json.Unmarshal(raw, &doc); err != nil {
			slog.Warn("swagger rbac mapping parse failed, skip dynamic rbac mapping", "path", path, "error", err)
			return
		}
		if len(doc.Paths) == 0 {
			return
		}
		for rawPath, operations := range doc.Paths {
			path := normalizeSwaggerRBACPath(rawPath)
			if path == "" {
				continue
			}
			for method, operation := range operations {
				actions := normalizeSwaggerScope(operation.Scope)
				if len(actions) == 0 {
					continue
				}
				key := strings.ToLower(strings.TrimSpace(method)) + " " + path
				swaggerRBACIndex[key] = actions
			}
		}
	})
	return swaggerRBACIndex
}

func normalizeSwaggerRBACPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	path = strings.TrimSpace(swaggerPathParamPattern.ReplaceAllString(path, `:$1`))
	if path != "/" {
		path = strings.TrimRight(path, "/")
	}
	if path == "" {
		return "/"
	}
	return path
}

func resolveSwaggerRBACPath() string {
	envPath := strings.TrimSpace(os.Getenv(envSwaggerRBACPath))
	if envPath != "" {
		return envPath
	}
	if _, err := os.Stat(defaultSwaggerRBACPath); err == nil {
		return defaultSwaggerRBACPath
	}
	wd, err := os.Getwd()
	if err != nil {
		return defaultSwaggerRBACPath
	}
	current := wd
	for i := 0; i < 8; i++ {
		candidate := filepath.Join(current, defaultSwaggerRBACPath)
		if _, statErr := os.Stat(candidate); statErr == nil {
			return candidate
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return defaultSwaggerRBACPath
}

func normalizeSwaggerScope(raw any) []string {
	if raw == nil {
		return nil
	}
	items := make([]string, 0)
	switch typed := raw.(type) {
	case string:
		items = append(items, typed)
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				items = append(items, text)
			}
		}
	case []string:
		items = append(items, typed...)
	default:
		return nil
	}
	if len(items) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		normalized := strings.NewReplacer("|", swaggerScopeSeparatorSpace, ",", swaggerScopeSeparatorSpace, ";", swaggerScopeSeparatorSpace).Replace(strings.TrimSpace(item))
		for _, token := range strings.Fields(normalized) {
			action := strings.TrimSpace(token)
			if action == "" || strings.EqualFold(action, swaggerScopePublic) {
				continue
			}
			if _, ok := seen[action]; ok {
				continue
			}
			seen[action] = struct{}{}
			out = append(out, action)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
