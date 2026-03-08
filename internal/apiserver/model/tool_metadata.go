package model

import "time"

const TableNameToolMetadataM = "tool_metadata"

// ToolMetadataM stores tool classification metadata.
// This table is managed by the platform (Go side) and synced to the orchestrator (Python side)
// via McpServerRef.tool_metadata field.
//
//nolint:tagalign // Keep explicit column/index tags aligned with existing model style.
type ToolMetadataM struct {
	ID          int64     `json:"id" gorm:"column:id;primaryKey;autoIncrement:true"`
	ToolName    string    `json:"tool_name" gorm:"column:tool_name;type:varchar(128);uniqueIndex:uniq_tool_metadata_name;not null"`
	Kind        string    `json:"kind" gorm:"column:kind;type:varchar(32);not null;default:'unknown'"`
	Domain      string    `json:"domain" gorm:"column:domain;type:varchar(64);not null;default:'general'"`
	ReadOnly    bool      `json:"read_only" gorm:"column:read_only;not null"`
	RiskLevel   string    `json:"risk_level" gorm:"column:risk_level;type:varchar(16);not null;default:'low'"`
	LatencyTier string    `json:"latency_tier" gorm:"column:latency_tier;type:varchar(16);not null;default:'fast'"`
	CostHint    string    `json:"cost_hint" gorm:"column:cost_hint;type:varchar(16);not null;default:'free'"`
	TagsJSON    *string   `json:"tags_json" gorm:"column:tags_json;type:text"`
	Description *string   `json:"description" gorm:"column:description;type:varchar(512)"`
	McpServerID *string   `json:"mcp_server_id" gorm:"column:mcp_server_id;type:varchar(64);index:idx_tool_metadata_mcp_server"`
	Status      string    `json:"status" gorm:"column:status;type:varchar(32);not null;default:'active'"`
	CreatedAt   time.Time `json:"created_at" gorm:"column:created_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
	UpdatedAt   time.Time `json:"updated_at" gorm:"column:updated_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
}

func (*ToolMetadataM) TableName() string {
	return TableNameToolMetadataM
}

// ToolMetadataRef is passed to Python orchestrator via McpServerRef.tool_metadata.
// This is a lightweight representation containing only the fields needed by the orchestrator.
type ToolMetadataRef struct {
	ToolName    string   `json:"tool_name"`
	Kind        string   `json:"kind,omitempty"`
	Domain      string   `json:"domain,omitempty"`
	ReadOnly    bool     `json:"read_only,omitempty"`
	RiskLevel   string   `json:"risk_level,omitempty"`
	LatencyTier string   `json:"latency_tier,omitempty"`
	CostHint    string   `json:"cost_hint,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Description string   `json:"description,omitempty"`
}