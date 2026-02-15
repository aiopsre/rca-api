package handler

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSwaggerRbacActions_FromGeneratedOpenAPI(t *testing.T) {
	resetSwaggerRBACCacheForTest()

	replayActions := swaggerRbacActions("post", "/v1/sessions/:sessionID/actions/replay")
	require.Contains(t, replayActions, "ai.run")

	reviewActions := swaggerRbacActions("post", "/v1/sessions/:sessionID/actions/review-start")
	require.Contains(t, reviewActions, "session.review")

	configActions := swaggerRbacActions("post", "/v1/config/pipeline/update")
	require.Contains(t, configActions, "config.admin")

	publicActions := swaggerRbacActions("post", "/v1/auth/login")
	require.Empty(t, publicActions)
}

func TestNormalizeSwaggerRBACPath(t *testing.T) {
	require.Equal(t, "/v1/sessions/:sessionID/workbench", normalizeSwaggerRBACPath("/v1/sessions/{sessionID}/workbench"))
	require.Equal(t, "/v1/sessions/:sessionID/workbench", normalizeSwaggerRBACPath("/v1/sessions/:sessionID/workbench/"))
	require.Equal(t, "/v1/ai/jobs:trace-compare", normalizeSwaggerRBACPath("v1/ai/jobs:trace-compare"))
}

func resetSwaggerRBACCacheForTest() {
	swaggerRBACOnce = sync.Once{}
	swaggerRBACIndex = nil
}
