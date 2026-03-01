package alerting_policy

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	alertingruntime "github.com/aiopsre/rca-api/internal/apiserver/pkg/alerting/policy"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

func newAlertingPolicyBizTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.AlertingPolicyM{}))
	return db
}

func newTestAlertingPolicyBiz(t *testing.T) (*alertingPolicyBiz, store.IStore) {
	t.Helper()
	store.ResetForTest()
	t.Cleanup(store.ResetForTest)
	db := newAlertingPolicyBizTestDB(t)
	s := store.NewStore(db)
	return New(s), s
}

func TestAlertingPolicyBiz_Create(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		biz, _ := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		desc := "test description"
		resp, err := biz.Create(ctx, &v1.CreateAlertingPolicyRequest{
			Name:        "test-policy",
			Description: &desc,
			ConfigJSON:  `{"version":1}`,
		})

		require.NoError(t, err)
		require.NotNil(t, resp.AlertingPolicy)
		require.Equal(t, "test-policy", resp.AlertingPolicy.Name)
		require.Equal(t, int32(1), resp.AlertingPolicy.Version)
		require.False(t, resp.AlertingPolicy.Active)
	})

	t.Run("nil request", func(t *testing.T) {
		biz, _ := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		_, err := biz.Create(ctx, nil)
		require.Error(t, err)
	})

	t.Run("empty name", func(t *testing.T) {
		biz, _ := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		_, err := biz.Create(ctx, &v1.CreateAlertingPolicyRequest{
			Name:       "",
			ConfigJSON: `{"version":1}`,
		})
		require.Error(t, err)
	})

	t.Run("empty config", func(t *testing.T) {
		biz, _ := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		_, err := biz.Create(ctx, &v1.CreateAlertingPolicyRequest{
			Name:       "test",
			ConfigJSON: "",
		})
		require.Error(t, err)
	})

	t.Run("duplicate name", func(t *testing.T) {
		biz, _ := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		_, err := biz.Create(ctx, &v1.CreateAlertingPolicyRequest{
			Name:       "duplicate",
			ConfigJSON: `{"version":1}`,
		})
		require.NoError(t, err)

		_, err = biz.Create(ctx, &v1.CreateAlertingPolicyRequest{
			Name:       "duplicate",
			ConfigJSON: `{"version":1}`,
		})
		require.Error(t, err)
	})
}

func TestAlertingPolicyBiz_Get(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		biz, s := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		obj := &model.AlertingPolicyM{
			Name:       "test-policy",
			Version:    1,
			ConfigJSON: `{"version":1}`,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.AlertingPolicy().Create(ctx, obj))

		resp, err := biz.Get(ctx, &v1.GetAlertingPolicyRequest{Id: obj.ID})
		require.NoError(t, err)
		require.NotNil(t, resp.AlertingPolicy)
		require.Equal(t, "test-policy", resp.AlertingPolicy.Name)
	})

	t.Run("invalid id", func(t *testing.T) {
		biz, _ := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		_, err := biz.Get(ctx, &v1.GetAlertingPolicyRequest{Id: 0})
		require.Error(t, err)
	})

	t.Run("not found", func(t *testing.T) {
		biz, _ := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		_, err := biz.Get(ctx, &v1.GetAlertingPolicyRequest{Id: 999})
		require.Error(t, err)
	})
}

