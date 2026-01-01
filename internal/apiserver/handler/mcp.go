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
	mcpToolRegistryVersion   = "c1"
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

type MCPErrorCode string

const (
	MCPErrorCodeScopeDenied MCPErrorCode = "SCOPE_DENIED"
	MCPErrorCodeInvalidArg  MCPErrorCode = "INVALID_ARGUMENT"
	MCPErrorCodeNotFound    MCPErrorCode = "NOT_FOUND"
	MCPErrorCodeRateLimited MCPErrorCode = "RATE_LIMITED"
	MCPErrorCodeInternal    MCPErrorCode = "INTERNAL"
)

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
	Code    MCPErrorCode    `json:"code"`
	Message string          `json:"message"`
	Details mcpErrorDetails `json:"details"`
}

type mcpErrorDetails struct {
	Step string `json:"step"`
	Tool string `json:"tool,omitempty"`
}

//nolint:dupl // Tool schemas are kept explicit per MCP contract for readability.
var mcpReadonlyTools = []mcpToolDefinition{
	{
		Name:           "list_incidents",
		Description:    "List incidents with optional filters and page/limit.",
		RequiredScopes: []string{authz.ScopeIncidentRead},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"service":          map[string]any{"type": "string"},
				"namespace":        map[string]any{"type": "string"},
				"status":           map[string]any{"type": "string"},
				"severity":         map[string]any{"type": "string"},
				"created_at_start": map[string]any{"type": "string"},
				"created_at_end":   map[string]any{"type": "string"},
				"page":             map[string]any{"type": "integer"},
				"offset":           map[string]any{"type": "integer"},
				"limit":            map[string]any{"type": "integer"},
			},
		},
		OutputSchema: map[string]any{
			"type": "object",
		},
		Execute: (*Handler).mcpListIncidents,
	},
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
		RequiredScopes: []string{authz.ScopeAlertEventRead},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"namespace": map[string]any{"type": "string"},
				"service":   map[string]any{"type": "string"},
				"cluster":   map[string]any{"type": "string"},
				"severity":  map[string]any{"type": "string"},
				"status":    map[string]any{"type": "string"},
				"fingerprint": map[string]any{
					"type": "string",
				},
				"last_seen_start": map[string]any{
					"type": "string",
				},
				"last_seen_end": map[string]any{
					"type": "string",
				},
				"page":   map[string]any{"type": "integer"},
				"offset": map[string]any{"type": "integer"},
				"limit":  map[string]any{"type": "integer"},
			},
		},
		OutputSchema: map[string]any{
			"type": "object",
		},
		Execute: (*Handler).mcpListAlertEventsCurrent,
	},
	{
		Name:           "list_alert_events_history",
		Description:    "List history alert events with optional filters and page/limit.",
		RequiredScopes: []string{authz.ScopeAlertEventRead},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"namespace": map[string]any{"type": "string"},
				"service":   map[string]any{"type": "string"},
				"cluster":   map[string]any{"type": "string"},
				"severity":  map[string]any{"type": "string"},
				"status":    map[string]any{"type": "string"},
				"fingerprint": map[string]any{
					"type": "string",
				},
				"incident_id": map[string]any{
					"type": "string",
				},
				"last_seen_start": map[string]any{
					"type": "string",
				},
				"last_seen_end": map[string]any{
					"type": "string",
				},
				"page":   map[string]any{"type": "integer"},
				"offset": map[string]any{"type": "integer"},
				"limit":  map[string]any{"type": "integer"},
			},
		},
		OutputSchema: map[string]any{
			"type": "object",
		},
		Execute: (*Handler).mcpListAlertEventsHistory,
	},
	{
		Name:           "list_datasources",
		Description:    "List datasource metadata with optional filters and page/limit.",
		RequiredScopes: []string{authz.ScopeDatasourceRead},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"type": map[string]any{"type": "string"},
				"name": map[string]any{"type": "string"},
				"is_enabled": map[string]any{
					"type": "boolean",
				},
				"page":   map[string]any{"type": "integer"},
				"offset": map[string]any{"type": "integer"},
				"limit":  map[string]any{"type": "integer"},
			},
		},
		OutputSchema: map[string]any{
			"type": "object",
		},
		Execute: (*Handler).mcpListDatasources,
	},
	{
		Name:           "get_datasource",
		Description:    "Get one datasource metadata by datasource_id.",
		RequiredScopes: []string{authz.ScopeDatasourceRead},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"datasource_id": map[string]any{"type": "string"},
			},
			"required": []string{"datasource_id"},
		},
		OutputSchema: map[string]any{
			"type": "object",
		},
		Execute: (*Handler).mcpGetDatasource,
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
	{
		Name:           "get_ai_job",
		Description:    "Get one AI job by job_id.",
		RequiredScopes: []string{authz.ScopeAIJobRead},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"job_id": map[string]any{"type": "string"},
			},
			"required": []string{"job_id"},
		},
		OutputSchema: map[string]any{
			"type": "object",
		},
		Execute: (*Handler).mcpGetAIJob,
	},
	{
		Name:           "list_ai_jobs",
		Description:    "List AI jobs by status or by incident_id with page/limit.",
		RequiredScopes: []string{authz.ScopeAIJobRead},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"status": map[string]any{"type": "string"},
				"incident_id": map[string]any{
					"type": "string",
				},
				"page":   map[string]any{"type": "integer"},
				"offset": map[string]any{"type": "integer"},
				"limit":  map[string]any{"type": "integer"},
			},
		},
		OutputSchema: map[string]any{
			"type": "object",
		},
		Execute: (*Handler).mcpListAIJobs,
	},
	{
		Name:           "list_tool_calls",
		Description:    "List tool call audits by job_id with page/limit.",
		RequiredScopes: []string{authz.ScopeToolCallRead},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"job_id": map[string]any{"type": "string"},
				"seq":    map[string]any{"type": "integer"},
				"page":   map[string]any{"type": "integer"},
				"offset": map[string]any{"type": "integer"},
				"limit":  map[string]any{"type": "integer"},
			},
			"required": []string{"job_id"},
		},
		OutputSchema: map[string]any{
			"type": "object",
		},
		Execute: (*Handler).mcpListToolCalls,
	},
	{
		Name:           "list_silences",
		Description:    "List silences with optional filters and page/limit.",
		RequiredScopes: []string{authz.ScopeSilenceRead},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"namespace": map[string]any{"type": "string"},
				"enabled":   map[string]any{"type": "boolean"},
				"active":    map[string]any{"type": "boolean"},
				"page":      map[string]any{"type": "integer"},
				"offset":    map[string]any{"type": "integer"},
				"limit":     map[string]any{"type": "integer"},
			},
		},
		OutputSchema: map[string]any{
			"type": "object",
		},
		Execute: (*Handler).mcpListSilences,
	},
	{
		Name:           "list_notice_deliveries",
		Description:    "List notice deliveries with optional filters and page/limit.",
		RequiredScopes: []string{authz.ScopeNoticeRead},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"channel_id":  map[string]any{"type": "string"},
				"incident_id": map[string]any{"type": "string"},
				"event_type":  map[string]any{"type": "string"},
				"status":      map[string]any{"type": "string"},
				"page":        map[string]any{"type": "integer"},
				"offset":      map[string]any{"type": "integer"},
				"limit":       map[string]any{"type": "integer"},
			},
		},
		OutputSchema: map[string]any{
			"type": "object",
		},
		Execute: (*Handler).mcpListNoticeDeliveries,
	},
}

