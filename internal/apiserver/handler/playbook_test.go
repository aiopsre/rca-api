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

func newPlaybookHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.PlaybookM{}))
	return db
}

func newTestPlaybookHandlerEngine(t *testing.T) (*gin.Engine, store.IStore, func()) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	store.ResetForTest()

	db := newPlaybookHandlerTestDB(t)
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

func performPlaybookRequest(r *gin.Engine, method, path string, body interface{}) *httptest.ResponseRecorder {
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

func TestPlaybookHandler_Create(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		engine, _, cleanup := newTestPlaybookHandlerEngine(t)
		defer cleanup()

		body := map[string]interface{}{
			"name":        "test-playbook",
			"description": "test description",
			"config": map[string]interface{}{
				"version": "1.0",
			},
		}

		w := performPlaybookRequest(engine, http.MethodPost, "/v1/playbooks", body)
		require.Equal(t, http.StatusOK, w.Code)

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.NotNil(t, resp["playbook"])
	})

	t.Run("missing name", func(t *testing.T) {
		engine, _, cleanup := newTestPlaybookHandlerEngine(t)
		defer cleanup()

		body := map[string]interface{}{
			"config": map[string]interface{}{
				"version": "1.0",
			},
		}

		w := performPlaybookRequest(engine, http.MethodPost, "/v1/playbooks", body)
		require.NotEqual(t, http.StatusOK, w.Code)
	})

	t.Run("missing config", func(t *testing.T) {
		engine, _, cleanup := newTestPlaybookHandlerEngine(t)
		defer cleanup()

		body := map[string]interface{}{
			"name": "test-playbook",
		}

		w := performPlaybookRequest(engine, http.MethodPost, "/v1/playbooks", body)
		require.NotEqual(t, http.StatusOK, w.Code)
	})
}

func TestPlaybookHandler_Get(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		engine, s, cleanup := newTestPlaybookHandlerEngine(t)
		defer cleanup()

		ctx := context.Background()
		obj := &model.PlaybookM{
			Name:       "test-playbook",
			Version:    1,
			ConfigJSON: `{"version":"1.0"}`,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.Playbook().Create(ctx, obj))

		w := performPlaybookRequest(engine, http.MethodGet, fmt.Sprintf("/v1/playbooks/%d", obj.ID), nil)
		require.Equal(t, http.StatusOK, w.Code)

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.NotNil(t, resp["playbook"])
	})

	t.Run("invalid id", func(t *testing.T) {
		engine, _, cleanup := newTestPlaybookHandlerEngine(t)
		defer cleanup()

		w := performPlaybookRequest(engine, http.MethodGet, "/v1/playbooks/invalid", nil)
		require.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("not found", func(t *testing.T) {
		engine, _, cleanup := newTestPlaybookHandlerEngine(t)
		defer cleanup()

		w := performPlaybookRequest(engine, http.MethodGet, "/v1/playbooks/999", nil)
		require.NotEqual(t, http.StatusOK, w.Code)
	})
}

func TestPlaybookHandler_List(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		engine, s, cleanup := newTestPlaybookHandlerEngine(t)
		defer cleanup()

		ctx := context.Background()
		for i := 1; i <= 3; i++ {
			obj := &model.PlaybookM{
				Name:       fmt.Sprintf("playbook-%d", i),
				Version:    1,
				ConfigJSON: `{"version":"1.0"}`,
				Active:     i%2 == 0,
				CreatedBy:  "admin",
			}
			require.NoError(t, s.Playbook().Create(ctx, obj))
		}

		w := performPlaybookRequest(engine, http.MethodGet, "/v1/playbooks", nil)
		require.Equal(t, http.StatusOK, w.Code)

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.NotNil(t, resp["playbooks"])
	})

	t.Run("filter by active", func(t *testing.T) {
		engine, s, cleanup := newTestPlaybookHandlerEngine(t)
		defer cleanup()

		ctx := context.Background()
		for i := 1; i <= 3; i++ {
			obj := &model.PlaybookM{
				Name:       fmt.Sprintf("playbook-%d", i),
				Version:    1,
				ConfigJSON: `{"version":"1.0"}`,
				Active:     i%2 == 0,
				CreatedBy:  "admin",
			}
			require.NoError(t, s.Playbook().Create(ctx, obj))
		}

		w := performPlaybookRequest(engine, http.MethodGet, "/v1/playbooks?active=true", nil)
		require.Equal(t, http.StatusOK, w.Code)

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		playbooks := resp["playbooks"].([]interface{})
		require.Len(t, playbooks, 1)
	})
}