func TestAlertingPolicyBiz_List(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		biz, s := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		for i := 1; i <= 5; i++ {
			obj := &model.AlertingPolicyM{
				Name:       "policy-" + string(rune('0'+i)),
				Version:    1,
				ConfigJSON: `{"version":1}`,
				Active:     i%2 == 0,
				CreatedBy:  "admin",
			}
			require.NoError(t, s.AlertingPolicy().Create(ctx, obj))
		}

		limit := int64(10)
		resp, err := biz.List(ctx, &v1.ListAlertingPoliciesRequest{
			Offset: 0,
			Limit:  limit,
		})
		require.NoError(t, err)
		require.Equal(t, int64(5), resp.TotalCount)
		require.Len(t, resp.AlertingPolicies, 5)
	})

	t.Run("filter by name", func(t *testing.T) {
		biz, s := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		obj := &model.AlertingPolicyM{
			Name:       "specific-policy",
			Version:    1,
			ConfigJSON: `{"version":1}`,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.AlertingPolicy().Create(ctx, obj))

		name := "specific-policy"
		limit := int64(10)
		resp, err := biz.List(ctx, &v1.ListAlertingPoliciesRequest{
			Name:   &name,
			Offset: 0,
			Limit:  limit,
		})
		require.NoError(t, err)
		require.Equal(t, int64(1), resp.TotalCount)
		require.Len(t, resp.AlertingPolicies, 1)
	})

	t.Run("filter by active", func(t *testing.T) {
		biz, s := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		for i := 1; i <= 3; i++ {
			obj := &model.AlertingPolicyM{
				Name:       "policy-" + string(rune('0'+i)),
				Version:    1,
				ConfigJSON: `{"version":1}`,
				Active:     i%2 == 0,
				CreatedBy:  "admin",
			}
			require.NoError(t, s.AlertingPolicy().Create(ctx, obj))
		}

		active := true
		limit := int64(10)
		resp, err := biz.List(ctx, &v1.ListAlertingPoliciesRequest{
			Active: &active,
			Offset: 0,
			Limit:  limit,
		})
		require.NoError(t, err)
		require.Equal(t, int64(1), resp.TotalCount)
	})

	t.Run("default limit", func(t *testing.T) {
		biz, _ := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		resp, err := biz.List(ctx, nil)
		require.NoError(t, err)
		require.Equal(t, int64(0), resp.TotalCount)
	})
}

func TestAlertingPolicyBiz_Update(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		biz, s := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		obj := &model.AlertingPolicyM{
			Name:       "test-policy",
			Version:    1,
			ConfigJSON: `{"version":1}`,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.AlertingPolicy().Create(ctx, obj))

		name := "updated-policy"
		desc := "updated description"
		resp, err := biz.Update(ctx, &v1.UpdateAlertingPolicyRequest{
			Id:          obj.ID,
			Name:        &name,
			Description: &desc,
		})

		require.NoError(t, err)
		require.Equal(t, "updated-policy", resp.AlertingPolicy.Name)
		require.Equal(t, int32(2), resp.AlertingPolicy.Version)
		require.Equal(t, "updated description", *resp.AlertingPolicy.Description)
	})

	t.Run("version mismatch", func(t *testing.T) {
		biz, s := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		obj := &model.AlertingPolicyM{
			Name:       "test-policy",
			Version:    1,
			ConfigJSON: `{"version":1}`,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.AlertingPolicy().Create(ctx, obj))

		name := "updated-policy"
		expectedVer := int32(999)
		_, err := biz.Update(ctx, &v1.UpdateAlertingPolicyRequest{
			Id:              obj.ID,
			Name:            &name,
			ExpectedVersion: &expectedVer,
		})
		require.Error(t, err)
	})

	t.Run("not found", func(t *testing.T) {
		biz, _ := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		name := "updated"
		_, err := biz.Update(ctx, &v1.UpdateAlertingPolicyRequest{
			Id:   999,
			Name: &name,
		})
		require.Error(t, err)
	})

	t.Run("nil request", func(t *testing.T) {
		biz, _ := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		_, err := biz.Update(ctx, nil)
		require.Error(t, err)
	})
}

