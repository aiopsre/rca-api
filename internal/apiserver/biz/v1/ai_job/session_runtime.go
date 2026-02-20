package ai_job

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	sessionbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/session"
	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/runtimecontract"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

type GetJobSessionContextRequest struct {
	JobID string `json:"job_id"`
}

type GetJobSessionContextResponse struct {
	SessionID       string           `json:"session_id"`
	SessionRevision string           `json:"session_revision"`
	LatestSummary   map[string]any   `json:"latest_summary,omitempty"`
	PinnedEvidence  []map[string]any `json:"pinned_evidence,omitempty"`
	ContextState    map[string]any   `json:"context_state,omitempty"`
	ActiveRunID     string           `json:"active_run_id,omitempty"`
}

type PatchJobSessionContextRequest struct {
	JobID                string           `json:"job_id"`
	SessionRevision      *string          `json:"session_revision,omitempty"`
	LatestSummary        *json.RawMessage `json:"latest_summary,omitempty"`
	PinnedEvidenceAppend []map[string]any `json:"pinned_evidence_append,omitempty"`
	PinnedEvidenceRemove []string         `json:"pinned_evidence_remove,omitempty"`
	ContextStatePatch    *json.RawMessage `json:"context_state_patch,omitempty"`
	Actor                *string          `json:"actor,omitempty"`
	Note                 *string          `json:"note,omitempty"`
	Source               *string          `json:"source,omitempty"`
}

type PatchJobSessionContextResponse struct {
	SessionID       string           `json:"session_id"`
	SessionRevision string           `json:"session_revision"`
	LatestSummary   map[string]any   `json:"latest_summary,omitempty"`
	PinnedEvidence  []map[string]any `json:"pinned_evidence,omitempty"`
	ContextState    map[string]any   `json:"context_state,omitempty"`
	ActiveRunID     string           `json:"active_run_id,omitempty"`
}

func (b *aiJobBiz) GetJobSessionContext(
	ctx context.Context,
	rq *GetJobSessionContextRequest,
) (*GetJobSessionContextResponse, error) {
	if rq == nil {
		return nil, errno.ErrInvalidArgument
	}
	_, sessionObj, err := b.loadJobSessionContext(ctx, strings.TrimSpace(rq.JobID))
	if err != nil {
		return nil, err
	}
	return buildJobSessionContextResponse(sessionObj), nil
}

