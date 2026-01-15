package model

import "time"

const TableNameIncidentVerificationRunM = "incident_verification_runs"

// IncidentVerificationRunM stores one verification run result bound to an incident.
type IncidentVerificationRunM struct {
	ID               int64     `json:"id" gorm:"column:id;primaryKey;autoIncrement:true"`
	RunID            string    `json:"run_id" gorm:"column:run_id;type:varchar(64);uniqueIndex;not null"`
	IncidentID       string    `json:"incident_id" gorm:"column:incident_id;type:varchar(64);index:idx_incident_verification_runs_incident_created,priority:1;not null"`
	Actor            string    `json:"actor" gorm:"column:actor;type:varchar(128);not null"`
	Source           string    `json:"source" gorm:"column:source;type:varchar(64);not null"`
	StepIndex        int64     `json:"step_index" gorm:"column:step_index;not null;default:0"`
	Tool             string    `json:"tool" gorm:"column:tool;type:varchar(128);not null"`
	ParamsJSON       *string   `json:"params_json" gorm:"column:params_json;type:longtext"`
	Observed         string    `json:"observed" gorm:"column:observed;type:varchar(512);not null"`
	MeetsExpectation bool      `json:"meets_expectation" gorm:"column:meets_expectation;not null;default:false"`
	CreatedAt        time.Time `json:"created_at" gorm:"column:created_at;type:datetime;index:idx_incident_verification_runs_incident_created,priority:2;not null;default:CURRENT_TIMESTAMP"`
	UpdatedAt        time.Time `json:"updated_at" gorm:"column:updated_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
}

// TableName IncidentVerificationRunM's table name.
func (*IncidentVerificationRunM) TableName() string {
	return TableNameIncidentVerificationRunM
}
