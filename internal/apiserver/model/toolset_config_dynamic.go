package model

import "time"

const TableNameToolsetConfigDynamicM = "toolset_configs_dynamic"

// ToolsetConfigDynamicM stores dynamic pipeline to toolset mapping.
//
//nolint:tagalign // Keep explicit column/index tags aligned with existing model style.
type ToolsetConfigDynamicM struct {
	ID               int64     `json:"id" gorm:"column:id;primaryKey;autoIncrement:true"`
	PipelineID       string    `json:"pipeline_id" gorm:"column:pipeline_id;type:varchar(64);uniqueIndex:uniq_toolset_configs_dynamic_pipeline_toolset,priority:1;index:idx_toolset_configs_dynamic_pipeline,priority:1;not null"`
	ToolsetName      string    `json:"toolset_name" gorm:"column:toolset_name;type:varchar(128);uniqueIndex:uniq_toolset_configs_dynamic_pipeline_toolset,priority:2;not null"`
	AllowedToolsJSON *string   `json:"allowed_tools_json" gorm:"column:allowed_tools_json;type:longtext"`
	CreatedAt        time.Time `json:"created_at" gorm:"column:created_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
	UpdatedAt        time.Time `json:"updated_at" gorm:"column:updated_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
}

func (*ToolsetConfigDynamicM) TableName() string {
	return TableNameToolsetConfigDynamicM
}
