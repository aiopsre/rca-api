package toolmetadata

//go:generate mockgen -destination mock_toolmetadata.go -package toolmetadata github.com/aiopsre/rca-api/internal/apiserver/biz/v1/toolmetadata ToolMetadataBiz

import (
	"context"
	"encoding/json"
	"strings"
	"time"

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

// ToolMetadataBiz defines tool metadata management use-cases.
//
//nolint:interfacebloat // CRUD + list intentionally grouped in one biz entrypoint.
type ToolMetadataBiz interface {
	Create(ctx context.Context, rq *v1.CreateToolMetadataRequest) (*v1.CreateToolMetadataResponse, error)
	Get(ctx context.Context, rq *v1.GetToolMetadataRequest) (*v1.GetToolMetadataResponse, error)
	List(ctx context.Context, rq *v1.ListToolMetadataRequest) (*v1.ListToolMetadataResponse, error)
	Update(ctx context.Context, rq *v1.UpdateToolMetadataRequest) (*v1.UpdateToolMetadataResponse, error)
	Delete(ctx context.Context, rq *v1.DeleteToolMetadataRequest) (*v1.DeleteToolMetadataResponse, error)
	// BatchGetMap retrieves tool metadata as a map for quick lookup.
	// Returns a map of tool_name -> ToolMetadataM. If a tool is not found, it is omitted from the map.
	BatchGetMap(ctx context.Context, toolNames []string) (map[string]*model.ToolMetadataM, error)
	// GetByToolName retrieves a single tool metadata by tool name.
	GetByToolName(ctx context.Context, toolName string) (*model.ToolMetadataM, error)

	ToolMetadataExpansion
}

type ToolMetadataExpansion interface{}

type toolMetadataBiz struct {
	store store.IStore
}

var _ ToolMetadataBiz = (*toolMetadataBiz)(nil)

func New(store store.IStore) *toolMetadataBiz {
	return &toolMetadataBiz{store: store}
}

// BatchGetMap retrieves tool metadata as a map for quick lookup.
// Returns a map of tool_name -> ToolMetadataM. If a tool is not found, it is omitted from the map.
func (b *toolMetadataBiz) BatchGetMap(ctx context.Context, toolNames []string) (map[string]*model.ToolMetadataM, error) {
	if len(toolNames) == 0 {
		return make(map[string]*model.ToolMetadataM), nil
	}

	list, err := b.store.ToolMetadata().BatchGetByToolNames(ctx, toolNames)
	if err != nil {
		return nil, errno.ErrToolMetadataGetFailed
	}

	result := make(map[string]*model.ToolMetadataM, len(list))
	for _, m := range list {
		result[m.ToolName] = m
	}
	return result, nil
}

// GetByToolName retrieves a single tool metadata by tool name.
func (b *toolMetadataBiz) GetByToolName(ctx context.Context, toolName string) (*model.ToolMetadataM, error) {
	if strings.TrimSpace(toolName) == "" {
		return nil, nil
	}

	m, err := b.store.ToolMetadata().Get(ctx, where.T(ctx).F("tool_name", strings.TrimSpace(toolName)))
	if err != nil {
		if isRecordNotFound(err) {
			return nil, nil
		}
		return nil, errno.ErrToolMetadataGetFailed
	}
	return m, nil
}

// Create creates a new tool metadata entry.
func (b *toolMetadataBiz) Create(ctx context.Context, rq *v1.CreateToolMetadataRequest) (*v1.CreateToolMetadataResponse, error) {
	if rq == nil || strings.TrimSpace(rq.GetToolName()) == "" {
		return nil, errno.ErrToolMetadataCreateFailed
	}

	toolName := strings.TrimSpace(rq.GetToolName())

	// Check if tool_name already exists
	existing, err := b.store.ToolMetadata().Get(ctx, where.T(ctx).F("tool_name", toolName))
	if err == nil && existing != nil {
		return nil, errno.ErrToolMetadataAlreadyExists
	}
	if err != nil && !isRecordNotFound(err) {
		return nil, errno.ErrToolMetadataGetFailed
	}

	obj := &model.ToolMetadataM{
		ToolName:    toolName,
		Kind:        normalizeKind(rq.Kind),
		Domain:      normalizeDomain(rq.Domain),
		ReadOnly:    normalizeReadOnly(rq.ReadOnly),
		RiskLevel:   normalizeRiskLevel(rq.RiskLevel),
		LatencyTier: normalizeLatencyTier(rq.LatencyTier),
		CostHint:    normalizeCostHint(rq.CostHint),
		TagsJSON:    trimStringPtr(rq.TagsJSON),
		Description: trimStringPtr(rq.Description),
		McpServerID: trimStringPtr(rq.McpServerID),
		Status:      "active",
	}

	if err := b.store.ToolMetadata().Create(ctx, obj); err != nil {
		return nil, errno.ErrToolMetadataCreateFailed
	}

	return &v1.CreateToolMetadataResponse{ToolMetadata: modelToProto(obj)}, nil
}

// Get retrieves a tool metadata by tool name.
func (b *toolMetadataBiz) Get(ctx context.Context, rq *v1.GetToolMetadataRequest) (*v1.GetToolMetadataResponse, error) {
	if rq == nil || strings.TrimSpace(rq.GetToolName()) == "" {
		return nil, errno.ErrToolMetadataNotFound
	}

	m, err := b.store.ToolMetadata().Get(ctx, where.T(ctx).F("tool_name", strings.TrimSpace(rq.GetToolName())))
	if err != nil {
		return nil, toToolMetadataGetError(err)
	}
	return &v1.GetToolMetadataResponse{ToolMetadata: modelToProto(m)}, nil
}

// List retrieves a paginated list of tool metadata.
func (b *toolMetadataBiz) List(ctx context.Context, rq *v1.ListToolMetadataRequest) (*v1.ListToolMetadataResponse, error) {
	if rq == nil {
		rq = &v1.ListToolMetadataRequest{}
	}

	limit := normalizeListLimit(rq.Limit)
	whr := where.T(ctx).O(int(rq.Offset)).L(int(limit))

	if rq.Kind != nil && strings.TrimSpace(*rq.Kind) != "" {
		whr = whr.F("kind", strings.ToLower(strings.TrimSpace(*rq.Kind)))
	}
	if rq.Domain != nil && strings.TrimSpace(*rq.Domain) != "" {
		whr = whr.F("domain", strings.ToLower(strings.TrimSpace(*rq.Domain)))
	}
	if rq.Status != nil && strings.TrimSpace(*rq.Status) != "" {
		whr = whr.F("status", strings.ToLower(strings.TrimSpace(*rq.Status)))
	}
	if rq.McpServerID != nil && strings.TrimSpace(*rq.McpServerID) != "" {
		whr = whr.F("mcp_server_id", strings.TrimSpace(*rq.McpServerID))
	}

	total, list, err := b.store.ToolMetadata().List(ctx, whr)
	if err != nil {
		return nil, errno.ErrToolMetadataListFailed
	}

	protoList := make([]*v1.ToolMetadata, 0, len(list))
	for _, m := range list {
		protoList = append(protoList, modelToProto(m))
	}

	return &v1.ListToolMetadataResponse{
		TotalCount:       total,
		ToolMetadataList: protoList,
	}, nil
}

// Update updates an existing tool metadata entry.
func (b *toolMetadataBiz) Update(ctx context.Context, rq *v1.UpdateToolMetadataRequest) (*v1.UpdateToolMetadataResponse, error) {
	if rq == nil || strings.TrimSpace(rq.GetToolName()) == "" {
		return nil, errno.ErrToolMetadataNotFound
	}

	obj, err := b.store.ToolMetadata().Get(ctx, where.T(ctx).F("tool_name", strings.TrimSpace(rq.GetToolName())))
	if err != nil {
		return nil, toToolMetadataGetError(err)
	}

	if rq.Kind != nil {
		obj.Kind = normalizeKind(rq.Kind)
	}
	if rq.Domain != nil {
		obj.Domain = normalizeDomain(rq.Domain)
	}
	if rq.ReadOnly != nil {
		obj.ReadOnly = *rq.ReadOnly
	}
	if rq.RiskLevel != nil {
		obj.RiskLevel = normalizeRiskLevel(rq.RiskLevel)
	}
	if rq.LatencyTier != nil {
		obj.LatencyTier = normalizeLatencyTier(rq.LatencyTier)
	}
	if rq.CostHint != nil {
		obj.CostHint = normalizeCostHint(rq.CostHint)
	}
	if rq.TagsJSON != nil {
		obj.TagsJSON = trimStringPtr(rq.TagsJSON)
	}
	if rq.Description != nil {
		obj.Description = trimStringPtr(rq.Description)
	}
	if rq.McpServerID != nil {
		obj.McpServerID = trimStringPtr(rq.McpServerID)
	}
	if rq.Status != nil {
		obj.Status = strings.ToLower(strings.TrimSpace(*rq.Status))
	}
	obj.UpdatedAt = time.Now()

	if err := b.store.ToolMetadata().Update(ctx, obj); err != nil {
		return nil, errno.ErrToolMetadataUpdateFailed
	}

	return &v1.UpdateToolMetadataResponse{ToolMetadata: modelToProto(obj)}, nil
}

// Delete removes a tool metadata entry by tool name.
func (b *toolMetadataBiz) Delete(ctx context.Context, rq *v1.DeleteToolMetadataRequest) (*v1.DeleteToolMetadataResponse, error) {
	if rq == nil || strings.TrimSpace(rq.GetToolName()) == "" {
		return nil, errno.ErrToolMetadataNotFound
	}

	_, err := b.store.ToolMetadata().Get(ctx, where.T(ctx).F("tool_name", strings.TrimSpace(rq.GetToolName())))
	if err != nil {
		return nil, toToolMetadataGetError(err)
	}

	if err := b.store.ToolMetadata().Delete(ctx, where.T(ctx).F("tool_name", strings.TrimSpace(rq.GetToolName()))); err != nil {
		return nil, errno.ErrToolMetadataDeleteFailed
	}
	return &v1.DeleteToolMetadataResponse{}, nil
}

// BuildToolMetadataRefs builds ToolMetadataRef slice from tool names and metadata map.
// This is used by mcpserver.ResolveMcpServerRefs to attach metadata to McpServerRef.
func BuildToolMetadataRefs(tools []string, metadataMap map[string]*model.ToolMetadataM) []model.ToolMetadataRef {
	if len(tools) == 0 || len(metadataMap) == 0 {
		return nil
	}

	var result []model.ToolMetadataRef
	for _, tool := range tools {
		if meta, ok := metadataMap[tool]; ok {
			var tags []string
			if meta.TagsJSON != nil && *meta.TagsJSON != "" {
				_ = json.Unmarshal([]byte(*meta.TagsJSON), &tags)
			}

			description := ""
			if meta.Description != nil {
				description = *meta.Description
			}

			result = append(result, model.ToolMetadataRef{
				ToolName:    meta.ToolName,
				Kind:        meta.Kind,
				Domain:      meta.Domain,
				ReadOnly:    meta.ReadOnly,
				RiskLevel:   meta.RiskLevel,
				LatencyTier: meta.LatencyTier,
				CostHint:    meta.CostHint,
				Tags:        tags,
				Description: description,
			})
		}
	}
	return result
}

func isRecordNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "record not found")
}

