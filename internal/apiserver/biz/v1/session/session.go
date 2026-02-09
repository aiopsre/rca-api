package session

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/onexstack/onexstack/pkg/errorsx"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

const (
	SessionTypeIncident = "incident"
	SessionTypeAlert    = "alert"
	SessionTypeService  = "service"
	SessionTypeChange   = "change"
)

const (
	SessionStatusActive   = "active"
	SessionStatusResolved = "resolved"
	SessionStatusArchived = "archived"
)

const (
	SessionReviewStatePending   = "pending"
	SessionReviewStateInReview  = "in_review"
	SessionReviewStateConfirmed = "confirmed"
	SessionReviewStateRejected  = "rejected"
)

const (
	sessionContextStateReviewKey = "review"
	sessionContextStateAssignKey = "assignment"
)

var allowedSessionTypes = map[string]struct{}{
	SessionTypeIncident: {},
	SessionTypeAlert:    {},
	SessionTypeService:  {},
	SessionTypeChange:   {},
}

var allowedSessionStatuses = map[string]struct{}{
	SessionStatusActive:   {},
	SessionStatusResolved: {},
	SessionStatusArchived: {},
}

var allowedSessionReviewStates = map[string]struct{}{
	SessionReviewStatePending:   {},
	SessionReviewStateInReview:  {},
	SessionReviewStateConfirmed: {},
	SessionReviewStateRejected:  {},
}

// SessionBiz defines internal session context operations.
//
//nolint:interfacebloat // Keep minimal create/get/update/internal helper in one entrypoint.
type SessionBiz interface {
	ResolveOrCreate(ctx context.Context, rq *ResolveOrCreateRequest) (*ResolveOrCreateResponse, error)
	EnsureIncidentSession(ctx context.Context, rq *EnsureIncidentSessionRequest) (*ResolveOrCreateResponse, error)
	Get(ctx context.Context, rq *GetSessionContextRequest) (*GetSessionContextResponse, error)
	Update(ctx context.Context, rq *UpdateSessionContextRequest) (*UpdateSessionContextResponse, error)
	UpdateReviewState(ctx context.Context, rq *UpdateReviewStateRequest) (*UpdateReviewStateResponse, error)
	UpdateAssignment(ctx context.Context, rq *UpdateAssignmentRequest) (*UpdateAssignmentResponse, error)

	SessionExpansion
}

//nolint:modernize // Keep explicit empty interface as placeholder expansion point.
type SessionExpansion interface{}

type sessionBiz struct {
	store store.IStore
}

type ResolveOrCreateRequest struct {
	SessionType      string
	BusinessKey      string
	IncidentID       *string
	Title            *string
	Status           *string
	ContextStateJSON *string
}

type ResolveOrCreateResponse struct {
	Session *model.SessionContextM
	Created bool
}

type EnsureIncidentSessionRequest struct {
	IncidentID string
	Title      *string
}

type GetSessionContextRequest struct {
	SessionID   *string
	SessionType *string
	BusinessKey *string
	IncidentID  *string
}

type GetSessionContextResponse struct {
	Session *model.SessionContextM
}

type UpdateSessionContextRequest struct {
	SessionID          string
	IncidentID         *string
	Title              *string
	Status             *string
	LatestSummaryJSON  *string
	PinnedEvidenceJSON *string
	ActiveRunID        *string
	ContextStateJSON   *string
}

type UpdateSessionContextResponse struct {
	Session *model.SessionContextM
}

type ReviewState struct {
	State      string `json:"state"`
	Note       string `json:"note,omitempty"`
	ReviewedBy string `json:"reviewed_by,omitempty"`
	ReviewedAt string `json:"reviewed_at,omitempty"`
	ReasonCode string `json:"reason_code,omitempty"`
}

type UpdateReviewStateRequest struct {
	SessionID   string
	ReviewState string
	ReviewNote  *string
	ReviewedBy  *string
	ReasonCode  *string
	ReviewedAt  *time.Time
}

type UpdateReviewStateResponse struct {
	Session *model.SessionContextM
	Review  *ReviewState
}

type AssignmentState struct {
	Assignee   string `json:"assignee,omitempty"`
	AssignedBy string `json:"assigned_by,omitempty"`
	AssignedAt string `json:"assigned_at,omitempty"`
	Note       string `json:"note,omitempty"`
}

