package internal_strategy_config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
)

func TestConfigBiz_DynamicAndFallback(t *testing.T) {
	ctx := context.Background()
	biz, s := newConfigBizTestSetup(t)
	t.Setenv("RCA_DEFAULT_TRIGGER_CONFIG_PATH", writeTestFile(t, "default_trigger.yml", `triggers:
  - trigger_type: "manual"
    pipeline_id: "basic_rca"
    session_type: "incident"
    fallback: true
`))
	t.Setenv("RCA_DEFAULT_TOOLSET_CONFIG_PATH", writeTestFile(t, "default_toolset.yml", `toolsets:
  - pipeline_id: "basic_rca"
    toolset_name: "fallback_toolset"
    allowed_tools:
      - "logs.search"
      - "metrics.query_range"
`))

	fallbackTrigger, err := biz.GetTrigger(ctx, &GetTriggerConfigRequest{TriggerType: "manual"})
	require.NoError(t, err)
	require.Equal(t, "basic_rca", fallbackTrigger.PipelineID)
	require.Equal(t, "static_fallback", fallbackTrigger.Source)

	_, err = biz.UpsertTrigger(ctx, &UpsertTriggerConfigRequest{
		TriggerType: "manual",
		PipelineID:  "advanced_rca",
		SessionType: "incident",
		Fallback:    false,
	})
	require.NoError(t, err)

	dynamicTrigger, err := biz.GetTrigger(ctx, &GetTriggerConfigRequest{TriggerType: "manual"})
	require.NoError(t, err)
	require.Equal(t, "advanced_rca", dynamicTrigger.PipelineID)
	require.Equal(t, "dynamic_db", dynamicTrigger.Source)

	pipelineID, sessionType, source, err := biz.ResolveTriggerPipeline(ctx, "manual")
	require.NoError(t, err)
	require.Equal(t, "advanced_rca", pipelineID)
	require.Equal(t, "incident", sessionType)
	require.Equal(t, "dynamic_db", source)

	fallbackToolsets, err := biz.GetToolsets(ctx, &GetToolsetConfigRequest{PipelineID: "basic_rca"})
	require.NoError(t, err)
	require.Equal(t, "static_fallback", fallbackToolsets.Source)
	require.Len(t, fallbackToolsets.Items, 1)
	require.Equal(t, "fallback_toolset", fallbackToolsets.Items[0].ToolsetName)

	_, err = biz.UpsertToolset(ctx, &UpsertToolsetConfigRequest{
		PipelineID:   "basic_rca",
		ToolsetName:  "dynamic_toolset",
		AllowedTools: []string{"k8s.get_objects", "logs.search"},
	})
	require.NoError(t, err)

	dynamicToolsets, err := biz.GetToolsets(ctx, &GetToolsetConfigRequest{PipelineID: "basic_rca"})
	require.NoError(t, err)
	require.Equal(t, "dynamic_db", dynamicToolsets.Source)
	require.NotEmpty(t, dynamicToolsets.Items)

	session := &model.SessionContextM{
		SessionID:   "session-config-test-1",
		SessionType: "incident",
		BusinessKey: "incident-config-test-1",
		Status:      "active",
	}
	require.NoError(t, s.SessionContext().Create(ctx, session))

	assignedAt := time.Now().UTC().Add(-2 * time.Minute)
	assignment, err := biz.AssignSession(ctx, &AssignSessionRequest{
		SessionID:  session.SessionID,
		Assignee:   "user:oncall-a",
		AssignedBy: "user:lead-a",
		Note:       strPtr("assign by config api"),
		AssignedAt: &assignedAt,
	})
	require.NoError(t, err)
	require.Equal(t, "user:oncall-a", assignment.Assignee)

	readAssignment, err := biz.GetSessionAssignment(ctx, &GetSessionAssignmentRequest{SessionID: session.SessionID})
	require.NoError(t, err)
	require.Equal(t, "dynamic_db", readAssignment.Source)
	require.Equal(t, "user:oncall-a", readAssignment.Assignee)
}

func TestConfigBiz_SLAConfigDynamic(t *testing.T) {
	ctx := context.Background()
	biz, _ := newConfigBizTestSetup(t)

	fallback, err := biz.GetSLA(ctx, &GetSLAConfigRequest{SessionType: "incident"})
	require.NoError(t, err)
	require.Equal(t, int64(7200), fallback.DueSeconds)
	require.Equal(t, "static_fallback", fallback.Source)

	updated, err := biz.UpsertSLA(ctx, &UpsertSLAConfigRequest{
		SessionType:          "incident",
		DueSeconds:           1800,
		EscalationThresholds: []int64{1800, 3600},
	})
	require.NoError(t, err)
	require.Equal(t, "dynamic_db", updated.Source)
	require.Equal(t, int64(1800), updated.DueSeconds)
	require.Equal(t, []int64{1800, 3600}, updated.EscalationThresholds)
}

func newConfigBizTestSetup(t *testing.T) (*configBiz, store.IStore) {
	t.Helper()
	store.ResetForTest()
	t.Cleanup(store.ResetForTest)

	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.SessionContextM{},
		&model.SessionHistoryEventM{},
		&model.PipelineConfigM{},
		&model.TriggerConfigM{},
		&model.ToolsetConfigDynamicM{},
		&model.SLAEscalationConfigM{},
		&model.SessionAssignmentM{},
	))
	s := store.NewStore(db)
	return New(s), s
}

func writeTestFile(t *testing.T, name string, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func strPtr(in string) *string {
	out := in
	return &out
}
