package ai_job

import (
	"context"
	"strings"
	"time"

	"github.com/onexstack/onexstack/pkg/errorsx"
	"gorm.io/gorm"

	sessionbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/session"
	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

const (
	defaultSessionWorkbenchRecentLimit = int64(10)
	maxSessionWorkbenchRecentLimit     = int64(50)

	workbenchHintNeedHumanReview     = "need_human_review"
	workbenchHintAwaitMoreEvidence   = "await_more_evidence"
	workbenchHintReviewConflicts     = "review_conflicts"
	workbenchHintVerificationPending = "verification_pending"
	workbenchHintRunInProgress       = "run_in_progress"
	workbenchHintReviewCompare       = "review_compare"
	workbenchHintReviewInProgress    = "review_in_progress"
	workbenchHintConsiderFollowUp    = "consider_follow_up"
	workbenchHintConsiderReplay      = "consider_replay"
)

type GetSessionWorkbenchRequest struct {
	SessionID   string
	RecentLimit int64
}

type GetSessionWorkbenchResponse struct {
	Session          *SessionWorkbenchReadModel  `json:"session"`
	Incident         *IncidentWorkbenchReadModel `json:"incident,omitempty"`
	LatestRun        *TraceReadSummary           `json:"latest_run,omitempty"`
	LatestDecision   *DecisionTraceReadModel     `json:"latest_decision,omitempty"`
	RecentRuns       []*TraceReadSummary         `json:"recent_runs"`
	RecentTotalCount int64                       `json:"recent_total_count"`
	LatestCompare    *WorkbenchCompareSummary    `json:"latest_compare,omitempty"`
	ReviewFlags      *WorkbenchReviewFlags       `json:"review_flags"`
	NextActionHints  []string                    `json:"next_action_hints"`
}

type SessionWorkbenchReadModel struct {
	SessionID          string         `json:"session_id"`
	SessionType        string         `json:"session_type"`
	BusinessKey        string         `json:"business_key"`
	IncidentID         string         `json:"incident_id,omitempty"`
	Title              string         `json:"title,omitempty"`
	Status             string         `json:"status"`
	ActiveRunID        string         `json:"active_run_id,omitempty"`
	LatestSummary      map[string]any `json:"latest_summary,omitempty"`
	PinnedEvidenceRefs []string       `json:"pinned_evidence_refs"`
	HasPinnedEvidence  bool           `json:"has_pinned_evidence"`
	ReviewState        string         `json:"review_state"`
	ReviewNote         string         `json:"review_note,omitempty"`
	ReviewedBy         string         `json:"reviewed_by,omitempty"`
	ReviewedAt         string         `json:"reviewed_at,omitempty"`
	ReviewReasonCode   string         `json:"review_reason_code,omitempty"`
	CreatedAt          string         `json:"created_at,omitempty"`
	UpdatedAt          string         `json:"updated_at,omitempty"`
}

type IncidentWorkbenchReadModel struct {
	IncidentID   string `json:"incident_id"`
	Service      string `json:"service,omitempty"`
	Namespace    string `json:"namespace,omitempty"`
	Environment  string `json:"environment,omitempty"`
	Status       string `json:"status,omitempty"`
	RCAStatus    string `json:"rca_status,omitempty"`
	Severity     string `json:"severity,omitempty"`
	WorkloadKind string `json:"workload_kind,omitempty"`
	WorkloadName string `json:"workload_name,omitempty"`
	Title        string `json:"title,omitempty"`
}

type WorkbenchCompareSummary struct {
	LeftJobID         string  `json:"left_job_id"`
	RightJobID        string  `json:"right_job_id"`
	SameSession       bool    `json:"same_session"`
	SameIncident      bool    `json:"same_incident"`
	ChangedRootCause  bool    `json:"changed_root_cause"`
	ChangedConfidence bool    `json:"changed_confidence"`
	LeftTriggerType   string  `json:"left_trigger_type,omitempty"`
	RightTriggerType  string  `json:"right_trigger_type,omitempty"`
	LeftPipeline      string  `json:"left_pipeline,omitempty"`
	RightPipeline     string  `json:"right_pipeline,omitempty"`
	LeftSummary       string  `json:"left_summary,omitempty"`
	RightSummary      string  `json:"right_summary,omitempty"`
	LeftConfidence    float64 `json:"left_confidence"`
	RightConfidence   float64 `json:"right_confidence"`
}

type WorkbenchReviewFlags struct {
	HumanReviewRequired bool     `json:"human_review_required"`
	MissingFacts        []string `json:"missing_facts"`
	Conflicts           []string `json:"conflicts"`
	VerificationRefs    []string `json:"verification_refs"`
	VerificationCount   int64    `json:"verification_count"`
	HasPinnedEvidence   bool     `json:"has_pinned_evidence"`
	PinnedEvidenceCount int64    `json:"pinned_evidence_count"`
}

type sessionReviewContextState struct {
	State      string
	Note       string
	ReviewedBy string
	ReviewedAt string
	ReasonCode string
}

func (b *aiJobBiz) GetSessionWorkbench(
	ctx context.Context,
	rq *GetSessionWorkbenchRequest,
) (*GetSessionWorkbenchResponse, error) {
	if rq == nil {
		return nil, errorsx.ErrInvalidArgument
	}
	sessionID := strings.TrimSpace(rq.SessionID)
	if sessionID == "" {
		return nil, errorsx.ErrInvalidArgument
	}

	limit := rq.RecentLimit
	if limit <= 0 {
		limit = defaultSessionWorkbenchRecentLimit
	}
	if limit > maxSessionWorkbenchRecentLimit {
		return nil, errorsx.ErrInvalidArgument
	}

	sessionObj, err := b.store.SessionContext().Get(ctx, where.T(ctx).F("session_id", sessionID))
	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrSessionContextNotFound
		}
		return nil, errno.ErrSessionContextGetFailed
	}

	listResp, err := b.ListTraceReadModels(ctx, &ListTraceReadModelsRequest{
		SessionID: &sessionID,
		Offset:    0,
		Limit:     limit,
	})
	if err != nil {
		return nil, err
	}

	recentRuns := listResp.Summaries
	latestRun := firstTraceReadSummary(recentRuns)

	var latestDecision *DecisionTraceReadModel
	if latestRun != nil && strings.TrimSpace(latestRun.JobID) != "" {
		traceResp, traceErr := b.GetTraceReadModel(ctx, &GetTraceReadModelRequest{JobID: latestRun.JobID})
		if traceErr == nil && traceResp != nil {
			latestDecision = traceResp.DecisionTrace
		}
	}

	incidentID := firstTraceNonEmpty(trimOptional(sessionObj.IncidentID), traceSummaryIncidentID(latestRun))
	incidentBlock, err := b.loadWorkbenchIncident(ctx, incidentID)
	if err != nil {
		return nil, err
	}

	pinnedEvidenceRefs := extractPinnedEvidenceRefs(sessionObj.PinnedEvidenceJSON)
	reviewState := extractSessionReviewState(sessionObj.ContextStateJSON)
	latestCompare := b.buildLatestWorkbenchCompare(ctx, recentRuns)
	reviewFlags := buildWorkbenchReviewFlags(latestDecision, latestRun, pinnedEvidenceRefs)
	nextHints := buildWorkbenchNextActionHints(
		latestRun,
		latestDecision,
		reviewFlags,
		latestCompare,
		trimOptional(sessionObj.ActiveRunID),
		reviewState,
	)

	return &GetSessionWorkbenchResponse{
		Session:          sessionToWorkbench(sessionObj, pinnedEvidenceRefs, reviewState),
		Incident:         incidentBlock,
		LatestRun:        latestRun,
		LatestDecision:   latestDecision,
		RecentRuns:       recentRuns,
		RecentTotalCount: listResp.TotalCount,
		LatestCompare:    latestCompare,
		ReviewFlags:      reviewFlags,
		NextActionHints:  nextHints,
	}, nil
}

