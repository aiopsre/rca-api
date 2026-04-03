package gin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ginpkg "github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestDefaultCorsConfig_IncludesOperatorHeaders(t *testing.T) {
	config := DefaultCorsConfig()

	require.Contains(t, config.AllowedHeaders, "X-Operator-ID")
	require.Contains(t, config.AllowedHeaders, "X-Operator-Teams")
}

func TestCors_PreflightAllowsOperatorHeaders(t *testing.T) {
	ginpkg.SetMode(ginpkg.TestMode)

	engine := ginpkg.New()
	engine.Use(Cors(DefaultCorsConfig()))
	engine.OPTIONS("/v1/operator/inbox", func(c *ginpkg.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodOptions, "/v1/operator/inbox", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", http.MethodGet)
	req.Header.Set("Access-Control-Request-Headers", "Authorization, X-Operator-ID, X-Operator-Teams")

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusNoContent, recorder.Code)
	allowedHeaders := recorder.Header().Get("Access-Control-Allow-Headers")
	require.NotEmpty(t, allowedHeaders)
	require.Contains(t, strings.ToLower(allowedHeaders), strings.ToLower("X-Operator-ID"))
	require.Contains(t, strings.ToLower(allowedHeaders), strings.ToLower("X-Operator-Teams"))
}
