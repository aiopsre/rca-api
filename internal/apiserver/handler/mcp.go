package handler

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/onexstack/onexstack/pkg/errorsx"
	"google.golang.org/protobuf/types/known/timestamppb"

	aijobbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/ai_job"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/authz"
	"github.com/aiopsre/rca-api/internal/pkg/contextx"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

const (
	mcpToolCallAuditJobID    = "mcp-readonly"
	mcpToolCallNodeName      = "mcp.http-shim"
	mcpToolCallToolPrefix    = "mcp."
	mcpWarningTruncated      = "TRUNCATED_OUTPUT"
	mcpCallStep              = "mcp.call"
	mcpMaxResponseBytes      = 16 * 1024
	mcpMaxAuditInputBytes    = 8 * 1024
	mcpMaxAuditOutputBytes   = 8 * 1024
	mcpMaxAuditErrorBytes    = 2 * 1024
	mcpDefaultPageLimit      = int64(20)
	mcpPreviewBytes          = 1024
	mcpMinimumPreviewBytes   = 64
	mcpMaxJSONPreviewRetries = 8
)

var mcpToolCallSeq atomic.Int64

type mcpToolDefinition struct {
	Name           string
	Description    string
	RequiredScopes []string
	InputSchema    map[string]any
	OutputSchema   map[string]any
	Execute        func(h *Handler, c *gin.Context, input map[string]any) (any, bool, error)
}

type mcpToolCallRequest struct {
	Tool           string         `json:"tool"`
	Input          map[string]any `json:"input"`
	IdempotencyKey *string        `json:"idempotency_key,omitempty"`
}

type mcpErrorEnvelope struct {
	Error mcpErrorBody `json:"error"`
}

type mcpErrorBody struct {
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Details mcpErrorDetails `json:"details"`
}

type mcpErrorDetails struct {
	Step string `json:"step"`
	Tool string `json:"tool,omitempty"`
}

var mcpReadonlyTools = []mcpToolDefinition{
	{
		Name:           "get_incident",
		Description:    "Get one incident by incident_id.",
		RequiredScopes: []string{authz.ScopeIncidentRead},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"incident_id": map[string]any{"type": "string"},
			},
			"required": []string{"incident_id"},
		},
		OutputSchema: map[string]any{
			"type": "object",
		},
		Execute: (*Handler).mcpGetIncident,
	},
	{
		Name:           "list_alert_events_current",
		Description:    "List current alert events with optional filters and page/limit.",
		RequiredScopes: []string{authz.ScopeAlertRead},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"namespace": map[string]any{"type": "string"},
				"service":   map[string]any{"type": "string"},
				"severity":  map[string]any{"type": "string"},
				"page":      map[string]any{"type": "integer"},
				"limit":     map[string]any{"type": "integer"},
			},
		},
		OutputSchema: map[string]any{
			"type": "object",
		},
		Execute: (*Handler).mcpListAlertEventsCurrent,
	},
	{
		Name:           "get_evidence",
		Description:    "Get one evidence by evidence_id.",
		RequiredScopes: []string{authz.ScopeEvidenceRead},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"evidence_id": map[string]any{"type": "string"},
			},
			"required": []string{"evidence_id"},
		},
		OutputSchema: map[string]any{
			"type": "object",
		},
		Execute: (*Handler).mcpGetEvidence,
	},
	{
		Name:           "list_incident_evidence",
		Description:    "List evidence by incident_id with page/limit.",
		RequiredScopes: []string{authz.ScopeEvidenceRead},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"incident_id": map[string]any{"type": "string"},
				"page":        map[string]any{"type": "integer"},
				"limit":       map[string]any{"type": "integer"},
			},
			"required": []string{"incident_id"},
		},
		OutputSchema: map[string]any{
			"type": "object",
		},
		Execute: (*Handler).mcpListIncidentEvidence,
	},
	{
		Name:           "query_metrics",
		Description:    "Read-only metrics query with evidence guardrails.",
		RequiredScopes: []string{authz.ScopeEvidenceQuery},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"datasource_id": map[string]any{"type": "string"},
				"expr":          map[string]any{"type": "string"},
				"time_range_start": map[string]any{
					"type": "string",
				},
				"time_range_end": map[string]any{
					"type": "string",
				},
				"step_seconds": map[string]any{"type": "integer"},
			},
			"required": []string{"datasource_id", "expr", "time_range_start", "time_range_end"},
		},
		OutputSchema: map[string]any{
			"type": "object",
		},
		Execute: (*Handler).mcpQueryMetrics,
	},
	{
		Name:           "query_logs",
		Description:    "Read-only logs query with evidence guardrails.",
		RequiredScopes: []string{authz.ScopeEvidenceQuery},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"datasource_id": map[string]any{"type": "string"},
				"query":         map[string]any{"type": "string"},
				"query_json":    map[string]any{"type": "object"},
				"time_range_start": map[string]any{
					"type": "string",
				},
				"time_range_end": map[string]any{
					"type": "string",
				},
				"limit": map[string]any{"type": "integer"},
			},
			"required": []string{"datasource_id", "query", "time_range_start", "time_range_end"},
		},
		OutputSchema: map[string]any{
			"type": "object",
		},
		Execute: (*Handler).mcpQueryLogs,
	},
}

