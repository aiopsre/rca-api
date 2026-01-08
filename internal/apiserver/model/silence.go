//nolint:tagalign
package model

import "time"

const TableNameSilenceM = "silences"

// SilenceM stores one suppression/maintenance rule for a time window.
type SilenceM struct {
	ID           int64     `gorm:"column:id;primaryKey;autoIncrement:true" json:"id"`
	SilenceID    string    `gorm:"column:silence_id;type:varchar(64);uniqueIndex;not null" json:"silence_id"`
	Namespace    string    `gorm:"column:namespace;type:varchar(128);not null;index:idx_silences_namespace_enabled_window,priority:1" json:"namespace"`
	Enabled      bool      `gorm:"column:enabled;not null;default:true;index:idx_silences_namespace_enabled_window,priority:2" json:"enabled"`
	StartsAt     time.Time `gorm:"column:starts_at;not null;index:idx_silences_namespace_enabled_window,priority:3" json:"starts_at"`
	EndsAt       time.Time `gorm:"column:ends_at;not null;index:idx_silences_namespace_enabled_window,priority:4" json:"ends_at"`
	Reason       *string   `gorm:"column:reason;type:text" json:"reason"`
	CreatedBy    *string   `gorm:"column:created_by;type:varchar(128)" json:"created_by"`
	MatchersJSON string    `gorm:"column:matchers_json;type:longtext;not null" json:"matchers_json"`
	CreatedAt    time.Time `gorm:"column:created_at;not null;default:CURRENT_TIMESTAMP" json:"created_at"`
	UpdatedAt    time.Time `gorm:"column:updated_at;not null;default:CURRENT_TIMESTAMP" json:"updated_at"`
}

// TableName SilenceM's table name.
func (*SilenceM) TableName() string {
	return TableNameSilenceM
}
