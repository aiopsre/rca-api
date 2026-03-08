package mcpserver

//go:generate mockgen -destination mock_mcpserver.go -package mcpserver github.com/aiopsre/rca-api/internal/apiserver/biz/v1/mcpserver McpServerBiz

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/onexstack/onexstack/pkg/errorsx"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/aiopsre/rca-api/internal/apiserver/biz/v1/toolmetadata"
	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	"github.com/aiopsre/rca-api/internal/pkg/contextx"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

const (
	defaultListLimit = int64(20)
	maxListLimit     = int64(200)
)

// McpServerBiz defines MCP server management use-cases.
//
//nolint:interfacebloat // CRUD + list intentionally grouped in one biz entrypoint.
type McpServerBiz interface {
	Create(ctx context.Context, rq *v1.CreateMcpServerRequest) (*v1.CreateMcpServerResponse, error)
	Get(ctx context.Context, rq *v1.GetMcpServerRequest) (*v1.GetMcpServerResponse, error)
	List(ctx context.Context, rq *v1.ListMcpServersRequest) (*v1.ListMcpServersResponse, error)
	Update(ctx context.Context, rq *v1.UpdateMcpServerRequest) (*v1.UpdateMcpServerResponse, error)
	Delete(ctx context.Context, rq *v1.DeleteMcpServerRequest) (*v1.DeleteMcpServerResponse, error)
	// ResolveMcpServerRefs resolves MCP server references for a given pipeline.
	ResolveMcpServerRefs(ctx context.Context, pipelineID string) ([]model.McpServerRef, error)

	McpServerExpansion
}

//nolint:modernize // Keep explicit empty interface as placeholder expansion point.
type McpServerExpansion interface{}

type mcpServerBiz struct {
	store           store.IStore
	toolMetadataBiz toolmetadata.ToolMetadataBiz
}

var _ McpServerBiz = (*mcpServerBiz)(nil)

// New creates MCP server biz.
func New(store store.IStore, toolMetadataBiz toolmetadata.ToolMetadataBiz) *mcpServerBiz {
	return &mcpServerBiz{store: store, toolMetadataBiz: toolMetadataBiz}
}

func (b *mcpServerBiz) Create(ctx context.Context, rq *v1.CreateMcpServerRequest) (*v1.CreateMcpServerResponse, error) {
	if rq == nil || strings.TrimSpace(rq.GetName()) == "" || strings.TrimSpace(rq.GetBaseURL()) == "" {
		return nil, errorsx.ErrInvalidArgument
	}

	name := strings.TrimSpace(rq.GetName())

	// Check if name already exists
	existing, err := b.store.McpServer().Get(ctx, where.T(ctx).F("name", name))
	if err == nil && existing != nil {
		return nil, errno.ErrMcpServerAlreadyExists
	}
	if err != nil && !isRecordNotFound(err) {
		return nil, errno.ErrMcpServerGetFailed
	}

	authType := "none"
	if rq.AuthType != nil && strings.TrimSpace(*rq.AuthType) != "" {
		authType = strings.ToLower(strings.TrimSpace(*rq.AuthType))
	}
	timeoutSec := 10
	if rq.TimeoutSec != nil && *rq.TimeoutSec > 0 {
		timeoutSec = int(*rq.TimeoutSec)
	}

	var allowedToolsJSON *string
	if rq.AllowedToolsJSON != nil && strings.TrimSpace(*rq.AllowedToolsJSON) != "" {
		v := strings.TrimSpace(*rq.AllowedToolsJSON)
		allowedToolsJSON = &v
	}

	createdBy := normalizeCreatedBy(ctx, nil)

	obj := &model.McpServerM{
		Name:          name,
		DisplayName:   trimStringPtr(rq.DisplayName),
		Description:   trimStringPtr(rq.Description),
		BaseURL:       strings.TrimSpace(rq.GetBaseURL()),
		AuthType:      authType,
		AuthSecretRef: trimStringPtr(rq.AuthSecretRef),
		AllowedTools:  allowedToolsJSON,
		TimeoutSec:    timeoutSec,
		Scopes:        trimStringPtr(rq.Scopes),
		Status:        "active",
		CreatedBy:     createdBy,
	}

	if err := b.store.McpServer().Create(ctx, obj); err != nil {
		return nil, errno.ErrMcpServerCreateFailed
	}

	return &v1.CreateMcpServerResponse{McpServer: modelToProto(obj)}, nil
}