func (h *Handler) ListMCPTools(c *gin.Context) {
	items := make([]map[string]any, 0, len(mcpReadonlyTools))
	for _, tool := range mcpReadonlyTools {
		items = append(items, map[string]any{
			"name":            tool.Name,
			"description":     tool.Description,
			"input_schema":    tool.InputSchema,
			"output_schema":   tool.OutputSchema,
			"required_scopes": tool.RequiredScopes,
		})
	}
	c.JSON(http.StatusOK, map[string]any{"tools": items})
}

func (h *Handler) CallMCPTool(c *gin.Context) {
	startedAt := time.Now()

	var req mcpToolCallRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.writeMCPCallFailure(c, "", nil, nil, startedAt, errorsx.ErrInvalidArgument)
		return
	}

	req.Tool = strings.ToLower(strings.TrimSpace(req.Tool))
	if req.Input == nil {
		req.Input = map[string]any{}
	}
	if req.Tool == "" {
		h.writeMCPCallFailure(c, req.Tool, req.Input, req.IdempotencyKey, startedAt, errorsx.ErrInvalidArgument)
		return
	}

	tool, ok := lookupMCPTool(req.Tool)
	if !ok {
		h.writeMCPCallFailure(c, req.Tool, req.Input, req.IdempotencyKey, startedAt, errorsx.ErrInvalidArgument)
		return
	}
	if err := requireAllScopes(c, tool.RequiredScopes...); err != nil {
		h.writeMCPCallFailure(c, req.Tool, req.Input, req.IdempotencyKey, startedAt, err)
		return
	}

	output, toolTruncated, err := tool.Execute(h, c, req.Input)
	if err != nil {
		h.writeMCPCallFailure(c, req.Tool, req.Input, req.IdempotencyKey, startedAt, err)
		return
	}

	latencyMs := time.Since(startedAt).Milliseconds()
	warnings := buildMCPWarnings(toolTruncated)
	incidentID := extractIncidentID(req.Input)
	datasourceID := extractDatasourceID(req.Input)

	auditOutput := map[string]any{
		"output":    output,
		"truncated": toolTruncated,
		"warnings":  warnings,
	}
	toolCallID, auditErr := h.recordMCPToolCall(
		c,
		req.Tool,
		req.Input,
		req.IdempotencyKey,
		incidentID,
		datasourceID,
		auditOutput,
		"",
		latencyMs,
		"ok",
	)
	if auditErr != nil {
		h.writeMCPCallFailure(c, req.Tool, req.Input, req.IdempotencyKey, startedAt, auditErr)
		return
	}

	resp := map[string]any{
		"tool":         req.Tool,
		"output":       sanitizeAny(output),
		"truncated":    toolTruncated,
		"warnings":     warnings,
		"tool_call_id": toolCallID,
		"latency_ms":   latencyMs,
	}

	c.Data(http.StatusOK, "application/json", finalizeMCPCallResponse(resp))
}

func (h *Handler) writeMCPCallFailure(
	c *gin.Context,
	tool string,
	input map[string]any,
	idempotencyKey *string,
	startedAt time.Time,
	callErr error,
) {

	latencyMs := time.Since(startedAt).Milliseconds()
	statusCode, payload := mapMCPCallError(callErr, tool)
	auditStatus := mcpAuditStatus(callErr)

	_, auditErr := h.recordMCPToolCall(
		c,
		tool,
		input,
		idempotencyKey,
		extractIncidentID(input),
		extractDatasourceID(input),
		payload.toMap(),
		payload.Error.Message,
		latencyMs,
		auditStatus,
	)
	if auditErr != nil {
		statusCode, payload = mapMCPCallError(auditErr, tool)
	}

	c.JSON(statusCode, payload)
}

