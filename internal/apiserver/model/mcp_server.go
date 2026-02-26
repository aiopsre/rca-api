package model

import "time"

const TableNameMcpServerM = "mcp_servers"

// McpServerM stores MCP server registration metadata.
// External MCP servers are called by the orchestrator via MCP protocol.
//
//nolint:tagalign // Keep explicit column/index tags aligned with existing model style.
type McpServerM struct {
	ID             int64     `json:"id" gorm:"column:id;primaryKey;autoIncrement:true"`
	McpServerID    string    `json:"mcp_server_id" gorm:"column:mcp_server_id;type:varchar(64);uniqueIndex:uniq_mcp_servers_id;not null"`
	Name           string    `json:"name" gorm:"column:name;type:varchar(128);uniqueIndex:uniq_mcp_servers_name;not null"`
	DisplayName    *string   `json:"display_name" gorm:"column:display_name;type:varchar(256)"`
	Description    *string   `json:"description" gorm:"column:description;type:varchar(1024)"`
	BaseURL        string    `json:"base_url" gorm:"column:base_url;type:varchar(512);not null"`
	AuthType       string    `json:"auth_type" gorm:"column:auth_type;type:varchar(32);not null;default:none"`
	AuthSecretRef  *string   `json:"auth_secret_ref" gorm:"column:auth_secret_ref;type:varchar(256)"`
	AllowedTools   *string   `json:"allowed_tools" gorm:"column:allowed_tools;type:text"`
	TimeoutSec     int       `json:"timeout_sec" gorm:"column:timeout_sec;not null;default:10"`
	Scopes         *string   `json:"scopes" gorm:"column:scopes;type:varchar(256)"`
	Status         string    `json:"status" gorm:"column:status;type:varchar(32);index:idx_mcp_servers_status;not null;default:active"`
	CreatedBy      *string   `json:"created_by" gorm:"column:created_by;type:varchar(191)"`
	CreatedAt      time.Time `json:"created_at" gorm:"column:created_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
	UpdatedAt      time.Time `json:"updated_at" gorm:"column:updated_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
}

func (*McpServerM) TableName() string {
	return TableNameMcpServerM
}

const TableNameMcpServerConfigM = "mcp_server_configs"

// McpServerConfigM stores dynamic pipeline to MCP server bindings.
// Similar to SkillsetConfigDynamicM, this enables runtime association
// between pipelines and external MCP servers.
//
//nolint:tagalign // Keep explicit column/index tags aligned with existing model style.
type McpServerConfigM struct {
	ID               int64     `json:"id" gorm:"column:id;primaryKey;autoIncrement:true"`
	PipelineID       string    `json:"pipeline_id" gorm:"column:pipeline_id;type:varchar(64);uniqueIndex:uniq_mcp_server_configs_pipeline_name,priority:1;index:idx_mcp_server_configs_pipeline,priority:1;not null"`
	McpServerName    string    `json:"mcp_server_name" gorm:"column:mcp_server_name;type:varchar(128);uniqueIndex:uniq_mcp_server_configs_pipeline_name,priority:2;not null"`
	McpServerRefsJSON *string  `json:"mcp_server_refs_json" gorm:"column:mcp_server_refs_json;type:longtext"`
	Priority         int       `json:"priority" gorm:"column:priority;not null;default:0"`
	Enabled          bool      `json:"enabled" gorm:"column:enabled;not null"`
	CreatedAt        time.Time `json:"created_at" gorm:"column:created_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
	UpdatedAt        time.Time `json:"updated_at" gorm:"column:updated_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
}

func (*McpServerConfigM) TableName() string {
	return TableNameMcpServerConfigM
}

// McpServerRef is the reference passed to the orchestrator worker
// for calling external MCP servers. Auth secrets are not included;
// the platform manages secrets separately.
type McpServerRef struct {
	McpServerID  string   `json:"mcp_server_id"`
	Name         string   `json:"name"`
	BaseURL      string   `json:"base_url"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
	TimeoutSec   int      `json:"timeout_sec,omitempty"`
	Scopes       string   `json:"scopes,omitempty"`
	AuthType     string   `json:"auth_type,omitempty"`
}