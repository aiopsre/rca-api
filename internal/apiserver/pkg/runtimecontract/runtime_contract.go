package runtimecontract

import (
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

const (
	JobStatusQueued    = "queued"
	JobStatusRunning   = "running"
	JobStatusSucceeded = "succeeded"
	JobStatusFailed    = "failed"
	JobStatusCanceled  = "canceled"

	ToolCallStatusOK       = "ok"
	ToolCallStatusError    = "error"
	ToolCallStatusTimeout  = "timeout"
	ToolCallStatusCanceled = "canceled"
)

// ClaimStartRequest defines the canonical runtime contract for worker claim/start.
type ClaimStartRequest struct {
	JobID                  string
	OrchestratorInstanceID string
}

// ClaimStartResponse is the canonical runtime contract response for claim/start.
type ClaimStartResponse struct{}

// RenewHeartbeatRequest defines the canonical runtime contract for worker heartbeat.
type RenewHeartbeatRequest struct {
	JobID                  string
	OrchestratorInstanceID string
}

// RenewHeartbeatResponse is the canonical runtime contract response for heartbeat.
type RenewHeartbeatResponse struct{}

// ToolCallReportRequest defines the canonical runtime contract for tool call reporting.
type ToolCallReportRequest struct {
	JobID                  string
	OrchestratorInstanceID string
	Seq                    int64
	NodeName               string
	ToolName               string
	RequestJSON            string
	ResponseJSON           *string
	ResponseRef            *string
	Status                 string
	LatencyMs              int64
	ErrorMessage           *string
	EvidenceIDs            []string
}

// ToolCallReportResponse defines the canonical runtime contract response for tool call reporting.
type ToolCallReportResponse struct {
	ToolCallID string
}

// FinalizeRequest defines the canonical runtime contract for job finalize.
type FinalizeRequest struct {
	JobID                  string
	OrchestratorInstanceID string
	Status                 string
	OutputSummary          *string
	DiagnosisJSON          *string
	EvidenceIDs            []string
	ErrorMessage           *string
}

// FinalizeResponse defines the canonical runtime contract response for finalize.
type FinalizeResponse struct{}

// EvidencePublishRequest defines the canonical runtime contract for evidence persistence.
type EvidencePublishRequest struct {
	IncidentID     string
	IdempotencyKey *string
	JobID          *string
	Type           string
	DatasourceID   *string
	QueryText      string
	QueryJSON      *string
	TimeRangeStart time.Time
	TimeRangeEnd   time.Time
	ResultJSON     string
	Summary        *string
	CreatedBy      *string
}

// EvidencePublishResponse defines the canonical runtime contract response for evidence persistence.
type EvidencePublishResponse struct {
	EvidenceID string
}

func NewClaimStartRequest(jobID, orchestratorInstanceID string) ClaimStartRequest {
	return ClaimStartRequest{
		JobID:                  strings.TrimSpace(jobID),
		OrchestratorInstanceID: strings.TrimSpace(orchestratorInstanceID),
	}
}

func ClaimStartRequestFromAPI(rq *v1.StartAIJobRequest, orchestratorInstanceID string) ClaimStartRequest {
	if rq == nil {
		return NewClaimStartRequest("", orchestratorInstanceID)
	}
	return NewClaimStartRequest(rq.GetJobID(), orchestratorInstanceID)
}

func (r ClaimStartRequest) ToAPIRequest() *v1.StartAIJobRequest {
	return &v1.StartAIJobRequest{
		JobID: strings.TrimSpace(r.JobID),
	}
}

func ClaimStartResponseFromAPI(_ *v1.StartAIJobResponse) ClaimStartResponse {
	return ClaimStartResponse{}
}

func (r ClaimStartResponse) ToAPIResponse() *v1.StartAIJobResponse {
	_ = r
	return &v1.StartAIJobResponse{}
}

func NewRenewHeartbeatRequest(jobID, orchestratorInstanceID string) RenewHeartbeatRequest {
	return RenewHeartbeatRequest{
		JobID:                  strings.TrimSpace(jobID),
		OrchestratorInstanceID: strings.TrimSpace(orchestratorInstanceID),
	}
}

func RenewHeartbeatRequestFromAPI(rq *v1.StartAIJobRequest, orchestratorInstanceID string) RenewHeartbeatRequest {
	if rq == nil {
		return NewRenewHeartbeatRequest("", orchestratorInstanceID)
	}
	return NewRenewHeartbeatRequest(rq.GetJobID(), orchestratorInstanceID)
}

func (r RenewHeartbeatRequest) ToAPIRequest() *v1.StartAIJobRequest {
	return &v1.StartAIJobRequest{
		JobID: strings.TrimSpace(r.JobID),
	}
}

func RenewHeartbeatResponseFromAPI(_ *v1.StartAIJobResponse) RenewHeartbeatResponse {
	return RenewHeartbeatResponse{}
}

func (r RenewHeartbeatResponse) ToAPIResponse() *v1.StartAIJobResponse {
	_ = r
	return &v1.StartAIJobResponse{}
}

func ToolCallReportRequestFromAPI(rq *v1.CreateAIToolCallRequest, orchestratorInstanceID string) ToolCallReportRequest {
	if rq == nil {
		return ToolCallReportRequest{
			OrchestratorInstanceID: strings.TrimSpace(orchestratorInstanceID),
		}
	}
	return ToolCallReportRequest{
		JobID:                  strings.TrimSpace(rq.GetJobID()),
		OrchestratorInstanceID: strings.TrimSpace(orchestratorInstanceID),
		Seq:                    rq.GetSeq(),
		NodeName:               strings.TrimSpace(rq.GetNodeName()),
		ToolName:               strings.TrimSpace(rq.GetToolName()),
		RequestJSON:            strings.TrimSpace(rq.GetRequestJSON()),
		ResponseJSON:           cloneOptionalTrimmedString(rq.ResponseJSON),
		ResponseRef:            cloneOptionalTrimmedString(rq.ResponseRef),
		Status:                 NormalizeLowerText(rq.GetStatus()),
		LatencyMs:              rq.GetLatencyMs(),
		ErrorMessage:           cloneOptionalTrimmedString(rq.ErrorMessage),
		EvidenceIDs:            NormalizeStringList(rq.GetEvidenceIDs()),
	}
}

func (r ToolCallReportRequest) ToAPIRequest() *v1.CreateAIToolCallRequest {
	return &v1.CreateAIToolCallRequest{
		JobID:        strings.TrimSpace(r.JobID),
		Seq:          r.Seq,
		NodeName:     strings.TrimSpace(r.NodeName),
		ToolName:     strings.TrimSpace(r.ToolName),
		RequestJSON:  strings.TrimSpace(r.RequestJSON),
		ResponseJSON: cloneOptionalTrimmedString(r.ResponseJSON),
		ResponseRef:  cloneOptionalTrimmedString(r.ResponseRef),
		Status:       NormalizeLowerText(r.Status),
		LatencyMs:    r.LatencyMs,
		ErrorMessage: cloneOptionalTrimmedString(r.ErrorMessage),
		EvidenceIDs:  NormalizeStringList(r.EvidenceIDs),
	}
}

func ToolCallReportResponseFromAPI(rq *v1.CreateAIToolCallResponse) ToolCallReportResponse {
	if rq == nil {
		return ToolCallReportResponse{}
	}
	return ToolCallReportResponse{
		ToolCallID: strings.TrimSpace(rq.GetToolCallID()),
	}
}

func (r ToolCallReportResponse) ToAPIResponse() *v1.CreateAIToolCallResponse {
	return &v1.CreateAIToolCallResponse{
		ToolCallID: strings.TrimSpace(r.ToolCallID),
	}
}

func FinalizeRequestFromAPI(rq *v1.FinalizeAIJobRequest, orchestratorInstanceID string) FinalizeRequest {
	if rq == nil {
		return FinalizeRequest{
			OrchestratorInstanceID: strings.TrimSpace(orchestratorInstanceID),
		}
	}
	return FinalizeRequest{
		JobID:                  strings.TrimSpace(rq.GetJobID()),
		OrchestratorInstanceID: strings.TrimSpace(orchestratorInstanceID),
		Status:                 NormalizeLowerText(rq.GetStatus()),
		OutputSummary:          cloneOptionalTrimmedString(rq.OutputSummary),
		DiagnosisJSON:          cloneOptionalTrimmedString(rq.DiagnosisJSON),
		EvidenceIDs:            NormalizeStringList(rq.GetEvidenceIDs()),
		ErrorMessage:           cloneOptionalTrimmedString(rq.ErrorMessage),
	}
}

func (r FinalizeRequest) ToAPIRequest() *v1.FinalizeAIJobRequest {
	return &v1.FinalizeAIJobRequest{
		JobID:         strings.TrimSpace(r.JobID),
		Status:        NormalizeLowerText(r.Status),
		OutputSummary: cloneOptionalTrimmedString(r.OutputSummary),
		DiagnosisJSON: cloneOptionalTrimmedString(r.DiagnosisJSON),
		EvidenceIDs:   NormalizeStringList(r.EvidenceIDs),
		ErrorMessage:  cloneOptionalTrimmedString(r.ErrorMessage),
	}
}

func FinalizeResponseFromAPI(_ *v1.FinalizeAIJobResponse) FinalizeResponse {
	return FinalizeResponse{}
}

func (r FinalizeResponse) ToAPIResponse() *v1.FinalizeAIJobResponse {
	_ = r
	return &v1.FinalizeAIJobResponse{}
}

func EvidencePublishRequestFromAPI(rq *v1.SaveEvidenceRequest) EvidencePublishRequest {
	if rq == nil {
		return EvidencePublishRequest{}
	}
	return EvidencePublishRequest{
		IncidentID:     strings.TrimSpace(rq.GetIncidentID()),
		IdempotencyKey: cloneOptionalTrimmedString(rq.IdempotencyKey),
		JobID:          cloneOptionalTrimmedString(rq.JobID),
		Type:           NormalizeLowerText(rq.GetType()),
		DatasourceID:   cloneOptionalTrimmedString(rq.DatasourceID),
		QueryText:      strings.TrimSpace(rq.GetQueryText()),
		QueryJSON:      cloneOptionalTrimmedString(rq.QueryJSON),
		TimeRangeStart: rq.GetTimeRangeStart().AsTime().UTC(),
		TimeRangeEnd:   rq.GetTimeRangeEnd().AsTime().UTC(),
		ResultJSON:     strings.TrimSpace(rq.GetResultJSON()),
		Summary:        cloneOptionalTrimmedString(rq.Summary),
		CreatedBy:      cloneOptionalTrimmedString(rq.CreatedBy),
	}
}

func (r EvidencePublishRequest) ToAPIRequest() *v1.SaveEvidenceRequest {
	return &v1.SaveEvidenceRequest{
		IncidentID:     strings.TrimSpace(r.IncidentID),
		IdempotencyKey: cloneOptionalTrimmedString(r.IdempotencyKey),
		JobID:          cloneOptionalTrimmedString(r.JobID),
		Type:           NormalizeLowerText(r.Type),
		DatasourceID:   cloneOptionalTrimmedString(r.DatasourceID),
		QueryText:      strings.TrimSpace(r.QueryText),
		QueryJSON:      cloneOptionalTrimmedString(r.QueryJSON),
		TimeRangeStart: timestampOrNil(r.TimeRangeStart),
		TimeRangeEnd:   timestampOrNil(r.TimeRangeEnd),
		ResultJSON:     strings.TrimSpace(r.ResultJSON),
		Summary:        cloneOptionalTrimmedString(r.Summary),
		CreatedBy:      cloneOptionalTrimmedString(r.CreatedBy),
	}
}

func EvidencePublishResponseFromAPI(rq *v1.SaveEvidenceResponse) EvidencePublishResponse {
	if rq == nil {
		return EvidencePublishResponse{}
	}
	return EvidencePublishResponse{
		EvidenceID: strings.TrimSpace(rq.GetEvidenceID()),
	}
}

func (r EvidencePublishResponse) ToAPIResponse() *v1.SaveEvidenceResponse {
	return &v1.SaveEvidenceResponse{
		EvidenceID: strings.TrimSpace(r.EvidenceID),
	}
}

func NormalizeLowerText(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func NormalizeStringList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, item := range in {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func cloneOptionalTrimmedString(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	return &trimmed
}

func timestampOrNil(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t.UTC())
}
