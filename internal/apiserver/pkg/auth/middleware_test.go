package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/aiopsre/rca-api/internal/pkg/contextx"
)

func TestRequireOperatorToken_InjectsContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	issueResp, err := IssueToken(&IssueTokenRequest{
		OperatorID: "operator:alice",
		Username:   "alice",
		TeamIDs:    []string{"namespace:payments"},
		Scopes:     []string{"ai.read", "ai.run"},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/s-1/workbench", nil)
	req.Header.Set("Authorization", "Bearer "+issueResp.Token)
	c.Request = req

	called := false
	RequireOperatorToken()(c)
	if !c.IsAborted() {
		called = true
	}
	require.True(t, called)
	require.Equal(t, "operator:alice", contextx.UserID(c.Request.Context()))
	require.Equal(t, "alice", contextx.Username(c.Request.Context()))
	require.Equal(t, []string{"namespace:payments"}, contextx.OperatorTeams(c.Request.Context()))
	require.Equal(t, "ai.read ai.run", c.Request.Header.Get("X-Scopes"))
}

func TestRequireOperatorToken_MissingTokenDenied(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/operator/inbox", nil)

	RequireOperatorToken()(c)
	require.True(t, c.IsAborted())
	require.Equal(t, http.StatusUnauthorized, w.Code)
}
