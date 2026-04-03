package model

import "time"

const TableNameMcpServerM = "mcp_servers"

// McpServerM stores MCP server registration metadata.
// External MCP servers are called by the orchestrator via MCP protocol.
//
//nolint:tagalign // Keep explicit column/index tags aligned with existing model style.
type McpServerM struct {
	ID           int64     `json:"id" gorm:"column:id;primaryKey;autoIncrement:true"`
	McpServerID  string    `json:"mcp_server_id" gorm:"column:mcp_server_id;type:varchar(64);uniqueIndex:uniq_mcp_servers_id;not null"`
	Name         string    `json:"name" gorm:"column:name;type:varchar(128);uniqueIndex:uniq_mcp_servers_name;not null"`
	ServerKind   string    `json:"server_kind" gorm:"column:server_kind;type:varchar(32);not null;default:'external'"`
	IsSystem     bool      `json:"is_system" gorm:"column:is_system;not null;default:false"`
	BuiltinKey   *string   `json:"builtin_key" gorm:"column:builtin_key;type:varchar(64);index:idx_mcp_servers_builtin_key"`
	TenantID     *string   `json:"tenant_id" gorm:"column:tenant_id;type:varchar(64);index:idx_mcp_servers_tenant"`
	DisplayName  *string   `json:"display_name" gorm:"column:display_name;type:varchar(256)"`
	Description  *string   `json:"description" gorm:"column:description;type:varchar(1024)"`
	BaseURL      string    `json:"base_url" gorm:"column:base_url;type:varchar(512);not null"`
	AuthType     string    `json:"auth_type" gorm:"column:auth_type;type:varchar(32);not null;default:none"`
	AuthSecretRef *string  `json:"auth_secret_ref" gorm:"column:auth_secret_ref;type:varchar(256)"`
	AllowedTools *string   `json:"allowed_tools" gorm:"column:allowed_tools;type:text"`
	TimeoutSec   int       `json:"timeout_sec" gorm:"column:timeout_sec;not null;default:10"`
	Scopes       *string   `json:"scopes" gorm:"column:scopes;type:varchar(256)"`
	Status       string    `json:"status" gorm:"column:status;type:varchar(32);index:idx_mcp_servers_status;not null;default:active"`
	CreatedBy    *string   `json:"created_by" gorm:"column:created_by;type:varchar(191)"`
	CreatedAt    time.Time `json:"created_at" gorm:"column:created_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
	UpdatedAt    time.Time `json:"updated_at" gorm:"column:updated_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
}

func (*McpServerM) TableName() string {
	return TableNameMcpServerM
}

// McpServerRef is the reference passed to the orchestrator worker
// for calling external MCP servers. Auth secrets are not included;
// the platform manages secrets separately.
type McpServerRef struct {
	McpServerID  string            `json:"mcp_server_id"`
	Name         string            `json:"name"`
	ServerKind   string            `json:"server_kind,omitempty"`
	BaseURL      string            `json:"base_url"`
	AllowedTools []string          `json:"allowed_tools,omitempty"`
	ToolMetadata []ToolMetadataRef `json:"tool_metadata,omitempty"`
	TimeoutSec   int               `json:"timeout_sec,omitempty"`
	Scopes       string            `json:"scopes,omitempty"`
	AuthType     string            `json:"auth_type,omitempty"`
	Priority     int               `json:"priority,omitempty"`
}
