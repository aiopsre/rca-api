package orchestrator_skillset

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/skillartifact"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

func TestSkillsetBiz_ResolvePresignsArtifactURL(t *testing.T) {
	store.ResetForTest()
	t.Cleanup(store.ResetForTest)

	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.SkillReleaseM{},
		&model.SkillsetConfigDynamicM{},
	))
	s := store.NewStore(db)
	ctx := context.Background()

	manifest := `{"skill_id":"claude.analysis","version":"1.0.0","runtime":"python","entrypoint":{"module":"skills.analysis","callable":"run"},"instruction_file":"SKILL.md","resource_files":[],"allowed_tools":["query_logs"]}`
	require.NoError(t, s.InternalStrategyConfig().UpsertSkillRelease(ctx, &model.SkillReleaseM{
		SkillID:      "claude.analysis",
		Version:      "1.0.0",
		BundleDigest: "8f990ba0b577b51cf009ea049368c16bbda1b21e1b93be07a824758bb253c39b",
		ArtifactURL:  "s3://rca-skills-dev/skills/claude.analysis/1.0.0/bundle.zip",
		ManifestJSON: &manifest,
		Status:       "active",
	}))
	skillRefsJSON := `[{"skill_id":"claude.analysis","version":"1.0.0"}]`
	require.NoError(t, s.InternalStrategyConfig().UpsertSkillsetConfig(ctx, &model.SkillsetConfigDynamicM{
		PipelineID:    "basic_rca",
		SkillsetName:  "claude_default",
		SkillRefsJSON: &skillRefsJSON,
	}))

	restore := skillartifact.SetRuntimeManagerForTest(&fakeResolveManager{
		resolvedURL: "http://192.168.39.3:9000/rca-skills-dev/skills/claude.analysis/1.0.0/bundle.zip?X-Amz-Signature=test",
	})
	defer restore()

	pipeline := "basic_rca"
	resp, err := New(s).Resolve(ctx, &v1.ResolveOrchestratorSkillsetsRequest{Pipeline: &pipeline})
	require.NoError(t, err)
	require.Equal(t, "basic_rca", resp.GetPipeline())
	require.Len(t, resp.GetSkillsets(), 1)
	require.Len(t, resp.GetSkillsets()[0].GetSkills(), 1)
	require.Equal(t, "claude.analysis", resp.GetSkillsets()[0].GetSkills()[0].GetSkillID())
	require.Equal(t, "http://192.168.39.3:9000/rca-skills-dev/skills/claude.analysis/1.0.0/bundle.zip?X-Amz-Signature=test", resp.GetSkillsets()[0].GetSkills()[0].GetArtifactURL())
}

type fakeResolveManager struct {
	resolvedURL string
}

func (f *fakeResolveManager) Enabled() bool {
	return true
}

func (f *fakeResolveManager) UploadBundle(context.Context, string, string, []byte) (string, string, error) {
	return "", "", nil
}

func (f *fakeResolveManager) ResolveDownloadURL(_ context.Context, _ string) (string, error) {
	return f.resolvedURL, nil
}
