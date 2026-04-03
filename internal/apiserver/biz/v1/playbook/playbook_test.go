package playbook

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

func newPlaybookBizTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.PlaybookM{}))
	return db
}

func newTestPlaybookBiz(t *testing.T) (*playbookBiz, store.IStore) {
	t.Helper()
	store.ResetForTest()
	t.Cleanup(store.ResetForTest)
	db := newPlaybookBizTestDB(t)
	s := store.NewStore(db)
	return New(s), s
}

func TestPlaybookBiz_Create(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		biz, _ := newTestPlaybookBiz(t)
		ctx := context.Background()

		desc := "test description"
		resp, err := biz.Create(ctx, &v1.CreatePlaybookRequest{
			Name:        "test-playbook",
			Description: &desc,
			ConfigJSON:  `{"version":"1.0"}`,
		})

		require.NoError(t, err)
		require.NotNil(t, resp.Playbook)
		require.Equal(t, "test-playbook", resp.Playbook.Name)
		require.Equal(t, int32(1), resp.Playbook.Version)
		require.False(t, resp.Playbook.Active)
	})

	t.Run("nil request", func(t *testing.T) {
		biz, _ := newTestPlaybookBiz(t)
		ctx := context.Background()

		_, err := biz.Create(ctx, nil)
		require.Error(t, err)
	})

	t.Run("empty name", func(t *testing.T) {
		biz, _ := newTestPlaybookBiz(t)
		ctx := context.Background()

		_, err := biz.Create(ctx, &v1.CreatePlaybookRequest{
			Name:       "",
			ConfigJSON: `{"version":"1.0"}`,
		})
		require.Error(t, err)
	})

	t.Run("empty config", func(t *testing.T) {
		biz, _ := newTestPlaybookBiz(t)
		ctx := context.Background()

		_, err := biz.Create(ctx, &v1.CreatePlaybookRequest{
			Name:       "test",
			ConfigJSON: "",
		})
		require.Error(t, err)
	})

	t.Run("duplicate name", func(t *testing.T) {
		biz, _ := newTestPlaybookBiz(t)
		ctx := context.Background()

		_, err := biz.Create(ctx, &v1.CreatePlaybookRequest{
			Name:       "duplicate",
			ConfigJSON: `{"version":"1.0"}`,
		})
		require.NoError(t, err)

		_, err = biz.Create(ctx, &v1.CreatePlaybookRequest{
			Name:       "duplicate",
			ConfigJSON: `{"version":"1.0"}`,
		})
		require.Error(t, err)
	})
}

func TestPlaybookBiz_Get(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		biz, s := newTestPlaybookBiz(t)
		ctx := context.Background()

		obj := &model.PlaybookM{
			Name:       "test-playbook",
			Version:    1,
			ConfigJSON: `{"version":"1.0"}`,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.Playbook().Create(ctx, obj))

		resp, err := biz.Get(ctx, &v1.GetPlaybookRequest{Id: obj.ID})
		require.NoError(t, err)
		require.NotNil(t, resp.Playbook)
		require.Equal(t, "test-playbook", resp.Playbook.Name)
	})

	t.Run("invalid id", func(t *testing.T) {
		biz, _ := newTestPlaybookBiz(t)
		ctx := context.Background()

		_, err := biz.Get(ctx, &v1.GetPlaybookRequest{Id: 0})
		require.Error(t, err)
	})

	t.Run("not found", func(t *testing.T) {
		biz, _ := newTestPlaybookBiz(t)
		ctx := context.Background()

		_, err := biz.Get(ctx, &v1.GetPlaybookRequest{Id: 999})
		require.Error(t, err)
	})
}

