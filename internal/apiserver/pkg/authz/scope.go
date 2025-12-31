package authz

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/aiopsre/rca-api/internal/pkg/errno"
)

const (
	scopeHeader = "X-Scopes"

	ScopeAlertRead   = "alert.read"
	ScopeAlertIngest = "alert.ingest"
	ScopeAlertAck    = "alert.ack"

	ScopeDatasourceRead  = "datasource.read"
	ScopeDatasourceAdmin = "datasource.admin"
	ScopeEvidenceQuery   = "evidence.query"
	ScopeEvidenceSave    = "evidence.save"
	ScopeEvidenceRead    = "evidence.read"
	ScopeAIRun           = "ai.run"
	ScopeAIRead          = "ai.read"
	ScopeAICancel        = "ai.cancel"
	ScopeSilenceRead     = "silence.read"
	ScopeSilenceAdmin    = "silence.admin"
	ScopeNoticeRead      = "notice.read"
	ScopeNoticeAdmin     = "notice.admin"
)

// RequireAnyScope verifies caller scopes from X-Scopes header.
// Wildcard '*' is allowed for bootstrap/testing environments.
func RequireAnyScope(c *gin.Context, required ...string) error {
	raw := strings.TrimSpace(c.GetHeader(scopeHeader))
	if raw == "" {
		return errno.ErrPermissionDenied
	}

	scopeSet := parseScopes(raw)
	if _, ok := scopeSet["*"]; ok {
		return nil
	}
	for _, scope := range required {
		if _, ok := scopeSet[scope]; ok {
			return nil
		}
	}
	return errno.ErrPermissionDenied
}

func parseScopes(raw string) map[string]struct{} {
	normalized := strings.NewReplacer(",", " ", ";", " ", "\n", " ").Replace(raw)
	parts := strings.Fields(normalized)
	out := make(map[string]struct{}, len(parts))
	for _, item := range parts {
		scope := strings.TrimSpace(item)
		if scope == "" {
			continue
		}
		out[scope] = struct{}{}
	}
	return out
}