func (b *aiJobBiz) PatchJobSessionContext(
	ctx context.Context,
	rq *PatchJobSessionContextRequest,
) (*PatchJobSessionContextResponse, error) {
	if rq == nil {
		return nil, errno.ErrInvalidArgument
	}
	_, sessionObj, err := b.loadJobSessionContext(ctx, strings.TrimSpace(rq.JobID))
	if err != nil {
		return nil, err
	}
	currentRevision := sessionRevision(sessionObj)
	if rq.SessionRevision != nil && strings.TrimSpace(*rq.SessionRevision) != currentRevision {
		return nil, errno.ErrSessionContextRevisionConflict
	}

	updateReq := &sessionbiz.UpdateSessionContextRequest{
		SessionID: strings.TrimSpace(sessionObj.SessionID),
	}
	updatedFields := map[string]any{}

	if rq.LatestSummary != nil {
		normalized, summaryPayload, latestErr := normalizeOptionalJSONPayload(*rq.LatestSummary)
		if latestErr != nil {
			return nil, errno.ErrInvalidArgument
		}
		updateReq.LatestSummaryJSON = normalized
		updatedFields["latest_summary"] = summaryPayload
	}

	if rq.ContextStatePatch != nil {
		mergedRaw, mergedPayload, mergeErr := applyContextStateMergePatch(sessionObj.ContextStateJSON, *rq.ContextStatePatch)
		if mergeErr != nil {
			return nil, errno.ErrInvalidArgument
		}
		updateReq.ContextStateJSON = mergedRaw
		updatedFields["context_state_patch"] = mergedPayload
	}

	if len(rq.PinnedEvidenceAppend) > 0 || len(rq.PinnedEvidenceRemove) > 0 {
		mergedRaw, mergedPayload, mergeErr := mergePinnedEvidence(
			sessionObj.PinnedEvidenceJSON,
			rq.PinnedEvidenceAppend,
			rq.PinnedEvidenceRemove,
		)
		if mergeErr != nil {
			return nil, errno.ErrInvalidArgument
		}
		updateReq.PinnedEvidenceJSON = mergedRaw
		updatedFields["pinned_evidence_count"] = len(mergedPayload)
	}

	updated, updateErr := b.sessionBiz.Update(ctx, updateReq)
	if updateErr != nil {
		return nil, updateErr
	}
	out := sessionObj
	if updated != nil && updated.Session != nil {
		out = updated.Session
	}

	source := strings.TrimSpace(trimOptional(rq.Source))
	if source == "" {
		source = "orchestrator_runtime"
	}
	payloadSummary := map[string]any{
		"source": source,
	}
	for key, value := range updatedFields {
		payloadSummary[key] = value
	}
	_, _ = b.sessionBiz.AppendHistoryEvent(ctx, &sessionbiz.AppendSessionHistoryEventRequest{
		SessionID:      strings.TrimSpace(out.SessionID),
		EventType:      sessionbiz.SessionHistoryEventContextPatched,
		IncidentID:     out.IncidentID,
		JobID:          &rq.JobID,
		Actor:          sanitizeHistoryTextPtr(rq.Actor),
		Note:           sanitizeHistoryTextPtr(rq.Note),
		PayloadSummary: payloadSummary,
	})

	return &PatchJobSessionContextResponse{
		SessionID:       strings.TrimSpace(out.SessionID),
		SessionRevision: sessionRevision(out),
		LatestSummary:   parseJSONObject(trimOptional(out.LatestSummaryJSON)),
		PinnedEvidence:  parsePinnedEvidenceList(out.PinnedEvidenceJSON),
		ContextState:    parseJSONObject(trimOptional(out.ContextStateJSON)),
		ActiveRunID:     trimOptional(out.ActiveRunID),
	}, nil
}

func (b *aiJobBiz) loadJobSessionContext(
	ctx context.Context,
	jobID string,
) (*model.AIJobM, *model.SessionContextM, error) {
	if strings.TrimSpace(jobID) == "" {
		return nil, nil, errno.ErrInvalidArgument
	}
	job, err := b.store.AIJob().Get(ctx, where.T(ctx).F("job_id", strings.TrimSpace(jobID)))
	if err != nil {
		return nil, nil, errno.ErrAIJobGetFailed
	}
	sessionID := trimOptional(job.SessionID)
	if sessionID == "" {
		incident, incidentErr := b.getIncident(ctx, job.IncidentID)
		if incidentErr == nil {
			sessionID = b.ensureIncidentSessionIDBestEffort(ctx, incident)
			if sessionID != "" {
				job.SessionID = &sessionID
				_ = b.store.AIJob().Update(ctx, job)
			}
		}
	}
	if sessionID == "" {
		return nil, nil, errno.ErrSessionContextNotFound
	}
	resp, err := b.sessionBiz.Get(ctx, &sessionbiz.GetSessionContextRequest{SessionID: &sessionID})
	if err != nil {
		return nil, nil, err
	}
	if resp == nil || resp.Session == nil {
		return nil, nil, errno.ErrSessionContextNotFound
	}
	return job, resp.Session, nil
}

func buildJobSessionContextResponse(sessionObj *model.SessionContextM) *GetJobSessionContextResponse {
	if sessionObj == nil {
		return &GetJobSessionContextResponse{}
	}
	return &GetJobSessionContextResponse{
		SessionID:       strings.TrimSpace(sessionObj.SessionID),
		SessionRevision: sessionRevision(sessionObj),
		LatestSummary:   parseJSONObject(trimOptional(sessionObj.LatestSummaryJSON)),
		PinnedEvidence:  parsePinnedEvidenceList(sessionObj.PinnedEvidenceJSON),
		ContextState:    parseJSONObject(trimOptional(sessionObj.ContextStateJSON)),
		ActiveRunID:     trimOptional(sessionObj.ActiveRunID),
	}
}

