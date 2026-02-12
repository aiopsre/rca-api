package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/aiopsre/rca-api/internal/apiserver/biz"
	sessionbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/session"
	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/cachex"
)

func TestOperatorReadAPIs_RedisCacheAndInvalidation(t *testing.T) {
	baseURL, cleanup, s, client := newTestServer(t)
	defer cleanup()
	require.NoError(t, s.DB(context.Background()).AutoMigrate(&model.SessionContextM{}, &model.SessionHistoryEventM{}))

	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	cachex.ConfigureRedisClient(redis.NewClient(&redis.Options{Addr: mr.Addr()}))
	defer func() { _ = cachex.Close() }()

	incident := createAIJobLongPollTestIncident(t, s)
	aiBiz := biz.NewBiz(s).AIJobV1()
	sessionSvc := biz.NewBiz(s).SessionV1()

	leftJobID := createFailedTraceJob(t, aiBiz, incident.IncidentID, "manual", "manual_api", "user:cache", "cache trace left")
	rightJobID := createFailedTraceJob(t, aiBiz, incident.IncidentID, "follow_up", "follow_up_api", "user:cache", "cache trace right")
	sessionID := mustHandlerSessionIDByJob(t, s, rightJobID)

	_, err = sessionSvc.UpdateAssignment(context.Background(), &sessionbiz.UpdateAssignmentRequest{
		SessionID:  sessionID,
		Assignee:   "user:oncall-a",
		AssignedBy: strPtr("user:lead-a"),
	})
	require.NoError(t, err)

	requests := []string{
		fmt.Sprintf("%s/v1/operator/inbox?offset=0&limit=10", baseURL),
		fmt.Sprintf("%s/v1/sessions/%s/workbench?limit=10", baseURL, sessionID),
		fmt.Sprintf("%s/v1/operator/dashboard", baseURL),
		fmt.Sprintf("%s/v1/ai/jobs/%s/trace", baseURL, rightJobID),
		fmt.Sprintf("%s/v1/ai/jobs:trace-compare?left_job_id=%s&right_job_id=%s", baseURL, leftJobID, rightJobID),
		fmt.Sprintf("%s/v1/sessions/%s/history?offset=0&limit=10&order=desc", baseURL, sessionID),
	}
	for _, reqURL := range requests {
		status, _, reqErr := doJSONRequest(client, http.MethodGet, reqURL, nil)
		require.NoError(t, reqErr)
		require.Equal(t, http.StatusOK, status, reqURL)
	}

	keys := mr.Keys()
	require.True(t, hasCachePrefix(keys, "inbox:operator_test:"), "missing inbox cache key")
	require.True(t, hasCachePrefix(keys, "workbench:"+strings.ToLower(sessionID)), "missing workbench cache key")
	require.True(t, hasCachePrefix(keys, "dashboard:default:"), "missing dashboard cache key")
	require.True(t, hasCachePrefix(keys, "trace:"+strings.ToLower(rightJobID)), "missing trace cache key")
	require.True(t, hasCachePrefix(keys, "compare:"+strings.ToLower(leftJobID)+":"+strings.ToLower(rightJobID)), "missing compare cache key")
	require.True(t, hasCachePrefix(keys, "history:"+strings.ToLower(sessionID)+":0:10:desc"), "missing history cache key")
	require.True(t, hasCachePrefix(keys, "session_state:"+strings.ToLower(sessionID)), "missing session_state cache key")

	reassignStatus, _, err := doJSONRequest(
		client,
		http.MethodPost,
		fmt.Sprintf("%s/v1/sessions/%s/actions/reassign", baseURL, sessionID),
		[]byte(`{"assignee":"user:oncall-b","note":"invalidate read cache"}`),
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, reassignStatus)

	keysAfter := mr.Keys()
	require.False(t, hasCachePrefix(keysAfter, "workbench:"+strings.ToLower(sessionID)))
	require.False(t, hasCachePrefix(keysAfter, "history:"+strings.ToLower(sessionID)+":"))
	require.False(t, hasCachePrefix(keysAfter, "inbox:"))
	require.False(t, hasCachePrefix(keysAfter, "dashboard:"))

	status, _, err := doJSONRequest(client, http.MethodGet, fmt.Sprintf("%s/v1/sessions/%s/workbench?limit=10", baseURL, sessionID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.True(t, hasCachePrefix(mr.Keys(), "workbench:"+strings.ToLower(sessionID)))
}

func hasCachePrefix(keys []string, prefix string) bool {
	for _, key := range keys {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(key)), strings.ToLower(strings.TrimSpace(prefix))) {
			return true
		}
	}
	return false
}
