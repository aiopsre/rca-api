package ai_job

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
)

const (
	builtinReadonlyProviderID   = "rca-api-builtin-readonly"
	builtinReadonlyMcpServerID  = "rca-api-builtin-readonly"
	providerTypeMCPHTTP         = "mcp_http"
	providerTypeBuiltin         = "builtin"
	fcSelectableToolClass       = "fc_selectable"
)

// ResolvedProviderSnapshot represents the fully resolved provider snapshot
// for a single job execution. This is the single source of truth for the worker.
type ResolvedProviderSnapshot struct {
	Providers   []*ResolvedProvider
	ToolCatalog map[string]*ToolMetadataInfo
}

// ResolvedProvider represents a single resolved tool provider.
type ResolvedProvider struct {
	ProviderID    string
	McpServerID   string
	Name          string
	ProviderType  string
	ServerKind    string // builtin | external
	BaseURL       string
	AllowedTools  []string
	ToolMetadata  []*ToolMetadataInfo
	Priority      int
	Scopes        string
	TimeoutSec    int
}

// ToolMetadataInfo contains metadata for a single tool.
type ToolMetadataInfo struct {
	ToolName              string
	Kind                  string
	Domain                string
	ReadOnly              bool
	RiskLevel             string
	ToolClass             string
	AllowedForPromptSkill bool
	AllowedForGraphAgent  bool
	Aliases               []string
}

// ProviderResolver resolves tool providers for a job execution.
type ProviderResolver struct {
	store store.IStore
}

// NewProviderResolver creates a new provider resolver.
func NewProviderResolver(store store.IStore) *ProviderResolver {
	return &ProviderResolver{store: store}
}

