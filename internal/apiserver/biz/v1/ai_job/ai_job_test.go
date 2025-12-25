package ai_job

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"zk8s.com/rca-api/internal/apiserver/model"
	"zk8s.com/rca-api/internal/apiserver/store"
	"zk8s.com/rca-api/internal/pkg/errno"
	v1 "zk8s.com/rca-api/pkg/api/apiserver/v1"
	"zk8s.com/rca-api/pkg/store/where"
)

func TestAIJobRunToolCallFinalize_Success(t *testing.T) {
	db := newAIJobTestDB(t)
	s := store.NewStore(db)
	biz := New(s)

	incident := createTestIncident(t, s)

	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-30 * time.Minute)

	runResp1, err := biz.Run(context.Background(), &v1.RunAIJobRequest{
		IncidentID:     incident.IncidentID,
		IdempotencyKey: ptrAIString("idem-ai-run-1"),
		Pipeline:       ptrAIString("basic_rca"),
		Trigger:        ptrAIString("manual"),
		TimeRangeStart: timestamppb.New(start),
		TimeRangeEnd:   timestamppb.New(end),
		InputHintsJSON: ptrAIString(`{"hint":"check error spike"}`),
		CreatedBy:      ptrAIString("user:tester"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, runResp1.JobID)

	// Idempotent run should return same job id.
	runResp2, err := biz.Run(context.Background(), &v1.RunAIJobRequest{
		IncidentID:     incident.IncidentID,
		IdempotencyKey: ptrAIString("idem-ai-run-1"),
		TimeRangeStart: timestamppb.New(start),
		TimeRangeEnd:   timestamppb.New(end),
	})
	require.NoError(t, err)
	require.Equal(t, runResp1.JobID, runResp2.JobID)

	_, err = biz.CreateToolCall(context.Background(), &v1.CreateAIToolCallRequest{
		JobID:        runResp1.JobID,
		Seq:          2,
		NodeName:     "logs_specialist",
		ToolName:     "evidence.queryLogs",
		RequestJSON:  `{"q":"error"}`,
		ResponseJSON: ptrAIString(`{"rows":10}`),
		Status:       "ok",
		LatencyMs:    12,
		EvidenceIDs:  []string{"evidence-2"},
	})
	require.NoError(t, err)

	_, err = biz.CreateToolCall(context.Background(), &v1.CreateAIToolCallRequest{
		JobID:       runResp1.JobID,
		Seq:         1,
		NodeName:    "metrics_specialist",
		ToolName:    "evidence.queryMetrics",
		RequestJSON: `{"q":"up"}`,
		Status:      "ok",
		LatencyMs:   8,
		EvidenceIDs: []string{"evidence-1"},
	})
	require.NoError(t, err)

	toolCalls, err := biz.ListToolCalls(context.Background(), &v1.ListAIToolCallsRequest{
		JobID:  runResp1.JobID,
		Offset: 0,
		Limit:  20,
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), toolCalls.TotalCount)
	require.Len(t, toolCalls.ToolCalls, 2)
	require.Equal(t, int64(1), toolCalls.ToolCalls[0].Seq)
	require.Equal(t, int64(2), toolCalls.ToolCalls[1].Seq)

	_, err = biz.Finalize(context.Background(), &v1.FinalizeAIJobRequest{
		JobID:         runResp1.JobID,
		Status:        "succeeded",
		OutputSummary: ptrAIString("db connection pool exhausted"),
		DiagnosisJSON: ptrAIString(`{
			"summary":"db connection pool exhausted",
			"root_cause":{
				"category":"db",
				"statement":"connection pool saturated",
				"confidence":0.9,
				"evidence_ids":["evidence-1","evidence-2"]
			},
			"timeline":[{"t":"2026-02-01T10:00:00Z","event":"alert_fired","ref":"alert-1"}],
			"hypotheses":[
				{
					"statement":"db pool limit reached",
					"confidence":0.9,
					"supporting_evidence_ids":["evidence-1","evidence-2"],
					"missing_evidence":[]
				}
			],
			"recommendations":[{"type":"readonly_check","action":"check pool config","risk":"low"}],
			"unknowns":[],
			"next_steps":["increase max open connections"]
		}`),
		EvidenceIDs: []string{"evidence-1", "evidence-2"},
	})
	require.NoError(t, err)

	jobResp, err := biz.Get(context.Background(), &v1.GetAIJobRequest{JobID: runResp1.JobID})
	require.NoError(t, err)
	require.Equal(t, "succeeded", jobResp.Job.Status)
	require.NotNil(t, jobResp.Job.OutputJSON)

	updatedIncident, err := s.Incident().Get(context.Background(), where.T(context.Background()).F("incident_id", incident.IncidentID))
	require.NoError(t, err)
	require.Equal(t, "done", updatedIncident.RCAStatus)
	require.NotNil(t, updatedIncident.DiagnosisJSON)
	require.NotNil(t, updatedIncident.RootCauseType)
	require.Equal(t, "db", *updatedIncident.RootCauseType)
}

func TestAIJobFinalize_RejectsInvalidDiagnosis(t *testing.T) {
	db := newAIJobTestDB(t)
	s := store.NewStore(db)
	biz := New(s)
	incident := createTestIncident(t, s)

	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-15 * time.Minute)

	runResp, err := biz.Run(context.Background(), &v1.RunAIJobRequest{
		IncidentID:     incident.IncidentID,
		TimeRangeStart: timestamppb.New(start),
		TimeRangeEnd:   timestamppb.New(end),
	})
	require.NoError(t, err)

	_, err = biz.Start(context.Background(), &v1.StartAIJobRequest{JobID: runResp.JobID})
	require.NoError(t, err)

	_, err = biz.Finalize(context.Background(), &v1.FinalizeAIJobRequest{
		JobID:  runResp.JobID,
		Status: "succeeded",
		DiagnosisJSON: ptrAIString(`{
			"summary":"bad diagnosis",
			"root_cause":{
				"category":"db",
				"statement":"pool exhausted",
				"confidence":0.8,
				"evidence_ids":["only-one"]
			}
		}`),
	})
	require.Error(t, err)
	require.Equal(t, errno.ErrAIJobInvalidDiagnosis, err)
}