func toToolMetadataGetError(err error) error {
	if isRecordNotFound(err) {
		return errno.ErrToolMetadataNotFound
	}
	return errno.ErrToolMetadataGetFailed
}

func modelToProto(m *model.ToolMetadataM) *v1.ToolMetadata {
	if m == nil {
		return nil
	}
	return &v1.ToolMetadata{
		Id:          m.ID,
		ToolName:    m.ToolName,
		Kind:        m.Kind,
		Domain:      m.Domain,
		ReadOnly:    m.ReadOnly,
		RiskLevel:   m.RiskLevel,
		LatencyTier: m.LatencyTier,
		CostHint:    m.CostHint,
		TagsJSON:    m.TagsJSON,
		Description: m.Description,
		McpServerID: m.McpServerID,
		Status:      m.Status,
		CreatedAt:   timestamppb.New(m.CreatedAt),
		UpdatedAt:   timestamppb.New(m.UpdatedAt),
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

func normalizeKind(in *string) string {
	if in == nil || strings.TrimSpace(*in) == "" {
		return "unknown"
	}
	return strings.ToLower(strings.TrimSpace(*in))
}

func normalizeDomain(in *string) string {
	if in == nil || strings.TrimSpace(*in) == "" {
		return "general"
	}
	return strings.ToLower(strings.TrimSpace(*in))
}

func normalizeReadOnly(in *bool) bool {
	if in == nil {
		return true
	}
	return *in
}

func normalizeRiskLevel(in *string) string {
	if in == nil || strings.TrimSpace(*in) == "" {
		return "low"
	}
	return strings.ToLower(strings.TrimSpace(*in))
}

func normalizeLatencyTier(in *string) string {
	if in == nil || strings.TrimSpace(*in) == "" {
		return "fast"
	}
	return strings.ToLower(strings.TrimSpace(*in))
}

func normalizeCostHint(in *string) string {
	if in == nil || strings.TrimSpace(*in) == "" {
		return "free"
	}
	return strings.ToLower(strings.TrimSpace(*in))
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