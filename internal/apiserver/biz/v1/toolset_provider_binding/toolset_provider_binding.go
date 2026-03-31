package toolset_provider_binding

//go:generate mockgen -destination mock_toolset_provider_binding.go -package toolset_provider_binding github.com/aiopsre/rca-api/internal/apiserver/biz/v1/toolset_provider_binding ToolsetProviderBindingBiz

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/onexstack/onexstack/pkg/errorsx"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

const (
	defaultListLimit = int64(20)
	maxListLimit     = int64(200)
)

type ToolsetProviderBindingBiz interface {
	Create(ctx context.Context, rq *v1.CreateToolsetProviderBindingRequest) (*v1.CreateToolsetProviderBindingResponse, error)
	Get(ctx context.Context, rq *v1.GetToolsetProviderBindingRequest) (*v1.GetToolsetProviderBindingResponse, error)
	List(ctx context.Context, rq *v1.ListToolsetProviderBindingsRequest) (*v1.ListToolsetProviderBindingsResponse, error)
	Update(ctx context.Context, rq *v1.UpdateToolsetProviderBindingRequest) (*v1.UpdateToolsetProviderBindingResponse, error)
	Delete(ctx context.Context, rq *v1.DeleteToolsetProviderBindingRequest) (*v1.DeleteToolsetProviderBindingResponse, error)

	ToolsetProviderBindingExpansion
}

type ToolsetProviderBindingExpansion interface{}

type toolsetProviderBindingBiz struct {
	store store.IStore
}

var _ ToolsetProviderBindingBiz = (*toolsetProviderBindingBiz)(nil)

func New(store store.IStore) *toolsetProviderBindingBiz {
	return &toolsetProviderBindingBiz{store: store}
}

func (b *toolsetProviderBindingBiz) Create(ctx context.Context, rq *v1.CreateToolsetProviderBindingRequest) (*v1.CreateToolsetProviderBindingResponse, error) {
	if rq == nil {
		return nil, errorsx.ErrInvalidArgument
	}
	toolsetName := strings.TrimSpace(rq.GetToolsetName())
	mcpServerID := strings.TrimSpace(rq.GetMcpServerID())
	if toolsetName == "" || mcpServerID == "" {
		return nil, errorsx.ErrInvalidArgument
	}

	if existing, err := b.store.ToolsetProviderBinding().Get(ctx, where.T(ctx).F("toolset_name", toolsetName).F("mcp_server_id", mcpServerID)); err == nil && existing != nil {
		return nil, errno.ErrToolsetProviderBindingAlreadyExists
	} else if err != nil && !isRecordNotFound(err) {
		return nil, errno.ErrToolsetProviderBindingGetFailed
	}
	if _, err := b.store.McpServer().Get(ctx, where.T(ctx).F("mcp_server_id", mcpServerID)); err != nil {
		if isRecordNotFound(err) {
			return nil, errno.ErrMcpServerNotFound
		}
		return nil, errno.ErrMcpServerGetFailed
	}

	allowedToolsJSON, err := normalizeAllowedToolsJSON(rq.AllowedToolsJSON)
	if err != nil {
		return nil, errorsx.ErrInvalidArgument
	}

	priority := 0
	if rq.Priority != nil {
		priority = int(*rq.Priority)
	}
	enabled := true
	if rq.Enabled != nil {
		enabled = *rq.Enabled
	}

	obj := &model.ToolsetProviderBinding{
		ToolsetName:      toolsetName,
		McpServerID:      mcpServerID,
		AllowedToolsJSON: allowedToolsJSON,
		Priority:         priority,
		Enabled:          enabled,
	}
	if err := b.store.ToolsetProviderBinding().Create(ctx, obj); err != nil {
		return nil, errno.ErrToolsetProviderBindingCreateFailed
	}
	return &v1.CreateToolsetProviderBindingResponse{ToolsetProviderBinding: modelToProto(obj)}, nil
}

func (b *toolsetProviderBindingBiz) Get(ctx context.Context, rq *v1.GetToolsetProviderBindingRequest) (*v1.GetToolsetProviderBindingResponse, error) {
	if rq == nil {
		return nil, errorsx.ErrInvalidArgument
	}
	toolsetName := strings.TrimSpace(rq.GetToolsetName())
	mcpServerID := strings.TrimSpace(rq.GetMcpServerID())
	if toolsetName == "" || mcpServerID == "" {
		return nil, errorsx.ErrInvalidArgument
	}
	m, err := b.store.ToolsetProviderBinding().Get(ctx, where.T(ctx).F("toolset_name", toolsetName).F("mcp_server_id", mcpServerID))
	if err != nil {
		return nil, toBindingGetError(err)
	}
	return &v1.GetToolsetProviderBindingResponse{ToolsetProviderBinding: modelToProto(m)}, nil
}

