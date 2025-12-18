package ai_job

import (
	"context"
	"strings"
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