func (h *Handler) mcpGetIncident(c *gin.Context, input map[string]any) (any, bool, error) {
	incidentID := readInputString(input, "incident_id", "incidentID")
	req := &v1.GetIncidentRequest{IncidentID: incidentID}
	if err := h.val.ValidateGetIncidentRequest(c.Request.Context(), req); err != nil {
		return nil, false, err
	}

	resp, err := h.biz.IncidentV1().Get(c.Request.Context(), req)
	if err != nil {
		return nil, false, err
	}
	incident := resp.GetIncident()
	if incident == nil {
		return nil, false, errno.ErrIncidentNotFound
	}

	out := map[string]any{
		"incidentID":   incident.GetIncidentID(),
		"namespace":    incident.GetNamespace(),
		"workloadKind": incident.GetWorkloadKind(),
		"workloadName": incident.GetWorkloadName(),
		"service":      incident.GetService(),
		"severity":     incident.GetSeverity(),
		"status":       incident.GetStatus(),
		"createdAt":    timestampToRFC3339(incident.GetCreatedAt()),
		"updatedAt":    timestampToRFC3339(incident.GetUpdatedAt()),
	}
	putNonEmpty(out, "rcaStatus", strings.TrimSpace(incident.GetRcaStatus()))
	putNonEmpty(out, "rootCauseSummary", strings.TrimSpace(incident.GetRootCauseSummary()))
	if diagnosis := strings.TrimSpace(incident.GetDiagnosisJSON()); diagnosis != "" {
		putNonEmpty(out, "rootCauseType", extractRootCauseTypeFromDiagnosis(diagnosis))
		out["diagnosisJSON"] = sanitizeJSONString(diagnosis)
	}
	return out, false, nil
}

func (h *Handler) mcpListAlertEventsCurrent(c *gin.Context, input map[string]any) (any, bool, error) {
	offset, limit, err := parsePageOffset(input)
	if err != nil {
		return nil, false, err
	}

	req := &v1.ListCurrentAlertEventsRequest{
		Offset: offset,
		Limit:  limit,
	}
	if v := readInputString(input, "namespace"); v != "" {
		req.Namespace = &v
	}
	if v := readInputString(input, "service"); v != "" {
		req.Service = &v
	}
	if v := readInputString(input, "severity"); v != "" {
		req.Severity = &v
	}

	if err := h.val.ValidateListCurrentAlertEventsRequest(c.Request.Context(), req); err != nil {
		return nil, false, err
	}

	resp, err := h.biz.AlertEventV1().ListCurrent(c.Request.Context(), req)
	if err != nil {
		return nil, false, err
	}

	events := make([]map[string]any, 0, len(resp.GetEvents()))
	for _, item := range resp.GetEvents() {
		event := map[string]any{
			"eventID":     item.GetEventID(),
			"fingerprint": item.GetFingerprint(),
			"status":      item.GetStatus(),
			"severity":    item.GetSeverity(),
			"service":     item.GetService(),
			"cluster":     item.GetCluster(),
			"namespace":   item.GetNamespace(),
			"workload":    item.GetWorkload(),
			"isCurrent":   item.GetIsCurrent(),
			"isSilenced":  item.GetIsSilenced(),
			"lastSeenAt":  timestampToRFC3339(item.GetLastSeenAt()),
		}
		putNonEmpty(event, "incidentID", strings.TrimSpace(item.GetIncidentID()))
		putNonEmpty(event, "silenceID", strings.TrimSpace(item.GetSilenceID()))
		events = append(events, event)
	}

	out := map[string]any{
		"totalCount": resp.GetTotalCount(),
		"offset":     offset,
		"limit":      limit,
		"events":     events,
	}
	return out, false, nil
}

func (h *Handler) mcpGetEvidence(c *gin.Context, input map[string]any) (any, bool, error) {
	evidenceID := readInputString(input, "evidence_id", "evidenceID")
	req := &v1.GetEvidenceRequest{EvidenceID: evidenceID}
	if err := h.val.ValidateGetEvidenceRequest(c.Request.Context(), req); err != nil {
		return nil, false, err
	}

	resp, err := h.biz.EvidenceV1().Get(c.Request.Context(), req)
	if err != nil {
		return nil, false, err
	}
	evidence := resp.GetEvidence()
	if evidence == nil {
		return nil, false, errno.ErrEvidenceNotFound
	}

	out := map[string]any{
		"evidenceID":      evidence.GetEvidenceID(),
		"incidentID":      evidence.GetIncidentID(),
		"type":            evidence.GetType(),
		"queryText":       evidence.GetQueryText(),
		"timeRangeStart":  timestampToRFC3339(evidence.GetTimeRangeStart()),
		"timeRangeEnd":    timestampToRFC3339(evidence.GetTimeRangeEnd()),
		"resultJSON":      sanitizeJSONString(evidence.GetResultJSON()),
		"resultSizeBytes": evidence.GetResultSizeBytes(),
		"isTruncated":     evidence.GetIsTruncated(),
		"createdAt":       timestampToRFC3339(evidence.GetCreatedAt()),
		"createdBy":       evidence.GetCreatedBy(),
	}
	putNonEmpty(out, "jobID", strings.TrimSpace(evidence.GetJobID()))
	putNonEmpty(out, "datasourceID", strings.TrimSpace(evidence.GetDatasourceID()))
	putNonEmpty(out, "summary", strings.TrimSpace(evidence.GetSummary()))
	return out, evidence.GetIsTruncated(), nil
}