func (b *toolsetProviderBindingBiz) List(ctx context.Context, rq *v1.ListToolsetProviderBindingsRequest) (*v1.ListToolsetProviderBindingsResponse, error) {
	if rq == nil {
		rq = &v1.ListToolsetProviderBindingsRequest{}
	}
	limit := normalizeListLimit(rq.Limit)
	whr := where.T(ctx).O(int(rq.Offset)).L(int(limit))
	if rq.ToolsetName != nil && strings.TrimSpace(*rq.ToolsetName) != "" {
		whr = whr.F("toolset_name", strings.TrimSpace(*rq.ToolsetName))
	}
	if rq.McpServerID != nil && strings.TrimSpace(*rq.McpServerID) != "" {
		whr = whr.F("mcp_server_id", strings.TrimSpace(*rq.McpServerID))
	}
	if rq.Enabled != nil {
		whr = whr.F("enabled", *rq.Enabled)
	}
	total, list, err := b.store.ToolsetProviderBinding().List(ctx, whr)
	if err != nil {
		return nil, errno.ErrToolsetProviderBindingListFailed
	}
	protoList := make([]*v1.ToolsetProviderBinding, 0, len(list))
	for _, m := range list {
		protoList = append(protoList, modelToProto(m))
	}
	return &v1.ListToolsetProviderBindingsResponse{
		TotalCount:              total,
		ToolsetProviderBindings: protoList,
	}, nil
}

func (b *toolsetProviderBindingBiz) Update(ctx context.Context, rq *v1.UpdateToolsetProviderBindingRequest) (*v1.UpdateToolsetProviderBindingResponse, error) {
	if rq == nil {
		return nil, errorsx.ErrInvalidArgument
	}
	toolsetName := strings.TrimSpace(rq.GetToolsetName())
	mcpServerID := strings.TrimSpace(rq.GetMcpServerID())
	if toolsetName == "" || mcpServerID == "" {
		return nil, errorsx.ErrInvalidArgument
	}
	if rq.AllowedToolsJSON == nil && rq.Priority == nil && rq.Enabled == nil {
		return nil, errorsx.ErrInvalidArgument
	}

	obj, err := b.store.ToolsetProviderBinding().Get(ctx, where.T(ctx).F("toolset_name", toolsetName).F("mcp_server_id", mcpServerID))
	if err != nil {
		return nil, toBindingGetError(err)
	}
	if rq.AllowedToolsJSON != nil {
		allowedToolsJSON, err := normalizeAllowedToolsJSON(rq.AllowedToolsJSON)
		if err != nil {
			return nil, errorsx.ErrInvalidArgument
		}
		obj.AllowedToolsJSON = allowedToolsJSON
	}
	if rq.Priority != nil {
		obj.Priority = int(*rq.Priority)
	}
	if rq.Enabled != nil {
		obj.Enabled = *rq.Enabled
	}
	obj.UpdatedAt = time.Now()
	if err := b.store.ToolsetProviderBinding().Update(ctx, obj); err != nil {
		return nil, errno.ErrToolsetProviderBindingUpdateFailed
	}
	return &v1.UpdateToolsetProviderBindingResponse{ToolsetProviderBinding: modelToProto(obj)}, nil
}

func (b *toolsetProviderBindingBiz) Delete(ctx context.Context, rq *v1.DeleteToolsetProviderBindingRequest) (*v1.DeleteToolsetProviderBindingResponse, error) {
	if rq == nil {
		return nil, errorsx.ErrInvalidArgument
	}
	toolsetName := strings.TrimSpace(rq.GetToolsetName())
	mcpServerID := strings.TrimSpace(rq.GetMcpServerID())
	if toolsetName == "" || mcpServerID == "" {
		return nil, errorsx.ErrInvalidArgument
	}
	if _, err := b.store.ToolsetProviderBinding().Get(ctx, where.T(ctx).F("toolset_name", toolsetName).F("mcp_server_id", mcpServerID)); err != nil {
		return nil, toBindingGetError(err)
	}
	if err := b.store.ToolsetProviderBinding().Delete(ctx, where.T(ctx).F("toolset_name", toolsetName).F("mcp_server_id", mcpServerID)); err != nil {
		return nil, errno.ErrToolsetProviderBindingDeleteFailed
	}
	return &v1.DeleteToolsetProviderBindingResponse{}, nil
}

func modelToProto(m *model.ToolsetProviderBinding) *v1.ToolsetProviderBinding {
	if m == nil {
		return nil
	}
	return &v1.ToolsetProviderBinding{
		Id:               m.ID,
		ToolsetName:      m.ToolsetName,
		McpServerID:      m.McpServerID,
		AllowedToolsJSON: m.AllowedToolsJSON,
		Priority:         int32(m.Priority),
		Enabled:          m.Enabled,
		CreatedAt:        timestamppb.New(m.CreatedAt),
		UpdatedAt:        timestamppb.New(m.UpdatedAt),
	}
}

func normalizeListLimit(limit int64) int64 {
	if limit <= 0 {
		return defaultListLimit
	}
	if limit > maxListLimit {
		return maxListLimit
	}
	return limit
}

func normalizeAllowedToolsJSON(raw *string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*raw)
	if trimmed == "" {
		return nil, nil
	}
	var tools []string
	if err := json.Unmarshal([]byte(trimmed), &tools); err != nil {
		return nil, err
	}
	for i := range tools {
		tools[i] = strings.TrimSpace(tools[i])
		if tools[i] == "" {
			return nil, errorsx.ErrInvalidArgument
		}
	}
	normalized, err := json.Marshal(tools)
	if err != nil {
		return nil, err
	}
	s := string(normalized)
	return &s, nil
}

func isRecordNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "record not found")
}

func toBindingGetError(err error) error {
	if isRecordNotFound(err) {
		return errno.ErrToolsetProviderBindingNotFound
	}
	return errno.ErrToolsetProviderBindingGetFailed
}