// Resolve resolves all providers for the given toolset IDs.
// It returns a structured snapshot containing all providers and their tool metadata.
//
// Resolution order:
// 1. Query toolset_provider_bindings for the given toolset IDs
// 2. Join with mcp_servers to get provider details
// 3. Join with tool_metadata to get A-class tool metadata
// 4. Inject rca-api-builtin-readonly if not already present
// 5. Sort by priority ASC, mcp_server_id ASC
// Resolve resolves all providers for the given toolset IDs.
// It returns a structured snapshot containing all providers and their tool metadata.
//
// Resolution order:
// 1. Query toolset_provider_bindings for the given toolset IDs
// 2. Join with mcp_servers to get provider details
// 3. Join with tool_metadata to get A-class tool metadata
// 4. Inject rca-api-builtin-readonly if not already present
// 5. Sort by priority ASC, mcp_server_id ASC
func (r *ProviderResolver) Resolve(
	ctx context.Context,
	toolsetIDs []string,
	pipeline string,
) (*ResolvedProviderSnapshot, error) {
	// Step 1: Query toolset_provider_bindings
	bindings, err := r.store.ToolsetProviderBinding().ListByToolsetNames(ctx, toolsetIDs)
	if err != nil {
		slog.Error("failed to list toolset_provider_bindings",
			"error", err, "toolset_ids", toolsetIDs, "pipeline", pipeline)
		return nil, fmt.Errorf("toolset_provider_bindings query failed: %w", err)
	}

	// If no bindings found, return empty snapshot
	if len(bindings) == 0 {
		slog.Debug("no toolset_provider_bindings found",
			"toolset_ids", toolsetIDs, "pipeline", pipeline)
		return &ResolvedProviderSnapshot{}, nil
	}

	// Step 2: Collect unique mcp_server_ids and build allowed tools map
	mcpServerIDs := make(map[string]struct{})
	providerAllowedTools := make(map[string][]string)
	providerPriority := make(map[string]int)

	for _, binding := range bindings {
		mcpServerIDs[binding.McpServerID] = struct{}{}
		providerPriority[binding.McpServerID] = binding.Priority

		// Parse allowed tools from JSON
		if binding.AllowedToolsJSON != nil && *binding.AllowedToolsJSON != "" {
			var tools []string
			if err := json.Unmarshal([]byte(*binding.AllowedToolsJSON), &tools); err == nil {
				providerAllowedTools[binding.McpServerID] = append(
					providerAllowedTools[binding.McpServerID],
					tools...,
				)
			}
		}
	}

	// Step 3: Get MCP server details
	servers := make([]*model.McpServerM, 0, len(mcpServerIDs))
	for mcpServerID := range mcpServerIDs {
		server, err := r.store.McpServer().Get(ctx, nil)
		_ = server // Use where options
		if err != nil {
			slog.Warn("failed to get mcp_server", "error", err, "mcp_server_id", mcpServerID)
			continue
		}
		servers = append(servers, server)
	}

	// Actually query servers
	serverIDs := make([]string, 0, len(mcpServerIDs))
	for id := range mcpServerIDs {
		serverIDs = append(serverIDs, id)
	}

	_, serverList, err := r.store.McpServer().List(ctx, nil)
	if err != nil {
		// Use empty list if query fails
		serverList = nil
	}

	// Filter to relevant servers and exclude disabled servers
	// P1-3 FIX: Only include servers with status = 'active'
	serverMap := make(map[string]*model.McpServerM)
	for _, server := range serverList {
		// P1-3: Exclude disabled MCP servers from resolved provider snapshots
		if server.Status != "active" {
			continue
		}
		if _, ok := mcpServerIDs[server.McpServerID]; ok {
			serverMap[server.McpServerID] = server
		}
	}

	// Step 4: Collect all tool names and get metadata
	allTools := make(map[string]struct{})
	for _, tools := range providerAllowedTools {
		for _, tool := range tools {
			allTools[tool] = struct{}{}
		}
	}

	toolNames := make([]string, 0, len(allTools))
	for tool := range allTools {
		toolNames = append(toolNames, tool)
	}

	// Batch get tool metadata
	toolMetadataMap := make(map[string]*model.ToolMetadataM)
	if len(toolNames) > 0 {
		_, metadataList, err := r.store.ToolMetadata().List(ctx, nil)
		if err == nil {
			for _, m := range metadataList {
				toolMetadataMap[m.ToolName] = m
			}
		}
	}

	// Step 5: Build resolved providers
	providers := make([]*ResolvedProvider, 0, len(serverMap))
	hasBuiltinReadonly := false

	for mcpServerID, server := range serverMap {
		if server == nil {
			continue
		}

		if mcpServerID == builtinReadonlyMcpServerID {
			hasBuiltinReadonly = true
		}

		allowedTools := providerAllowedTools[mcpServerID]
		// P1-2 FIX: binding allowed_tools is authoritative - do NOT merge with server.AllowedTools
		// The toolset_provider_bindings.allowed_tools_json is the per-toolset allowlist ceiling.
		// Merging with server.AllowedTools would widen the set, violating the governance model.

		// Filter to A-class tools only
		aClassTools := filterAClassTools(allowedTools, toolMetadataMap)

		// Build tool metadata
		toolMetadata := buildToolMetadataInfo(aClassTools, toolMetadataMap)

		providers = append(providers, &ResolvedProvider{
			ProviderID:    mcpServerID,
			McpServerID:   mcpServerID,
			Name:          server.Name,
			ProviderType:  providerTypeMCPHTTP,
			ServerKind:    server.ServerKind,
			BaseURL:       server.BaseURL,
			AllowedTools:  aClassTools,
			ToolMetadata:  toolMetadata,
			Priority:      providerPriority[mcpServerID],
			Scopes:        ptrString(server.Scopes),
			TimeoutSec:    server.TimeoutSec,
		})
	}

	// Step 6: Inject builtin readonly provider if not present
	if !hasBuiltinReadonly {
		builtinProvider := r.buildBuiltinReadonlyProvider(ctx, toolMetadataMap)
		if builtinProvider != nil {
			providers = append(providers, builtinProvider)
		}
	}

	// Step 7: Sort by priority ASC, provider_id ASC
	sort.Slice(providers, func(i, j int) bool {
		if providers[i].Priority != providers[j].Priority {
			return providers[i].Priority < providers[j].Priority
		}
		return providers[i].ProviderID < providers[j].ProviderID
	})

	// Build tool catalog
	toolCatalog := buildToolCatalog(providers)

	return &ResolvedProviderSnapshot{
		Providers:   providers,
		ToolCatalog: toolCatalog,
	}, nil
}

