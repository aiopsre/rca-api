//nolint:tagalign
package model

import "time"

const TableNameAIJobQueueSignalM = "ai_job_queue_signal"

// AIJobQueueSignalM stores queue watermark used by cross-instance long polling.
type AIJobQueueSignalM struct {
	ID        int64     `gorm:"column:id;primaryKey;autoIncrement:false" json:"id"`
	Version   int64     `gorm:"column:version;not null;default:1" json:"version"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null;default:CURRENT_TIMESTAMP" json:"updated_at"`
}

// TableName AIJobQueueSignalM's table name.
func (*AIJobQueueSignalM) TableName() string {
	return TableNameAIJobQueueSignalM
}
