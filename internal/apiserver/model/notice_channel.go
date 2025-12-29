//nolint:tagalign
package model

import "time"

const TableNameNoticeChannelM = "notice_channels"

// NoticeChannelM stores one webhook notice channel config.
type NoticeChannelM struct {
	ID            int64   `gorm:"column:id;primaryKey;autoIncrement:true" json:"id"`
	ChannelID     string  `gorm:"column:channel_id;type:varchar(64);uniqueIndex;not null" json:"channel_id"`
	Name          string  `gorm:"column:name;type:varchar(128);not null;index:idx_notice_channels_name" json:"name"`
	Type          string  `gorm:"column:type;type:varchar(32);not null;default:webhook;index:idx_notice_channels_type_enabled,priority:1" json:"type"`
	Enabled       bool    `gorm:"column:enabled;not null;default:true;index:idx_notice_channels_type_enabled,priority:2" json:"enabled"`
	EndpointURL   string  `gorm:"column:endpoint_url;type:varchar(2048);not null" json:"endpoint_url"`
	Secret        *string `gorm:"column:secret;type:text" json:"secret"`
	HeadersJSON   *string `gorm:"column:headers_json;type:longtext" json:"headers_json"`
	SelectorsJSON *string `gorm:"column:selectors_json;type:longtext" json:"selectors_json"`
	TimeoutMs     int64   `gorm:"column:timeout_ms;not null;default:3000" json:"timeout_ms"`
	MaxRetries    int64   `gorm:"column:max_retries;not null;default:0" json:"max_retries"`
	PayloadMode   string  `gorm:"column:payload_mode;type:varchar(16);not null;default:COMPACT" json:"payload_mode"`
	// include_* toggles are resolved at create/patch time based on payload mode defaults.
	IncludeDiagnosis   bool      `gorm:"column:include_diagnosis;not null;default:false" json:"include_diagnosis"`
	IncludeEvidenceIDs bool      `gorm:"column:include_evidence_ids;not null;default:false" json:"include_evidence_ids"`
	IncludeRootCause   bool      `gorm:"column:include_root_cause;not null;default:false" json:"include_root_cause"`
	IncludeLinks       bool      `gorm:"column:include_links;not null;default:false" json:"include_links"`
	CreatedAt          time.Time `gorm:"column:created_at;not null;default:current_timestamp" json:"created_at"`
	UpdatedAt          time.Time `gorm:"column:updated_at;not null;default:current_timestamp" json:"updated_at"`
}

// TableName NoticeChannelM's table name.
func (*NoticeChannelM) TableName() string {
	return TableNameNoticeChannelM
}