func TestAIJobFinalize_MissingEvidenceTemplate(t *testing.T) {
	db := newAIJobTestDB(t)
	s := store.NewStore(db)
	biz := New(s)
	incident := createTestIncident(t, s)

	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-20 * time.Minute)

	runResp, err := biz.Run(context.Background(), &v1.RunAIJobRequest{
		IncidentID:     incident.IncidentID,
		TimeRangeStart: timestamppb.New(start),
		TimeRangeEnd:   timestamppb.New(end),
	})
	require.NoError(t, err)

	_, err = biz.Start(context.Background(), &v1.StartAIJobRequest{JobID: runResp.JobID})
	require.NoError(t, err)

	_, err = biz.Finalize(context.Background(), &v1.FinalizeAIJobRequest{
		JobID:  runResp.JobID,
		Status: "succeeded",
		DiagnosisJSON: ptrAIString(`{
			"schema_version":"1.0",
			"generated_at":"2026-02-07T00:00:00Z",
			"incident_id":"` + incident.IncidentID + `",
			"summary":"Insufficient evidence to determine root cause.",
			"root_cause":{
				"type":"missing_evidence",
				"category":"unknown",
				"summary":"Insufficient evidence to determine root cause.",
				"statement":"",
				"confidence":0.15,
				"evidence_ids":["evidence-placeholder-1"]
			},
			"missing_evidence":["logs","traces"],
			"hypotheses":[
				{
					"statement":"Evidence gap prevents confident root-cause attribution.",
					"confidence":0.15,
					"supporting_evidence_ids":["evidence-placeholder-1"],
					"missing_evidence":["logs","traces"]
				}
			],
			"recommendations":[{"type":"readonly_check","action":"collect more evidence","risk":"low"}],
			"unknowns":["root cause remains unknown"],
			"next_steps":["re-run after logs/traces are available"]
		}`),
		EvidenceIDs: []string{"evidence-placeholder-1"},
	})
	require.NoError(t, err)

	updatedIncident, err := s.Incident().Get(context.Background(), where.T(context.Background()).F("incident_id", incident.IncidentID))
	require.NoError(t, err)
	require.Equal(t, incidentRCAStatusDone, updatedIncident.RCAStatus)
	require.NotNil(t, updatedIncident.DiagnosisJSON)
	require.NotNil(t, updatedIncident.RootCauseType)
	require.Equal(t, "missing_evidence", *updatedIncident.RootCauseType)
	require.NotNil(t, updatedIncident.EvidenceRefsJSON)

	var diagnosis map[string]any
	require.NoError(t, json.Unmarshal([]byte(*updatedIncident.DiagnosisJSON), &diagnosis))
	root, ok := diagnosis["root_cause"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "missing_evidence", root["type"])
	require.Equal(t, 0.15, root["confidence"])
	missing, ok := diagnosis["missing_evidence"].([]any)
	require.True(t, ok)
	require.Len(t, missing, 2)
}

