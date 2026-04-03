package model

import "time"

const TableNameToolsetProviderBinding = "toolset_provider_bindings"

// ToolsetProviderBinding stores the binding between toolsets and MCP server providers.
// This table enables fine-grained control over which providers are available to which toolsets,
// including tool-level filtering and priority ordering.
//
//nolint:tagalign // Keep explicit column/index tags aligned with existing model style.
type ToolsetProviderBinding struct {
	ID               int64     `json:"id" gorm:"column:id;primaryKey;autoIncrement:true"`
	ToolsetName      string    `json:"toolset_name" gorm:"column:toolset_name;type:varchar(128);uniqueIndex:uniq_toolset_provider,priority:1;index:idx_toolset_provider_toolset;not null"`
	McpServerID      string    `json:"mcp_server_id" gorm:"column:mcp_server_id;type:varchar(64);uniqueIndex:uniq_toolset_provider,priority:2;index:idx_toolset_provider_server;not null"`
	AllowedToolsJSON *string   `json:"allowed_tools_json" gorm:"column:allowed_tools_json;type:longtext"`
	Priority         int       `json:"priority" gorm:"column:priority;not null;default:0"`
	Enabled          bool      `json:"enabled" gorm:"column:enabled;not null;default:true"`
	CreatedAt        time.Time `json:"created_at" gorm:"column:created_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
	UpdatedAt        time.Time `json:"updated_at" gorm:"column:updated_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
}

func (*ToolsetProviderBinding) TableName() string {
	return TableNameToolsetProviderBinding
}