package model

import "time"

const TableNameSkillReleaseM = "skill_releases"

// SkillReleaseM stores immutable skill bundle metadata and artifact references.
//
//nolint:tagalign // Keep explicit column/index tags aligned with existing model style.
type SkillReleaseM struct {
	ID           int64     `json:"id" gorm:"column:id;primaryKey;autoIncrement:true"`
	SkillID      string    `json:"skill_id" gorm:"column:skill_id;type:varchar(128);uniqueIndex:uniq_skill_releases_skill_version,priority:1;index:idx_skill_releases_skill_status_created,priority:1;not null"`
	Version      string    `json:"version" gorm:"column:version;type:varchar(64);uniqueIndex:uniq_skill_releases_skill_version,priority:2;not null"`
	BundleDigest string    `json:"bundle_digest" gorm:"column:bundle_digest;type:varchar(128);not null"`
	ArtifactURL  string    `json:"artifact_url" gorm:"column:artifact_url;type:text;not null"`
	ManifestJSON *string   `json:"manifest_json" gorm:"column:manifest_json;type:longtext"`
	Status       string    `json:"status" gorm:"column:status;type:varchar(32);index:idx_skill_releases_skill_status_created,priority:2;not null;default:active"`
	CreatedBy    *string   `json:"created_by" gorm:"column:created_by;type:varchar(191)"`
	CreatedAt    time.Time `json:"created_at" gorm:"column:created_at;type:datetime;index:idx_skill_releases_skill_status_created,priority:3;not null;default:CURRENT_TIMESTAMP"`
	UpdatedAt    time.Time `json:"updated_at" gorm:"column:updated_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
}

func (*SkillReleaseM) TableName() string {
	return TableNameSkillReleaseM
}