func TestAIJobFinalize_RejectsMissingEvidenceWithHighConfidence(t *testing.T) {
	db := newAIJobTestDB(t)
	s := store.NewStore(db)
	biz := New(s)
	incident := createTestIncident(t, s)

	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-10 * time.Minute)

	runResp, err := biz.Run(context.Background(), &v1.RunAIJobRequest{
		IncidentID:     incident.IncidentID,
		TimeRangeStart: timestamppb.New(start),
		TimeRangeEnd:   timestamppb.New(end),
	})
	require.NoError(t, err)

	_, err = biz.Start(context.Background(), &v1.StartAIJobRequest{JobID: runResp.JobID})
	require.NoError(t, err)

	_, err = biz.Finalize(context.Background(), &v1.FinalizeAIJobRequest{
		JobID:  runResp.JobID,
		Status: "succeeded",
		DiagnosisJSON: ptrAIString(`{
			"summary":"invalid missing evidence diagnosis",
			"root_cause":{
				"type":"missing_evidence",
				"category":"unknown",
				"statement":"definitive root cause",
				"confidence":0.5,
				"evidence_ids":["evidence-1"]
			},
			"missing_evidence":["logs"],
			"hypotheses":[
				{
					"statement":"need more logs",
					"confidence":0.2,
					"supporting_evidence_ids":["evidence-1"],
					"missing_evidence":["logs"]
				}
			]
		}`),
	})
	require.Error(t, err)
	require.Equal(t, errno.ErrAIJobInvalidDiagnosis, err)
}

func TestAIJobList_QueuedOrderAndPagination(t *testing.T) {
	db := newAIJobTestDB(t)
	s := store.NewStore(db)
	biz := New(s)

	incidentA := createTestIncident(t, s)
	incidentB := createTestIncident(t, s)
	incidentC := createTestIncident(t, s)

	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-10 * time.Minute)

	jobA, err := biz.Run(context.Background(), &v1.RunAIJobRequest{
		IncidentID:     incidentA.IncidentID,
		TimeRangeStart: timestamppb.New(start),
		TimeRangeEnd:   timestamppb.New(end),
	})
	require.NoError(t, err)

	jobB, err := biz.Run(context.Background(), &v1.RunAIJobRequest{
		IncidentID:     incidentB.IncidentID,
		TimeRangeStart: timestamppb.New(start),
		TimeRangeEnd:   timestamppb.New(end),
	})
	require.NoError(t, err)
	_, err = biz.Start(context.Background(), &v1.StartAIJobRequest{JobID: jobB.JobID})
	require.NoError(t, err)

	jobC, err := biz.Run(context.Background(), &v1.RunAIJobRequest{
		IncidentID:     incidentC.IncidentID,
		TimeRangeStart: timestamppb.New(start),
		TimeRangeEnd:   timestamppb.New(end),
	})
	require.NoError(t, err)

	resp, err := biz.List(context.Background(), &v1.ListAIJobsRequest{
		Status: "queued",
		Offset: 0,
		Limit:  10,
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), resp.TotalCount)
	require.Len(t, resp.Jobs, 2)
	require.Equal(t, "queued", resp.Jobs[0].Status)
	require.Equal(t, "queued", resp.Jobs[1].Status)
	require.Contains(t, []string{jobA.JobID, jobC.JobID}, resp.Jobs[0].JobID)
	require.Contains(t, []string{jobA.JobID, jobC.JobID}, resp.Jobs[1].JobID)

	first, err := s.AIJob().Get(context.Background(), where.T(context.Background()).F("job_id", resp.Jobs[0].JobID))
	require.NoError(t, err)
	second, err := s.AIJob().Get(context.Background(), where.T(context.Background()).F("job_id", resp.Jobs[1].JobID))
	require.NoError(t, err)
	require.Less(t, first.ID, second.ID)

	page2, err := biz.List(context.Background(), &v1.ListAIJobsRequest{
		Status: "queued",
		Offset: 1,
		Limit:  1,
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), page2.TotalCount)
	require.Len(t, page2.Jobs, 1)
	require.Equal(t, resp.Jobs[1].JobID, page2.Jobs[0].JobID)
}