func TestPlaybookBiz_List(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		biz, s := newTestPlaybookBiz(t)
		ctx := context.Background()

		for i := 1; i <= 5; i++ {
			obj := &model.PlaybookM{
				Name:       "playbook-" + string(rune('0'+i)),
				Version:    1,
				ConfigJSON: `{"version":"1.0"}`,
				Active:     i%2 == 0,
				CreatedBy:  "admin",
			}
			require.NoError(t, s.Playbook().Create(ctx, obj))
		}

		limit := int64(10)
		resp, err := biz.List(ctx, &v1.ListPlaybooksRequest{
			Offset: 0,
			Limit:  limit,
		})
		require.NoError(t, err)
		require.Equal(t, int64(5), resp.TotalCount)
		require.Len(t, resp.Playbooks, 5)
	})

	t.Run("filter by name", func(t *testing.T) {
		biz, s := newTestPlaybookBiz(t)
		ctx := context.Background()

		obj := &model.PlaybookM{
			Name:       "specific-playbook",
			Version:    1,
			ConfigJSON: `{"version":"1.0"}`,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.Playbook().Create(ctx, obj))

		name := "specific-playbook"
		limit := int64(10)
		resp, err := biz.List(ctx, &v1.ListPlaybooksRequest{
			Name:   &name,
			Offset: 0,
			Limit:  limit,
		})
		require.NoError(t, err)
		require.Equal(t, int64(1), resp.TotalCount)
		require.Len(t, resp.Playbooks, 1)
	})

	t.Run("filter by active", func(t *testing.T) {
		biz, s := newTestPlaybookBiz(t)
		ctx := context.Background()

		for i := 1; i <= 3; i++ {
			obj := &model.PlaybookM{
				Name:       "playbook-" + string(rune('0'+i)),
				Version:    1,
				ConfigJSON: `{"version":"1.0"}`,
				Active:     i%2 == 0,
				CreatedBy:  "admin",
			}
			require.NoError(t, s.Playbook().Create(ctx, obj))
		}

		active := true
		limit := int64(10)
		resp, err := biz.List(ctx, &v1.ListPlaybooksRequest{
			Active: &active,
			Offset: 0,
			Limit:  limit,
		})
		require.NoError(t, err)
		require.Equal(t, int64(1), resp.TotalCount)
	})

	t.Run("default limit", func(t *testing.T) {
		biz, _ := newTestPlaybookBiz(t)
		ctx := context.Background()

		resp, err := biz.List(ctx, nil)
		require.NoError(t, err)
		require.Equal(t, int64(0), resp.TotalCount)
	})
}

func TestPlaybookBiz_Update(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		biz, s := newTestPlaybookBiz(t)
		ctx := context.Background()

		obj := &model.PlaybookM{
			Name:       "test-playbook",
			Version:    1,
			ConfigJSON: `{"version":"1.0"}`,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.Playbook().Create(ctx, obj))

		name := "updated-playbook"
		desc := "updated description"
		resp, err := biz.Update(ctx, &v1.UpdatePlaybookRequest{
			Id:          obj.ID,
			Name:        &name,
			Description: &desc,
		})

		require.NoError(t, err)
		require.Equal(t, "updated-playbook", resp.Playbook.Name)
		require.Equal(t, int32(2), resp.Playbook.Version)
		require.Equal(t, "updated description", *resp.Playbook.Description)
	})

	t.Run("version mismatch", func(t *testing.T) {
		biz, s := newTestPlaybookBiz(t)
		ctx := context.Background()

		obj := &model.PlaybookM{
			Name:       "test-playbook",
			Version:    1,
			ConfigJSON: `{"version":"1.0"}`,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.Playbook().Create(ctx, obj))

		name := "updated-playbook"
		expectedVer := int32(999)
		_, err := biz.Update(ctx, &v1.UpdatePlaybookRequest{
			Id:              obj.ID,
			Name:            &name,
			ExpectedVersion: &expectedVer,
		})
		require.Error(t, err)
	})

	t.Run("not found", func(t *testing.T) {
		biz, _ := newTestPlaybookBiz(t)
		ctx := context.Background()

		name := "updated"
		_, err := biz.Update(ctx, &v1.UpdatePlaybookRequest{
			Id:   999,
			Name: &name,
		})
		require.Error(t, err)
	})

	t.Run("nil request", func(t *testing.T) {
		biz, _ := newTestPlaybookBiz(t)
		ctx := context.Background()

		_, err := biz.Update(ctx, nil)
		require.Error(t, err)
	})
}

