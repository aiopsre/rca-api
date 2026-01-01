package policy

// MCPToolPolicyLimits defines optional policy limits for one MCP tool.
type MCPToolPolicyLimits struct {
	MaxLimit            *int64 `json:"maxLimit,omitempty"            mapstructure:"maxLimit"`
	MaxTimeRangeSeconds *int64 `json:"maxTimeRangeSeconds,omitempty" mapstructure:"maxTimeRangeSeconds"`
	MaxResponseBytes    *int   `json:"maxResponseBytes,omitempty"    mapstructure:"maxResponseBytes"`
}

// MCPToolPolicy defines optional governance settings for one MCP tool.
type MCPToolPolicy struct {
	Enabled   *bool               `json:"enabled,omitempty"   mapstructure:"enabled"`
	RiskLevel string              `json:"riskLevel,omitempty" mapstructure:"riskLevel"`
	Limits    MCPToolPolicyLimits `json:"limits"              mapstructure:"limits"`
}

// MCPPolicyConfig contains per-tool MCP policy overrides loaded from config/env.
type MCPPolicyConfig struct {
	Tools map[string]MCPToolPolicy `json:"tools,omitempty" mapstructure:"tools"`
}
