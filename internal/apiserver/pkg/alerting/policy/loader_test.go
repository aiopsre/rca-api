package policy

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
)

func TestLoad_BootstrapsDefaultActivePolicy(t *testing.T) {
	s := newLoaderTestStore(t)

	cfg, source, err := Load(context.Background(), s)
	require.NoError(t, err)
	require.Equal(t, PolicyActiveSourceDynamicDB, source)
	require.Equal(t, DefaultPolicyConfig(), cfg)

	active, err := s.AlertingPolicy().GetActive(context.Background())
	require.NoError(t, err)
	require.Equal(t, bootstrapPolicyName, active.Name)
	require.True(t, active.Active)
}

func TestLoad_UsesActiveDatabasePolicy(t *testing.T) {
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

	loaded, source, err := Load(context.Background(), s)
	require.NoError(t, err)
	require.Equal(t, PolicyActiveSourceDynamicDB, source)
	require.True(t, loaded.Triggers.OnIngest.Rules[0].Action.Run)
	require.Equal(t, "db_rca", loaded.Triggers.OnIngest.Rules[0].Action.Pipeline)
}

func TestLoad_DefaultsWhenPoliciesExistButNoActive(t *testing.T) {
	s := newLoaderTestStore(t)

	cfg := DefaultPolicyConfig()
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)

	inactive := &model.AlertingPolicyM{
		Name:       "inactive-policy",
		LineageID:  "lineage-inactive-policy",
		Version:    1,
		ConfigJSON: string(raw),
		Active:     false,
		CreatedBy:  "tester",
	}
	require.NoError(t, s.AlertingPolicy().Create(context.Background(), inactive))

	loaded, source, err := Load(context.Background(), s)
	require.NoError(t, err)
	require.Equal(t, PolicyActiveSourceDefault, source)
	require.Equal(t, DefaultPolicyConfig(), loaded)
}

func TestSyncRuntimeConfig_UsesDatabasePolicy(t *testing.T) {
	s := newLoaderTestStore(t)

	cfg := DefaultPolicyConfig()
	cfg.Triggers.Scheduled.Rules[0].Action.Run = true
	cfg.Triggers.Scheduled.Rules[0].Action.Pipeline = "scheduled_rca"
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)

	active := &model.AlertingPolicyM{
		Name:       "runtime-policy",
		LineageID:  "lineage-runtime-policy",
		Version:    1,
		ConfigJSON: string(raw),
		Active:     true,
		CreatedBy:  "tester",
	}
	require.NoError(t, s.AlertingPolicy().Create(context.Background(), active))

	old := CurrentRuntimeConfig()
	t.Cleanup(func() {
		SetRuntimeConfig(old)
	})

	require.NoError(t, SyncRuntimeConfig(context.Background(), s))

	runtimeCfg := CurrentRuntimeConfig()
	require.Equal(t, RuleSourceDynamicDB, runtimeCfg.Source)
	require.Equal(t, PolicyActiveSourceDynamicDB, runtimeCfg.ActiveSource)
	require.True(t, runtimeCfg.Policy.Triggers.Scheduled.Rules[0].Action.Run)
	require.Equal(t, "scheduled_rca", runtimeCfg.Policy.Triggers.Scheduled.Rules[0].Action.Pipeline)
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