func (b *mcpServerBiz) Get(ctx context.Context, rq *v1.GetMcpServerRequest) (*v1.GetMcpServerResponse, error) {
	if rq == nil || strings.TrimSpace(rq.GetMcpServerID()) == "" {
		return nil, errorsx.ErrInvalidArgument
	}

	m, err := b.store.McpServer().Get(ctx, where.T(ctx).F("mcp_server_id", strings.TrimSpace(rq.GetMcpServerID())))
	if err != nil {
		return nil, toMcpServerGetError(err)
	}
	return &v1.GetMcpServerResponse{McpServer: modelToProto(m)}, nil
}

func (b *mcpServerBiz) List(ctx context.Context, rq *v1.ListMcpServersRequest) (*v1.ListMcpServersResponse, error) {
	if rq == nil {
		rq = &v1.ListMcpServersRequest{}
	}

	limit := normalizeListLimit(rq.Limit)
	whr := where.T(ctx).O(int(rq.Offset)).L(int(limit))

	if rq.Name != nil && strings.TrimSpace(*rq.Name) != "" {
		whr = whr.F("name", strings.TrimSpace(*rq.Name))
	}
	if rq.Status != nil && strings.TrimSpace(*rq.Status) != "" {
		whr = whr.F("status", strings.ToLower(strings.TrimSpace(*rq.Status)))
	}

	total, list, err := b.store.McpServer().List(ctx, whr)
	if err != nil {
		return nil, errno.ErrMcpServerListFailed
	}

	protoList := make([]*v1.McpServer, 0, len(list))
	for _, m := range list {
		protoList = append(protoList, modelToProto(m))
	}

	return &v1.ListMcpServersResponse{
		TotalCount: total,
		McpServers: protoList,
	}, nil
}

func (b *mcpServerBiz) Update(ctx context.Context, rq *v1.UpdateMcpServerRequest) (*v1.UpdateMcpServerResponse, error) {
	if rq == nil || strings.TrimSpace(rq.GetMcpServerID()) == "" {
		return nil, errorsx.ErrInvalidArgument
	}

	obj, err := b.store.McpServer().Get(ctx, where.T(ctx).F("mcp_server_id", strings.TrimSpace(rq.GetMcpServerID())))
	if err != nil {
		return nil, toMcpServerGetError(err)
	}

	if rq.DisplayName != nil {
		obj.DisplayName = trimStringPtr(rq.DisplayName)
	}
	if rq.Description != nil {
		obj.Description = trimStringPtr(rq.Description)
	}
	if rq.BaseURL != nil && strings.TrimSpace(*rq.BaseURL) != "" {
		obj.BaseURL = strings.TrimSpace(*rq.BaseURL)
	}
	if rq.AuthType != nil {
		obj.AuthType = strings.ToLower(strings.TrimSpace(*rq.AuthType))
	}
	if rq.AuthSecretRef != nil {
		obj.AuthSecretRef = trimStringPtr(rq.AuthSecretRef)
	}
	if rq.AllowedToolsJSON != nil {
		v := strings.TrimSpace(*rq.AllowedToolsJSON)
		if v != "" {
			obj.AllowedTools = &v
		} else {
			obj.AllowedTools = nil
		}
	}
	if rq.TimeoutSec != nil && *rq.TimeoutSec > 0 {
		obj.TimeoutSec = int(*rq.TimeoutSec)
	}
	if rq.Scopes != nil {
		obj.Scopes = trimStringPtr(rq.Scopes)
	}
	if rq.Status != nil {
		obj.Status = strings.ToLower(strings.TrimSpace(*rq.Status))
	}
	obj.UpdatedAt = time.Now()

	if err := b.store.McpServer().Update(ctx, obj); err != nil {
		return nil, errno.ErrMcpServerUpdateFailed
	}

	return &v1.UpdateMcpServerResponse{McpServer: modelToProto(obj)}, nil
}