func TestAlertingPolicyBiz_Delete(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		biz, s := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		obj := &model.AlertingPolicyM{
			Name:       "test-policy",
			Version:    1,
			ConfigJSON: `{"version":1}`,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.AlertingPolicy().Create(ctx, obj))

		_, err := biz.Delete(ctx, &v1.DeleteAlertingPolicyRequest{Id: obj.ID})
		require.NoError(t, err)

		_, err = biz.Get(ctx, &v1.GetAlertingPolicyRequest{Id: obj.ID})
		require.Error(t, err)
	})

	t.Run("invalid id", func(t *testing.T) {
		biz, _ := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		_, err := biz.Delete(ctx, &v1.DeleteAlertingPolicyRequest{Id: 0})
		require.Error(t, err)
	})

	t.Run("not found", func(t *testing.T) {
		biz, _ := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		_, err := biz.Delete(ctx, &v1.DeleteAlertingPolicyRequest{Id: 999})
		require.Error(t, err)
	})
}

func TestAlertingPolicyBiz_Activate(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		biz, s := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		obj := &model.AlertingPolicyM{
			Name:       "test-policy",
			Version:    1,
			ConfigJSON: `{"version":1}`,
			Active:     false,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.AlertingPolicy().Create(ctx, obj))

		op := "operator"
		_, err := biz.Activate(ctx, &v1.ActivateAlertingPolicyRequest{
			Id:       obj.ID,
			Operator: &op,
		})
		require.NoError(t, err)

		updated, err := s.AlertingPolicy().Get(ctx, where.T(ctx).F("id", obj.ID))
		require.NoError(t, err)
		require.True(t, updated.Active)
		require.Equal(t, "operator", *updated.ActivatedBy)
	})

	t.Run("already active", func(t *testing.T) {
		biz, s := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		obj := &model.AlertingPolicyM{
			Name:       "test-policy",
			Version:    1,
			ConfigJSON: `{"version":1}`,
			Active:     true,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.AlertingPolicy().Create(ctx, obj))

		op := "operator"
		_, err := biz.Activate(ctx, &v1.ActivateAlertingPolicyRequest{
			Id:       obj.ID,
			Operator: &op,
		})
		require.NoError(t, err)
	})

	t.Run("invalid id", func(t *testing.T) {
		biz, _ := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		op := "operator"
		_, err := biz.Activate(ctx, &v1.ActivateAlertingPolicyRequest{
			Id:       0,
			Operator: &op,
		})
		require.Error(t, err)
	})

	t.Run("not found", func(t *testing.T) {
		biz, _ := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		op := "operator"
		_, err := biz.Activate(ctx, &v1.ActivateAlertingPolicyRequest{
			Id:       999,
			Operator: &op,
		})
		require.Error(t, err)
	})
}

func TestAlertingPolicyBiz_Deactivate(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		biz, s := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		obj := &model.AlertingPolicyM{
			Name:       "test-policy",
			Version:    1,
			ConfigJSON: `{"version":1}`,
			Active:     true,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.AlertingPolicy().Create(ctx, obj))

		_, err := biz.Deactivate(ctx, &v1.DeactivateAlertingPolicyRequest{Id: obj.ID})
		require.NoError(t, err)

		updated, err := s.AlertingPolicy().Get(ctx, where.T(ctx).F("id", obj.ID))
		require.NoError(t, err)
		require.False(t, updated.Active)
	})

	t.Run("already inactive", func(t *testing.T) {
		biz, s := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		obj := &model.AlertingPolicyM{
			Name:       "test-policy",
			Version:    1,
			ConfigJSON: `{"version":1}`,
			Active:     false,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.AlertingPolicy().Create(ctx, obj))

		_, err := biz.Deactivate(ctx, &v1.DeactivateAlertingPolicyRequest{Id: obj.ID})
		require.NoError(t, err)
	})

	t.Run("invalid id", func(t *testing.T) {
		biz, _ := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		_, err := biz.Deactivate(ctx, &v1.DeactivateAlertingPolicyRequest{Id: 0})
		require.Error(t, err)
	})
}

