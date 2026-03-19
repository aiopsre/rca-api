package model

import "time"

const TableNameVerificationTemplateM = "verification_templates"

// VerificationTemplateM stores dynamic verification step templates.
//
//nolint:tagalign // Keep explicit column/index tags aligned with existing model style.
type VerificationTemplateM struct {
	ID              int64      `json:"id" gorm:"column:id;primaryKey;autoIncrement:true"`
	Name            string     `json:"name" gorm:"column:name;type:varchar(128);not null;comment:模板名称"`
	Description     *string    `json:"description" gorm:"column:description;type:text;comment:模板描述"`
	LineageID       string     `json:"lineage_id" gorm:"column:lineage_id;type:varchar(64);index:idx_verification_templates_lineage_version,priority:1;comment:逻辑模板版本链归属 ID"`
	Version         int        `json:"version" gorm:"column:version;not null;default:1;index:idx_verification_templates_lineage_version,priority:2;comment:版本号（自动递增）"`
	MatchJSON       string     `json:"match_json" gorm:"column:match_json;type:text;not null;comment:匹配条件 JSON（root_cause_types, patterns_contain, confidence_min）"`
	StepsJSON       string     `json:"steps_json" gorm:"column:steps_json;type:longtext;not null;comment:验证步骤 JSON（复用 verification.Plan 格式）"`
	Active          bool       `json:"active" gorm:"column:active;not null;default:false;index:idx_verification_templates_active;comment:是否激活"`
	ActivatedAt     *time.Time `json:"activated_at" gorm:"column:activated_at;comment:激活时间"`
	ActivatedBy     *string    `json:"activated_by" gorm:"column:activated_by;type:varchar(128);comment:激活人"`
	PreviousVersion *int       `json:"previous_version" gorm:"column:previous_version;comment:前一个版本号（用于回滚）"`
	CreatedBy       string     `json:"created_by" gorm:"column:created_by;type:varchar(128);not null;comment:创建人"`
	UpdatedBy       *string    `json:"updated_by" gorm:"column:updated_by;type:varchar(128);comment:更新人"`
	CreatedAt       time.Time  `json:"created_at" gorm:"column:created_at;type:datetime;not null;default:CURRENT_TIMESTAMP;comment:创建时间"`
	UpdatedAt       time.Time  `json:"updated_at" gorm:"column:updated_at;type:datetime;not null;default:CURRENT_TIMESTAMP;comment:更新时间"`
}

// TableName VerificationTemplateM's table name.
func (*VerificationTemplateM) TableName() string {
	return TableNameVerificationTemplateM
}