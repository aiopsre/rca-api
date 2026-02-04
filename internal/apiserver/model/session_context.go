package model

import "time"

const TableNameSessionContextM = "session_contexts"

// SessionContextM stores reusable cross-run RCA context.
//
//nolint:tagalign // Keep explicit column/index tags aligned with existing model style.
type SessionContextM struct {
	ID                 int64     `json:"id" gorm:"column:id;primaryKey;autoIncrement:true"`
	SessionID          string    `json:"session_id" gorm:"column:session_id;type:varchar(64);uniqueIndex;not null"`
	SessionType        string    `json:"session_type" gorm:"column:session_type;type:varchar(32);uniqueIndex:uniq_session_contexts_type_business_key,priority:1;index:idx_session_contexts_type_status_created_at,priority:1;not null"`
	BusinessKey        string    `json:"business_key" gorm:"column:business_key;type:varchar(191);uniqueIndex:uniq_session_contexts_type_business_key,priority:2;not null"`
	IncidentID         *string   `json:"incident_id" gorm:"column:incident_id;type:varchar(64);index:idx_session_contexts_incident_id"`
	Title              *string   `json:"title" gorm:"column:title;type:text"`
	Status             string    `json:"status" gorm:"column:status;type:varchar(32);index:idx_session_contexts_type_status_created_at,priority:2;not null;default:active"`
	LatestSummaryJSON  *string   `json:"latest_summary_json" gorm:"column:latest_summary_json;type:longtext"`
	PinnedEvidenceJSON *string   `json:"pinned_evidence_json" gorm:"column:pinned_evidence_json;type:longtext"`
	ActiveRunID        *string   `json:"active_run_id" gorm:"column:active_run_id;type:varchar(64);index:idx_session_contexts_active_run_id"`
	ContextStateJSON   *string   `json:"context_state_json" gorm:"column:context_state_json;type:longtext"`
	CreatedAt          time.Time `json:"created_at" gorm:"column:created_at;type:datetime;index:idx_session_contexts_type_status_created_at,priority:3;not null;default:CURRENT_TIMESTAMP"`
	UpdatedAt          time.Time `json:"updated_at" gorm:"column:updated_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
}

// TableName SessionContextM's table name.
func (*SessionContextM) TableName() string {
	return TableNameSessionContextM
}
