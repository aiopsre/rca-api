package toolmetadata

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dbName := t.Name()
	db, err := gorm.Open(sqlite.Open("file:"+dbName+"?mode=memory"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.ToolMetadataM{}))
	return db
}

func TestToolMetadataBiz_Create(t *testing.T) {
	store.ResetForTest()
	t.Cleanup(store.ResetForTest)

	db := setupTestDB(t)
	s := store.NewStore(db)
	ctx := context.Background()
	biz := New(s)

	t.Run("creates tool metadata with required fields", func(t *testing.T) {
		resp, err := biz.Create(ctx, &v1.CreateToolMetadataRequest{
			ToolName: "prometheus_query",
		})
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.NotNil(t, resp.ToolMetadata)
		require.Equal(t, "prometheus_query", resp.ToolMetadata.ToolName)
		require.Equal(t, "unknown", resp.ToolMetadata.Kind)
		require.Equal(t, "general", resp.ToolMetadata.Domain)
		require.Equal(t, true, resp.ToolMetadata.ReadOnly)
		require.Equal(t, "low", resp.ToolMetadata.RiskLevel)
		require.Equal(t, "fast", resp.ToolMetadata.LatencyTier)
		require.Equal(t, "free", resp.ToolMetadata.CostHint)
		require.Equal(t, "active", resp.ToolMetadata.Status)
	})

	t.Run("creates tool metadata with all fields", func(t *testing.T) {
		kind := "metrics"
		domain := "observability"
		readOnly := false
		riskLevel := "medium"
		latencyTier := "slow"
		costHint := "high"
		tagsJSON := `["metrics","query","promql"]`
		description := "Query Prometheus metrics"
		mcpServerID := "ms-prometheus-001"

		resp, err := biz.Create(ctx, &v1.CreateToolMetadataRequest{
			ToolName:     "loki_search",
			Kind:         &kind,
			Domain:       &domain,
			ReadOnly:     &readOnly,
			RiskLevel:    &riskLevel,
			LatencyTier:  &latencyTier,
			CostHint:     &costHint,
			TagsJSON:     &tagsJSON,
			Description:  &description,
			McpServerID:  &mcpServerID,
		})
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.Equal(t, "loki_search", resp.ToolMetadata.ToolName)
		require.Equal(t, "metrics", resp.ToolMetadata.Kind)
		require.Equal(t, "observability", resp.ToolMetadata.Domain)
		require.Equal(t, false, resp.ToolMetadata.ReadOnly)
		require.Equal(t, "medium", resp.ToolMetadata.RiskLevel)
		require.Equal(t, "slow", resp.ToolMetadata.LatencyTier)
		require.Equal(t, "high", resp.ToolMetadata.CostHint)
		require.NotNil(t, resp.ToolMetadata.TagsJSON)
		require.Contains(t, *resp.ToolMetadata.TagsJSON, "metrics")
	})

	t.Run("rejects duplicate tool_name", func(t *testing.T) {
		_, err := biz.Create(ctx, &v1.CreateToolMetadataRequest{
			ToolName: "prometheus_query",
		})
		require.Error(t, err)
		require.Equal(t, errno.ErrToolMetadataAlreadyExists, err)
	})

	t.Run("rejects missing tool_name", func(t *testing.T) {
		_, err := biz.Create(ctx, &v1.CreateToolMetadataRequest{})
		require.Error(t, err)
		require.Equal(t, errno.ErrToolMetadataCreateFailed, err)
	})

	t.Run("normalizes kind to lowercase", func(t *testing.T) {
		kind := "METRICS"
		resp, err := biz.Create(ctx, &v1.CreateToolMetadataRequest{
			ToolName: "test_normalize",
			Kind:     &kind,
		})
		require.NoError(t, err)
		require.Equal(t, "metrics", resp.ToolMetadata.Kind)
	})
}

