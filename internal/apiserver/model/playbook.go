package model

import "time"

const TableNamePlaybookM = "playbooks"

// PlaybookM stores dynamic RCA playbook configurations.
//
//nolint:tagalign // Keep explicit column/index tags aligned with existing model style.
type PlaybookM struct {
	ID              int64      `json:"id" gorm:"column:id;primaryKey;autoIncrement:true"`
	Name            string     `json:"name" gorm:"column:name;type:varchar(128);not null;comment:剧本名称"`
	Description     *string    `json:"description" gorm:"column:description;type:text;comment:剧本描述"`
	LineageID       string     `json:"lineage_id" gorm:"column:lineage_id;type:varchar(64);index:idx_playbooks_lineage_version,priority:1;comment:逻辑剧本版本链归属 ID"`
	Version         int        `json:"version" gorm:"column:version;not null;default:1;index:idx_playbooks_lineage_version,priority:2;comment:版本号（自动递增）"`
	ConfigJSON      string     `json:"config_json" gorm:"column:config_json;type:longtext;not null;comment:剧本配置 JSON"`
	Active          bool       `json:"active" gorm:"column:active;not null;default:false;index:idx_playbooks_active;comment:是否激活（同一时间只能有一个激活）"`
	ActivatedAt     *time.Time `json:"activated_at" gorm:"column:activated_at;comment:激活时间"`
	ActivatedBy     *string    `json:"activated_by" gorm:"column:activated_by;type:varchar(128);comment:激活人"`
	PreviousVersion *int       `json:"previous_version" gorm:"column:previous_version;comment:前一个版本号（用于回滚）"`
	CreatedBy       string     `json:"created_by" gorm:"column:created_by;type:varchar(128);not null;comment:创建人"`
	UpdatedBy       *string    `json:"updated_by" gorm:"column:updated_by;type:varchar(128);comment:更新人"`
	CreatedAt       time.Time  `json:"created_at" gorm:"column:created_at;type:datetime;not null;default:CURRENT_TIMESTAMP;comment:创建时间"`
	UpdatedAt       time.Time  `json:"updated_at" gorm:"column:updated_at;type:datetime;not null;default:CURRENT_TIMESTAMP;comment:更新时间"`
}

// TableName PlaybookM's table name.
func (*PlaybookM) TableName() string {
	return TableNamePlaybookM
}
