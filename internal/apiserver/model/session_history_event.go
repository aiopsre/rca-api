package model

import "time"

const TableNameSessionHistoryEventM = "session_history_events"

// SessionHistoryEventM stores one session-centric operator/system event.
//
//nolint:tagalign // Keep explicit column/index tags aligned with existing model style.
type SessionHistoryEventM struct {
	ID                 int64     `json:"id" gorm:"column:id;primaryKey;autoIncrement:true"`
	EventID            string    `json:"event_id" gorm:"column:event_id;type:varchar(64);uniqueIndex;not null"`
	SessionID          string    `json:"session_id" gorm:"column:session_id;type:varchar(64);index:idx_session_history_events_session_created,priority:1;not null"`
	IncidentID         *string   `json:"incident_id" gorm:"column:incident_id;type:varchar(64);index:idx_session_history_events_incident_id"`
	JobID              *string   `json:"job_id" gorm:"column:job_id;type:varchar(64);index:idx_session_history_events_job_id"`
	EventType          string    `json:"event_type" gorm:"column:event_type;type:varchar(64);index:idx_session_history_events_session_created,priority:2;not null"`
	Actor              string    `json:"actor" gorm:"column:actor;type:varchar(128);not null"`
	Note               *string   `json:"note" gorm:"column:note;type:text"`
	ReasonCode         *string   `json:"reason_code" gorm:"column:reason_code;type:varchar(64)"`
	PayloadSummaryJSON *string   `json:"payload_summary_json" gorm:"column:payload_summary_json;type:longtext"`
	CreatedAt          time.Time `json:"created_at" gorm:"column:created_at;type:datetime;index:idx_session_history_events_session_created,priority:3;not null;default:CURRENT_TIMESTAMP"`
	UpdatedAt          time.Time `json:"updated_at" gorm:"column:updated_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
}

// TableName SessionHistoryEventM's table name.
func (*SessionHistoryEventM) TableName() string {
	return TableNameSessionHistoryEventM
}