func TestPlaybookBiz_Delete(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		biz, s := newTestPlaybookBiz(t)
		ctx := context.Background()

		obj := &model.PlaybookM{
			Name:       "test-playbook",
			Version:    1,
			ConfigJSON: `{"version":"1.0"}`,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.Playbook().Create(ctx, obj))

		_, err := biz.Delete(ctx, &v1.DeletePlaybookRequest{Id: obj.ID})
		require.NoError(t, err)

		_, err = biz.Get(ctx, &v1.GetPlaybookRequest{Id: obj.ID})
		require.Error(t, err)
	})

	t.Run("invalid id", func(t *testing.T) {
		biz, _ := newTestPlaybookBiz(t)
		ctx := context.Background()

		_, err := biz.Delete(ctx, &v1.DeletePlaybookRequest{Id: 0})
		require.Error(t, err)
	})

	t.Run("not found", func(t *testing.T) {
		biz, _ := newTestPlaybookBiz(t)
		ctx := context.Background()

		_, err := biz.Delete(ctx, &v1.DeletePlaybookRequest{Id: 999})
		require.Error(t, err)
	})
}

func TestPlaybookBiz_Activate(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		biz, s := newTestPlaybookBiz(t)
		ctx := context.Background()

		obj := &model.PlaybookM{
			Name:       "test-playbook",
			Version:    1,
			ConfigJSON: `{"version":"1.0"}`,
			Active:     false,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.Playbook().Create(ctx, obj))

		op := "operator"
		_, err := biz.Activate(ctx, &v1.ActivatePlaybookRequest{
			Id:       obj.ID,
			Operator: &op,
		})
		require.NoError(t, err)

		updated, err := s.Playbook().Get(ctx, where.T(ctx).F("id", obj.ID))
		require.NoError(t, err)
		require.True(t, updated.Active)
		require.Equal(t, "operator", *updated.ActivatedBy)
	})

	t.Run("already active", func(t *testing.T) {
		biz, s := newTestPlaybookBiz(t)
		ctx := context.Background()

		obj := &model.PlaybookM{
			Name:       "test-playbook",
			Version:    1,
			ConfigJSON: `{"version":"1.0"}`,
			Active:     true,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.Playbook().Create(ctx, obj))

		op := "operator"
		_, err := biz.Activate(ctx, &v1.ActivatePlaybookRequest{
			Id:       obj.ID,
			Operator: &op,
		})
		require.NoError(t, err)
	})

	t.Run("invalid id", func(t *testing.T) {
		biz, _ := newTestPlaybookBiz(t)
		ctx := context.Background()

		op := "operator"
		_, err := biz.Activate(ctx, &v1.ActivatePlaybookRequest{
			Id:       0,
			Operator: &op,
		})
		require.Error(t, err)
	})

	t.Run("not found", func(t *testing.T) {
		biz, _ := newTestPlaybookBiz(t)
		ctx := context.Background()

		op := "operator"
		_, err := biz.Activate(ctx, &v1.ActivatePlaybookRequest{
			Id:       999,
			Operator: &op,
		})
		require.Error(t, err)
	})
}

func TestPlaybookBiz_Deactivate(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		biz, s := newTestPlaybookBiz(t)
		ctx := context.Background()

		obj := &model.PlaybookM{
			Name:       "test-playbook",
			Version:    1,
			ConfigJSON: `{"version":"1.0"}`,
			Active:     true,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.Playbook().Create(ctx, obj))

		_, err := biz.Deactivate(ctx, &v1.DeactivatePlaybookRequest{Id: obj.ID})
		require.NoError(t, err)

		updated, err := s.Playbook().Get(ctx, where.T(ctx).F("id", obj.ID))
		require.NoError(t, err)
		require.False(t, updated.Active)
	})

	t.Run("already inactive", func(t *testing.T) {
		biz, s := newTestPlaybookBiz(t)
		ctx := context.Background()

		obj := &model.PlaybookM{
			Name:       "test-playbook",
			Version:    1,
			ConfigJSON: `{"version":"1.0"}`,
			Active:     false,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.Playbook().Create(ctx, obj))

		_, err := biz.Deactivate(ctx, &v1.DeactivatePlaybookRequest{Id: obj.ID})
		require.NoError(t, err)
	})

	t.Run("invalid id", func(t *testing.T) {
		biz, _ := newTestPlaybookBiz(t)
		ctx := context.Background()

		_, err := biz.Deactivate(ctx, &v1.DeactivatePlaybookRequest{Id: 0})
		require.Error(t, err)
	})
}

