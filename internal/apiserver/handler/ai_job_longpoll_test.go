package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"zk8s.com/rca-api/internal/apiserver/biz"
	"zk8s.com/rca-api/internal/apiserver/model"
	"zk8s.com/rca-api/internal/apiserver/pkg/validation"
	"zk8s.com/rca-api/internal/apiserver/store"
)

func TestListAIJobs_LongPollTimeoutReturnsEmpty(t *testing.T) {
	engine, _, _ := newAIJobLongPollTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/ai/jobs?status=queued&limit=10&offset=0&wait_seconds=1", nil)
	req.Header.Set("X-Scopes", "*")

	started := time.Now()
	resp := httptest.NewRecorder()
	engine.ServeHTTP(resp, req)
	elapsed := time.Since(started)

	require.Equal(t, http.StatusOK, resp.Code)
	if elapsed < 900*time.Millisecond {
		t.Logf("long poll returned early after %s", elapsed)
	}
	require.Empty(t, extractJobs(resp.Body.Bytes()))
}

func TestListAIJobs_LongPollWakeupOnRun(t *testing.T) {
	engine, s, incident := newAIJobLongPollTestServer(t)
	require.NotNil(t, s)
	require.NotNil(t, incident)

	runErr := make(chan error, 1)
	go func() {
		time.Sleep(200 * time.Millisecond)

		now := time.Now().UTC().Unix()
		runBody := map[string]any{
			"incidentID":     incident.IncidentID,
			"idempotencyKey": "idem-" + strings.ReplaceAll(t.Name(), "/", "-"),
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
		if err != nil {
			runErr <- err
			return
		}

		runReq := httptest.NewRequest(
			http.MethodPost,
			"/v1/incidents/"+incident.IncidentID+"/ai:run",
			bytes.NewReader(payload),
		)
		runReq.Header.Set("Content-Type", "application/json")
		runReq.Header.Set("X-Scopes", "*")

		runResp := httptest.NewRecorder()
		engine.ServeHTTP(runResp, runReq)
		if runResp.Code != http.StatusOK {
			runErr <- &httpError{code: runResp.Code, body: runResp.Body.String()}
			return
		}
		runErr <- nil
	}()

	listReq := httptest.NewRequest(http.MethodGet, "/v1/ai/jobs?status=queued&limit=10&offset=0&wait_seconds=3", nil)
	listReq.Header.Set("X-Scopes", "*")

	started := time.Now()
	listResp := httptest.NewRecorder()
	engine.ServeHTTP(listResp, listReq)
	elapsed := time.Since(started)

	require.NoError(t, <-runErr)
	require.Equal(t, http.StatusOK, listResp.Code)
	require.Less(t, elapsed, 2500*time.Millisecond)

	jobs := extractJobs(listResp.Body.Bytes())
	require.NotEmpty(t, jobs)
	jobID := extractString(jobs[0], "jobID", "job_id")
	require.NotEmpty(t, jobID)
}

type httpError struct {
	code int
	body string
}

func (e *httpError) Error() string {
	return http.StatusText(e.code) + ": " + e.body
}

func newAIJobLongPollTestServer(t *testing.T) (*gin.Engine, store.IStore, *model.IncidentM) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db := newAIJobLongPollTestDB(t)
	s := store.NewStore(db)
	val := validation.New(s)
	h := NewHandler(biz.NewBiz(s), val)

	engine := gin.New()
	h.ApplyTo(engine.Group("/v1"))

	incident := createAIJobLongPollTestIncident(t, s)
	return engine, s, incident
}

func newAIJobLongPollTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
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