func TestAlertingPolicyBiz_Rollback(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		biz, s := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		for i := 1; i <= 3; i++ {
			obj := &model.AlertingPolicyM{
				Name:            "versioned-policy",
				Version:         i,
				PreviousVersion: ptrInt(i - 1),
				ConfigJSON:      `{"version": ` + string(rune('0'+i)) + `}`,
				CreatedBy:       "admin",
			}
			require.NoError(t, s.AlertingPolicy().Create(ctx, obj))
		}

		total, list, err := s.AlertingPolicy().List(ctx, where.T(ctx).F("name", "versioned-policy"))
		require.NoError(t, err)
		require.GreaterOrEqual(t, total, int64(3))
		current := list[0]
		for _, v := range list {
			if v.Version > current.Version {
				current = v
			}
		}
		require.Equal(t, 3, current.Version)

		op := "operator"
		_, err = biz.Rollback(ctx, &v1.RollbackAlertingPolicyRequest{
			Id:       current.ID,
			Version:  1,
			Operator: &op,
		})
		require.NoError(t, err)

		total, list, err = s.AlertingPolicy().List(ctx, where.T(ctx).F("name", "versioned-policy"))
		require.NoError(t, err)
		rolledBack := list[0]
		for _, v := range list {
			if v.Version > rolledBack.Version {
				rolledBack = v
			}
		}
		require.NoError(t, err)
		require.Equal(t, 4, rolledBack.Version)
		require.Equal(t, `{"version": 1}`, rolledBack.ConfigJSON)
		require.Equal(t, "operator", *rolledBack.UpdatedBy)
	})

	t.Run("invalid version", func(t *testing.T) {
		biz, s := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		obj := &model.AlertingPolicyM{
			Name:       "test-policy",
			Version:    1,
			ConfigJSON: `{"version":1}`,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.AlertingPolicy().Create(ctx, obj))

		op := "operator"
		_, err := biz.Rollback(ctx, &v1.RollbackAlertingPolicyRequest{
			Id:       obj.ID,
			Version:  0,
			Operator: &op,
		})
		require.Error(t, err)
	})

	t.Run("version not less than current", func(t *testing.T) {
		biz, s := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		obj := &model.AlertingPolicyM{
			Name:       "test-policy",
			Version:    1,
			ConfigJSON: `{"version":1}`,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.AlertingPolicy().Create(ctx, obj))

		op := "operator"
		_, err := biz.Rollback(ctx, &v1.RollbackAlertingPolicyRequest{
			Id:       obj.ID,
			Version:  1,
			Operator: &op,
		})
		require.Error(t, err)
	})

	t.Run("not found", func(t *testing.T) {
		biz, _ := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		op := "operator"
		_, err := biz.Rollback(ctx, &v1.RollbackAlertingPolicyRequest{
			Id:       999,
			Version:  1,
			Operator: &op,
		})
		require.Error(t, err)
	})
}

func TestAlertingPolicyBiz_GetActive(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		biz, s := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		obj := &model.AlertingPolicyM{
			Name:       "active-policy",
			Version:    1,
			ConfigJSON: `{"version":1}`,
			Active:     true,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.AlertingPolicy().Create(ctx, obj))

		resp, err := biz.GetActive(ctx, &v1.GetActiveAlertingPolicyRequest{})
		require.NoError(t, err)
		require.NotNil(t, resp.AlertingPolicy)
		require.Equal(t, "active-policy", resp.AlertingPolicy.Name)
		require.True(t, resp.AlertingPolicy.Active)
	})

	t.Run("not found", func(t *testing.T) {
		biz, _ := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		_, err := biz.GetActive(ctx, &v1.GetActiveAlertingPolicyRequest{})
		require.Error(t, err)
	})
}