func sessionRevision(sessionObj *model.SessionContextM) string {
	if sessionObj == nil {
		return ""
	}
	return sessionObj.UpdatedAt.UTC().Format(time.RFC3339Nano)
}

func normalizeOptionalJSONPayload(raw json.RawMessage) (*string, map[string]any, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil, nil
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, nil, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, err
	}
	out := string(encoded)
	return &out, payload, nil
}

func applyContextStateMergePatch(base *string, patch json.RawMessage) (*string, map[string]any, error) {
	trimmed := strings.TrimSpace(string(patch))
	if trimmed == "" {
		return base, parseJSONObject(trimOptional(base)), nil
	}
	if trimmed == "null" {
		return nil, nil, nil
	}
	var patchObj map[string]any
	if err := json.Unmarshal(patch, &patchObj); err != nil {
		return nil, nil, err
	}
	current := parseJSONObject(trimOptional(base))
	merged := mergeJSONObject(current, patchObj)
	if len(merged) == 0 {
		return nil, map[string]any{}, nil
	}
	encoded, err := json.Marshal(merged)
	if err != nil {
		return nil, nil, err
	}
	out := string(encoded)
	return &out, merged, nil
}

func mergeJSONObject(base map[string]any, patch map[string]any) map[string]any {
	if base == nil {
		base = map[string]any{}
	}
	out := cloneJSONObject(base)
	for key, rawValue := range patch {
		if rawValue == nil {
			delete(out, key)
			continue
		}
		patchMap, patchIsMap := rawValue.(map[string]any)
		baseMap, baseIsMap := out[key].(map[string]any)
		if patchIsMap {
			if !baseIsMap {
				baseMap = map[string]any{}
			}
			out[key] = mergeJSONObject(baseMap, patchMap)
			continue
		}
		out[key] = rawValue
	}
	return out
}

func cloneJSONObject(in map[string]any) map[string]any {
	if len(in) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		if nested, ok := value.(map[string]any); ok {
			out[key] = cloneJSONObject(nested)
			continue
		}
		out[key] = value
	}
	return out
}

func mergePinnedEvidence(
	base *string,
	appendItems []map[string]any,
	removeIDs []string,
) (*string, []map[string]any, error) {
	current := parsePinnedEvidenceList(base)
	removeSet := map[string]struct{}{}
	for _, item := range runtimecontract.NormalizeStringList(removeIDs) {
		removeSet[item] = struct{}{}
	}
	out := make([]map[string]any, 0, len(current)+len(appendItems))
	seen := map[string]struct{}{}
	for _, item := range current {
		key := pinnedEvidenceKey(item)
		if key == "" {
			continue
		}
		if _, removed := removeSet[key]; removed {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	for _, item := range appendItems {
		if item == nil {
			continue
		}
		key := pinnedEvidenceKey(item)
		if key == "" {
			continue
		}
		if _, removed := removeSet[key]; removed {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, cloneJSONObject(item))
	}
	if len(out) == 0 {
		return nil, []map[string]any{}, nil
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, nil, err
	}
	raw := string(encoded)
	return &raw, out, nil
}

func parsePinnedEvidenceList(raw *string) []map[string]any {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return []map[string]any{}
	}
	var out []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(*raw)), &out); err != nil {
		return []map[string]any{}
	}
	if out == nil {
		return []map[string]any{}
	}
	return out
}

func pinnedEvidenceKey(item map[string]any) string {
	if len(item) == 0 {
		return ""
	}
	for _, key := range []string{"evidence_id", "evidenceID", "id"} {
		if value := strings.TrimSpace(anyToSessionString(item[key])); value != "" {
			return value
		}
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func anyToSessionString(value any) string {
	switch in := value.(type) {
	case string:
		return in
	default:
		return ""
	}
}

func sanitizeHistoryTextPtr(in *string) *string {
	if in == nil {
		return nil
	}
	value := strings.TrimSpace(*in)
	if value == "" {
		return nil
	}
	return &value
}
