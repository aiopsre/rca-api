package internal_strategy_config

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/skillartifact"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
)

func TestConfigBiz_BuiltinDefaultsAndDynamicOverrides(t *testing.T) {
	ctx := context.Background()
	biz, s := newConfigBizTestSetup(t)

	pipelineCfg, err := biz.GetPipeline(ctx, &GetPipelineConfigRequest{AlertSource: "manual"})
	require.NoError(t, err)
	require.Equal(t, "basic_rca", pipelineCfg.PipelineID)
	require.Equal(t, "dynamic_db", pipelineCfg.Source)

	triggerCfg, err := biz.GetTrigger(ctx, &GetTriggerConfigRequest{TriggerType: "manual"})
	require.NoError(t, err)
	require.Equal(t, "basic_rca", triggerCfg.PipelineID)
	require.Equal(t, "dynamic_db", triggerCfg.Source)

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

	builtinToolsets, err := biz.GetToolsets(ctx, &GetToolsetConfigRequest{PipelineID: "basic_rca"})
	require.NoError(t, err)
	require.Equal(t, "dynamic_db", builtinToolsets.Source)
	require.Len(t, builtinToolsets.Items, 1)
	require.Equal(t, "canonical_default", builtinToolsets.Items[0].ToolsetName)

	_, err = biz.UpsertToolset(ctx, &UpsertToolsetConfigRequest{
		PipelineID:   "basic_rca",
		ToolsetName:  "dynamic_toolset",
		AllowedTools: []string{"k8s.get_objects", "logs.search"},
	})
	require.NoError(t, err)

	dynamicToolsets, err := biz.GetToolsets(ctx, &GetToolsetConfigRequest{PipelineID: "basic_rca"})
	require.NoError(t, err)
	require.Equal(t, "dynamic_db", dynamicToolsets.Source)
	require.Len(t, dynamicToolsets.Items, 2)
	require.ElementsMatch(t, []string{"canonical_default", "dynamic_toolset"}, []string{
		dynamicToolsets.Items[0].ToolsetName,
		dynamicToolsets.Items[1].ToolsetName,
	})

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

func TestConfigBiz_SkillReleaseAndSkillsetDynamic(t *testing.T) {
	ctx := context.Background()
	biz, _ := newConfigBizTestSetup(t)

	release, err := biz.RegisterSkillRelease(ctx, &RegisterSkillReleaseRequest{
		SkillID:      "claude.analysis",
		Version:      "1.0.0",
		BundleDigest: "8f990ba0b577b51cf009ea049368c16bbda1b21e1b93be07a824758bb253c39b",
		ArtifactURL:  "https://artifacts.example.com/skills/claude.analysis-1.0.0.zip",
		ManifestJSON: `{"bundle_format":"claude_skill_v1","instruction_file":"SKILL.md","name":"Claude Analysis","description":"Analyze incident evidence","compatibility":"Requires query_logs access"}`,
		Status:       "active",
		CreatedBy:    strPtr("user:config-admin"),
	})
	require.NoError(t, err)
	require.Equal(t, "claude.analysis", release.SkillID)
	require.Equal(t, "dynamic_db", release.Source)

	view, err := biz.UpsertSkillset(ctx, &UpsertSkillsetConfigRequest{
		PipelineID:   "basic_rca",
		SkillsetName: "claude_default",
		Skills: []*SkillRef{
			{
				SkillID:      "claude.analysis",
				Version:      "1.0.0",
				Capability:   "diagnosis.enrich",
				Role:         "knowledge",
				ExecutorMode: "script",
				AllowedTools: []string{"query_logs"},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, "dynamic_db", view.Source)
	require.Len(t, view.Items, 1)
	require.Equal(t, "claude_default", view.Items[0].SkillsetName)
	require.Len(t, view.Items[0].Skills, 1)
	require.Equal(t, "claude.analysis", view.Items[0].Skills[0].SkillID)
	require.Equal(t, "knowledge", view.Items[0].Skills[0].Role)
	require.Empty(t, view.Items[0].Skills[0].ExecutorMode)

	resolved, source, err := biz.ResolveSkillsetByPipeline(ctx, "basic_rca")
	require.NoError(t, err)
	require.Equal(t, "dynamic_db", source)
	require.Len(t, resolved, 1)
	require.Equal(t, "claude_default", resolved[0].SkillsetName)
	require.Len(t, resolved[0].Skills, 1)
	require.Equal(t, "1.0.0", resolved[0].Skills[0].Version)
	require.Equal(t, "diagnosis.enrich", resolved[0].Skills[0].Capability)
	require.Equal(t, "knowledge", resolved[0].Skills[0].Role)
	require.Empty(t, resolved[0].Skills[0].ExecutorMode)
	require.Equal(t, []string{"query_logs"}, resolved[0].Skills[0].AllowedTools)
	require.NotNil(t, resolved[0].Skills[0].Priority)
	require.Equal(t, 100, *resolved[0].Skills[0].Priority)
	require.NotNil(t, resolved[0].Skills[0].Enabled)
	require.True(t, *resolved[0].Skills[0].Enabled)
}

func TestConfigBiz_SkillsetDefaultsMissingRoleToExecutor(t *testing.T) {
	ctx := context.Background()
	biz, _ := newConfigBizTestSetup(t)

	_, err := biz.UpsertSkillset(ctx, &UpsertSkillsetConfigRequest{
		PipelineID:   "basic_rca",
		SkillsetName: "default_role",
		Skills: []*SkillRef{
			{
				SkillID:    "claude.analysis",
				Version:    "1.0.0",
				Capability: "diagnosis.enrich",
			},
		},
	})
	require.NoError(t, err)

	resolved, _, err := biz.ResolveSkillsetByPipeline(ctx, "basic_rca")
	require.NoError(t, err)
	require.Len(t, resolved, 1)
	require.Len(t, resolved[0].Skills, 1)
	require.Equal(t, "executor", resolved[0].Skills[0].Role)
	require.Equal(t, "prompt", resolved[0].Skills[0].ExecutorMode)
}

func TestConfigBiz_SkillsetExecutorModeRoundTrip(t *testing.T) {
	ctx := context.Background()
	biz, _ := newConfigBizTestSetup(t)

	_, err := biz.UpsertSkillset(ctx, &UpsertSkillsetConfigRequest{
		PipelineID:   "basic_rca",
		SkillsetName: "script_executor",
		Skills: []*SkillRef{
			{
				SkillID:      "claude.diagnosis.script_enricher",
				Version:      "1.0.0",
				Capability:   "diagnosis.enrich",
				Role:         "executor",
				ExecutorMode: "script",
			},
		},
	})
	require.NoError(t, err)

	resolved, _, err := biz.ResolveSkillsetByPipeline(ctx, "basic_rca")
	require.NoError(t, err)
	require.Len(t, resolved, 1)
	require.Len(t, resolved[0].Skills, 1)
	require.Equal(t, "executor", resolved[0].Skills[0].Role)
	require.Equal(t, "script", resolved[0].Skills[0].ExecutorMode)
}

func TestConfigBiz_UploadSkillReleaseUsesArtifactStorage(t *testing.T) {
	ctx := context.Background()
	biz, _ := newConfigBizTestSetup(t)
	fake := &fakeSkillArtifactManager{
		uploadArtifactRef: "s3://rca-skills-dev/skills/claude.analysis/1.0.0/bundle.zip",
		uploadDigest:      "8f990ba0b577b51cf009ea049368c16bbda1b21e1b93be07a824758bb253c39b",
	}
	restore := skillartifact.SetRuntimeManagerForTest(fake)
	defer restore()

	release, err := biz.UploadSkillRelease(ctx, &UploadSkillReleaseRequest{
		SkillID:   "claude.analysis",
		Version:   "1.0.0",
		BundleRaw: buildSkillBundleZip(t, "Claude Analysis", "Analyze incident evidence", "Requires query_logs access"),
		Status:    "active",
		CreatedBy: strPtr("user:config-admin"),
	})
	require.NoError(t, err)
	require.Equal(t, "claude.analysis", release.SkillID)
	require.Equal(t, "1.0.0", release.Version)
	require.Equal(t, fake.uploadArtifactRef, release.ArtifactURL)
	require.Equal(t, fake.uploadDigest, release.BundleDigest)
	require.Equal(t, "claude.analysis", fake.lastUploadSkillID)
	require.Equal(t, "1.0.0", fake.lastUploadVersion)
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
		&model.SkillReleaseM{},
		&model.SkillsetConfigDynamicM{},
		&model.SLAEscalationConfigM{},
		&model.SessionAssignmentM{},
	))
	s := store.NewStore(db)
	return New(s), s
}

func strPtr(in string) *string {
	out := in
	return &out
}

type fakeSkillArtifactManager struct {
	uploadArtifactRef string
	uploadDigest      string
	lastUploadSkillID string
	lastUploadVersion string
}

func (f *fakeSkillArtifactManager) Enabled() bool {
	return true
}

func (f *fakeSkillArtifactManager) UploadBundle(_ context.Context, skillID string, version string, _ []byte) (string, string, error) {
	f.lastUploadSkillID = skillID
	f.lastUploadVersion = version
	return f.uploadArtifactRef, f.uploadDigest, nil
}

func (f *fakeSkillArtifactManager) ResolveDownloadURL(_ context.Context, artifactRef string) (string, error) {
	return artifactRef, nil
}

func buildSkillBundleZip(t *testing.T, name string, description string, compatibility string) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	skillFile, err := writer.Create("SKILL.md")
	require.NoError(t, err)
	content := fmt.Sprintf("---\nname: %s\ndescription: %s\ncompatibility: %s\n---\n\n# test skill\n", name, description, compatibility)
	_, err = skillFile.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	return buf.Bytes()
}