func (b *mcpServerBiz) Delete(ctx context.Context, rq *v1.DeleteMcpServerRequest) (*v1.DeleteMcpServerResponse, error) {
	if rq == nil || strings.TrimSpace(rq.GetMcpServerID()) == "" {
		return nil, errorsx.ErrInvalidArgument
	}

	_, err := b.store.McpServer().Get(ctx, where.T(ctx).F("mcp_server_id", strings.TrimSpace(rq.GetMcpServerID())))
	if err != nil {
		return nil, toMcpServerGetError(err)
	}

	if err := b.store.McpServer().Delete(ctx, where.T(ctx).F("mcp_server_id", strings.TrimSpace(rq.GetMcpServerID()))); err != nil {
		return nil, errno.ErrMcpServerDeleteFailed
	}
	return &v1.DeleteMcpServerResponse{}, nil
}

// ResolveMcpServerRefs resolves MCP server references for a given pipeline.
// This is used by the orchestrator_skillset package to build McpServerRef array for dispatch.
func (b *mcpServerBiz) ResolveMcpServerRefs(ctx context.Context, pipelineID string) ([]model.McpServerRef, error) {
	if strings.TrimSpace(pipelineID) == "" {
		return nil, nil
	}

	// Get McpServerConfigM bindings for this pipeline
	total, configs, err := b.store.McpServerConfig().List(ctx, where.T(ctx).F("pipeline_id", strings.TrimSpace(pipelineID)).F("enabled", true))
	if err != nil {
		return nil, errno.ErrMcpServerConfigListFailed
	}
	if total == 0 {
		return nil, nil
	}

	var refs []model.McpServerRef
	for _, config := range configs {
		// Parse McpServerRefsJSON if present
		if config.McpServerRefsJSON != nil && *config.McpServerRefsJSON != "" {
			var configRefs []model.McpServerRef
			if err := json.Unmarshal([]byte(*config.McpServerRefsJSON), &configRefs); err == nil {
				refs = append(refs, configRefs...)
			}
		}
	}

	// Collect all tool names from refs
	allTools := make(map[string]struct{})
	for _, ref := range refs {
		for _, tool := range ref.AllowedTools {
			allTools[tool] = struct{}{}
		}
	}

	// Batch get tool metadata
	toolNames := make([]string, 0, len(allTools))
	for tool := range allTools {
		toolNames = append(toolNames, tool)
	}

	metadataMap := make(map[string]*model.ToolMetadataM)
	if b.toolMetadataBiz != nil && len(toolNames) > 0 {
		var err error
		metadataMap, err = b.toolMetadataBiz.BatchGetMap(ctx, toolNames)
		if err != nil {
			// Log warning but continue - metadata is optional
			slog.Warn("failed to get tool metadata", "error", err, "pipeline_id", pipelineID)
		}
	}

	// Attach metadata to each ref
	for i := range refs {
		refs[i].ToolMetadata = toolmetadata.BuildToolMetadataRefs(refs[i].AllowedTools, metadataMap)
	}

	return refs, nil
}

func modelToProto(m *model.McpServerM) *v1.McpServer {
	if m == nil {
		return nil
	}
	return &v1.McpServer{
		McpServerID:     m.McpServerID,
		Name:            m.Name,
		DisplayName:     m.DisplayName,
		Description:     m.Description,
		BaseURL:         m.BaseURL,
		AuthType:        m.AuthType,
		AuthSecretRef:   m.AuthSecretRef,
		AllowedToolsJSON: m.AllowedTools,
		TimeoutSec:      int32(m.TimeoutSec),
		Scopes:          m.Scopes,
		Status:          m.Status,
		CreatedBy:       m.CreatedBy,
		CreatedAt:       timestamppb.New(m.CreatedAt),
		UpdatedAt:       timestamppb.New(m.UpdatedAt),
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

func normalizeCreatedBy(ctx context.Context, fallback *string) *string {
	if fallback != nil && strings.TrimSpace(*fallback) != "" {
		v := strings.TrimSpace(*fallback)
		return &v
	}
	if user := contextx.Username(ctx); user != "" {
		return &user
	}
	v := "system"
	return &v
}

func trimStringPtr(in *string) *string {
	if in == nil {
		return nil
	}
	value := strings.TrimSpace(*in)
	if value == "" {
		return nil
	}
	return &value
}

func isRecordNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "record not found")
}

func toMcpServerGetError(err error) error {
	if isRecordNotFound(err) {
		return errno.ErrMcpServerNotFound
	}
	return errno.ErrMcpServerGetFailed
}