package runtimecontract

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

func TestClaimStartRoundTrip(t *testing.T) {
	req := ClaimStartRequestFromAPI(&v1.StartAIJobRequest{JobID: "  job-1  "}, "  orc-1  ")
	require.Equal(t, "job-1", req.JobID)
	require.Equal(t, "orc-1", req.OrchestratorInstanceID)

	apiReq := req.ToAPIRequest()
	require.Equal(t, "job-1", apiReq.GetJobID())

	apiResp := ClaimStartResponse{}.ToAPIResponse()
	require.NotNil(t, apiResp)
}

func TestRenewHeartbeatRoundTrip(t *testing.T) {
	req := RenewHeartbeatRequestFromAPI(&v1.StartAIJobRequest{JobID: "  job-2  "}, "  orc-2  ")
	require.Equal(t, "job-2", req.JobID)
	require.Equal(t, "orc-2", req.OrchestratorInstanceID)

	apiReq := req.ToAPIRequest()
	require.Equal(t, "job-2", apiReq.GetJobID())

	apiResp := RenewHeartbeatResponse{}.ToAPIResponse()
	require.NotNil(t, apiResp)
}

func TestToolCallReportRoundTrip(t *testing.T) {
	req := ToolCallReportRequestFromAPI(&v1.CreateAIToolCallRequest{
		JobID:        "  job-1  ",
		Seq:          3,
		NodeName:     "  collect  ",
		ToolName:     "  mcp.query_metrics  ",
		RequestJSON:  "  {\"k\":\"v\"}  ",
		ResponseJSON: ptrString("  {\"ok\":true}  "),
		ResponseRef:  ptrString("  ref-1  "),
		Status:       "  OK  ",
		LatencyMs:    12,
		ErrorMessage: ptrString("  err  "),
		EvidenceIDs:  []string{"  evidence-1  ", "evidence-1", "", "  evidence-2"},
	}, "  orc-3  ")

	require.Equal(t, "job-1", req.JobID)
	require.Equal(t, int64(3), req.Seq)
	require.Equal(t, "collect", req.NodeName)
	require.Equal(t, "mcp.query_metrics", req.ToolName)
	require.Equal(t, "{\"k\":\"v\"}", req.RequestJSON)
	require.NotNil(t, req.ResponseJSON)
	require.Equal(t, "{\"ok\":true}", *req.ResponseJSON)
	require.NotNil(t, req.ResponseRef)
	require.Equal(t, "ref-1", *req.ResponseRef)
	require.Equal(t, "ok", req.Status)
	require.Equal(t, int64(12), req.LatencyMs)
	require.NotNil(t, req.ErrorMessage)
	require.Equal(t, "err", *req.ErrorMessage)
	require.Equal(t, "orc-3", req.OrchestratorInstanceID)
	require.Equal(t, []string{"evidence-1", "evidence-2"}, req.EvidenceIDs)

	apiReq := req.ToAPIRequest()
	require.Equal(t, "ok", apiReq.GetStatus())
	require.Equal(t, []string{"evidence-1", "evidence-2"}, apiReq.GetEvidenceIDs())
}

func TestFinalizeRoundTrip(t *testing.T) {
	req := FinalizeRequestFromAPI(&v1.FinalizeAIJobRequest{
		JobID:         "  job-3  ",
		Status:        "  SuCcEeDeD  ",
		OutputSummary: ptrString("  summary  "),
		DiagnosisJSON: ptrString("  {\"summary\":\"ok\"}  "),
		EvidenceIDs:   []string{"evidence-1", " evidence-1 ", " evidence-2 "},
		ErrorMessage:  ptrString("  maybe  "),
	}, "  orc-3  ")

	require.Equal(t, "job-3", req.JobID)
	require.Equal(t, "succeeded", req.Status)
	require.NotNil(t, req.OutputSummary)
	require.Equal(t, "summary", *req.OutputSummary)
	require.NotNil(t, req.DiagnosisJSON)
	require.Equal(t, "{\"summary\":\"ok\"}", *req.DiagnosisJSON)
	require.Equal(t, []string{"evidence-1", "evidence-2"}, req.EvidenceIDs)
	require.NotNil(t, req.ErrorMessage)
	require.Equal(t, "maybe", *req.ErrorMessage)
	require.Equal(t, "orc-3", req.OrchestratorInstanceID)

	apiReq := req.ToAPIRequest()
	require.Equal(t, "succeeded", apiReq.GetStatus())
	require.Equal(t, []string{"evidence-1", "evidence-2"}, apiReq.GetEvidenceIDs())
}

func TestEvidencePublishRoundTrip(t *testing.T) {
	start := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Second)
	end := start.Add(5 * time.Minute)
	req := EvidencePublishRequestFromAPI(&v1.SaveEvidenceRequest{
		IncidentID:     "  incident-1  ",
		IdempotencyKey: ptrString("  idem-1  "),
		JobID:          ptrString("  job-1  "),
		Type:           "  Logs  ",
		DatasourceID:   ptrString("  ds-1  "),
		QueryText:      "  query  ",
		QueryJSON:      ptrString("  {\"x\":1}  "),
		TimeRangeStart: timestamppb.New(start),
		TimeRangeEnd:   timestamppb.New(end),
		ResultJSON:     "  {\"rows\":1}  ",
		Summary:        ptrString("  summary  "),
		CreatedBy:      ptrString("  ai:job-1  "),
	})

	require.Equal(t, "incident-1", req.IncidentID)
	require.NotNil(t, req.IdempotencyKey)
	require.Equal(t, "idem-1", *req.IdempotencyKey)
	require.NotNil(t, req.JobID)
	require.Equal(t, "job-1", *req.JobID)
	require.Equal(t, "logs", req.Type)
	require.NotNil(t, req.DatasourceID)
	require.Equal(t, "ds-1", *req.DatasourceID)
	require.Equal(t, "query", req.QueryText)
	require.NotNil(t, req.QueryJSON)
	require.Equal(t, "{\"x\":1}", *req.QueryJSON)
	require.Equal(t, start, req.TimeRangeStart)
	require.Equal(t, end, req.TimeRangeEnd)
	require.Equal(t, "{\"rows\":1}", req.ResultJSON)
	require.NotNil(t, req.Summary)
	require.Equal(t, "summary", *req.Summary)
	require.NotNil(t, req.CreatedBy)
	require.Equal(t, "ai:job-1", *req.CreatedBy)

	apiReq := req.ToAPIRequest()
	require.Equal(t, "logs", apiReq.GetType())
	require.Equal(t, start, apiReq.GetTimeRangeStart().AsTime().UTC())
	require.Equal(t, end, apiReq.GetTimeRangeEnd().AsTime().UTC())
}

func TestNormalizeStringList(t *testing.T) {
	require.Nil(t, NormalizeStringList(nil))
	require.Equal(t, []string{"a", "b"}, NormalizeStringList([]string{" a ", "b", "a", "", " "}))
}

func ptrString(value string) *string {
	return &value
}
