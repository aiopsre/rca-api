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

func newSessionBizForTest(t *testing.T) *sessionBiz {
	t.Helper()
	store.ResetForTest()

	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.SessionContextM{}))

	s := store.NewStore(db)
	return New(s)
}