func TestPlaybookHandler_Update(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		engine, s, cleanup := newTestPlaybookHandlerEngine(t)
		defer cleanup()

		ctx := context.Background()
		obj := &model.PlaybookM{
			Name:       "test-playbook",
			Version:    1,
			ConfigJSON: `{"version":"1.0"}`,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.Playbook().Create(ctx, obj))

		body := map[string]interface{}{
			"name":        "updated-playbook",
			"description": "updated description",
		}

		w := performPlaybookRequest(engine, http.MethodPut, fmt.Sprintf("/v1/playbooks/%d", obj.ID), body)
		require.Equal(t, http.StatusOK, w.Code)

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.NotNil(t, resp["playbook"])
	})

	t.Run("not found", func(t *testing.T) {
		engine, _, cleanup := newTestPlaybookHandlerEngine(t)
		defer cleanup()

		body := map[string]interface{}{
			"name": "updated-playbook",
		}

		w := performPlaybookRequest(engine, http.MethodPut, "/v1/playbooks/999", body)
		require.NotEqual(t, http.StatusOK, w.Code)
	})
}

func TestPlaybookHandler_Delete(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		engine, s, cleanup := newTestPlaybookHandlerEngine(t)
		defer cleanup()

		ctx := context.Background()
		obj := &model.PlaybookM{
			Name:       "test-playbook",
			Version:    1,
			ConfigJSON: `{"version":"1.0"}`,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.Playbook().Create(ctx, obj))

		w := performPlaybookRequest(engine, http.MethodDelete, fmt.Sprintf("/v1/playbooks/%d", obj.ID), nil)
		require.Equal(t, http.StatusOK, w.Code)

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Equal(t, true, resp["deleted"])
	})

	t.Run("invalid id", func(t *testing.T) {
		engine, _, cleanup := newTestPlaybookHandlerEngine(t)
		defer cleanup()

		w := performPlaybookRequest(engine, http.MethodDelete, "/v1/playbooks/invalid", nil)
		require.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestPlaybookHandler_Activate(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		engine, s, cleanup := newTestPlaybookHandlerEngine(t)
		defer cleanup()

		ctx := context.Background()
		obj := &model.PlaybookM{
			Name:       "test-playbook",
			Version:    1,
			ConfigJSON: `{"version":"1.0"}`,
			Active:     false,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.Playbook().Create(ctx, obj))

		w := performPlaybookRequest(engine, http.MethodPost, fmt.Sprintf("/v1/playbooks/%d/activate", obj.ID), nil)
		require.Equal(t, http.StatusOK, w.Code)

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Equal(t, true, resp["activated"])
	})

	t.Run("invalid id", func(t *testing.T) {
		engine, _, cleanup := newTestPlaybookHandlerEngine(t)
		defer cleanup()

		w := performPlaybookRequest(engine, http.MethodPost, "/v1/playbooks/invalid/activate", nil)
		require.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestPlaybookHandler_Deactivate(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		engine, s, cleanup := newTestPlaybookHandlerEngine(t)
		defer cleanup()

		ctx := context.Background()
		obj := &model.PlaybookM{
			Name:       "test-playbook",
			Version:    1,
			ConfigJSON: `{"version":"1.0"}`,
			Active:     true,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.Playbook().Create(ctx, obj))

		w := performPlaybookRequest(engine, http.MethodPost, fmt.Sprintf("/v1/playbooks/%d/deactivate", obj.ID), nil)
		require.Equal(t, http.StatusOK, w.Code)

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Equal(t, true, resp["deactivated"])
	})
}

func TestPlaybookHandler_Rollback(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		engine, s, cleanup := newTestPlaybookHandlerEngine(t)
		defer cleanup()

		ctx := context.Background()
		for i := 1; i <= 3; i++ {
			obj := &model.PlaybookM{
				Name:       "versioned-playbook",
				Version:    i,
				ConfigJSON: fmt.Sprintf(`{"version":"%d.0"}`, i),
				CreatedBy:  "admin",
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

		body := map[string]interface{}{
			"version": 1,
		}

		w := performPlaybookRequest(engine, http.MethodPost, fmt.Sprintf("/v1/playbooks/%d/rollback", current.ID), body)
		require.Equal(t, http.StatusOK, w.Code)

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Equal(t, true, resp["rollbacked"])
	})

	t.Run("invalid version", func(t *testing.T) {
		engine, s, cleanup := newTestPlaybookHandlerEngine(t)
		defer cleanup()

		ctx := context.Background()
		obj := &model.PlaybookM{
			Name:       "test-playbook",
			Version:    1,
			ConfigJSON: `{"version":"1.0"}`,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.Playbook().Create(ctx, obj))

		body := map[string]interface{}{
			"version": 0,
		}

		w := performPlaybookRequest(engine, http.MethodPost, fmt.Sprintf("/v1/playbooks/%d/rollback", obj.ID), body)
		require.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestPlaybookHandler_GetActive(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		engine, s, cleanup := newTestPlaybookHandlerEngine(t)
		defer cleanup()

		ctx := context.Background()
		obj := &model.PlaybookM{
			Name:       "active-playbook",
			Version:    1,
			ConfigJSON: `{"version":"1.0"}`,
			Active:     true,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.Playbook().Create(ctx, obj))

		w := performPlaybookRequest(engine, http.MethodGet, "/v1/playbooks/active", nil)
		require.Equal(t, http.StatusOK, w.Code)

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.NotNil(t, resp["playbook"])
	})

	t.Run("not found", func(t *testing.T) {
		engine, _, cleanup := newTestPlaybookHandlerEngine(t)
		defer cleanup()

		w := performPlaybookRequest(engine, http.MethodGet, "/v1/playbooks/active", nil)
		require.NotEqual(t, http.StatusOK, w.Code)
	})
}

func TestPlaybookHandler_AuditFieldsAutoPopulated(t *testing.T) {
	t.Run("create populates created_by and updated_by", func(t *testing.T) {
		engine, _, cleanup := newTestPlaybookHandlerEngine(t)
		defer cleanup()

		body := map[string]interface{}{
			"name": "audit-test",
			"config": map[string]interface{}{
				"version": "1.0",
			},
		}

		w := performPlaybookRequest(engine, http.MethodPost, "/v1/playbooks", body)
		require.Equal(t, http.StatusOK, w.Code)

		var resp map[string]interface{}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		playbook := resp["playbook"].(map[string]interface{})
		require.Equal(t, "system", playbook["created_by"])
		require.Equal(t, "system", playbook["updated_by"])
	})
}

func TestPlaybookHandler_VersionManagement(t *testing.T) {
	t.Run("version increments on update", func(t *testing.T) {
		engine, s, cleanup := newTestPlaybookHandlerEngine(t)
		defer cleanup()

		ctx := context.Background()
		obj := &model.PlaybookM{
			Name:       "version-test",
			Version:    1,
			ConfigJSON: `{"version":"1.0"}`,
			CreatedBy:  "admin",
		}
		require.NoError(t, s.Playbook().Create(ctx, obj))

		for i := 2; i <= 3; i++ {
			body := map[string]interface{}{
				"description": fmt.Sprintf("version %d", i),
			}

			w := performPlaybookRequest(engine, http.MethodPut, fmt.Sprintf("/v1/playbooks/%d", obj.ID), body)
			require.Equal(t, http.StatusOK, w.Code)

			var resp map[string]interface{}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			playbook := resp["playbook"].(map[string]interface{})
			require.Equal(t, float64(i), playbook["version"])
		}
	})
}
