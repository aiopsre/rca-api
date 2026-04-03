package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

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

func TestOperatorReadAPIs_HighConcurrencyPressure(t *testing.T) {
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
	db := s.DB(context.Background())

	var queryCount int64
	queryCounterName := "test_cache_pressure_query_counter_" + strings.ReplaceAll(t.Name(), "/", "_")
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(queryCounterName, func(_ *gorm.DB) {
		atomic.AddInt64(&queryCount, 1)
	}))
	defer db.Callback().Query().Remove(queryCounterName)

	jobIDs := make([]string, 0, 10)
	sessionIDs := make([]string, 0, 10)
	for i := 0; i < 10; i++ {
		triggerType := "manual"
		triggerSource := "manual_api"
		if i%2 == 1 {
			triggerType = "follow_up"
			triggerSource = "follow_up_api"
		}
		jobID := createFailedTraceJob(
			t,
			aiBiz,
			incident.IncidentID,
			triggerType,
			triggerSource,
			fmt.Sprintf("user:pressure-%d", i),
			fmt.Sprintf("pressure run %d", i),
		)
		jobIDs = append(jobIDs, jobID)
		sessionID := mustHandlerSessionIDByJob(t, s, jobID)
		sessionIDs = append(sessionIDs, sessionID)
		_, err = sessionSvc.UpdateAssignment(context.Background(), &sessionbiz.UpdateAssignmentRequest{
			SessionID:  sessionID,
			Assignee:   fmt.Sprintf("user:oncall-%d", i%3),
			AssignedBy: strPtr("user:lead-pressure"),
		})
		require.NoError(t, err)
	}

	leftJobID := jobIDs[0]
	rightJobID := jobIDs[1]

	inboxHitBefore := gatherCounterValue(t, "rca_cache_operation_total", map[string]string{
		"op":     "get",
		"module": "inbox",
		"result": "hit",
	})
	inboxMissBefore := gatherCounterValue(t, "rca_cache_operation_total", map[string]string{
		"op":     "get",
		"module": "inbox",
		"result": "miss",
	})
	workbenchHitBefore := gatherCounterValue(t, "rca_cache_operation_total", map[string]string{
		"op":     "get",
		"module": "workbench",
		"result": "hit",
	})
	dashboardHitBefore := gatherCounterValue(t, "rca_cache_operation_total", map[string]string{
		"op":     "get",
		"module": "dashboard",
		"result": "hit",
	})
	historyHitBefore := gatherCounterValue(t, "rca_cache_operation_total", map[string]string{
		"op":     "get",
		"module": "history",
		"result": "hit",
	})
	queryBefore := atomic.LoadInt64(&queryCount)

	workers := 80
	loops := 6
	var wg sync.WaitGroup
	errCh := make(chan error, workers*loops)
	latencyCh := make(chan time.Duration, workers*loops)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for j := 0; j < loops; j++ {
				sessionID := sessionIDs[(worker+j)%len(sessionIDs)]
				jobID := jobIDs[(worker+j)%len(jobIDs)]
				var reqURL string
				switch j % 6 {
				case 0:
					reqURL = fmt.Sprintf("%s/v1/operator/inbox?offset=%d&limit=5", baseURL, (worker+j)%3)
				case 1:
					reqURL = fmt.Sprintf("%s/v1/sessions/%s/workbench?limit=10", baseURL, sessionID)
				case 2:
					reqURL = fmt.Sprintf("%s/v1/operator/dashboard?scan_limit=200", baseURL)
				case 3:
					reqURL = fmt.Sprintf("%s/v1/sessions/%s/history?offset=0&limit=10&order=desc", baseURL, sessionID)
				case 4:
					reqURL = fmt.Sprintf("%s/v1/ai/jobs/%s/trace", baseURL, jobID)
				default:
					reqURL = fmt.Sprintf(
						"%s/v1/ai/jobs:trace-compare?left_job_id=%s&right_job_id=%s",
						baseURL,
						leftJobID,
						rightJobID,
					)
				}
				start := time.Now()
				status, _, reqErr := doJSONRequest(client, http.MethodGet, reqURL, nil)
				latencyCh <- time.Since(start)
				if reqErr != nil {
					errCh <- reqErr
					continue
				}
				if status != http.StatusOK {
					errCh <- fmt.Errorf("status=%d url=%s", status, reqURL)
				}
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	close(latencyCh)
	for reqErr := range errCh {
		require.NoError(t, reqErr)
	}

	var count int64
	var sum time.Duration
	var max time.Duration
	for latency := range latencyCh {
		sum += latency
		count++
		if latency > max {
			max = latency
		}
	}
	require.Greater(t, count, int64(0))
	avg := sum / time.Duration(count)
	t.Logf("pressure_result requests=%d avg=%s max=%s", count, avg, max)
	require.LessOrEqual(t, max, 2*time.Second)

	inboxHitAfter := gatherCounterValue(t, "rca_cache_operation_total", map[string]string{
		"op":     "get",
		"module": "inbox",
		"result": "hit",
	})
	inboxMissAfter := gatherCounterValue(t, "rca_cache_operation_total", map[string]string{
		"op":     "get",
		"module": "inbox",
		"result": "miss",
	})
	hitDelta := inboxHitAfter - inboxHitBefore
	missDelta := inboxMissAfter - inboxMissBefore
	workbenchHitAfter := gatherCounterValue(t, "rca_cache_operation_total", map[string]string{
		"op":     "get",
		"module": "workbench",
		"result": "hit",
	})
	dashboardHitAfter := gatherCounterValue(t, "rca_cache_operation_total", map[string]string{
		"op":     "get",
		"module": "dashboard",
		"result": "hit",
	})
	historyHitAfter := gatherCounterValue(t, "rca_cache_operation_total", map[string]string{
		"op":     "get",
		"module": "history",
		"result": "hit",
	})
	queryAfter := atomic.LoadInt64(&queryCount)
	queryDelta := queryAfter - queryBefore
	t.Logf(
		"cache_delta inbox_hit=%.0f inbox_miss=%.0f workbench_hit=%.0f dashboard_hit=%.0f history_hit=%.0f db_query_delta=%d",
		hitDelta,
		missDelta,
		workbenchHitAfter-workbenchHitBefore,
		dashboardHitAfter-dashboardHitBefore,
		historyHitAfter-historyHitBefore,
		queryDelta,
	)
	require.Greater(t, hitDelta, float64(0))
	require.Greater(t, missDelta, float64(0))
	require.Greater(t, queryDelta, int64(0))
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

func gatherCounterValue(t *testing.T, metricName string, labels map[string]string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if strings.TrimSpace(family.GetName()) != strings.TrimSpace(metricName) {
			continue
		}
		for _, metric := range family.GetMetric() {
			matched := true
			for key, value := range labels {
				if !labelPairsContain(metric.GetLabel(), key, value) {
					matched = false
					break
				}
			}
			if !matched || metric.GetCounter() == nil {
				continue
			}
			return metric.GetCounter().GetValue()
		}
	}
	return 0
}

func labelPairsContain(pairs []*dto.LabelPair, key string, value string) bool {
	for _, pair := range pairs {
		if strings.TrimSpace(pair.GetName()) == strings.TrimSpace(key) &&
			strings.TrimSpace(pair.GetValue()) == strings.TrimSpace(value) {
			return true
		}
	}
	return false
}
