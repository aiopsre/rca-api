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

func newAlertingPolicyTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	ResetForTest()
	t.Cleanup(ResetForTest)
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.AlertingPolicyM{}))
	return db
}

func TestAlertingPolicyStore_CRUD(t *testing.T) {
	db := newAlertingPolicyTestDB(t)
	s := NewStore(db)
	ctx := context.Background()

	obj := &model.AlertingPolicyM{
		Name:        "test-policy",
		Description: ptrString("test description"),
		Version:     1,
		ConfigJSON:  `{"version":1}`,
		Active:      false,
		CreatedBy:   "admin",
		UpdatedBy:   ptrString("admin"),
	}

	err := s.AlertingPolicy().Create(ctx, obj)
	require.NoError(t, err)
	require.Greater(t, obj.ID, int64(0))

	getObj, err := s.AlertingPolicy().Get(ctx, where.T(ctx).F("id", obj.ID))
	require.NoError(t, err)
	require.Equal(t, "test-policy", getObj.Name)
	require.Equal(t, "test description", *getObj.Description)

	total, list, err := s.AlertingPolicy().List(ctx, where.T(ctx))
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, list, 1)
	require.Equal(t, obj.ID, list[0].ID)

	obj.Description = ptrString("updated description")
	err = s.AlertingPolicy().Update(ctx, obj)
	require.NoError(t, err)

	updatedObj, err := s.AlertingPolicy().Get(ctx, where.T(ctx).F("id", obj.ID))
	require.NoError(t, err)
	require.Equal(t, "updated description", *updatedObj.Description)

	err = s.AlertingPolicy().Delete(ctx, where.T(ctx).F("id", obj.ID))
	require.NoError(t, err)

	_, err = s.AlertingPolicy().Get(ctx, where.T(ctx).F("id", obj.ID))
	require.Error(t, err)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestAlertingPolicyStore_GetActive(t *testing.T) {
	db := newAlertingPolicyTestDB(t)
	s := NewStore(db)
	ctx := context.Background()

	_, err := s.AlertingPolicy().GetActive(ctx)
	require.Error(t, err)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)

	obj1 := &model.AlertingPolicyM{
		Name:       "policy-1",
		Version:    1,
		ConfigJSON: `{"version":1}`,
		Active:     false,
		CreatedBy:  "admin",
	}
	require.NoError(t, s.AlertingPolicy().Create(ctx, obj1))

	obj2 := &model.AlertingPolicyM{
		Name:       "policy-2",
		Version:    1,
		ConfigJSON: `{"version":1}`,
		Active:     true,
		CreatedBy:  "admin",
	}
	require.NoError(t, s.AlertingPolicy().Create(ctx, obj2))

	activeObj, err := s.AlertingPolicy().GetActive(ctx)
	require.NoError(t, err)
	require.Equal(t, "policy-2", activeObj.Name)
	require.True(t, activeObj.Active)
}

func TestAlertingPolicyStore_Activate(t *testing.T) {
	db := newAlertingPolicyTestDB(t)
	s := NewStore(db)
	ctx := context.Background()

	obj1 := &model.AlertingPolicyM{
		Name:       "policy-1",
		Version:    1,
		ConfigJSON: `{"version":1}`,
		Active:     true,
		CreatedBy:  "admin",
	}
	require.NoError(t, s.AlertingPolicy().Create(ctx, obj1))

	obj2 := &model.AlertingPolicyM{
		Name:       "policy-2",
		Version:    1,
		ConfigJSON: `{"version":1}`,
		Active:     false,
		CreatedBy:  "admin",
	}
	require.NoError(t, s.AlertingPolicy().Create(ctx, obj2))

	operator := "test-operator"
	err := s.AlertingPolicy().Activate(ctx, obj2.ID, operator)
	require.NoError(t, err)

	activeObj1, err := s.AlertingPolicy().Get(ctx, where.T(ctx).F("id", obj1.ID))
	require.NoError(t, err)
	require.False(t, activeObj1.Active)

	activeObj2, err := s.AlertingPolicy().Get(ctx, where.T(ctx).F("id", obj2.ID))
	require.NoError(t, err)
	require.True(t, activeObj2.Active)
	require.NotNil(t, activeObj2.ActivatedAt)
	require.Equal(t, operator, *activeObj2.ActivatedBy)
	require.Equal(t, operator, *activeObj2.UpdatedBy)
}

