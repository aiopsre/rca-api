//nolint:tagalign
package model

import "time"

const TableNameNoticeDeliveryM = "notice_deliveries"

// NoticeDeliveryM stores one webhook delivery audit record.
type NoticeDeliveryM struct {
	ID           int64     `gorm:"column:id;primaryKey;autoIncrement:true" json:"id"`
	DeliveryID   string    `gorm:"column:delivery_id;type:varchar(64);uniqueIndex;not null" json:"delivery_id"`
	ChannelID    string    `gorm:"column:channel_id;type:varchar(64);not null;index:idx_notice_deliveries_channel_created,priority:1" json:"channel_id"`
	EventType    string    `gorm:"column:event_type;type:varchar(64);not null;index:idx_notice_deliveries_event_created,priority:1" json:"event_type"`
	IncidentID   *string   `gorm:"column:incident_id;type:varchar(64);index:idx_notice_deliveries_incident_created,priority:1" json:"incident_id"`
	JobID        *string   `gorm:"column:job_id;type:varchar(64);index:idx_notice_deliveries_job_created,priority:1" json:"job_id"`
	RequestBody  string    `gorm:"column:request_body;type:longtext;not null" json:"request_body"`
	ResponseCode *int32    `gorm:"column:response_code" json:"response_code"`
	ResponseBody *string   `gorm:"column:response_body;type:longtext" json:"response_body"`
	LatencyMs    int64     `gorm:"column:latency_ms;not null;default:0" json:"latency_ms"`
	Status       string    `gorm:"column:status;type:varchar(16);not null;index:idx_notice_deliveries_status_created,priority:1" json:"status"`
	Error        *string   `gorm:"column:error;type:text" json:"error"`
	CreatedAt    time.Time `gorm:"column:created_at;not null;default:current_timestamp;index:idx_notice_deliveries_channel_created,priority:2;index:idx_notice_deliveries_incident_created,priority:2;index:idx_notice_deliveries_event_created,priority:2;index:idx_notice_deliveries_job_created,priority:2;index:idx_notice_deliveries_status_created,priority:2" json:"created_at"`
}

// TableName NoticeDeliveryM's table name.
func (*NoticeDeliveryM) TableName() string {
	return TableNameNoticeDeliveryM
}
