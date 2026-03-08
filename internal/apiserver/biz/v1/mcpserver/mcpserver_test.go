package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/apiserver/biz/v1/toolmetadata"
	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// Use unique DB name per test to avoid ID collisions
	// Don't use cache=shared to ensure true isolation
	dbName := t.Name()
	db, err := gorm.Open(sqlite.Open("file:"+dbName+"?mode=memory"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.McpServerM{},
		&model.McpServerConfigM{},
		&model.ToolMetadataM{},
	))
	return db
}

func TestMcpServerBiz_Create(t *testing.T) {
	store.ResetForTest()
	t.Cleanup(store.ResetForTest)

	db := setupTestDB(t)
	s := store.NewStore(db)
	ctx := context.Background()
	toolMetadataBiz := toolmetadata.New(s)
	biz := New(s, toolMetadataBiz)

	t.Run("creates mcp server with required fields", func(t *testing.T) {
		resp, err := biz.Create(ctx, &v1.CreateMcpServerRequest{
			Name:    "prometheus",
			BaseURL: "http://prometheus.mcp:8080",
		})
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.NotNil(t, resp.McpServer)
		require.NotEmpty(t, resp.McpServer.McpServerID)
		require.Equal(t, "prometheus", resp.McpServer.Name)
		require.Equal(t, "http://prometheus.mcp:8080", resp.McpServer.BaseURL)
		require.Equal(t, "none", resp.McpServer.AuthType)
		require.Equal(t, int32(10), resp.McpServer.TimeoutSec)
		require.Equal(t, "active", resp.McpServer.Status)
	})

	t.Run("creates mcp server with all fields", func(t *testing.T) {
		displayName := "Prometheus MCP Server"
		description := "Prometheus metrics query server"
		allowedToolsJSON := `["query_metrics", "query_range"]`
		scopes := "read:metrics"
		authType := "bearer"
		timeoutSec := int32(30)

		resp, err := biz.Create(ctx, &v1.CreateMcpServerRequest{
			Name:              "loki",
			DisplayName:       &displayName,
			Description:       &description,
			BaseURL:           "http://loki.mcp:8080",
			AuthType:          &authType,
			AllowedToolsJSON:  &allowedToolsJSON,
			TimeoutSec:        &timeoutSec,
			Scopes:            &scopes,
		})
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.Equal(t, "loki", resp.McpServer.Name)
		require.Equal(t, &displayName, resp.McpServer.DisplayName)
		require.Equal(t, &description, resp.McpServer.Description)
		require.Equal(t, "bearer", resp.McpServer.AuthType)
		require.Equal(t, int32(30), resp.McpServer.TimeoutSec)
		require.Equal(t, &scopes, resp.McpServer.Scopes)
	})

	t.Run("rejects duplicate name", func(t *testing.T) {
		_, err := biz.Create(ctx, &v1.CreateMcpServerRequest{
			Name:    "prometheus",
			BaseURL: "http://prometheus2.mcp:8080",
		})
		require.Error(t, err)
		require.Equal(t, errno.ErrMcpServerAlreadyExists, err)
	})

	t.Run("rejects missing name", func(t *testing.T) {
		_, err := biz.Create(ctx, &v1.CreateMcpServerRequest{
			BaseURL: "http://test.mcp:8080",
		})
		require.Error(t, err)
	})

	t.Run("rejects missing base_url", func(t *testing.T) {
		_, err := biz.Create(ctx, &v1.CreateMcpServerRequest{
			Name: "test-no-url",
		})
		require.Error(t, err)
	})

	t.Run("normalizes auth_type to lowercase", func(t *testing.T) {
		authType := "BEARER"
		resp, err := biz.Create(ctx, &v1.CreateMcpServerRequest{
			Name:     "test-auth-normalize",
			BaseURL:  "http://test.mcp:8080",
			AuthType: &authType,
		})
		require.NoError(t, err)
		require.Equal(t, "bearer", resp.McpServer.AuthType)
	})
}

func TestMcpServerBiz_Get(t *testing.T) {
	store.ResetForTest()
	t.Cleanup(store.ResetForTest)

	db := setupTestDB(t)
	s := store.NewStore(db)
	ctx := context.Background()
	toolMetadataBiz := toolmetadata.New(s)
	biz := New(s, toolMetadataBiz)

	// Create a test server
	createResp, err := biz.Create(ctx, &v1.CreateMcpServerRequest{
		Name:    "test-get",
		BaseURL: "http://test.mcp:8080",
	})
	require.NoError(t, err)

	t.Run("gets mcp server by id", func(t *testing.T) {
		resp, err := biz.Get(ctx, &v1.GetMcpServerRequest{
			McpServerID: createResp.McpServer.McpServerID,
		})
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.Equal(t, "test-get", resp.McpServer.Name)
	})

	t.Run("returns error for non-existent id", func(t *testing.T) {
		_, err := biz.Get(ctx, &v1.GetMcpServerRequest{
			McpServerID: "non-existent-id",
		})
		require.Error(t, err)
		require.Equal(t, errno.ErrMcpServerNotFound, err)
	})

	t.Run("rejects empty id", func(t *testing.T) {
		_, err := biz.Get(ctx, &v1.GetMcpServerRequest{})
		require.Error(t, err)
	})
}

