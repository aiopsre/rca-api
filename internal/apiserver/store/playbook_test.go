package store

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

func newPlaybookTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	ResetForTest()
	t.Cleanup(ResetForTest)
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.AlertingPolicyM{}, &model.PlaybookM{}))
	return db
}

func TestPlaybookStore_CRUD(t *testing.T) {
	db := newPlaybookTestDB(t)
	s := NewStore(db)
	ctx := context.Background()

	obj := &model.PlaybookM{
		Name:        "test-playbook",
		Description: ptrString("test description"),
		Version:     1,
		ConfigJSON:  `{"version":"1.0"}`,
		Active:      false,
		CreatedBy:   "admin",
		UpdatedBy:   ptrString("admin"),
	}

	err := s.Playbook().Create(ctx, obj)
	require.NoError(t, err)
	require.Greater(t, obj.ID, int64(0))

	getObj, err := s.Playbook().Get(ctx, where.T(ctx).F("id", obj.ID))
	require.NoError(t, err)
	require.Equal(t, "test-playbook", getObj.Name)
	require.Equal(t, "test description", *getObj.Description)

	total, list, err := s.Playbook().List(ctx, where.T(ctx))
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, list, 1)
	require.Equal(t, obj.ID, list[0].ID)

	obj.Description = ptrString("updated description")
	err = s.Playbook().Update(ctx, obj)
	require.NoError(t, err)

	updatedObj, err := s.Playbook().Get(ctx, where.T(ctx).F("id", obj.ID))
	require.NoError(t, err)
	require.Equal(t, "updated description", *updatedObj.Description)

	err = s.Playbook().Delete(ctx, where.T(ctx).F("id", obj.ID))
	require.NoError(t, err)

	_, err = s.Playbook().Get(ctx, where.T(ctx).F("id", obj.ID))
	require.Error(t, err)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestPlaybookStore_GetActive(t *testing.T) {
	db := newPlaybookTestDB(t)
	s := NewStore(db)
	ctx := context.Background()

	_, err := s.Playbook().GetActive(ctx)
	require.Error(t, err)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)

	obj1 := &model.PlaybookM{
		Name:       "playbook-1",
		Version:    1,
		ConfigJSON: `{"version":"1.0"}`,
		Active:     false,
		CreatedBy:  "admin",
	}
	require.NoError(t, s.Playbook().Create(ctx, obj1))

	obj2 := &model.PlaybookM{
		Name:       "playbook-2",
		Version:    1,
		ConfigJSON: `{"version":"1.0"}`,
		Active:     true,
		CreatedBy:  "admin",
	}
	require.NoError(t, s.Playbook().Create(ctx, obj2))

	activeObj, err := s.Playbook().GetActive(ctx)
	require.NoError(t, err)
	require.Equal(t, "playbook-2", activeObj.Name)
	require.True(t, activeObj.Active)
}

func TestPlaybookStore_Activate(t *testing.T) {
	db := newPlaybookTestDB(t)
	s := NewStore(db)
	ctx := context.Background()

	obj1 := &model.PlaybookM{
		Name:       "playbook-1",
		Version:    1,
		ConfigJSON: `{"version":"1.0"}`,
		Active:     true,
		CreatedBy:  "admin",
	}
	require.NoError(t, s.Playbook().Create(ctx, obj1))

	obj2 := &model.PlaybookM{
		Name:       "playbook-2",
		Version:    1,
		ConfigJSON: `{"version":"1.0"}`,
		Active:     false,
		CreatedBy:  "admin",
	}
	require.NoError(t, s.Playbook().Create(ctx, obj2))

	operator := "test-operator"
	err := s.Playbook().Activate(ctx, obj2.ID, operator)
	require.NoError(t, err)

	activeObj1, err := s.Playbook().Get(ctx, where.T(ctx).F("id", obj1.ID))
	require.NoError(t, err)
	require.False(t, activeObj1.Active)

	activeObj2, err := s.Playbook().Get(ctx, where.T(ctx).F("id", obj2.ID))
	require.NoError(t, err)
	require.True(t, activeObj2.Active)
	require.NotNil(t, activeObj2.ActivatedAt)
	require.Equal(t, operator, *activeObj2.ActivatedBy)
	require.Equal(t, operator, *activeObj2.UpdatedBy)
}

