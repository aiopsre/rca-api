package mcpserver

//go:generate mockgen -destination mock_mcpserver.go -package mcpserver github.com/aiopsre/rca-api/internal/apiserver/biz/v1/mcpserver McpServerBiz

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/onexstack/onexstack/pkg/errorsx"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	"github.com/aiopsre/rca-api/internal/pkg/contextx"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
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
	Create(ctx context.Context, rq *CreateMcpServerRequest) (*CreateMcpServerResponse, error)
	Get(ctx context.Context, rq *GetMcpServerRequest) (*GetMcpServerResponse, error)
	List(ctx context.Context, rq *ListMcpServersRequest) (*ListMcpServersResponse, error)
	Update(ctx context.Context, rq *UpdateMcpServerRequest) (*UpdateMcpServerResponse, error)
	Delete(ctx context.Context, rq *DeleteMcpServerRequest) error
	// ResolveMcpServerRefs resolves MCP server references for a given pipeline.
	ResolveMcpServerRefs(ctx context.Context, pipelineID string) ([]model.McpServerRef, error)

	McpServerExpansion
}

//nolint:modernize // Keep explicit empty interface as placeholder expansion point.
type McpServerExpansion interface{}

type mcpServerBiz struct {
	store store.IStore
}

var _ McpServerBiz = (*mcpServerBiz)(nil)

// New creates MCP server biz.
func New(store store.IStore) *mcpServerBiz {
	return &mcpServerBiz{store: store}
}

// CreateMcpServerRequest defines the request for creating an MCP server.
type CreateMcpServerRequest struct {
	Name          string
	DisplayName   *string
	Description   *string
	BaseURL       string
	AuthType      string
	AuthSecretRef *string
	AllowedTools  []string
	TimeoutSec    int
	Scopes        *string
	CreatedBy     *string
}

// CreateMcpServerResponse defines the response for creating an MCP server.
type CreateMcpServerResponse struct {
	McpServer *model.McpServerM `json:"mcp_server"`
}

// GetMcpServerRequest defines the request for getting an MCP server.
type GetMcpServerRequest struct {
	McpServerID string
}

// GetMcpServerResponse defines the response for getting an MCP server.
type GetMcpServerResponse struct {
	McpServer *model.McpServerM `json:"mcp_server"`
}

// ListMcpServersRequest defines the request for listing MCP servers.
type ListMcpServersRequest struct {
	Name   *string
	Status *string
	Offset int64
	Limit  *int64
}

// ListMcpServersResponse defines the response for listing MCP servers.
type ListMcpServersResponse struct {
	TotalCount int64                `json:"total_count"`
	McpServers []*model.McpServerM `json:"mcp_servers"`
}

// UpdateMcpServerRequest defines the request for updating an MCP server.
type UpdateMcpServerRequest struct {
	McpServerID   string
	DisplayName   *string
	Description   *string
	BaseURL       *string
	AuthType      *string
	AuthSecretRef *string
	AllowedTools  []string
	TimeoutSec    *int
	Scopes        *string
	Status        *string
	UpdatedBy     *string
}

// UpdateMcpServerResponse defines the response for updating an MCP server.
type UpdateMcpServerResponse struct {
	McpServer *model.McpServerM `json:"mcp_server"`
}

// DeleteMcpServerRequest defines the request for deleting an MCP server.
type DeleteMcpServerRequest struct {
	McpServerID string
}