func TestMcpServerBiz_List(t *testing.T) {
	store.ResetForTest()
	t.Cleanup(store.ResetForTest)

	db := setupTestDB(t)
	s := store.NewStore(db)
	ctx := context.Background()
	toolMetadataBiz := toolmetadata.New(s)
	biz := New(s, toolMetadataBiz)

	// Create test servers
	names := []string{"list-test-a", "list-test-b", "list-test-c", "list-test-d", "list-test-e"}
	for _, name := range names {
		_, err := biz.Create(ctx, &v1.CreateMcpServerRequest{
			Name:    name,
			BaseURL: "http://test.mcp:8080",
		})
		require.NoError(t, err)
	}

	t.Run("lists all mcp servers", func(t *testing.T) {
		resp, err := biz.List(ctx, &v1.ListMcpServersRequest{})
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.Equal(t, int64(5), resp.TotalCount)
		require.Len(t, resp.McpServers, 5)
	})

	t.Run("lists with limit", func(t *testing.T) {
		limit := int64(2)
		resp, err := biz.List(ctx, &v1.ListMcpServersRequest{
			Limit: limit,
		})
		require.NoError(t, err)
		require.Equal(t, int64(5), resp.TotalCount)
		require.Len(t, resp.McpServers, 2)
	})

	t.Run("lists with offset", func(t *testing.T) {
		resp, err := biz.List(ctx, &v1.ListMcpServersRequest{
			Offset: 2,
		})
		require.NoError(t, err)
		require.Equal(t, int64(5), resp.TotalCount)
		require.Len(t, resp.McpServers, 3)
	})
}

func TestMcpServerBiz_Update(t *testing.T) {
	store.ResetForTest()
	t.Cleanup(store.ResetForTest)

	db := setupTestDB(t)
	s := store.NewStore(db)
	ctx := context.Background()
	toolMetadataBiz := toolmetadata.New(s)
	biz := New(s, toolMetadataBiz)

	// Create a test server
	createResp, err := biz.Create(ctx, &v1.CreateMcpServerRequest{
		Name:    "test-update",
		BaseURL: "http://test.mcp:8080",
	})
	require.NoError(t, err)

	t.Run("updates display name", func(t *testing.T) {
		newDisplayName := "Updated Display Name"
		resp, err := biz.Update(ctx, &v1.UpdateMcpServerRequest{
			McpServerID: createResp.McpServer.McpServerID,
			DisplayName: &newDisplayName,
		})
		require.NoError(t, err)
		require.Equal(t, &newDisplayName, resp.McpServer.DisplayName)
	})

	t.Run("updates allowed tools", func(t *testing.T) {
		newToolsJSON := `["tool1", "tool2"]`
		resp, err := biz.Update(ctx, &v1.UpdateMcpServerRequest{
			McpServerID:      createResp.McpServer.McpServerID,
			AllowedToolsJSON: &newToolsJSON,
		})
		require.NoError(t, err)
		require.NotNil(t, resp.McpServer.AllowedToolsJSON)
	})

	t.Run("updates status", func(t *testing.T) {
		newStatus := "inactive"
		resp, err := biz.Update(ctx, &v1.UpdateMcpServerRequest{
			McpServerID: createResp.McpServer.McpServerID,
			Status:      &newStatus,
		})
		require.NoError(t, err)
		require.Equal(t, "inactive", resp.McpServer.Status)
	})

	t.Run("returns error for non-existent id", func(t *testing.T) {
		_, err := biz.Update(ctx, &v1.UpdateMcpServerRequest{
			McpServerID: "non-existent-id",
		})
		require.Error(t, err)
		require.Equal(t, errno.ErrMcpServerNotFound, err)
	})
}