func TestPlaybookStore_Deactivate(t *testing.T) {
	db := newPlaybookTestDB(t)
	s := NewStore(db)
	ctx := context.Background()

	obj := &model.PlaybookM{
		Name:       "playbook-1",
		Version:    1,
		ConfigJSON: `{"version":"1.0"}`,
		Active:     true,
		CreatedBy:  "admin",
	}
	require.NoError(t, s.Playbook().Create(ctx, obj))

	err := s.Playbook().Deactivate(ctx, obj.ID)
	require.NoError(t, err)

	deactivatedObj, err := s.Playbook().Get(ctx, where.T(ctx).F("id", obj.ID))
	require.NoError(t, err)
	require.False(t, deactivatedObj.Active)
}

func TestPlaybookStore_VersionManagement(t *testing.T) {
	db := newPlaybookTestDB(t)
	s := NewStore(db)
	ctx := context.Background()

	for i := 1; i <= 5; i++ {
		obj := &model.PlaybookM{
			Name:            "versioned-playbook",
			Version:         i,
			PreviousVersion: ptrInt(i - 1),
			ConfigJSON:      `{"version": "` + string(rune('0'+i)) + `.0"}`,
			Active:          false,
			CreatedBy:       "admin",
			UpdatedBy:       ptrString("admin"),
		}
		require.NoError(t, s.Playbook().Create(ctx, obj))
	}

	total, list, err := s.Playbook().List(ctx, where.T(ctx).F("name", "versioned-playbook"))
	require.NoError(t, err)
	require.Equal(t, int64(5), total)
	require.Len(t, list, 5)

	for i, playbook := range list {
		require.Equal(t, 5-i, playbook.Version)
		require.NotNil(t, playbook.PreviousVersion)
		require.Equal(t, playbook.Version-1, *playbook.PreviousVersion)
	}
}

func TestPlaybookStore_ActivateTransactionConsistency(t *testing.T) {
	db := newPlaybookTestDB(t)
	s := NewStore(db)
	ctx := context.Background()

	obj1 := &model.PlaybookM{
		Name:       "playbook-1",
		Version:    1,
		ConfigJSON: `{"version":"1.0"}`,
		Active:     true,
		CreatedBy:  "admin",
	}
	require.NoError(t, s.Playbook().Create(ctx, obj1))

	obj2 := &model.PlaybookM{
		Name:       "playbook-2",
		Version:    1,
		ConfigJSON: `{"version":"1.0"}`,
		Active:     false,
		CreatedBy:  "admin",
	}
	require.NoError(t, s.Playbook().Create(ctx, obj2))

	activeCountBefore := countActivePlaybooks(t, s, ctx)
	require.Equal(t, int64(1), activeCountBefore)

	err := s.Playbook().Activate(ctx, obj2.ID, "operator")
	require.NoError(t, err)

	activeObj1, _ := s.Playbook().Get(ctx, where.T(ctx).F("id", obj1.ID))
	require.False(t, activeObj1.Active, "Previously active playbook should be deactivated")

	activeObj2, _ := s.Playbook().Get(ctx, where.T(ctx).F("id", obj2.ID))
	require.True(t, activeObj2.Active, "New playbook should be activated")

	activeCountAfter := countActivePlaybooks(t, s, ctx)
	require.Equal(t, int64(1), activeCountAfter, "Should have exactly one active playbook")
}

func TestPlaybookStore_ListFilters(t *testing.T) {
	db := newPlaybookTestDB(t)
	s := NewStore(db)
	ctx := context.Background()

	playbooks := []*model.PlaybookM{
		{Name: "playbook-alpha", Version: 1, ConfigJSON: `{"version":"1.0"}`, Active: true, CreatedBy: "admin"},
		{Name: "playbook-beta", Version: 1, ConfigJSON: `{"version":"1.0"}`, Active: false, CreatedBy: "admin"},
		{Name: "playbook-gamma", Version: 1, ConfigJSON: `{"version":"1.0"}`, Active: false, CreatedBy: "admin"},
	}

	for _, p := range playbooks {
		require.NoError(t, s.Playbook().Create(ctx, p))
	}

	total, list, err := s.Playbook().List(ctx, where.T(ctx).F("active", true))
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, list, 1)
	require.Equal(t, "playbook-alpha", list[0].Name)

	total, list, err = s.Playbook().List(ctx, where.T(ctx).F("active", false))
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.Len(t, list, 2)

	total, list, err = s.Playbook().List(ctx, where.T(ctx).F("name", "playbook-beta"))
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, list, 1)
	require.Equal(t, "playbook-beta", list[0].Name)
}

func countActivePlaybooks(t *testing.T, s IStore, ctx context.Context) int64 {
	t.Helper()
	total, _, err := s.Playbook().List(ctx, where.T(ctx).F("active", true))
	require.NoError(t, err)
	return total
}