func TestAlertingPolicyBiz_AuditFieldsAutoPopulated(t *testing.T) {
	t.Run("create auto populates created_by", func(t *testing.T) {
		biz, _ := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		resp, err := biz.Create(ctx, &v1.CreateAlertingPolicyRequest{
			Name:       "audit-test",
			ConfigJSON: `{"version":1}`,
		})
		require.NoError(t, err)
		require.Equal(t, "system", resp.AlertingPolicy.CreatedBy)
		require.Equal(t, "system", *resp.AlertingPolicy.UpdatedBy)
	})

	t.Run("update auto populates updated_by", func(t *testing.T) {
		biz, s := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		obj := &model.AlertingPolicyM{
			Name:       "audit-test",
			Version:    1,
			ConfigJSON: `{"version":1}`,
			CreatedBy:  "creator",
		}
		require.NoError(t, s.AlertingPolicy().Create(ctx, obj))

		name := "updated-audit-test"
		resp, err := biz.Update(ctx, &v1.UpdateAlertingPolicyRequest{
			Id:   obj.ID,
			Name: &name,
		})
		require.NoError(t, err)
		require.Equal(t, "system", *resp.AlertingPolicy.UpdatedBy)
	})

	t.Run("activate populates activated_by and updated_by", func(t *testing.T) {
		biz, s := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		obj := &model.AlertingPolicyM{
			Name:       "audit-test",
			Version:    1,
			ConfigJSON: `{"version":1}`,
			Active:     false,
			CreatedBy:  "creator",
		}
		require.NoError(t, s.AlertingPolicy().Create(ctx, obj))

		op := "activator"
		_, err := biz.Activate(ctx, &v1.ActivateAlertingPolicyRequest{
			Id:       obj.ID,
			Operator: &op,
		})
		require.NoError(t, err)

		updated, err := s.AlertingPolicy().Get(ctx, where.T(ctx).F("id", obj.ID))
		require.NoError(t, err)
		require.Equal(t, "activator", *updated.ActivatedBy)
		require.NotNil(t, updated.ActivatedAt)
	})
}

func TestAlertingPolicyBiz_VersionManagement(t *testing.T) {
	t.Run("version increments on update", func(t *testing.T) {
		biz, s := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		obj := &model.AlertingPolicyM{
			Name:       "version-test",
			Version:    1,
			ConfigJSON: `{"version":1}`,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.AlertingPolicy().Create(ctx, obj))

		for i := 2; i <= 5; i++ {
			desc := "version " + string(rune('0'+i))
			resp, err := biz.Update(ctx, &v1.UpdateAlertingPolicyRequest{
				Id:          obj.ID,
				Description: &desc,
			})
			require.NoError(t, err)
			require.Equal(t, int32(i), resp.AlertingPolicy.Version)
		}
	})

	t.Run("previous version tracking", func(t *testing.T) {
		biz, s := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		obj := &model.AlertingPolicyM{
			Name:       "version-test",
			Version:    1,
			ConfigJSON: `{"version":1}`,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.AlertingPolicy().Create(ctx, obj))

		desc := "v2"
		resp, err := biz.Update(ctx, &v1.UpdateAlertingPolicyRequest{
			Id:          obj.ID,
			Description: &desc,
		})
		require.NoError(t, err)
		require.Equal(t, int32(2), resp.AlertingPolicy.Version)
		require.NotNil(t, resp.AlertingPolicy.PreviousVersion)
		require.Equal(t, int32(1), *resp.AlertingPolicy.PreviousVersion)
	})

	t.Run("rename keeps lineage snapshot and rollback restores old metadata", func(t *testing.T) {
		biz, s := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		desc := "first policy description"
		createResp, err := biz.Create(ctx, &v1.CreateAlertingPolicyRequest{
			Name:        "lineage-policy-v1",
			Description: &desc,
			ConfigJSON:  `{"version": 1}`,
		})
		require.NoError(t, err)
		require.NotEmpty(t, createResp.AlertingPolicy.LineageID)

		oldConfigJSON := createResp.AlertingPolicy.ConfigJSON

		name := "lineage-policy-v2"
		desc2 := "second policy description"
		updateResp, err := biz.Update(ctx, &v1.UpdateAlertingPolicyRequest{
			Id:          createResp.AlertingPolicy.Id,
			Name:        &name,
			Description: &desc2,
			ConfigJSON:  ptrString(`{"version": 2}`),
		})
		require.NoError(t, err)
		require.Equal(t, createResp.AlertingPolicy.LineageID, updateResp.AlertingPolicy.LineageID)

		total, versions, err := s.AlertingPolicy().List(ctx, where.T(ctx).F("lineage_id", updateResp.AlertingPolicy.LineageID))
		require.NoError(t, err)
		require.Equal(t, int64(2), total)

		var snapshot *model.AlertingPolicyM
		for _, item := range versions {
			if item.Version == 1 {
				snapshot = item
				break
			}
		}
		require.NotNil(t, snapshot)
		require.Equal(t, "lineage-policy-v1", snapshot.Name)
		require.NotNil(t, snapshot.Description)
		require.Equal(t, "first policy description", *snapshot.Description)
		require.Equal(t, oldConfigJSON, snapshot.ConfigJSON)

		_, err = biz.Activate(ctx, &v1.ActivateAlertingPolicyRequest{
			Id: updateResp.AlertingPolicy.Id,
		})
		require.NoError(t, err)

		_, err = biz.Rollback(ctx, &v1.RollbackAlertingPolicyRequest{
			Id:       updateResp.AlertingPolicy.Id,
			Version:  1,
		})
		require.NoError(t, err)

		total, versions, err = s.AlertingPolicy().List(ctx, where.T(ctx).F("lineage_id", updateResp.AlertingPolicy.LineageID))
		require.NoError(t, err)
		require.Equal(t, int64(3), total)

		var rolledBack *model.AlertingPolicyM
		for _, item := range versions {
			if item.Version == 3 {
				rolledBack = item
				break
			}
		}
		require.NotNil(t, rolledBack)
		require.Equal(t, "lineage-policy-v1", rolledBack.Name)
		require.NotNil(t, rolledBack.Description)
		require.Equal(t, "first policy description", *rolledBack.Description)
		require.Equal(t, oldConfigJSON, rolledBack.ConfigJSON)
		require.True(t, rolledBack.Active)
		require.NotNil(t, rolledBack.ActivatedAt)

		activeCount, activeList, err := s.AlertingPolicy().List(ctx, where.T(ctx).F("active", true))
		require.NoError(t, err)
		require.Equal(t, int64(1), activeCount)
		require.Len(t, activeList, 1)
		require.Equal(t, rolledBack.ID, activeList[0].ID)
	})
}

