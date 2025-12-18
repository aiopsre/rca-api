package model

import "time"

const TableNameAIToolCallM = "ai_tool_calls"

// AIToolCallM stores one tool-call audit record for AI jobs.
type AIToolCallM struct {
	ID                int64     `gorm:"column:id;primaryKey;autoIncrement:true" json:"id"`
	ToolCallID        string    `gorm:"column:tool_call_id;uniqueIndex;not null" json:"tool_call_id"`
	JobID             string    `gorm:"column:job_id;index:idx_tool_calls_job_id_seq,priority:1;uniqueIndex:uniq_tool_calls_job_seq,priority:1;not null" json:"job_id"`
	Seq               int64     `gorm:"column:seq;index:idx_tool_calls_job_id_seq,priority:2;uniqueIndex:uniq_tool_calls_job_seq,priority:2;not null" json:"seq"`
	NodeName          string    `gorm:"column:node_name;not null" json:"node_name"`
	ToolName          string    `gorm:"column:tool_name;index:idx_tool_calls_tool_created_at,priority:1;not null" json:"tool_name"`
	RequestJSON       string    `gorm:"column:request_json;type:longtext;not null" json:"request_json"`
	ResponseJSON      *string   `gorm:"column:response_json;type:longtext" json:"response_json"`
	ResponseRef       *string   `gorm:"column:response_ref" json:"response_ref"`
	ResponseSizeBytes int64     `gorm:"column:response_size_bytes;not null;default:0" json:"response_size_bytes"`
	Status            string    `gorm:"column:status;not null" json:"status"`
	LatencyMs         int64     `gorm:"column:latency_ms;not null;default:0" json:"latency_ms"`
	ErrorMessage      *string   `gorm:"column:error_message;type:text" json:"error_message"`
	EvidenceIDsJSON   *string   `gorm:"column:evidence_ids_json;type:text" json:"evidence_ids_json"`
	CreatedAt         time.Time `gorm:"column:created_at;index:idx_tool_calls_tool_created_at,priority:2;not null;default:current_timestamp" json:"created_at"`
	UpdatedAt         time.Time `gorm:"column:updated_at;not null;default:current_timestamp" json:"updated_at"`
}

// TableName AIToolCallM's table name.
func (*AIToolCallM) TableName() string {
	return TableNameAIToolCallM
}
