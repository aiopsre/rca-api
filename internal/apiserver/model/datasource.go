package model

import "time"

const TableNameDatasourceM = "datasources"

// DatasourceM stores datasource configs for metrics/logs queries.
type DatasourceM struct {
	ID                 int64     `json:"id" gorm:"column:id;primaryKey;autoIncrement:true"`
	DatasourceID       string    `json:"datasource_id" gorm:"column:datasource_id;type:varchar(64);uniqueIndex;not null"`
	Type               string    `json:"type" gorm:"column:type;type:varchar(64);index:idx_datasource_type_enabled,priority:1;not null"`
	Name               string    `json:"name" gorm:"column:name;not null"`
	BaseURL            string    `json:"base_url" gorm:"column:base_url;not null"`
	AuthType           string    `json:"auth_type" gorm:"column:auth_type;not null;default:none"`
	AuthSecretRef      *string   `json:"auth_secret_ref" gorm:"column:auth_secret_ref"`
	TimeoutMs          int64     `json:"timeout_ms" gorm:"column:timeout_ms;not null;default:5000"`
	IsEnabled          bool      `json:"is_enabled" gorm:"column:is_enabled;index:idx_datasource_type_enabled,priority:2;not null;default:true"`
	LabelsJSON         *string   `json:"labels_json" gorm:"column:labels_json;type:text"`
	DefaultHeadersJSON *string   `json:"default_headers_json" gorm:"column:default_headers_json;type:text"`
	TLSConfigJSON      *string   `json:"tls_config_json" gorm:"column:tls_config_json;type:text"`
	CreatedAt          time.Time `json:"created_at" gorm:"column:created_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
	UpdatedAt          time.Time `json:"updated_at" gorm:"column:updated_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
}

// TableName DatasourceM's table name.
func (*DatasourceM) TableName() string {
	return TableNameDatasourceM
}