type UpdateAssignmentRequest struct {
	SessionID  string
	Assignee   string
	AssignedBy *string
	AssignNote *string
	AssignedAt *time.Time
}

type UpdateAssignmentResponse struct {
	Session    *model.SessionContextM
	Assignment *AssignmentState
}

var _ SessionBiz = (*sessionBiz)(nil)

// New creates session context biz.
func New(store store.IStore) *sessionBiz {
	return &sessionBiz{store: store}
}

func (b *sessionBiz) ResolveOrCreate(ctx context.Context, rq *ResolveOrCreateRequest) (*ResolveOrCreateResponse, error) {
	if rq == nil {
		return nil, errorsx.ErrInvalidArgument
	}
	sessionType, err := normalizeSessionType(rq.SessionType)
	if err != nil {
		return nil, err
	}
	businessKey := strings.TrimSpace(rq.BusinessKey)
	if businessKey == "" {
		return nil, errorsx.ErrInvalidArgument
	}

	incidentID := trimOptional(rq.IncidentID)
	title := trimOptional(rq.Title)
	contextStateJSON := trimOptional(rq.ContextStateJSON)

	status := SessionStatusActive
	if rq.Status != nil {
		status, err = normalizeSessionStatus(*rq.Status)
		if err != nil {
			return nil, err
		}
	}

	existing, err := b.store.SessionContext().GetByTypeAndBusinessKey(ctx, sessionType, businessKey)
	if err == nil {
		updated := false
		if existing.IncidentID == nil && incidentID != "" {
			existing.IncidentID = ptrString(incidentID)
			updated = true
		}
		if existing.Title == nil && title != "" {
			existing.Title = ptrString(title)
			updated = true
		}
		if existing.ContextStateJSON == nil && contextStateJSON != "" {
			existing.ContextStateJSON = ptrString(contextStateJSON)
			updated = true
		}
		if strings.TrimSpace(existing.Status) == "" {
			existing.Status = SessionStatusActive
			updated = true
		}
		if updated {
			if updateErr := b.store.SessionContext().Update(ctx, existing); updateErr != nil {
				return nil, errno.ErrSessionContextUpdateFailed
			}
		}
		return &ResolveOrCreateResponse{Session: existing, Created: false}, nil
	}
	if !errorsx.Is(err, gorm.ErrRecordNotFound) {
		return nil, errno.ErrSessionContextGetFailed
	}

	obj := &model.SessionContextM{
		SessionType: sessionType,
		BusinessKey: businessKey,
		Status:      status,
	}
	if incidentID != "" {
		obj.IncidentID = ptrString(incidentID)
	}
	if title != "" {
		obj.Title = ptrString(title)
	}
	if contextStateJSON != "" {
		obj.ContextStateJSON = ptrString(contextStateJSON)
	}

	if err := b.store.SessionContext().Create(ctx, obj); err != nil {
		if isDuplicateKeyError(err) {
			existing, getErr := b.store.SessionContext().GetByTypeAndBusinessKey(ctx, sessionType, businessKey)
			if getErr == nil {
				return &ResolveOrCreateResponse{Session: existing, Created: false}, nil
			}
			if errorsx.Is(getErr, gorm.ErrRecordNotFound) {
				return nil, errno.ErrSessionContextConflict
			}
			return nil, errno.ErrSessionContextGetFailed
		}
		return nil, errno.ErrSessionContextCreateFailed
	}
	return &ResolveOrCreateResponse{Session: obj, Created: true}, nil
}

func (b *sessionBiz) EnsureIncidentSession(
	ctx context.Context,
	rq *EnsureIncidentSessionRequest,
) (*ResolveOrCreateResponse, error) {
	if rq == nil {
		return nil, errorsx.ErrInvalidArgument
	}
	incidentID := strings.TrimSpace(rq.IncidentID)
	if incidentID == "" {
		return nil, errorsx.ErrInvalidArgument
	}

	return b.ResolveOrCreate(ctx, &ResolveOrCreateRequest{
		SessionType: SessionTypeIncident,
		BusinessKey: incidentID,
		IncidentID:  &incidentID,
		Title:       rq.Title,
		Status:      ptrString(SessionStatusActive),
	})
}

