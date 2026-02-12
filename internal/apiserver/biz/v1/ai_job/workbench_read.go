package ai_job

import (
	"context"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/onexstack/onexstack/pkg/errorsx"
	"gorm.io/gorm"

	sessionbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/session"
	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/pkg/contextx"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

const (
	defaultSessionWorkbenchRecentLimit   = int64(10)
	maxSessionWorkbenchRecentLimit       = int64(50)
	defaultSessionWorkbenchHistoryLimit  = int64(5)
	defaultOperatorInboxLimit            = int64(20)
	maxOperatorInboxLimit                = int64(100)
	maxOperatorInboxScan                 = int64(500)
	defaultOperatorDashboardPreviewLimit = int64(3)
	maxOperatorDashboardPreviewLimit     = int64(10)
	defaultOperatorDashboardRecentWindow = 24 * time.Hour
	defaultOperatorDashboardTrendWindow  = 7 * 24 * time.Hour
	operatorDashboardTrendWindow7D       = 7 * 24 * time.Hour
	operatorDashboardTrendWindow30D      = 30 * 24 * time.Hour
	defaultOperatorDashboardTrendScan    = int64(2000)
	maxOperatorDashboardTrendScan        = int64(10000)
	defaultOperatorTeamDashboardLimit    = int64(20)
	maxOperatorTeamDashboardLimit        = int64(100)
	defaultOperatorTeamDashboardTopN     = int64(5)
	maxOperatorTeamDashboardTopN         = int64(20)
	defaultWorkbenchViewerLimit          = int64(20)
	maxWorkbenchViewerLimit              = int64(100)
	defaultWorkbenchViewerTabsLimit      = int64(5)
	operatorInboxTraceProbeLimit         = int64(5)
	defaultSessionSLAWindow              = 2 * time.Hour

	workbenchHintNeedHumanReview     = "need_human_review"
	workbenchHintAwaitMoreEvidence   = "await_more_evidence"
	workbenchHintReviewConflicts     = "review_conflicts"
	workbenchHintVerificationPending = "verification_pending"
	workbenchHintRunInProgress       = "run_in_progress"
	workbenchHintReviewCompare       = "review_compare"
	workbenchHintReviewInProgress    = "review_in_progress"
	workbenchHintConsiderFollowUp    = "consider_follow_up"
	workbenchHintConsiderReplay      = "consider_replay"

	operatorDashboardTrendGroupByOperator    = "operator"
	operatorDashboardTrendGroupByTeam        = "team"
	operatorDashboardTrendGroupBySessionType = "session_type"

	workbenchViewerTabEvidence     = "evidence"
	workbenchViewerTabVerification = "verification"
	workbenchViewerTabCompare      = "compare"
	workbenchViewerTabHistory      = "history"

	workbenchViewerHistoryScopeSession      = "session"
	workbenchViewerHistoryScopeJob          = "job"
	workbenchViewerHistoryScopeCrossSession = "cross_session"
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
	RecentHistory    []*SessionHistorySummary    `json:"recent_history"`
	HistoryPath      string                      `json:"history_path,omitempty"`
	DrillDown        *WorkbenchDrillDown         `json:"drilldown,omitempty"`
}

type ListOperatorInboxRequest struct {
	ReviewState     *string
	NeedsReview     *bool
	SessionType     *string
	Assignee        *string
	TeamID          *string
	EscalationState *string
	Offset          int64
	Limit           int64
}

type ListOperatorInboxResponse struct {
	TotalCount int64                `json:"total_count"`
	Items      []*OperatorInboxItem `json:"items"`
}

type GetOperatorDashboardRequest struct {
	PreviewLimit int64
	RecentWindow time.Duration
}

type GetOperatorTeamDashboardRequest struct {
	TeamID string
	Offset int64
	Limit  int64
	TopN   int64
	Order  *string
}

type GetOperatorDashboardTrendsRequest struct {
	Window      *string
	GroupBy     *string
	Operator    *string
	TeamID      *string
	SessionType *string
	ScanLimit   int64
}

type GetSessionWorkbenchViewerRequest struct {
	SessionID     string
	View          *string
	JobID         *string
	LeftJobID     *string
	RightJobID    *string
	HistoryScope  *string
	HistoryOffset int64
	HistoryLimit  int64
	HistoryOrder  *string
}

type GetOperatorDashboardResponse struct {
	AsOf         string                         `json:"as_of"`
	Overview     *OperatorDashboardOverview     `json:"overview"`
	Escalation   *OperatorDashboardEscalation   `json:"escalation"`
	Distribution *OperatorDashboardDistribution `json:"distribution"`
	Activity     *OperatorDashboardActivity     `json:"activity"`
	QueuePreview *OperatorDashboardQueuePreview `json:"queue_preview"`
	Navigation   *OperatorDashboardNavigation   `json:"navigation"`
}

type GetOperatorDashboardTrendsResponse struct {
	AsOf         string                            `json:"as_of"`
	Window       string                            `json:"window"`
	WindowStart  string                            `json:"window_start"`
	WindowEnd    string                            `json:"window_end"`
	GroupBy      string                            `json:"group_by"`
	Applied      *OperatorDashboardTrendFilters    `json:"applied,omitempty"`
	Summary      *OperatorDashboardTrendCounter    `json:"summary"`
	ByDay        []*OperatorDashboardTrendDay      `json:"by_day"`
	Grouped      []*OperatorDashboardTrendGroup    `json:"grouped"`
	Navigation   *OperatorDashboardTrendNavigation `json:"navigation"`
	Truncated    bool                              `json:"truncated"`
	ScannedCount int64                             `json:"scanned_count"`
}

type OperatorDashboardTrendFilters struct {
	Operator    string `json:"operator,omitempty"`
	TeamID      string `json:"team_id,omitempty"`
	SessionType string `json:"session_type,omitempty"`
}

type OperatorDashboardTrendCounter struct {
	ReplayCount           int64 `json:"replay_count"`
	FollowUpCount         int64 `json:"follow_up_count"`
	ReviewStartedCount    int64 `json:"review_started_count"`
	ReviewConfirmedCount  int64 `json:"review_confirmed_count"`
	ReviewRejectedCount   int64 `json:"review_rejected_count"`
	SLAPendingCount       int64 `json:"sla_pending_count"`
	SLAEscalatedCount     int64 `json:"sla_escalated_count"`
	SLAClearedCount       int64 `json:"sla_cleared_count"`
	ReviewActionCount     int64 `json:"review_action_count"`
	EscalationActionCount int64 `json:"escalation_action_count"`
	TotalCount            int64 `json:"total_count"`
}

type OperatorDashboardTrendDay struct {
	Date    string                         `json:"date"`
	Counter *OperatorDashboardTrendCounter `json:"counter"`
}

type OperatorDashboardTrendGroup struct {
	GroupKey string                         `json:"group_key"`
	Counter  *OperatorDashboardTrendCounter `json:"counter"`
}

type OperatorDashboardTrendNavigation struct {
	DashboardPath string            `json:"dashboard_path"`
	InboxPath     string            `json:"inbox_path"`
	Filters       map[string]string `json:"filters"`
}

type GetOperatorTeamDashboardResponse struct {
	AsOf         string                             `json:"as_of"`
	TeamID       string                             `json:"team_id,omitempty"`
	Overview     *OperatorTeamDashboardOverview     `json:"overview"`
	Distribution *OperatorTeamDashboardDistribution `json:"distribution"`
	TopHighRisk  []*OperatorTeamDashboardSession    `json:"top_high_risk"`
	TotalCount   int64                              `json:"total_count"`
	Offset       int64                              `json:"offset"`
	Limit        int64                              `json:"limit"`
	Items        []*OperatorTeamDashboardSession    `json:"items"`
	SortOrder    string                             `json:"sort_order"`
	Navigation   *OperatorTeamDashboardNavigation   `json:"navigation"`
}

type GetSessionWorkbenchViewerResponse struct {
	SessionID    string                       `json:"session_id"`
	IncidentID   string                       `json:"incident_id,omitempty"`
	Tabs         []string                     `json:"tabs"`
	Selected     string                       `json:"selected"`
	Evidence     *WorkbenchEvidenceViewer     `json:"evidence,omitempty"`
	Verification *WorkbenchVerificationViewer `json:"verification,omitempty"`
	Compare      *WorkbenchCompareViewer      `json:"compare,omitempty"`
	History      *WorkbenchHistoryViewer      `json:"history,omitempty"`
}

type WorkbenchEvidenceViewer struct {
	IncidentID    string                   `json:"incident_id,omitempty"`
	EvidencePath  string                   `json:"evidence_path,omitempty"`
	EvidenceRefs  []string                 `json:"evidence_refs"`
	Items         []*WorkbenchEvidenceItem `json:"items"`
	RelatedJobIDs []string                 `json:"related_job_ids"`
}

type WorkbenchEvidenceItem struct {
	EvidenceID  string `json:"evidence_id"`
	JobID       string `json:"job_id,omitempty"`
	Type        string `json:"type,omitempty"`
	Summary     string `json:"summary,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
	ResultBytes int64  `json:"result_size_bytes"`
	IsTruncated bool   `json:"is_truncated"`
}

type WorkbenchVerificationViewer struct {
	IncidentID       string                       `json:"incident_id,omitempty"`
	VerificationPath string                       `json:"verification_path,omitempty"`
	VerificationRefs []string                     `json:"verification_refs"`
	Items            []*WorkbenchVerificationItem `json:"items"`
	Pending          bool                         `json:"pending"`
}

type WorkbenchVerificationItem struct {
	RunID            string `json:"run_id"`
	JobID            string `json:"job_id,omitempty"`
	Actor            string `json:"actor,omitempty"`
	Source           string `json:"source,omitempty"`
	Tool             string `json:"tool,omitempty"`
	Observed         string `json:"observed,omitempty"`
	MeetsExpectation bool   `json:"meets_expectation"`
	CreatedAt        string `json:"created_at,omitempty"`
}

type WorkbenchCompareViewer struct {
	Latest       *WorkbenchCompareDrillDown   `json:"latest"`
	Paths        []*WorkbenchCompareViewerTab `json:"paths"`
	SelectedPair *WorkbenchCompareDrillDown   `json:"selected_pair,omitempty"`
}

type WorkbenchCompareViewerTab struct {
	Label       string `json:"label"`
	ComparePath string `json:"compare_path"`
	LeftJobID   string `json:"left_job_id"`
	RightJobID  string `json:"right_job_id"`
}

type WorkbenchHistoryViewer struct {
	Scope              string                   `json:"scope"`
	SessionHistoryPath string                   `json:"session_history_path,omitempty"`
	CrossSessionPath   string                   `json:"cross_session_path,omitempty"`
	Offset             int64                    `json:"offset"`
	Limit              int64                    `json:"limit"`
	Order              string                   `json:"order"`
	TotalCount         int64                    `json:"total_count"`
	Events             []*SessionHistorySummary `json:"events"`
}

type OperatorTeamDashboardOverview struct {
	TotalSessions          int64 `json:"total_sessions"`
	MyQueueCount           int64 `json:"my_queue_count"`
	NeedsReviewCount       int64 `json:"needs_review_count"`
	AssignedCount          int64 `json:"assigned_count"`
	UnassignedCount        int64 `json:"unassigned_count"`
	PendingEscalationCount int64 `json:"pending_escalation_count"`
	EscalatedCount         int64 `json:"escalated_count"`
	LongUnhandledCount     int64 `json:"long_unhandled_count"`
	HighRiskCount          int64 `json:"high_risk_count"`
}

type OperatorTeamDashboardDistribution struct {
	ByAssignee        map[string]int64 `json:"by_assignee"`
	ByReviewState     map[string]int64 `json:"by_review_state"`
	ByEscalationState map[string]int64 `json:"by_escalation_state"`
	BySessionType     map[string]int64 `json:"by_session_type"`
}

type OperatorTeamDashboardSession struct {
	SessionID       string `json:"session_id"`
	IncidentID      string `json:"incident_id,omitempty"`
	SessionType     string `json:"session_type"`
	Assignee        string `json:"assignee,omitempty"`
	ReviewState     string `json:"review_state"`
	EscalationState string `json:"escalation_state"`
	NeedsReview     bool   `json:"needs_review"`
	IsMyQueue       bool   `json:"is_my_queue"`
	LongUnhandled   bool   `json:"long_unhandled"`
	HighRisk        bool   `json:"high_risk"`
	LastActivityAt  string `json:"last_activity_at,omitempty"`
	WorkbenchPath   string `json:"workbench_path"`
}

type OperatorTeamDashboardNavigation struct {
	InboxPath     string `json:"inbox_path"`
	TeamInboxPath string `json:"team_inbox_path,omitempty"`
}

type OperatorDashboardOverview struct {
	TotalSessions    int64 `json:"total_sessions"`
	NeedsReviewCount int64 `json:"needs_review_count"`
	InReviewCount    int64 `json:"in_review_count"`
	ConfirmedCount   int64 `json:"confirmed_count"`
	RejectedCount    int64 `json:"rejected_count"`
	AssignedCount    int64 `json:"assigned_count"`
	UnassignedCount  int64 `json:"unassigned_count"`
	MyQueueCount     int64 `json:"my_queue_count"`
	LongUnhandled    int64 `json:"long_unhandled_count"`
	HighRiskCount    int64 `json:"high_risk_count"`
}

type OperatorDashboardEscalation struct {
	PendingEscalationCount int64 `json:"pending_escalation_count"`
	EscalatedCount         int64 `json:"escalated_count"`
	NormalCount            int64 `json:"normal_count"`
}

type OperatorDashboardDistribution struct {
	BySessionType       map[string]int64 `json:"by_session_type"`
	ByLatestTriggerType map[string]int64 `json:"by_latest_trigger_type"`
	ByReviewState       map[string]int64 `json:"by_review_state"`
}

type OperatorDashboardActivity struct {
	ActiveRunCount       int64 `json:"active_run_count"`
	RecentlyUpdatedCount int64 `json:"recently_updated_count"`
	RecentReplayCount    int64 `json:"recent_replay_count"`
	RecentFollowUpCount  int64 `json:"recent_follow_up_count"`
	RecentWindowMinutes  int64 `json:"recent_window_minutes"`
}

type OperatorDashboardQueuePreview struct {
	InReview    []*OperatorDashboardPreviewItem `json:"in_review"`
	Escalated   []*OperatorDashboardPreviewItem `json:"escalated"`
	NeedsReview []*OperatorDashboardPreviewItem `json:"needs_review"`
}

type OperatorDashboardPreviewItem struct {
	SessionID         string `json:"session_id"`
	IncidentID        string `json:"incident_id,omitempty"`
	SessionType       string `json:"session_type"`
	ReviewState       string `json:"review_state"`
	EscalationState   string `json:"escalation_state"`
	Assignee          string `json:"assignee,omitempty"`
	LatestTriggerType string `json:"latest_trigger_type,omitempty"`
	LastActivityAt    string `json:"last_activity_at,omitempty"`
	WorkbenchPath     string `json:"workbench_path"`
}

type OperatorDashboardNavigation struct {
	InboxPath          string            `json:"inbox_path"`
	RecommendedFilters map[string]string `json:"recommended_filters"`
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
	SlaDueAt               string   `json:"sla_due_at,omitempty"`
	EscalationState        string   `json:"escalation_state"`
	EscalationLevel        int64    `json:"escalation_level"`
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
	IsMyQueue              bool     `json:"is_my_queue"`
	LongUnhandled          bool     `json:"long_unhandled"`
	HighRisk               bool     `json:"high_risk"`
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
	SlaDueAt           string         `json:"sla_due_at,omitempty"`
	EscalationState    string         `json:"escalation_state"`
	EscalationLevel    int64          `json:"escalation_level"`
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

type WorkbenchDrillDown struct {
	LatestTracePath        string                          `json:"latest_trace_path,omitempty"`
	LatestComparePath      string                          `json:"latest_compare_path,omitempty"`
	HistoryPath            string                          `json:"history_path,omitempty"`
	AssignmentHistoryPath  string                          `json:"assignment_history_path,omitempty"`
	ViewerPath             string                          `json:"viewer_path,omitempty"`
	EvidenceViewerPath     string                          `json:"evidence_viewer_path,omitempty"`
	VerificationViewerPath string                          `json:"verification_viewer_path,omitempty"`
	CompareViewerPath      string                          `json:"compare_viewer_path,omitempty"`
	HistoryViewerPath      string                          `json:"history_viewer_path,omitempty"`
	RecommendedNextView    []string                        `json:"recommended_next_view"`
	LatestDecision         *WorkbenchDecisionDrillDown     `json:"latest_decision,omitempty"`
	LatestCompare          *WorkbenchCompareDrillDown      `json:"latest_compare,omitempty"`
	LatestAssignment       *WorkbenchAssignmentDrillDown   `json:"latest_assignment,omitempty"`
	PinnedEvidence         *WorkbenchEvidenceDrillDown     `json:"pinned_evidence,omitempty"`
	Verification           *WorkbenchVerificationDrillDown `json:"verification,omitempty"`
	History                *WorkbenchHistoryDrillDown      `json:"history,omitempty"`
}

type WorkbenchDecisionDrillDown struct {
	JobID                   string   `json:"job_id,omitempty"`
	TracePath               string   `json:"trace_path,omitempty"`
	DecisionDetailAvailable bool     `json:"decision_detail_available"`
	RelatedEvidenceRefs     []string `json:"related_evidence_refs"`
	RelatedVerificationRefs []string `json:"related_verification_refs"`
}

type WorkbenchCompareDrillDown struct {
	CompareAvailable bool   `json:"compare_available"`
	ComparePath      string `json:"compare_path,omitempty"`
	LeftJobID        string `json:"left_job_id,omitempty"`
	RightJobID       string `json:"right_job_id,omitempty"`
}

type WorkbenchAssignmentDrillDown struct {
	Assignee   string `json:"assignee,omitempty"`
	AssignedBy string `json:"assigned_by,omitempty"`
	AssignedAt string `json:"assigned_at,omitempty"`
	Note       string `json:"note,omitempty"`
}

type WorkbenchEvidenceDrillDown struct {
	IncidentEvidencePath string   `json:"incident_evidence_path,omitempty"`
	EvidenceRefs         []string `json:"evidence_refs"`
}

type WorkbenchVerificationDrillDown struct {
	IncidentVerificationPath string   `json:"incident_verification_path,omitempty"`
	VerificationRefs         []string `json:"verification_refs"`
	VerificationPending      bool     `json:"verification_pending"`
}

type WorkbenchHistoryDrillDown struct {
	HistoryPath    string `json:"history_path,omitempty"`
	RecentPath     string `json:"recent_path,omitempty"`
	NextPagePath   string `json:"next_page_path,omitempty"`
	RecentLimit    int64  `json:"recent_limit"`
	RecentReturned int64  `json:"recent_returned"`
	Order          string `json:"order"`
}

type SessionHistorySummary struct {
	EventID        string         `json:"event_id"`
	EventType      string         `json:"event_type"`
	SessionID      string         `json:"session_id"`
	IncidentID     string         `json:"incident_id,omitempty"`
	JobID          string         `json:"job_id,omitempty"`
	Actor          string         `json:"actor"`
	Note           string         `json:"note,omitempty"`
	ReasonCode     string         `json:"reason_code,omitempty"`
	PayloadSummary map[string]any `json:"payload_summary,omitempty"`
	CreatedAt      string         `json:"created_at"`
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

type sessionSLAContextState struct {
	AssignedAt      string
	DueAt           string
	EscalationState string
	EscalationLevel int64
	ReasonCode      string
}

type operatorInboxFilters struct {
	ReviewState     string
	HasNeedsReview  bool
	NeedsReview     bool
	SessionType     string
	Assignee        string
	EscalationState string
	TeamID          string
}

type operatorDashboardTrendSessionMeta struct {
	SessionType string
	TeamKey     string
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
	assignmentState := extractSessionAssignmentState(sessionObj.ContextStateJSON)
	if !b.canAccessOperatorSession(ctx, sessionObj, assignmentState) {
		return nil, errno.ErrPermissionDenied
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
	baseSLAState := extractSessionSLAState(sessionObj.ContextStateJSON)
	slaState := evaluateSessionSLAContext(
		time.Now().UTC(),
		reviewState,
		assignmentState,
		latestRun,
		baseSLAState,
	)
	b.syncSessionSLAStateBestEffort(ctx, sessionObj, baseSLAState, slaState)
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
	recentHistory := b.loadSessionHistorySummariesBestEffort(ctx, sessionID, defaultSessionWorkbenchHistoryLimit)
	historyPath := buildSessionHistoryPath(sessionID)
	drillDown := buildWorkbenchDrillDown(
		sessionID,
		incidentID,
		latestRun,
		latestDecision,
		latestCompare,
		assignmentState,
		reviewFlags,
		pinnedEvidenceRefs,
		recentHistory,
		nextHints,
	)

	return &GetSessionWorkbenchResponse{
		Session:          sessionToWorkbench(sessionObj, pinnedEvidenceRefs, reviewState, assignmentState, slaState),
		Incident:         incidentBlock,
		LatestRun:        latestRun,
		LatestDecision:   latestDecision,
		RecentRuns:       recentRuns,
		RecentTotalCount: listResp.TotalCount,
		LatestCompare:    latestCompare,
		ReviewFlags:      reviewFlags,
		NextActionHints:  nextHints,
		RecentHistory:    recentHistory,
		HistoryPath:      historyPath,
		DrillDown:        drillDown,
	}, nil
}

func (b *aiJobBiz) GetSessionWorkbenchViewer(
	ctx context.Context,
	rq *GetSessionWorkbenchViewerRequest,
) (*GetSessionWorkbenchViewerResponse, error) {
	if rq == nil {
		return nil, errorsx.ErrInvalidArgument
	}
	sessionID := strings.TrimSpace(rq.SessionID)
	if sessionID == "" {
		return nil, errorsx.ErrInvalidArgument
	}
	selectedView, err := normalizeWorkbenchViewerTab(rq.View)
	if err != nil {
		return nil, err
	}
	historyScope, err := normalizeWorkbenchViewerHistoryScope(rq.HistoryScope)
	if err != nil {
		return nil, err
	}
	historyOffset := rq.HistoryOffset
	if historyOffset < 0 {
		return nil, errorsx.ErrInvalidArgument
	}
	historyLimit := rq.HistoryLimit
	if historyLimit <= 0 {
		historyLimit = defaultWorkbenchViewerLimit
	}
	if historyLimit > maxWorkbenchViewerLimit {
		return nil, errorsx.ErrInvalidArgument
	}
	historyAscending, err := normalizeOperatorOrder(rq.HistoryOrder)
	if err != nil {
		return nil, err
	}

	sessionObj, err := b.store.SessionContext().Get(ctx, where.T(ctx).F("session_id", sessionID))
	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrSessionContextNotFound
		}
		return nil, errno.ErrSessionContextGetFailed
	}
	assignmentState := extractSessionAssignmentState(sessionObj.ContextStateJSON)
	if !b.canAccessOperatorSession(ctx, sessionObj, assignmentState) {
		return nil, errno.ErrPermissionDenied
	}

	traceResp, err := b.ListTraceReadModels(ctx, &ListTraceReadModelsRequest{
		SessionID: strPtr(sessionID),
		Offset:    0,
		Limit:     maxSessionWorkbenchRecentLimit,
	})
	if err != nil {
		return nil, err
	}
	recentRuns := traceResp.Summaries
	latestRun := firstTraceReadSummary(recentRuns)
	var latestDecision *DecisionTraceReadModel
	if latestRun != nil && strings.TrimSpace(latestRun.JobID) != "" {
		latestTraceResp, traceErr := b.GetTraceReadModel(ctx, &GetTraceReadModelRequest{JobID: latestRun.JobID})
		if traceErr == nil && latestTraceResp != nil {
			latestDecision = latestTraceResp.DecisionTrace
		}
	}
	incidentID := firstTraceNonEmpty(trimOptional(sessionObj.IncidentID), traceSummaryIncidentID(latestRun))
	pinnedEvidenceRefs := extractPinnedEvidenceRefs(sessionObj.PinnedEvidenceJSON)

	evidenceViewer, err := b.buildWorkbenchEvidenceViewer(ctx, incidentID, latestDecision, pinnedEvidenceRefs, recentRuns)
	if err != nil {
		return nil, err
	}
	verificationViewer, err := b.buildWorkbenchVerificationViewer(ctx, incidentID, latestDecision, latestRun)
	if err != nil {
		return nil, err
	}
	compareViewer := b.buildWorkbenchCompareViewer(recentRuns, rq.LeftJobID, rq.RightJobID)
	historyViewer, err := b.buildWorkbenchHistoryViewer(
		ctx,
		sessionID,
		historyScope,
		rq.JobID,
		latestRun,
		historyOffset,
		historyLimit,
		historyAscending,
	)
	if err != nil {
		return nil, err
	}

	return &GetSessionWorkbenchViewerResponse{
		SessionID:  sessionID,
		IncidentID: incidentID,
		Tabs: []string{
			workbenchViewerTabEvidence,
			workbenchViewerTabVerification,
			workbenchViewerTabCompare,
			workbenchViewerTabHistory,
		},
		Selected:     selectedView,
		Evidence:     evidenceViewer,
		Verification: verificationViewer,
		Compare:      compareViewer,
		History:      historyViewer,
	}, nil
}

func normalizeWorkbenchViewerTab(raw *string) (string, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return workbenchViewerTabEvidence, nil
	}
	switch strings.ToLower(strings.TrimSpace(*raw)) {
	case workbenchViewerTabEvidence:
		return workbenchViewerTabEvidence, nil
	case workbenchViewerTabVerification:
		return workbenchViewerTabVerification, nil
	case workbenchViewerTabCompare:
		return workbenchViewerTabCompare, nil
	case workbenchViewerTabHistory:
		return workbenchViewerTabHistory, nil
	default:
		return "", errorsx.ErrInvalidArgument
	}
}

func normalizeWorkbenchViewerHistoryScope(raw *string) (string, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return workbenchViewerHistoryScopeSession, nil
	}
	switch strings.ToLower(strings.TrimSpace(*raw)) {
	case workbenchViewerHistoryScopeSession:
		return workbenchViewerHistoryScopeSession, nil
	case workbenchViewerHistoryScopeJob:
		return workbenchViewerHistoryScopeJob, nil
	case workbenchViewerHistoryScopeCrossSession:
		return workbenchViewerHistoryScopeCrossSession, nil
	default:
		return "", errorsx.ErrInvalidArgument
	}
}

func (b *aiJobBiz) buildWorkbenchEvidenceViewer(
	ctx context.Context,
	incidentID string,
	latestDecision *DecisionTraceReadModel,
	pinnedEvidenceRefs []string,
	recentRuns []*TraceReadSummary,
) (*WorkbenchEvidenceViewer, error) {
	viewer := &WorkbenchEvidenceViewer{
		IncidentID:   strings.TrimSpace(incidentID),
		EvidencePath: buildIncidentEvidencePath(incidentID),
		EvidenceRefs: []string{},
		Items:        []*WorkbenchEvidenceItem{},
	}
	evidenceRefs := normalizeStringSlice(pinnedEvidenceRefs)
	if latestDecision != nil {
		evidenceRefs = mergeStringSlices(latestDecision.EvidenceRefs, evidenceRefs)
	}
	viewer.EvidenceRefs = evidenceRefs

	if strings.TrimSpace(incidentID) == "" {
		viewer.RelatedJobIDs = uniqueTraceJobIDs(recentRuns, nil)
		return viewer, nil
	}

	itemLimit := int(defaultWorkbenchViewerLimit)
	_, evidenceList, err := b.store.Evidence().List(
		ctx,
		where.T(ctx).F("incident_id", strings.TrimSpace(incidentID)).O(0).L(itemLimit),
	)
	if err != nil {
		return nil, errno.ErrEvidenceListFailed
	}
	orderedEvidence := reorderEvidenceByRefs(evidenceList, evidenceRefs)
	viewer.Items = evidenceListToWorkbenchItems(orderedEvidence)
	viewer.RelatedJobIDs = uniqueTraceJobIDs(recentRuns, orderedEvidence)
	return viewer, nil
}

func reorderEvidenceByRefs(list []*model.EvidenceM, refs []string) []*model.EvidenceM {
	if len(list) == 0 {
		return []*model.EvidenceM{}
	}
	if len(refs) == 0 {
		return list
	}
	evidenceByID := map[string]*model.EvidenceM{}
	for _, evidence := range list {
		if evidence == nil {
			continue
		}
		evidenceByID[strings.TrimSpace(evidence.EvidenceID)] = evidence
	}
	out := make([]*model.EvidenceM, 0, len(list))
	seen := map[string]struct{}{}
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		evidence, ok := evidenceByID[ref]
		if !ok || evidence == nil {
			continue
		}
		out = append(out, evidence)
		seen[ref] = struct{}{}
	}
	for _, evidence := range list {
		if evidence == nil {
			continue
		}
		evidenceID := strings.TrimSpace(evidence.EvidenceID)
		if _, ok := seen[evidenceID]; ok {
			continue
		}
		out = append(out, evidence)
	}
	return out
}

func evidenceListToWorkbenchItems(list []*model.EvidenceM) []*WorkbenchEvidenceItem {
	items := make([]*WorkbenchEvidenceItem, 0, len(list))
	for _, evidence := range list {
		if evidence == nil {
			continue
		}
		summary := strings.TrimSpace(trimOptional(evidence.Summary))
		if summary == "" {
			summary = summarizeText(evidence.ResultJSON, 160)
		}
		items = append(items, &WorkbenchEvidenceItem{
			EvidenceID:  strings.TrimSpace(evidence.EvidenceID),
			JobID:       strings.TrimSpace(trimOptional(evidence.JobID)),
			Type:        strings.TrimSpace(evidence.Type),
			Summary:     summary,
			CreatedAt:   toRFC3339String(evidence.CreatedAt),
			ResultBytes: evidence.ResultSizeBytes,
			IsTruncated: evidence.IsTruncated,
		})
	}
	return items
}

func summarizeText(raw string, limit int) string {
	if limit <= 0 {
		return ""
	}
	text := strings.TrimSpace(raw)
	if text == "" {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}

func uniqueTraceJobIDs(runs []*TraceReadSummary, evidence []*model.EvidenceM) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, run := range runs {
		if run == nil {
			continue
		}
		jobID := strings.TrimSpace(run.JobID)
		if jobID == "" {
			continue
		}
		if _, ok := seen[jobID]; ok {
			continue
		}
		seen[jobID] = struct{}{}
		out = append(out, jobID)
	}
	for _, item := range evidence {
		if item == nil {
			continue
		}
		jobID := strings.TrimSpace(trimOptional(item.JobID))
		if jobID == "" {
			continue
		}
		if _, ok := seen[jobID]; ok {
			continue
		}
		seen[jobID] = struct{}{}
		out = append(out, jobID)
	}
	return out
}

func (b *aiJobBiz) buildWorkbenchVerificationViewer(
	ctx context.Context,
	incidentID string,
	latestDecision *DecisionTraceReadModel,
	latestRun *TraceReadSummary,
) (*WorkbenchVerificationViewer, error) {
	viewer := &WorkbenchVerificationViewer{
		IncidentID:       strings.TrimSpace(incidentID),
		VerificationPath: buildIncidentVerificationPath(incidentID),
		VerificationRefs: []string{},
		Items:            []*WorkbenchVerificationItem{},
		Pending:          false,
	}
	if latestDecision != nil {
		viewer.VerificationRefs = normalizeStringSlice(latestDecision.VerificationRefs)
	}
	if strings.TrimSpace(incidentID) == "" {
		return viewer, nil
	}
	_, runs, err := b.store.IncidentVerificationRun().List(
		ctx,
		where.T(ctx).F("incident_id", strings.TrimSpace(incidentID)).O(0).L(int(defaultWorkbenchViewerLimit)),
	)
	if err != nil {
		return nil, errno.ErrIncidentVerificationRunListFailed
	}
	items := make([]*WorkbenchVerificationItem, 0, len(runs))
	for _, run := range runs {
		if run == nil {
			continue
		}
		items = append(items, &WorkbenchVerificationItem{
			RunID:            strings.TrimSpace(run.RunID),
			JobID:            strings.TrimSpace(trimOptional(run.JobID)),
			Actor:            strings.TrimSpace(run.Actor),
			Source:           strings.TrimSpace(run.Source),
			Tool:             strings.TrimSpace(run.Tool),
			Observed:         strings.TrimSpace(run.Observed),
			MeetsExpectation: run.MeetsExpectation,
			CreatedAt:        toRFC3339String(run.CreatedAt),
		})
	}
	viewer.Items = items
	if latestRun != nil && latestRun.VerificationCount > 0 && len(viewer.VerificationRefs) == 0 {
		viewer.Pending = true
	}
	return viewer, nil
}

func (b *aiJobBiz) buildWorkbenchCompareViewer(
	recentRuns []*TraceReadSummary,
	leftJobID *string,
	rightJobID *string,
) *WorkbenchCompareViewer {
	latestLeft, latestRight := pickLatestCompareJobPair(recentRuns)
	latest := &WorkbenchCompareDrillDown{
		CompareAvailable: false,
	}
	if latestLeft != "" && latestRight != "" {
		latest.CompareAvailable = true
		latest.LeftJobID = latestLeft
		latest.RightJobID = latestRight
		latest.ComparePath = buildTraceComparePath(latestLeft, latestRight)
	}

	selectedLeft := strings.TrimSpace(trimOptional(leftJobID))
	selectedRight := strings.TrimSpace(trimOptional(rightJobID))
	selected := &WorkbenchCompareDrillDown{
		CompareAvailable: false,
	}
	if selectedLeft == "" || selectedRight == "" {
		selected = latest
	} else {
		selectedPath := buildTraceComparePath(selectedLeft, selectedRight)
		if selectedPath != "" {
			selected = &WorkbenchCompareDrillDown{
				CompareAvailable: true,
				ComparePath:      selectedPath,
				LeftJobID:        selectedLeft,
				RightJobID:       selectedRight,
			}
		}
	}

	paths := []*WorkbenchCompareViewerTab{}
	for _, pair := range collectWorkbenchComparePairs(recentRuns, int(defaultWorkbenchViewerTabsLimit)) {
		paths = append(paths, &WorkbenchCompareViewerTab{
			Label:       pair.label,
			ComparePath: buildTraceComparePath(pair.leftJobID, pair.rightJobID),
			LeftJobID:   pair.leftJobID,
			RightJobID:  pair.rightJobID,
		})
	}

	return &WorkbenchCompareViewer{
		Latest:       latest,
		Paths:        paths,
		SelectedPair: selected,
	}
}

type workbenchComparePair struct {
	label      string
	leftJobID  string
	rightJobID string
}

func collectWorkbenchComparePairs(recentRuns []*TraceReadSummary, limit int) []workbenchComparePair {
	if limit <= 0 {
		return nil
	}
	out := make([]workbenchComparePair, 0, limit)
	seen := map[string]struct{}{}
	for idx, right := range recentRuns {
		if len(out) >= limit {
			break
		}
		if right == nil {
			continue
		}
		rightTrigger := strings.TrimSpace(right.TriggerType)
		if rightTrigger != "replay" && rightTrigger != "follow_up" {
			continue
		}
		rightJobID := strings.TrimSpace(right.JobID)
		if rightJobID == "" {
			continue
		}
		for j := idx + 1; j < len(recentRuns); j++ {
			left := recentRuns[j]
			if left == nil {
				continue
			}
			leftJobID := strings.TrimSpace(left.JobID)
			if leftJobID == "" || leftJobID == rightJobID {
				continue
			}
			key := leftJobID + ":" + rightJobID
			if _, ok := seen[key]; ok {
				break
			}
			seen[key] = struct{}{}
			out = append(out, workbenchComparePair{
				label:      rightTrigger + ":" + rightJobID,
				leftJobID:  leftJobID,
				rightJobID: rightJobID,
			})
			break
		}
	}
	return out
}

func (b *aiJobBiz) buildWorkbenchHistoryViewer(
	ctx context.Context,
	sessionID string,
	scope string,
	jobID *string,
	latestRun *TraceReadSummary,
	offset int64,
	limit int64,
	ascending bool,
) (*WorkbenchHistoryViewer, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errorsx.ErrInvalidArgument
	}
	order := "desc"
	if ascending {
		order = "asc"
	}
	viewer := &WorkbenchHistoryViewer{
		Scope:              scope,
		SessionHistoryPath: buildSessionHistoryPath(sessionID),
		CrossSessionPath: buildSessionWorkbenchViewerPath(sessionID, map[string]string{
			"view":          workbenchViewerTabHistory,
			"history_scope": workbenchViewerHistoryScopeCrossSession,
			"order":         order,
			"offset":        strconv.FormatInt(offset, 10),
			"limit":         strconv.FormatInt(limit, 10),
		}),
		Offset: offset,
		Limit:  limit,
		Order:  order,
		Events: []*SessionHistorySummary{},
	}
	switch scope {
	case workbenchViewerHistoryScopeJob:
		historyJobID := strings.TrimSpace(trimOptional(jobID))
		if historyJobID == "" && latestRun != nil {
			historyJobID = strings.TrimSpace(latestRun.JobID)
		}
		total, events, err := b.loadWorkbenchHistoryByJob(ctx, sessionID, historyJobID, offset, limit, ascending)
		if err != nil {
			return nil, err
		}
		viewer.TotalCount = total
		viewer.Events = events
	case workbenchViewerHistoryScopeCrossSession:
		total, events, err := b.loadWorkbenchCrossSessionHistory(ctx, sessionID, offset, limit, ascending)
		if err != nil {
			return nil, err
		}
		viewer.TotalCount = total
		viewer.Events = events
	default:
		resp, err := b.sessionBiz.ListHistory(ctx, &sessionbiz.ListSessionHistoryRequest{
			SessionID: sessionID,
			Offset:    offset,
			Limit:     limit,
			Order:     strPtr(order),
		})
		if err != nil {
			return nil, err
		}
		viewer.TotalCount = resp.TotalCount
		viewer.Events = sessionHistoryReadModelsToSummaries(resp.Events)
	}
	return viewer, nil
}

func (b *aiJobBiz) loadWorkbenchHistoryByJob(
	ctx context.Context,
	sessionID string,
	jobID string,
	offset int64,
	limit int64,
	ascending bool,
) (int64, []*SessionHistorySummary, error) {
	if strings.TrimSpace(jobID) == "" {
		return 0, []*SessionHistorySummary{}, nil
	}
	total, list, err := b.store.SessionHistoryEvent().ListBySession(
		ctx,
		sessionID,
		0,
		int(maxOperatorDashboardTrendScan),
		ascending,
	)
	if err != nil {
		return 0, nil, errno.ErrSessionHistoryListFailed
	}
	filtered := make([]*model.SessionHistoryEventM, 0, len(list))
	for _, item := range list {
		if item == nil {
			continue
		}
		if strings.TrimSpace(trimOptional(item.JobID)) != strings.TrimSpace(jobID) {
			continue
		}
		filtered = append(filtered, item)
	}
	_ = total
	return int64(len(filtered)), sessionHistoryModelsToSummaries(filtered, offset, limit), nil
}

func sessionHistoryModelsToSummaries(list []*model.SessionHistoryEventM, offset int64, limit int64) []*SessionHistorySummary {
	total := int64(len(list))
	if total == 0 {
		return []*SessionHistorySummary{}
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return []*SessionHistorySummary{}
	}
	if limit <= 0 {
		limit = defaultWorkbenchViewerLimit
	}
	end := offset + limit
	if end > total {
		end = total
	}
	summaries := make([]*SessionHistorySummary, 0, end-offset)
	for _, item := range list[offset:end] {
		if item == nil {
			continue
		}
		summaries = append(summaries, sessionHistoryModelToSummary(item))
	}
	return summaries
}

func sessionHistoryModelToSummary(in *model.SessionHistoryEventM) *SessionHistorySummary {
	if in == nil {
		return &SessionHistorySummary{}
	}
	return &SessionHistorySummary{
		EventID:        strings.TrimSpace(in.EventID),
		EventType:      strings.TrimSpace(in.EventType),
		SessionID:      strings.TrimSpace(in.SessionID),
		IncidentID:     strings.TrimSpace(trimOptional(in.IncidentID)),
		JobID:          strings.TrimSpace(trimOptional(in.JobID)),
		Actor:          strings.TrimSpace(in.Actor),
		Note:           strings.TrimSpace(trimOptional(in.Note)),
		ReasonCode:     strings.TrimSpace(trimOptional(in.ReasonCode)),
		PayloadSummary: parseOptionalJSONObject(in.PayloadSummaryJSON),
		CreatedAt:      toRFC3339String(in.CreatedAt),
	}
}

func (b *aiJobBiz) loadWorkbenchCrossSessionHistory(
	ctx context.Context,
	sessionID string,
	offset int64,
	limit int64,
	ascending bool,
) (int64, []*SessionHistorySummary, error) {
	sessionObj, err := b.store.SessionContext().Get(ctx, where.T(ctx).F("session_id", sessionID))
	if err != nil {
		return 0, nil, errno.ErrSessionContextGetFailed
	}
	incidentID := strings.TrimSpace(trimOptional(sessionObj.IncidentID))
	if incidentID == "" {
		order := "desc"
		if ascending {
			order = "asc"
		}
		resp, listErr := b.sessionBiz.ListHistory(ctx, &sessionbiz.ListSessionHistoryRequest{
			SessionID: sessionID,
			Offset:    offset,
			Limit:     limit,
			Order:     strPtr(order),
		})
		if listErr != nil {
			return 0, nil, listErr
		}
		return resp.TotalCount, sessionHistoryReadModelsToSummaries(resp.Events), nil
	}
	_, sessions, err := b.store.SessionContext().List(ctx, where.T(ctx).F("incident_id", incidentID).O(0).L(int(maxOperatorDashboardTrendScan)))
	if err != nil {
		return 0, nil, errno.ErrSessionContextListFailed
	}
	sessionIDs := make([]string, 0, len(sessions))
	for _, candidate := range sessions {
		if candidate == nil {
			continue
		}
		assignment := extractSessionAssignmentState(candidate.ContextStateJSON)
		if !b.canAccessOperatorSession(ctx, candidate, assignment) {
			continue
		}
		sessionIDs = append(sessionIDs, strings.TrimSpace(candidate.SessionID))
	}
	if len(sessionIDs) == 0 {
		sessionIDs = append(sessionIDs, sessionID)
	}
	total, list, err := b.store.SessionHistoryEvent().ListBySessionIDsAndEventTypes(
		ctx,
		sessionIDs,
		nil,
		int(offset),
		int(limit),
		ascending,
	)
	if err != nil {
		return 0, nil, errno.ErrSessionHistoryListFailed
	}
	return total, sessionHistoryModelsToSummaries(list, 0, int64(len(list))), nil
}

func sessionHistoryReadModelsToSummaries(in []*sessionbiz.SessionHistoryEventReadModel) []*SessionHistorySummary {
	out := make([]*SessionHistorySummary, 0, len(in))
	for _, event := range in {
		if event == nil {
			continue
		}
		out = append(out, &SessionHistorySummary{
			EventID:        strings.TrimSpace(event.EventID),
			EventType:      strings.TrimSpace(event.EventType),
			SessionID:      strings.TrimSpace(event.SessionID),
			IncidentID:     strings.TrimSpace(event.IncidentID),
			JobID:          strings.TrimSpace(event.JobID),
			Actor:          strings.TrimSpace(event.Actor),
			Note:           strings.TrimSpace(event.Note),
			ReasonCode:     strings.TrimSpace(event.ReasonCode),
			PayloadSummary: cloneMapAny(event.PayloadSummary),
			CreatedAt:      strings.TrimSpace(event.CreatedAt),
		})
	}
	return out
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
	filters, err := normalizeOperatorInboxFilters(rq)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(filters.TeamID) != "" && !canOperatorScopeTeam(ctx, filters.TeamID) {
		return nil, errno.ErrPermissionDenied
	}
	items, err := b.listOperatorInboxItems(ctx, filters)
	if err != nil {
		return nil, err
	}

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

func (b *aiJobBiz) GetOperatorDashboard(
	ctx context.Context,
	rq *GetOperatorDashboardRequest,
) (*GetOperatorDashboardResponse, error) {
	if rq == nil {
		return nil, errorsx.ErrInvalidArgument
	}
	previewLimit := rq.PreviewLimit
	if previewLimit <= 0 {
		previewLimit = defaultOperatorDashboardPreviewLimit
	}
	if previewLimit > maxOperatorDashboardPreviewLimit {
		return nil, errorsx.ErrInvalidArgument
	}
	recentWindow := rq.RecentWindow
	if recentWindow <= 0 {
		recentWindow = defaultOperatorDashboardRecentWindow
	}
	items, err := b.listOperatorInboxItems(ctx, &operatorInboxFilters{})
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	recentThreshold := now.Add(-recentWindow)
	overview := &OperatorDashboardOverview{
		TotalSessions: int64(len(items)),
	}
	escalation := &OperatorDashboardEscalation{}
	distribution := &OperatorDashboardDistribution{
		BySessionType:       map[string]int64{},
		ByLatestTriggerType: map[string]int64{},
		ByReviewState:       map[string]int64{},
	}
	activity := &OperatorDashboardActivity{
		RecentWindowMinutes: int64(recentWindow / time.Minute),
	}

	for _, item := range items {
		if item == nil {
			continue
		}
		reviewState := normalizeInboxReviewState(item.ReviewState)
		distribution.ByReviewState[reviewState]++
		switch reviewState {
		case sessionbiz.SessionReviewStateInReview:
			overview.InReviewCount++
		case sessionbiz.SessionReviewStateConfirmed:
			overview.ConfirmedCount++
		case sessionbiz.SessionReviewStateRejected:
			overview.RejectedCount++
		}
		if item.NeedsReview {
			overview.NeedsReviewCount++
		}
		if strings.TrimSpace(item.Assignee) == "" {
			overview.UnassignedCount++
		} else {
			overview.AssignedCount++
		}
		if item.IsMyQueue {
			overview.MyQueueCount++
		}
		if item.LongUnhandled {
			overview.LongUnhandled++
		}
		if item.HighRisk {
			overview.HighRiskCount++
		}

		escalationState := normalizeSLAEscalationState(item.EscalationState)
		if escalationState == "" {
			escalationState = sessionbiz.SessionEscalationStateNone
		}
		switch escalationState {
		case sessionbiz.SessionEscalationStatePending:
			escalation.PendingEscalationCount++
		case sessionbiz.SessionEscalationStateEscalated:
			escalation.EscalatedCount++
		default:
			escalation.NormalCount++
		}

		sessionType := normalizeInboxSessionType(item.SessionType)
		if sessionType == "" {
			sessionType = "unknown"
		}
		distribution.BySessionType[sessionType]++

		latestTriggerType := strings.ToLower(strings.TrimSpace(item.LatestTriggerType))
		if latestTriggerType == "" {
			latestTriggerType = "unknown"
		}
		distribution.ByLatestTriggerType[latestTriggerType]++

		if containsString(item.NextActionHints, workbenchHintRunInProgress) {
			activity.ActiveRunCount++
		}
		lastActivityAt, ok := parseRFC3339Time(item.LastActivityAt)
		if !ok {
			continue
		}
		if !lastActivityAt.Before(recentThreshold) {
			activity.RecentlyUpdatedCount++
			switch latestTriggerType {
			case "replay":
				activity.RecentReplayCount++
			case "follow_up":
				activity.RecentFollowUpCount++
			}
		}
	}

	return &GetOperatorDashboardResponse{
		AsOf:         toRFC3339String(now),
		Overview:     overview,
		Escalation:   escalation,
		Distribution: distribution,
		Activity:     activity,
		QueuePreview: buildOperatorDashboardQueuePreview(items, int(previewLimit)),
		Navigation: &OperatorDashboardNavigation{
			InboxPath: "/v1/operator/inbox",
			RecommendedFilters: map[string]string{
				"in_review":          "/v1/operator/inbox?review_state=in_review",
				"needs_review":       "/v1/operator/inbox?needs_review=true",
				"pending_escalation": "/v1/operator/inbox?escalation_state=pending",
				"escalated":          "/v1/operator/inbox?escalation_state=escalated",
			},
		},
	}, nil
}

func (b *aiJobBiz) GetOperatorDashboardTrends(
	ctx context.Context,
	rq *GetOperatorDashboardTrendsRequest,
) (*GetOperatorDashboardTrendsResponse, error) {
	if rq == nil {
		return nil, errorsx.ErrInvalidArgument
	}
	windowLabel, windowDuration, err := normalizeOperatorDashboardTrendWindow(rq.Window)
	if err != nil {
		return nil, err
	}
	groupBy, err := normalizeOperatorDashboardTrendGroupBy(rq.GroupBy)
	if err != nil {
		return nil, err
	}
	scanLimit := rq.ScanLimit
	if scanLimit <= 0 {
		scanLimit = defaultOperatorDashboardTrendScan
	}
	if scanLimit > maxOperatorDashboardTrendScan {
		return nil, errorsx.ErrInvalidArgument
	}

	operatorFilter := normalizeOperatorActor(trimOptional(rq.Operator))
	teamFilter := strings.TrimSpace(trimOptional(rq.TeamID))
	if teamFilter != "" && !canOperatorScopeTeam(ctx, teamFilter) {
		return nil, errno.ErrPermissionDenied
	}
	sessionTypeFilter := ""
	if value := strings.TrimSpace(trimOptional(rq.SessionType)); value != "" {
		sessionTypeFilter = normalizeInboxSessionType(value)
		if sessionTypeFilter == "" {
			return nil, errorsx.ErrInvalidArgument
		}
	}

	now := time.Now().UTC()
	windowEnd := now
	windowStart := startOfUTCDay(now).Add(-windowDuration + (24 * time.Hour))
	dayKeys := buildTrendDayKeys(windowStart, windowEnd)
	byDayCounters := map[string]*OperatorDashboardTrendCounter{}
	for _, dayKey := range dayKeys {
		byDayCounters[dayKey] = &OperatorDashboardTrendCounter{}
	}
	summary := &OperatorDashboardTrendCounter{}
	groupedCounters := map[string]*OperatorDashboardTrendCounter{}

	sessionMeta, err := b.listOperatorDashboardTrendSessions(ctx, teamFilter, sessionTypeFilter, scanLimit)
	if err != nil {
		return nil, err
	}
	if len(sessionMeta) == 0 {
		return &GetOperatorDashboardTrendsResponse{
			AsOf:         toRFC3339String(now),
			Window:       windowLabel,
			WindowStart:  toRFC3339String(windowStart),
			WindowEnd:    toRFC3339String(windowEnd),
			GroupBy:      groupBy,
			Applied:      buildOperatorDashboardTrendFilters(operatorFilter, teamFilter, sessionTypeFilter),
			Summary:      summary,
			ByDay:        buildOperatorDashboardTrendByDay(dayKeys, byDayCounters),
			Grouped:      buildOperatorDashboardTrendGrouped(groupedCounters),
			Navigation:   buildOperatorDashboardTrendNavigation(windowLabel, groupBy, operatorFilter, teamFilter, sessionTypeFilter),
			Truncated:    false,
			ScannedCount: 0,
		}, nil
	}

	sessionIDs := make([]string, 0, len(sessionMeta))
	for sessionID := range sessionMeta {
		sessionIDs = append(sessionIDs, sessionID)
	}
	sort.Strings(sessionIDs)
	trendEventTypes := []string{
		sessionbiz.SessionHistoryEventReplayRequested,
		sessionbiz.SessionHistoryEventFollowUpRequested,
		sessionbiz.SessionHistoryEventReviewStarted,
		sessionbiz.SessionHistoryEventReviewConfirmed,
		sessionbiz.SessionHistoryEventReviewRejected,
		sessionbiz.SessionHistoryEventEscalationPending,
		sessionbiz.SessionHistoryEventEscalationEscalated,
		sessionbiz.SessionHistoryEventEscalationCleared,
	}
	totalEvents, events, err := b.store.SessionHistoryEvent().ListBySessionIDsAndEventTypes(
		ctx,
		sessionIDs,
		trendEventTypes,
		0,
		int(scanLimit),
		false,
	)
	if err != nil {
		return nil, errno.ErrSessionHistoryListFailed
	}

	scannedCount := int64(0)
	for _, event := range events {
		if event == nil {
			continue
		}
		scannedCount++
		createdAt := event.CreatedAt.UTC()
		if createdAt.Before(windowStart) || createdAt.After(windowEnd) {
			continue
		}
		if operatorFilter != "" && !operatorActorMatches(event.Actor, operatorFilter) {
			continue
		}
		if !incrementOperatorDashboardTrendCounter(summary, strings.TrimSpace(event.EventType)) {
			continue
		}
		dayKey := createdAt.Format("2006-01-02")
		if dayCounter, ok := byDayCounters[dayKey]; ok {
			incrementOperatorDashboardTrendCounter(dayCounter, strings.TrimSpace(event.EventType))
		}

		meta := sessionMeta[strings.TrimSpace(event.SessionID)]
		groupKey := resolveOperatorDashboardTrendGroupKey(groupBy, event, meta)
		groupCounter, ok := groupedCounters[groupKey]
		if !ok {
			groupCounter = &OperatorDashboardTrendCounter{}
			groupedCounters[groupKey] = groupCounter
		}
		incrementOperatorDashboardTrendCounter(groupCounter, strings.TrimSpace(event.EventType))
	}

	return &GetOperatorDashboardTrendsResponse{
		AsOf:         toRFC3339String(now),
		Window:       windowLabel,
		WindowStart:  toRFC3339String(windowStart),
		WindowEnd:    toRFC3339String(windowEnd),
		GroupBy:      groupBy,
		Applied:      buildOperatorDashboardTrendFilters(operatorFilter, teamFilter, sessionTypeFilter),
		Summary:      summary,
		ByDay:        buildOperatorDashboardTrendByDay(dayKeys, byDayCounters),
		Grouped:      buildOperatorDashboardTrendGrouped(groupedCounters),
		Navigation:   buildOperatorDashboardTrendNavigation(windowLabel, groupBy, operatorFilter, teamFilter, sessionTypeFilter),
		Truncated:    totalEvents > int64(len(events)),
		ScannedCount: scannedCount,
	}, nil
}

func (b *aiJobBiz) GetOperatorTeamDashboard(
	ctx context.Context,
	rq *GetOperatorTeamDashboardRequest,
) (*GetOperatorTeamDashboardResponse, error) {
	if rq == nil {
		return nil, errorsx.ErrInvalidArgument
	}
	offset := rq.Offset
	if offset < 0 {
		return nil, errorsx.ErrInvalidArgument
	}
	limit := rq.Limit
	if limit <= 0 {
		limit = defaultOperatorTeamDashboardLimit
	}
	if limit > maxOperatorTeamDashboardLimit {
		return nil, errorsx.ErrInvalidArgument
	}
	topN := rq.TopN
	if topN <= 0 {
		topN = defaultOperatorTeamDashboardTopN
	}
	if topN > maxOperatorTeamDashboardTopN {
		return nil, errorsx.ErrInvalidArgument
	}
	ascending, err := normalizeOperatorOrder(rq.Order)
	if err != nil {
		return nil, err
	}
	teamID := strings.TrimSpace(rq.TeamID)
	if teamID != "" && !canOperatorScopeTeam(ctx, teamID) {
		return nil, errno.ErrPermissionDenied
	}

	items, err := b.listOperatorInboxItems(ctx, &operatorInboxFilters{TeamID: teamID})
	if err != nil {
		return nil, err
	}
	teamItems := make([]*OperatorInboxItem, 0, len(items))
	teamItems = append(teamItems, items...)
	sortOperatorTeamDashboardItems(teamItems, ascending)

	now := time.Now().UTC()
	overview := &OperatorTeamDashboardOverview{
		TotalSessions: int64(len(teamItems)),
	}
	distribution := &OperatorTeamDashboardDistribution{
		ByAssignee:        map[string]int64{},
		ByReviewState:     map[string]int64{},
		ByEscalationState: map[string]int64{},
		BySessionType:     map[string]int64{},
	}
	topHighRisk := make([]*OperatorTeamDashboardSession, 0, int(topN))
	for _, item := range teamItems {
		if item == nil {
			continue
		}
		if item.NeedsReview {
			overview.NeedsReviewCount++
		}
		if item.IsMyQueue {
			overview.MyQueueCount++
		}
		if strings.TrimSpace(item.Assignee) == "" {
			overview.UnassignedCount++
			distribution.ByAssignee["unassigned"]++
		} else {
			overview.AssignedCount++
			distribution.ByAssignee[strings.TrimSpace(item.Assignee)]++
		}
		if item.LongUnhandled {
			overview.LongUnhandledCount++
		}
		if item.HighRisk {
			overview.HighRiskCount++
			if len(topHighRisk) < int(topN) {
				topHighRisk = append(topHighRisk, operatorInboxItemToTeamDashboardSession(item))
			}
		}
		reviewState := normalizeInboxReviewState(item.ReviewState)
		distribution.ByReviewState[reviewState]++
		escalationState := normalizeSLAEscalationState(item.EscalationState)
		if escalationState == "" {
			escalationState = sessionbiz.SessionEscalationStateNone
		}
		distribution.ByEscalationState[escalationState]++
		switch escalationState {
		case sessionbiz.SessionEscalationStatePending:
			overview.PendingEscalationCount++
		case sessionbiz.SessionEscalationStateEscalated:
			overview.EscalatedCount++
		}
		sessionType := normalizeInboxSessionType(item.SessionType)
		if sessionType == "" {
			sessionType = "unknown"
		}
		distribution.BySessionType[sessionType]++
	}

	totalCount := int64(len(teamItems))
	pagedItems := []*OperatorTeamDashboardSession{}
	if offset < totalCount {
		end := offset + limit
		if end > totalCount {
			end = totalCount
		}
		for _, item := range teamItems[offset:end] {
			pagedItems = append(pagedItems, operatorInboxItemToTeamDashboardSession(item))
		}
	}
	teamInboxPath := ""
	if teamID != "" {
		teamInboxPath = "/v1/operator/inbox?team_id=" + url.QueryEscape(teamID)
	}
	sortOrder := "desc"
	if ascending {
		sortOrder = "asc"
	}
	return &GetOperatorTeamDashboardResponse{
		AsOf:         toRFC3339String(now),
		TeamID:       teamID,
		Overview:     overview,
		Distribution: distribution,
		TopHighRisk:  topHighRisk,
		TotalCount:   totalCount,
		Offset:       offset,
		Limit:        limit,
		Items:        pagedItems,
		SortOrder:    sortOrder,
		Navigation: &OperatorTeamDashboardNavigation{
			InboxPath:     "/v1/operator/inbox",
			TeamInboxPath: teamInboxPath,
		},
	}, nil
}

func buildOperatorDashboardQueuePreview(items []*OperatorInboxItem, previewLimit int) *OperatorDashboardQueuePreview {
	out := &OperatorDashboardQueuePreview{
		InReview:    []*OperatorDashboardPreviewItem{},
		Escalated:   []*OperatorDashboardPreviewItem{},
		NeedsReview: []*OperatorDashboardPreviewItem{},
	}
	if previewLimit <= 0 {
		return out
	}
	for _, item := range items {
		if item == nil {
			continue
		}
		if len(out.InReview) < previewLimit && normalizeInboxReviewState(item.ReviewState) == sessionbiz.SessionReviewStateInReview {
			out.InReview = append(out.InReview, operatorInboxItemToDashboardPreview(item))
		}
		if len(out.Escalated) < previewLimit && normalizeSLAEscalationState(item.EscalationState) == sessionbiz.SessionEscalationStateEscalated {
			out.Escalated = append(out.Escalated, operatorInboxItemToDashboardPreview(item))
		}
		if len(out.NeedsReview) < previewLimit && item.NeedsReview {
			out.NeedsReview = append(out.NeedsReview, operatorInboxItemToDashboardPreview(item))
		}
		if len(out.InReview) >= previewLimit &&
			len(out.Escalated) >= previewLimit &&
			len(out.NeedsReview) >= previewLimit {
			break
		}
	}
	return out
}

func operatorInboxItemToDashboardPreview(item *OperatorInboxItem) *OperatorDashboardPreviewItem {
	if item == nil {
		return &OperatorDashboardPreviewItem{}
	}
	return &OperatorDashboardPreviewItem{
		SessionID:         strings.TrimSpace(item.SessionID),
		IncidentID:        strings.TrimSpace(item.IncidentID),
		SessionType:       strings.TrimSpace(item.SessionType),
		ReviewState:       normalizeInboxReviewState(item.ReviewState),
		EscalationState:   firstTraceNonEmpty(normalizeSLAEscalationState(item.EscalationState), sessionbiz.SessionEscalationStateNone),
		Assignee:          strings.TrimSpace(item.Assignee),
		LatestTriggerType: strings.TrimSpace(item.LatestTriggerType),
		LastActivityAt:    strings.TrimSpace(item.LastActivityAt),
		WorkbenchPath:     strings.TrimSpace(item.WorkbenchPath),
	}
}

func normalizeOperatorInboxFilters(rq *ListOperatorInboxRequest) (*operatorInboxFilters, error) {
	if rq == nil {
		return nil, errorsx.ErrInvalidArgument
	}
	filters := &operatorInboxFilters{}
	if value := trimOptional(rq.ReviewState); value != "" {
		filters.ReviewState = normalizeInboxReviewState(value)
	}
	if rq.NeedsReview != nil {
		filters.HasNeedsReview = true
		filters.NeedsReview = *rq.NeedsReview
	}
	if trimOptional(rq.SessionType) != "" {
		filters.SessionType = normalizeInboxSessionType(trimOptional(rq.SessionType))
		if filters.SessionType == "" {
			return nil, errorsx.ErrInvalidArgument
		}
	}
	filters.Assignee = strings.TrimSpace(trimOptional(rq.Assignee))
	filters.TeamID = strings.TrimSpace(trimOptional(rq.TeamID))
	if value := trimOptional(rq.EscalationState); value != "" {
		filters.EscalationState = normalizeSLAEscalationState(value)
		if filters.EscalationState == "" {
			return nil, errorsx.ErrInvalidArgument
		}
	}
	return filters, nil
}

func (b *aiJobBiz) listOperatorInboxItems(
	ctx context.Context,
	filters *operatorInboxFilters,
) ([]*OperatorInboxItem, error) {
	if filters == nil {
		filters = &operatorInboxFilters{}
	}
	whr := where.T(ctx).O(0).L(int(maxOperatorInboxScan))
	if filters.SessionType != "" {
		whr = whr.F("session_type", filters.SessionType)
	}
	_, sessions, err := b.store.SessionContext().List(ctx, whr)
	if err != nil {
		return nil, errno.ErrSessionContextListFailed
	}

	items := make([]*OperatorInboxItem, 0, len(sessions))
	for _, sessionObj := range sessions {
		assignmentState := extractSessionAssignmentState(sessionObj.ContextStateJSON)
		if !b.canAccessOperatorSession(ctx, sessionObj, assignmentState) {
			continue
		}
		if strings.TrimSpace(filters.TeamID) != "" &&
			!b.matchesOperatorTeamFilter(ctx, strings.TrimSpace(filters.TeamID), sessionObj, assignmentState) {
			continue
		}
		item, buildErr := b.buildOperatorInboxItem(ctx, sessionObj)
		if buildErr != nil {
			return nil, buildErr
		}
		if !matchesOperatorInboxFilters(item, filters) {
			continue
		}
		items = append(items, item)
	}
	sortOperatorInboxItems(items)
	return items, nil
}

func matchesOperatorInboxFilters(item *OperatorInboxItem, filters *operatorInboxFilters) bool {
	if item == nil {
		return false
	}
	if filters == nil {
		return true
	}
	if filters.ReviewState != "" && normalizeInboxReviewState(item.ReviewState) != filters.ReviewState {
		return false
	}
	if filters.HasNeedsReview && item.NeedsReview != filters.NeedsReview {
		return false
	}
	if filters.Assignee != "" && strings.TrimSpace(item.Assignee) != filters.Assignee {
		return false
	}
	if filters.EscalationState != "" && normalizeSLAEscalationState(item.EscalationState) != filters.EscalationState {
		return false
	}
	return true
}

func sortOperatorInboxItems(items []*OperatorInboxItem) {
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
	baseSLAState := extractSessionSLAState(sessionObj.ContextStateJSON)
	slaState := evaluateSessionSLAContext(
		time.Now().UTC(),
		reviewState,
		assignmentState,
		latestRun,
		baseSLAState,
	)
	b.syncSessionSLAStateBestEffort(ctx, sessionObj, baseSLAState, slaState)

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
		SlaDueAt:               strings.TrimSpace(slaState.DueAt),
		EscalationState:        normalizeSLAEscalationState(slaState.EscalationState),
		EscalationLevel:        slaState.EscalationLevel,
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
	item.IsMyQueue = isOperatorAssignee(ctx, item.Assignee)
	item.LongUnhandled = computeOperatorInboxLongUnhandled(item, time.Now().UTC())
	item.HighRisk = computeOperatorInboxHighRisk(item)
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

func computeOperatorInboxLongUnhandled(item *OperatorInboxItem, now time.Time) bool {
	if item == nil {
		return false
	}
	if normalizeInboxReviewState(item.ReviewState) == sessionbiz.SessionReviewStateConfirmed {
		return false
	}
	if dueAt, ok := parseRFC3339Time(item.SlaDueAt); ok && now.After(dueAt) {
		return true
	}
	if strings.TrimSpace(item.Assignee) == "" {
		if lastActivityAt, ok := parseRFC3339Time(item.LastActivityAt); ok {
			return now.Sub(lastActivityAt) > defaultOperatorDashboardRecentWindow
		}
	}
	return false
}

func computeOperatorInboxHighRisk(item *OperatorInboxItem) bool {
	if item == nil {
		return false
	}
	if normalizeSLAEscalationState(item.EscalationState) == sessionbiz.SessionEscalationStateEscalated {
		return true
	}
	if normalizeSLAEscalationState(item.EscalationState) == sessionbiz.SessionEscalationStatePending && item.LongUnhandled {
		return true
	}
	return item.NeedsReview && (item.ConflictsCount > 0 || item.VerificationPending || item.HumanReviewRequired)
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

func sortOperatorTeamDashboardItems(items []*OperatorInboxItem, ascending bool) {
	sort.SliceStable(items, func(i, j int) bool {
		pi := operatorTeamDashboardPriority(items[i])
		pj := operatorTeamDashboardPriority(items[j])
		if pi != pj {
			if ascending {
				return pi > pj
			}
			return pi < pj
		}

		ti := strings.TrimSpace(items[i].LastActivityAt)
		tj := strings.TrimSpace(items[j].LastActivityAt)
		if ti != tj {
			if ascending {
				return ti < tj
			}
			return ti > tj
		}
		return strings.TrimSpace(items[i].SessionID) < strings.TrimSpace(items[j].SessionID)
	})
}

func operatorTeamDashboardPriority(item *OperatorInboxItem) int {
	if item == nil {
		return 5
	}
	if item.HighRisk {
		return 0
	}
	if item.LongUnhandled {
		return 1
	}
	switch normalizeSLAEscalationState(item.EscalationState) {
	case sessionbiz.SessionEscalationStateEscalated:
		return 2
	case sessionbiz.SessionEscalationStatePending:
		return 3
	default:
		if item.NeedsReview {
			return 4
		}
		return 5
	}
}

func operatorInboxItemToTeamDashboardSession(item *OperatorInboxItem) *OperatorTeamDashboardSession {
	if item == nil {
		return &OperatorTeamDashboardSession{}
	}
	return &OperatorTeamDashboardSession{
		SessionID:       strings.TrimSpace(item.SessionID),
		IncidentID:      strings.TrimSpace(item.IncidentID),
		SessionType:     normalizeInboxSessionType(item.SessionType),
		Assignee:        strings.TrimSpace(item.Assignee),
		ReviewState:     normalizeInboxReviewState(item.ReviewState),
		EscalationState: firstTraceNonEmpty(normalizeSLAEscalationState(item.EscalationState), sessionbiz.SessionEscalationStateNone),
		NeedsReview:     item.NeedsReview,
		IsMyQueue:       item.IsMyQueue,
		LongUnhandled:   item.LongUnhandled,
		HighRisk:        item.HighRisk,
		LastActivityAt:  strings.TrimSpace(item.LastActivityAt),
		WorkbenchPath:   strings.TrimSpace(item.WorkbenchPath),
	}
}

func isOperatorAssignee(ctx context.Context, assignee string) bool {
	assignee = strings.ToLower(strings.TrimSpace(assignee))
	if assignee == "" {
		return false
	}
	identities := []string{
		strings.ToLower(strings.TrimSpace(contextx.UserID(ctx))),
		strings.ToLower(strings.TrimSpace(contextx.Username(ctx))),
	}
	for _, raw := range identities {
		if raw == "" {
			continue
		}
		if raw == assignee || "user:"+raw == assignee {
			return true
		}
		if strings.HasPrefix(raw, "user:") && strings.TrimPrefix(raw, "user:") == assignee {
			return true
		}
	}
	return false
}

func normalizeOperatorOrder(order *string) (bool, error) {
	if order == nil || strings.TrimSpace(*order) == "" {
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(*order)) {
	case "asc":
		return true, nil
	case "desc":
		return false, nil
	default:
		return false, errorsx.ErrInvalidArgument
	}
}

func normalizeOperatorDashboardTrendWindow(raw *string) (string, time.Duration, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return "7d", operatorDashboardTrendWindow7D, nil
	}
	switch strings.ToLower(strings.TrimSpace(*raw)) {
	case "7d":
		return "7d", operatorDashboardTrendWindow7D, nil
	case "30d":
		return "30d", operatorDashboardTrendWindow30D, nil
	default:
		return "", 0, errorsx.ErrInvalidArgument
	}
}

func normalizeOperatorDashboardTrendGroupBy(raw *string) (string, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return operatorDashboardTrendGroupBySessionType, nil
	}
	switch strings.ToLower(strings.TrimSpace(*raw)) {
	case operatorDashboardTrendGroupByOperator:
		return operatorDashboardTrendGroupByOperator, nil
	case operatorDashboardTrendGroupByTeam:
		return operatorDashboardTrendGroupByTeam, nil
	case operatorDashboardTrendGroupBySessionType:
		return operatorDashboardTrendGroupBySessionType, nil
	default:
		return "", errorsx.ErrInvalidArgument
	}
}

func buildTrendDayKeys(start time.Time, end time.Time) []string {
	start = startOfUTCDay(start)
	end = end.UTC()
	if start.After(end) {
		return []string{}
	}
	keys := []string{}
	for cursor := start; !cursor.After(end); cursor = cursor.Add(24 * time.Hour) {
		keys = append(keys, cursor.Format("2006-01-02"))
	}
	return keys
}

func startOfUTCDay(ts time.Time) time.Time {
	utc := ts.UTC()
	return time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
}

func incrementOperatorDashboardTrendCounter(counter *OperatorDashboardTrendCounter, eventType string) bool {
	if counter == nil {
		return false
	}
	switch strings.TrimSpace(eventType) {
	case sessionbiz.SessionHistoryEventReplayRequested:
		counter.ReplayCount++
	case sessionbiz.SessionHistoryEventFollowUpRequested:
		counter.FollowUpCount++
	case sessionbiz.SessionHistoryEventReviewStarted:
		counter.ReviewStartedCount++
		counter.ReviewActionCount++
	case sessionbiz.SessionHistoryEventReviewConfirmed:
		counter.ReviewConfirmedCount++
		counter.ReviewActionCount++
	case sessionbiz.SessionHistoryEventReviewRejected:
		counter.ReviewRejectedCount++
		counter.ReviewActionCount++
	case sessionbiz.SessionHistoryEventEscalationPending:
		counter.SLAPendingCount++
		counter.EscalationActionCount++
	case sessionbiz.SessionHistoryEventEscalationEscalated:
		counter.SLAEscalatedCount++
		counter.EscalationActionCount++
	case sessionbiz.SessionHistoryEventEscalationCleared:
		counter.SLAClearedCount++
		counter.EscalationActionCount++
	default:
		return false
	}
	counter.TotalCount++
	return true
}

func resolveOperatorDashboardTrendGroupKey(
	groupBy string,
	event *model.SessionHistoryEventM,
	meta *operatorDashboardTrendSessionMeta,
) string {
	switch groupBy {
	case operatorDashboardTrendGroupByOperator:
		return firstTraceNonEmpty(normalizeOperatorActor(strings.TrimSpace(event.Actor)), "unknown")
	case operatorDashboardTrendGroupByTeam:
		if meta != nil && strings.TrimSpace(meta.TeamKey) != "" {
			return strings.TrimSpace(meta.TeamKey)
		}
		return "unknown"
	case operatorDashboardTrendGroupBySessionType:
		fallthrough
	default:
		if meta != nil && strings.TrimSpace(meta.SessionType) != "" {
			return strings.TrimSpace(meta.SessionType)
		}
		return "unknown"
	}
}

func buildOperatorDashboardTrendByDay(
	dayKeys []string,
	dayCounters map[string]*OperatorDashboardTrendCounter,
) []*OperatorDashboardTrendDay {
	out := make([]*OperatorDashboardTrendDay, 0, len(dayKeys))
	for _, dayKey := range dayKeys {
		counter := dayCounters[dayKey]
		if counter == nil {
			counter = &OperatorDashboardTrendCounter{}
		}
		out = append(out, &OperatorDashboardTrendDay{
			Date:    dayKey,
			Counter: counter,
		})
	}
	return out
}

func buildOperatorDashboardTrendGrouped(groupedCounters map[string]*OperatorDashboardTrendCounter) []*OperatorDashboardTrendGroup {
	if len(groupedCounters) == 0 {
		return []*OperatorDashboardTrendGroup{}
	}
	groupKeys := make([]string, 0, len(groupedCounters))
	for groupKey := range groupedCounters {
		groupKeys = append(groupKeys, groupKey)
	}
	sort.Strings(groupKeys)
	out := make([]*OperatorDashboardTrendGroup, 0, len(groupKeys))
	for _, groupKey := range groupKeys {
		counter := groupedCounters[groupKey]
		if counter == nil {
			counter = &OperatorDashboardTrendCounter{}
		}
		out = append(out, &OperatorDashboardTrendGroup{
			GroupKey: groupKey,
			Counter:  counter,
		})
	}
	return out
}

func normalizeOperatorActor(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "user:") {
		value = strings.TrimPrefix(value, "user:")
	}
	return value
}

func operatorActorMatches(actor string, filter string) bool {
	actor = normalizeOperatorActor(actor)
	filter = normalizeOperatorActor(filter)
	if actor == "" || filter == "" {
		return false
	}
	return actor == filter
}

func buildOperatorDashboardTrendFilters(
	operator string,
	teamID string,
	sessionType string,
) *OperatorDashboardTrendFilters {
	operator = strings.TrimSpace(operator)
	teamID = strings.TrimSpace(teamID)
	sessionType = strings.TrimSpace(sessionType)
	if operator == "" && teamID == "" && sessionType == "" {
		return nil
	}
	return &OperatorDashboardTrendFilters{
		Operator:    operator,
		TeamID:      teamID,
		SessionType: sessionType,
	}
}

func buildOperatorDashboardTrendNavigation(
	window string,
	groupBy string,
	operator string,
	teamID string,
	sessionType string,
) *OperatorDashboardTrendNavigation {
	filters := map[string]string{
		"window":   strings.TrimSpace(window),
		"group_by": strings.TrimSpace(groupBy),
	}
	if operator != "" {
		filters["operator"] = operator
	}
	if teamID != "" {
		filters["team_id"] = teamID
	}
	if sessionType != "" {
		filters["session_type"] = sessionType
	}
	return &OperatorDashboardTrendNavigation{
		DashboardPath: "/v1/operator/dashboard",
		InboxPath:     "/v1/operator/inbox",
		Filters:       filters,
	}
}

func (b *aiJobBiz) listOperatorDashboardTrendSessions(
	ctx context.Context,
	teamFilter string,
	sessionTypeFilter string,
	scanLimit int64,
) (map[string]*operatorDashboardTrendSessionMeta, error) {
	whr := where.T(ctx).O(0).L(int(scanLimit))
	if sessionTypeFilter != "" {
		whr = whr.F("session_type", sessionTypeFilter)
	}
	_, sessions, err := b.store.SessionContext().List(ctx, whr)
	if err != nil {
		return nil, errno.ErrSessionContextListFailed
	}
	out := map[string]*operatorDashboardTrendSessionMeta{}
	teamFilter = strings.TrimSpace(teamFilter)
	for _, sessionObj := range sessions {
		if sessionObj == nil {
			continue
		}
		assignee := strings.TrimSpace(extractSessionAssignmentState(sessionObj.ContextStateJSON).Assignee)
		incident := b.loadSessionIncidentForAccess(ctx, sessionObj)
		if !sessionbiz.CanOperatorAccessSession(ctx, sessionObj, incident, assignee) {
			continue
		}
		if teamFilter != "" {
			filterCtx := contextx.WithOperatorTeams(
				contextx.WithUserID(context.Background(), "operator:dashboard-trend-team-filter"),
				[]string{teamFilter},
			)
			if !sessionbiz.CanOperatorAccessSession(filterCtx, sessionObj, incident, assignee) {
				continue
			}
		}
		sessionID := strings.TrimSpace(sessionObj.SessionID)
		if sessionID == "" {
			continue
		}
		out[sessionID] = &operatorDashboardTrendSessionMeta{
			SessionType: firstTraceNonEmpty(normalizeInboxSessionType(sessionObj.SessionType), "unknown"),
			TeamKey:     deriveOperatorDashboardTrendTeamKey(sessionObj, incident),
		}
	}
	return out, nil
}

func deriveOperatorDashboardTrendTeamKey(sessionObj *model.SessionContextM, incident *model.IncidentM) string {
	if incident != nil {
		if namespace := strings.TrimSpace(incident.Namespace); namespace != "" {
			return "namespace:" + strings.ToLower(namespace)
		}
		if tenantID := strings.TrimSpace(incident.TenantID); tenantID != "" {
			return "tenant:" + strings.ToLower(tenantID)
		}
	}
	if sessionObj != nil {
		segments := strings.Split(strings.ToLower(strings.TrimSpace(sessionObj.BusinessKey)), ":")
		for idx := 0; idx+1 < len(segments); idx++ {
			prefix := strings.TrimSpace(segments[idx])
			value := strings.TrimSpace(segments[idx+1])
			if value == "" {
				continue
			}
			switch prefix {
			case "team", "namespace", "tenant", "ns":
				if prefix == "ns" {
					prefix = "namespace"
				}
				return prefix + ":" + value
			}
		}
	}
	return "unknown"
}

func canOperatorScopeTeam(ctx context.Context, teamID string) bool {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return true
	}
	probe := &model.SessionContextM{
		SessionID:   "team-probe",
		SessionType: sessionbiz.SessionTypeService,
		BusinessKey: "team:" + teamID,
	}
	return sessionbiz.CanOperatorAccessSession(ctx, probe, nil, "")
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

func normalizeSLAEscalationState(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case sessionbiz.SessionEscalationStateNone:
		return sessionbiz.SessionEscalationStateNone
	case sessionbiz.SessionEscalationStatePending:
		return sessionbiz.SessionEscalationStatePending
	case sessionbiz.SessionEscalationStateEscalated:
		return sessionbiz.SessionEscalationStateEscalated
	default:
		return ""
	}
}

func (b *aiJobBiz) canAccessOperatorSession(
	ctx context.Context,
	sessionObj *model.SessionContextM,
	assignmentState *sessionAssignmentContextState,
) bool {
	if sessionObj == nil {
		return false
	}
	if strings.TrimSpace(contextx.UserID(ctx)) == "" {
		return true
	}
	assignee := ""
	if assignmentState != nil {
		assignee = strings.TrimSpace(assignmentState.Assignee)
	} else {
		assignee = strings.TrimSpace(extractSessionAssignmentState(sessionObj.ContextStateJSON).Assignee)
	}
	if sessionbiz.CanOperatorAccessSession(ctx, sessionObj, nil, assignee) {
		return true
	}
	incident := b.loadSessionIncidentForAccess(ctx, sessionObj)
	return sessionbiz.CanOperatorAccessSession(ctx, sessionObj, incident, assignee)
}

func (b *aiJobBiz) loadSessionIncidentForAccess(ctx context.Context, sessionObj *model.SessionContextM) *model.IncidentM {
	if b == nil || b.store == nil || sessionObj == nil {
		return nil
	}
	incidentID := trimOptional(sessionObj.IncidentID)
	if incidentID == "" {
		return nil
	}
	incident, err := b.store.Incident().Get(ctx, where.T(ctx).F("incident_id", incidentID))
	if err != nil {
		return nil
	}
	return incident
}

func (b *aiJobBiz) matchesOperatorTeamFilter(
	ctx context.Context,
	teamID string,
	sessionObj *model.SessionContextM,
	assignmentState *sessionAssignmentContextState,
) bool {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" || sessionObj == nil {
		return teamID == ""
	}
	assignee := ""
	if assignmentState != nil {
		assignee = strings.TrimSpace(assignmentState.Assignee)
	}
	incident := b.loadSessionIncidentForAccess(ctx, sessionObj)
	filterCtx := contextx.WithOperatorTeams(contextx.WithUserID(context.Background(), "operator:team-filter"), []string{teamID})
	return sessionbiz.CanOperatorAccessSession(filterCtx, sessionObj, incident, assignee)
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

func (b *aiJobBiz) loadSessionHistorySummariesBestEffort(
	ctx context.Context,
	sessionID string,
	limit int64,
) []*SessionHistorySummary {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || b == nil || b.sessionBiz == nil {
		return []*SessionHistorySummary{}
	}
	resp, err := b.sessionBiz.ListHistory(ctx, &sessionbiz.ListSessionHistoryRequest{
		SessionID: sessionID,
		Offset:    0,
		Limit:     limit,
		Order:     strPtr("desc"),
	})
	if err != nil || resp == nil {
		return []*SessionHistorySummary{}
	}
	items := make([]*SessionHistorySummary, 0, len(resp.Events))
	for _, event := range resp.Events {
		if event == nil {
			continue
		}
		items = append(items, &SessionHistorySummary{
			EventID:        strings.TrimSpace(event.EventID),
			EventType:      strings.TrimSpace(event.EventType),
			SessionID:      strings.TrimSpace(event.SessionID),
			IncidentID:     strings.TrimSpace(event.IncidentID),
			JobID:          strings.TrimSpace(event.JobID),
			Actor:          strings.TrimSpace(event.Actor),
			Note:           strings.TrimSpace(event.Note),
			ReasonCode:     strings.TrimSpace(event.ReasonCode),
			PayloadSummary: cloneMapAny(event.PayloadSummary),
			CreatedAt:      strings.TrimSpace(event.CreatedAt),
		})
	}
	return items
}

func buildWorkbenchDrillDown(
	sessionID string,
	incidentID string,
	latestRun *TraceReadSummary,
	latestDecision *DecisionTraceReadModel,
	latestCompare *WorkbenchCompareSummary,
	assignmentState *sessionAssignmentContextState,
	reviewFlags *WorkbenchReviewFlags,
	pinnedEvidenceRefs []string,
	recentHistory []*SessionHistorySummary,
	nextHints []string,
) *WorkbenchDrillDown {
	sessionID = strings.TrimSpace(sessionID)
	incidentID = strings.TrimSpace(incidentID)

	latestJobID := ""
	if latestRun != nil {
		latestJobID = strings.TrimSpace(latestRun.JobID)
	}
	latestTracePath := buildAIJobTracePath(latestJobID)
	historyPath := buildSessionHistoryPath(sessionID)
	assignmentHistoryPath := buildOperatorAssignmentHistoryPath(sessionID)
	recentHistoryPath := buildSessionHistoryRecentPath(sessionID, 0, defaultSessionWorkbenchHistoryLimit)
	viewerPath := buildSessionWorkbenchViewerPath(sessionID, nil)
	evidenceViewerPath := buildSessionWorkbenchViewerPath(sessionID, map[string]string{
		"view": workbenchViewerTabEvidence,
	})
	verificationViewerPath := buildSessionWorkbenchViewerPath(sessionID, map[string]string{
		"view": workbenchViewerTabVerification,
	})
	historyViewerPath := buildSessionWorkbenchViewerPath(sessionID, map[string]string{
		"view":          workbenchViewerTabHistory,
		"history_scope": workbenchViewerHistoryScopeSession,
		"order":         "desc",
		"offset":        "0",
		"limit":         strconv.FormatInt(defaultSessionWorkbenchHistoryLimit, 10),
	})
	relatedEvidenceRefs := normalizeStringSlice(pinnedEvidenceRefs)
	relatedVerificationRefs := []string{}
	if latestDecision != nil {
		relatedEvidenceRefs = mergeStringSlices(latestDecision.EvidenceRefs, relatedEvidenceRefs)
		relatedVerificationRefs = normalizeStringSlice(latestDecision.VerificationRefs)
	} else if reviewFlags != nil {
		relatedVerificationRefs = normalizeStringSlice(reviewFlags.VerificationRefs)
	}

	compare := &WorkbenchCompareDrillDown{
		CompareAvailable: false,
	}
	latestComparePath := ""
	compareViewerPath := buildSessionWorkbenchViewerPath(sessionID, map[string]string{
		"view": workbenchViewerTabCompare,
	})
	if latestCompare != nil {
		compare.LeftJobID = strings.TrimSpace(latestCompare.LeftJobID)
		compare.RightJobID = strings.TrimSpace(latestCompare.RightJobID)
		latestComparePath = buildTraceComparePath(compare.LeftJobID, compare.RightJobID)
		compare.ComparePath = latestComparePath
		compare.CompareAvailable = latestComparePath != ""
		if compare.LeftJobID != "" && compare.RightJobID != "" {
			compareViewerPath = buildSessionWorkbenchViewerPath(sessionID, map[string]string{
				"view":         workbenchViewerTabCompare,
				"left_job_id":  compare.LeftJobID,
				"right_job_id": compare.RightJobID,
			})
		}
	}

	incidentEvidencePath := buildIncidentEvidencePath(incidentID)
	incidentVerificationPath := buildIncidentVerificationPath(incidentID)
	recommendedNextView := []string{}
	if latestTracePath != "" {
		recommendedNextView = appendUniqueHint(recommendedNextView, latestTracePath)
	}
	if latestComparePath != "" {
		recommendedNextView = appendUniqueHint(recommendedNextView, latestComparePath)
	}
	if historyPath != "" {
		recommendedNextView = appendUniqueHint(recommendedNextView, historyPath)
	}
	if viewerPath != "" {
		recommendedNextView = appendUniqueHint(recommendedNextView, viewerPath)
	}
	if evidenceViewerPath != "" {
		recommendedNextView = appendUniqueHint(recommendedNextView, evidenceViewerPath)
	}
	if verificationViewerPath != "" {
		recommendedNextView = appendUniqueHint(recommendedNextView, verificationViewerPath)
	}
	if compareViewerPath != "" {
		recommendedNextView = appendUniqueHint(recommendedNextView, compareViewerPath)
	}
	if historyViewerPath != "" {
		recommendedNextView = appendUniqueHint(recommendedNextView, historyViewerPath)
	}
	if assignmentHistoryPath != "" {
		recommendedNextView = appendUniqueHint(recommendedNextView, assignmentHistoryPath)
	}
	if incidentEvidencePath != "" && len(relatedEvidenceRefs) > 0 {
		recommendedNextView = appendUniqueHint(recommendedNextView, incidentEvidencePath)
	}
	if incidentVerificationPath != "" && len(relatedVerificationRefs) > 0 {
		recommendedNextView = appendUniqueHint(recommendedNextView, incidentVerificationPath)
	}

	verificationPending := containsString(nextHints, workbenchHintVerificationPending)
	nextHistoryPath := ""
	if int64(len(recentHistory)) >= defaultSessionWorkbenchHistoryLimit {
		nextHistoryPath = buildSessionHistoryRecentPath(
			sessionID,
			defaultSessionWorkbenchHistoryLimit,
			defaultSessionWorkbenchHistoryLimit,
		)
	}
	latestAssignment := &WorkbenchAssignmentDrillDown{}
	if assignmentState != nil {
		latestAssignment.Assignee = strings.TrimSpace(assignmentState.Assignee)
		latestAssignment.AssignedBy = strings.TrimSpace(assignmentState.AssignedBy)
		latestAssignment.AssignedAt = strings.TrimSpace(assignmentState.AssignedAt)
		latestAssignment.Note = strings.TrimSpace(assignmentState.Note)
	}

	return &WorkbenchDrillDown{
		LatestTracePath:        latestTracePath,
		LatestComparePath:      latestComparePath,
		HistoryPath:            historyPath,
		AssignmentHistoryPath:  assignmentHistoryPath,
		ViewerPath:             viewerPath,
		EvidenceViewerPath:     evidenceViewerPath,
		VerificationViewerPath: verificationViewerPath,
		CompareViewerPath:      compareViewerPath,
		HistoryViewerPath:      historyViewerPath,
		RecommendedNextView:    recommendedNextView,
		LatestDecision: &WorkbenchDecisionDrillDown{
			JobID:                   latestJobID,
			TracePath:               latestTracePath,
			DecisionDetailAvailable: latestDecision != nil,
			RelatedEvidenceRefs:     relatedEvidenceRefs,
			RelatedVerificationRefs: relatedVerificationRefs,
		},
		LatestCompare:    compare,
		LatestAssignment: latestAssignment,
		PinnedEvidence: &WorkbenchEvidenceDrillDown{
			IncidentEvidencePath: incidentEvidencePath,
			EvidenceRefs:         normalizeStringSlice(pinnedEvidenceRefs),
		},
		Verification: &WorkbenchVerificationDrillDown{
			IncidentVerificationPath: incidentVerificationPath,
			VerificationRefs:         relatedVerificationRefs,
			VerificationPending:      verificationPending,
		},
		History: &WorkbenchHistoryDrillDown{
			HistoryPath:    historyPath,
			RecentPath:     recentHistoryPath,
			NextPagePath:   nextHistoryPath,
			RecentLimit:    defaultSessionWorkbenchHistoryLimit,
			RecentReturned: int64(len(recentHistory)),
			Order:          "desc",
		},
	}
}

func buildAIJobTracePath(jobID string) string {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return ""
	}
	return "/v1/ai/jobs/" + jobID + "/trace"
}

func buildTraceComparePath(leftJobID string, rightJobID string) string {
	leftJobID = strings.TrimSpace(leftJobID)
	rightJobID = strings.TrimSpace(rightJobID)
	if leftJobID == "" || rightJobID == "" {
		return ""
	}
	return "/v1/ai/jobs:trace-compare?left_job_id=" + leftJobID + "&right_job_id=" + rightJobID
}

func buildSessionHistoryPath(sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	return "/v1/sessions/" + sessionID + "/history"
}

func buildSessionWorkbenchViewerPath(sessionID string, params map[string]string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	base := "/v1/sessions/" + sessionID + "/workbench/viewer"
	if len(params) == 0 {
		return base
	}
	values := url.Values{}
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := strings.TrimSpace(params[key])
		if value == "" {
			continue
		}
		values.Set(key, value)
	}
	encoded := values.Encode()
	if encoded == "" {
		return base
	}
	return base + "?" + encoded
}

func buildOperatorAssignmentHistoryPath(sessionID string) string {
	base := "/v1/operator/assignment_history"
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return base
	}
	return base + "?session_id=" + url.QueryEscape(sessionID)
}

func buildSessionHistoryRecentPath(sessionID string, offset int64, limit int64) string {
	base := buildSessionHistoryPath(sessionID)
	if base == "" {
		return ""
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = defaultSessionWorkbenchHistoryLimit
	}
	return base + "?order=desc&offset=" + strconv.FormatInt(offset, 10) + "&limit=" + strconv.FormatInt(limit, 10)
}

func buildIncidentEvidencePath(incidentID string) string {
	incidentID = strings.TrimSpace(incidentID)
	if incidentID == "" {
		return ""
	}
	return "/v1/incidents/" + incidentID + "/evidence"
}

func buildIncidentVerificationPath(incidentID string) string {
	incidentID = strings.TrimSpace(incidentID)
	if incidentID == "" {
		return ""
	}
	return "/v1/incidents/" + incidentID + "/verification-runs"
}

func (b *aiJobBiz) syncSessionSLAStateBestEffort(
	ctx context.Context,
	sessionObj *model.SessionContextM,
	base *sessionSLAContextState,
	computed *sessionSLAContextState,
) {
	if b == nil || b.sessionBiz == nil || sessionObj == nil {
		return
	}
	sessionID := strings.TrimSpace(sessionObj.SessionID)
	if sessionID == "" || !sessionSLAStateChanged(base, computed) {
		return
	}
	_, _ = b.sessionBiz.SyncSLAState(ctx, &sessionbiz.SyncSessionSLAStateRequest{
		SessionID:       sessionID,
		AssignedAt:      strPtr(strings.TrimSpace(computed.AssignedAt)),
		DueAt:           strPtr(strings.TrimSpace(computed.DueAt)),
		EscalationState: strings.TrimSpace(computed.EscalationState),
		EscalationLevel: computed.EscalationLevel,
		ReasonCode:      strPtr(strings.TrimSpace(computed.ReasonCode)),
	})
}

func sessionSLAStateChanged(base *sessionSLAContextState, computed *sessionSLAContextState) bool {
	if computed == nil {
		return false
	}
	if base == nil {
		base = &sessionSLAContextState{
			EscalationState: sessionbiz.SessionEscalationStateNone,
		}
	}
	return strings.TrimSpace(base.AssignedAt) != strings.TrimSpace(computed.AssignedAt) ||
		strings.TrimSpace(base.DueAt) != strings.TrimSpace(computed.DueAt) ||
		normalizeSLAEscalationState(base.EscalationState) != normalizeSLAEscalationState(computed.EscalationState) ||
		base.EscalationLevel != computed.EscalationLevel ||
		strings.TrimSpace(base.ReasonCode) != strings.TrimSpace(computed.ReasonCode)
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
	slaState *sessionSLAContextState,
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
	if slaState == nil {
		slaState = &sessionSLAContextState{
			EscalationState: sessionbiz.SessionEscalationStateNone,
			EscalationLevel: 0,
		}
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
		SlaDueAt:           strings.TrimSpace(slaState.DueAt),
		EscalationState:    normalizeSLAEscalationState(slaState.EscalationState),
		EscalationLevel:    slaState.EscalationLevel,
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

func extractSessionSLAState(raw *string) *sessionSLAContextState {
	out := &sessionSLAContextState{
		EscalationState: sessionbiz.SessionEscalationStateNone,
	}
	obj := parseOptionalJSONObject(raw)
	if obj == nil {
		return out
	}
	slaRaw, ok := obj["sla"]
	if !ok {
		return out
	}
	slaObj, ok := slaRaw.(map[string]any)
	if !ok {
		return out
	}
	out.AssignedAt = strings.TrimSpace(anyToString(slaObj["assigned_at"]))
	out.DueAt = strings.TrimSpace(anyToString(slaObj["due_at"]))
	out.EscalationState = normalizeSLAEscalationState(anyToString(slaObj["escalation_state"]))
	if out.EscalationState == "" {
		out.EscalationState = sessionbiz.SessionEscalationStateNone
	}
	out.ReasonCode = strings.TrimSpace(anyToString(slaObj["reason_code"]))
	switch value := slaObj["escalation_level"].(type) {
	case float64:
		out.EscalationLevel = int64(value)
	case int64:
		out.EscalationLevel = value
	case int:
		out.EscalationLevel = int64(value)
	}
	return out
}

func evaluateSessionSLAContext(
	now time.Time,
	reviewState *sessionReviewContextState,
	assignmentState *sessionAssignmentContextState,
	latestRun *TraceReadSummary,
	base *sessionSLAContextState,
) *sessionSLAContextState {
	out := &sessionSLAContextState{
		EscalationState: sessionbiz.SessionEscalationStateNone,
		EscalationLevel: 0,
	}
	if base != nil {
		out.AssignedAt = strings.TrimSpace(base.AssignedAt)
		out.DueAt = strings.TrimSpace(base.DueAt)
		out.EscalationState = normalizeSLAEscalationState(base.EscalationState)
		out.EscalationLevel = base.EscalationLevel
		out.ReasonCode = strings.TrimSpace(base.ReasonCode)
	}
	if out.EscalationState == "" {
		out.EscalationState = sessionbiz.SessionEscalationStateNone
	}
	if assignmentState == nil || strings.TrimSpace(assignmentState.Assignee) == "" {
		out.EscalationState = sessionbiz.SessionEscalationStateNone
		out.EscalationLevel = 0
		return out
	}

	assignedAtRaw := firstTraceNonEmpty(strings.TrimSpace(out.AssignedAt), strings.TrimSpace(assignmentState.AssignedAt))
	assignedAt, ok := parseRFC3339Time(assignedAtRaw)
	if !ok {
		out.EscalationState = sessionbiz.SessionEscalationStateNone
		out.EscalationLevel = 0
		return out
	}
	out.AssignedAt = assignedAt.UTC().Format(time.RFC3339Nano)

	dueAt, ok := parseRFC3339Time(out.DueAt)
	if !ok {
		dueAt = assignedAt.Add(defaultSessionSLAWindow)
	}
	out.DueAt = dueAt.UTC().Format(time.RFC3339Nano)

	if !needsSessionSLAAttention(reviewState, latestRun) {
		out.EscalationState = sessionbiz.SessionEscalationStateNone
		out.EscalationLevel = 0
		return out
	}
	if !now.After(dueAt) {
		out.EscalationState = sessionbiz.SessionEscalationStateNone
		out.EscalationLevel = 0
		return out
	}

	if now.After(dueAt.Add(defaultSessionSLAWindow)) {
		out.EscalationState = sessionbiz.SessionEscalationStateEscalated
		out.EscalationLevel = 2
		out.ReasonCode = "sla_timeout_escalated"
		return out
	}

	out.EscalationState = sessionbiz.SessionEscalationStatePending
	out.EscalationLevel = 1
	out.ReasonCode = "sla_due_passed"
	return out
}

func needsSessionSLAAttention(reviewState *sessionReviewContextState, latestRun *TraceReadSummary) bool {
	review := sessionbiz.SessionReviewStatePending
	if reviewState != nil {
		review = normalizeWorkbenchReviewState(reviewState.State)
	}
	if review != sessionbiz.SessionReviewStateConfirmed {
		return true
	}
	if latestRun == nil {
		return true
	}
	switch strings.TrimSpace(latestRun.Status) {
	case jobStatusSucceeded, jobStatusFailed, jobStatusCanceled:
		return false
	default:
		return true
	}
}

func parseRFC3339Time(raw string) (time.Time, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, trimmed)
	if err != nil {
		return time.Time{}, false
	}
	return parsed.UTC(), true
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

func cloneMapAny(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
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
