package model

import "time"

const TableNameKBEntryM = "kb_entries"

// KBEntryM stores one reusable root-cause knowledge entry.
type KBEntryM struct {
	ID                    int64      `json:"id" gorm:"column:id;primaryKey;autoIncrement:true"`
	KBID                  string     `json:"kb_id" gorm:"column:kb_id;type:varchar(64);uniqueIndex;not null"`
	Namespace             string     `json:"namespace" gorm:"column:namespace;type:varchar(128);index:idx_kb_entries_scope_type,priority:1;uniqueIndex:uniq_kb_entries_scope_type_patterns,priority:1;not null;default:''"`
	Service               string     `json:"service" gorm:"column:service;type:varchar(256);index:idx_kb_entries_scope_type,priority:2;uniqueIndex:uniq_kb_entries_scope_type_patterns,priority:2;not null;default:''"`
	RootCauseType         string     `json:"root_cause_type" gorm:"column:root_cause_type;type:varchar(64);index:idx_kb_entries_scope_type,priority:3;uniqueIndex:uniq_kb_entries_scope_type_patterns,priority:3;not null"`
	RootCauseSummary      string     `json:"root_cause_summary" gorm:"column:root_cause_summary;type:varchar(512);not null"`
	PatternsJSON          string     `json:"patterns_json" gorm:"column:patterns_json;type:varchar(4096);not null"`
	PatternsHash          string     `json:"patterns_hash" gorm:"column:patterns_hash;type:char(64);uniqueIndex:uniq_kb_entries_scope_type_patterns,priority:4;not null"`
	EvidenceSignatureJSON *string    `json:"evidence_signature_json" gorm:"column:evidence_signature_json;type:varchar(4096)"`
	Confidence            float64    `json:"confidence" gorm:"column:confidence;not null;default:0.7"`
	HitCount              int64      `json:"hit_count" gorm:"column:hit_count;not null;default:0"`
	LastHitAt             *time.Time `json:"last_hit_at" gorm:"column:last_hit_at"`
	CreatedAt             time.Time  `json:"created_at" gorm:"column:created_at;not null;default:CURRENT_TIMESTAMP"`
	UpdatedAt             time.Time  `json:"updated_at" gorm:"column:updated_at;index:idx_kb_entries_updated_at;not null;default:CURRENT_TIMESTAMP"`
}

// TableName KBEntryM's table name.
func (*KBEntryM) TableName() string {
	return TableNameKBEntryM
}