func (b *sessionBiz) Get(ctx context.Context, rq *GetSessionContextRequest) (*GetSessionContextResponse, error) {
	if rq == nil {
		return nil, errorsx.ErrInvalidArgument
	}

	var (
		out *model.SessionContextM
		err error
	)

	if rq.SessionID != nil && strings.TrimSpace(*rq.SessionID) != "" {
		out, err = b.store.SessionContext().Get(ctx, where.T(ctx).F("session_id", strings.TrimSpace(*rq.SessionID)))
	} else if rq.IncidentID != nil && strings.TrimSpace(*rq.IncidentID) != "" {
		out, err = b.store.SessionContext().GetByIncidentID(ctx, strings.TrimSpace(*rq.IncidentID))
	} else if rq.SessionType != nil && rq.BusinessKey != nil {
		sessionType, normalizeErr := normalizeSessionType(*rq.SessionType)
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		businessKey := strings.TrimSpace(*rq.BusinessKey)
		if businessKey == "" {
			return nil, errorsx.ErrInvalidArgument
		}
		out, err = b.store.SessionContext().GetByTypeAndBusinessKey(ctx, sessionType, businessKey)
	} else {
		return nil, errorsx.ErrInvalidArgument
	}

	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrSessionContextNotFound
		}
		return nil, errno.ErrSessionContextGetFailed
	}
	return &GetSessionContextResponse{Session: out}, nil
}

func (b *sessionBiz) Update(ctx context.Context, rq *UpdateSessionContextRequest) (*UpdateSessionContextResponse, error) {
	if rq == nil {
		return nil, errorsx.ErrInvalidArgument
	}
	sessionID := strings.TrimSpace(rq.SessionID)
	if sessionID == "" {
		return nil, errorsx.ErrInvalidArgument
	}

	out, err := b.store.SessionContext().Get(ctx, where.T(ctx).F("session_id", sessionID))
	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrSessionContextNotFound
		}
		return nil, errno.ErrSessionContextGetFailed
	}

	if rq.IncidentID != nil {
		out.IncidentID = normalizeOptionalPtr(rq.IncidentID)
	}
	if rq.Title != nil {
		out.Title = normalizeOptionalPtr(rq.Title)
	}
	if rq.Status != nil {
		status, normalizeErr := normalizeSessionStatus(*rq.Status)
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		out.Status = status
	}
	if rq.LatestSummaryJSON != nil {
		out.LatestSummaryJSON = normalizeOptionalPtr(rq.LatestSummaryJSON)
	}
	if rq.PinnedEvidenceJSON != nil {
		out.PinnedEvidenceJSON = normalizeOptionalPtr(rq.PinnedEvidenceJSON)
	}
	if rq.ActiveRunID != nil {
		out.ActiveRunID = normalizeOptionalPtr(rq.ActiveRunID)
	}
	if rq.ContextStateJSON != nil {
		out.ContextStateJSON = normalizeOptionalPtr(rq.ContextStateJSON)
	}

	if err := b.store.SessionContext().Update(ctx, out); err != nil {
		if isDuplicateKeyError(err) {
			return nil, errno.ErrSessionContextConflict
		}
		return nil, errno.ErrSessionContextUpdateFailed
	}

	return &UpdateSessionContextResponse{Session: out}, nil
}

func (b *sessionBiz) UpdateReviewState(
	ctx context.Context,
	rq *UpdateReviewStateRequest,
) (*UpdateReviewStateResponse, error) {
	if rq == nil {
		return nil, errorsx.ErrInvalidArgument
	}
	sessionID := strings.TrimSpace(rq.SessionID)
	if sessionID == "" {
		return nil, errorsx.ErrInvalidArgument
	}
	reviewState, err := normalizeSessionReviewState(rq.ReviewState)
	if err != nil {
		return nil, err
	}

	out, err := b.store.SessionContext().Get(ctx, where.T(ctx).F("session_id", sessionID))
	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrSessionContextNotFound
		}
		return nil, errno.ErrSessionContextGetFailed
	}

	review := &ReviewState{
		State:      reviewState,
		Note:       trimOptional(rq.ReviewNote),
		ReviewedBy: trimOptional(rq.ReviewedBy),
		ReasonCode: trimOptional(rq.ReasonCode),
	}
	reviewedAt := time.Now().UTC()
	if rq.ReviewedAt != nil && !rq.ReviewedAt.IsZero() {
		reviewedAt = rq.ReviewedAt.UTC()
	}
	review.ReviewedAt = reviewedAt.Format(time.RFC3339Nano)

	stateObj := parseContextStateJSONObject(out.ContextStateJSON)
	stateObj[sessionContextStateReviewKey] = map[string]any{
		"state":       review.State,
		"note":        review.Note,
		"reviewed_by": review.ReviewedBy,
		"reviewed_at": review.ReviewedAt,
		"reason_code": review.ReasonCode,
	}
	encoded, err := json.Marshal(stateObj)
	if err != nil {
		return nil, errno.ErrSessionContextUpdateFailed
	}
	encodedRaw := string(encoded)
	out.ContextStateJSON = &encodedRaw

	if err := b.store.SessionContext().Update(ctx, out); err != nil {
		if isDuplicateKeyError(err) {
			return nil, errno.ErrSessionContextConflict
		}
		return nil, errno.ErrSessionContextUpdateFailed
	}
	return &UpdateReviewStateResponse{Session: out, Review: review}, nil
}