func TestToolMetadataBiz_Get(t *testing.T) {
	store.ResetForTest()
	t.Cleanup(store.ResetForTest)

	db := setupTestDB(t)
	s := store.NewStore(db)
	ctx := context.Background()
	biz := New(s)

	// Create test metadata
	_, err := biz.Create(ctx, &v1.CreateToolMetadataRequest{
		ToolName: "test_get",
	})
	require.NoError(t, err)

	t.Run("gets tool metadata by tool name", func(t *testing.T) {
		resp, err := biz.Get(ctx, &v1.GetToolMetadataRequest{
			ToolName: "test_get",
		})
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.Equal(t, "test_get", resp.ToolMetadata.ToolName)
	})

	t.Run("returns error for non-existent tool name", func(t *testing.T) {
		_, err := biz.Get(ctx, &v1.GetToolMetadataRequest{
			ToolName: "non_existent",
		})
		require.Error(t, err)
		require.Equal(t, errno.ErrToolMetadataNotFound, err)
	})

	t.Run("rejects empty tool name", func(t *testing.T) {
		_, err := biz.Get(ctx, &v1.GetToolMetadataRequest{})
		require.Error(t, err)
		require.Equal(t, errno.ErrToolMetadataNotFound, err)
	})
}

func TestToolMetadataBiz_List(t *testing.T) {
	store.ResetForTest()
	t.Cleanup(store.ResetForTest)

	db := setupTestDB(t)
	s := store.NewStore(db)
	ctx := context.Background()
	biz := New(s)

	// Create test metadata with different kinds and domains
	kinds := []string{"metrics", "logs", "traces"}
	for i, kind := range kinds {
		_, err := biz.Create(ctx, &v1.CreateToolMetadataRequest{
			ToolName: "list_test_" + kind,
			Kind:     &kind,
			Domain:   ptrString("observability"),
		})
		require.NoError(t, err)
		_ = i
	}

	t.Run("lists all tool metadata", func(t *testing.T) {
		resp, err := biz.List(ctx, &v1.ListToolMetadataRequest{})
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.Equal(t, int64(3), resp.TotalCount)
		require.Len(t, resp.ToolMetadataList, 3)
	})

	t.Run("filters by kind", func(t *testing.T) {
		kind := "metrics"
		resp, err := biz.List(ctx, &v1.ListToolMetadataRequest{
			Kind: &kind,
		})
		require.NoError(t, err)
		require.Equal(t, int64(1), resp.TotalCount)
		require.Len(t, resp.ToolMetadataList, 1)
		require.Equal(t, "metrics", resp.ToolMetadataList[0].Kind)
	})

	t.Run("filters by domain", func(t *testing.T) {
		domain := "observability"
		resp, err := biz.List(ctx, &v1.ListToolMetadataRequest{
			Domain: &domain,
		})
		require.NoError(t, err)
		require.Equal(t, int64(3), resp.TotalCount)
	})

	t.Run("lists with limit", func(t *testing.T) {
		limit := int64(2)
		resp, err := biz.List(ctx, &v1.ListToolMetadataRequest{
			Limit: limit,
		})
		require.NoError(t, err)
		require.Equal(t, int64(3), resp.TotalCount)
		require.Len(t, resp.ToolMetadataList, 2)
	})

	t.Run("lists with offset", func(t *testing.T) {
		resp, err := biz.List(ctx, &v1.ListToolMetadataRequest{
			Offset: 1,
		})
		require.NoError(t, err)
		require.Equal(t, int64(3), resp.TotalCount)
		require.Len(t, resp.ToolMetadataList, 2)
	})

	t.Run("normalizes limit bounds", func(t *testing.T) {
		// Test default limit
		resp, err := biz.List(ctx, &v1.ListToolMetadataRequest{
			Limit: 0,
		})
		require.NoError(t, err)
		require.Len(t, resp.ToolMetadataList, 3)

		// Test max limit cap
		largeLimit := int64(500)
		resp, err = biz.List(ctx, &v1.ListToolMetadataRequest{
			Limit: largeLimit,
		})
		require.NoError(t, err)
		require.Len(t, resp.ToolMetadataList, 3)
	})
}