func (b *aiJobBiz) loadWorkbenchIncident(
	ctx context.Context,
	incidentID string,
) (*IncidentWorkbenchReadModel, error) {
	incidentID = strings.TrimSpace(incidentID)
	if incidentID == "" {
		return nil, nil
	}
	incident, err := b.store.Incident().Get(ctx, where.T(ctx).F("incident_id", incidentID))
	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, errno.ErrIncidentGetFailed
	}
	return incidentToWorkbench(incident), nil
}

func (b *aiJobBiz) buildLatestWorkbenchCompare(
	ctx context.Context,
	recentRuns []*TraceReadSummary,
) *WorkbenchCompareSummary {
	leftJobID, rightJobID := pickLatestCompareJobPair(recentRuns)
	if leftJobID == "" || rightJobID == "" {
		return nil
	}
	cmp, err := b.CompareTraceReadModels(ctx, &CompareTraceReadModelsRequest{
		LeftJobID:  leftJobID,
		RightJobID: rightJobID,
	})
	if err != nil || cmp == nil || cmp.Left == nil || cmp.Right == nil {
		return nil
	}
	return &WorkbenchCompareSummary{
		LeftJobID:         strings.TrimSpace(cmp.Left.JobID),
		RightJobID:        strings.TrimSpace(cmp.Right.JobID),
		SameSession:       cmp.SameSession,
		SameIncident:      cmp.SameIncident,
		ChangedRootCause:  cmp.ChangedRootCause,
		ChangedConfidence: cmp.ChangedConfidence,
		LeftTriggerType:   strings.TrimSpace(cmp.Left.TriggerType),
		RightTriggerType:  strings.TrimSpace(cmp.Right.TriggerType),
		LeftPipeline:      strings.TrimSpace(cmp.Left.Pipeline),
		RightPipeline:     strings.TrimSpace(cmp.Right.Pipeline),
		LeftSummary:       strings.TrimSpace(cmp.Left.RootCauseSummary),
		RightSummary:      strings.TrimSpace(cmp.Right.RootCauseSummary),
		LeftConfidence:    cmp.Left.Confidence,
		RightConfidence:   cmp.Right.Confidence,
	}
}