var mcpScopeAliases = map[string][]string{
	authz.ScopeAlertEventRead: {authz.ScopeAlertRead},
	authz.ScopeAIJobRead:      {authz.ScopeAIRead},
	authz.ScopeToolCallRead:   {authz.ScopeAIRead},
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
	c.JSON(http.StatusOK, map[string]any{
		"version": mcpToolRegistryVersion,
		"tools":   items,
	})
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

//nolint:gocognit,gocyclo // MCP input-to-request mapping is intentionally explicit.
func (h *Handler) mcpListIncidents(c *gin.Context, input map[string]any) (any, bool, error) {
	offset, limit, err := parsePageOffset(input)
	if err != nil {
		return nil, false, err
	}

	req := &v1.ListIncidentRequest{
		Offset: offset,
		Limit:  limit,
	}
	if v := readInputString(input, "service"); v != "" {
		req.Service = &v
	}
	if v := readInputString(input, "namespace"); v != "" {
		req.Namespace = &v
	}
	if v := readInputString(input, "status"); v != "" {
		req.Status = &v
	}
	if v := readInputString(input, "severity"); v != "" {
		req.Severity = &v
	}
	if ts, has, err := readInputTimestamp(input, "created_at_start", "createdAtStart"); err != nil {
		return nil, false, err
	} else if has {
		req.CreatedAtStart = ts
	}
	if ts, has, err := readInputTimestamp(input, "created_at_end", "createdAtEnd"); err != nil {
		return nil, false, err
	} else if has {
		req.CreatedAtEnd = ts
	}

	if err := h.val.ValidateListIncidentRequest(c.Request.Context(), req); err != nil {
		return nil, false, err
	}

	resp, err := h.biz.IncidentV1().List(c.Request.Context(), req)
	if err != nil {
		return nil, false, err
	}

	incidents := make([]map[string]any, 0, len(resp.GetIncidents()))
	for _, item := range resp.GetIncidents() {
		incidents = append(incidents, buildMCPIncidentOutput(item))
	}
	return map[string]any{
		"totalCount": resp.GetTotalCount(),
		"offset":     offset,
		"limit":      limit,
		"incidents":  incidents,
	}, false, nil
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
	return buildMCPIncidentOutput(incident), false, nil
}

//nolint:gocognit,gocyclo // MCP input-to-request mapping is intentionally explicit.
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
	if v := readInputString(input, "cluster"); v != "" {
		req.Cluster = &v
	}
	if v := readInputString(input, "severity"); v != "" {
		req.Severity = &v
	}
	if v := readInputString(input, "status"); v != "" {
		req.Status = &v
	}
	if v := readInputString(input, "fingerprint"); v != "" {
		req.Fingerprint = &v
	}
	if ts, has, err := readInputTimestamp(input, "last_seen_start", "lastSeenStart"); err != nil {
		return nil, false, err
	} else if has {
		req.LastSeenStart = ts
	}
	if ts, has, err := readInputTimestamp(input, "last_seen_end", "lastSeenEnd"); err != nil {
		return nil, false, err
	} else if has {
		req.LastSeenEnd = ts
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
		events = append(events, buildMCPAlertEventOutput(item))
	}

	out := map[string]any{
		"totalCount": resp.GetTotalCount(),
		"offset":     offset,
		"limit":      limit,
		"events":     events,
	}
	return out, false, nil
}