func (h *Handler) mcpListIncidentEvidence(c *gin.Context, input map[string]any) (any, bool, error) {
	offset, limit, err := parsePageOffset(input)
	if err != nil {
		return nil, false, err
	}

	req := &v1.ListIncidentEvidenceRequest{
		IncidentID: readInputString(input, "incident_id", "incidentID"),
		Offset:     offset,
		Limit:      limit,
	}
	if v := readInputString(input, "type"); v != "" {
		req.Type = &v
	}
	if v := readInputString(input, "datasource_id", "datasourceID"); v != "" {
		req.DatasourceID = &v
	}

	if err := h.val.ValidateListIncidentEvidenceRequest(c.Request.Context(), req); err != nil {
		return nil, false, err
	}

	resp, err := h.biz.EvidenceV1().ListByIncident(c.Request.Context(), req)
	if err != nil {
		return nil, false, err
	}

	items := make([]map[string]any, 0, len(resp.GetEvidence()))
	for _, item := range resp.GetEvidence() {
		out := map[string]any{
			"evidenceID":      item.GetEvidenceID(),
			"incidentID":      item.GetIncidentID(),
			"type":            item.GetType(),
			"queryText":       item.GetQueryText(),
			"timeRangeStart":  timestampToRFC3339(item.GetTimeRangeStart()),
			"timeRangeEnd":    timestampToRFC3339(item.GetTimeRangeEnd()),
			"resultSizeBytes": item.GetResultSizeBytes(),
			"isTruncated":     item.GetIsTruncated(),
			"createdAt":       timestampToRFC3339(item.GetCreatedAt()),
			"createdBy":       item.GetCreatedBy(),
		}
		putNonEmpty(out, "jobID", strings.TrimSpace(item.GetJobID()))
		putNonEmpty(out, "datasourceID", strings.TrimSpace(item.GetDatasourceID()))
		putNonEmpty(out, "summary", strings.TrimSpace(item.GetSummary()))
		items = append(items, out)
	}

	out := map[string]any{
		"totalCount": resp.GetTotalCount(),
		"offset":     offset,
		"limit":      limit,
		"evidence":   items,
	}
	return out, false, nil
}

func (h *Handler) mcpQueryMetrics(c *gin.Context, input map[string]any) (any, bool, error) {
	startTS, _, err := readInputTimestamp(input, "time_range_start", "timeRangeStart", "start")
	if err != nil {
		return nil, false, err
	}
	endTS, _, err := readInputTimestamp(input, "time_range_end", "timeRangeEnd", "end")
	if err != nil {
		return nil, false, err
	}
	step, hasStep, err := readInputInt64(input, "step_seconds", "stepSeconds", "step")
	if err != nil {
		return nil, false, err
	}

	req := &v1.QueryMetricsRequest{
		DatasourceID:   readInputString(input, "datasource_id", "datasourceID"),
		Promql:         firstNonEmpty(readInputString(input, "expr"), readInputString(input, "promql")),
		TimeRangeStart: startTS,
		TimeRangeEnd:   endTS,
	}
	if hasStep {
		req.StepSeconds = &step
	}

	if err := h.val.ValidateQueryMetricsRequest(c.Request.Context(), req); err != nil {
		return nil, false, err
	}

	resp, err := h.biz.EvidenceV1().QueryMetrics(c.Request.Context(), req)
	if err != nil {
		return nil, false, err
	}

	out := map[string]any{
		"queryResultJSON": sanitizeJSONString(resp.GetQueryResultJSON()),
		"resultSizeBytes": resp.GetResultSizeBytes(),
		"rowCount":        resp.GetRowCount(),
		"isTruncated":     resp.GetIsTruncated(),
	}
	return out, resp.GetIsTruncated(), nil
}