func TestMcpServerBiz_Delete(t *testing.T) {
	store.ResetForTest()
	t.Cleanup(store.ResetForTest)

	db := setupTestDB(t)
	s := store.NewStore(db)
	ctx := context.Background()
	toolMetadataBiz := toolmetadata.New(s)
	biz := New(s, toolMetadataBiz)

	// Create a test server
	createResp, err := biz.Create(ctx, &v1.CreateMcpServerRequest{
		Name:    "test-delete",
		BaseURL: "http://test.mcp:8080",
	})
	require.NoError(t, err)

	t.Run("deletes mcp server", func(t *testing.T) {
		_, err := biz.Delete(ctx, &v1.DeleteMcpServerRequest{
			McpServerID: createResp.McpServer.McpServerID,
		})
		require.NoError(t, err)

		// Verify deleted
		_, err = biz.Get(ctx, &v1.GetMcpServerRequest{
			McpServerID: createResp.McpServer.McpServerID,
		})
		require.Error(t, err)
		require.Equal(t, errno.ErrMcpServerNotFound, err)
	})

	t.Run("returns error for non-existent id", func(t *testing.T) {
		_, err := biz.Delete(ctx, &v1.DeleteMcpServerRequest{
			McpServerID: "non-existent-id",
		})
		require.Error(t, err)
		require.Equal(t, errno.ErrMcpServerNotFound, err)
	})
}

func TestMcpServerBiz_ResolveMcpServerRefs(t *testing.T) {
	store.ResetForTest()
	t.Cleanup(store.ResetForTest)

	db := setupTestDB(t)
	s := store.NewStore(db)
	ctx := context.Background()
	toolMetadataBiz := toolmetadata.New(s)
	biz := New(s, toolMetadataBiz)

	t.Run("returns empty for pipeline with no configs", func(t *testing.T) {
		refs, err := biz.ResolveMcpServerRefs(ctx, "pipeline-without-configs")
		require.NoError(t, err)
		require.Empty(t, refs)
	})

	t.Run("returns empty for empty pipeline id", func(t *testing.T) {
		refs, err := biz.ResolveMcpServerRefs(ctx, "")
		require.NoError(t, err)
		require.Empty(t, refs)
	})

	t.Run("returns refs for pipeline with configs", func(t *testing.T) {
		// Create McpServerConfig with refs
		refsJSON := `[{"mcp_server_id":"ms-001","name":"prometheus","base_url":"http://prometheus:8080","allowed_tools":["query_metrics"],"timeout_sec":30,"scopes":"read","auth_type":"none"}]`
		require.NoError(t, s.McpServerConfig().Create(ctx, &model.McpServerConfigM{
			PipelineID:        "basic_rca",
			McpServerName:     "prometheus",
			McpServerRefsJSON: &refsJSON,
			Enabled:           true,
		}))

		refs, err := biz.ResolveMcpServerRefs(ctx, "basic_rca")
		require.NoError(t, err)
		require.Len(t, refs, 1)
		require.Equal(t, "ms-001", refs[0].McpServerID)
		require.Equal(t, "prometheus", refs[0].Name)
		require.Equal(t, "http://prometheus:8080", refs[0].BaseURL)
		require.Equal(t, []string{"query_metrics"}, refs[0].AllowedTools)
		require.Equal(t, 30, refs[0].TimeoutSec)
	})

	t.Run("excludes disabled configs", func(t *testing.T) {
		refsJSON := `[{"mcp_server_id":"ms-002","name":"loki","base_url":"http://loki:8080","allowed_tools":["query_logs"]}]`
		require.NoError(t, s.McpServerConfig().Create(ctx, &model.McpServerConfigM{
			PipelineID:        "disabled_pipeline",
			McpServerName:     "loki",
			McpServerRefsJSON: &refsJSON,
			Enabled:           false,
		}))

		refs, err := biz.ResolveMcpServerRefs(ctx, "disabled_pipeline")
		require.NoError(t, err)
		require.Empty(t, refs, "disabled configs should be excluded")
	})

	t.Run("merges refs from multiple configs", func(t *testing.T) {
		refs1 := `[{"mcp_server_id":"ms-003","name":"prometheus","base_url":"http://prometheus:8080","allowed_tools":["query_metrics"]}]`
		refs2 := `[{"mcp_server_id":"ms-004","name":"loki","base_url":"http://loki:8080","allowed_tools":["query_logs"]}]`
		require.NoError(t, s.McpServerConfig().Create(ctx, &model.McpServerConfigM{
			PipelineID:        "multi_mcp_pipeline",
			McpServerName:     "prometheus",
			McpServerRefsJSON: &refs1,
			Enabled:           true,
		}))
		require.NoError(t, s.McpServerConfig().Create(ctx, &model.McpServerConfigM{
			PipelineID:        "multi_mcp_pipeline",
			McpServerName:     "loki",
			McpServerRefsJSON: &refs2,
			Enabled:           true,
		}))

		refs, err := biz.ResolveMcpServerRefs(ctx, "multi_mcp_pipeline")
		require.NoError(t, err)
		require.Len(t, refs, 2)
	})

	t.Run("attaches tool metadata to refs", func(t *testing.T) {
		// Create tool metadata
		kind := "metrics"
		domain := "observability"
		tagsJSON := `["promql","query"]`
		_, err := toolMetadataBiz.Create(ctx, &v1.CreateToolMetadataRequest{
			ToolName:    "prometheus_query",
			Kind:        &kind,
			Domain:      &domain,
			TagsJSON:    &tagsJSON,
			Description: ptrString("Query Prometheus metrics"),
		})
		require.NoError(t, err)

		_, err = toolMetadataBiz.Create(ctx, &v1.CreateToolMetadataRequest{
			ToolName:    "loki_search",
			Kind:        ptrString("logs"),
			Domain:      ptrString("observability"),
			Description: ptrString("Search Loki logs"),
		})
		require.NoError(t, err)

		// Create McpServerConfig with refs that reference the tools
		refsJSON := `[{"mcp_server_id":"ms-meta-001","name":"prometheus","base_url":"http://prometheus:8080","allowed_tools":["prometheus_query","unknown_tool"]},{"mcp_server_id":"ms-meta-002","name":"loki","base_url":"http://loki:8080","allowed_tools":["loki_search"]}]`
		require.NoError(t, s.McpServerConfig().Create(ctx, &model.McpServerConfigM{
			PipelineID:        "metadata_test_pipeline",
			McpServerName:     "mixed",
			McpServerRefsJSON: &refsJSON,
			Enabled:           true,
		}))

		refs, err := biz.ResolveMcpServerRefs(ctx, "metadata_test_pipeline")
		require.NoError(t, err)
		require.Len(t, refs, 2)

		// First ref should have metadata for prometheus_query (unknown_tool has no metadata)
		require.Equal(t, "ms-meta-001", refs[0].McpServerID)
		require.Len(t, refs[0].ToolMetadata, 1, "should have metadata for prometheus_query only")
		require.Equal(t, "prometheus_query", refs[0].ToolMetadata[0].ToolName)
		require.Equal(t, "metrics", refs[0].ToolMetadata[0].Kind)
		require.Equal(t, "observability", refs[0].ToolMetadata[0].Domain)
		require.Equal(t, []string{"promql", "query"}, refs[0].ToolMetadata[0].Tags)
		require.Equal(t, "Query Prometheus metrics", refs[0].ToolMetadata[0].Description)

		// Second ref should have metadata for loki_search
		require.Equal(t, "ms-meta-002", refs[1].McpServerID)
		require.Len(t, refs[1].ToolMetadata, 1)
		require.Equal(t, "loki_search", refs[1].ToolMetadata[0].ToolName)
		require.Equal(t, "logs", refs[1].ToolMetadata[0].Kind)
	})

	t.Run("handles missing tool metadata gracefully", func(t *testing.T) {
		// Create config with tools that have no metadata
		refsJSON := `[{"mcp_server_id":"ms-no-meta","name":"unknown","base_url":"http://unknown:8080","allowed_tools":["nonexistent_tool_1","nonexistent_tool_2"]}]`
		require.NoError(t, s.McpServerConfig().Create(ctx, &model.McpServerConfigM{
			PipelineID:        "no_metadata_pipeline",
			McpServerName:     "unknown",
			McpServerRefsJSON: &refsJSON,
			Enabled:           true,
		}))

		refs, err := biz.ResolveMcpServerRefs(ctx, "no_metadata_pipeline")
		require.NoError(t, err)
		require.Len(t, refs, 1)
		require.Empty(t, refs[0].ToolMetadata, "should have empty metadata when no tools are found")
	})
}