func firstTraceReadSummary(in []*TraceReadSummary) *TraceReadSummary {
	for _, item := range in {
		if item != nil {
			return item
		}
	}
	return nil
}

func traceSummaryIncidentID(in *TraceReadSummary) string {
	if in == nil {
		return ""
	}
	return strings.TrimSpace(in.IncidentID)
}

func sessionToWorkbench(
	in *model.SessionContextM,
	pinnedRefs []string,
	reviewState *sessionReviewContextState,
) *SessionWorkbenchReadModel {
	if in == nil {
		return &SessionWorkbenchReadModel{}
	}
	if reviewState == nil {
		reviewState = &sessionReviewContextState{State: sessionbiz.SessionReviewStatePending}
	}
	latestSummary := parseOptionalJSONObject(in.LatestSummaryJSON)
	return &SessionWorkbenchReadModel{
		SessionID:          strings.TrimSpace(in.SessionID),
		SessionType:        strings.TrimSpace(in.SessionType),
		BusinessKey:        strings.TrimSpace(in.BusinessKey),
		IncidentID:         trimOptional(in.IncidentID),
		Title:              trimOptional(in.Title),
		Status:             strings.TrimSpace(in.Status),
		ActiveRunID:        trimOptional(in.ActiveRunID),
		LatestSummary:      latestSummary,
		PinnedEvidenceRefs: append([]string(nil), pinnedRefs...),
		HasPinnedEvidence:  len(pinnedRefs) > 0,
		ReviewState:        strings.TrimSpace(reviewState.State),
		ReviewNote:         strings.TrimSpace(reviewState.Note),
		ReviewedBy:         strings.TrimSpace(reviewState.ReviewedBy),
		ReviewedAt:         strings.TrimSpace(reviewState.ReviewedAt),
		ReviewReasonCode:   strings.TrimSpace(reviewState.ReasonCode),
		CreatedAt:          toRFC3339String(in.CreatedAt),
		UpdatedAt:          toRFC3339String(in.UpdatedAt),
	}
}

func incidentToWorkbench(in *model.IncidentM) *IncidentWorkbenchReadModel {
	if in == nil {
		return nil
	}
	return &IncidentWorkbenchReadModel{
		IncidentID:   strings.TrimSpace(in.IncidentID),
		Service:      strings.TrimSpace(in.Service),
		Namespace:    strings.TrimSpace(in.Namespace),
		Environment:  strings.TrimSpace(in.Environment),
		Status:       strings.TrimSpace(in.Status),
		RCAStatus:    strings.TrimSpace(in.RCAStatus),
		Severity:     strings.TrimSpace(in.Severity),
		WorkloadKind: strings.TrimSpace(in.WorkloadKind),
		WorkloadName: strings.TrimSpace(in.WorkloadName),
		Title:        incidentTitle(in),
	}
}

