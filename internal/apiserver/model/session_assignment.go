package model

import "time"

const TableNameSessionAssignmentM = "session_assignments"

// SessionAssignmentM stores latest assignment read model for /v1/session/:id/assignment.
//
//nolint:tagalign // Keep explicit column/index tags aligned with existing model style.
type SessionAssignmentM struct {
	ID         int64      `json:"id" gorm:"column:id;primaryKey;autoIncrement:true"`
	SessionID  string     `json:"session_id" gorm:"column:session_id;type:varchar(64);uniqueIndex;not null"`
	Assignee   string     `json:"assignee" gorm:"column:assignee;type:varchar(128);not null;default:''"`
	AssignedBy string     `json:"assigned_by" gorm:"column:assigned_by;type:varchar(128);not null;default:''"`
	AssignedAt *time.Time `json:"assigned_at" gorm:"column:assigned_at;type:datetime"`
	Note       *string    `json:"note" gorm:"column:note;type:text"`
	CreatedAt  time.Time  `json:"created_at" gorm:"column:created_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
	UpdatedAt  time.Time  `json:"updated_at" gorm:"column:updated_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
}

func (*SessionAssignmentM) TableName() string {
	return TableNameSessionAssignmentM
}
