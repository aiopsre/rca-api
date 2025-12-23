//nolint:tagalign
package model

import "time"

const TableNameAlertEventM = "alert_events_history"

// AlertEventM stores alert history rows and current view rows in one table.
// P0 strategy:
// - history rows are append-only (is_current=false)
// - current view rows are maintained with is_current=true + current_key=fingerprint
type AlertEventM struct {
	ID              int64      `gorm:"column:id;primaryKey;autoIncrement:true" json:"id"`
	EventID         string     `gorm:"column:event_id;type:varchar(64);uniqueIndex;not null" json:"event_id"`
	IncidentID      *string    `gorm:"column:incident_id;type:varchar(64);index:idx_alert_events_incident_id_created_at,priority:1" json:"incident_id"`
	Fingerprint     string     `gorm:"column:fingerprint;type:varchar(128);not null;index:idx_alert_events_fingerprint_last_seen,priority:1;index:idx_alert_events_current_fingerprint,priority:2" json:"fingerprint"`
	DedupKey        string     `gorm:"column:dedup_key;type:varchar(128);not null;default:''" json:"dedup_key"`
	Source          string     `gorm:"column:source;type:varchar(32);not null;default:alertmanager" json:"source"`
	Status          string     `gorm:"column:status;type:varchar(32);not null;index:idx_alert_events_severity_status_last_seen,priority:2;index:idx_alert_events_current_status,priority:2" json:"status"`
	Severity        string     `gorm:"column:severity;type:varchar(32);not null;index:idx_alert_events_severity_status_last_seen,priority:1;index:idx_alert_events_current_severity,priority:2" json:"severity"`
	AlertName       *string    `gorm:"column:alert_name;type:varchar(256)" json:"alert_name"`
	Service         *string    `gorm:"column:service;type:varchar(128);index:idx_alert_events_current_service,priority:2" json:"service"`
	Cluster         *string    `gorm:"column:cluster;type:varchar(128);index:idx_alert_events_current_cluster,priority:2" json:"cluster"`
	Namespace       *string    `gorm:"column:namespace;type:varchar(128);index:idx_alert_events_current_namespace,priority:2" json:"namespace"`
	Workload        *string    `gorm:"column:workload;type:varchar(256)" json:"workload"`
	StartsAt        *time.Time `gorm:"column:starts_at" json:"starts_at"`
	EndsAt          *time.Time `gorm:"column:ends_at" json:"ends_at"`
	LastSeenAt      time.Time  `gorm:"column:last_seen_at;not null;index:idx_alert_events_fingerprint_last_seen,priority:2;index:idx_alert_events_severity_status_last_seen,priority:3;index:idx_alert_events_current_last_seen,priority:2;index:idx_alert_events_current_fingerprint,priority:3;index:idx_alert_events_current_severity,priority:3;index:idx_alert_events_current_status,priority:3;index:idx_alert_events_current_service,priority:3;index:idx_alert_events_current_cluster,priority:3;index:idx_alert_events_current_namespace,priority:3" json:"last_seen_at"`
	LabelsJSON      *string    `gorm:"column:labels_json;type:longtext" json:"labels_json"`
	AnnotationsJSON *string    `gorm:"column:annotations_json;type:longtext" json:"annotations_json"`
	GeneratorURL    *string    `gorm:"column:generator_url;type:text" json:"generator_url"`
	RawEventJSON    *string    `gorm:"column:raw_event_json;type:longtext" json:"raw_event_json"`
	IsCurrent       bool       `gorm:"column:is_current;not null;default:false;index:idx_alert_events_current_last_seen,priority:1;index:idx_alert_events_current_fingerprint,priority:1;index:idx_alert_events_current_severity,priority:1;index:idx_alert_events_current_status,priority:1;index:idx_alert_events_current_service,priority:1;index:idx_alert_events_current_cluster,priority:1;index:idx_alert_events_current_namespace,priority:1" json:"is_current"`
	// current_key uses nullable unique index to guarantee only one current row per fingerprint.
	CurrentKey *string `gorm:"column:current_key;type:varchar(128);uniqueIndex:uniq_alert_events_current_key" json:"current_key"`
	// idempotency_key uses nullable unique index for Appendix G3 retry safety.
	IdempotencyKey *string    `gorm:"column:idempotency_key;type:varchar(128);uniqueIndex:uniq_alert_events_idempotency_key" json:"idempotency_key"`
	AckedAt        *time.Time `gorm:"column:acked_at" json:"acked_at"`
	AckedBy        *string    `gorm:"column:acked_by;type:varchar(128)" json:"acked_by"`
	IsSilenced     bool       `gorm:"column:is_silenced;not null;default:false;index:idx_alert_events_current_silenced,priority:1" json:"is_silenced"`
	SilenceID      *string    `gorm:"column:silence_id;type:varchar(64);index:idx_alert_events_current_silenced,priority:2" json:"silence_id"`
	CreatedAt      time.Time  `gorm:"column:created_at;index:idx_alert_events_incident_id_created_at,priority:2;not null;default:current_timestamp" json:"created_at"`
	UpdatedAt      time.Time  `gorm:"column:updated_at;not null;default:current_timestamp" json:"updated_at"`
}

// TableName AlertEventM's table name.
func (*AlertEventM) TableName() string {
	return TableNameAlertEventM
}