func TestMcpServerRefJSONSerialization(t *testing.T) {
	store.ResetForTest()
	t.Cleanup(store.ResetForTest)

	db := setupTestDB(t)
	s := store.NewStore(db)
	ctx := context.Background()
	toolMetadataBiz := toolmetadata.New(s)
	biz := New(s, toolMetadataBiz)

	t.Run("allowed_tools serialized as json array", func(t *testing.T) {
		tools := []string{"query_metrics", "query_range", "query_logs"}
		toolsJSON, err := json.Marshal(tools)
		require.NoError(t, err)
		toolsJSONStr := string(toolsJSON)

		resp, err := biz.Create(ctx, &v1.CreateMcpServerRequest{
			Name:             "json-test",
			BaseURL:          "http://test.mcp:8080",
			AllowedToolsJSON: &toolsJSONStr,
		})
		require.NoError(t, err)
		require.NotNil(t, resp.McpServer.AllowedToolsJSON)

		// Verify the stored JSON can be parsed back
		require.Contains(t, *resp.McpServer.AllowedToolsJSON, "query_metrics")
		require.Contains(t, *resp.McpServer.AllowedToolsJSON, "query_range")
		require.Contains(t, *resp.McpServer.AllowedToolsJSON, "query_logs")
	})
}

func ptrString(s string) *string {
	return &s
}