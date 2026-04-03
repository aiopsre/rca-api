package model

import "time"

const TableNameRBACUserM = "rbac_users"

// RBACUserM stores operator identity used by rbac role bindings.
//
//nolint:tagalign // Keep explicit column tags consistent with existing model style.
type RBACUserM struct {
	ID           int64     `json:"id" gorm:"column:id;primaryKey;autoIncrement:true"`
	UserID       string    `json:"user_id" gorm:"column:user_id;type:varchar(64);uniqueIndex;not null"`
	Username     string    `json:"username" gorm:"column:username;type:varchar(128);not null;default:''"`
	PasswordHash *string   `json:"password_hash" gorm:"column:password_hash;type:varchar(255)"`
	TeamID       string    `json:"team_id" gorm:"column:team_id;type:varchar(64);not null;default:''"`
	Status       string    `json:"status" gorm:"column:status;type:varchar(32);index;not null;default:active"`
	CreatedAt    time.Time `json:"created_at" gorm:"column:created_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
	UpdatedAt    time.Time `json:"updated_at" gorm:"column:updated_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
}

func (*RBACUserM) TableName() string {
	return TableNameRBACUserM
}