func (h *Handler) mcpQueryLogs(c *gin.Context, input map[string]any) (any, bool, error) {
	startTS, _, err := readInputTimestamp(input, "time_range_start", "timeRangeStart", "start")
	if err != nil {
		return nil, false, err
	}
	endTS, _, err := readInputTimestamp(input, "time_range_end", "timeRangeEnd", "end")
	if err != nil {
		return nil, false, err
	}
	limit, hasLimit, err := readInputInt64(input, "limit")
	if err != nil {
		return nil, false, err
	}
	queryJSONString, hasQueryJSON, err := readOptionalJSONString(input, "query_json", "queryJSON")
	if err != nil {
		return nil, false, err
	}

	req := &v1.QueryLogsRequest{
		DatasourceID:   readInputString(input, "datasource_id", "datasourceID"),
		QueryText:      firstNonEmpty(readInputString(input, "query"), readInputString(input, "query_text", "queryText")),
		TimeRangeStart: startTS,
		TimeRangeEnd:   endTS,
	}
	if hasQueryJSON {
		req.QueryJSON = &queryJSONString
	}
	if hasLimit {
		req.Limit = &limit
	}

	if err := h.val.ValidateQueryLogsRequest(c.Request.Context(), req); err != nil {
		return nil, false, err
	}

	resp, err := h.biz.EvidenceV1().QueryLogs(c.Request.Context(), req)
	if err != nil {
		return nil, false, err
	}

	out := map[string]any{
		"queryResultJSON": sanitizeJSONString(resp.GetQueryResultJSON()),
		"resultSizeBytes": resp.GetResultSizeBytes(),
		"rowCount":        resp.GetRowCount(),
		"isTruncated":     resp.GetIsTruncated(),
	}
	return out, resp.GetIsTruncated(), nil
}

func (h *Handler) recordMCPToolCall(
	c *gin.Context,
	tool string,
	input map[string]any,
	idempotencyKey *string,
	incidentID string,
	datasourceID string,
	output any,
	errorMessage string,
	latencyMs int64,
	status string,
) (string, error) {

	trimmedTool := strings.TrimSpace(tool)
	if trimmedTool == "" {
		trimmedTool = "unknown"
	}

	requestPayload := map[string]any{
		"tool":  trimmedTool,
		"input": sanitizeAny(input),
	}
	if idempotencyKey != nil {
		putNonEmpty(requestPayload, "idempotency_key", strings.TrimSpace(*idempotencyKey))
	}
	putNonEmpty(requestPayload, "request_id", strings.TrimSpace(contextx.RequestID(c.Request.Context())))
	putNonEmpty(requestPayload, "incident_id", strings.TrimSpace(incidentID))
	putNonEmpty(requestPayload, "datasource_id", strings.TrimSpace(datasourceID))

	requestJSON := clampJSONStringByBytes(mustMarshalJSON(requestPayload), mcpMaxAuditInputBytes)

	var responseJSON *string
	responseSizeBytes := int64(0)
	if output != nil {
		raw := mustMarshalJSON(sanitizeAny(output))
		responseSizeBytes = int64(len(raw))
		clipped := clampJSONStringByBytes(raw, mcpMaxAuditOutputBytes)
		responseJSON = &clipped
	}

	var errorPtr *string
	if text := strings.TrimSpace(errorMessage); text != "" {
		clipped, _ := truncateStringByBytes(sanitizeString(text), mcpMaxAuditErrorBytes)
		errorPtr = &clipped
	}

	seq := nextMCPToolCallSeq()
	toolCallID, err := h.biz.AIJobV1().RecordToolCallAudit(c.Request.Context(), &aijobbiz.RecordToolCallAuditRequest{
		JobID:             mcpToolCallAuditJobID,
		Seq:               seq,
		NodeName:          mcpToolCallNodeName,
		ToolName:          mcpToolCallToolPrefix + trimmedTool,
		RequestJSON:       requestJSON,
		ResponseJSON:      responseJSON,
		ResponseSizeBytes: responseSizeBytes,
		Status:            strings.ToLower(strings.TrimSpace(status)),
		LatencyMs:         latencyMs,
		ErrorMessage:      errorPtr,
	})
	if err != nil {
		return "", err
	}
	return toolCallID, nil
}

func lookupMCPTool(name string) (mcpToolDefinition, bool) {
	for _, tool := range mcpReadonlyTools {
		if tool.Name == name {
			return tool, true
		}
	}
	return mcpToolDefinition{}, false
}

func requireAllScopes(c *gin.Context, scopes ...string) error {
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if err := authz.RequireAnyScope(c, scope); err != nil {
			return err
		}
	}
	return nil
}

