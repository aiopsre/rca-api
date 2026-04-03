package model

import "time"

const TableNameAIToolCallM = "ai_tool_calls"

// AIToolCallM stores one tool-call audit record for AI jobs.
type AIToolCallM struct {
	ID                int64     `json:"id" gorm:"column:id;primaryKey;autoIncrement:true"`
	ToolCallID        string    `json:"tool_call_id" gorm:"column:tool_call_id;type:varchar(64);uniqueIndex;not null"`
	JobID             string    `json:"job_id" gorm:"column:job_id;type:varchar(64);index:idx_tool_calls_job_id_seq,priority:1;uniqueIndex:uniq_tool_calls_job_seq,priority:1;not null"`
	Seq               int64     `json:"seq" gorm:"column:seq;index:idx_tool_calls_job_id_seq,priority:2;uniqueIndex:uniq_tool_calls_job_seq,priority:2;not null"`
	NodeName          string    `json:"node_name" gorm:"column:node_name;not null"`
	ToolName          string    `json:"tool_name" gorm:"column:tool_name;type:varchar(128);index:idx_tool_calls_tool_created_at,priority:1;not null"`
	RequestJSON       string    `json:"request_json" gorm:"column:request_json;type:longtext;not null"`
	ResponseJSON      *string   `json:"response_json" gorm:"column:response_json;type:longtext"`
	ResponseRef       *string   `json:"response_ref" gorm:"column:response_ref"`
	ResponseSizeBytes int64     `json:"response_size_bytes" gorm:"column:response_size_bytes;not null;default:0"`
	Status            string    `json:"status" gorm:"column:status;not null"`
	LatencyMs         int64     `json:"latency_ms" gorm:"column:latency_ms;not null;default:0"`
	ErrorMessage      *string   `json:"error_message" gorm:"column:error_message;type:text"`
	EvidenceIDsJSON   *string   `json:"evidence_ids_json" gorm:"column:evidence_ids_json;type:text"`
	CreatedAt         time.Time `json:"created_at" gorm:"column:created_at;type:datetime;index:idx_tool_calls_tool_created_at,priority:2;not null;default:CURRENT_TIMESTAMP"`
	UpdatedAt         time.Time `json:"updated_at" gorm:"column:updated_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
}

// TableName AIToolCallM's table name.
func (*AIToolCallM) TableName() string {
	return TableNameAIToolCallM
}
