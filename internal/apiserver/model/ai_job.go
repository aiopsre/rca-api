//nolint:tagalign
package model

import "time"

const TableNameAIJobM = "ai_jobs"

// AIJobM stores one durable AI orchestration run.
type AIJobM struct {
	ID              int64      `gorm:"column:id;primaryKey;autoIncrement:true" json:"id"`
	JobID           string     `gorm:"column:job_id;type:varchar(64);uniqueIndex;not null" json:"job_id"`
	IncidentID      string     `gorm:"column:incident_id;type:varchar(64);index:idx_ai_jobs_incident_id_created_at,priority:1;not null" json:"incident_id"`
	Pipeline        string     `gorm:"column:pipeline;not null" json:"pipeline"`
	Trigger         string     `gorm:"column:trigger;not null" json:"trigger"`
	Status          string     `gorm:"column:status;type:varchar(32);index:idx_ai_jobs_status_created_at,priority:1;not null" json:"status"`
	TimeRangeStart  time.Time  `gorm:"column:time_range_start;not null" json:"time_range_start"`
	TimeRangeEnd    time.Time  `gorm:"column:time_range_end;not null" json:"time_range_end"`
	InputHintsJSON  *string    `gorm:"column:input_hints_json;type:longtext" json:"input_hints_json"`
	OutputSummary   *string    `gorm:"column:output_summary;type:text" json:"output_summary"`
	OutputJSON      *string    `gorm:"column:output_json;type:longtext" json:"output_json"`
	EvidenceIDsJSON *string    `gorm:"column:evidence_ids_json;type:text" json:"evidence_ids_json"`
	ErrorMessage    *string    `gorm:"column:error_message;type:text" json:"error_message"`
	IdempotencyKey  *string    `gorm:"column:idempotency_key;type:varchar(191);uniqueIndex:uniq_ai_jobs_idempotency_key" json:"idempotency_key"`
	LeaseOwner      *string    `gorm:"column:lease_owner;type:varchar(128);index:idx_ai_jobs_lease_owner" json:"lease_owner"`
	LeaseExpiresAt  *time.Time `gorm:"column:lease_expires_at;index:idx_ai_jobs_lease_expires_at" json:"lease_expires_at"`
	LeaseVersion    int64      `gorm:"column:lease_version;not null;default:0" json:"lease_version"`
	HeartbeatAt     *time.Time `gorm:"column:heartbeat_at" json:"heartbeat_at"`
	CreatedBy       string     `gorm:"column:created_by;not null" json:"created_by"`
	CreatedAt       time.Time  `gorm:"column:created_at;index:idx_ai_jobs_incident_id_created_at,priority:2;index:idx_ai_jobs_status_created_at,priority:2;not null;default:CURRENT_TIMESTAMP" json:"created_at"`
	StartedAt       *time.Time `gorm:"column:started_at" json:"started_at"`
	FinishedAt      *time.Time `gorm:"column:finished_at" json:"finished_at"`
	UpdatedAt       time.Time  `gorm:"column:updated_at;not null;default:CURRENT_TIMESTAMP" json:"updated_at"`
}

// TableName AIJobM's table name.
func (*AIJobM) TableName() string {
	return TableNameAIJobM
}
