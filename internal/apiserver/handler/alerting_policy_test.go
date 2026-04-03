package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/apiserver/biz"
	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/validation"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

func newAlertingPolicyHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.AlertingPolicyM{}))
	return db
}

func newTestAlertingPolicyHandlerEngine(t *testing.T) (*gin.Engine, store.IStore, func()) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	store.ResetForTest()

	db := newAlertingPolicyHandlerTestDB(t)
	s := store.NewStore(db)
	val := validation.New(s)
	h := NewHandler(biz.NewBiz(s), val)

	engine := gin.New()
	h.ApplyTo(engine.Group("/v1"))

	cleanup := func() {
		store.ResetForTest()
	}
	return engine, s, cleanup
}

func performRequest(r *gin.Engine, method, path string, body interface{}) *httptest.ResponseRecorder {
	var w *httptest.ResponseRecorder

	if body != nil {
		bodyBytes, _ := json.Marshal(body)
		w = httptest.NewRecorder()
		req, _ := http.NewRequest(method, path, bytes.NewReader(bodyBytes))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Scopes", "*")
		r.ServeHTTP(w, req)
	} else {
		w = httptest.NewRecorder()
		req, _ := http.NewRequest(method, path, nil)
		req.Header.Set("X-Scopes", "*")
		r.ServeHTTP(w, req)
	}

	return w
}

func TestAlertingPolicyHandler_Create(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		engine, _, cleanup := newTestAlertingPolicyHandlerEngine(t)
		defer cleanup()

		body := map[string]interface{}{
			"name":        "test-policy",
			"description": "test description",
			"config": map[string]interface{}{
				"version": 1,
			},
		}

		w := performRequest(engine, http.MethodPost, "/v1/alerting-policies", body)
		require.Equal(t, http.StatusOK, w.Code)

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.NotNil(t, resp["alerting_policy"])
	})

	t.Run("missing name", func(t *testing.T) {
		engine, _, cleanup := newTestAlertingPolicyHandlerEngine(t)
		defer cleanup()

		body := map[string]interface{}{
			"config": map[string]interface{}{
				"version": 1,
			},
		}

		w := performRequest(engine, http.MethodPost, "/v1/alerting-policies", body)
		require.NotEqual(t, http.StatusOK, w.Code)
	})

	t.Run("missing config", func(t *testing.T) {
		engine, _, cleanup := newTestAlertingPolicyHandlerEngine(t)
		defer cleanup()

		body := map[string]interface{}{
			"name": "test-policy",
		}

		w := performRequest(engine, http.MethodPost, "/v1/alerting-policies", body)
		require.NotEqual(t, http.StatusOK, w.Code)
	})
}

func TestAlertingPolicyHandler_Get(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		engine, s, cleanup := newTestAlertingPolicyHandlerEngine(t)
		defer cleanup()

		ctx := context.Background()
		obj := &model.AlertingPolicyM{
			Name:       "test-policy",
			Version:    1,
			ConfigJSON: `{"version":1}`,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.AlertingPolicy().Create(ctx, obj))

		w := performRequest(engine, http.MethodGet, fmt.Sprintf("/v1/alerting-policies/%d", obj.ID), nil)
		require.Equal(t, http.StatusOK, w.Code)

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.NotNil(t, resp["alerting_policy"])
	})

	t.Run("invalid id", func(t *testing.T) {
		engine, _, cleanup := newTestAlertingPolicyHandlerEngine(t)
		defer cleanup()

		w := performRequest(engine, http.MethodGet, "/v1/alerting-policies/invalid", nil)
		require.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("not found", func(t *testing.T) {
		engine, _, cleanup := newTestAlertingPolicyHandlerEngine(t)
		defer cleanup()

		w := performRequest(engine, http.MethodGet, "/v1/alerting-policies/999", nil)
		require.NotEqual(t, http.StatusOK, w.Code)
	})
}

func TestAlertingPolicyHandler_List(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		engine, s, cleanup := newTestAlertingPolicyHandlerEngine(t)
		defer cleanup()

		ctx := context.Background()
		for i := 1; i <= 3; i++ {
			obj := &model.AlertingPolicyM{
				Name:       fmt.Sprintf("policy-%d", i),
				Version:    1,
				ConfigJSON: `{"version":1}`,
				Active:     i%2 == 0,
				CreatedBy:  "admin",
			}
			require.NoError(t, s.AlertingPolicy().Create(ctx, obj))
		}

		w := performRequest(engine, http.MethodGet, "/v1/alerting-policies", nil)
		require.Equal(t, http.StatusOK, w.Code)

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.NotNil(t, resp["alerting_policies"])
	})

	t.Run("filter by active", func(t *testing.T) {
		engine, s, cleanup := newTestAlertingPolicyHandlerEngine(t)
		defer cleanup()

		ctx := context.Background()
		for i := 1; i <= 3; i++ {
			obj := &model.AlertingPolicyM{
				Name:       fmt.Sprintf("policy-%d", i),
				Version:    1,
				ConfigJSON: `{"version":1}`,
				Active:     i%2 == 0,
				CreatedBy:  "admin",
			}
			require.NoError(t, s.AlertingPolicy().Create(ctx, obj))
		}

		w := performRequest(engine, http.MethodGet, "/v1/alerting-policies?active=true", nil)
		require.Equal(t, http.StatusOK, w.Code)

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		policies := resp["alerting_policies"].([]interface{})
		require.Len(t, policies, 1)
	})
}

