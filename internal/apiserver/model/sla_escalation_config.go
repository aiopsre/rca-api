package model

import "time"

const TableNameSLAEscalationConfigM = "sla_escalation_configs"

// SLAEscalationConfigM stores dynamic SLA due/escalation thresholds by session_type.
//
//nolint:tagalign // Keep explicit column/index tags aligned with existing model style.
type SLAEscalationConfigM struct {
	ID                       int64     `json:"id" gorm:"column:id;primaryKey;autoIncrement:true"`
	SessionType              string    `json:"session_type" gorm:"column:session_type;type:varchar(64);uniqueIndex;not null"`
	DueSeconds               int64     `json:"due_seconds" gorm:"column:due_seconds;not null;default:7200"`
	EscalationThresholdsJSON *string   `json:"escalation_thresholds_json" gorm:"column:escalation_thresholds_json;type:longtext"`
	CreatedAt                time.Time `json:"created_at" gorm:"column:created_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
	UpdatedAt                time.Time `json:"updated_at" gorm:"column:updated_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
}

func (*SLAEscalationConfigM) TableName() string {
	return TableNameSLAEscalationConfigM
}