func TestPlaybookBiz_Rollback(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		biz, s := newTestPlaybookBiz(t)
		ctx := context.Background()

		for i := 1; i <= 3; i++ {
			obj := &model.PlaybookM{
				Name:            "versioned-playbook",
				Version:         i,
				PreviousVersion: ptrInt(i - 1),
				ConfigJSON:      `{"version": "` + string(rune('0'+i)) + `.0"}`,
				CreatedBy:       "admin",
			}
			require.NoError(t, s.Playbook().Create(ctx, obj))
		}

		total, list, err := s.Playbook().List(ctx, where.T(ctx).F("name", "versioned-playbook"))
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
		_, err = biz.Rollback(ctx, &v1.RollbackPlaybookRequest{
			Id:       current.ID,
			Version:  1,
			Operator: &op,
		})
		require.NoError(t, err)

		total, list, err = s.Playbook().List(ctx, where.T(ctx).F("name", "versioned-playbook"))
		require.NoError(t, err)
		rolledBack := list[0]
		for _, v := range list {
			if v.Version > rolledBack.Version {
				rolledBack = v
			}
		}
		require.Equal(t, 4, rolledBack.Version)
		require.Equal(t, `{"version": "1.0"}`, rolledBack.ConfigJSON)
		require.Equal(t, "operator", *rolledBack.UpdatedBy)
	})

	t.Run("invalid version", func(t *testing.T) {
		biz, s := newTestPlaybookBiz(t)
		ctx := context.Background()

		obj := &model.PlaybookM{
			Name:       "test-playbook",
			Version:    1,
			ConfigJSON: `{"version":"1.0"}`,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.Playbook().Create(ctx, obj))

		op := "operator"
		_, err := biz.Rollback(ctx, &v1.RollbackPlaybookRequest{
			Id:       obj.ID,
			Version:  0,
			Operator: &op,
		})
		require.Error(t, err)
	})

	t.Run("version not less than current", func(t *testing.T) {
		biz, s := newTestPlaybookBiz(t)
		ctx := context.Background()

		obj := &model.PlaybookM{
			Name:       "test-playbook",
			Version:    1,
			ConfigJSON: `{"version":"1.0"}`,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.Playbook().Create(ctx, obj))

		op := "operator"
		_, err := biz.Rollback(ctx, &v1.RollbackPlaybookRequest{
			Id:       obj.ID,
			Version:  1,
			Operator: &op,
		})
		require.Error(t, err)
	})

	t.Run("not found", func(t *testing.T) {
		biz, _ := newTestPlaybookBiz(t)
		ctx := context.Background()

		op := "operator"
		_, err := biz.Rollback(ctx, &v1.RollbackPlaybookRequest{
			Id:       999,
			Version:  1,
			Operator: &op,
		})
		require.Error(t, err)
	})
}

func TestPlaybookBiz_GetActive(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		biz, s := newTestPlaybookBiz(t)
		ctx := context.Background()

		obj := &model.PlaybookM{
			Name:       "active-playbook",
			Version:    1,
			ConfigJSON: `{"version":"1.0"}`,
			Active:     true,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.Playbook().Create(ctx, obj))

		resp, err := biz.GetActive(ctx, &v1.GetActivePlaybookRequest{})
		require.NoError(t, err)
		require.NotNil(t, resp.Playbook)
		require.Equal(t, "active-playbook", resp.Playbook.Name)
		require.True(t, resp.Playbook.Active)
	})

	t.Run("not found", func(t *testing.T) {
		biz, _ := newTestPlaybookBiz(t)
		ctx := context.Background()

		_, err := biz.GetActive(ctx, &v1.GetActivePlaybookRequest{})
		require.Error(t, err)
	})
}

