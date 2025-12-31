package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/apiserver/biz"
	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/validation"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
)

func TestListAIJobs_LongPollTimeoutReturnsEmpty(t *testing.T) {
	baseURL, cleanup, _, client := newTestServer(t)
	defer cleanup()

	const waitSeconds = int64(1)
	listURL := fmt.Sprintf("%s/v1/ai/jobs?status=queued&limit=10&offset=0&wait_seconds=%d", baseURL, waitSeconds)

	started := time.Now()
	status, body, err := doJSONRequest(client, http.MethodGet, listURL, nil)
	elapsed := time.Since(started)

	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Empty(t, extractJobs(body))

	fixedLowerBound := time.Duration(waitSeconds)*time.Second - 200*time.Millisecond
	ratioLowerBound := time.Duration(waitSeconds) * 700 * time.Millisecond
	minAllowed := fixedLowerBound
	if ratioLowerBound < minAllowed {
		minAllowed = ratioLowerBound
	}
	require.GreaterOrEqual(t, elapsed, minAllowed, "long poll returned too early after %s", elapsed)
}

func TestListAIJobs_LongPollWakeupOnRun(t *testing.T) {
	baseURL, cleanup, s, client := newTestServer(t)
	defer cleanup()

	incident := createAIJobLongPollTestIncident(t, s)
	require.NotNil(t, s)
	require.NotNil(t, incident)

	type pollResult struct {
		status  int
		body    []byte
		elapsed time.Duration
		err     error
	}
	pollDone := make(chan pollResult, 1)

	const waitSeconds = int64(5)
	go func() {
		listURL := fmt.Sprintf("%s/v1/ai/jobs?status=queued&limit=10&offset=0&wait_seconds=%d", baseURL, waitSeconds)
		started := time.Now()
		status, body, err := doJSONRequest(client, http.MethodGet, listURL, nil)
		pollDone <- pollResult{
			status:  status,
			body:    body,
			elapsed: time.Since(started),
			err:     err,
		}
	}()

	time.Sleep(250 * time.Millisecond)

	now := time.Now().UTC().Unix()
	runBody := map[string]any{
		"incidentID":     incident.IncidentID,
		"idempotencyKey": fmt.Sprintf("idem-%s-%d", strings.ReplaceAll(t.Name(), "/", "-"), time.Now().UTC().UnixNano()),
		"timeRangeStart": map[string]any{
			"seconds": now - 1200,
			"nanos":   0,
		},
		"timeRangeEnd": map[string]any{
			"seconds": now,
			"nanos":   0,
		},
	}
	payload, err := json.Marshal(runBody)
	require.NoError(t, err)

	runURL := fmt.Sprintf("%s/v1/incidents/%s/ai:run", baseURL, incident.IncidentID)
	runStatus, runRespBody, err := doJSONRequest(client, http.MethodPost, runURL, payload)
	require.NoError(t, err)
	require.Equalf(t, http.StatusOK, runStatus, "run response: %s", string(runRespBody))

	select {
	case res := <-pollDone:
		require.NoError(t, res.err)
		require.Equal(t, http.StatusOK, res.status)
		require.Less(t, res.elapsed, 4500*time.Millisecond, "long poll did not wake up early")

		jobs := extractJobs(res.body)
		require.NotEmpty(t, jobs)
		jobID := extractString(jobs[0], "jobID", "job_id")
		require.NotEmpty(t, jobID)
	case <-time.After(7 * time.Second):
		t.Fatalf("long poll did not return in expected time window")
	}
}

func newTestServer(t *testing.T) (baseURL string, cleanup func(), s store.IStore, client *http.Client) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	store.ResetForTest()

	db := newAIJobLongPollTestDB(t)
	s = store.NewStore(db)
	val := validation.New(s)
	h := NewHandler(biz.NewBiz(s), val)

	engine := gin.New()
	h.ApplyTo(engine.Group("/v1"))

	server := httptest.NewServer(engine)
	cleanup = func() {
		server.Close()
		store.ResetForTest()
	}
	return server.URL, cleanup, s, server.Client()
}

func doJSONRequest(client *http.Client, method string, url string, payload []byte) (status int, body []byte, err error) {
	var reqBody io.Reader
	if payload != nil {
		reqBody = bytes.NewReader(payload)
	}

	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Scopes", "*")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, data, nil
}

func newAIJobLongPollTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf(
		"file:%s-%d?mode=memory&cache=shared",
		strings.ReplaceAll(t.Name(), "/", "_"),
		time.Now().UTC().UnixNano(),
	)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)

	require.NoError(t, db.Exec(`
CREATE TABLE incidents (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	incident_id TEXT NOT NULL DEFAULT '',
	tenant_id TEXT NOT NULL DEFAULT 'default',
	cluster TEXT NOT NULL DEFAULT 'default',
	namespace TEXT NOT NULL,
	workload_kind TEXT NOT NULL DEFAULT 'Deployment',
	workload_name TEXT NOT NULL,
	pod TEXT,
	node TEXT,
	service TEXT NOT NULL,
	environment TEXT NOT NULL DEFAULT 'prod',
	version TEXT,
	source TEXT NOT NULL DEFAULT 'alertmanager',
	alertname TEXT,
	fingerprint TEXT,
	active_fingerprint_key TEXT,
	rule_id TEXT,
	labels_json TEXT,
	annotations_json TEXT,
	severity TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'open',
	start_at DATETIME,
	end_at DATETIME,
	rca_status TEXT NOT NULL DEFAULT 'pending',
	root_cause_type TEXT,
	root_cause_summary TEXT,
	diagnosis_json TEXT,
	evidence_refs_json TEXT,
	action_status TEXT NOT NULL DEFAULT 'none',
	action_summary TEXT,
	trace_id TEXT,
	log_trace_key TEXT,
	change_id TEXT,
	created_by TEXT,
	approved_by TEXT,
	closed_by TEXT,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
)`).Error)
	require.NoError(t, db.AutoMigrate(&model.AIJobM{}, &model.AIToolCallM{}))
	return db
}

func createAIJobLongPollTestIncident(t *testing.T, s store.IStore) *model.IncidentM {
	t.Helper()
	incident := &model.IncidentM{
		TenantID:     "default",
		Cluster:      "default",
		Namespace:    "default",
		WorkloadKind: "Deployment",
		WorkloadName: "demo",
		Service:      "demo",
		Environment:  "prod",
		Source:       "api",
		Severity:     "P1",
		Status:       "open",
		RCAStatus:    "pending",
		ActionStatus: "none",
	}
	require.NoError(t, s.Incident().Create(context.Background(), incident))
	require.NotEmpty(t, incident.IncidentID)
	return incident
}

func extractJobs(body []byte) []map[string]any {
	container := extractDataContainer(body)
	if container == nil {
		return nil
	}

	rawJobs, ok := container["jobs"]
	if !ok {
		return nil
	}
	list, ok := rawJobs.([]any)
	if !ok {
		return nil
	}

	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, obj)
	}
	return out
}

func extractDataContainer(body []byte) map[string]any {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}
	if data, ok := payload["data"].(map[string]any); ok {
		return data
	}
	return payload
}

func extractString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
