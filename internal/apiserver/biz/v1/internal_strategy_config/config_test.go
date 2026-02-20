package internal_strategy_config

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/skillartifact"
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

func TestConfigBiz_SkillReleaseAndSkillsetDynamic(t *testing.T) {
	ctx := context.Background()
	biz, _ := newConfigBizTestSetup(t)

	release, err := biz.RegisterSkillRelease(ctx, &RegisterSkillReleaseRequest{
		SkillID:      "claude.analysis",
		Version:      "1.0.0",
		BundleDigest: "8f990ba0b577b51cf009ea049368c16bbda1b21e1b93be07a824758bb253c39b",
		ArtifactURL:  "https://artifacts.example.com/skills/claude.analysis-1.0.0.zip",
		ManifestJSON: `{"skill_id":"claude.analysis","version":"1.0.0","runtime":"python","entrypoint":{"module":"skills.analysis","callable":"run"},"instruction_file":"SKILL.md","resource_files":["templates/guide.md"],"allowed_tools":["query_logs"]}`,
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
			{SkillID: "claude.analysis", Version: "1.0.0"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, "dynamic_db", view.Source)
	require.Len(t, view.Items, 1)
	require.Equal(t, "claude_default", view.Items[0].SkillsetName)
	require.Len(t, view.Items[0].Skills, 1)
	require.Equal(t, "claude.analysis", view.Items[0].Skills[0].SkillID)

	resolved, source, err := biz.ResolveSkillsetByPipeline(ctx, "basic_rca")
	require.NoError(t, err)
	require.Equal(t, "dynamic_db", source)
	require.Len(t, resolved, 1)
	require.Equal(t, "claude_default", resolved[0].SkillsetName)
	require.Len(t, resolved[0].Skills, 1)
	require.Equal(t, "1.0.0", resolved[0].Skills[0].Version)
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
		BundleRaw: buildSkillBundleZip(t, map[string]any{
			"skill_id":         "claude.analysis",
			"version":          "1.0.0",
			"runtime":          "python",
			"entrypoint":       map[string]any{"module": "skills.analysis", "callable": "run"},
			"instruction_file": "SKILL.md",
			"resource_files":   []string{"templates/guide.md"},
			"allowed_tools":    []string{"query_logs"},
		}),
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

func buildSkillBundleZip(t *testing.T, manifest map[string]any) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	manifestFile, err := writer.Create("manifest.json")
	require.NoError(t, err)
	rawManifest, err := json.Marshal(manifest)
	require.NoError(t, err)
	_, err = manifestFile.Write(rawManifest)
	require.NoError(t, err)
	skillFile, err := writer.Create("SKILL.md")
	require.NoError(t, err)
	_, err = skillFile.Write([]byte("# test skill\n"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	return buf.Bytes()
}
