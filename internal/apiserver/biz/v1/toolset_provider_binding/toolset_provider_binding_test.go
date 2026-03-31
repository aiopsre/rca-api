package toolset_provider_binding

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/testsupport"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

func seedMcpServer(t *testing.T, s *testsupport.MemoryStore, name string) string {
	t.Helper()
	obj := &model.McpServerM{
		Name:    name,
		BaseURL: "http://" + name + ".mcp:8080",
		Status:  "active",
	}
	require.NoError(t, s.McpServer().Create(context.Background(), obj))
	return obj.McpServerID
}

func TestToolsetProviderBindingBiz_CRUD(t *testing.T) {
	ctx := context.Background()
	s := testsupport.NewMemoryStore()
	mcpServerID := seedMcpServer(t, s, "tempo")
	biz := New(s)

	allowedToolsJSON := `["tempo_query","tempo_get_trace"]`
	createResp, err := biz.Create(ctx, &v1.CreateToolsetProviderBindingRequest{
		ToolsetName:      "observability_tempo",
		McpServerID:      "mcp-xxxxxx",
		AllowedToolsJSON: &allowedToolsJSON,
		Priority:         int32Ptr(10),
		Enabled:          boolPtr(true),
	})
	require.Error(t, err)
	require.Equal(t, errno.ErrMcpServerNotFound, err)

	createResp, err = biz.Create(ctx, &v1.CreateToolsetProviderBindingRequest{
		ToolsetName:      "observability_tempo",
		McpServerID:      mcpServerID,
		AllowedToolsJSON: &allowedToolsJSON,
		Priority:         int32Ptr(10),
		Enabled:          boolPtr(true),
	})
	require.NoError(t, err)
	require.NotNil(t, createResp)
	require.Equal(t, "observability_tempo", createResp.ToolsetProviderBinding.ToolsetName)
	require.JSONEq(t, allowedToolsJSON, *createResp.ToolsetProviderBinding.AllowedToolsJSON)
	require.Equal(t, int32(10), createResp.ToolsetProviderBinding.Priority)
	require.True(t, createResp.ToolsetProviderBinding.Enabled)

	got, err := biz.Get(ctx, &v1.GetToolsetProviderBindingRequest{
		ToolsetName: "observability_tempo",
		McpServerID: mcpServerID,
	})
	require.NoError(t, err)
	require.Equal(t, createResp.ToolsetProviderBinding.Id, got.ToolsetProviderBinding.Id)

	list, err := biz.List(ctx, &v1.ListToolsetProviderBindingsRequest{})
	require.NoError(t, err)
	require.Equal(t, int64(1), list.TotalCount)
	require.Len(t, list.ToolsetProviderBindings, 1)

	updatedToolsJSON := `["tempo_query"]`
	updateResp, err := biz.Update(ctx, &v1.UpdateToolsetProviderBindingRequest{
		ToolsetName:      "observability_tempo",
		McpServerID:      mcpServerID,
		AllowedToolsJSON: &updatedToolsJSON,
		Priority:         int32Ptr(5),
		Enabled:          boolPtr(false),
	})
	require.NoError(t, err)
	require.NotNil(t, updateResp)
	require.Equal(t, int32(5), updateResp.ToolsetProviderBinding.Priority)
	require.False(t, updateResp.ToolsetProviderBinding.Enabled)

	_, err = biz.Delete(ctx, &v1.DeleteToolsetProviderBindingRequest{
		ToolsetName: "observability_tempo",
		McpServerID: mcpServerID,
	})
	require.NoError(t, err)

	_, err = biz.Get(ctx, &v1.GetToolsetProviderBindingRequest{
		ToolsetName: "observability_tempo",
		McpServerID: mcpServerID,
	})
	require.Error(t, err)
	require.Equal(t, errno.ErrToolsetProviderBindingNotFound, err)
}

func TestToolsetProviderBindingBiz_ListFiltersAndSorts(t *testing.T) {
	ctx := context.Background()
	s := testsupport.NewMemoryStore()
	mcpA := seedMcpServer(t, s, "tempo-a")
	mcpB := seedMcpServer(t, s, "tempo-b")
	biz := New(s)

	binding1 := `["tempo_query"]`
	binding2 := `["tempo_get_trace"]`
	_, err := biz.Create(ctx, &v1.CreateToolsetProviderBindingRequest{
		ToolsetName:      "observability_tempo",
		McpServerID:      mcpB,
		AllowedToolsJSON: &binding2,
		Priority:         int32Ptr(20),
		Enabled:          boolPtr(true),
	})
	require.NoError(t, err)
	_, err = biz.Create(ctx, &v1.CreateToolsetProviderBindingRequest{
		ToolsetName:      "observability_tempo",
		McpServerID:      mcpA,
		AllowedToolsJSON: &binding1,
		Priority:         int32Ptr(10),
		Enabled:          boolPtr(false),
	})
	require.NoError(t, err)

	list, err := biz.List(ctx, &v1.ListToolsetProviderBindingsRequest{
		ToolsetName: strPtr("observability_tempo"),
		Enabled:     boolPtr(true),
	})
	require.NoError(t, err)
	require.Len(t, list.ToolsetProviderBindings, 1)
	require.Equal(t, mcpB, list.ToolsetProviderBindings[0].McpServerID)
}

func TestToolsetProviderBindingBiz_CreateRejectsInvalidJSON(t *testing.T) {
	ctx := context.Background()
	s := testsupport.NewMemoryStore()
	mcpServerID := seedMcpServer(t, s, "tempo")
	biz := New(s)

	invalid := `{"not":"array"}`
	_, err := biz.Create(ctx, &v1.CreateToolsetProviderBindingRequest{
		ToolsetName:      "observability_tempo",
		McpServerID:      mcpServerID,
		AllowedToolsJSON: &invalid,
	})
	require.Error(t, err)
}

func TestToolsetProviderBindingBiz_NormalizesJSON(t *testing.T) {
	ctx := context.Background()
	s := testsupport.NewMemoryStore()
	mcpServerID := seedMcpServer(t, s, "tempo")
	biz := New(s)

	raw := "[\n  \"tempo_query\", \"tempo_get_trace\"\n]"
	resp, err := biz.Create(ctx, &v1.CreateToolsetProviderBindingRequest{
		ToolsetName:      "observability_tempo",
		McpServerID:      mcpServerID,
		AllowedToolsJSON: &raw,
	})
	require.NoError(t, err)
	require.NotNil(t, resp.ToolsetProviderBinding.AllowedToolsJSON)
	require.JSONEq(t, `["tempo_query","tempo_get_trace"]`, *resp.ToolsetProviderBinding.AllowedToolsJSON)
}

func int32Ptr(v int32) *int32 { return &v }
func boolPtr(v bool) *bool { return &v }
func strPtr(v string) *string { return &v }