func (b *mcpServerBiz) Create(ctx context.Context, rq *CreateMcpServerRequest) (*CreateMcpServerResponse, error) {
	if rq == nil || strings.TrimSpace(rq.Name) == "" || strings.TrimSpace(rq.BaseURL) == "" {
		return nil, errorsx.ErrInvalidArgument
	}

	name := strings.TrimSpace(rq.Name)

	// Check if name already exists
	existing, err := b.store.McpServer().Get(ctx, where.T(ctx).F("name", name))
	if err == nil && existing != nil {
		return nil, errno.ErrMcpServerAlreadyExists
	}
	if err != nil && !isRecordNotFound(err) {
		return nil, errno.ErrMcpServerGetFailed
	}

	authType := "none"
	if rq.AuthType != "" {
		authType = strings.ToLower(strings.TrimSpace(rq.AuthType))
	}
	timeoutSec := 10
	if rq.TimeoutSec > 0 {
		timeoutSec = rq.TimeoutSec
	}

	var allowedToolsJSON *string
	if len(rq.AllowedTools) > 0 {
		data, err := json.Marshal(rq.AllowedTools)
		if err != nil {
			return nil, errorsx.ErrInvalidArgument
		}
		v := string(data)
		allowedToolsJSON = &v
	}

	createdBy := normalizeCreatedBy(ctx, rq.CreatedBy)

	obj := &model.McpServerM{
		Name:          name,
		DisplayName:   trimStringPtr(rq.DisplayName),
		Description:   trimStringPtr(rq.Description),
		BaseURL:       strings.TrimSpace(rq.BaseURL),
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

	return &CreateMcpServerResponse{McpServer: obj}, nil
}

func (b *mcpServerBiz) Get(ctx context.Context, rq *GetMcpServerRequest) (*GetMcpServerResponse, error) {
	if rq == nil || strings.TrimSpace(rq.McpServerID) == "" {
		return nil, errorsx.ErrInvalidArgument
	}

	m, err := b.store.McpServer().Get(ctx, where.T(ctx).F("mcp_server_id", strings.TrimSpace(rq.McpServerID)))
	if err != nil {
		return nil, toMcpServerGetError(err)
	}
	return &GetMcpServerResponse{McpServer: m}, nil
}

func (b *mcpServerBiz) List(ctx context.Context, rq *ListMcpServersRequest) (*ListMcpServersResponse, error) {
	if rq == nil {
		rq = &ListMcpServersRequest{}
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

	return &ListMcpServersResponse{
		TotalCount: total,
		McpServers: list,
	}, nil
}

func (b *mcpServerBiz) Update(ctx context.Context, rq *UpdateMcpServerRequest) (*UpdateMcpServerResponse, error) {
	if rq == nil || strings.TrimSpace(rq.McpServerID) == "" {
		return nil, errorsx.ErrInvalidArgument
	}

	obj, err := b.store.McpServer().Get(ctx, where.T(ctx).F("mcp_server_id", strings.TrimSpace(rq.McpServerID)))
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
	if rq.AllowedTools != nil {
		if len(rq.AllowedTools) > 0 {
			data, err := json.Marshal(rq.AllowedTools)
			if err != nil {
				return nil, errorsx.ErrInvalidArgument
			}
			v := string(data)
			obj.AllowedTools = &v
		} else {
			obj.AllowedTools = nil
		}
	}
	if rq.TimeoutSec != nil && *rq.TimeoutSec > 0 {
		obj.TimeoutSec = *rq.TimeoutSec
	}
	if rq.Scopes != nil {
		obj.Scopes = trimStringPtr(rq.Scopes)
	}
	if rq.Status != nil {
		obj.Status = strings.ToLower(strings.TrimSpace(*rq.Status))
	}
	_ = normalizeCreatedBy(ctx, rq.UpdatedBy) // Track who made the update for audit
	obj.UpdatedAt = time.Now()

	if err := b.store.McpServer().Update(ctx, obj); err != nil {
		return nil, errno.ErrMcpServerUpdateFailed
	}

	return &UpdateMcpServerResponse{McpServer: obj}, nil
}

func (b *mcpServerBiz) Delete(ctx context.Context, rq *DeleteMcpServerRequest) error {
	if rq == nil || strings.TrimSpace(rq.McpServerID) == "" {
		return errorsx.ErrInvalidArgument
	}

	_, err := b.store.McpServer().Get(ctx, where.T(ctx).F("mcp_server_id", strings.TrimSpace(rq.McpServerID)))
	if err != nil {
		return toMcpServerGetError(err)
	}

	if err := b.store.McpServer().Delete(ctx, where.T(ctx).F("mcp_server_id", strings.TrimSpace(rq.McpServerID))); err != nil {
		return errno.ErrMcpServerDeleteFailed
	}
	return nil
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

	return refs, nil
}


func normalizeListLimit(limit *int64) int64 {
	if limit == nil || *limit <= 0 {
		return defaultListLimit
	}
	if *limit > maxListLimit {
		return maxListLimit
	}
	return *limit
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