func TestToolMetadataBiz_Update(t *testing.T) {
	store.ResetForTest()
	t.Cleanup(store.ResetForTest)

	db := setupTestDB(t)
	s := store.NewStore(db)
	ctx := context.Background()
	biz := New(s)

	// Create test metadata
	_, err := biz.Create(ctx, &v1.CreateToolMetadataRequest{
		ToolName: "test_update",
	})
	require.NoError(t, err)

	t.Run("updates kind", func(t *testing.T) {
		kind := "logs"
		resp, err := biz.Update(ctx, &v1.UpdateToolMetadataRequest{
			ToolName: "test_update",
			Kind:     &kind,
		})
		require.NoError(t, err)
		require.Equal(t, "logs", resp.ToolMetadata.Kind)
	})

	t.Run("updates domain", func(t *testing.T) {
		domain := "incident"
		resp, err := biz.Update(ctx, &v1.UpdateToolMetadataRequest{
			ToolName: "test_update",
			Domain:   &domain,
		})
		require.NoError(t, err)
		require.Equal(t, "incident", resp.ToolMetadata.Domain)
	})

	t.Run("updates read_only", func(t *testing.T) {
		readOnly := false
		resp, err := biz.Update(ctx, &v1.UpdateToolMetadataRequest{
			ToolName: "test_update",
			ReadOnly: &readOnly,
		})
		require.NoError(t, err)
		require.Equal(t, false, resp.ToolMetadata.ReadOnly)
	})

	t.Run("updates risk_level", func(t *testing.T) {
		riskLevel := "high"
		resp, err := biz.Update(ctx, &v1.UpdateToolMetadataRequest{
			ToolName:  "test_update",
			RiskLevel: &riskLevel,
		})
		require.NoError(t, err)
		require.Equal(t, "high", resp.ToolMetadata.RiskLevel)
	})

	t.Run("updates tags_json", func(t *testing.T) {
		tagsJSON := `["metrics","promql"]`
		resp, err := biz.Update(ctx, &v1.UpdateToolMetadataRequest{
			ToolName: "test_update",
			TagsJSON: &tagsJSON,
		})
		require.NoError(t, err)
		require.NotNil(t, resp.ToolMetadata.TagsJSON)
		require.Contains(t, *resp.ToolMetadata.TagsJSON, "promql")
	})

	t.Run("updates description", func(t *testing.T) {
		description := "Updated description"
		resp, err := biz.Update(ctx, &v1.UpdateToolMetadataRequest{
			ToolName:    "test_update",
			Description: &description,
		})
		require.NoError(t, err)
		require.Equal(t, &description, resp.ToolMetadata.Description)
	})

	t.Run("updates status", func(t *testing.T) {
		status := "inactive"
		resp, err := biz.Update(ctx, &v1.UpdateToolMetadataRequest{
			ToolName: "test_update",
			Status:   &status,
		})
		require.NoError(t, err)
		require.Equal(t, "inactive", resp.ToolMetadata.Status)
	})

	t.Run("returns error for non-existent tool name", func(t *testing.T) {
		kind := "metrics"
		_, err := biz.Update(ctx, &v1.UpdateToolMetadataRequest{
			ToolName: "non_existent",
			Kind:     &kind,
		})
		require.Error(t, err)
		require.Equal(t, errno.ErrToolMetadataNotFound, err)
	})
}

func TestToolMetadataBiz_Delete(t *testing.T) {
	store.ResetForTest()
	t.Cleanup(store.ResetForTest)

	db := setupTestDB(t)
	s := store.NewStore(db)
	ctx := context.Background()
	biz := New(s)

	// Create test metadata
	_, err := biz.Create(ctx, &v1.CreateToolMetadataRequest{
		ToolName: "test_delete",
	})
	require.NoError(t, err)

	t.Run("deletes tool metadata", func(t *testing.T) {
		_, err := biz.Delete(ctx, &v1.DeleteToolMetadataRequest{
			ToolName: "test_delete",
		})
		require.NoError(t, err)

		// Verify deleted
		_, err = biz.Get(ctx, &v1.GetToolMetadataRequest{
			ToolName: "test_delete",
		})
		require.Error(t, err)
		require.Equal(t, errno.ErrToolMetadataNotFound, err)
	})

	t.Run("returns error for non-existent tool name", func(t *testing.T) {
		_, err := biz.Delete(ctx, &v1.DeleteToolMetadataRequest{
			ToolName: "non_existent",
		})
		require.Error(t, err)
		require.Equal(t, errno.ErrToolMetadataNotFound, err)
	})
}