func (b *sessionBiz) UpdateAssignment(
	ctx context.Context,
	rq *UpdateAssignmentRequest,
) (*UpdateAssignmentResponse, error) {
	if rq == nil {
		return nil, errorsx.ErrInvalidArgument
	}
	sessionID := strings.TrimSpace(rq.SessionID)
	if sessionID == "" {
		return nil, errorsx.ErrInvalidArgument
	}
	assignee := strings.TrimSpace(rq.Assignee)
	if assignee == "" {
		return nil, errorsx.ErrInvalidArgument
	}

	out, err := b.store.SessionContext().Get(ctx, where.T(ctx).F("session_id", sessionID))
	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrSessionContextNotFound
		}
		return nil, errno.ErrSessionContextGetFailed
	}

	assignment := &AssignmentState{
		Assignee:   assignee,
		AssignedBy: trimOptional(rq.AssignedBy),
		Note:       trimOptional(rq.AssignNote),
	}
	assignedAt := time.Now().UTC()
	if rq.AssignedAt != nil && !rq.AssignedAt.IsZero() {
		assignedAt = rq.AssignedAt.UTC()
	}
	assignment.AssignedAt = assignedAt.Format(time.RFC3339Nano)

	stateObj := parseContextStateJSONObject(out.ContextStateJSON)
	stateObj[sessionContextStateAssignKey] = map[string]any{
		"assignee":    assignment.Assignee,
		"assigned_by": assignment.AssignedBy,
		"assigned_at": assignment.AssignedAt,
		"note":        assignment.Note,
	}
	encoded, err := json.Marshal(stateObj)
	if err != nil {
		return nil, errno.ErrSessionContextUpdateFailed
	}
	encodedRaw := string(encoded)
	out.ContextStateJSON = &encodedRaw

	if err := b.store.SessionContext().Update(ctx, out); err != nil {
		if isDuplicateKeyError(err) {
			return nil, errno.ErrSessionContextConflict
		}
		return nil, errno.ErrSessionContextUpdateFailed
	}
	return &UpdateAssignmentResponse{Session: out, Assignment: assignment}, nil
}

func normalizeSessionType(input string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(input))
	if normalized == "" {
		return "", errorsx.ErrInvalidArgument
	}
	if _, ok := allowedSessionTypes[normalized]; !ok {
		return "", errorsx.ErrInvalidArgument
	}
	return normalized, nil
}

func normalizeSessionStatus(input string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(input))
	if normalized == "" {
		return "", errorsx.ErrInvalidArgument
	}
	if _, ok := allowedSessionStatuses[normalized]; !ok {
		return "", errorsx.ErrInvalidArgument
	}
	return normalized, nil
}

func normalizeSessionReviewState(input string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(input))
	if normalized == "" {
		return "", errorsx.ErrInvalidArgument
	}
	if _, ok := allowedSessionReviewStates[normalized]; !ok {
		return "", errorsx.ErrInvalidArgument
	}
	return normalized, nil
}

func normalizeOptionalPtr(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func trimOptional(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func isDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "duplicate") || strings.Contains(lower, "unique constraint")
}

func ptrString(v string) *string {
	return &v
}

func parseContextStateJSONObject(raw *string) map[string]any {
	if raw == nil {
		return map[string]any{}
	}
	trimmed := strings.TrimSpace(*raw)
	if trimmed == "" {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}
