package model

import "time"

const TableNameTriggerConfigM = "trigger_configs"

// TriggerConfigM stores dynamic trigger_type routing and fallback semantics.
//
//nolint:tagalign // Keep explicit column/index tags aligned with existing model style.
type TriggerConfigM struct {
	ID          int64     `json:"id" gorm:"column:id;primaryKey;autoIncrement:true"`
	TriggerType string    `json:"trigger_type" gorm:"column:trigger_type;type:varchar(64);uniqueIndex;not null"`
	PipelineID  string    `json:"pipeline_id" gorm:"column:pipeline_id;type:varchar(64);not null"`
	SessionType string    `json:"session_type" gorm:"column:session_type;type:varchar(64);not null;default:''"`
	Fallback    bool      `json:"fallback" gorm:"column:fallback;not null;default:false"`
	CreatedAt   time.Time `json:"created_at" gorm:"column:created_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
	UpdatedAt   time.Time `json:"updated_at" gorm:"column:updated_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
}

func (*TriggerConfigM) TableName() string {
	return TableNameTriggerConfigM
}