func TestToolMetadataBiz_BatchGetMap(t *testing.T) {
	store.ResetForTest()
	t.Cleanup(store.ResetForTest)

	db := setupTestDB(t)
	s := store.NewStore(db)
	ctx := context.Background()
	biz := New(s)

	// Create test metadata
	for _, name := range []string{"batch_1", "batch_2", "batch_3"} {
		kind := "metrics"
		_, err := biz.Create(ctx, &v1.CreateToolMetadataRequest{
			ToolName: name,
			Kind:     &kind,
		})
		require.NoError(t, err)
	}

	t.Run("batch get returns map", func(t *testing.T) {
		result, err := biz.BatchGetMap(ctx, []string{"batch_1", "batch_2", "non_existent"})
		require.NoError(t, err)
		require.Len(t, result, 2)
		require.Contains(t, result, "batch_1")
		require.Contains(t, result, "batch_2")
		require.NotContains(t, result, "non_existent")
		require.Equal(t, "metrics", result["batch_1"].Kind)
	})

	t.Run("returns empty map for empty input", func(t *testing.T) {
		result, err := biz.BatchGetMap(ctx, []string{})
		require.NoError(t, err)
		require.Empty(t, result)
	})
}

func TestToolMetadataBiz_GetByToolName(t *testing.T) {
	store.ResetForTest()
	t.Cleanup(store.ResetForTest)

	db := setupTestDB(t)
	s := store.NewStore(db)
	ctx := context.Background()
	biz := New(s)

	// Create test metadata
	kind := "logs"
	_, err := biz.Create(ctx, &v1.CreateToolMetadataRequest{
		ToolName: "get_by_name",
		Kind:     &kind,
	})
	require.NoError(t, err)

	t.Run("gets by tool name", func(t *testing.T) {
		m, err := biz.GetByToolName(ctx, "get_by_name")
		require.NoError(t, err)
		require.NotNil(t, m)
		require.Equal(t, "get_by_name", m.ToolName)
		require.Equal(t, "logs", m.Kind)
	})

	t.Run("returns nil for non-existent", func(t *testing.T) {
		m, err := biz.GetByToolName(ctx, "non_existent")
		require.NoError(t, err)
		require.Nil(t, m)
	})

	t.Run("returns nil for empty name", func(t *testing.T) {
		m, err := biz.GetByToolName(ctx, "")
		require.NoError(t, err)
		require.Nil(t, m)
	})
}

func TestBuildToolMetadataRefs(t *testing.T) {
	t.Run("builds refs from tools and metadata", func(t *testing.T) {
		tagsJSON := ptrString(`["metrics","query"]`)
		description := "Query metrics"

		metadataMap := map[string]*model.ToolMetadataM{
			"tool1": {
				ToolName:    "tool1",
				Kind:        "metrics",
				Domain:      "observability",
				ReadOnly:    true,
				RiskLevel:   "low",
				LatencyTier: "fast",
				CostHint:    "free",
				TagsJSON:    tagsJSON,
				Description: &description,
			},
			"tool2": {
				ToolName: "tool2",
				Kind:     "logs",
			},
		}

		refs := BuildToolMetadataRefs([]string{"tool1", "tool2", "tool3"}, metadataMap)
		require.Len(t, refs, 2)

		require.Equal(t, "tool1", refs[0].ToolName)
		require.Equal(t, "metrics", refs[0].Kind)
		require.Equal(t, "observability", refs[0].Domain)
		require.Equal(t, true, refs[0].ReadOnly)
		require.Equal(t, []string{"metrics", "query"}, refs[0].Tags)
		require.Equal(t, "Query metrics", refs[0].Description)

		require.Equal(t, "tool2", refs[1].ToolName)
		require.Equal(t, "logs", refs[1].Kind)
	})

	t.Run("returns nil for empty input", func(t *testing.T) {
		refs := BuildToolMetadataRefs([]string{}, nil)
		require.Nil(t, refs)

		refs = BuildToolMetadataRefs(nil, map[string]*model.ToolMetadataM{})
		require.Nil(t, refs)
	})
}

func ptrString(s string) *string {
	return &s
}