func buildWorkbenchReviewFlags(
	latestDecision *DecisionTraceReadModel,
	latestRun *TraceReadSummary,
	pinnedEvidenceRefs []string,
) *WorkbenchReviewFlags {
	flags := &WorkbenchReviewFlags{
		MissingFacts:        []string{},
		Conflicts:           []string{},
		VerificationRefs:    []string{},
		VerificationCount:   0,
		HasPinnedEvidence:   len(pinnedEvidenceRefs) > 0,
		PinnedEvidenceCount: int64(len(pinnedEvidenceRefs)),
	}
	if latestDecision == nil {
		if latestRun != nil {
			flags.VerificationCount = latestRun.VerificationCount
		}
		return flags
	}
	flags.HumanReviewRequired = latestDecision.HumanReviewRequired
	flags.MissingFacts = normalizeStringSlice(latestDecision.MissingFacts)
	flags.Conflicts = normalizeStringSlice(latestDecision.Conflicts)
	flags.VerificationRefs = normalizeStringSlice(latestDecision.VerificationRefs)
	flags.VerificationCount = int64(len(flags.VerificationRefs))
	if flags.VerificationCount == 0 && latestRun != nil {
		flags.VerificationCount = latestRun.VerificationCount
	}
	return flags
}

func buildWorkbenchNextActionHints(
	latestRun *TraceReadSummary,
	latestDecision *DecisionTraceReadModel,
	reviewFlags *WorkbenchReviewFlags,
	latestCompare *WorkbenchCompareSummary,
	activeRunID string,
	reviewState *sessionReviewContextState,
) []string {
	hints := make([]string, 0, 8)
	state := sessionbiz.SessionReviewStatePending
	if reviewState != nil {
		state = normalizeWorkbenchReviewState(reviewState.State)
	}
	if strings.TrimSpace(activeRunID) != "" || isActiveRunStatus(latestRun) {
		hints = appendUniqueHint(hints, workbenchHintRunInProgress)
	}
	if state == sessionbiz.SessionReviewStateInReview {
		hints = appendUniqueHint(hints, workbenchHintReviewInProgress)
	}
	reviewTerminal := state == sessionbiz.SessionReviewStateConfirmed || state == sessionbiz.SessionReviewStateRejected
	if reviewFlags != nil {
		if reviewFlags.HumanReviewRequired && !reviewTerminal && state != sessionbiz.SessionReviewStateInReview {
			hints = appendUniqueHint(hints, workbenchHintNeedHumanReview)
		}
		if len(reviewFlags.MissingFacts) > 0 {
			hints = appendUniqueHint(hints, workbenchHintAwaitMoreEvidence)
		}
		if len(reviewFlags.Conflicts) > 0 && state != sessionbiz.SessionReviewStateConfirmed {
			hints = appendUniqueHint(hints, workbenchHintReviewConflicts)
		}
	}
	if latestDecision != nil && latestDecision.Confidence < humanReviewConfidenceGate {
		verificationRefs := []string{}
		if reviewFlags != nil {
			verificationRefs = reviewFlags.VerificationRefs
		}
		if len(verificationRefs) == 0 {
			hints = appendUniqueHint(hints, workbenchHintVerificationPending)
		}
	}
	if latestCompare != nil && (latestCompare.ChangedRootCause || latestCompare.ChangedConfidence) {
		hints = appendUniqueHint(hints, workbenchHintReviewCompare)
	}
	if state == sessionbiz.SessionReviewStateRejected {
		hints = appendUniqueHint(hints, workbenchHintConsiderFollowUp)
		hints = appendUniqueHint(hints, workbenchHintConsiderReplay)
	} else if state != sessionbiz.SessionReviewStateConfirmed {
		if latestRun != nil && shouldSuggestFollowUp(latestRun, reviewFlags) {
			hints = appendUniqueHint(hints, workbenchHintConsiderFollowUp)
		}
		if latestRun != nil && shouldSuggestReplay(latestRun, reviewFlags) {
			hints = appendUniqueHint(hints, workbenchHintConsiderReplay)
		}
	}
	return hints
}

func shouldSuggestFollowUp(latestRun *TraceReadSummary, reviewFlags *WorkbenchReviewFlags) bool {
	if latestRun == nil || reviewFlags == nil {
		return false
	}
	if reviewFlags.HumanReviewRequired || len(reviewFlags.MissingFacts) > 0 || len(reviewFlags.Conflicts) > 0 {
		return false
	}
	switch strings.TrimSpace(latestRun.TriggerType) {
	case "alert", "manual", "cron", "change":
		return strings.TrimSpace(latestRun.Status) == jobStatusSucceeded
	default:
		return false
	}
}