func TestPlaybookBiz_GetActiveForRuntime(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		biz, s := newTestPlaybookBiz(t)
		ctx := context.Background()

		obj := &model.PlaybookM{
			Name:       "active-playbook",
			Version:    1,
			ConfigJSON: `{"version":"1.0","rules":[]}`,
			Active:     true,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.Playbook().Create(ctx, obj))

		config, source, err := biz.GetActiveForRuntime(ctx)
		require.NoError(t, err)
		require.NotNil(t, config)
		require.Equal(t, "dynamic_db", source)
		require.Equal(t, "1.0", config.Version)
	})

	t.Run("not found", func(t *testing.T) {
		biz, _ := newTestPlaybookBiz(t)
		ctx := context.Background()

		config, source, err := biz.GetActiveForRuntime(ctx)
		require.Error(t, err)
		require.Nil(t, config)
		require.Empty(t, source)
	})

	t.Run("inactive playbook", func(t *testing.T) {
		biz, s := newTestPlaybookBiz(t)
		ctx := context.Background()

		obj := &model.PlaybookM{
			Name:       "inactive-playbook",
			Version:    1,
			ConfigJSON: `{"version":"1.0"}`,
			Active:     false,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.Playbook().Create(ctx, obj))

		config, source, err := biz.GetActiveForRuntime(ctx)
		require.Error(t, err)
		require.Nil(t, config)
		require.Empty(t, source)
	})
}

func TestPlaybookBiz_AuditFieldsAutoPopulated(t *testing.T) {
	t.Run("create auto populates created_by", func(t *testing.T) {
		biz, _ := newTestPlaybookBiz(t)
		ctx := context.Background()

		resp, err := biz.Create(ctx, &v1.CreatePlaybookRequest{
			Name:       "audit-test",
			ConfigJSON: `{"version":"1.0"}`,
		})
		require.NoError(t, err)
		require.Equal(t, "system", resp.Playbook.CreatedBy)
		require.Equal(t, "system", *resp.Playbook.UpdatedBy)
	})

	t.Run("update auto populates updated_by", func(t *testing.T) {
		biz, s := newTestPlaybookBiz(t)
		ctx := context.Background()

		obj := &model.PlaybookM{
			Name:       "audit-test",
			Version:    1,
			ConfigJSON: `{"version":"1.0"}`,
			CreatedBy:  "creator",
		}
		require.NoError(t, s.Playbook().Create(ctx, obj))

		name := "updated-audit-test"
		resp, err := biz.Update(ctx, &v1.UpdatePlaybookRequest{
			Id:   obj.ID,
			Name: &name,
		})
		require.NoError(t, err)
		require.Equal(t, "system", *resp.Playbook.UpdatedBy)
	})

	t.Run("activate populates activated_by and updated_by", func(t *testing.T) {
		biz, s := newTestPlaybookBiz(t)
		ctx := context.Background()

		obj := &model.PlaybookM{
			Name:       "audit-test",
			Version:    1,
			ConfigJSON: `{"version":"1.0"}`,
			Active:     false,
			CreatedBy:  "creator",
		}
		require.NoError(t, s.Playbook().Create(ctx, obj))

		op := "activator"
		_, err := biz.Activate(ctx, &v1.ActivatePlaybookRequest{
			Id:       obj.ID,
			Operator: &op,
		})
		require.NoError(t, err)

		updated, err := s.Playbook().Get(ctx, where.T(ctx).F("id", obj.ID))
		require.NoError(t, err)
		require.Equal(t, "activator", *updated.ActivatedBy)
		require.NotNil(t, updated.ActivatedAt)
	})
}