func TestAlertingPolicyBiz_ActivateRefreshesRuntimeConfig(t *testing.T) {
	biz, s := newTestAlertingPolicyBiz(t)
	ctx := context.Background()

	old := alertingruntime.CurrentRuntimeConfig()
	t.Cleanup(func() {
		alertingruntime.SetRuntimeConfig(old)
	})

	obj := &model.AlertingPolicyM{
		Name:       "runtime-policy",
		LineageID:  "lineage-runtime-policy",
		Version:    1,
		ConfigJSON: `{"version":1,"triggers":{"on_ingest":{"rules":[{"name":"runtime-rule","action":{"run":true,"pipeline":"runtime_rca"}}]}}}`,
		Active:     false,
		CreatedBy:  "admin",
	}
	require.NoError(t, s.AlertingPolicy().Create(ctx, obj))

	_, err := biz.Activate(ctx, &v1.ActivateAlertingPolicyRequest{
		Id: obj.ID,
	})
	require.NoError(t, err)

	runtimeCfg := alertingruntime.CurrentRuntimeConfig()
	require.Equal(t, alertingruntime.RuleSourceDynamicDB, runtimeCfg.Source)
	require.Equal(t, alertingruntime.PolicyActiveSourceDynamicDB, runtimeCfg.ActiveSource)
	require.True(t, runtimeCfg.Policy.Triggers.OnIngest.Rules[0].Action.Run)
	require.Equal(t, "runtime_rca", runtimeCfg.Policy.Triggers.OnIngest.Rules[0].Action.Pipeline)
}

func ptrString(v string) *string {
	return &v
}

func ptrInt(v int) *int {
	return &v
}