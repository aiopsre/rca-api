package alerting_policy

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
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

		resp, err := biz.Create(ctx, &CreateRequest{
			Name:        "test-policy",
			Description: ptrString("test description"),
			Config: &AlertingPolicyConfig{
				Version: 1,
			},
			CreatedBy: "admin",
		})

		require.NoError(t, err)
		require.NotNil(t, resp.AlertingPolicy)
		require.Equal(t, "test-policy", resp.AlertingPolicy.Name)
		require.Equal(t, 1, resp.AlertingPolicy.Version)
		require.False(t, resp.AlertingPolicy.Active)
		require.Equal(t, "admin", resp.AlertingPolicy.CreatedBy)
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

		_, err := biz.Create(ctx, &CreateRequest{
			Name:      "",
			Config:    &AlertingPolicyConfig{Version: 1},
			CreatedBy: "admin",
		})
		require.Error(t, err)
	})

	t.Run("nil config", func(t *testing.T) {
		biz, _ := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		_, err := biz.Create(ctx, &CreateRequest{
			Name:      "test",
			Config:    nil,
			CreatedBy: "admin",
		})
		require.Error(t, err)
	})

	t.Run("duplicate name", func(t *testing.T) {
		biz, _ := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		_, err := biz.Create(ctx, &CreateRequest{
			Name:      "duplicate",
			Config:    &AlertingPolicyConfig{Version: 1},
			CreatedBy: "admin",
		})
		require.NoError(t, err)

		_, err = biz.Create(ctx, &CreateRequest{
			Name:      "duplicate",
			Config:    &AlertingPolicyConfig{Version: 1},
			CreatedBy: "admin",
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

		resp, err := biz.Get(ctx, obj.ID)
		require.NoError(t, err)
		require.NotNil(t, resp.AlertingPolicy)
		require.Equal(t, "test-policy", resp.AlertingPolicy.Name)
	})

	t.Run("invalid id", func(t *testing.T) {
		biz, _ := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		_, err := biz.Get(ctx, 0)
		require.Error(t, err)
	})

	t.Run("not found", func(t *testing.T) {
		biz, _ := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		_, err := biz.Get(ctx, 999)
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

		resp, err := biz.List(ctx, &ListRequest{
			Offset: 0,
			Limit:  ptrInt64(10),
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

		resp, err := biz.List(ctx, &ListRequest{
			Name:   ptrString("specific-policy"),
			Offset: 0,
			Limit:  ptrInt64(10),
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

		resp, err := biz.List(ctx, &ListRequest{
			Active: ptrBool(true),
			Offset: 0,
			Limit:  ptrInt64(10),
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

		resp, err := biz.Update(ctx, obj.ID, &UpdateRequest{
			Name:        ptrString("updated-policy"),
			Description: ptrString("updated description"),
			UpdatedBy:   "admin",
		})

		require.NoError(t, err)
		require.Equal(t, "updated-policy", resp.AlertingPolicy.Name)
		require.Equal(t, 2, resp.AlertingPolicy.Version)
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

		_, err := biz.Update(ctx, obj.ID, &UpdateRequest{
			Name:            ptrString("updated-policy"),
			ExpectedVersion: ptrInt(999),
			UpdatedBy:       "admin",
		})
		require.Error(t, err)
	})

	t.Run("not found", func(t *testing.T) {
		biz, _ := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		_, err := biz.Update(ctx, 999, &UpdateRequest{
			Name:      ptrString("updated"),
			UpdatedBy: "admin",
		})
		require.Error(t, err)
	})

	t.Run("nil request", func(t *testing.T) {
		biz, _ := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		_, err := biz.Update(ctx, 1, nil)
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

		err := biz.Delete(ctx, obj.ID)
		require.NoError(t, err)

		_, err = biz.Get(ctx, obj.ID)
		require.Error(t, err)
	})

	t.Run("invalid id", func(t *testing.T) {
		biz, _ := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		err := biz.Delete(ctx, 0)
		require.Error(t, err)
	})

	t.Run("not found", func(t *testing.T) {
		biz, _ := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		err := biz.Delete(ctx, 999)
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

		err := biz.Activate(ctx, obj.ID, "operator")
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

		err := biz.Activate(ctx, obj.ID, "operator")
		require.NoError(t, err)
	})

	t.Run("invalid id", func(t *testing.T) {
		biz, _ := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		err := biz.Activate(ctx, 0, "operator")
		require.Error(t, err)
	})

	t.Run("not found", func(t *testing.T) {
		biz, _ := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		err := biz.Activate(ctx, 999, "operator")
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

		err := biz.Deactivate(ctx, obj.ID)
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

		err := biz.Deactivate(ctx, obj.ID)
		require.NoError(t, err)
	})

	t.Run("invalid id", func(t *testing.T) {
		biz, _ := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		err := biz.Deactivate(ctx, 0)
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

		err = biz.Rollback(ctx, current.ID, 1, "operator")
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

		err := biz.Rollback(ctx, obj.ID, 0, "operator")
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

		err := biz.Rollback(ctx, obj.ID, 1, "operator")
		require.Error(t, err)
	})

	t.Run("not found", func(t *testing.T) {
		biz, _ := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		err := biz.Rollback(ctx, 999, 1, "operator")
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

		resp, err := biz.GetActive(ctx)
		require.NoError(t, err)
		require.NotNil(t, resp.AlertingPolicy)
		require.Equal(t, "active-policy", resp.AlertingPolicy.Name)
		require.True(t, resp.AlertingPolicy.Active)
	})

	t.Run("not found", func(t *testing.T) {
		biz, _ := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		_, err := biz.GetActive(ctx)
		require.Error(t, err)
	})
}

func TestAlertingPolicyBiz_AuditFieldsAutoPopulated(t *testing.T) {
	t.Run("create auto populates created_by", func(t *testing.T) {
		biz, _ := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		resp, err := biz.Create(ctx, &CreateRequest{
			Name:      "audit-test",
			Config:    &AlertingPolicyConfig{Version: 1},
			CreatedBy: "test-user",
		})
		require.NoError(t, err)
		require.Equal(t, "test-user", resp.AlertingPolicy.CreatedBy)
		require.Equal(t, "test-user", *resp.AlertingPolicy.UpdatedBy)
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

		resp, err := biz.Update(ctx, obj.ID, &UpdateRequest{
			Name:      ptrString("updated-audit-test"),
			UpdatedBy: "updater",
		})
		require.NoError(t, err)
		require.Equal(t, "updater", *resp.AlertingPolicy.UpdatedBy)
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

		err := biz.Activate(ctx, obj.ID, "activator")
		require.NoError(t, err)

		updated, err := s.AlertingPolicy().Get(ctx, where.T(ctx).F("id", obj.ID))
		require.NoError(t, err)
		require.Equal(t, "activator", *updated.ActivatedBy)
		require.Equal(t, "activator", *updated.UpdatedBy)
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
			resp, err := biz.Update(ctx, obj.ID, &UpdateRequest{
				Description: ptrString("version " + string(rune('0'+i))),
				UpdatedBy:   "admin",
			})
			require.NoError(t, err)
			require.Equal(t, i, resp.AlertingPolicy.Version)
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

		resp, err := biz.Update(ctx, obj.ID, &UpdateRequest{
			Description: ptrString("v2"),
			UpdatedBy:   "admin",
		})
		require.NoError(t, err)
		require.Equal(t, 2, resp.AlertingPolicy.Version)
		require.NotNil(t, resp.AlertingPolicy.PreviousVersion)
		require.Equal(t, 1, *resp.AlertingPolicy.PreviousVersion)
	})

	t.Run("rename keeps lineage snapshot and rollback restores old metadata", func(t *testing.T) {
		biz, s := newTestAlertingPolicyBiz(t)
		ctx := context.Background()

		createResp, err := biz.Create(ctx, &CreateRequest{
			Name:        "lineage-policy-v1",
			Description: ptrString("first policy description"),
			Config: &AlertingPolicyConfig{
				Version: 1,
			},
			CreatedBy: "admin",
		})
		require.NoError(t, err)
		require.NotEmpty(t, createResp.AlertingPolicy.LineageID)

		oldConfigJSON := createResp.AlertingPolicy.ConfigJSON

		updateResp, err := biz.Update(ctx, createResp.AlertingPolicy.ID, &UpdateRequest{
			Name:        ptrString("lineage-policy-v2"),
			Description: ptrString("second policy description"),
			Config: &AlertingPolicyConfig{
				Version: 2,
			},
			UpdatedBy: "editor",
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

		require.NoError(t, biz.Activate(ctx, updateResp.AlertingPolicy.ID, "operator"))
		require.NoError(t, biz.Rollback(ctx, updateResp.AlertingPolicy.ID, 1, "rollbacker"))

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
		require.Equal(t, "rollbacker", *rolledBack.ActivatedBy)

		activeCount, activeList, err := s.AlertingPolicy().List(ctx, where.T(ctx).F("active", true))
		require.NoError(t, err)
		require.Equal(t, int64(1), activeCount)
		require.Len(t, activeList, 1)
		require.Equal(t, rolledBack.ID, activeList[0].ID)
	})
}

func ptrString(v string) *string {
	return &v
}

func ptrInt(v int) *int {
	return &v
}

func ptrInt64(v int64) *int64 {
	return &v
}

func ptrBool(v bool) *bool {
	return &v
}
