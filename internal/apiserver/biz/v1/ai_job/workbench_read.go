package ai_job

import (
	"context"
	"sort"
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
	defaultOperatorInboxLimit          = int64(20)
	maxOperatorInboxLimit              = int64(100)
	maxOperatorInboxScan               = int64(500)
	operatorInboxTraceProbeLimit       = int64(5)

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

type ListOperatorInboxRequest struct {
	ReviewState *string
	NeedsReview *bool
	SessionType *string
	Assignee    *string
	Offset      int64
	Limit       int64
}

type ListOperatorInboxResponse struct {
	TotalCount int64                `json:"total_count"`
	Items      []*OperatorInboxItem `json:"items"`
}

type OperatorInboxItem struct {
	SessionID              string   `json:"session_id"`
	SessionType            string   `json:"session_type"`
	BusinessKey            string   `json:"business_key"`
	IncidentID             string   `json:"incident_id,omitempty"`
	Assignee               string   `json:"assignee,omitempty"`
	AssignedBy             string   `json:"assigned_by,omitempty"`
	AssignedAt             string   `json:"assigned_at,omitempty"`
	AssignNote             string   `json:"assign_note,omitempty"`
	ReviewState            string   `json:"review_state"`
	ReviewedBy             string   `json:"reviewed_by,omitempty"`
	ReviewedAt             string   `json:"reviewed_at,omitempty"`
	LatestJobID            string   `json:"latest_job_id,omitempty"`
	LatestTriggerType      string   `json:"latest_trigger_type,omitempty"`
	LatestPipeline         string   `json:"latest_pipeline,omitempty"`
	LatestSummary          string   `json:"latest_summary,omitempty"`
	LatestConfidence       float64  `json:"latest_confidence"`
	LatestUpdatedAt        string   `json:"latest_updated_at,omitempty"`
	HumanReviewRequired    bool     `json:"human_review_required"`
	MissingFactsCount      int64    `json:"missing_facts_count"`
	ConflictsCount         int64    `json:"conflicts_count"`
	VerificationPending    bool     `json:"verification_pending"`
	HasPinnedEvidence      bool     `json:"has_pinned_evidence"`
	NextActionHints        []string `json:"next_action_hints"`
	WorkbenchPath          string   `json:"workbench_path"`
	LatestCompareAvailable bool     `json:"latest_compare_available"`
	LastActivityAt         string   `json:"last_activity_at,omitempty"`
	NeedsReview            bool     `json:"needs_review"`
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
	Assignee           string         `json:"assignee,omitempty"`
	AssignedBy         string         `json:"assigned_by,omitempty"`
	AssignedAt         string         `json:"assigned_at,omitempty"`
	AssignNote         string         `json:"assign_note,omitempty"`
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

type sessionAssignmentContextState struct {
	Assignee   string
	AssignedBy string
	AssignedAt string
	Note       string
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
	assignmentState := extractSessionAssignmentState(sessionObj.ContextStateJSON)
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
		Session:          sessionToWorkbench(sessionObj, pinnedEvidenceRefs, reviewState, assignmentState),
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

func (b *aiJobBiz) ListOperatorInbox(
	ctx context.Context,
	rq *ListOperatorInboxRequest,
) (*ListOperatorInboxResponse, error) {
	if rq == nil {
		return nil, errorsx.ErrInvalidArgument
	}
	offset := rq.Offset
	if offset < 0 {
		return nil, errorsx.ErrInvalidArgument
	}
	limit := rq.Limit
	if limit <= 0 {
		limit = defaultOperatorInboxLimit
	}
	if limit > maxOperatorInboxLimit {
		return nil, errorsx.ErrInvalidArgument
	}
	reviewStateFilter := ""
	if value := trimOptional(rq.ReviewState); value != "" {
		reviewStateFilter = normalizeInboxReviewState(value)
	}
	needsReviewFilter := false
	if rq.NeedsReview != nil {
		needsReviewFilter = *rq.NeedsReview
	}
	hasNeedsReviewFilter := rq.NeedsReview != nil
	sessionTypeFilter := normalizeInboxSessionType(trimOptional(rq.SessionType))
	if trimOptional(rq.SessionType) != "" && sessionTypeFilter == "" {
		return nil, errorsx.ErrInvalidArgument
	}
	assigneeFilter := strings.TrimSpace(trimOptional(rq.Assignee))

	whr := where.T(ctx).O(0).L(int(maxOperatorInboxScan))
	if sessionTypeFilter != "" {
		whr = whr.F("session_type", sessionTypeFilter)
	}
	_, sessions, err := b.store.SessionContext().List(ctx, whr)
	if err != nil {
		return nil, errno.ErrSessionContextListFailed
	}

	items := make([]*OperatorInboxItem, 0, len(sessions))
	for _, sessionObj := range sessions {
		item, buildErr := b.buildOperatorInboxItem(ctx, sessionObj)
		if buildErr != nil {
			return nil, buildErr
		}
		if reviewStateFilter != "" && item.ReviewState != reviewStateFilter {
			continue
		}
		if hasNeedsReviewFilter && item.NeedsReview != needsReviewFilter {
			continue
		}
		if assigneeFilter != "" && strings.TrimSpace(item.Assignee) != assigneeFilter {
			continue
		}
		items = append(items, item)
	}

	sort.SliceStable(items, func(i, j int) bool {
		ri := operatorInboxPriority(items[i])
		rj := operatorInboxPriority(items[j])
		if ri != rj {
			return ri < rj
		}
		ti := strings.TrimSpace(items[i].LastActivityAt)
		tj := strings.TrimSpace(items[j].LastActivityAt)
		if ti != tj {
			return ti > tj
		}
		return strings.TrimSpace(items[i].SessionID) < strings.TrimSpace(items[j].SessionID)
	})

	totalCount := int64(len(items))
	if offset >= totalCount {
		return &ListOperatorInboxResponse{TotalCount: totalCount, Items: []*OperatorInboxItem{}}, nil
	}
	end := offset + limit
	if end > totalCount {
		end = totalCount
	}
	startIdx := int(offset)
	endIdx := int(end)
	return &ListOperatorInboxResponse{
		TotalCount: totalCount,
		Items:      items[startIdx:endIdx],
	}, nil
}

func (b *aiJobBiz) buildOperatorInboxItem(
	ctx context.Context,
	sessionObj *model.SessionContextM,
) (*OperatorInboxItem, error) {
	if sessionObj == nil {
		return &OperatorInboxItem{}, nil
	}
	sessionID := strings.TrimSpace(sessionObj.SessionID)
	reviewState := extractSessionReviewState(sessionObj.ContextStateJSON)
	assignmentState := extractSessionAssignmentState(sessionObj.ContextStateJSON)
	pinnedEvidenceRefs := extractPinnedEvidenceRefs(sessionObj.PinnedEvidenceJSON)
	listResp, err := b.ListTraceReadModels(ctx, &ListTraceReadModelsRequest{
		SessionID: strPtr(sessionID),
		Offset:    0,
		Limit:     operatorInboxTraceProbeLimit,
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
	reviewFlags := buildWorkbenchReviewFlags(latestDecision, latestRun, pinnedEvidenceRefs)

	var latestCompare *WorkbenchCompareSummary
	if leftJobID, rightJobID := pickLatestCompareJobPair(recentRuns); leftJobID != "" && rightJobID != "" {
		cmp, cmpErr := b.CompareTraceReadModels(ctx, &CompareTraceReadModelsRequest{
			LeftJobID:  leftJobID,
			RightJobID: rightJobID,
		})
		if cmpErr == nil && cmp != nil && cmp.Left != nil && cmp.Right != nil {
			latestCompare = &WorkbenchCompareSummary{
				LeftJobID:         strings.TrimSpace(cmp.Left.JobID),
				RightJobID:        strings.TrimSpace(cmp.Right.JobID),
				SameSession:       cmp.SameSession,
				SameIncident:      cmp.SameIncident,
				ChangedRootCause:  cmp.ChangedRootCause,
				ChangedConfidence: cmp.ChangedConfidence,
			}
		}
	}
	nextHints := buildWorkbenchNextActionHints(
		latestRun,
		latestDecision,
		reviewFlags,
		latestCompare,
		trimOptional(sessionObj.ActiveRunID),
		reviewState,
	)

	item := &OperatorInboxItem{
		SessionID:              sessionID,
		SessionType:            strings.TrimSpace(sessionObj.SessionType),
		BusinessKey:            strings.TrimSpace(sessionObj.BusinessKey),
		IncidentID:             trimOptional(sessionObj.IncidentID),
		Assignee:               strings.TrimSpace(assignmentState.Assignee),
		AssignedBy:             strings.TrimSpace(assignmentState.AssignedBy),
		AssignedAt:             strings.TrimSpace(assignmentState.AssignedAt),
		AssignNote:             strings.TrimSpace(assignmentState.Note),
		ReviewState:            normalizeInboxReviewState(reviewState.State),
		ReviewedBy:             strings.TrimSpace(reviewState.ReviewedBy),
		ReviewedAt:             strings.TrimSpace(reviewState.ReviewedAt),
		HumanReviewRequired:    reviewFlags.HumanReviewRequired,
		MissingFactsCount:      int64(len(reviewFlags.MissingFacts)),
		ConflictsCount:         int64(len(reviewFlags.Conflicts)),
		HasPinnedEvidence:      reviewFlags.HasPinnedEvidence,
		VerificationPending:    containsString(nextHints, workbenchHintVerificationPending),
		NextActionHints:        append([]string(nil), nextHints...),
		WorkbenchPath:          "/v1/sessions/" + sessionID + "/workbench",
		LatestCompareAvailable: latestCompare != nil,
	}
	if latestRun != nil {
		item.LatestJobID = strings.TrimSpace(latestRun.JobID)
		item.LatestTriggerType = strings.TrimSpace(latestRun.TriggerType)
		item.LatestPipeline = strings.TrimSpace(latestRun.Pipeline)
		item.LatestSummary = strings.TrimSpace(latestRun.RootCauseSummary)
		item.LatestConfidence = latestRun.Confidence
		item.LatestUpdatedAt = firstTraceNonEmpty(strings.TrimSpace(latestRun.DecisionUpdatedAt), strings.TrimSpace(latestRun.RunUpdatedAt))
	}
	if item.LatestUpdatedAt == "" {
		item.LatestUpdatedAt = toRFC3339String(sessionObj.UpdatedAt)
	}
	item.LastActivityAt = firstTraceNonEmpty(item.LatestUpdatedAt, toRFC3339String(sessionObj.UpdatedAt))
	item.NeedsReview = computeOperatorInboxNeedsReview(item)
	return item, nil
}

func computeOperatorInboxNeedsReview(item *OperatorInboxItem) bool {
	if item == nil {
		return false
	}
	reviewState := normalizeInboxReviewState(item.ReviewState)
	if reviewState == sessionbiz.SessionReviewStateConfirmed {
		return false
	}
	if reviewState == sessionbiz.SessionReviewStateInReview || reviewState == sessionbiz.SessionReviewStateRejected {
		return true
	}
	if item.HumanReviewRequired || item.MissingFactsCount > 0 || item.ConflictsCount > 0 {
		return true
	}
	return containsString(item.NextActionHints, workbenchHintNeedHumanReview) ||
		containsString(item.NextActionHints, workbenchHintReviewConflicts)
}

func operatorInboxPriority(item *OperatorInboxItem) int {
	if item == nil {
		return 5
	}
	reviewState := normalizeInboxReviewState(item.ReviewState)
	switch reviewState {
	case sessionbiz.SessionReviewStateInReview:
		return 0
	case sessionbiz.SessionReviewStateRejected:
		return 1
	case sessionbiz.SessionReviewStateConfirmed:
		return 4
	default:
		if item.NeedsReview {
			return 2
		}
		return 3
	}
}

func normalizeInboxReviewState(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case sessionbiz.SessionReviewStateInReview:
		return sessionbiz.SessionReviewStateInReview
	case sessionbiz.SessionReviewStateConfirmed:
		return sessionbiz.SessionReviewStateConfirmed
	case sessionbiz.SessionReviewStateRejected:
		return sessionbiz.SessionReviewStateRejected
	default:
		return sessionbiz.SessionReviewStatePending
	}
}

func normalizeInboxSessionType(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case sessionbiz.SessionTypeIncident:
		return sessionbiz.SessionTypeIncident
	case sessionbiz.SessionTypeAlert:
		return sessionbiz.SessionTypeAlert
	case sessionbiz.SessionTypeService:
		return sessionbiz.SessionTypeService
	case sessionbiz.SessionTypeChange:
		return sessionbiz.SessionTypeChange
	default:
		return ""
	}
}

func containsString(items []string, target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	for _, item := range items {
		if strings.TrimSpace(item) == target {
			return true
		}
	}
	return false
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
	assignmentState *sessionAssignmentContextState,
) *SessionWorkbenchReadModel {
	if in == nil {
		return &SessionWorkbenchReadModel{}
	}
	if reviewState == nil {
		reviewState = &sessionReviewContextState{State: sessionbiz.SessionReviewStatePending}
	}
	if assignmentState == nil {
		assignmentState = &sessionAssignmentContextState{}
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
		Assignee:           strings.TrimSpace(assignmentState.Assignee),
		AssignedBy:         strings.TrimSpace(assignmentState.AssignedBy),
		AssignedAt:         strings.TrimSpace(assignmentState.AssignedAt),
		AssignNote:         strings.TrimSpace(assignmentState.Note),
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

func extractSessionAssignmentState(raw *string) *sessionAssignmentContextState {
	out := &sessionAssignmentContextState{}
	obj := parseOptionalJSONObject(raw)
	if obj == nil {
		return out
	}
	assignRaw, ok := obj["assignment"]
	if !ok {
		return out
	}
	assignObj, ok := assignRaw.(map[string]any)
	if !ok {
		return out
	}
	out.Assignee = strings.TrimSpace(anyToString(assignObj["assignee"]))
	out.AssignedBy = strings.TrimSpace(anyToString(assignObj["assigned_by"]))
	out.AssignedAt = strings.TrimSpace(anyToString(assignObj["assigned_at"]))
	out.Note = strings.TrimSpace(anyToString(assignObj["note"]))
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
