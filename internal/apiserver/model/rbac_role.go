package model

import "time"

const TableNameRBACRoleM = "rbac_roles"

//nolint:tagalign // Keep explicit column tags consistent with existing model style.
type RBACRoleM struct {
	ID          int64     `json:"id" gorm:"column:id;primaryKey;autoIncrement:true"`
	RoleID      string    `json:"role_id" gorm:"column:role_id;type:varchar(64);uniqueIndex;not null"`
	DisplayName string    `json:"display_name" gorm:"column:display_name;type:varchar(128);not null;default:''"`
	Description string    `json:"description" gorm:"column:description;type:text;not null"`
	Status      string    `json:"status" gorm:"column:status;type:varchar(32);index;not null;default:active"`
	CreatedAt   time.Time `json:"created_at" gorm:"column:created_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
	UpdatedAt   time.Time `json:"updated_at" gorm:"column:updated_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
}

func (*RBACRoleM) TableName() string {
	return TableNameRBACRoleM
}
