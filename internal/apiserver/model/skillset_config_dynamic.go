package model

import "time"

const TableNameSkillsetConfigDynamicM = "skillset_configs_dynamic"

// SkillsetConfigDynamicM stores dynamic pipeline to skillset bindings.
//
//nolint:tagalign // Keep explicit column/index tags aligned with existing model style.
type SkillsetConfigDynamicM struct {
	ID            int64     `json:"id" gorm:"column:id;primaryKey;autoIncrement:true"`
	PipelineID    string    `json:"pipeline_id" gorm:"column:pipeline_id;type:varchar(64);uniqueIndex:uniq_skillset_configs_dynamic_pipeline_skillset,priority:1;index:idx_skillset_configs_dynamic_pipeline,priority:1;not null"`
	SkillsetName  string    `json:"skillset_name" gorm:"column:skillset_name;type:varchar(128);uniqueIndex:uniq_skillset_configs_dynamic_pipeline_skillset,priority:2;not null"`
	SkillRefsJSON *string   `json:"skill_refs_json" gorm:"column:skill_refs_json;type:longtext"`
	CreatedAt     time.Time `json:"created_at" gorm:"column:created_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
	UpdatedAt     time.Time `json:"updated_at" gorm:"column:updated_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
}

func (*SkillsetConfigDynamicM) TableName() string {
	return TableNameSkillsetConfigDynamicM
}