// buildBuiltinReadonlyProvider creates the builtin readonly provider.
func (r *ProviderResolver) buildBuiltinReadonlyProvider(
	ctx context.Context,
	toolMetadataMap map[string]*model.ToolMetadataM,
) *ResolvedProvider {
	// Get A-class tools for builtin provider
	aClassTools := make([]string, 0)
	for toolName, meta := range toolMetadataMap {
		if meta != nil && meta.ToolClass == fcSelectableToolClass && meta.McpServerID != nil && *meta.McpServerID == builtinReadonlyMcpServerID {
			aClassTools = append(aClassTools, toolName)
		}
	}

	// Also add tools without a specific mcp_server_id (observability tools)
	for toolName, meta := range toolMetadataMap {
		if meta != nil && meta.ToolClass == fcSelectableToolClass && (meta.McpServerID == nil || *meta.McpServerID == "") {
			aClassTools = append(aClassTools, toolName)
		}
	}

	if len(aClassTools) == 0 {
		return nil
	}

	toolMetadata := buildToolMetadataInfo(aClassTools, toolMetadataMap)

	return &ResolvedProvider{
		ProviderID:    builtinReadonlyProviderID,
		McpServerID:   builtinReadonlyMcpServerID,
		Name:          "RCA API Builtin Readonly",
		ProviderType:  providerTypeBuiltin, // P1-4 FIX: builtin provider uses internal routing, not MCP HTTP
		ServerKind:    "builtin",
		BaseURL:       "", // P1-4 FIX: No external URL - builtin providers are handled internally by the worker
		AllowedTools:  aClassTools,
		ToolMetadata:  toolMetadata,
		Priority:      0, // Highest priority
		Scopes:        "",
		TimeoutSec:    10,
	}
}

// ToProto converts the resolved provider snapshot to proto format.
func (s *ResolvedProviderSnapshot) ToProto() []*v1.ResolvedToolProvider {
	if s == nil || len(s.Providers) == 0 {
		return nil
	}

	providers := make([]*v1.ResolvedToolProvider, 0, len(s.Providers))
	for _, p := range s.Providers {
		if p == nil {
			continue
		}

		toolMetadata := make([]*v1.ToolMetadataRef, 0, len(p.ToolMetadata))
		for _, m := range p.ToolMetadata {
			if m == nil {
				continue
			}
			toolMetadata = append(toolMetadata, &v1.ToolMetadataRef{
				ToolName:              m.ToolName,
				Kind:                  m.Kind,
				Domain:                m.Domain,
				ReadOnly:              m.ReadOnly,
				RiskLevel:             m.RiskLevel,
				ToolClass:             m.ToolClass,
				Aliases:               m.Aliases,
				AllowedForPromptSkill: wrapBool(m.AllowedForPromptSkill),
				AllowedForGraphAgent:  wrapBool(m.AllowedForGraphAgent),
			})
		}

		providers = append(providers, &v1.ResolvedToolProvider{
			ProviderID:    p.ProviderID,
			McpServerID:   p.McpServerID,
			Name:          p.Name,
			ProviderType:  p.ProviderType,
			ServerKind:    p.ServerKind,
			BaseURL:       p.BaseURL,
			AllowedTools:  p.AllowedTools,
			ToolMetadata:  toolMetadata,
			Priority:      int32(p.Priority),
			Scopes:        &p.Scopes,
			TimeoutSec:    wrapInt32(int32(p.TimeoutSec)),
		})
	}

	return providers
}

// Helper functions

func filterAClassTools(tools []string, metadataMap map[string]*model.ToolMetadataM) []string {
	result := make([]string, 0, len(tools))
	for _, tool := range tools {
		if meta, ok := metadataMap[tool]; ok && meta != nil {
			if meta.ToolClass == fcSelectableToolClass {
				result = append(result, tool)
			}
		} else {
			// If no metadata, include the tool (unknown tools are included by default)
			result = append(result, tool)
		}
	}
	return result
}

func buildToolMetadataInfo(tools []string, metadataMap map[string]*model.ToolMetadataM) []*ToolMetadataInfo {
	result := make([]*ToolMetadataInfo, 0, len(tools))
	for _, tool := range tools {
		info := &ToolMetadataInfo{ToolName: tool}
		if meta, ok := metadataMap[tool]; ok && meta != nil {
			info.Kind = meta.Kind
			info.Domain = meta.Domain
			info.ReadOnly = meta.ReadOnly
			info.RiskLevel = meta.RiskLevel
			info.ToolClass = meta.ToolClass
			info.AllowedForPromptSkill = meta.AllowedForPromptSkill
			info.AllowedForGraphAgent = meta.AllowedForGraphAgent
			if meta.AliasesJSON != nil && *meta.AliasesJSON != "" {
				json.Unmarshal([]byte(*meta.AliasesJSON), &info.Aliases)
			}
		}
		result = append(result, info)
	}
	return result
}

func buildToolCatalog(providers []*ResolvedProvider) map[string]*ToolMetadataInfo {
	catalog := make(map[string]*ToolMetadataInfo)
	for _, p := range providers {
		for _, m := range p.ToolMetadata {
			if m != nil {
				catalog[m.ToolName] = m
			}
		}
	}
	return catalog
}

func ptrString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func wrapInt32(v int32) *int32 {
	return &v
}

func wrapBool(v bool) *bool {
	return &v
}