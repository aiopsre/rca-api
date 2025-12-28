//nolint:tagalign
package model

import "time"

const TableNameNoticeDeliveryM = "notice_deliveries"

// NoticeDeliveryM stores one webhook delivery audit record.
type NoticeDeliveryM struct {
	ID                        int64      `gorm:"column:id;primaryKey;autoIncrement:true;index:idx_notice_deliveries_status_retry,priority:3" json:"id"`
	DeliveryID                string     `gorm:"column:delivery_id;type:varchar(64);uniqueIndex;not null" json:"delivery_id"`
	ChannelID                 string     `gorm:"column:channel_id;type:varchar(64);not null;index:idx_notice_deliveries_channel_created,priority:1" json:"channel_id"`
	EventType                 string     `gorm:"column:event_type;type:varchar(64);not null;index:idx_notice_deliveries_event_created,priority:1" json:"event_type"`
	IncidentID                *string    `gorm:"column:incident_id;type:varchar(64);index:idx_notice_deliveries_incident_created,priority:1" json:"incident_id"`
	JobID                     *string    `gorm:"column:job_id;type:varchar(64);index:idx_notice_deliveries_job_created,priority:1" json:"job_id"`
	RequestBody               string     `gorm:"column:request_body;type:longtext;not null" json:"request_body"`
	ResponseCode              *int32     `gorm:"column:response_code" json:"response_code"`
	ResponseBody              *string    `gorm:"column:response_body;type:longtext" json:"response_body"`
	LatencyMs                 int64      `gorm:"column:latency_ms;not null;default:0" json:"latency_ms"`
	Status                    string     `gorm:"column:status;type:varchar(16);not null;index:idx_notice_deliveries_status_created,priority:1;index:idx_notice_deliveries_status_retry,priority:1;index:idx_notice_deliveries_status_lock,priority:1" json:"status"`
	Attempts                  int64      `gorm:"column:attempts;not null;default:0" json:"attempts"`
	MaxAttempts               int64      `gorm:"column:max_attempts;not null;default:3" json:"max_attempts"`
	NextRetryAt               time.Time  `gorm:"column:next_retry_at;not null;default:current_timestamp;index:idx_notice_deliveries_status_retry,priority:2" json:"next_retry_at"`
	SnapshotEndpointURL       *string    `gorm:"column:snapshot_endpoint_url;type:varchar(2048)" json:"snapshot_endpoint_url"`
	SnapshotTimeoutMs         *int64     `gorm:"column:snapshot_timeout_ms" json:"snapshot_timeout_ms"`
	SnapshotHeadersJSON       *string    `gorm:"column:snapshot_headers_json;type:longtext" json:"snapshot_headers_json"`
	SnapshotSecretFingerprint *string    `gorm:"column:snapshot_secret_fingerprint;type:varchar(128)" json:"snapshot_secret_fingerprint"`
	SnapshotChannelVersion    *int64     `gorm:"column:snapshot_channel_version" json:"snapshot_channel_version"`
	LockedBy                  *string    `gorm:"column:locked_by;type:varchar(128)" json:"locked_by"`
	LockedAt                  *time.Time `gorm:"column:locked_at;index:idx_notice_deliveries_status_lock,priority:2" json:"locked_at"`
	IdempotencyKey            string     `gorm:"column:idempotency_key;type:varchar(128);not null;index:idx_notice_deliveries_idempotency_key" json:"idempotency_key"`
	Error                     *string    `gorm:"column:error;type:text" json:"error"`
	CreatedAt                 time.Time  `gorm:"column:created_at;not null;default:current_timestamp;index:idx_notice_deliveries_channel_created,priority:2;index:idx_notice_deliveries_incident_created,priority:2;index:idx_notice_deliveries_event_created,priority:2;index:idx_notice_deliveries_job_created,priority:2;index:idx_notice_deliveries_status_created,priority:2" json:"created_at"`
}

// TableName NoticeDeliveryM's table name.
func (*NoticeDeliveryM) TableName() string {
	return TableNameNoticeDeliveryM
}