func TestAIJobFinalize_DiagnosisWrittenTriggersNoticeDelivery(t *testing.T) {
	db := newAIJobTestDB(t)
	s := store.NewStore(db)
	biz := New(s)
	incident := createTestIncident(t, s)

	var hitCount atomic.Int32
	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hitCount.Add(1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer mockSrv.Close()

	require.NoError(t, s.NoticeChannel().Create(context.Background(), &model.NoticeChannelM{
		Name:        "notice-diagnosis-written",
		Type:        "webhook",
		Enabled:     true,
		EndpointURL: mockSrv.URL,
		TimeoutMs:   1000,
	}))

	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-30 * time.Minute)

	runResp, err := biz.Run(context.Background(), &v1.RunAIJobRequest{
		IncidentID:     incident.IncidentID,
		TimeRangeStart: timestamppb.New(start),
		TimeRangeEnd:   timestamppb.New(end),
	})
	require.NoError(t, err)

	_, err = biz.Start(context.Background(), &v1.StartAIJobRequest{JobID: runResp.GetJobID()})
	require.NoError(t, err)

	_, err = biz.Finalize(context.Background(), &v1.FinalizeAIJobRequest{
		JobID:  runResp.GetJobID(),
		Status: "succeeded",
		DiagnosisJSON: ptrAIString(`{
			"summary":"db pool exhausted",
			"root_cause":{
				"category":"db",
				"statement":"connection pool saturated",
				"confidence":0.9,
				"evidence_ids":["evidence-1","evidence-2"]
			},
			"timeline":[{"t":"2026-02-01T10:00:00Z","event":"alert_fired","ref":"alert-1"}],
			"hypotheses":[
				{
					"statement":"db pool limit reached",
					"confidence":0.9,
					"supporting_evidence_ids":["evidence-1","evidence-2"],
					"missing_evidence":[]
				}
			],
			"recommendations":[{"type":"readonly_check","action":"check pool config","risk":"low"}],
			"unknowns":[],
			"next_steps":["increase max open connections"]
		}`),
	})
	require.NoError(t, err)

	require.Equal(t, int32(1), hitCount.Load())

	total, deliveries, err := s.NoticeDelivery().List(context.Background(),
		where.T(context.Background()).P(0, 20).F("incident_id", incident.IncidentID).F("event_type", "diagnosis_written"))
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Equal(t, "succeeded", deliveries[0].Status)
	require.NotNil(t, deliveries[0].JobID)
	require.Equal(t, runResp.GetJobID(), *deliveries[0].JobID)
}

func newAIJobTestDB(t *testing.T) *gorm.DB {
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
	require.NoError(t, db.AutoMigrate(&model.NoticeChannelM{}, &model.NoticeDeliveryM{}))
	return db
}

func createTestIncident(t *testing.T, s store.IStore) *model.IncidentM {
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

func ptrAIString(v string) *string { return &v }
