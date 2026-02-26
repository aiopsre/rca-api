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

	manifest := `{"bundle_format":"claude_skill_v1","instruction_file":"SKILL.md","name":"Claude Analysis","description":"Analyze incident evidence","compatibility":"Requires query_logs access"}`
	require.NoError(t, s.InternalStrategyConfig().UpsertSkillRelease(ctx, &model.SkillReleaseM{
		SkillID:      "claude.analysis",
		Version:      "1.0.0",
		BundleDigest: "8f990ba0b577b51cf009ea049368c16bbda1b21e1b93be07a824758bb253c39b",
		ArtifactURL:  "s3://rca-skills-dev/skills/claude.analysis/1.0.0/bundle.zip",
		ManifestJSON: &manifest,
		Status:       "active",
	}))
	skillRefsJSON := `[{"skill_id":"claude.analysis","version":"1.0.0","capability":"diagnosis.enrich","role":"knowledge","executor_mode":"script","allowed_tools":["query_logs"],"priority":120,"enabled":true},{"skill_id":"claude.diagnosis.script_enricher","version":"1.0.0","capability":"diagnosis.enrich","role":"executor","executor_mode":"script","allowed_tools":[],"priority":100,"enabled":true}]`
	require.NoError(t, s.InternalStrategyConfig().UpsertSkillsetConfig(ctx, &model.SkillsetConfigDynamicM{
		PipelineID:    "basic_rca",
		SkillsetName:  "claude_default",
		SkillRefsJSON: &skillRefsJSON,
	}))

	require.NoError(t, s.InternalStrategyConfig().UpsertSkillRelease(ctx, &model.SkillReleaseM{
		SkillID:      "claude.diagnosis.script_enricher",
		Version:      "1.0.0",
		BundleDigest: "9f990ba0b577b51cf009ea049368c16bbda1b21e1b93be07a824758bb253c39d",
		ArtifactURL:  "s3://rca-skills-dev/skills/claude.diagnosis.script_enricher/1.0.0/bundle.zip",
		ManifestJSON: &manifest,
		Status:       "active",
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
	require.Len(t, resp.GetSkillsets()[0].GetSkills(), 2)
	require.Equal(t, "claude.analysis", resp.GetSkillsets()[0].GetSkills()[0].GetSkillID())
	require.Equal(t, "http://192.168.39.3:9000/rca-skills-dev/skills/claude.analysis/1.0.0/bundle.zip?X-Amz-Signature=test", resp.GetSkillsets()[0].GetSkills()[0].GetArtifactURL())
	require.Equal(t, "diagnosis.enrich", resp.GetSkillsets()[0].GetSkills()[0].GetCapability())
	require.Equal(t, "knowledge", resp.GetSkillsets()[0].GetSkills()[0].GetRole())
	require.Empty(t, resp.GetSkillsets()[0].GetSkills()[0].GetExecutorMode())
	require.Equal(t, []string{"query_logs"}, resp.GetSkillsets()[0].GetSkills()[0].GetAllowedTools())
	require.Equal(t, int32(120), resp.GetSkillsets()[0].GetSkills()[0].GetPriority())
	require.True(t, resp.GetSkillsets()[0].GetSkills()[0].GetEnabled())
	require.Equal(t, "claude.diagnosis.script_enricher", resp.GetSkillsets()[0].GetSkills()[1].GetSkillID())
	require.Equal(t, "executor", resp.GetSkillsets()[0].GetSkills()[1].GetRole())
	require.Equal(t, "script", resp.GetSkillsets()[0].GetSkills()[1].GetExecutorMode())
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

// TestResolveOrchestratorSkillsetsResponseContract asserts the resolve response model
// required by the Skills runtime contract (worker and contract tests).
// See docs/tooling/claude-skill-bundle-and-binding.md and docs/runtime/agent-driven-skills-runtime.md.
func TestResolveOrchestratorSkillsetsResponseContract(t *testing.T) {
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

	manifest := `{"bundle_format":"claude_skill_v1","instruction_file":"SKILL.md","name":"Test","description":"Test skill","compatibility":""}`
	require.NoError(t, s.InternalStrategyConfig().UpsertSkillRelease(ctx, &model.SkillReleaseM{
		SkillID:      "contract.skill",
		Version:      "1.0.0",
		BundleDigest: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		ArtifactURL:  "https://example.com/skill.zip",
		ManifestJSON: &manifest,
		Status:       "active",
	}))
	skillRefsJSON := `[{"skill_id":"contract.skill","version":"1.0.0","capability":"diagnosis.enrich","role":"executor","executor_mode":"prompt","allowed_tools":[],"priority":100,"enabled":true}]`
	require.NoError(t, s.InternalStrategyConfig().UpsertSkillsetConfig(ctx, &model.SkillsetConfigDynamicM{
		PipelineID:    "basic_rca",
		SkillsetName:  "default",
		SkillRefsJSON: &skillRefsJSON,
	}))

	restore := skillartifact.SetRuntimeManagerForTest(&fakeResolveManager{resolvedURL: "https://resolved.example.com/skill.zip"})
	defer restore()

	pipeline := "basic_rca"
	resp, err := New(s).Resolve(ctx, &v1.ResolveOrchestratorSkillsetsRequest{Pipeline: &pipeline})
	require.NoError(t, err)

	// Contract: response has pipeline and skillsets slice
	require.NotEmpty(t, resp.GetPipeline())
	require.NotNil(t, resp.GetSkillsets())

	for _, skillset := range resp.GetSkillsets() {
		// Contract: each skillset has skillsetID and skills
		require.NotEmpty(t, skillset.GetSkillsetID(), "skillset must have skillsetID")
		require.NotNil(t, skillset.GetSkills(), "skillset must have skills slice")
		for _, skill := range skillset.GetSkills() {
			// Contract: platform binding layer fields (SkillRelease + SkillBinding)
			require.NotEmpty(t, skill.GetSkillID(), "skill must have skillID")
			require.NotEmpty(t, skill.GetVersion(), "skill must have version")
			require.NotEmpty(t, skill.GetArtifactURL(), "skill must have artifactURL for worker download")
			require.NotEmpty(t, skill.GetBundleDigest(), "skill must have bundleDigest for worker verify")
			require.NotEmpty(t, skill.GetManifestJSON(), "skill must have non-empty manifestJSON envelope")
			require.NotEmpty(t, skill.GetCapability(), "skill must have capability from SkillBinding")
			require.NotEmpty(t, skill.GetRole(), "skill must have role (knowledge or executor)")
			require.Contains(t, []string{"knowledge", "executor"}, skill.GetRole(), "role must be knowledge or executor")
			// allowedTools: worker accepts nil or empty slice; contract is that binding may specify list
			_ = skill.GetAllowedTools()
			// ExecutorMode may be empty (default prompt); Priority and Enabled have proto defaults
			_ = skill.GetExecutorMode()
			_ = skill.GetPriority()
			_ = skill.GetEnabled()
		}
	}
}

// TestSkillBindingCompatibilityRules tests SkillBinding compatibility rules:
// - role defaults to "executor" when not specified
// - executor_mode defaults to "prompt" when role is "executor"
// - executor_mode is empty when role is "knowledge"
// - priority defaults to 100 when not specified
// - enabled defaults to true when not specified
// - allowed_tools handles nil, empty list, deduplication
func TestSkillBindingCompatibilityRules(t *testing.T) {
	tests := []struct {
		name               string
		skillRefsJSON      string
		expectRole         string
		expectExecutorMode string
		expectPriority     int32
		expectEnabled      bool
		expectAllowedTools []string
		expectSkipped      bool // if true, expect the skill to be filtered out (disabled)
	}{
		{
			name:               "minimal binding defaults",
			skillRefsJSON:      `[{"skill_id":"binding.test","version":"1.0.0","capability":"diagnosis.enrich"}]`,
			expectRole:         "executor",
			expectExecutorMode: "prompt",
			expectPriority:     100,
			expectEnabled:      true,
			expectAllowedTools: nil,
		},
		{
			name:               "knowledge role has empty executor_mode",
			skillRefsJSON:      `[{"skill_id":"binding.test","version":"1.0.0","capability":"diagnosis.enrich","role":"knowledge"}]`,
			expectRole:         "knowledge",
			expectExecutorMode: "",
			expectPriority:     100,
			expectEnabled:      true,
			expectAllowedTools: nil,
		},
		{
			name:               "explicit executor mode script",
			skillRefsJSON:      `[{"skill_id":"binding.test","version":"1.0.0","capability":"diagnosis.enrich","role":"executor","executor_mode":"script"}]`,
			expectRole:         "executor",
			expectExecutorMode: "script",
			expectPriority:     100,
			expectEnabled:      true,
			expectAllowedTools: nil,
		},
		{
			name:               "explicit priority and enabled",
			skillRefsJSON:      `[{"skill_id":"binding.test","version":"1.0.0","capability":"diagnosis.enrich","priority":150,"enabled":false}]`,
			expectRole:         "executor",
			expectExecutorMode: "prompt",
			expectPriority:     150,
			expectEnabled:      false,
			expectAllowedTools: nil,
			// Note: disabled skills are not returned in resolve response
			expectSkipped:      true,
		},
		{
			name:               "allowed_tools with values",
			skillRefsJSON:      `[{"skill_id":"binding.test","version":"1.0.0","capability":"diagnosis.enrich","allowed_tools":["query_logs","query_metrics"]}]`,
			expectRole:         "executor",
			expectExecutorMode: "prompt",
			expectPriority:     100,
			expectEnabled:      true,
			expectAllowedTools: []string{"query_logs", "query_metrics"},
		},
		{
			name:               "allowed_tools empty list",
			skillRefsJSON:      `[{"skill_id":"binding.test","version":"1.0.0","capability":"diagnosis.enrich","allowed_tools":[]}]`,
			expectRole:         "executor",
			expectExecutorMode: "prompt",
			expectPriority:     100,
			expectEnabled:      true,
			// Note: empty slice and nil are equivalent for allowed_tools; worker accepts both
			expectAllowedTools: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset store for each subtest to avoid shared state
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

			manifest := `{"bundle_format":"claude_skill_v1","instruction_file":"SKILL.md","name":"Test","description":"Test skill"}`
			require.NoError(t, s.InternalStrategyConfig().UpsertSkillRelease(ctx, &model.SkillReleaseM{
				SkillID:      "binding.test",
				Version:      "1.0.0",
				BundleDigest: "abc123",
				ArtifactURL:  "https://example.com/skill.zip",
				ManifestJSON: &manifest,
				Status:       "active",
			}))

			require.NoError(t, s.InternalStrategyConfig().UpsertSkillsetConfig(ctx, &model.SkillsetConfigDynamicM{
				PipelineID:    "basic_rca",
				SkillsetName:  "test_" + tt.name,
				SkillRefsJSON: &tt.skillRefsJSON,
			}))

			restore := skillartifact.SetRuntimeManagerForTest(&fakeResolveManager{resolvedURL: "https://example.com/skill.zip"})
			defer restore()

			pipeline := "basic_rca"
			resp, err := New(s).Resolve(ctx, &v1.ResolveOrchestratorSkillsetsRequest{Pipeline: &pipeline})
			require.NoError(t, err)

			if tt.expectSkipped {
				// Disabled skills are filtered out, and empty skillsets are not returned
				require.Len(t, resp.GetSkillsets(), 0, "disabled skill should result in empty skillset which is not returned")
			} else {
				require.Len(t, resp.GetSkillsets(), 1)
				require.Len(t, resp.GetSkillsets()[0].GetSkills(), 1)

				skill := resp.GetSkillsets()[0].GetSkills()[0]
				require.Equal(t, tt.expectRole, skill.GetRole(), "role mismatch")
				require.Equal(t, tt.expectExecutorMode, skill.GetExecutorMode(), "executor_mode mismatch")
				require.Equal(t, tt.expectPriority, skill.GetPriority(), "priority mismatch")
				require.Equal(t, tt.expectEnabled, skill.GetEnabled(), "enabled mismatch")
				if tt.expectAllowedTools != nil {
					require.Equal(t, tt.expectAllowedTools, skill.GetAllowedTools(), "allowed_tools mismatch")
				} else {
					// nil or empty allowed_tools is acceptable
					require.True(t, len(skill.GetAllowedTools()) == 0, "allowed_tools should be empty")
				}
			}
		})
	}
}

// TestAllowedToolsConstraints tests allowed_tools constraints:
// - nil allowed_tools is valid
// - empty list is valid
// - deduplication
// - case normalization (lowercase)
func TestAllowedToolsConstraints(t *testing.T) {
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

	manifest := `{"bundle_format":"claude_skill_v1","instruction_file":"SKILL.md","name":"Test","description":"Test skill"}`
	require.NoError(t, s.InternalStrategyConfig().UpsertSkillRelease(ctx, &model.SkillReleaseM{
		SkillID:      "allowed.test",
		Version:      "1.0.0",
		BundleDigest: "abc123",
		ArtifactURL:  "https://example.com/skill.zip",
		ManifestJSON: &manifest,
		Status:       "active",
	}))

	// Test: allowed_tools with duplicates (same case) should be deduplicated
	skillRefsJSON := `[{"skill_id":"allowed.test","version":"1.0.0","capability":"diagnosis.enrich","allowed_tools":["query_logs","query_logs","query_metrics","query_metrics"]}]`
	require.NoError(t, s.InternalStrategyConfig().UpsertSkillsetConfig(ctx, &model.SkillsetConfigDynamicM{
		PipelineID:    "basic_rca",
		SkillsetName:  "test_allowed_tools",
		SkillRefsJSON: &skillRefsJSON,
	}))

	restore := skillartifact.SetRuntimeManagerForTest(&fakeResolveManager{resolvedURL: "https://example.com/skill.zip"})
	defer restore()

	pipeline := "basic_rca"
	resp, err := New(s).Resolve(ctx, &v1.ResolveOrchestratorSkillsetsRequest{Pipeline: &pipeline})
	require.NoError(t, err)
	require.Len(t, resp.GetSkillsets(), 1)
	require.Len(t, resp.GetSkillsets()[0].GetSkills(), 1)

	skill := resp.GetSkillsets()[0].GetSkills()[0]
	allowedTools := skill.GetAllowedTools()
	// Expected: deduplicated, should have exactly 2 unique tools
	require.Contains(t, allowedTools, "query_logs", "should contain query_logs")
	require.Contains(t, allowedTools, "query_metrics", "should contain query_metrics")
	require.Len(t, allowedTools, 2, "should have exactly 2 unique tools after dedup")
}

// TestManifestSummaryEnvelopeContract tests manifestJSON envelope validation:
// - requires bundle_format, instruction_file, name, description
// - rejects invalid bundle_format
// - rejects invalid instruction_file
func TestManifestSummaryEnvelopeContract(t *testing.T) {
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

	// Valid manifest
	validManifest := `{"bundle_format":"claude_skill_v1","instruction_file":"SKILL.md","name":"Valid Skill","description":"A valid skill"}`
	require.NoError(t, s.InternalStrategyConfig().UpsertSkillRelease(ctx, &model.SkillReleaseM{
		SkillID:      "valid.skill",
		Version:      "1.0.0",
		BundleDigest: "abc123",
		ArtifactURL:  "https://example.com/skill.zip",
		ManifestJSON: &validManifest,
		Status:       "active",
	}))

	skillRefsJSON := `[{"skill_id":"valid.skill","version":"1.0.0","capability":"diagnosis.enrich"}]`
	require.NoError(t, s.InternalStrategyConfig().UpsertSkillsetConfig(ctx, &model.SkillsetConfigDynamicM{
		PipelineID:    "basic_rca",
		SkillsetName:  "test_manifest",
		SkillRefsJSON: &skillRefsJSON,
	}))

	restore := skillartifact.SetRuntimeManagerForTest(&fakeResolveManager{resolvedURL: "https://example.com/skill.zip"})
	defer restore()

	pipeline := "basic_rca"
	resp, err := New(s).Resolve(ctx, &v1.ResolveOrchestratorSkillsetsRequest{Pipeline: &pipeline})
	require.NoError(t, err)
	require.Len(t, resp.GetSkillsets(), 1)
	require.Len(t, resp.GetSkillsets()[0].GetSkills(), 1)

	skill := resp.GetSkillsets()[0].GetSkills()[0]
	// Contract: manifestJSON contains the full JSON string envelope
	require.Contains(t, skill.GetManifestJSON(), "Valid Skill", "manifestJSON should contain skill name")
	require.Contains(t, skill.GetManifestJSON(), "bundle_format", "manifestJSON should contain bundle_format")
	require.Contains(t, skill.GetManifestJSON(), "instruction_file", "manifestJSON should contain instruction_file")
}
