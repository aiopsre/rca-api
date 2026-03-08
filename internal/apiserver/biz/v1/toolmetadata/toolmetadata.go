package toolmetadata

//go:generate mockgen -destination mock_toolmetadata.go -package toolmetadata github.com/aiopsre/rca-api/internal/apiserver/biz/v1/toolmetadata ToolMetadataBiz

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

// ToolMetadataBiz defines tool metadata management use-cases.
type ToolMetadataBiz interface {
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