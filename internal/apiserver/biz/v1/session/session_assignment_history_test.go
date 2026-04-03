package session

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/aiopsre/rca-api/internal/pkg/contextx"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
)

func TestListGlobalAssignmentHistory_OrderAndPagination(t *testing.T) {
	biz := newSessionBizForTest(t)

	createdA, err := biz.ResolveOrCreate(context.Background(), &ResolveOrCreateRequest{
		SessionType: SessionTypeIncident,
		BusinessKey: "incident-global-assign-a",
		IncidentID:  ptrString("incident-global-assign-a"),
	})
	require.NoError(t, err)
	createdB, err := biz.ResolveOrCreate(context.Background(), &ResolveOrCreateRequest{
		SessionType: SessionTypeIncident,
		BusinessKey: "incident-global-assign-b",
		IncidentID:  ptrString("incident-global-assign-b"),
	})
	require.NoError(t, err)

	first := time.Now().UTC().Add(-3 * time.Minute).Truncate(time.Second)
	second := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Second)
	third := time.Now().UTC().Add(-1 * time.Minute).Truncate(time.Second)

	_, err = biz.UpdateAssignment(context.Background(), &UpdateAssignmentRequest{
		SessionID:  createdA.Session.SessionID,
		Assignee:   "user:oncall-a",
		AssignedBy: ptrString("user:lead-a"),
		AssignedAt: &first,
	})
	require.NoError(t, err)
	_, err = biz.UpdateAssignment(context.Background(), &UpdateAssignmentRequest{
		SessionID:  createdA.Session.SessionID,
		Assignee:   "user:oncall-b",
		AssignedBy: ptrString("user:lead-b"),
		AssignedAt: &second,
	})
	require.NoError(t, err)
	_, err = biz.UpdateAssignment(context.Background(), &UpdateAssignmentRequest{
		SessionID:  createdB.Session.SessionID,
		Assignee:   "user:oncall-c",
		AssignedBy: ptrString("user:lead-c"),
		AssignedAt: &third,
	})
	require.NoError(t, err)

	descResp, err := biz.ListGlobalAssignmentHistory(context.Background(), &ListGlobalAssignmentHistoryRequest{
		Offset: 0,
		Limit:  10,
	})
	require.NoError(t, err)
	require.EqualValues(t, 3, descResp.TotalCount)
	require.Len(t, descResp.Events, 3)
	require.Equal(t, SessionHistoryEventAssigned, descResp.Events[0].EventType)
	require.Equal(t, createdB.Session.SessionID, descResp.Events[0].SessionID)
	require.Equal(t, SessionHistoryEventReassigned, descResp.Events[1].EventType)
	require.Equal(t, "user:oncall-a", descResp.Events[1].PreviousAssignee)

	ascOrder := "asc"
	ascResp, err := biz.ListGlobalAssignmentHistory(context.Background(), &ListGlobalAssignmentHistoryRequest{
		Offset: 0,
		Limit:  10,
		Order:  &ascOrder,
	})
	require.NoError(t, err)
	require.Len(t, ascResp.Events, 3)
	require.Equal(t, SessionHistoryEventAssigned, ascResp.Events[0].EventType)
	require.Equal(t, createdA.Session.SessionID, ascResp.Events[0].SessionID)
	require.Equal(t, SessionHistoryEventAssigned, ascResp.Events[2].EventType)
	require.Equal(t, createdB.Session.SessionID, ascResp.Events[2].SessionID)

	pagedResp, err := biz.ListGlobalAssignmentHistory(context.Background(), &ListGlobalAssignmentHistoryRequest{
		Offset: 1,
		Limit:  1,
	})
	require.NoError(t, err)
	require.EqualValues(t, 3, pagedResp.TotalCount)
	require.Len(t, pagedResp.Events, 1)
	require.Equal(t, SessionHistoryEventReassigned, pagedResp.Events[0].EventType)
}

func TestListGlobalAssignmentHistory_OperatorAccessControl(t *testing.T) {
	biz := newSessionBizForTest(t)

	payments, err := biz.ResolveOrCreate(context.Background(), &ResolveOrCreateRequest{
		SessionType: SessionTypeService,
		BusinessKey: "service:checkout:ns:payments",
	})
	require.NoError(t, err)
	checkout, err := biz.ResolveOrCreate(context.Background(), &ResolveOrCreateRequest{
		SessionType: SessionTypeService,
		BusinessKey: "service:checkout:ns:checkout",
	})
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Second)
	_, err = biz.UpdateAssignment(context.Background(), &UpdateAssignmentRequest{
		SessionID:  payments.Session.SessionID,
		Assignee:   "user:payments-oncall",
		AssignedBy: ptrString("user:lead-payments"),
		AssignedAt: &now,
	})
	require.NoError(t, err)
	_, err = biz.UpdateAssignment(context.Background(), &UpdateAssignmentRequest{
		SessionID:  checkout.Session.SessionID,
		Assignee:   "user:checkout-oncall",
		AssignedBy: ptrString("user:lead-checkout"),
		AssignedAt: &now,
	})
	require.NoError(t, err)

	paymentsCtx := contextx.WithOperatorTeams(contextx.WithUserID(context.Background(), "operator:payments"), []string{"namespace:payments"})
	resp, err := biz.ListGlobalAssignmentHistory(paymentsCtx, &ListGlobalAssignmentHistoryRequest{Offset: 0, Limit: 10})
	require.NoError(t, err)
	require.EqualValues(t, 1, resp.TotalCount)
	require.Len(t, resp.Events, 1)
	require.Equal(t, payments.Session.SessionID, resp.Events[0].SessionID)

	_, err = biz.ListGlobalAssignmentHistory(paymentsCtx, &ListGlobalAssignmentHistoryRequest{
		SessionID: &checkout.Session.SessionID,
		Offset:    0,
		Limit:     10,
	})
	require.ErrorIs(t, err, errno.ErrPermissionDenied)
}
