package model

import "time"

const TableNamePipelineConfigM = "pipeline_configs"

// PipelineConfigM stores dynamic alert-source to pipeline/graph mapping.
//
//nolint:tagalign // Keep explicit column/index tags aligned with existing model style.
type PipelineConfigM struct {
	ID          int64     `json:"id" gorm:"column:id;primaryKey;autoIncrement:true"`
	AlertSource string    `json:"alert_source" gorm:"column:alert_source;type:varchar(64);uniqueIndex:uniq_pipeline_configs_match,priority:1;index:idx_pipeline_configs_lookup,priority:1;not null;default:''"`
	Service     string    `json:"service" gorm:"column:service;type:varchar(128);uniqueIndex:uniq_pipeline_configs_match,priority:2;index:idx_pipeline_configs_lookup,priority:2;not null;default:''"`
	Namespace   string    `json:"namespace" gorm:"column:namespace;type:varchar(128);uniqueIndex:uniq_pipeline_configs_match,priority:3;index:idx_pipeline_configs_lookup,priority:3;not null;default:''"`
	PipelineID  string    `json:"pipeline_id" gorm:"column:pipeline_id;type:varchar(64);not null"`
	GraphID     *string   `json:"graph_id" gorm:"column:graph_id;type:varchar(128)"`
	CreatedAt   time.Time `json:"created_at" gorm:"column:created_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
	UpdatedAt   time.Time `json:"updated_at" gorm:"column:updated_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
}

func (*PipelineConfigM) TableName() string {
	return TableNamePipelineConfigM
}