func shouldSuggestReplay(latestRun *TraceReadSummary, reviewFlags *WorkbenchReviewFlags) bool {
	if latestRun == nil || reviewFlags == nil {
		return false
	}
	if strings.TrimSpace(latestRun.TriggerType) != "follow_up" {
		return false
	}
	if strings.TrimSpace(latestRun.Status) != jobStatusSucceeded {
		return false
	}
	return reviewFlags.HumanReviewRequired || len(reviewFlags.Conflicts) > 0
}

func isActiveRunStatus(latestRun *TraceReadSummary) bool {
	if latestRun == nil {
		return false
	}
	switch strings.TrimSpace(latestRun.Status) {
	case jobStatusQueued, jobStatusRunning:
		return true
	default:
		return false
	}
}

func appendUniqueHint(hints []string, hint string) []string {
	hint = strings.TrimSpace(hint)
	if hint == "" {
		return hints
	}
	for _, existing := range hints {
		if existing == hint {
			return hints
		}
	}
	return append(hints, hint)
}

func pickLatestCompareJobPair(recentRuns []*TraceReadSummary) (string, string) {
	for idx, candidate := range recentRuns {
		if candidate == nil {
			continue
		}
		triggerType := strings.TrimSpace(candidate.TriggerType)
		if triggerType != "replay" && triggerType != "follow_up" {
			continue
		}
		rightJobID := strings.TrimSpace(candidate.JobID)
		if rightJobID == "" {
			continue
		}
		for j := idx + 1; j < len(recentRuns); j++ {
			base := recentRuns[j]
			if base == nil {
				continue
			}
			leftJobID := strings.TrimSpace(base.JobID)
			if leftJobID == "" {
				continue
			}
			if leftJobID == rightJobID {
				continue
			}
			return leftJobID, rightJobID
		}
	}
	return "", ""
}

func parseOptionalJSONObject(raw *string) map[string]any {
	if raw == nil {
		return nil
	}
	return parseJSONObject(*raw)
}

func extractSessionReviewState(raw *string) *sessionReviewContextState {
	out := &sessionReviewContextState{State: sessionbiz.SessionReviewStatePending}
	obj := parseOptionalJSONObject(raw)
	if obj == nil {
		return out
	}
	reviewRaw, ok := obj["review"]
	if !ok {
		return out
	}
	reviewObj, ok := reviewRaw.(map[string]any)
	if !ok {
		return out
	}
	out.State = normalizeWorkbenchReviewState(anyToString(reviewObj["state"]))
	out.Note = strings.TrimSpace(anyToString(reviewObj["note"]))
	out.ReviewedBy = strings.TrimSpace(anyToString(reviewObj["reviewed_by"]))
	out.ReviewedAt = strings.TrimSpace(anyToString(reviewObj["reviewed_at"]))
	out.ReasonCode = strings.TrimSpace(anyToString(reviewObj["reason_code"]))
	return out
}

func normalizeWorkbenchReviewState(raw string) string {
	state := strings.ToLower(strings.TrimSpace(raw))
	switch state {
	case sessionbiz.SessionReviewStateInReview, sessionbiz.SessionReviewStateConfirmed, sessionbiz.SessionReviewStateRejected:
		return state
	default:
		return sessionbiz.SessionReviewStatePending
	}
}

func extractPinnedEvidenceRefs(raw *string) []string {
	obj := parseOptionalJSONObject(raw)
	if obj == nil {
		return []string{}
	}
	if refs, ok := obj["refs"]; ok {
		return extractStringSlice(refs)
	}
	if refs, ok := obj["evidence_refs"]; ok {
		return extractStringSlice(refs)
	}
	return []string{}
}

func extractStringSlice(raw any) []string {
	switch value := raw.(type) {
	case []string:
		return normalizeStringSlice(value)
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			v := strings.TrimSpace(anyToString(item))
			if v == "" {
				continue
			}
			out = append(out, v)
		}
		return normalizeStringSlice(out)
	case string:
		v := strings.TrimSpace(value)
		if v == "" {
			return []string{}
		}
		return []string{v}
	default:
		return []string{}
	}
}

func incidentTitle(incident *model.IncidentM) string {
	if incident == nil {
		return ""
	}
	if v := strings.TrimSpace(trimOptional(incident.AlertName)); v != "" {
		return v
	}
	if v := strings.TrimSpace(incident.Service); v != "" {
		if workload := strings.TrimSpace(incident.WorkloadName); workload != "" {
			return v + "/" + workload
		}
		return v
	}
	return strings.TrimSpace(incident.IncidentID)
}

func toRFC3339String(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return ts.UTC().Format(time.RFC3339Nano)
}
