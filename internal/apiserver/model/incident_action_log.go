package model

import "time"

const TableNameIncidentActionLogM = "incident_action_logs"

// IncidentActionLogM stores one operator action log bound to an incident.
type IncidentActionLogM struct {
	ID          int64     `json:"id" gorm:"column:id;primaryKey;autoIncrement:true"`
	ActionID    string    `json:"action_id" gorm:"column:action_id;type:varchar(64);uniqueIndex;not null"`
	IncidentID  string    `json:"incident_id" gorm:"column:incident_id;type:varchar(64);index:idx_incident_action_logs_incident_created,priority:1;not null"`
	Actor       string    `json:"actor" gorm:"column:actor;type:varchar(128);not null"`
	ActionType  string    `json:"action_type" gorm:"column:action_type;type:varchar(64);not null"`
	Summary     string    `json:"summary" gorm:"column:summary;type:varchar(256);not null"`
	DetailsJSON *string   `json:"details_json" gorm:"column:details_json;type:longtext"`
	CreatedAt   time.Time `json:"created_at" gorm:"column:created_at;index:idx_incident_action_logs_incident_created,priority:2;not null;default:CURRENT_TIMESTAMP"`
	UpdatedAt   time.Time `json:"updated_at" gorm:"column:updated_at;not null;default:CURRENT_TIMESTAMP"`
}

// TableName IncidentActionLogM's table name.
func (*IncidentActionLogM) TableName() string {
	return TableNameIncidentActionLogM
}
