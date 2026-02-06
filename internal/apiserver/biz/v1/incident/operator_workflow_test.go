package incident

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	apiv1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

func TestIncidentBiz_CreateAndListActionLog(t *testing.T) {
	store.ResetForTest()
	t.Cleanup(store.ResetForTest)

	db := newIncidentOperatorTestDB(t)
	s := store.NewStore(db)
	biz := New(s)
	ctx := context.Background()

	incidentID := createIncidentForOperatorTest(t, biz, ctx)
	details := `{"Authorization":"Bearer t-1","headers":{"X-Test":"a"},"safe":"` + strings.Repeat("x", 9000) + `"}`

	resp, err := biz.CreateAction(ctx, &apiv1.CreateIncidentActionRequest{
		IncidentID:  incidentID,
		ActionType:  "restart",
		Summary:     "rotation token check",
		DetailsJSON: &details,
	})
	require.NoError(t, err)
	require.NotNil(t, resp.GetAction())
	require.NotEmpty(t, resp.GetAction().GetActionID())
	require.Contains(t, resp.GetWarnings(), operatorWarningRedacted)
	require.Contains(t, resp.GetWarnings(), operatorWarningTruncated)
	assertNoSensitiveText(t, resp.GetAction().GetSummary())
	assertNoSensitiveText(t, resp.GetAction().GetDetailsJSON())

	listResp, err := biz.ListActions(ctx, &apiv1.ListIncidentActionsRequest{
		IncidentID: incidentID,
		Page:       1,
		Limit:      20,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), listResp.GetTotalCount())
	require.Len(t, listResp.GetActions(), 1)
	require.Equal(t, resp.GetAction().GetActionID(), listResp.GetActions()[0].GetActionID())
	assertNoSensitiveText(t, listResp.GetActions()[0].GetDetailsJSON())

	var timelineCount int64
	require.NoError(t, db.Table("incident_timeline").
		Where("incident_id = ? AND event_type = ? AND ref_id = ?", incidentID, "operator_action", resp.GetAction().GetActionID()).
		Count(&timelineCount).Error)
	require.Equal(t, int64(1), timelineCount)
}

func TestIncidentBiz_CreateAndListVerificationRun(t *testing.T) {
	store.ResetForTest()
	t.Cleanup(store.ResetForTest)

	db := newIncidentOperatorTestDB(t)
	s := store.NewStore(db)
	biz := New(s)
	ctx := context.Background()

	incidentID := createIncidentForOperatorTest(t, biz, ctx)
	paramsJSON := `{"token":"abc","query":"` + strings.Repeat("y", 3000) + `","headers":{"Authorization":"Bearer x"}}`

	resp, err := biz.CreateVerificationRun(ctx, &apiv1.CreateIncidentVerificationRunRequest{
		IncidentID:       incidentID,
		Source:           "manual",
		StepIndex:        1,
		Tool:             "mcp.query_logs",
		ParamsJSON:       &paramsJSON,
		Observed:         "authorization mismatch detected",
		MeetsExpectation: false,
	})
	require.NoError(t, err)
	require.NotNil(t, resp.GetRun())
	require.NotEmpty(t, resp.GetRun().GetRunID())
	require.Contains(t, resp.GetWarnings(), operatorWarningRedacted)
	require.Contains(t, resp.GetWarnings(), operatorWarningTruncated)
	assertNoSensitiveText(t, resp.GetRun().GetObserved())
	assertNoSensitiveText(t, resp.GetRun().GetParamsJSON())

	listResp, err := biz.ListVerificationRuns(ctx, &apiv1.ListIncidentVerificationRunsRequest{
		IncidentID: incidentID,
		Page:       1,
		Limit:      20,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), listResp.GetTotalCount())
	require.Len(t, listResp.GetRuns(), 1)
	require.Equal(t, resp.GetRun().GetRunID(), listResp.GetRuns()[0].GetRunID())
	assertNoSensitiveText(t, listResp.GetRuns()[0].GetParamsJSON())

	var timelineCount int64
	require.NoError(t, db.Table("incident_timeline").
		Where("incident_id = ? AND event_type = ? AND ref_id = ?", incidentID, "verification_run", resp.GetRun().GetRunID()).
		Count(&timelineCount).Error)
	require.Equal(t, int64(1), timelineCount)
}

func TestIncidentBiz_CreateVerificationRun_DerivesJobIDFromActor(t *testing.T) {
	store.ResetForTest()
	t.Cleanup(store.ResetForTest)

	db := newIncidentOperatorTestDB(t)
	s := store.NewStore(db)
	biz := New(s)
	ctx := context.Background()

	incidentID := createIncidentForOperatorTest(t, biz, ctx)
	now := time.Now().UTC().Truncate(time.Second)
	job := &model.AIJobM{
		IncidentID:     incidentID,
		Pipeline:       "basic_rca",
		Trigger:        "manual",
		Status:         "running",
		TimeRangeStart: now.Add(-15 * time.Minute),
		TimeRangeEnd:   now,
		CreatedBy:      "system",
	}
	require.NoError(t, s.AIJob().Create(ctx, job))
	require.NotEmpty(t, job.JobID)

	resp, err := biz.CreateVerificationRun(ctx, &apiv1.CreateIncidentVerificationRunRequest{
		IncidentID:       incidentID,
		Actor:            func() *string { s := "ai:" + job.JobID; return &s }(),
		Source:           "ai_job",
		StepIndex:        1,
		Tool:             "evidence.queryMetrics",
		Observed:         "verification ok",
		MeetsExpectation: true,
	})
	require.NoError(t, err)
	require.NotNil(t, resp.GetRun())

	total, runs, err := s.IncidentVerificationRun().List(ctx, where.T(ctx).O(0).L(10).F("incident_id", incidentID))
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, runs, 1)
	require.NotNil(t, runs[0].JobID)
	require.Equal(t, job.JobID, strings.TrimSpace(*runs[0].JobID))
}

func newIncidentOperatorTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.IncidentM{},
		&model.IncidentActionLogM{},
		&model.IncidentVerificationRunM{},
		&model.AIJobM{},
	))
	require.NoError(t, db.Exec(`
CREATE TABLE incident_timeline (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  incident_id TEXT NOT NULL,
  event_type TEXT,
  ref_id TEXT,
  payload_json TEXT,
  created_at DATETIME
)`).Error)
	return db
}

func createIncidentForOperatorTest(t *testing.T, biz *incidentBiz, ctx context.Context) string {
	t.Helper()

	resp, err := biz.Create(ctx, &apiv1.CreateIncidentRequest{
		Namespace:    "default",
		WorkloadKind: "Deployment",
		WorkloadName: "demo",
		Service:      "demo-svc",
		Severity:     "P1",
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.GetIncidentID())
	return resp.GetIncidentID()
}

func assertNoSensitiveText(t *testing.T, value string) {
	t.Helper()
	lower := strings.ToLower(value)
	require.NotContains(t, lower, "secret")
	require.NotContains(t, lower, "token")
	require.NotContains(t, lower, "authorization")
	require.NotContains(t, lower, "headers")
}
