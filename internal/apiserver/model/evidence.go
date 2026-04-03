package model

import "time"

const TableNameEvidenceM = "evidence"

// EvidenceM stores persisted evidence bound to incidents/jobs.
type EvidenceM struct {
	ID              int64     `json:"id" gorm:"column:id;primaryKey;autoIncrement:true"`
	EvidenceID      string    `json:"evidence_id" gorm:"column:evidence_id;type:varchar(64);uniqueIndex;not null"`
	IncidentID      string    `json:"incident_id" gorm:"column:incident_id;type:varchar(64);index:idx_evidence_incident_id_created_at,priority:1;index:uniq_evidence_incident_idempotency,priority:1;not null"`
	JobID           *string   `json:"job_id" gorm:"column:job_id"`
	Type            string    `json:"type" gorm:"column:type;type:varchar(64);index:idx_evidence_type_created_at,priority:1;not null"`
	DatasourceID    *string   `json:"datasource_id" gorm:"column:datasource_id"`
	QueryText       string    `json:"query_text" gorm:"column:query_text;type:text;not null"`
	QueryJSON       *string   `json:"query_json" gorm:"column:query_json;type:longtext"`
	QueryHash       string    `json:"query_hash" gorm:"column:query_hash;type:varchar(128);index:idx_evidence_query_hash;not null"`
	TimeRangeStart  time.Time `json:"time_range_start" gorm:"column:time_range_start;not null"`
	TimeRangeEnd    time.Time `json:"time_range_end" gorm:"column:time_range_end;not null"`
	ResultJSON      string    `json:"result_json" gorm:"column:result_json;type:longtext;not null"`
	ResultSizeBytes int64     `json:"result_size_bytes" gorm:"column:result_size_bytes;not null;default:0"`
	IsTruncated     bool      `json:"is_truncated" gorm:"column:is_truncated;not null;default:false"`
	Summary         *string   `json:"summary" gorm:"column:summary;type:text"`
	CreatedBy       string    `json:"created_by" gorm:"column:created_by;not null"`
	IdempotencyKey  *string   `json:"idempotency_key" gorm:"column:idempotency_key;type:varchar(191);index:uniq_evidence_incident_idempotency,priority:2"`
	CreatedAt       time.Time `json:"created_at" gorm:"column:created_at;type:datetime;index:idx_evidence_incident_id_created_at,priority:2;index:idx_evidence_type_created_at,priority:2;not null;default:CURRENT_TIMESTAMP"`
	UpdatedAt       time.Time `json:"updated_at" gorm:"column:updated_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
}

// TableName EvidenceM's table name.
func (*EvidenceM) TableName() string {
	return TableNameEvidenceM
}
