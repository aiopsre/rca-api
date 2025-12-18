package model

import "time"

const TableNameDatasourceM = "datasources"

// DatasourceM stores datasource configs for metrics/logs queries.
type DatasourceM struct {
	ID                 int64     `gorm:"column:id;primaryKey;autoIncrement:true" json:"id"`
	DatasourceID       string    `gorm:"column:datasource_id;uniqueIndex;not null" json:"datasource_id"`
	Type               string    `gorm:"column:type;index:idx_datasource_type_enabled,priority:1;not null" json:"type"`
	Name               string    `gorm:"column:name;not null" json:"name"`
	BaseURL            string    `gorm:"column:base_url;not null" json:"base_url"`
	AuthType           string    `gorm:"column:auth_type;not null;default:none" json:"auth_type"`
	AuthSecretRef      *string   `gorm:"column:auth_secret_ref" json:"auth_secret_ref"`
	TimeoutMs          int64     `gorm:"column:timeout_ms;not null;default:5000" json:"timeout_ms"`
	IsEnabled          bool      `gorm:"column:is_enabled;index:idx_datasource_type_enabled,priority:2;not null;default:true" json:"is_enabled"`
	LabelsJSON         *string   `gorm:"column:labels_json;type:text" json:"labels_json"`
	DefaultHeadersJSON *string   `gorm:"column:default_headers_json;type:text" json:"default_headers_json"`
	TLSConfigJSON      *string   `gorm:"column:tls_config_json;type:text" json:"tls_config_json"`
	CreatedAt          time.Time `gorm:"column:created_at;not null;default:current_timestamp" json:"created_at"`
	UpdatedAt          time.Time `gorm:"column:updated_at;not null;default:current_timestamp" json:"updated_at"`
}

// TableName DatasourceM's table name.
func (*DatasourceM) TableName() string {
	return TableNameDatasourceM
}