func mapMCPCallError(err error, tool string) (int, mcpErrorEnvelope) {
	status := http.StatusInternalServerError
	message := "Internal error."

	if ex := errorsx.FromError(err); ex != nil {
		if ex.Code > 0 {
			status = ex.Code
		}
		if text := strings.TrimSpace(ex.Message); text != "" {
			message = text
		}
	}

	code := "INTERNAL"
	switch status {
	case http.StatusBadRequest:
		code = "INVALID_ARGUMENT"
	case http.StatusForbidden:
		code = "SCOPE_DENIED"
	case http.StatusNotFound:
		code = "NOT_FOUND"
	case http.StatusTooManyRequests:
		code = "RATE_LIMITED"
	default:
		status = http.StatusInternalServerError
	}

	return status, mcpErrorEnvelope{
		Error: mcpErrorBody{
			Code:    code,
			Message: message,
			Details: mcpErrorDetails{
				Step: mcpCallStep,
				Tool: strings.TrimSpace(tool),
			},
		},
	}
}

func (e mcpErrorEnvelope) toMap() map[string]any {
	return map[string]any{
		"error": map[string]any{
			"code":    e.Error.Code,
			"message": e.Error.Message,
			"details": map[string]any{
				"step": e.Error.Details.Step,
				"tool": e.Error.Details.Tool,
			},
		},
	}
}

func mcpAuditStatus(err error) string {
	ex := errorsx.FromError(err)
	if ex != nil && ex.Code == http.StatusGatewayTimeout {
		return "timeout"
	}
	return "error"
}

func finalizeMCPCallResponse(resp map[string]any) []byte {
	raw := mustMarshalJSONBytes(resp)
	if len(raw) <= mcpMaxResponseBytes {
		return raw
	}

	resp["truncated"] = true
	resp["warnings"] = ensureMCPWarning(resp["warnings"], mcpWarningTruncated)

	previewSource := mustMarshalJSON(sanitizeAny(resp["output"]))
	previewBytes := mcpPreviewBytes
	for range mcpMaxJSONPreviewRetries {
		preview, _ := truncateStringByBytes(previewSource, previewBytes)
		truncatedOutput := map[string]any{
			"truncated": true,
			"reason":    "max_response_bytes_exceeded",
		}
		if strings.TrimSpace(preview) != "" {
			truncatedOutput["preview"] = preview
		}
		resp["output"] = truncatedOutput

		raw = mustMarshalJSONBytes(resp)
		if len(raw) <= mcpMaxResponseBytes {
			return raw
		}
		if previewBytes <= mcpMinimumPreviewBytes {
			break
		}
		previewBytes = previewBytes / 2
		previewBytes = max(previewBytes, mcpMinimumPreviewBytes)
	}

	minimal := map[string]any{
		"tool":         resp["tool"],
		"output":       map[string]any{"truncated": true, "reason": "max_response_bytes_exceeded"},
		"truncated":    true,
		"warnings":     []string{mcpWarningTruncated},
		"tool_call_id": resp["tool_call_id"],
		"latency_ms":   resp["latency_ms"],
	}
	return mustMarshalJSONBytes(minimal)
}

func buildMCPWarnings(truncated bool) []string {
	if !truncated {
		return []string{}
	}
	return []string{mcpWarningTruncated}
}

func ensureMCPWarning(raw any, warning string) []string {
	if strings.TrimSpace(warning) == "" {
		return normalizeWarningList(raw)
	}

	items := normalizeWarningList(raw)
	if slices.Contains(items, warning) {
		return items
	}
	return append(items, warning)
}

//nolint:gocognit,wsl_v5 // Supports both []string and []any warning payloads.
func normalizeWarningList(raw any) []string {
	out := make([]string, 0, 2)
	switch values := raw.(type) {
	case []string:
		for _, item := range values {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				out = append(out, trimmed)
			}
		}
	case []any:
		for _, item := range values {
			if text, ok := item.(string); ok {
				if trimmed := strings.TrimSpace(text); trimmed != "" {
					out = append(out, trimmed)
				}
			}
		}
	}
	return out
}

func parsePageOffset(input map[string]any) (int64, int64, error) {
	limit := mcpDefaultPageLimit
	if v, ok, err := readInputInt64(input, "limit"); err != nil {
		return 0, 0, err
	} else if ok {
		limit = v
	}

	offset, hasOffset, err := readInputInt64(input, "offset")
	if err != nil {
		return 0, 0, err
	}
	if hasOffset {
		return offset, limit, nil
	}

	page, hasPage, err := readInputInt64(input, "page")
	if err != nil {
		return 0, 0, err
	}
	if !hasPage {
		return 0, limit, nil
	}
	if page <= 0 {
		return 0, 0, errorsx.ErrInvalidArgument
	}
	if limit <= 0 {
		return 0, 0, errorsx.ErrInvalidArgument
	}
	return (page - 1) * limit, limit, nil
}