func TestAlertingPolicyHandler_Update(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		engine, s, cleanup := newTestAlertingPolicyHandlerEngine(t)
		defer cleanup()

		ctx := context.Background()
		obj := &model.AlertingPolicyM{
			Name:       "test-policy",
			Version:    1,
			ConfigJSON: `{"version":1}`,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.AlertingPolicy().Create(ctx, obj))

		body := map[string]interface{}{
			"name":        "updated-policy",
			"description": "updated description",
		}

		w := performRequest(engine, http.MethodPut, fmt.Sprintf("/v1/alerting-policies/%d", obj.ID), body)
		require.Equal(t, http.StatusOK, w.Code)

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.NotNil(t, resp["alerting_policy"])
	})

	t.Run("not found", func(t *testing.T) {
		engine, _, cleanup := newTestAlertingPolicyHandlerEngine(t)
		defer cleanup()

		body := map[string]interface{}{
			"name": "updated-policy",
		}

		w := performRequest(engine, http.MethodPut, "/v1/alerting-policies/999", body)
		require.NotEqual(t, http.StatusOK, w.Code)
	})
}

func TestAlertingPolicyHandler_Delete(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		engine, s, cleanup := newTestAlertingPolicyHandlerEngine(t)
		defer cleanup()

		ctx := context.Background()
		obj := &model.AlertingPolicyM{
			Name:       "test-policy",
			Version:    1,
			ConfigJSON: `{"version":1}`,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.AlertingPolicy().Create(ctx, obj))

		w := performRequest(engine, http.MethodDelete, fmt.Sprintf("/v1/alerting-policies/%d", obj.ID), nil)
		require.Equal(t, http.StatusOK, w.Code)

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Equal(t, true, resp["deleted"])
	})

	t.Run("invalid id", func(t *testing.T) {
		engine, _, cleanup := newTestAlertingPolicyHandlerEngine(t)
		defer cleanup()

		w := performRequest(engine, http.MethodDelete, "/v1/alerting-policies/invalid", nil)
		require.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestAlertingPolicyHandler_Activate(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		engine, s, cleanup := newTestAlertingPolicyHandlerEngine(t)
		defer cleanup()

		ctx := context.Background()
		obj := &model.AlertingPolicyM{
			Name:       "test-policy",
			Version:    1,
			ConfigJSON: `{"version":1}`,
			Active:     false,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.AlertingPolicy().Create(ctx, obj))

		w := performRequest(engine, http.MethodPost, fmt.Sprintf("/v1/alerting-policies/%d/activate", obj.ID), nil)
		require.Equal(t, http.StatusOK, w.Code)

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Equal(t, true, resp["activated"])
	})

	t.Run("invalid id", func(t *testing.T) {
		engine, _, cleanup := newTestAlertingPolicyHandlerEngine(t)
		defer cleanup()

		w := performRequest(engine, http.MethodPost, "/v1/alerting-policies/invalid/activate", nil)
		require.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestAlertingPolicyHandler_Deactivate(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		engine, s, cleanup := newTestAlertingPolicyHandlerEngine(t)
		defer cleanup()

		ctx := context.Background()
		obj := &model.AlertingPolicyM{
			Name:       "test-policy",
			Version:    1,
			ConfigJSON: `{"version":1}`,
			Active:     true,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.AlertingPolicy().Create(ctx, obj))

		w := performRequest(engine, http.MethodPost, fmt.Sprintf("/v1/alerting-policies/%d/deactivate", obj.ID), nil)
		require.Equal(t, http.StatusOK, w.Code)

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Equal(t, true, resp["deactivated"])
	})
}

func TestAlertingPolicyHandler_Rollback(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		engine, s, cleanup := newTestAlertingPolicyHandlerEngine(t)
		defer cleanup()

		ctx := context.Background()
		for i := 1; i <= 3; i++ {
			obj := &model.AlertingPolicyM{
				Name:       "versioned-policy",
				Version:    i,
				ConfigJSON: fmt.Sprintf(`{"version":%d}`, i),
				CreatedBy:  "admin",
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

		body := map[string]interface{}{
			"version": 1,
		}

		w := performRequest(engine, http.MethodPost, fmt.Sprintf("/v1/alerting-policies/%d/rollback", current.ID), body)
		require.Equal(t, http.StatusOK, w.Code)

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Equal(t, true, resp["rollbacked"])
	})

	t.Run("invalid version", func(t *testing.T) {
		engine, s, cleanup := newTestAlertingPolicyHandlerEngine(t)
		defer cleanup()

		ctx := context.Background()
		obj := &model.AlertingPolicyM{
			Name:       "test-policy",
			Version:    1,
			ConfigJSON: `{"version":1}`,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.AlertingPolicy().Create(ctx, obj))

		body := map[string]interface{}{
			"version": 0,
		}

		w := performRequest(engine, http.MethodPost, fmt.Sprintf("/v1/alerting-policies/%d/rollback", obj.ID), body)
		require.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestAlertingPolicyHandler_GetActive(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		engine, s, cleanup := newTestAlertingPolicyHandlerEngine(t)
		defer cleanup()

		ctx := context.Background()
		obj := &model.AlertingPolicyM{
			Name:       "active-policy",
			Version:    1,
			ConfigJSON: `{"version":1}`,
			Active:     true,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.AlertingPolicy().Create(ctx, obj))

		w := performRequest(engine, http.MethodGet, "/v1/alerting-policies/active", nil)
		require.Equal(t, http.StatusOK, w.Code)

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.NotNil(t, resp["alerting_policy"])
	})

	t.Run("not found", func(t *testing.T) {
		engine, _, cleanup := newTestAlertingPolicyHandlerEngine(t)
		defer cleanup()

		w := performRequest(engine, http.MethodGet, "/v1/alerting-policies/active", nil)
		require.NotEqual(t, http.StatusOK, w.Code)
	})
}

func TestAlertingPolicyHandler_AuditFieldsAutoPopulated(t *testing.T) {
	t.Run("create populates created_by and updated_by", func(t *testing.T) {
		engine, _, cleanup := newTestAlertingPolicyHandlerEngine(t)
		defer cleanup()

		body := map[string]interface{}{
			"name": "audit-test",
			"config": map[string]interface{}{
				"version": 1,
			},
		}

		w := performRequest(engine, http.MethodPost, "/v1/alerting-policies", body)
		require.Equal(t, http.StatusOK, w.Code)

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		policy := resp["alerting_policy"].(map[string]interface{})
		require.Equal(t, "system", policy["created_by"])
		require.Equal(t, "system", policy["updated_by"])
	})
}

func TestAlertingPolicyHandler_VersionManagement(t *testing.T) {
	t.Run("version increments on update", func(t *testing.T) {
		engine, s, cleanup := newTestAlertingPolicyHandlerEngine(t)
		defer cleanup()

		ctx := context.Background()
		obj := &model.AlertingPolicyM{
			Name:       "version-test",
			Version:    1,
			ConfigJSON: `{"version":1}`,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.AlertingPolicy().Create(ctx, obj))

		for i := 2; i <= 3; i++ {
			body := map[string]interface{}{
				"description": fmt.Sprintf("version %d", i),
			}

			w := performRequest(engine, http.MethodPut, fmt.Sprintf("/v1/alerting-policies/%d", obj.ID), body)
			require.Equal(t, http.StatusOK, w.Code)

			var resp map[string]interface{}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			policy := resp["alerting_policy"].(map[string]interface{})
			require.Equal(t, float64(i), policy["version"])
		}
	})
}
