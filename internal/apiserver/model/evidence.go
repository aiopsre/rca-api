package model

import "time"

const TableNameEvidenceM = "evidence"

// EvidenceM stores persisted evidence bound to incidents/jobs.
type EvidenceM struct {
	ID              int64     `gorm:"column:id;primaryKey;autoIncrement:true" json:"id"`
	EvidenceID      string    `gorm:"column:evidence_id;uniqueIndex;not null" json:"evidence_id"`
	IncidentID      string    `gorm:"column:incident_id;index:idx_evidence_incident_id_created_at,priority:1;index:uniq_evidence_incident_idempotency,priority:1;not null" json:"incident_id"`
	JobID           *string   `gorm:"column:job_id" json:"job_id"`
	Type            string    `gorm:"column:type;index:idx_evidence_type_created_at,priority:1;not null" json:"type"`
	DatasourceID    *string   `gorm:"column:datasource_id" json:"datasource_id"`
	QueryText       string    `gorm:"column:query_text;type:text;not null" json:"query_text"`
	QueryJSON       *string   `gorm:"column:query_json;type:longtext" json:"query_json"`
	QueryHash       string    `gorm:"column:query_hash;index:idx_evidence_query_hash;not null" json:"query_hash"`
	TimeRangeStart  time.Time `gorm:"column:time_range_start;not null" json:"time_range_start"`
	TimeRangeEnd    time.Time `gorm:"column:time_range_end;not null" json:"time_range_end"`
	ResultJSON      string    `gorm:"column:result_json;type:longtext;not null" json:"result_json"`
	ResultSizeBytes int64     `gorm:"column:result_size_bytes;not null;default:0" json:"result_size_bytes"`
	IsTruncated     bool      `gorm:"column:is_truncated;not null;default:false" json:"is_truncated"`
	Summary         *string   `gorm:"column:summary;type:text" json:"summary"`
	CreatedBy       string    `gorm:"column:created_by;not null" json:"created_by"`
	IdempotencyKey  *string   `gorm:"column:idempotency_key;index:uniq_evidence_incident_idempotency,priority:2" json:"idempotency_key"`
	CreatedAt       time.Time `gorm:"column:created_at;index:idx_evidence_incident_id_created_at,priority:2;index:idx_evidence_type_created_at,priority:2;not null;default:current_timestamp" json:"created_at"`
	UpdatedAt       time.Time `gorm:"column:updated_at;not null;default:current_timestamp" json:"updated_at"`
}

// TableName EvidenceM's table name.
func (*EvidenceM) TableName() string {
	return TableNameEvidenceM
}
