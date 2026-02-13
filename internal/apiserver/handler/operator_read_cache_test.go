package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
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
		fmt.Sprintf("%s/v1/operator/assignment_history?session_id=%s&offset=0&limit=10&order=desc", baseURL, sessionID),
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
	require.True(t, hasCachePrefix(keys, "history:global_assignment:"), "missing global assignment history cache key")
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
	require.False(t, hasCachePrefix(keysAfter, "history:global_assignment:"))

	status, _, err := doJSONRequest(client, http.MethodGet, fmt.Sprintf("%s/v1/sessions/%s/workbench?limit=10", baseURL, sessionID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.True(t, hasCachePrefix(mr.Keys(), "workbench:"+strings.ToLower(sessionID)))
}

func TestOperatorReadAPIs_ReviewActionInvalidatesRedisReadCache(t *testing.T) {
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

	jobID := createFailedTraceJob(t, aiBiz, incident.IncidentID, "manual", "manual_api", "user:cache", "cache review invalidation")
	sessionID := mustHandlerSessionIDByJob(t, s, jobID)

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
		fmt.Sprintf("%s/v1/sessions/%s/history?offset=0&limit=10&order=desc", baseURL, sessionID),
		fmt.Sprintf("%s/v1/operator/assignment_history?session_id=%s&offset=0&limit=10&order=desc", baseURL, sessionID),
	}
	for _, reqURL := range requests {
		status, _, reqErr := doJSONRequest(client, http.MethodGet, reqURL, nil)
		require.NoError(t, reqErr)
		require.Equal(t, http.StatusOK, status)
	}

	require.True(t, hasCachePrefix(mr.Keys(), "workbench:"+strings.ToLower(sessionID)))
	require.True(t, hasCachePrefix(mr.Keys(), "inbox:"))
	require.True(t, hasCachePrefix(mr.Keys(), "dashboard:"))
	require.True(t, hasCachePrefix(mr.Keys(), "history:"+strings.ToLower(sessionID)+":"))
	require.True(t, hasCachePrefix(mr.Keys(), "history:global_assignment:"))

	reviewStatus, _, err := doJSONRequest(
		client,
		http.MethodPost,
		fmt.Sprintf("%s/v1/sessions/%s/actions/review-start", baseURL, sessionID),
		[]byte(`{"note":"start review and invalidate cache"}`),
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, reviewStatus)

	keysAfter := mr.Keys()
	require.False(t, hasCachePrefix(keysAfter, "workbench:"+strings.ToLower(sessionID)))
	require.False(t, hasCachePrefix(keysAfter, "history:"+strings.ToLower(sessionID)+":"))
	require.False(t, hasCachePrefix(keysAfter, "inbox:"))
	require.False(t, hasCachePrefix(keysAfter, "dashboard:"))
	require.False(t, hasCachePrefix(keysAfter, "history:global_assignment:"))
}

func TestOperatorReadAPIs_PaginationCacheKeysAreIsolated(t *testing.T) {
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

	jobA := createFailedTraceJob(t, aiBiz, incident.IncidentID, "manual", "manual_api", "user:a", "pagination cache a")
	jobB := createFailedTraceJob(t, aiBiz, incident.IncidentID, "follow_up", "follow_up_api", "user:b", "pagination cache b")
	sessionID := mustHandlerSessionIDByJob(t, s, jobA)
	_ = mustHandlerSessionIDByJob(t, s, jobB)

	inboxReqs := []string{
		fmt.Sprintf("%s/v1/operator/inbox?offset=0&limit=1", baseURL),
		fmt.Sprintf("%s/v1/operator/inbox?offset=1&limit=1", baseURL),
	}
	for _, reqURL := range inboxReqs {
		status, _, reqErr := doJSONRequest(client, http.MethodGet, reqURL, nil)
		require.NoError(t, reqErr)
		require.Equal(t, http.StatusOK, status)
	}

	historyReqs := []string{
		fmt.Sprintf("%s/v1/sessions/%s/history?offset=0&limit=5&order=desc", baseURL, sessionID),
		fmt.Sprintf("%s/v1/sessions/%s/history?offset=0&limit=10&order=desc", baseURL, sessionID),
	}
	for _, reqURL := range historyReqs {
		status, _, reqErr := doJSONRequest(client, http.MethodGet, reqURL, nil)
		require.NoError(t, reqErr)
		require.Equal(t, http.StatusOK, status)
	}

	keys := mr.Keys()
	require.GreaterOrEqual(t, countCachePrefix(keys, "inbox:operator_test:"), 2)
	require.True(t, hasCachePrefix(keys, "history:"+strings.ToLower(sessionID)+":0:5:desc"))
	require.True(t, hasCachePrefix(keys, "history:"+strings.ToLower(sessionID)+":0:10:desc"))
}

func TestOperatorReadAPIs_ConcurrentCacheReadsRemainConsistent(t *testing.T) {
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

	jobID := createFailedTraceJob(t, aiBiz, incident.IncidentID, "manual", "manual_api", "user:concurrent", "cache concurrent read")
	sessionID := mustHandlerSessionIDByJob(t, s, jobID)
	_, err = sessionSvc.UpdateAssignment(context.Background(), &sessionbiz.UpdateAssignmentRequest{
		SessionID:  sessionID,
		Assignee:   "user:oncall-concurrent",
		AssignedBy: strPtr("user:lead-concurrent"),
	})
	require.NoError(t, err)

	// Warmup
	for _, reqURL := range []string{
		fmt.Sprintf("%s/v1/operator/inbox?offset=0&limit=10", baseURL),
		fmt.Sprintf("%s/v1/sessions/%s/workbench?limit=10", baseURL, sessionID),
		fmt.Sprintf("%s/v1/operator/dashboard", baseURL),
	} {
		status, _, reqErr := doJSONRequest(client, http.MethodGet, reqURL, nil)
		require.NoError(t, reqErr)
		require.Equal(t, http.StatusOK, status)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 128)
	requests := []string{
		fmt.Sprintf("%s/v1/operator/inbox?offset=0&limit=10", baseURL),
		fmt.Sprintf("%s/v1/sessions/%s/workbench?limit=10", baseURL, sessionID),
		fmt.Sprintf("%s/v1/operator/dashboard", baseURL),
	}
	for i := 0; i < 30; i++ {
		for _, reqURL := range requests {
			wg.Add(1)
			go func(url string) {
				defer wg.Done()
				status, _, reqErr := doJSONRequest(client, http.MethodGet, url, nil)
				if reqErr != nil {
					errCh <- reqErr
					return
				}
				if status != http.StatusOK {
					errCh <- fmt.Errorf("status=%d url=%s", status, url)
				}
			}(reqURL)
		}
	}
	wg.Wait()
	close(errCh)
	for reqErr := range errCh {
		require.NoError(t, reqErr)
	}

	keys := mr.Keys()
	require.True(t, hasCachePrefix(keys, "inbox:operator_test:"))
	require.True(t, hasCachePrefix(keys, "workbench:"+strings.ToLower(sessionID)))
	require.True(t, hasCachePrefix(keys, "dashboard:default:"))
}

func hasCachePrefix(keys []string, prefix string) bool {
	for _, key := range keys {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(key)), strings.ToLower(strings.TrimSpace(prefix))) {
			return true
		}
	}
	return false
}

func countCachePrefix(keys []string, prefix string) int {
	count := 0
	for _, key := range keys {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(key)), strings.ToLower(strings.TrimSpace(prefix))) {
			count++
		}
	}
	return count
}