//nolint:gocognit,gocyclo // MCP input-to-request mapping is intentionally explicit.
func (h *Handler) mcpListAlertEventsHistory(c *gin.Context, input map[string]any) (any, bool, error) {
	offset, limit, err := parsePageOffset(input)
	if err != nil {
		return nil, false, err
	}

	req := &v1.ListHistoryAlertEventsRequest{
		Offset: offset,
		Limit:  limit,
	}
	if v := readInputString(input, "namespace"); v != "" {
		req.Namespace = &v
	}
	if v := readInputString(input, "service"); v != "" {
		req.Service = &v
	}
	if v := readInputString(input, "cluster"); v != "" {
		req.Cluster = &v
	}
	if v := readInputString(input, "severity"); v != "" {
		req.Severity = &v
	}
	if v := readInputString(input, "status"); v != "" {
		req.Status = &v
	}
	if v := readInputString(input, "fingerprint"); v != "" {
		req.Fingerprint = &v
	}
	if v := readInputString(input, "incident_id", "incidentID"); v != "" {
		req.IncidentID = &v
	}
	if ts, has, err := readInputTimestamp(input, "last_seen_start", "lastSeenStart"); err != nil {
		return nil, false, err
	} else if has {
		req.LastSeenStart = ts
	}
	if ts, has, err := readInputTimestamp(input, "last_seen_end", "lastSeenEnd"); err != nil {
		return nil, false, err
	} else if has {
		req.LastSeenEnd = ts
	}

	if err := h.val.ValidateListHistoryAlertEventsRequest(c.Request.Context(), req); err != nil {
		return nil, false, err
	}

	resp, err := h.biz.AlertEventV1().ListHistory(c.Request.Context(), req)
	if err != nil {
		return nil, false, err
	}

	events := make([]map[string]any, 0, len(resp.GetEvents()))
	for _, item := range resp.GetEvents() {
		events = append(events, buildMCPAlertEventOutput(item))
	}
	return map[string]any{
		"totalCount": resp.GetTotalCount(),
		"offset":     offset,
		"limit":      limit,
		"events":     events,
	}, false, nil
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

func (h *Handler) mcpListDatasources(c *gin.Context, input map[string]any) (any, bool, error) {
	offset, limit, err := parsePageOffset(input)
	if err != nil {
		return nil, false, err
	}

	req := &v1.ListDatasourceRequest{
		Offset: offset,
		Limit:  limit,
	}
	if v := readInputString(input, "type"); v != "" {
		req.Type = &v
	}
	if v := readInputString(input, "name"); v != "" {
		req.Name = &v
	}
	if v, ok, err := readInputBool(input, "is_enabled", "isEnabled"); err != nil {
		return nil, false, err
	} else if ok {
		req.IsEnabled = &v
	}
	if err := h.val.ValidateListDatasourceRequest(c.Request.Context(), req); err != nil {
		return nil, false, err
	}

	resp, err := h.biz.DatasourceV1().List(c.Request.Context(), req)
	if err != nil {
		return nil, false, err
	}

	items := make([]map[string]any, 0, len(resp.GetDatasources()))
	for _, item := range resp.GetDatasources() {
		items = append(items, buildMCPDatasourceOutput(item))
	}
	return map[string]any{
		"totalCount":  resp.GetTotalCount(),
		"offset":      offset,
		"limit":       limit,
		"datasources": items,
	}, false, nil
}

//nolint:dupl // Pattern matches other get-by-id tool branches by design.
func (h *Handler) mcpGetDatasource(c *gin.Context, input map[string]any) (any, bool, error) {
	req := &v1.GetDatasourceRequest{
		DatasourceID: readInputString(input, "datasource_id", "datasourceID"),
	}
	if err := h.val.ValidateGetDatasourceRequest(c.Request.Context(), req); err != nil {
		return nil, false, err
	}

	resp, err := h.biz.DatasourceV1().Get(c.Request.Context(), req)
	if err != nil {
		return nil, false, err
	}
	ds := resp.GetDatasource()
	if ds == nil {
		return nil, false, errno.ErrDatasourceNotFound
	}
	return buildMCPDatasourceOutput(ds), false, nil
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

//nolint:dupl // Pattern matches other get-by-id tool branches by design.
func (h *Handler) mcpGetAIJob(c *gin.Context, input map[string]any) (any, bool, error) {
	req := &v1.GetAIJobRequest{
		JobID: readInputString(input, "job_id", "jobID"),
	}
	if err := h.val.ValidateGetAIJobRequest(c.Request.Context(), req); err != nil {
		return nil, false, err
	}

	resp, err := h.biz.AIJobV1().Get(c.Request.Context(), req)
	if err != nil {
		return nil, false, err
	}
	job := resp.GetJob()
	if job == nil {
		return nil, false, errno.ErrAIJobNotFound
	}
	return buildMCPAIJobOutput(job), false, nil
}

//nolint:gocognit,gocyclo // MCP input-to-request mapping is intentionally explicit.
func (h *Handler) mcpListAIJobs(c *gin.Context, input map[string]any) (any, bool, error) {
	offset, limit, err := parsePageOffset(input)
	if err != nil {
		return nil, false, err
	}
	incidentID := readInputString(input, "incident_id", "incidentID")
	if incidentID != "" {
		req := &v1.ListIncidentAIJobsRequest{
			IncidentID: incidentID,
			Offset:     offset,
			Limit:      limit,
		}
		if err := h.val.ValidateListIncidentAIJobsRequest(c.Request.Context(), req); err != nil {
			return nil, false, err
		}
		resp, err := h.biz.AIJobV1().ListByIncident(c.Request.Context(), req)
		if err != nil {
			return nil, false, err
		}
		items := make([]map[string]any, 0, len(resp.GetJobs()))
		for _, item := range resp.GetJobs() {
			items = append(items, buildMCPAIJobOutput(item))
		}
		return map[string]any{
			"totalCount": resp.GetTotalCount(),
			"offset":     offset,
			"limit":      limit,
			"jobs":       items,
		}, false, nil
	}

	req := &v1.ListAIJobsRequest{
		Status: readInputString(input, "status"),
		Offset: offset,
		Limit:  limit,
	}
	if err := h.val.ValidateListAIJobsRequest(c.Request.Context(), req); err != nil {
		return nil, false, err
	}
	resp, err := h.biz.AIJobV1().List(c.Request.Context(), req)
	if err != nil {
		return nil, false, err
	}

	items := make([]map[string]any, 0, len(resp.GetJobs()))
	for _, item := range resp.GetJobs() {
		items = append(items, buildMCPAIJobOutput(item))
	}
	return map[string]any{
		"status":     req.GetStatus(),
		"totalCount": resp.GetTotalCount(),
		"offset":     offset,
		"limit":      limit,
		"jobs":       items,
	}, false, nil
}

func (h *Handler) mcpListToolCalls(c *gin.Context, input map[string]any) (any, bool, error) {
	offset, limit, err := parsePageOffset(input)
	if err != nil {
		return nil, false, err
	}

	req := &v1.ListAIToolCallsRequest{
		JobID:  readInputString(input, "job_id", "jobID"),
		Offset: offset,
		Limit:  limit,
	}
	if seq, hasSeq, err := readInputInt64(input, "seq"); err != nil {
		return nil, false, err
	} else if hasSeq {
		req.Seq = &seq
	}
	if err := h.val.ValidateListAIToolCallsRequest(c.Request.Context(), req); err != nil {
		return nil, false, err
	}

	resp, err := h.biz.AIJobV1().ListToolCalls(c.Request.Context(), req)
	if err != nil {
		return nil, false, err
	}
	items := make([]map[string]any, 0, len(resp.GetToolCalls()))
	for _, item := range resp.GetToolCalls() {
		items = append(items, buildMCPToolCallOutput(item))
	}
	out := map[string]any{
		"totalCount": resp.GetTotalCount(),
		"offset":     offset,
		"limit":      limit,
		"toolCalls":  items,
	}
	putNonEmpty(out, "jobID", req.GetJobID())
	if req.Seq != nil {
		out["seq"] = req.GetSeq()
	}
	return out, false, nil
}

func (h *Handler) mcpListSilences(c *gin.Context, input map[string]any) (any, bool, error) {
	offset, limit, err := parsePageOffset(input)
	if err != nil {
		return nil, false, err
	}

	req := &v1.ListSilencesRequest{
		Offset: offset,
		Limit:  limit,
	}
	if v := readInputString(input, "namespace"); v != "" {
		req.Namespace = &v
	}
	if v, ok, err := readInputBool(input, "enabled"); err != nil {
		return nil, false, err
	} else if ok {
		req.Enabled = &v
	}
	if v, ok, err := readInputBool(input, "active"); err != nil {
		return nil, false, err
	} else if ok {
		req.Active = &v
	}
	if err := h.val.ValidateListSilencesRequest(c.Request.Context(), req); err != nil {
		return nil, false, err
	}

	resp, err := h.biz.SilenceV1().List(c.Request.Context(), req)
	if err != nil {
		return nil, false, err
	}
	items := make([]map[string]any, 0, len(resp.GetSilences()))
	for _, item := range resp.GetSilences() {
		items = append(items, buildMCPSilenceOutput(item))
	}
	return map[string]any{
		"totalCount": resp.GetTotalCount(),
		"offset":     offset,
		"limit":      limit,
		"silences":   items,
	}, false, nil
}

func (h *Handler) mcpListNoticeDeliveries(c *gin.Context, input map[string]any) (any, bool, error) {
	offset, limit, err := parsePageOffset(input)
	if err != nil {
		return nil, false, err
	}

	req := &v1.ListNoticeDeliveriesRequest{
		Offset: offset,
		Limit:  limit,
	}
	if v := readInputString(input, "channel_id", "channelID"); v != "" {
		req.ChannelID = &v
	}
	if v := readInputString(input, "incident_id", "incidentID"); v != "" {
		req.IncidentID = &v
	}
	if v := readInputString(input, "event_type", "eventType"); v != "" {
		req.EventType = &v
	}
	if v := readInputString(input, "status"); v != "" {
		req.Status = &v
	}
	if err := h.val.ValidateListNoticeDeliveriesRequest(c.Request.Context(), req); err != nil {
		return nil, false, err
	}

	resp, err := h.biz.NoticeV1().ListDeliveries(c.Request.Context(), req)
	if err != nil {
		return nil, false, err
	}
	items := make([]map[string]any, 0, len(resp.GetNoticeDeliveries()))
	for _, item := range resp.GetNoticeDeliveries() {
		items = append(items, buildMCPNoticeDeliveryOutput(item))
	}
	return map[string]any{
		"totalCount":       resp.GetTotalCount(),
		"offset":           offset,
		"limit":            limit,
		"noticeDeliveries": items,
	}, false, nil
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
		if err := authz.RequireAnyScope(c, expandRequiredScope(scope)...); err != nil {
			return err
		}
	}
	return nil
}

func expandRequiredScope(scope string) []string {
	expanded := []string{scope}
	if aliases, ok := mcpScopeAliases[scope]; ok {
		for _, alias := range aliases {
			alias = strings.TrimSpace(alias)
			if alias != "" {
				expanded = append(expanded, alias)
			}
		}
	}
	return expanded
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

	code := MCPErrorCodeInternal
	switch status {
	case http.StatusBadRequest:
		code = MCPErrorCodeInvalidArg
	case http.StatusForbidden:
		code = MCPErrorCodeScopeDenied
	case http.StatusNotFound:
		code = MCPErrorCodeNotFound
	case http.StatusTooManyRequests:
		code = MCPErrorCodeRateLimited
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

func readInputBool(input map[string]any, keys ...string) (bool, bool, error) {
	value, ok := findInputValue(input, keys...)
	if !ok {
		return false, false, nil
	}
	out, err := castToBool(value)
	if err != nil {
		return false, false, errorsx.ErrInvalidArgument
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

//nolint:gocognit,gocyclo,wsl_v5 // Boolean parsing supports permissive MCP-compatible input types.
func castToBool(value any) (bool, error) {
	switch typed := value.(type) {
	case bool:
		return typed, nil
	case string:
		trimmed := strings.ToLower(strings.TrimSpace(typed))
		switch trimmed {
		case "1", "true", "yes", "y":
			return true, nil
		case "0", "false", "no", "n":
			return false, nil
		default:
			return false, errorsx.ErrInvalidArgument
		}
	case json.Number:
		out, err := typed.Int64()
		if err != nil {
			return false, err
		}
		switch out {
		case 1:
			return true, nil
		case 0:
			return false, nil
		default:
			return false, errorsx.ErrInvalidArgument
		}
	case float64:
		switch typed {
		case 1:
			return true, nil
		case 0:
			return false, nil
		default:
			return false, errorsx.ErrInvalidArgument
		}
	case int:
		switch typed {
		case 1:
			return true, nil
		case 0:
			return false, nil
		default:
			return false, errorsx.ErrInvalidArgument
		}
	case int64:
		switch typed {
		case 1:
			return true, nil
		case 0:
			return false, nil
		default:
			return false, errorsx.ErrInvalidArgument
		}
	default:
		return false, errorsx.ErrInvalidArgument
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

func buildMCPIncidentOutput(incident *v1.Incident) map[string]any {
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
	putNonEmpty(out, "tenantID", strings.TrimSpace(incident.GetTenantID()))
	putNonEmpty(out, "cluster", strings.TrimSpace(incident.GetCluster()))
	putNonEmpty(out, "environment", strings.TrimSpace(incident.GetEnvironment()))
	putNonEmpty(out, "source", strings.TrimSpace(incident.GetSource()))
	putNonEmpty(out, "alertName", strings.TrimSpace(incident.GetAlertName()))
	putNonEmpty(out, "fingerprint", strings.TrimSpace(incident.GetFingerprint()))
	putNonEmpty(out, "ruleID", strings.TrimSpace(incident.GetRuleID()))
	putNonEmpty(out, "traceID", strings.TrimSpace(incident.GetTraceID()))
	putNonEmpty(out, "logTraceKey", strings.TrimSpace(incident.GetLogTraceKey()))
	putNonEmpty(out, "changeID", strings.TrimSpace(incident.GetChangeID()))
	if ts := timestampToRFC3339(incident.GetStartAt()); ts != "" {
		out["startAt"] = ts
	}
	if ts := timestampToRFC3339(incident.GetEndAt()); ts != "" {
		out["endAt"] = ts
	}
	putNonEmpty(out, "rcaStatus", strings.TrimSpace(incident.GetRcaStatus()))
	putNonEmpty(out, "rootCauseSummary", strings.TrimSpace(incident.GetRootCauseSummary()))
	if diagnosis := strings.TrimSpace(incident.GetDiagnosisJSON()); diagnosis != "" {
		putNonEmpty(out, "rootCauseType", extractRootCauseTypeFromDiagnosis(diagnosis))
		out["diagnosisJSON"] = sanitizeJSONString(diagnosis)
	}
	if evidenceRefs := strings.TrimSpace(incident.GetEvidenceRefsJSON()); evidenceRefs != "" {
		out["evidenceRefsJSON"] = sanitizeJSONString(evidenceRefs)
	}
	return out
}

func buildMCPAlertEventOutput(item *v1.AlertEvent) map[string]any {
	out := map[string]any{
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
		"createdAt":   timestampToRFC3339(item.GetCreatedAt()),
		"updatedAt":   timestampToRFC3339(item.GetUpdatedAt()),
	}
	putNonEmpty(out, "incidentID", strings.TrimSpace(item.GetIncidentID()))
	putNonEmpty(out, "dedupKey", strings.TrimSpace(item.GetDedupKey()))
	putNonEmpty(out, "source", strings.TrimSpace(item.GetSource()))
	putNonEmpty(out, "alertName", strings.TrimSpace(item.GetAlertName()))
	putNonEmpty(out, "silenceID", strings.TrimSpace(item.GetSilenceID()))
	putNonEmpty(out, "ackedBy", strings.TrimSpace(item.GetAckedBy()))
	if ts := timestampToRFC3339(item.GetStartsAt()); ts != "" {
		out["startsAt"] = ts
	}
	if ts := timestampToRFC3339(item.GetEndsAt()); ts != "" {
		out["endsAt"] = ts
	}
	if ts := timestampToRFC3339(item.GetAckedAt()); ts != "" {
		out["ackedAt"] = ts
	}
	return out
}

func buildMCPDatasourceOutput(item *v1.Datasource) map[string]any {
	out := map[string]any{
		"datasourceID": item.GetDatasourceID(),
		"type":         item.GetType(),
		"name":         item.GetName(),
		"baseURL":      item.GetBaseURL(),
		"authType":     item.GetAuthType(),
		"timeoutMs":    item.GetTimeoutMs(),
		"isEnabled":    item.GetIsEnabled(),
		"createdAt":    timestampToRFC3339(item.GetCreatedAt()),
		"updatedAt":    timestampToRFC3339(item.GetUpdatedAt()),
	}
	if labels := strings.TrimSpace(item.GetLabelsJSON()); labels != "" {
		out["labelsJSON"] = sanitizeJSONString(labels)
	}
	return out
}

func buildMCPAIJobOutput(job *v1.AIJob) map[string]any {
	out := map[string]any{
		"jobID":          job.GetJobID(),
		"incidentID":     job.GetIncidentID(),
		"pipeline":       job.GetPipeline(),
		"trigger":        job.GetTrigger(),
		"status":         job.GetStatus(),
		"timeRangeStart": timestampToRFC3339(job.GetTimeRangeStart()),
		"timeRangeEnd":   timestampToRFC3339(job.GetTimeRangeEnd()),
		"createdAt":      timestampToRFC3339(job.GetCreatedAt()),
		"createdBy":      job.GetCreatedBy(),
	}
	if ts := timestampToRFC3339(job.GetStartedAt()); ts != "" {
		out["startedAt"] = ts
	}
	if ts := timestampToRFC3339(job.GetFinishedAt()); ts != "" {
		out["finishedAt"] = ts
	}
	if hints := strings.TrimSpace(job.GetInputHintsJSON()); hints != "" {
		out["inputHintsJSON"] = sanitizeJSONString(hints)
	}
	if summary := strings.TrimSpace(job.GetOutputSummary()); summary != "" {
		out["outputSummary"] = sanitizeString(summary)
	}
	if output := strings.TrimSpace(job.GetOutputJSON()); output != "" {
		out["outputJSON"] = sanitizeJSONString(output)
	}
	if evidence := strings.TrimSpace(job.GetEvidenceIDsJSON()); evidence != "" {
		out["evidenceIDsJSON"] = sanitizeJSONString(evidence)
	}
	if msg := strings.TrimSpace(job.GetErrorMessage()); msg != "" {
		out["errorMessage"] = sanitizeAuditError(msg)
	}
	return out
}

func buildMCPToolCallOutput(item *v1.AIToolCall) map[string]any {
	out := map[string]any{
		"toolCallID":        item.GetToolCallID(),
		"jobID":             item.GetJobID(),
		"seq":               item.GetSeq(),
		"nodeName":          item.GetNodeName(),
		"toolName":          item.GetToolName(),
		"responseSizeBytes": item.GetResponseSizeBytes(),
		"status":            item.GetStatus(),
		"latencyMs":         item.GetLatencyMs(),
		"createdAt":         timestampToRFC3339(item.GetCreatedAt()),
	}
	if input := sanitizeAuditPayload(item.GetRequestJSON(), mcpMaxAuditInputBytes); input != nil {
		out["input"] = input
	}
	if output := sanitizeAuditPayload(item.GetResponseJSON(), mcpMaxAuditOutputBytes); output != nil {
		out["output"] = output
	}
	if errText := strings.TrimSpace(item.GetErrorMessage()); errText != "" {
		out["error"] = sanitizeAuditError(errText)
	}
	if evidence := sanitizeAuditPayload(item.GetEvidenceIDsJSON(), mcpMaxAuditOutputBytes); evidence != nil {
		out["evidence"] = evidence
	}
	return out
}

func buildMCPSilenceOutput(item *v1.Silence) map[string]any {
	out := map[string]any{
		"silenceID": item.GetSilenceID(),
		"namespace": item.GetNamespace(),
		"enabled":   item.GetEnabled(),
		"startsAt":  timestampToRFC3339(item.GetStartsAt()),
		"endsAt":    timestampToRFC3339(item.GetEndsAt()),
		"createdAt": timestampToRFC3339(item.GetCreatedAt()),
		"updatedAt": timestampToRFC3339(item.GetUpdatedAt()),
	}
	putNonEmpty(out, "reason", strings.TrimSpace(item.GetReason()))
	putNonEmpty(out, "createdBy", strings.TrimSpace(item.GetCreatedBy()))
	matchers := item.GetMatchers()
	if len(matchers) > 0 {
		matcherItems := make([]map[string]any, 0, len(matchers))
		for _, matcher := range matchers {
			matcherItems = append(matcherItems, map[string]any{
				"key":   matcher.GetKey(),
				"op":    matcher.GetOp(),
				"value": matcher.GetValue(),
			})
		}
		out["matchers"] = matcherItems
	}
	return out
}

func buildMCPNoticeDeliveryOutput(item *v1.NoticeDelivery) map[string]any {
	out := map[string]any{
		"deliveryID":  item.GetDeliveryID(),
		"channelID":   item.GetChannelID(),
		"eventType":   item.GetEventType(),
		"latencyMs":   item.GetLatencyMs(),
		"status":      item.GetStatus(),
		"createdAt":   timestampToRFC3339(item.GetCreatedAt()),
		"attempts":    item.GetAttempts(),
		"maxAttempts": item.GetMaxAttempts(),
	}
	putNonEmpty(out, "incidentID", strings.TrimSpace(item.GetIncidentID()))
	putNonEmpty(out, "jobID", strings.TrimSpace(item.GetJobID()))
	putNonEmpty(out, "lockedBy", strings.TrimSpace(item.GetLockedBy()))
	if item.ResponseCode != nil {
		out["responseCode"] = item.GetResponseCode()
	}
	if ts := timestampToRFC3339(item.GetNextRetryAt()); ts != "" {
		out["nextRetryAt"] = ts
	}
	if ts := timestampToRFC3339(item.GetLockedAt()); ts != "" {
		out["lockedAt"] = ts
	}
	return out
}

func sanitizeAuditPayload(raw string, maxBytes int) any {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	sanitized := clampJSONStringByBytes(sanitizeJSONString(trimmed), maxBytes)
	if strings.TrimSpace(sanitized) == "" {
		return nil
	}
	var payload any
	if err := json.Unmarshal([]byte(sanitized), &payload); err == nil {
		return sanitizeAny(payload)
	}
	return sanitizeString(sanitized)
}

func sanitizeAuditError(raw string) string {
	clipped, _ := truncateStringByBytes(sanitizeString(raw), mcpMaxAuditErrorBytes)
	return clipped
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
	lower := strings.ToLower(trimmed)
	if containsSensitiveToken(lower) {
		return "[REDACTED]"
	}

	// Keep it simple and deterministic: remove obvious bearer token text.
	idx := strings.Index(lower, "bearer ")
	if idx >= 0 {
		return trimmed[:idx] + "Bearer [REDACTED]"
	}
	return trimmed
}

func containsSensitiveToken(lower string) bool {
	return strings.Contains(lower, "secret") ||
		strings.Contains(lower, "authorization") ||
		strings.Contains(lower, "\"token\"") ||
		strings.Contains(lower, "token=") ||
		strings.Contains(lower, "token:") ||
		strings.Contains(lower, "x-api-key") ||
		strings.Contains(lower, "apikey")
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
