package ai_job

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	sessionbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/session"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
)

func TestOperatorInbox_PreaggregationCacheAndShard(t *testing.T) {
	db := newAIJobTestDB(t)
	s := store.NewStore(db)
	biz := New(s)
	sessionSvc := sessionbiz.New(s)

	incidentA := createTestIncident(t, s)
	incidentB := createTestIncident(t, s)

	jobA := runAndFinalizeWorkbenchJob(t, biz, incidentA.IncidentID, workbenchRunSpec{
		Status:        "failed",
		TriggerType:   "manual",
		TriggerSource: "manual_api",
		Initiator:     "user:a",
		ErrorMessage:  "preaggregation test a",
	})
	jobB := runAndFinalizeWorkbenchJob(t, biz, incidentB.IncidentID, workbenchRunSpec{
		Status:        "failed",
		TriggerType:   "manual",
		TriggerSource: "manual_api",
		Initiator:     "user:b",
		ErrorMessage:  "preaggregation test b",
	})
	sessionA := mustSessionIDByJob(t, s, jobA)
	sessionB := mustSessionIDByJob(t, s, jobB)

	_, err := sessionSvc.UpdateAssignment(context.Background(), &sessionbiz.UpdateAssignmentRequest{
		SessionID:  sessionA,
		Assignee:   "user:oncall-a",
		AssignedBy: ptrAIString("user:lead-a"),
	})
	require.NoError(t, err)
	_, err = sessionSvc.UpdateAssignment(context.Background(), &sessionbiz.UpdateAssignmentRequest{
		SessionID:  sessionB,
		Assignee:   "user:oncall-b",
		AssignedBy: ptrAIString("user:lead-b"),
	})
	require.NoError(t, err)

	limit := int64(20)
	scanLimit := int64(200)
	firstResp, err := biz.ListOperatorInbox(context.Background(), &ListOperatorInboxRequest{
		Offset:    0,
		Limit:     limit,
		ScanLimit: scanLimit,
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, firstResp.TotalCount, int64(2))
	require.NotNil(t, firstResp.Preaggregation)
	require.GreaterOrEqual(t, firstResp.Preaggregation.CacheMissCount, int64(2))
	require.GreaterOrEqual(t, firstResp.Preaggregation.BuiltCount, int64(2))

	asyncRefresh := true
	secondResp, err := biz.ListOperatorInbox(context.Background(), &ListOperatorInboxRequest{
		Offset:       0,
		Limit:        limit,
		ScanLimit:    scanLimit,
		AsyncRefresh: &asyncRefresh,
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, secondResp.TotalCount, int64(2))
	require.NotNil(t, secondResp.Preaggregation)
	require.GreaterOrEqual(t, secondResp.Preaggregation.CacheHitCount, int64(2))
	require.Equal(t, true, secondResp.Preaggregation.AsyncRefresh)

	shardCount := int64(2)
	shardZero := int64(0)
	shardOne := int64(1)
	shardRespA, err := biz.ListOperatorInbox(context.Background(), &ListOperatorInboxRequest{
		Offset:     0,
		Limit:      limit,
		ScanLimit:  scanLimit,
		Shard:      &shardZero,
		ShardCount: &shardCount,
	})
	require.NoError(t, err)
	shardRespB, err := biz.ListOperatorInbox(context.Background(), &ListOperatorInboxRequest{
		Offset:     0,
		Limit:      limit,
		ScanLimit:  scanLimit,
		Shard:      &shardOne,
		ShardCount: &shardCount,
	})
	require.NoError(t, err)
	require.NotNil(t, shardRespA.Preaggregation)
	require.NotNil(t, shardRespB.Preaggregation)
	require.EqualValues(t, 2, shardRespA.Preaggregation.ShardCount)
	require.EqualValues(t, 2, shardRespB.Preaggregation.ShardCount)

	seen := map[string]struct{}{}
	for _, item := range shardRespA.Items {
		if item == nil {
			continue
		}
		seen[item.SessionID] = struct{}{}
	}
	for _, item := range shardRespB.Items {
		if item == nil {
			continue
		}
		if _, exists := seen[item.SessionID]; exists {
			t.Fatalf("session %s appears in both shards", item.SessionID)
		}
		seen[item.SessionID] = struct{}{}
	}
	require.Contains(t, seen, sessionA)
	require.Contains(t, seen, sessionB)
}
