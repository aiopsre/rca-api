package policy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
)

func TestLoadPolicy_DefaultWhenPathEmpty(t *testing.T) {
	cfg, source, err := LoadFromYAML("", false)
	require.NoError(t, err)
	require.Equal(t, PolicyActiveSourceDefault, source)
	require.Equal(t, DefaultPolicyConfig(), cfg)
	require.False(t, cfg.Triggers.OnIngest.Rules[0].Action.Run)
	require.False(t, cfg.Triggers.OnEscalation.Rules[0].Action.Run)
	require.False(t, cfg.Triggers.Scheduled.Rules[0].Action.Run)
}

func TestResolveLoadInput_CLIOverridesYAML(t *testing.T) {
	in := ResolveLoadInput(ExternalPolicyOptions{
		Enabled:      true,
		Path:         "/tmp/cli-policy.yaml",
		Strict:       true,
		PathSetByCLI: true,
	})
	require.Equal(t, "/tmp/cli-policy.yaml", in.Path)
	require.Equal(t, RuleSourceCLI, in.Source)
	require.True(t, in.Strict)
}

func TestLoadPolicy_ParseErrorStrictFalseFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid-policy.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: [\n"), 0o600))

	cfg, source, err := LoadFromYAML(path, false)
	require.Error(t, err)
	require.Equal(t, PolicyActiveSourceDefault, source)
	require.Equal(t, DefaultPolicyConfig(), cfg)
}

func TestLoadPolicy_ParseErrorStrictTrueFail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid-policy.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: [\n"), 0o600))

	_, source, err := LoadFromYAML(path, true)
	require.Error(t, err)
	require.Equal(t, PolicyActiveSourceDefault, source)
}

func TestLoadPolicy_DBActivePolicyOverridesFile(t *testing.T) {
	s := newLoaderTestStore(t)

	cfg := DefaultPolicyConfig()
	cfg.Triggers.OnIngest.Rules[0].Action.Run = true
	cfg.Triggers.OnIngest.Rules[0].Action.Pipeline = "db_rca"
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)

	active := &model.AlertingPolicyM{
		Name:       "db-policy",
		LineageID:  "lineage-db-policy",
		Version:    1,
		ConfigJSON: string(raw),
		Active:     true,
		CreatedBy:  "tester",
	}
	require.NoError(t, s.AlertingPolicy().Create(context.Background(), active))

	filePath := filepath.Join(t.TempDir(), "policy.yaml")
	filePolicy := strings.Join([]string{
		"version: 1",
		"triggers:",
		"  on_ingest:",
		"    rules:",
		"      - name: file-rule",
		"        action:",
		"          run: false",
		"          pipeline: file_rca",
	}, "\n")
	require.NoError(t, os.WriteFile(filePath, []byte(filePolicy), 0o600))

	loaded, source, err := Load(context.Background(), s, filePath, false)
	require.NoError(t, err)
	require.Equal(t, "dynamic_db", source)
	require.True(t, loaded.Triggers.OnIngest.Rules[0].Action.Run)
	require.Equal(t, "db_rca", loaded.Triggers.OnIngest.Rules[0].Action.Pipeline)
}

func newLoaderTestStore(t *testing.T) store.IStore {
	t.Helper()
	store.ResetForTest()
	t.Cleanup(store.ResetForTest)

	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.AlertingPolicyM{}))
	return store.NewStore(db)
}