func TestPlaybookBiz_VersionManagement(t *testing.T) {
	t.Run("version increments on update", func(t *testing.T) {
		biz, s := newTestPlaybookBiz(t)
		ctx := context.Background()

		obj := &model.PlaybookM{
			Name:       "version-test",
			Version:    1,
			ConfigJSON: `{"version":"1.0"}`,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.Playbook().Create(ctx, obj))

		for i := 2; i <= 5; i++ {
			desc := "version " + string(rune('0'+i))
			resp, err := biz.Update(ctx, &v1.UpdatePlaybookRequest{
				Id:          obj.ID,
				Description: &desc,
			})
			require.NoError(t, err)
			require.Equal(t, int32(i), resp.Playbook.Version)
		}
	})

	t.Run("previous version tracking", func(t *testing.T) {
		biz, s := newTestPlaybookBiz(t)
		ctx := context.Background()

		obj := &model.PlaybookM{
			Name:       "version-test",
			Version:    1,
			ConfigJSON: `{"version":"1.0"}`,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.Playbook().Create(ctx, obj))

		desc := "v2"
		resp, err := biz.Update(ctx, &v1.UpdatePlaybookRequest{
			Id:          obj.ID,
			Description: &desc,
		})
		require.NoError(t, err)
		require.Equal(t, int32(2), resp.Playbook.Version)
		require.NotNil(t, resp.Playbook.PreviousVersion)
		require.Equal(t, int32(1), *resp.Playbook.PreviousVersion)
	})

	t.Run("rename keeps lineage snapshot and rollback restores old metadata", func(t *testing.T) {
		biz, s := newTestPlaybookBiz(t)
		ctx := context.Background()

		desc := "first description"
		createResp, err := biz.Create(ctx, &v1.CreatePlaybookRequest{
			Name:        "lineage-playbook-v1",
			Description: &desc,
			ConfigJSON:  `{"version":"1.0"}`,
		})
		require.NoError(t, err)
		require.NotEmpty(t, createResp.Playbook.LineageID)

		oldConfigJSON := createResp.Playbook.ConfigJSON

		name := "lineage-playbook-v2"
		desc2 := "second description"
		updateResp, err := biz.Update(ctx, &v1.UpdatePlaybookRequest{
			Id:          createResp.Playbook.Id,
			Name:        &name,
			Description: &desc2,
			ConfigJSON:  ptrString(`{"version":"2.0"}`),
		})
		require.NoError(t, err)
		require.Equal(t, createResp.Playbook.LineageID, updateResp.Playbook.LineageID)

		total, versions, err := s.Playbook().List(ctx, where.T(ctx).F("lineage_id", updateResp.Playbook.LineageID))
		require.NoError(t, err)
		require.Equal(t, int64(2), total)

		var snapshot *model.PlaybookM
		for _, item := range versions {
			if item.Version == 1 {
				snapshot = item
				break
			}
		}
		require.NotNil(t, snapshot)
		require.Equal(t, "lineage-playbook-v1", snapshot.Name)
		require.NotNil(t, snapshot.Description)
		require.Equal(t, "first description", *snapshot.Description)
		require.Equal(t, oldConfigJSON, snapshot.ConfigJSON)

		_, err = biz.Activate(ctx, &v1.ActivatePlaybookRequest{
			Id: updateResp.Playbook.Id,
		})
		require.NoError(t, err)

		_, err = biz.Rollback(ctx, &v1.RollbackPlaybookRequest{
			Id:       updateResp.Playbook.Id,
			Version:  1,
		})
		require.NoError(t, err)

		total, versions, err = s.Playbook().List(ctx, where.T(ctx).F("lineage_id", updateResp.Playbook.LineageID))
		require.NoError(t, err)
		require.Equal(t, int64(3), total)

		var rolledBack *model.PlaybookM
		for _, item := range versions {
			if item.Version == 3 {
				rolledBack = item
				break
			}
		}
		require.NotNil(t, rolledBack)
		require.Equal(t, "lineage-playbook-v1", rolledBack.Name)
		require.NotNil(t, rolledBack.Description)
		require.Equal(t, "first description", *rolledBack.Description)
		require.Equal(t, oldConfigJSON, rolledBack.ConfigJSON)
		require.True(t, rolledBack.Active)
		require.NotNil(t, rolledBack.ActivatedAt)

		activeCount, activeList, err := s.Playbook().List(ctx, where.T(ctx).F("active", true))
		require.NoError(t, err)
		require.Equal(t, int64(1), activeCount)
		require.Len(t, activeList, 1)
		require.Equal(t, rolledBack.ID, activeList[0].ID)
	})
}

// MCL: TestBuild_LegacyOutput removed - Build() function was removed from active path

func ptrString(v string) *string {
	return &v
}

func ptrInt(v int) *int {
	return &v
}