func readInputString(input map[string]any, keys ...string) string {
	value, ok := findInputValue(input, keys...)
	if !ok {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return strings.TrimSpace(typed.String())
	case float64:
		return strings.TrimSpace(strconv.FormatInt(int64(typed), 10))
	case int64:
		return strings.TrimSpace(strconv.FormatInt(typed, 10))
	case int:
		return strings.TrimSpace(strconv.Itoa(typed))
	default:
		return ""
	}
}

func readInputInt64(input map[string]any, keys ...string) (int64, bool, error) {
	value, ok := findInputValue(input, keys...)
	if !ok {
		return 0, false, nil
	}
	out, err := castToInt64(value)
	if err != nil {
		return 0, false, errorsx.ErrInvalidArgument
	}
	return out, true, nil
}

//nolint:nilnil // found flag disambiguates missing timestamp input from validation errors.
func readInputTimestamp(input map[string]any, keys ...string) (*timestamppb.Timestamp, bool, error) {
	value, ok := findInputValue(input, keys...)
	if !ok {
		return &timestamppb.Timestamp{}, false, nil
	}
	if value == nil {
		return &timestamppb.Timestamp{}, true, errorsx.ErrInvalidArgument
	}

	timeValue, err := castToTime(value)
	if err != nil {
		return &timestamppb.Timestamp{}, true, errorsx.ErrInvalidArgument
	}
	return timestamppb.New(timeValue.UTC()), true, nil
}

//nolint:wsl_v5 // Keep switch branches compact for parse helpers.
func readOptionalJSONString(input map[string]any, keys ...string) (string, bool, error) {
	value, ok := findInputValue(input, keys...)
	if !ok || value == nil {
		return "", false, nil
	}

	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return "", false, nil
		}
		return trimmed, true, nil
	case map[string]any, []any:
		raw, err := json.Marshal(typed)
		if err != nil {
			return "", false, errorsx.ErrInvalidArgument
		}
		text := strings.TrimSpace(string(raw))
		if text == "" {
			return "", false, nil
		}
		return text, true, nil
	default:
		return "", false, errorsx.ErrInvalidArgument
	}
}

func findInputValue(input map[string]any, keys ...string) (any, bool) {
	if len(input) == 0 {
		return nil, false
	}
	for _, key := range keys {
		if value, ok := input[key]; ok {
			return value, true
		}
	}
	return nil, false
}

//nolint:gocyclo,wsl_v5 // Type conversion branch list is explicit for tool input safety.
func castToInt64(value any) (int64, error) {
	switch typed := value.(type) {
	case int:
		return int64(typed), nil
	case int64:
		return typed, nil
	case int32:
		return int64(typed), nil
	case float64:
		if typed != float64(int64(typed)) {
			return 0, errorsx.ErrInvalidArgument
		}
		return int64(typed), nil
	case json.Number:
		v, err := typed.Int64()
		if err != nil {
			return 0, err
		}
		return v, nil
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0, errorsx.ErrInvalidArgument
		}
		v, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil {
			return 0, err
		}
		return v, nil
	default:
		return 0, errorsx.ErrInvalidArgument
	}
}

//nolint:gocognit,gocyclo,wsl_v5 // Time decoding supports multiple MCP-compatible formats.
func castToTime(value any) (time.Time, error) {
	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return time.Time{}, errorsx.ErrInvalidArgument
		}
		if ts, err := time.Parse(time.RFC3339Nano, trimmed); err == nil {
			return ts.UTC(), nil
		}
		if ts, err := time.Parse(time.RFC3339, trimmed); err == nil {
			return ts.UTC(), nil
		}
		sec, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil {
			return time.Time{}, errorsx.ErrInvalidArgument
		}
		return time.Unix(sec, 0).UTC(), nil
	case float64:
		if typed != float64(int64(typed)) {
			return time.Time{}, errorsx.ErrInvalidArgument
		}
		return time.Unix(int64(typed), 0).UTC(), nil
	case int64:
		return time.Unix(typed, 0).UTC(), nil
	case int:
		return time.Unix(int64(typed), 0).UTC(), nil
	case map[string]any:
		seconds, ok := typed["seconds"]
		if !ok {
			return time.Time{}, errorsx.ErrInvalidArgument
		}
		sec, err := castToInt64(seconds)
		if err != nil {
			return time.Time{}, errorsx.ErrInvalidArgument
		}
		nanos := int64(0)
		if rawNanos, ok := typed["nanos"]; ok {
			nanos, err = castToInt64(rawNanos)
			if err != nil {
				return time.Time{}, errorsx.ErrInvalidArgument
			}
		}
		return time.Unix(sec, nanos).UTC(), nil
	default:
		return time.Time{}, errorsx.ErrInvalidArgument
	}
}

