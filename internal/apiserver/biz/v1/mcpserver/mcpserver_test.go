package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aiopsre/rca-api/internal/apiserver/testsupport"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

func TestMcpServerBiz_CRUD(t *testing.T) {
	ctx := context.Background()
	biz := New(testsupport.NewMemoryStore())

	allowedToolsJSON := `["query_metrics", "query_range"]`
	createResp, err := biz.Create(ctx, &v1.CreateMcpServerRequest{
		Name:             "prometheus",
		BaseURL:          "http://prometheus.mcp:8080",
		AllowedToolsJSON: &allowedToolsJSON,
	})
	require.NoError(t, err)
	require.NotNil(t, createResp)
	require.NotEmpty(t, createResp.McpServer.McpServerID)
	require.Equal(t, "prometheus", createResp.McpServer.Name)
	require.JSONEq(t, allowedToolsJSON, *createResp.McpServer.AllowedToolsJSON)

	got, err := biz.Get(ctx, &v1.GetMcpServerRequest{McpServerID: createResp.McpServer.McpServerID})
	require.NoError(t, err)
	require.Equal(t, createResp.McpServer.Name, got.McpServer.Name)

	list, err := biz.List(ctx, &v1.ListMcpServersRequest{})
	require.NoError(t, err)
	require.Equal(t, int64(1), list.TotalCount)
	require.Len(t, list.McpServers, 1)

	newStatus := "inactive"
	updateResp, err := biz.Update(ctx, &v1.UpdateMcpServerRequest{
		McpServerID: createResp.McpServer.McpServerID,
		Status:      &newStatus,
	})
	require.NoError(t, err)
	require.Equal(t, "inactive", updateResp.McpServer.Status)

	_, err = biz.Delete(ctx, &v1.DeleteMcpServerRequest{McpServerID: createResp.McpServer.McpServerID})
	require.NoError(t, err)

	_, err = biz.Get(ctx, &v1.GetMcpServerRequest{McpServerID: createResp.McpServer.McpServerID})
	require.Error(t, err)
	require.Equal(t, errno.ErrMcpServerNotFound, err)
}

func TestMcpServerBiz_NormalizesAllowedToolsJSON(t *testing.T) {
	ctx := context.Background()
	biz := New(testsupport.NewMemoryStore())

	raw := "[\n  \"tool1\",  \"tool2\"\n]"
	resp, err := biz.Create(ctx, &v1.CreateMcpServerRequest{
		Name:             "json-test",
		BaseURL:          "http://json-test.mcp:8080",
		AllowedToolsJSON: &raw,
	})
	require.NoError(t, err)
	require.NotNil(t, resp.McpServer.AllowedToolsJSON)
	require.JSONEq(t, `["tool1","tool2"]`, *resp.McpServer.AllowedToolsJSON)
}

func TestMcpServerBiz_CreateRejectsDuplicateName(t *testing.T) {
	ctx := context.Background()
	biz := New(testsupport.NewMemoryStore())

	_, err := biz.Create(ctx, &v1.CreateMcpServerRequest{
		Name:    "dup",
		BaseURL: "http://dup.mcp:8080",
	})
	require.NoError(t, err)

	_, err = biz.Create(ctx, &v1.CreateMcpServerRequest{
		Name:    "dup",
		BaseURL: "http://dup2.mcp:8080",
	})
	require.Error(t, err)
	require.Equal(t, errno.ErrMcpServerAlreadyExists, err)
}

func TestMcpServerBiz_CreateRejectsInvalidAllowedToolsJSON(t *testing.T) {
	ctx := context.Background()
	biz := New(testsupport.NewMemoryStore())

	invalid := `{"not":"array"}`
	_, err := biz.Create(ctx, &v1.CreateMcpServerRequest{
		Name:             "invalid-json",
		BaseURL:          "http://invalid.mcp:8080",
		AllowedToolsJSON: &invalid,
	})
	require.Error(t, err)
}

func TestMcpServerBiz_CreatePreservesAllowedToolsJSONArray(t *testing.T) {
	ctx := context.Background()
	biz := New(testsupport.NewMemoryStore())

	compact, err := json.Marshal([]string{"tool1"})
	require.NoError(t, err)
	raw := string(compact)
	resp, err := biz.Create(ctx, &v1.CreateMcpServerRequest{
		Name:             "compact",
		BaseURL:          "http://compact.mcp:8080",
		AllowedToolsJSON: &raw,
	})
	require.NoError(t, err)
	require.NotNil(t, resp.McpServer.AllowedToolsJSON)
	require.JSONEq(t, `["tool1"]`, *resp.McpServer.AllowedToolsJSON)
}
