package ai_job

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/onexstack/onexstack/pkg/errorsx"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	sessionbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/session"
	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	"github.com/aiopsre/rca-api/internal/pkg/contextx"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
	"github.com/aiopsre/rca-api/pkg/store/where"
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

	_, err = biz.CreateToolCall(orchestratorCtx(), &v1.CreateAIToolCallRequest{
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

	_, err = biz.CreateToolCall(orchestratorCtx(), &v1.CreateAIToolCallRequest{
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

	_, err = biz.Finalize(orchestratorCtx(), &v1.FinalizeAIJobRequest{
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

func TestAIJobRun_BindsExistingIncidentSession(t *testing.T) {
	db := newAIJobTestDB(t)
	s := store.NewStore(db)
	biz := New(s)
	incident := createTestIncident(t, s)

	sessionSvc := sessionbiz.New(s)
	ensureResp, err := sessionSvc.EnsureIncidentSession(context.Background(), &sessionbiz.EnsureIncidentSessionRequest{
		IncidentID: incident.IncidentID,
		Title:      ptrAIString("incident/" + incident.IncidentID),
	})
	require.NoError(t, err)
	require.NotNil(t, ensureResp.Session)

	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-15 * time.Minute)
	runResp, err := biz.Run(context.Background(), &v1.RunAIJobRequest{
		IncidentID:     incident.IncidentID,
		TimeRangeStart: timestamppb.New(start),
		TimeRangeEnd:   timestamppb.New(end),
	})
	require.NoError(t, err)
	require.NotEmpty(t, runResp.JobID)

	job, err := s.AIJob().Get(context.Background(), where.T(context.Background()).F("job_id", runResp.JobID))
	require.NoError(t, err)
	require.NotNil(t, job.SessionID)
	require.Equal(t, ensureResp.Session.SessionID, strings.TrimSpace(*job.SessionID))

	sessionObj, err := s.SessionContext().Get(context.Background(), where.T(context.Background()).F("session_id", ensureResp.Session.SessionID))
	require.NoError(t, err)
	require.NotNil(t, sessionObj.ActiveRunID)
	require.Equal(t, runResp.JobID, strings.TrimSpace(*sessionObj.ActiveRunID))
}

func TestAIJobRun_EnsuresIncidentSessionWhenMissing(t *testing.T) {
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
	require.NotEmpty(t, runResp.JobID)

	job, err := s.AIJob().Get(context.Background(), where.T(context.Background()).F("job_id", runResp.JobID))
	require.NoError(t, err)
	require.NotNil(t, job.SessionID)
	require.NotEmpty(t, strings.TrimSpace(*job.SessionID))

	sessionObj, err := s.SessionContext().GetByIncidentID(context.Background(), incident.IncidentID)
	require.NoError(t, err)
	require.Equal(t, strings.TrimSpace(*job.SessionID), sessionObj.SessionID)
	require.Equal(t, sessionbiz.SessionTypeIncident, sessionObj.SessionType)
	require.Equal(t, incident.IncidentID, sessionObj.BusinessKey)
	require.NotNil(t, sessionObj.ActiveRunID)
	require.Equal(t, runResp.JobID, strings.TrimSpace(*sessionObj.ActiveRunID))
}

func TestAIJobFinalize_UpdatesSessionContextAndClearsActiveRun(t *testing.T) {
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
	require.NotEmpty(t, runResp.JobID)

	_, err = biz.Start(orchestratorCtx(), &v1.StartAIJobRequest{JobID: runResp.JobID})
	require.NoError(t, err)

	_, err = biz.Finalize(orchestratorCtx(), &v1.FinalizeAIJobRequest{
		JobID:  runResp.JobID,
		Status: "succeeded",
		DiagnosisJSON: ptrAIString(`{
			"summary":"database pool saturation confirmed",
			"root_cause":{
				"type":"db_pool_exhausted",
				"category":"db",
				"summary":"connection pool saturated",
				"statement":"peak load exceeded pool size",
				"confidence":0.82,
				"evidence_ids":["evidence-1","evidence-2"]
			},
			"hypotheses":[
				{
					"statement":"pool limit reached",
					"confidence":0.82,
					"supporting_evidence_ids":["evidence-1","evidence-2"],
					"missing_evidence":[]
				}
			]
		}`),
		EvidenceIDs: []string{"evidence-1", "evidence-2"},
	})
	require.NoError(t, err)

	job, err := s.AIJob().Get(context.Background(), where.T(context.Background()).F("job_id", runResp.JobID))
	require.NoError(t, err)
	require.NotNil(t, job.SessionID)
	sessionObj, err := s.SessionContext().Get(context.Background(), where.T(context.Background()).F("session_id", strings.TrimSpace(*job.SessionID)))
	require.NoError(t, err)
	require.NotNil(t, sessionObj.LatestSummaryJSON)
	require.NotNil(t, sessionObj.PinnedEvidenceJSON)
	require.Nil(t, sessionObj.ActiveRunID)

	var latest map[string]any
	require.NoError(t, json.Unmarshal([]byte(*sessionObj.LatestSummaryJSON), &latest))
	require.Equal(t, "db_pool_exhausted", strings.TrimSpace(anyToString(latest["root_cause_type"])))
	require.Equal(t, "database pool saturation confirmed", strings.TrimSpace(anyToString(latest["summary"])))
	require.Equal(t, 0.82, latest["confidence"])
	refsAny, ok := latest["evidence_refs"].([]any)
	require.True(t, ok)
	require.Len(t, refsAny, 2)

	var pinned map[string]any
	require.NoError(t, json.Unmarshal([]byte(*sessionObj.PinnedEvidenceJSON), &pinned))
	require.Equal(t, "ai_job_finalize", strings.TrimSpace(anyToString(pinned["source"])))
	refsAny, ok = pinned["refs"].([]any)
	require.True(t, ok)
	require.Len(t, refsAny, 2)
}

func TestAIJobRunTraceAndDecisionTrace_MinimalStructuredPersistence(t *testing.T) {
	db := newAIJobTestDB(t)
	s := store.NewStore(db)
	biz := New(s)
	incident := createTestIncident(t, s)

	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-20 * time.Minute)
	runCtx := contextx.WithTriggerType(context.Background(), "manual")
	runCtx = contextx.WithTriggerSource(runCtx, "manual_api")
	runCtx = contextx.WithTriggerInitiator(runCtx, "user:tester")

	runResp, err := biz.Run(runCtx, &v1.RunAIJobRequest{
		IncidentID:     incident.IncidentID,
		TimeRangeStart: timestamppb.New(start),
		TimeRangeEnd:   timestamppb.New(end),
		CreatedBy:      ptrAIString("user:tester"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, runResp.JobID)

	job, err := s.AIJob().Get(context.Background(), where.T(context.Background()).F("job_id", runResp.JobID))
	require.NoError(t, err)
	require.NotNil(t, job.RunTraceJSON)

	var queuedTrace map[string]any
	require.NoError(t, json.Unmarshal([]byte(*job.RunTraceJSON), &queuedTrace))
	require.Equal(t, "manual", strings.TrimSpace(anyToString(queuedTrace["trigger_type"])))
	require.Equal(t, "manual_api", strings.TrimSpace(anyToString(queuedTrace["trigger_source"])))
	require.Equal(t, "user:tester", strings.TrimSpace(anyToString(queuedTrace["initiator"])))
	require.Equal(t, "queued", strings.TrimSpace(anyToString(queuedTrace["status"])))

	_, err = biz.Start(orchestratorCtx(), &v1.StartAIJobRequest{JobID: runResp.JobID})
	require.NoError(t, err)

	job, err = s.AIJob().Get(context.Background(), where.T(context.Background()).F("job_id", runResp.JobID))
	require.NoError(t, err)
	require.NotNil(t, job.RunTraceJSON)

	var runningTrace map[string]any
	require.NoError(t, json.Unmarshal([]byte(*job.RunTraceJSON), &runningTrace))
	require.Equal(t, "running", strings.TrimSpace(anyToString(runningTrace["status"])))
	require.NotEmpty(t, strings.TrimSpace(anyToString(runningTrace["started_at"])))
	require.Equal(t, "test-instance", strings.TrimSpace(anyToString(runningTrace["worker_id"])))

	_, err = biz.CreateToolCall(orchestratorCtx(), &v1.CreateAIToolCallRequest{
		JobID:        runResp.JobID,
		Seq:          1,
		NodeName:     "diagnosis",
		ToolName:     "evidence.queryMetrics",
		RequestJSON:  `{"q":"cpu"}`,
		ResponseJSON: ptrAIString(`{"series":1}`),
		Status:       "ok",
		LatencyMs:    5,
		EvidenceIDs:  []string{"evidence-1", "evidence-2"},
	})
	require.NoError(t, err)

	require.NoError(t, s.IncidentVerificationRun().Create(context.Background(), &model.IncidentVerificationRunM{
		RunID:            "verification-run-1",
		IncidentID:       incident.IncidentID,
		Actor:            "worker",
		Source:           "ai_job_finalize",
		StepIndex:        1,
		Tool:             "evidence.queryMetrics",
		Observed:         "ok",
		MeetsExpectation: true,
	}))
	require.NoError(t, s.IncidentVerificationRun().Create(context.Background(), &model.IncidentVerificationRunM{
		RunID:            "verification-run-2",
		IncidentID:       incident.IncidentID,
		Actor:            "worker",
		Source:           "ai_job_finalize",
		StepIndex:        2,
		Tool:             "evidence.queryLogs",
		Observed:         "ok",
		MeetsExpectation: true,
	}))

	_, err = biz.Finalize(orchestratorCtx(), &v1.FinalizeAIJobRequest{
		JobID:  runResp.JobID,
		Status: "succeeded",
		DiagnosisJSON: ptrAIString(`{
			"summary":"database pool saturation confirmed",
			"root_cause":{
				"type":"db_pool_exhausted",
				"category":"db",
				"summary":"connection pool saturated",
				"statement":"peak load exceeded pool size",
				"confidence":0.82,
				"evidence_ids":["evidence-1","evidence-2"]
			},
			"hypotheses":[
				{
					"statement":"pool limit reached",
					"confidence":0.82,
					"supporting_evidence_ids":["evidence-1","evidence-2"],
					"missing_evidence":[]
				}
			]
		}`),
		EvidenceIDs: []string{"evidence-1", "evidence-2"},
	})
	require.NoError(t, err)

	job, err = s.AIJob().Get(context.Background(), where.T(context.Background()).F("job_id", runResp.JobID))
	require.NoError(t, err)
	require.NotNil(t, job.RunTraceJSON)
	require.NotNil(t, job.DecisionTraceJSON)

	var finalizedTrace map[string]any
	require.NoError(t, json.Unmarshal([]byte(*job.RunTraceJSON), &finalizedTrace))
	require.Equal(t, "succeeded", strings.TrimSpace(anyToString(finalizedTrace["status"])))
	require.NotEmpty(t, strings.TrimSpace(anyToString(finalizedTrace["finished_at"])))
	require.Equal(t, 1, parseAnyInt(t, finalizedTrace["tool_call_count"]))
	require.Equal(t, 2, parseAnyInt(t, finalizedTrace["evidence_count"]))
	require.GreaterOrEqual(t, parseAnyInt(t, finalizedTrace["verification_count"]), 2)
	require.Equal(t, "manual", strings.TrimSpace(anyToString(finalizedTrace["trigger_type"])))
	require.Equal(t, "manual_api", strings.TrimSpace(anyToString(finalizedTrace["trigger_source"])))

	var decisionTrace map[string]any
	require.NoError(t, json.Unmarshal([]byte(*job.DecisionTraceJSON), &decisionTrace))
	require.Equal(t, "succeeded", strings.TrimSpace(anyToString(decisionTrace["status"])))
	require.Equal(t, "db_pool_exhausted", strings.TrimSpace(anyToString(decisionTrace["root_cause_type"])))
	require.Equal(t, "database pool saturation confirmed", strings.TrimSpace(anyToString(decisionTrace["root_cause_summary"])))
	require.Equal(t, 0.82, decisionTrace["confidence"])
	require.Equal(t, false, decisionTrace["human_review_required"])

	evidenceRefsAny, ok := decisionTrace["evidence_refs"].([]any)
	require.True(t, ok)
	require.Len(t, evidenceRefsAny, 2)
	verificationRefsAny, ok := decisionTrace["verification_refs"].([]any)
	require.True(t, ok)
	require.GreaterOrEqual(t, len(verificationRefsAny), 2)
}

func TestAIJobRunTrace_UsesReplayTriggerContext(t *testing.T) {
	db := newAIJobTestDB(t)
	s := store.NewStore(db)
	biz := New(s)
	incident := createTestIncident(t, s)

	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-15 * time.Minute)
	runCtx := contextx.WithTriggerType(context.Background(), "replay")
	runCtx = contextx.WithTriggerSource(runCtx, "replay_api")
	runCtx = contextx.WithTriggerInitiator(runCtx, "user:replay")

	runResp, err := biz.Run(runCtx, &v1.RunAIJobRequest{
		IncidentID:     incident.IncidentID,
		Trigger:        ptrAIString("replay"),
		TimeRangeStart: timestamppb.New(start),
		TimeRangeEnd:   timestamppb.New(end),
		CreatedBy:      ptrAIString("user:replay"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, runResp.JobID)

	job, err := s.AIJob().Get(context.Background(), where.T(context.Background()).F("job_id", runResp.JobID))
	require.NoError(t, err)
	require.NotNil(t, job.RunTraceJSON)

	var runTrace map[string]any
	require.NoError(t, json.Unmarshal([]byte(*job.RunTraceJSON), &runTrace))
	require.Equal(t, "replay", strings.TrimSpace(anyToString(runTrace["trigger_type"])))
	require.Equal(t, "replay_api", strings.TrimSpace(anyToString(runTrace["trigger_source"])))
	require.Equal(t, "user:replay", strings.TrimSpace(anyToString(runTrace["initiator"])))
}

func TestAIJobTraceReadModel_GetAndList(t *testing.T) {
	db := newAIJobTestDB(t)
	s := store.NewStore(db)
	biz := New(s)
	incident := createTestIncident(t, s)

	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-20 * time.Minute)

	runCtxReplay := contextx.WithTriggerType(context.Background(), "replay")
	runCtxReplay = contextx.WithTriggerSource(runCtxReplay, "replay_api")
	runCtxReplay = contextx.WithTriggerInitiator(runCtxReplay, "user:replay")
	replayResp, err := biz.Run(runCtxReplay, &v1.RunAIJobRequest{
		IncidentID:     incident.IncidentID,
		Trigger:        ptrAIString("replay"),
		TimeRangeStart: timestamppb.New(start),
		TimeRangeEnd:   timestamppb.New(end),
	})
	require.NoError(t, err)
	require.NotEmpty(t, replayResp.JobID)
	_, err = biz.Start(orchestratorCtx(), &v1.StartAIJobRequest{JobID: replayResp.JobID})
	require.NoError(t, err)
	_, err = biz.Finalize(orchestratorCtx(), &v1.FinalizeAIJobRequest{
		JobID:  replayResp.JobID,
		Status: "succeeded",
		DiagnosisJSON: ptrAIString(`{
			"summary":"database pool saturation confirmed",
			"root_cause":{
				"type":"db_pool_exhausted",
				"category":"db",
				"summary":"connection pool saturated",
				"statement":"peak load exceeded pool size",
				"confidence":0.82,
				"evidence_ids":["evidence-r1","evidence-r2"]
			},
			"hypotheses":[
				{
					"statement":"pool limit reached",
					"confidence":0.82,
					"supporting_evidence_ids":["evidence-r1","evidence-r2"],
					"missing_evidence":[]
				}
			]
		}`),
		EvidenceIDs: []string{"evidence-r1", "evidence-r2"},
	})
	require.NoError(t, err)

	runCtxFollowUp := contextx.WithTriggerType(context.Background(), "follow_up")
	runCtxFollowUp = contextx.WithTriggerSource(runCtxFollowUp, "follow_up_api")
	runCtxFollowUp = contextx.WithTriggerInitiator(runCtxFollowUp, "user:follow-up")
	followResp, err := biz.Run(runCtxFollowUp, &v1.RunAIJobRequest{
		IncidentID:     incident.IncidentID,
		Trigger:        ptrAIString("follow_up"),
		TimeRangeStart: timestamppb.New(start.Add(10 * time.Minute)),
		TimeRangeEnd:   timestamppb.New(end.Add(10 * time.Minute)),
	})
	require.NoError(t, err)
	require.NotEmpty(t, followResp.JobID)
	_, err = biz.Start(orchestratorCtx(), &v1.StartAIJobRequest{JobID: followResp.JobID})
	require.NoError(t, err)
	_, err = biz.Finalize(orchestratorCtx(), &v1.FinalizeAIJobRequest{
		JobID:         followResp.JobID,
		Status:        "failed",
		ErrorMessage:  ptrAIString("follow up evidence missing"),
		OutputSummary: ptrAIString("follow up failed"),
	})
	require.NoError(t, err)

	replayTrace, err := biz.GetTraceReadModel(context.Background(), &GetTraceReadModelRequest{JobID: replayResp.JobID})
	require.NoError(t, err)
	require.NotNil(t, replayTrace)
	require.NotNil(t, replayTrace.RunTrace)
	require.NotNil(t, replayTrace.DecisionTrace)
	require.Equal(t, "replay", replayTrace.RunTrace.TriggerType)
	require.Equal(t, "replay_api", replayTrace.RunTrace.TriggerSource)
	require.Equal(t, "db_pool_exhausted", replayTrace.DecisionTrace.RootCauseType)

	incidentSummaries, err := biz.ListTraceReadModels(context.Background(), &ListTraceReadModelsRequest{
		IncidentID: ptrAIString(incident.IncidentID),
		Limit:      10,
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, incidentSummaries.TotalCount, int64(2))
	require.Len(t, incidentSummaries.Summaries, 2)
	require.Equal(t, followResp.JobID, incidentSummaries.Summaries[0].JobID)
	require.Equal(t, "follow_up", incidentSummaries.Summaries[0].TriggerType)
	require.Equal(t, replayResp.JobID, incidentSummaries.Summaries[1].JobID)
	require.Equal(t, "replay", incidentSummaries.Summaries[1].TriggerType)

	replayJob, err := s.AIJob().Get(context.Background(), where.T(context.Background()).F("job_id", replayResp.JobID))
	require.NoError(t, err)
	require.NotNil(t, replayJob.SessionID)
	sessionSummaries, err := biz.ListTraceReadModels(context.Background(), &ListTraceReadModelsRequest{
		SessionID: replayJob.SessionID,
		Limit:     10,
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, sessionSummaries.TotalCount, int64(2))
}

func TestAIJobFinalize_DecisionTraceVerificationRefsPreferJobLinkedRuns(t *testing.T) {
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
	require.NotEmpty(t, runResp.JobID)
	_, err = biz.Start(orchestratorCtx(), &v1.StartAIJobRequest{JobID: runResp.JobID})
	require.NoError(t, err)

	targetJobID := runResp.JobID
	otherJobID := "ai-job-other"
	require.NoError(t, s.IncidentVerificationRun().Create(context.Background(), &model.IncidentVerificationRunM{
		RunID:            "verification-run-target",
		IncidentID:       incident.IncidentID,
		JobID:            &targetJobID,
		Actor:            "ai:" + targetJobID,
		Source:           "ai_job",
		StepIndex:        1,
		Tool:             "evidence.queryMetrics",
		Observed:         "ok",
		MeetsExpectation: true,
	}))
	require.NoError(t, s.IncidentVerificationRun().Create(context.Background(), &model.IncidentVerificationRunM{
		RunID:            "verification-run-other",
		IncidentID:       incident.IncidentID,
		JobID:            &otherJobID,
		Actor:            "ai:" + otherJobID,
		Source:           "ai_job",
		StepIndex:        1,
		Tool:             "evidence.queryLogs",
		Observed:         "ok",
		MeetsExpectation: true,
	}))

	_, err = biz.Finalize(orchestratorCtx(), &v1.FinalizeAIJobRequest{
		JobID:  runResp.JobID,
		Status: "succeeded",
		DiagnosisJSON: ptrAIString(`{
			"summary":"database pool saturation confirmed",
			"root_cause":{
				"type":"db_pool_exhausted",
				"category":"db",
				"summary":"connection pool saturated",
				"statement":"peak load exceeded pool size",
				"confidence":0.9,
				"evidence_ids":["evidence-1","evidence-2"]
			},
			"hypotheses":[
				{
					"statement":"pool limit reached",
					"confidence":0.9,
					"supporting_evidence_ids":["evidence-1","evidence-2"],
					"missing_evidence":[]
				}
			]
		}`),
		EvidenceIDs: []string{"evidence-1", "evidence-2"},
	})
	require.NoError(t, err)

	traceResp, err := biz.GetTraceReadModel(context.Background(), &GetTraceReadModelRequest{JobID: runResp.JobID})
	require.NoError(t, err)
	require.NotNil(t, traceResp.DecisionTrace)
	require.NotContains(t, traceResp.DecisionTrace.VerificationRefs, "verification-run-other")
	require.NotEmpty(t, traceResp.DecisionTrace.VerificationRefs)
	require.GreaterOrEqual(t, traceResp.RunTrace.VerificationCount, int64(1))
}

func TestAIJobFinalize_SessionPatchFailureIsBestEffort(t *testing.T) {
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
	require.NotEmpty(t, runResp.JobID)

	_, err = biz.Start(orchestratorCtx(), &v1.StartAIJobRequest{JobID: runResp.JobID})
	require.NoError(t, err)

	require.NoError(t, db.Exec("DROP TABLE session_contexts").Error)

	_, err = biz.Finalize(orchestratorCtx(), &v1.FinalizeAIJobRequest{
		JobID:  runResp.JobID,
		Status: "succeeded",
		DiagnosisJSON: ptrAIString(`{
			"summary":"service dependency timeout spike",
			"root_cause":{
				"category":"dependency",
				"statement":"downstream timeout rate increased",
				"confidence":0.5,
				"evidence_ids":["evidence-1"]
			}
		}`),
	})
	require.NoError(t, err)

	jobResp, err := biz.Get(context.Background(), &v1.GetAIJobRequest{JobID: runResp.JobID})
	require.NoError(t, err)
	require.Equal(t, "succeeded", jobResp.Job.Status)
}

func TestAIJobFinalize_InjectsPlaybookAndMirrorsToToolCall(t *testing.T) {
	db := newAIJobTestDB(t)
	s := store.NewStore(db)
	biz := New(s)

	incident := createTestIncident(t, s)
	createTestDatasource(t, s, "prometheus")

	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-20 * time.Minute)

	runResp, err := biz.Run(context.Background(), &v1.RunAIJobRequest{
		IncidentID:     incident.IncidentID,
		TimeRangeStart: timestamppb.New(start),
		TimeRangeEnd:   timestamppb.New(end),
	})
	require.NoError(t, err)
	require.NotEmpty(t, runResp.JobID)

	_, err = biz.CreateToolCall(orchestratorCtx(), &v1.CreateAIToolCallRequest{
		JobID:        runResp.JobID,
		Seq:          1,
		NodeName:     "synthesize",
		ToolName:     "diagnosis.generate",
		RequestJSON:  `{"job":"test"}`,
		ResponseJSON: ptrAIString(`{"result":"diagnosis_json_ready"}`),
		Status:       "ok",
		LatencyMs:    8,
	})
	require.NoError(t, err)

	_, err = biz.Finalize(orchestratorCtx(), &v1.FinalizeAIJobRequest{
		JobID:  runResp.JobID,
		Status: "succeeded",
		DiagnosisJSON: ptrAIString(`{
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
			"patterns":[{"type":"signal","value":"latency_spike","weight":0.7}],
			"timeline":[{"t":"2026-02-07T00:00:00Z","event":"evidence_gap_detected","ref":"evidence-placeholder-1"}],
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
	require.NotNil(t, updatedIncident.DiagnosisJSON)

	var diagnosis map[string]any
	require.NoError(t, json.Unmarshal([]byte(*updatedIncident.DiagnosisJSON), &diagnosis))

	verificationPlan, ok := diagnosis["verification_plan"].(map[string]any)
	require.True(t, ok)
	verificationSteps, ok := verificationPlan["steps"].([]any)
	require.True(t, ok)
	require.GreaterOrEqual(t, len(verificationSteps), 1)

	playbookObj, ok := diagnosis["playbook"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "t6", strings.TrimSpace(anyToString(playbookObj["version"])))
	itemsAny, ok := playbookObj["items"].([]any)
	require.True(t, ok)
	require.GreaterOrEqual(t, len(itemsAny), 1)

	firstItem, ok := itemsAny[0].(map[string]any)
	require.True(t, ok)
	require.NotEmpty(t, strings.TrimSpace(anyToString(firstItem["risk"])))
	stepsAny, ok := firstItem["steps"].([]any)
	require.True(t, ok)
	require.GreaterOrEqual(t, len(stepsAny), 1)
	for _, rawStep := range stepsAny {
		stepObj, stepOK := rawStep.(map[string]any)
		require.True(t, stepOK)
		require.LessOrEqual(t, len([]rune(strings.TrimSpace(anyToString(stepObj["text"])))), 256)
	}

	verificationAny, ok := firstItem["verification"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, verificationAny["use_verification_plan"])
	recommendedAny, ok := verificationAny["recommended_steps"].([]any)
	require.True(t, ok)
	require.GreaterOrEqual(t, len(recommendedAny), 1)
	recommendedIdx := parseAnyInt(t, recommendedAny[0])
	require.GreaterOrEqual(t, recommendedIdx, 0)
	require.Less(t, recommendedIdx, len(verificationSteps))
	require.LessOrEqual(t, len([]rune(strings.TrimSpace(anyToString(verificationAny["expected_outcome"])))), 256)

	playbookRaw, err := json.Marshal(playbookObj)
	require.NoError(t, err)
	lowered := strings.ToLower(string(playbookRaw))
	require.NotContains(t, lowered, "secret")
	require.NotContains(t, lowered, "token")
	require.NotContains(t, lowered, "authorization")
	require.NotContains(t, lowered, "headers")

	toolCallsResp, err := biz.ListToolCalls(context.Background(), &v1.ListAIToolCallsRequest{
		JobID:  runResp.JobID,
		Offset: 0,
		Limit:  20,
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(toolCallsResp.ToolCalls), 1)

	mirroredFound := false
	for _, item := range toolCallsResp.ToolCalls {
		responseJSON := strings.TrimSpace(item.GetResponseJSON())
		if responseJSON == "" {
			continue
		}
		var responseObj map[string]any
		if unmarshalErr := json.Unmarshal([]byte(responseJSON), &responseObj); unmarshalErr != nil {
			continue
		}
		if pb, has := responseObj["playbook"].(map[string]any); has {
			require.Equal(t, "t6", strings.TrimSpace(anyToString(pb["version"])))
			mirroredFound = true
			break
		}
	}
	require.True(t, mirroredFound)
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

	_, err = biz.Start(orchestratorCtx(), &v1.StartAIJobRequest{JobID: runResp.JobID})
	require.NoError(t, err)

	_, err = biz.Finalize(orchestratorCtx(), &v1.FinalizeAIJobRequest{
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

	_, err = biz.Start(orchestratorCtx(), &v1.StartAIJobRequest{JobID: runResp.JobID})
	require.NoError(t, err)

	_, err = biz.Finalize(orchestratorCtx(), &v1.FinalizeAIJobRequest{
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

	_, err = biz.Start(orchestratorCtx(), &v1.StartAIJobRequest{JobID: runResp.JobID})
	require.NoError(t, err)

	_, err = biz.Finalize(orchestratorCtx(), &v1.FinalizeAIJobRequest{
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

func TestAIJobFinalize_RejectsMissingEvidenceWithoutTopLevelMissingEvidence(t *testing.T) {
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

	_, err = biz.Start(orchestratorCtx(), &v1.StartAIJobRequest{JobID: runResp.JobID})
	require.NoError(t, err)

	_, err = biz.Finalize(orchestratorCtx(), &v1.FinalizeAIJobRequest{
		JobID:  runResp.JobID,
		Status: "succeeded",
		DiagnosisJSON: ptrAIString(`{
			"summary":"invalid missing evidence diagnosis",
			"root_cause":{
				"type":"missing_evidence",
				"category":"unknown",
				"summary":"insufficient evidence",
				"statement":"",
				"confidence":0.2,
				"evidence_ids":["evidence-1"]
			},
			"missing_evidence":[],
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

func TestAIJobFinalize_RejectsMissingEvidenceWithTooManyMissingEvidenceItems(t *testing.T) {
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

	_, err = biz.Start(orchestratorCtx(), &v1.StartAIJobRequest{JobID: runResp.JobID})
	require.NoError(t, err)

	missing := make([]string, 0, maxMissingEvidenceItems+1)
	for i := 0; i < maxMissingEvidenceItems+1; i++ {
		missing = append(missing, fmt.Sprintf("missing-%d", i))
	}
	missingJSON, err := json.Marshal(missing)
	require.NoError(t, err)

	diagnosis := fmt.Sprintf(`{
		"summary":"invalid missing evidence diagnosis",
		"root_cause":{
			"type":"missing_evidence",
			"category":"unknown",
			"summary":"insufficient evidence",
			"statement":"",
			"confidence":0.2,
			"evidence_ids":["evidence-1"]
		},
		"missing_evidence":%s
	}`, string(missingJSON))

	_, err = biz.Finalize(orchestratorCtx(), &v1.FinalizeAIJobRequest{
		JobID:         runResp.JobID,
		Status:        "succeeded",
		DiagnosisJSON: ptrAIString(diagnosis),
	})
	require.Error(t, err)
	require.Equal(t, errno.ErrAIJobInvalidDiagnosis, err)
}

func TestAIJobFinalize_ConflictEvidenceTemplate(t *testing.T) {
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

	_, err = biz.Start(orchestratorCtx(), &v1.StartAIJobRequest{JobID: runResp.JobID})
	require.NoError(t, err)

	evidenceIDMetrics := createTestEvidence(t, s, incident.IncidentID, "metrics", "metrics spike")
	evidenceIDLogs := createTestEvidence(t, s, incident.IncidentID, "logs", "logs do not corroborate")

	diagnosis := fmt.Sprintf(`{
		"schema_version":"1.0",
		"generated_at":"2026-02-07T00:00:00Z",
		"incident_id":"%s",
		"summary":"Evidence signals conflict: metrics indicate degradation while logs/traces do not corroborate within the same window.",
		"root_cause":{
			"type":"conflict_evidence",
			"category":"unknown",
			"summary":"metrics vs logs/traces conflict within aligned time window",
			"statement":"",
			"confidence":0.25,
			"evidence_ids":["%s","%s"]
		},
		"missing_evidence":[
			"align metrics/logs/traces time window and re-query within the same interval",
			"collect upstream/downstream traces or confirm tracing sampling/drop"
		],
		"hypotheses":[
			{
				"statement":"Current evidence is conflicting and insufficient for a decisive root cause.",
				"confidence":0.25,
				"supporting_evidence_ids":["%s","%s"],
				"missing_evidence":[
					"align metrics/logs/traces time window and re-query within the same interval"
				]
			}
		],
		"recommendations":[{"type":"readonly_check","action":"collect more evidence","risk":"low"}],
		"unknowns":["root cause remains uncertain due to conflicting evidence"],
		"next_steps":["re-run after aligned window evidence collection"]
	}`, incident.IncidentID, evidenceIDMetrics, evidenceIDLogs, evidenceIDMetrics, evidenceIDLogs)

	_, err = biz.Finalize(orchestratorCtx(), &v1.FinalizeAIJobRequest{
		JobID:         runResp.JobID,
		Status:        "succeeded",
		DiagnosisJSON: ptrAIString(diagnosis),
	})
	require.NoError(t, err)

	updatedIncident, err := s.Incident().Get(context.Background(), where.T(context.Background()).F("incident_id", incident.IncidentID))
	require.NoError(t, err)
	require.Equal(t, incidentRCAStatusDone, updatedIncident.RCAStatus)
	require.NotNil(t, updatedIncident.RootCauseType)
	require.Equal(t, "conflict_evidence", *updatedIncident.RootCauseType)
	require.NotNil(t, updatedIncident.DiagnosisJSON)
	require.NotNil(t, updatedIncident.EvidenceRefsJSON)

	var diagnosisObj map[string]any
	require.NoError(t, json.Unmarshal([]byte(*updatedIncident.DiagnosisJSON), &diagnosisObj))
	root, ok := diagnosisObj["root_cause"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "conflict_evidence", root["type"])
	require.Equal(t, 0.25, root["confidence"])
	topMissing, ok := diagnosisObj["missing_evidence"].([]any)
	require.True(t, ok)
	require.Len(t, topMissing, 2)

	var refs map[string]any
	require.NoError(t, json.Unmarshal([]byte(*updatedIncident.EvidenceRefsJSON), &refs))
	refsIDsAny, ok := refs["evidence_ids"].([]any)
	require.True(t, ok)
	refsIDs := make([]string, 0, len(refsIDsAny))
	for _, item := range refsIDsAny {
		if value, ok := item.(string); ok {
			refsIDs = append(refsIDs, value)
		}
	}
	require.Contains(t, refsIDs, evidenceIDMetrics)
	require.Contains(t, refsIDs, evidenceIDLogs)
}

func TestAIJobFinalize_RejectsConflictEvidenceWithHighConfidence(t *testing.T) {
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

	_, err = biz.Start(orchestratorCtx(), &v1.StartAIJobRequest{JobID: runResp.JobID})
	require.NoError(t, err)

	evidenceID := createTestEvidence(t, s, incident.IncidentID, "metrics", "metrics spike")
	diagnosis := fmt.Sprintf(`{
		"summary":"invalid conflict diagnosis",
		"root_cause":{
			"type":"conflict_evidence",
			"category":"unknown",
			"summary":"metrics vs logs conflict",
			"statement":"definitive root cause",
			"confidence":0.5,
			"evidence_ids":["%s"]
		},
		"missing_evidence":["collect traces in same time window"],
		"hypotheses":[
			{
				"statement":"evidence conflicts",
				"confidence":0.2,
				"supporting_evidence_ids":["%s"],
				"missing_evidence":["collect traces in same time window"]
			}
		]
	}`, evidenceID, evidenceID)

	_, err = biz.Finalize(orchestratorCtx(), &v1.FinalizeAIJobRequest{
		JobID:         runResp.JobID,
		Status:        "succeeded",
		DiagnosisJSON: ptrAIString(diagnosis),
	})
	require.Error(t, err)
	require.Equal(t, errno.ErrAIJobInvalidDiagnosis, err)
}

func TestAIJobFinalize_RejectsConflictEvidenceWithoutMissingEvidence(t *testing.T) {
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

	_, err = biz.Start(orchestratorCtx(), &v1.StartAIJobRequest{JobID: runResp.JobID})
	require.NoError(t, err)

	evidenceID := createTestEvidence(t, s, incident.IncidentID, "logs", "logs sample")
	diagnosis := fmt.Sprintf(`{
		"summary":"invalid conflict diagnosis",
		"root_cause":{
			"type":"conflict_evidence",
			"category":"unknown",
			"summary":"metrics vs logs conflict",
			"statement":"",
			"confidence":0.2,
			"evidence_ids":["%s"]
		},
		"missing_evidence":[],
		"hypotheses":[
			{
				"statement":"evidence conflicts",
				"confidence":0.2,
				"supporting_evidence_ids":["%s"],
				"missing_evidence":[]
			}
		]
	}`, evidenceID, evidenceID)

	_, err = biz.Finalize(orchestratorCtx(), &v1.FinalizeAIJobRequest{
		JobID:         runResp.JobID,
		Status:        "succeeded",
		DiagnosisJSON: ptrAIString(diagnosis),
	})
	require.Error(t, err)
	require.Equal(t, errno.ErrAIJobInvalidDiagnosis, err)
}

func TestAIJobFinalize_RejectsConflictEvidenceWithTooManyMissingEvidenceItems(t *testing.T) {
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

	_, err = biz.Start(orchestratorCtx(), &v1.StartAIJobRequest{JobID: runResp.JobID})
	require.NoError(t, err)

	missing := make([]string, 0, maxMissingEvidenceItems+1)
	for i := 0; i < maxMissingEvidenceItems+1; i++ {
		missing = append(missing, fmt.Sprintf("missing-%d", i))
	}
	missingJSON, err := json.Marshal(missing)
	require.NoError(t, err)

	diagnosis := fmt.Sprintf(`{
		"summary":"invalid conflict diagnosis",
		"root_cause":{
			"type":"conflict_evidence",
			"category":"unknown",
			"summary":"metrics vs logs conflict",
			"statement":"",
			"confidence":0.2,
			"evidence_ids":["evidence-1"]
		},
		"missing_evidence":%s
	}`, string(missingJSON))

	_, err = biz.Finalize(orchestratorCtx(), &v1.FinalizeAIJobRequest{
		JobID:         runResp.JobID,
		Status:        "succeeded",
		DiagnosisJSON: ptrAIString(diagnosis),
	})
	require.Error(t, err)
	require.Equal(t, errno.ErrAIJobInvalidDiagnosis, err)
}

func TestAIJobFinalize_RejectsConflictEvidenceWithUnknownEvidenceID(t *testing.T) {
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

	_, err = biz.Start(orchestratorCtx(), &v1.StartAIJobRequest{JobID: runResp.JobID})
	require.NoError(t, err)

	_, err = biz.Finalize(orchestratorCtx(), &v1.FinalizeAIJobRequest{
		JobID:  runResp.JobID,
		Status: "succeeded",
		DiagnosisJSON: ptrAIString(`{
			"summary":"invalid conflict diagnosis",
			"root_cause":{
				"type":"conflict_evidence",
				"category":"unknown",
				"summary":"metrics vs logs conflict",
				"statement":"",
				"confidence":0.2,
				"evidence_ids":["evidence-not-found"]
			},
			"missing_evidence":["collect traces in same time window"],
			"hypotheses":[
				{
					"statement":"evidence conflicts",
					"confidence":0.2,
					"supporting_evidence_ids":["evidence-not-found"],
					"missing_evidence":["collect traces in same time window"]
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
	_, err = biz.Start(orchestratorCtx(), &v1.StartAIJobRequest{JobID: jobB.JobID})
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

	require.NoError(t, s.NoticeChannel().Create(context.Background(), &model.NoticeChannelM{
		Name:        "notice-diagnosis-written",
		Type:        "webhook",
		Enabled:     true,
		EndpointURL: "http://127.0.0.1:19999/hook",
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

	_, err = biz.Start(orchestratorCtx(), &v1.StartAIJobRequest{JobID: runResp.GetJobID()})
	require.NoError(t, err)

	_, err = biz.Finalize(orchestratorCtx(), &v1.FinalizeAIJobRequest{
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

	total, deliveries, err := s.NoticeDelivery().List(context.Background(),
		where.T(context.Background()).P(0, 20).F("incident_id", incident.IncidentID).F("event_type", "diagnosis_written"))
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Equal(t, "pending", deliveries[0].Status)
	require.Equal(t, int64(0), deliveries[0].Attempts)
	require.NotNil(t, deliveries[0].JobID)
	require.Equal(t, runResp.GetJobID(), *deliveries[0].JobID)
}

func TestAIJobStart_MultiOwnerClaimAndRenew(t *testing.T) {
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

	ownerA := contextx.WithOrchestratorInstanceID(context.Background(), "orc-a")
	ownerB := contextx.WithOrchestratorInstanceID(context.Background(), "orc-b")

	_, err = biz.Start(ownerA, &v1.StartAIJobRequest{JobID: runResp.JobID})
	require.NoError(t, err)

	_, err = biz.Start(ownerB, &v1.StartAIJobRequest{JobID: runResp.JobID})
	require.Error(t, err)
	require.Equal(t, errno.ErrAIJobInvalidTransition, err)

	_, err = biz.Start(ownerA, &v1.StartAIJobRequest{JobID: runResp.JobID})
	require.NoError(t, err)
}

func TestAIJobStart_SameOwnerIdempotent(t *testing.T) {
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

	_, err = biz.Start(orchestratorCtx(), &v1.StartAIJobRequest{JobID: runResp.JobID})
	require.NoError(t, err)

	_, err = biz.Start(orchestratorCtx(), &v1.StartAIJobRequest{JobID: runResp.JobID})
	require.NoError(t, err)
}

func TestAIJobWriteOps_MissingOwnerRejected(t *testing.T) {
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
	require.Equal(t, errorsx.ErrInvalidArgument, err)

	_, err = biz.Renew(context.Background(), &v1.StartAIJobRequest{JobID: runResp.JobID})
	require.Equal(t, errorsx.ErrInvalidArgument, err)

	_, err = biz.CreateToolCall(context.Background(), &v1.CreateAIToolCallRequest{
		JobID:       runResp.JobID,
		Seq:         1,
		NodeName:    "metrics_specialist",
		ToolName:    "evidence.queryMetrics",
		RequestJSON: `{"q":"up"}`,
		Status:      "ok",
		LatencyMs:   8,
	})
	require.Equal(t, errorsx.ErrInvalidArgument, err)

	_, err = biz.Finalize(context.Background(), &v1.FinalizeAIJobRequest{
		JobID:  runResp.JobID,
		Status: "failed",
	})
	require.Equal(t, errorsx.ErrInvalidArgument, err)

	_, err = biz.Cancel(context.Background(), &v1.CancelAIJobRequest{
		JobID: runResp.JobID,
	})
	require.Equal(t, errorsx.ErrInvalidArgument, err)
}

func TestAIJobList_ReclaimExpiredLeaseToQueued(t *testing.T) {
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

	ownerA := contextx.WithOrchestratorInstanceID(context.Background(), "orc-a")
	_, err = biz.Start(ownerA, &v1.StartAIJobRequest{JobID: runResp.JobID})
	require.NoError(t, err)

	expiredAt := time.Now().UTC().Add(-2 * time.Minute)
	require.NoError(t, s.DB(context.Background()).
		Model(&model.AIJobM{}).
		Where("job_id = ?", runResp.JobID).
		Update("lease_expires_at", expiredAt).Error)

	resp, err := biz.List(context.Background(), &v1.ListAIJobsRequest{
		Status: "queued",
		Offset: 0,
		Limit:  20,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	reclaimed, err := s.AIJob().Get(context.Background(), where.T(context.Background()).F("job_id", runResp.JobID))
	require.NoError(t, err)
	require.Equal(t, "queued", reclaimed.Status)
	require.Nil(t, reclaimed.LeaseOwner)
	require.Nil(t, reclaimed.LeaseExpiresAt)

	ownerB := contextx.WithOrchestratorInstanceID(context.Background(), "orc-b")
	_, err = biz.Start(ownerB, &v1.StartAIJobRequest{JobID: runResp.JobID})
	require.NoError(t, err)
}

func newAIJobTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	store.ResetForTest()
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
	require.NoError(t, db.AutoMigrate(
		&model.AIJobM{},
		&model.AIJobQueueSignalM{},
		&model.AIToolCallM{},
		&model.EvidenceM{},
		&model.DatasourceM{},
		&model.SessionContextM{},
		&model.IncidentVerificationRunM{},
	))
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

func createTestEvidence(t *testing.T, s store.IStore, incidentID string, evidenceType string, summary string) string {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	resultJSON := `{"source":"unit-test"}`
	evidence := &model.EvidenceM{
		IncidentID:      incidentID,
		Type:            evidenceType,
		QueryText:       "mock://unit-test",
		QueryHash:       "unit-test-query-hash",
		TimeRangeStart:  now.Add(-5 * time.Minute),
		TimeRangeEnd:    now,
		ResultJSON:      resultJSON,
		ResultSizeBytes: int64(len(resultJSON)),
		CreatedBy:       "system",
	}
	if strings.TrimSpace(summary) != "" {
		evidence.Summary = &summary
	}
	require.NoError(t, s.Evidence().Create(context.Background(), evidence))
	require.NotEmpty(t, evidence.EvidenceID)
	return evidence.EvidenceID
}

func createTestDatasource(t *testing.T, s store.IStore, dsType string) string {
	t.Helper()
	datasourceID := "datasource-" + strings.ReplaceAll(strings.ToLower(t.Name()), "/", "-")
	ds := &model.DatasourceM{
		DatasourceID: datasourceID,
		Type:         dsType,
		Name:         "unit-test-" + dsType,
		BaseURL:      "http://127.0.0.1:19095",
		AuthType:     "none",
		TimeoutMs:    3000,
		IsEnabled:    true,
	}
	require.NoError(t, s.Datasource().Create(context.Background(), ds))
	return ds.DatasourceID
}

func parseAnyInt(t *testing.T, value any) int {
	t.Helper()
	switch v := value.(type) {
	case int:
		return v
	case int8:
		return int(v)
	case int16:
		return int(v)
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float32:
		return int(v)
	case float64:
		return int(v)
	default:
		require.Failf(t, "parseAnyInt", "unsupported integer value type: %T", value)
		return 0
	}
}

func orchestratorCtx() context.Context {
	return contextx.WithOrchestratorInstanceID(context.Background(), "test-instance")
}

func ptrAIString(v string) *string { return &v }