func extractIncidentID(input map[string]any) string {
	return readInputString(input, "incident_id", "incidentID")
}

func extractDatasourceID(input map[string]any) string {
	return readInputString(input, "datasource_id", "datasourceID")
}

//nolint:gocognit,wsl_v5 // Recursive sanitizer intentionally handles mixed JSON-like types.
func sanitizeAny(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if isSensitiveKey(key) {
				continue
			}
			out[key] = sanitizeAny(item)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, sanitizeAny(item))
		}
		return out
	case map[string]string:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if isSensitiveKey(key) {
				continue
			}
			out[key] = sanitizeAny(item)
		}
		return out
	case string:
		return sanitizeJSONString(typed)
	default:
		return typed
	}
}

func sanitizeJSONString(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}

	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var payload any
		if err := json.Unmarshal([]byte(trimmed), &payload); err == nil {
			return mustMarshalJSON(sanitizeAny(payload))
		}
	}
	return sanitizeString(trimmed)
}

func sanitizeString(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	// Keep it simple and deterministic: remove obvious bearer token text.
	lower := strings.ToLower(trimmed)
	idx := strings.Index(lower, "bearer ")
	if idx >= 0 {
		return trimmed[:idx] + "Bearer [REDACTED]"
	}
	return trimmed
}

func extractRootCauseTypeFromDiagnosis(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return ""
	}
	rootCause, ok := payload["root_cause"].(map[string]any)
	if !ok {
		return ""
	}
	value, ok := rootCause["type"].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func isSensitiveKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	if k == "" {
		return false
	}
	if strings.Contains(k, "secret") {
		return true
	}
	if strings.Contains(k, "token") {
		return true
	}
	if strings.Contains(k, "header") {
		return true
	}
	if strings.Contains(k, "authorization") {
		return true
	}
	if strings.Contains(k, "password") {
		return true
	}
	if strings.Contains(k, "api_key") || strings.Contains(k, "apikey") {
		return true
	}
	return false
}

func clampJSONStringByBytes(raw string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(raw) <= maxBytes {
		return raw
	}

	previewBytes := maxBytes / 2
	previewBytes = max(previewBytes, mcpMinimumPreviewBytes)
	preview, _ := truncateStringByBytes(raw, previewBytes)
	payload := map[string]any{
		"truncated": true,
		"reason":    "max_audit_bytes_exceeded",
		"preview":   preview,
	}
	clipped := mustMarshalJSON(payload)
	if len(clipped) <= maxBytes {
		return clipped
	}

	payload = map[string]any{
		"truncated": true,
		"reason":    "max_audit_bytes_exceeded",
	}
	clipped = mustMarshalJSON(payload)
	if len(clipped) <= maxBytes {
		return clipped
	}

	fallback := `{"truncated":true}`
	if len(fallback) <= maxBytes {
		return fallback
	}
	truncated, _ := truncateStringByBytes(fallback, maxBytes)
	return truncated
}

func truncateStringByBytes(raw string, maxBytes int) (string, bool) {
	if maxBytes <= 0 {
		return "", len(raw) > 0
	}
	if len(raw) <= maxBytes {
		return raw, false
	}

	truncated := raw[:maxBytes]
	for len(truncated) > 0 && !utf8.ValidString(truncated) {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated, true
}

func nextMCPToolCallSeq() int64 {
	for {
		current := mcpToolCallSeq.Load()
		if current > 0 {
			return mcpToolCallSeq.Add(1)
		}

		seed := time.Now().UTC().UnixNano()
		if seed < 0 {
			seed = -seed
		}
		if seed == 0 {
			seed = 1
		}
		if mcpToolCallSeq.CompareAndSwap(current, seed) {
			return seed
		}
	}
}

func timestampToRFC3339(ts *timestamppb.Timestamp) string {
	if ts == nil {
		return ""
	}
	return ts.AsTime().UTC().Format(time.RFC3339Nano)
}

func putNonEmpty(target map[string]any, key string, value string) {
	if target == nil {
		return
	}
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return
	}
	target[key] = trimmed
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func mustMarshalJSON(value any) string {
	return string(mustMarshalJSONBytes(value))
}

func mustMarshalJSONBytes(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		return []byte(`{"truncated":true,"reason":"marshal_failed"}`)
	}
	return raw
}

//nolint:gochecknoinits // Route registration is intentionally init-based in this codebase.
func init() {
	Register(func(v1 *gin.RouterGroup, handler *Handler, mws ...gin.HandlerFunc) {
		rg := v1.Group("/mcp", mws...)
		rg.GET("/tools", handler.ListMCPTools)
		rg.POST("/tools/call", handler.CallMCPTool)
	})
}
