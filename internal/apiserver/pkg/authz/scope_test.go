package authz

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/aiopsre/rca-api/internal/pkg/errno"
)

func TestRequireAnyScope_AllowsAIRead(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest("GET", "/v1/ai/jobs?status=queued", nil)
	req.Header.Set(scopeHeader, "ai.read ai.run")
	c.Request = req

	err := RequireAnyScope(c, ScopeAIRead)
	require.NoError(t, err)
}

func TestRequireAnyScope_DeniesMissingScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest("GET", "/v1/ai/jobs?status=queued", nil)
	req.Header.Set(scopeHeader, "incident.read")
	c.Request = req

	err := RequireAnyScope(c, ScopeAIRead)
	require.Error(t, err)
	require.Equal(t, errno.ErrPermissionDenied, err)
}

func TestRequireAnyScope_AllowsWildcard(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest("GET", "/v1/ai/jobs?status=queued", nil)
	req.Header.Set(scopeHeader, "*")
	c.Request = req

	err := RequireAnyScope(c, ScopeAIRead)
	require.NoError(t, err)
}