func TestAlertingPolicyStore_Deactivate(t *testing.T) {
	db := newAlertingPolicyTestDB(t)
	s := NewStore(db)
	ctx := context.Background()

	obj := &model.AlertingPolicyM{
		Name:       "policy-1",
		Version:    1,
		ConfigJSON: `{"version":1}`,
		Active:     true,
		CreatedBy:  "admin",
	}
	require.NoError(t, s.AlertingPolicy().Create(ctx, obj))

	err := s.AlertingPolicy().Deactivate(ctx, obj.ID)
	require.NoError(t, err)

	deactivatedObj, err := s.AlertingPolicy().Get(ctx, where.T(ctx).F("id", obj.ID))
	require.NoError(t, err)
	require.False(t, deactivatedObj.Active)
}

func TestAlertingPolicyStore_VersionManagement(t *testing.T) {
	db := newAlertingPolicyTestDB(t)
	s := NewStore(db)
	ctx := context.Background()

	for i := 1; i <= 5; i++ {
		obj := &model.AlertingPolicyM{
			Name:            "versioned-policy",
			Version:         i,
			PreviousVersion: ptrInt(i - 1),
			ConfigJSON:      `{"version": ` + string(rune('0'+i)) + `}`,
			Active:          false,
			CreatedBy:       "admin",
			UpdatedBy:       ptrString("admin"),
		}
		require.NoError(t, s.AlertingPolicy().Create(ctx, obj))
	}

	total, list, err := s.AlertingPolicy().List(ctx, where.T(ctx).F("name", "versioned-policy"))
	require.NoError(t, err)
	require.Equal(t, int64(5), total)
	require.Len(t, list, 5)

	expectedVersions := []int{5, 4, 3, 2, 1}
	for i, policy := range list {
		require.Equal(t, expectedVersions[i], policy.Version)
	}
}

func TestAlertingPolicyStore_ActivateTransactionConsistency(t *testing.T) {
	db := newAlertingPolicyTestDB(t)
	s := NewStore(db)
	ctx := context.Background()

	obj1 := &model.AlertingPolicyM{
		Name:       "policy-1",
		Version:    1,
		ConfigJSON: `{"version":1}`,
		Active:     true,
		CreatedBy:  "admin",
	}
	require.NoError(t, s.AlertingPolicy().Create(ctx, obj1))

	obj2 := &model.AlertingPolicyM{
		Name:       "policy-2",
		Version:    1,
		ConfigJSON: `{"version":1}`,
		Active:     false,
		CreatedBy:  "admin",
	}
	require.NoError(t, s.AlertingPolicy().Create(ctx, obj2))

	activeCountBefore := countActivePolicies(t, s, ctx)
	require.Equal(t, int64(1), activeCountBefore)

	err := s.AlertingPolicy().Activate(ctx, obj2.ID, "operator")
	require.NoError(t, err)

	// Refresh objects to get updated values
	refreshedObj1, err := s.AlertingPolicy().Get(ctx, where.T(ctx).F("id", obj1.ID))
	require.NoError(t, err)
	require.False(t, refreshedObj1.Active, "Previously active policy should be deactivated")

	refreshedObj2, err := s.AlertingPolicy().Get(ctx, where.T(ctx).F("id", obj2.ID))
	require.NoError(t, err)
	require.True(t, refreshedObj2.Active, "New policy should be activated")

	activeCountAfter := countActivePolicies(t, s, ctx)
	require.Equal(t, int64(1), activeCountAfter, "Should have exactly one active policy")
}

func TestAlertingPolicyStore_ListFilters(t *testing.T) {
	db := newAlertingPolicyTestDB(t)
	s := NewStore(db)
	ctx := context.Background()

	policies := []*model.AlertingPolicyM{
		{Name: "policy-alpha", Version: 1, ConfigJSON: `{"version":1}`, Active: true, CreatedBy: "admin"},
		{Name: "policy-beta", Version: 1, ConfigJSON: `{"version":1}`, Active: false, CreatedBy: "admin"},
		{Name: "policy-gamma", Version: 1, ConfigJSON: `{"version":1}`, Active: false, CreatedBy: "admin"},
	}

	for _, p := range policies {
		require.NoError(t, s.AlertingPolicy().Create(ctx, p))
	}

	total, list, err := s.AlertingPolicy().List(ctx, where.T(ctx).F("active", true))
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, list, 1)
	require.Equal(t, "policy-alpha", list[0].Name)

	total, list, err = s.AlertingPolicy().List(ctx, where.T(ctx).F("active", false))
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.Len(t, list, 2)

	total, list, err = s.AlertingPolicy().List(ctx, where.T(ctx).F("name", "policy-beta"))
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, list, 1)
	require.Equal(t, "policy-beta", list[0].Name)
}

func countActivePolicies(t *testing.T, s IStore, ctx context.Context) int64 {
	t.Helper()
	total, _, err := s.AlertingPolicy().List(ctx, where.T(ctx).F("active", true))
	require.NoError(t, err)
	return total
}

func ptrString(v string) *string {
	return &v
}

func ptrInt(v int) *int {
	return &v
}
