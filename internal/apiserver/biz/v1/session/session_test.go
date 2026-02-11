package session

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	"github.com/aiopsre/rca-api/internal/pkg/contextx"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
)

func TestResolveOrCreate_IdempotentByBusinessKey(t *testing.T) {
	biz := newSessionBizForTest(t)

	first, err := biz.ResolveOrCreate(context.Background(), &ResolveOrCreateRequest{
		SessionType: SessionTypeIncident,
		BusinessKey: "incident-001",
		IncidentID:  ptrString("incident-001"),
		Title:       ptrString("checkout"),
	})
	require.NoError(t, err)
	require.True(t, first.Created)
	require.NotNil(t, first.Session)
	require.NotEmpty(t, first.Session.SessionID)
	require.Equal(t, SessionStatusActive, first.Session.Status)

	second, err := biz.ResolveOrCreate(context.Background(), &ResolveOrCreateRequest{
		SessionType: SessionTypeIncident,
		BusinessKey: "incident-001",
		IncidentID:  ptrString("incident-001"),
		Title:       ptrString("checkout"),
	})
	require.NoError(t, err)
	require.False(t, second.Created)
	require.NotNil(t, second.Session)
	require.Equal(t, first.Session.SessionID, second.Session.SessionID)
}

func TestEnsureIncidentSession_CreatesAndCanFetchByIncidentID(t *testing.T) {
	biz := newSessionBizForTest(t)

	resp, err := biz.EnsureIncidentSession(context.Background(), &EnsureIncidentSessionRequest{
		IncidentID: "incident-xyz",
		Title:      ptrString("incident/incident-xyz"),
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Session)
	require.Equal(t, SessionTypeIncident, resp.Session.SessionType)
	require.Equal(t, "incident-xyz", resp.Session.BusinessKey)
	require.NotNil(t, resp.Session.IncidentID)
	require.Equal(t, "incident-xyz", *resp.Session.IncidentID)

	getResp, err := biz.Get(context.Background(), &GetSessionContextRequest{
		IncidentID: ptrString("incident-xyz"),
	})
	require.NoError(t, err)
	require.NotNil(t, getResp.Session)
	require.Equal(t, resp.Session.SessionID, getResp.Session.SessionID)
}

func TestUpdate_PersistsSummaryPinnedAndActiveRun(t *testing.T) {
	biz := newSessionBizForTest(t)

	created, err := biz.ResolveOrCreate(context.Background(), &ResolveOrCreateRequest{
		SessionType: SessionTypeService,
		BusinessKey: "service:checkout",
		Title:       ptrString("checkout service"),
	})
	require.NoError(t, err)
	require.NotNil(t, created.Session)

	updated, err := biz.Update(context.Background(), &UpdateSessionContextRequest{
		SessionID:          created.Session.SessionID,
		LatestSummaryJSON:  ptrString(`{"summary":"deploy regression suspected","confidence":0.81}`),
		PinnedEvidenceJSON: ptrString(`{"refs":["evidence-1","evidence-2"]}`),
		ActiveRunID:        ptrString("ai-job-0001"),
		ContextStateJSON:   ptrString(`{"watch_mode":true}`),
		Status:             ptrString(SessionStatusResolved),
	})
	require.NoError(t, err)
	require.NotNil(t, updated.Session)
	require.Equal(t, SessionStatusResolved, updated.Session.Status)
	require.NotNil(t, updated.Session.LatestSummaryJSON)
	require.NotNil(t, updated.Session.PinnedEvidenceJSON)
	require.NotNil(t, updated.Session.ActiveRunID)
	require.NotNil(t, updated.Session.ContextStateJSON)

	getResp, err := biz.Get(context.Background(), &GetSessionContextRequest{
		SessionID: &created.Session.SessionID,
	})
	require.NoError(t, err)
	require.NotNil(t, getResp.Session)
	require.Equal(t, `{"summary":"deploy regression suspected","confidence":0.81}`, *getResp.Session.LatestSummaryJSON)
	require.Equal(t, `{"refs":["evidence-1","evidence-2"]}`, *getResp.Session.PinnedEvidenceJSON)
	require.Equal(t, "ai-job-0001", *getResp.Session.ActiveRunID)
	require.Equal(t, `{"watch_mode":true}`, *getResp.Session.ContextStateJSON)
}

func TestGet_ReturnsNotFound(t *testing.T) {
	biz := newSessionBizForTest(t)

	_, err := biz.Get(context.Background(), &GetSessionContextRequest{
		SessionID: ptrString("session-missing"),
	})
	require.Error(t, err)
	require.ErrorIs(t, err, errno.ErrSessionContextNotFound)
}

func TestUpdateReviewState_PersistsIntoContextStateJSON(t *testing.T) {
	biz := newSessionBizForTest(t)

	created, err := biz.ResolveOrCreate(context.Background(), &ResolveOrCreateRequest{
		SessionType:      SessionTypeService,
		BusinessKey:      "service:checkout",
		ContextStateJSON: ptrString(`{"watch_mode":true}`),
	})
	require.NoError(t, err)
	require.NotNil(t, created.Session)

	reviewedAt := time.Now().UTC().Truncate(time.Second)
	updateResp, err := biz.UpdateReviewState(context.Background(), &UpdateReviewStateRequest{
		SessionID:   created.Session.SessionID,
		ReviewState: SessionReviewStateInReview,
		ReviewNote:  ptrString("needs manual validation"),
		ReviewedBy:  ptrString("user:alice"),
		ReasonCode:  ptrString("manual_takeover"),
		ReviewedAt:  &reviewedAt,
	})
	require.NoError(t, err)
	require.NotNil(t, updateResp)
	require.NotNil(t, updateResp.Review)
	require.Equal(t, SessionReviewStateInReview, updateResp.Review.State)
	require.Equal(t, "user:alice", updateResp.Review.ReviewedBy)
	require.Equal(t, "needs manual validation", updateResp.Review.Note)

	getResp, err := biz.Get(context.Background(), &GetSessionContextRequest{
		SessionID: ptrString(created.Session.SessionID),
	})
	require.NoError(t, err)
	require.NotNil(t, getResp.Session)
	require.NotNil(t, getResp.Session.ContextStateJSON)

	var state map[string]any
	require.NoError(t, json.Unmarshal([]byte(*getResp.Session.ContextStateJSON), &state))
	require.Equal(t, true, state["watch_mode"])
	reviewObj, ok := state["review"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, SessionReviewStateInReview, reviewObj["state"])
	require.Equal(t, "user:alice", reviewObj["reviewed_by"])
	require.Equal(t, "needs manual validation", reviewObj["note"])
	require.Equal(t, "manual_takeover", reviewObj["reason_code"])
}

func TestUpdateReviewState_InvalidState(t *testing.T) {
	biz := newSessionBizForTest(t)
	_, err := biz.UpdateReviewState(context.Background(), &UpdateReviewStateRequest{
		SessionID:   "session-1",
		ReviewState: "invalid",
	})
	require.Error(t, err)
}

func TestUpdateAssignment_PersistsIntoContextStateJSON(t *testing.T) {
	biz := newSessionBizForTest(t)

	created, err := biz.ResolveOrCreate(context.Background(), &ResolveOrCreateRequest{
		SessionType:      SessionTypeService,
		BusinessKey:      "service:checkout",
		ContextStateJSON: ptrString(`{"watch_mode":true}`),
	})
	require.NoError(t, err)
	require.NotNil(t, created.Session)

	assignedAt := time.Now().UTC().Truncate(time.Second)
	updateResp, err := biz.UpdateAssignment(context.Background(), &UpdateAssignmentRequest{
		SessionID:  created.Session.SessionID,
		Assignee:   "user:oncall-a",
		AssignedBy: ptrString("user:lead-a"),
		AssignNote: ptrString("handoff to oncall shift"),
		AssignedAt: &assignedAt,
	})
	require.NoError(t, err)
	require.NotNil(t, updateResp)
	require.NotNil(t, updateResp.Assignment)
	require.Equal(t, "user:oncall-a", updateResp.Assignment.Assignee)
	require.Equal(t, "user:lead-a", updateResp.Assignment.AssignedBy)
	require.Equal(t, "handoff to oncall shift", updateResp.Assignment.Note)

	getResp, err := biz.Get(context.Background(), &GetSessionContextRequest{
		SessionID: ptrString(created.Session.SessionID),
	})
	require.NoError(t, err)
	require.NotNil(t, getResp.Session)
	require.NotNil(t, getResp.Session.ContextStateJSON)

	var state map[string]any
	require.NoError(t, json.Unmarshal([]byte(*getResp.Session.ContextStateJSON), &state))
	require.Equal(t, true, state["watch_mode"])
	assignObj, ok := state["assignment"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "user:oncall-a", assignObj["assignee"])
	require.Equal(t, "user:lead-a", assignObj["assigned_by"])
	require.Equal(t, "handoff to oncall shift", assignObj["note"])

	slaObj, ok := state["sla"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "none", slaObj["escalation_state"])
	require.EqualValues(t, 0, slaObj["escalation_level"])
	require.NotEmpty(t, slaObj["assigned_at"])
	require.NotEmpty(t, slaObj["due_at"])
}

func TestUpdateAssignment_InvalidAssignee(t *testing.T) {
	biz := newSessionBizForTest(t)
	_, err := biz.UpdateAssignment(context.Background(), &UpdateAssignmentRequest{
		SessionID: "session-1",
		Assignee:  " ",
	})
	require.Error(t, err)
}

func TestSessionHistory_AppendAndList(t *testing.T) {
	biz := newSessionBizForTest(t)

	created, err := biz.ResolveOrCreate(context.Background(), &ResolveOrCreateRequest{
		SessionType: SessionTypeIncident,
		BusinessKey: "incident-1",
		IncidentID:  ptrString("incident-1"),
	})
	require.NoError(t, err)
	require.NotNil(t, created.Session)

	t1 := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Second)
	_, err = biz.AppendHistoryEvent(context.Background(), &AppendSessionHistoryEventRequest{
		SessionID:  created.Session.SessionID,
		EventType:  SessionHistoryEventReplayRequested,
		Actor:      ptrString("user:alice"),
		Note:       ptrString("operator replay"),
		ReasonCode: ptrString("manual_recheck"),
		CreatedAt:  &t1,
	})
	require.NoError(t, err)

	t2 := time.Now().UTC().Add(-1 * time.Minute).Truncate(time.Second)
	_, err = biz.AppendHistoryEvent(context.Background(), &AppendSessionHistoryEventRequest{
		SessionID: created.Session.SessionID,
		EventType: SessionHistoryEventFollowUpRequested,
		Actor:     ptrString("user:bob"),
		CreatedAt: &t2,
		PayloadSummary: map[string]any{
			"source": "workbench",
		},
	})
	require.NoError(t, err)

	desc, err := biz.ListHistory(context.Background(), &ListSessionHistoryRequest{
		SessionID: created.Session.SessionID,
		Offset:    0,
		Limit:     10,
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, desc.TotalCount)
	require.Len(t, desc.Events, 2)
	require.Equal(t, SessionHistoryEventFollowUpRequested, desc.Events[0].EventType)
	require.Equal(t, SessionHistoryEventReplayRequested, desc.Events[1].EventType)

	order := "asc"
	asc, err := biz.ListHistory(context.Background(), &ListSessionHistoryRequest{
		SessionID: created.Session.SessionID,
		Offset:    0,
		Limit:     10,
		Order:     &order,
	})
	require.NoError(t, err)
	require.Len(t, asc.Events, 2)
	require.Equal(t, SessionHistoryEventReplayRequested, asc.Events[0].EventType)
	require.Equal(t, SessionHistoryEventFollowUpRequested, asc.Events[1].EventType)

	paged, err := biz.ListHistory(context.Background(), &ListSessionHistoryRequest{
		SessionID: created.Session.SessionID,
		Offset:    1,
		Limit:     1,
	})
	require.NoError(t, err)
	require.Len(t, paged.Events, 1)
	require.Equal(t, SessionHistoryEventReplayRequested, paged.Events[0].EventType)
}

func TestSessionHistory_AutoRecordedByAssignmentAndReview(t *testing.T) {
	biz := newSessionBizForTest(t)

	created, err := biz.ResolveOrCreate(context.Background(), &ResolveOrCreateRequest{
		SessionType: SessionTypeIncident,
		BusinessKey: "incident-2",
		IncidentID:  ptrString("incident-2"),
	})
	require.NoError(t, err)
	require.NotNil(t, created.Session)

	_, err = biz.UpdateAssignment(context.Background(), &UpdateAssignmentRequest{
		SessionID:  created.Session.SessionID,
		Assignee:   "user:oncall-a",
		AssignedBy: ptrString("user:lead-a"),
	})
	require.NoError(t, err)
	_, err = biz.UpdateAssignment(context.Background(), &UpdateAssignmentRequest{
		SessionID:  created.Session.SessionID,
		Assignee:   "user:oncall-b",
		AssignedBy: ptrString("user:lead-b"),
	})
	require.NoError(t, err)
	_, err = biz.UpdateReviewState(context.Background(), &UpdateReviewStateRequest{
		SessionID:   created.Session.SessionID,
		ReviewState: SessionReviewStateInReview,
		ReviewedBy:  ptrString("user:reviewer"),
	})
	require.NoError(t, err)
	_, err = biz.UpdateReviewState(context.Background(), &UpdateReviewStateRequest{
		SessionID:   created.Session.SessionID,
		ReviewState: SessionReviewStateConfirmed,
		ReviewedBy:  ptrString("user:reviewer"),
	})
	require.NoError(t, err)
	_, err = biz.UpdateReviewState(context.Background(), &UpdateReviewStateRequest{
		SessionID:   created.Session.SessionID,
		ReviewState: SessionReviewStateRejected,
		ReviewedBy:  ptrString("user:reviewer"),
	})
	require.NoError(t, err)

	list, err := biz.ListHistory(context.Background(), &ListSessionHistoryRequest{
		SessionID: created.Session.SessionID,
		Offset:    0,
		Limit:     20,
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(list.Events), 5)

	eventTypes := map[string]bool{}
	for _, event := range list.Events {
		if event != nil {
			eventTypes[event.EventType] = true
		}
	}
	require.True(t, eventTypes[SessionHistoryEventAssigned])
	require.True(t, eventTypes[SessionHistoryEventReassigned])
	require.True(t, eventTypes[SessionHistoryEventReviewStarted])
	require.True(t, eventTypes[SessionHistoryEventReviewConfirmed])
	require.True(t, eventTypes[SessionHistoryEventReviewRejected])
}

func TestSessionHistory_ListAssignmentHistory(t *testing.T) {
	biz := newSessionBizForTest(t)

	created, err := biz.ResolveOrCreate(context.Background(), &ResolveOrCreateRequest{
		SessionType: SessionTypeIncident,
		BusinessKey: "incident-assign-history",
		IncidentID:  ptrString("incident-assign-history"),
	})
	require.NoError(t, err)
	require.NotNil(t, created.Session)

	firstAssignedAt := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Second)
	_, err = biz.UpdateAssignment(context.Background(), &UpdateAssignmentRequest{
		SessionID:  created.Session.SessionID,
		Assignee:   "user:oncall-a",
		AssignedBy: ptrString("user:lead-a"),
		AssignNote: ptrString("handoff to oncall-a"),
		AssignedAt: &firstAssignedAt,
	})
	require.NoError(t, err)
	secondAssignedAt := time.Now().UTC().Add(-1 * time.Minute).Truncate(time.Second)
	_, err = biz.UpdateAssignment(context.Background(), &UpdateAssignmentRequest{
		SessionID:  created.Session.SessionID,
		Assignee:   "user:oncall-b",
		AssignedBy: ptrString("user:lead-b"),
		AssignNote: ptrString("shift changed"),
		AssignedAt: &secondAssignedAt,
	})
	require.NoError(t, err)
	_, err = biz.UpdateReviewState(context.Background(), &UpdateReviewStateRequest{
		SessionID:   created.Session.SessionID,
		ReviewState: SessionReviewStateInReview,
		ReviewedBy:  ptrString("user:reviewer"),
	})
	require.NoError(t, err)

	desc, err := biz.ListAssignmentHistory(context.Background(), &ListSessionAssignmentHistoryRequest{
		SessionID: created.Session.SessionID,
		Offset:    0,
		Limit:     10,
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, desc.TotalCount)
	require.Len(t, desc.Events, 2)
	require.Equal(t, SessionHistoryEventReassigned, desc.Events[0].EventType)
	require.Equal(t, "user:oncall-b", desc.Events[0].Assignee)
	require.Equal(t, "user:lead-b", desc.Events[0].AssignedBy)
	require.Equal(t, "user:oncall-a", desc.Events[0].PreviousAssignee)
	require.Equal(t, secondAssignedAt.Format(time.RFC3339Nano), desc.Events[0].AssignedAt)
	require.Equal(t, "shift changed", desc.Events[0].Note)
	require.Equal(t, SessionHistoryEventAssigned, desc.Events[1].EventType)
	require.Equal(t, "user:oncall-a", desc.Events[1].Assignee)
	require.Equal(t, "user:lead-a", desc.Events[1].AssignedBy)
	require.Equal(t, firstAssignedAt.Format(time.RFC3339Nano), desc.Events[1].AssignedAt)

	order := "asc"
	asc, err := biz.ListAssignmentHistory(context.Background(), &ListSessionAssignmentHistoryRequest{
		SessionID: created.Session.SessionID,
		Offset:    0,
		Limit:     10,
		Order:     &order,
	})
	require.NoError(t, err)
	require.Len(t, asc.Events, 2)
	require.Equal(t, SessionHistoryEventAssigned, asc.Events[0].EventType)
	require.Equal(t, SessionHistoryEventReassigned, asc.Events[1].EventType)

	paged, err := biz.ListAssignmentHistory(context.Background(), &ListSessionAssignmentHistoryRequest{
		SessionID: created.Session.SessionID,
		Offset:    1,
		Limit:     1,
	})
	require.NoError(t, err)
	require.Len(t, paged.Events, 1)
	require.Equal(t, SessionHistoryEventAssigned, paged.Events[0].EventType)
}

func TestSessionHistory_SyncSLAStateRecordsEscalationTransition(t *testing.T) {
	biz := newSessionBizForTest(t)

	created, err := biz.ResolveOrCreate(context.Background(), &ResolveOrCreateRequest{
		SessionType: SessionTypeIncident,
		BusinessKey: "incident-3",
		IncidentID:  ptrString("incident-3"),
	})
	require.NoError(t, err)
	require.NotNil(t, created.Session)

	assignedAt := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second).Format(time.RFC3339Nano)
	dueAt := time.Now().UTC().Add(-1 * time.Hour).Truncate(time.Second).Format(time.RFC3339Nano)
	_, err = biz.SyncSLAState(context.Background(), &SyncSessionSLAStateRequest{
		SessionID:       created.Session.SessionID,
		AssignedAt:      &assignedAt,
		DueAt:           &dueAt,
		EscalationState: SessionEscalationStatePending,
		EscalationLevel: 1,
		ReasonCode:      ptrString("sla_due_passed"),
	})
	require.NoError(t, err)
	_, err = biz.SyncSLAState(context.Background(), &SyncSessionSLAStateRequest{
		SessionID:       created.Session.SessionID,
		AssignedAt:      &assignedAt,
		DueAt:           &dueAt,
		EscalationState: SessionEscalationStateEscalated,
		EscalationLevel: 2,
		ReasonCode:      ptrString("sla_timeout_escalated"),
	})
	require.NoError(t, err)
	_, err = biz.SyncSLAState(context.Background(), &SyncSessionSLAStateRequest{
		SessionID:       created.Session.SessionID,
		AssignedAt:      &assignedAt,
		DueAt:           &dueAt,
		EscalationState: SessionEscalationStateNone,
		EscalationLevel: 0,
		ReasonCode:      ptrString("handled"),
	})
	require.NoError(t, err)

	list, err := biz.ListHistory(context.Background(), &ListSessionHistoryRequest{
		SessionID: created.Session.SessionID,
		Offset:    0,
		Limit:     20,
	})
	require.NoError(t, err)
	eventTypes := []string{}
	for _, event := range list.Events {
		if event != nil {
			eventTypes = append(eventTypes, event.EventType)
		}
	}
	require.Contains(t, eventTypes, SessionHistoryEventEscalationPending)
	require.Contains(t, eventTypes, SessionHistoryEventEscalationEscalated)
	require.Contains(t, eventTypes, SessionHistoryEventEscalationCleared)
}

func TestSessionAccessControl_AssigneeScope(t *testing.T) {
	biz := newSessionBizForTest(t)

	created, err := biz.ResolveOrCreate(context.Background(), &ResolveOrCreateRequest{
		SessionType: SessionTypeIncident,
		BusinessKey: "incident-access-assignee",
		IncidentID:  ptrString("incident-access-assignee"),
	})
	require.NoError(t, err)
	require.NotNil(t, created.Session)

	_, err = biz.UpdateAssignment(context.Background(), &UpdateAssignmentRequest{
		SessionID:  created.Session.SessionID,
		Assignee:   "user:oncall-a",
		AssignedBy: ptrString("user:lead"),
	})
	require.NoError(t, err)

	selfCtx := contextx.WithUserID(context.Background(), "oncall-a")
	_, err = biz.UpdateReviewState(selfCtx, &UpdateReviewStateRequest{
		SessionID:   created.Session.SessionID,
		ReviewState: SessionReviewStateInReview,
	})
	require.NoError(t, err)

	otherCtx := contextx.WithUserID(context.Background(), "oncall-b")
	_, err = biz.UpdateReviewState(otherCtx, &UpdateReviewStateRequest{
		SessionID:   created.Session.SessionID,
		ReviewState: SessionReviewStateConfirmed,
	})
	require.ErrorIs(t, err, errno.ErrPermissionDenied)
}

func TestSessionAccessControl_TeamScope(t *testing.T) {
	biz := newSessionBizForTest(t)

	created, err := biz.ResolveOrCreate(context.Background(), &ResolveOrCreateRequest{
		SessionType: SessionTypeService,
		BusinessKey: "service:checkout:env:prod:ns:payments:tenant:tenant-a",
	})
	require.NoError(t, err)
	require.NotNil(t, created.Session)

	teamCtx := contextx.WithOperatorTeams(contextx.WithUserID(context.Background(), "operator:team-a"), []string{"namespace:payments"})
	_, err = biz.Get(teamCtx, &GetSessionContextRequest{SessionID: ptrString(created.Session.SessionID)})
	require.NoError(t, err)

	otherTeamCtx := contextx.WithOperatorTeams(contextx.WithUserID(context.Background(), "operator:team-b"), []string{"namespace:checkout"})
	_, err = biz.Get(otherTeamCtx, &GetSessionContextRequest{SessionID: ptrString(created.Session.SessionID)})
	require.ErrorIs(t, err, errno.ErrPermissionDenied)
}

func newSessionBizForTest(t *testing.T) *sessionBiz {
	t.Helper()
	store.ResetForTest()

	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.IncidentM{}, &model.SessionContextM{}, &model.SessionHistoryEventM{}))

	s := store.NewStore(db)
	return New(s)
}
