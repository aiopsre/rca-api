package model

import "time"

const TableNameRBACPermissionM = "rbac_permissions"

//nolint:tagalign // Keep explicit column tags consistent with existing model style.
type RBACPermissionM struct {
	ID           int64     `json:"id" gorm:"column:id;primaryKey;autoIncrement:true"`
	PermissionID string    `json:"permission_id" gorm:"column:permission_id;type:varchar(96);uniqueIndex;not null"`
	Resource     string    `json:"resource" gorm:"column:resource;type:varchar(255);index:idx_rbac_permissions_resource_action,priority:1;not null"`
	Action       string    `json:"action" gorm:"column:action;type:varchar(64);index:idx_rbac_permissions_resource_action,priority:2;not null"`
	Description  string    `json:"description" gorm:"column:description;type:text;not null"`
	Status       string    `json:"status" gorm:"column:status;type:varchar(32);index;not null;default:active"`
	CreatedAt    time.Time `json:"created_at" gorm:"column:created_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
	UpdatedAt    time.Time `json:"updated_at" gorm:"column:updated_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
}

func (*RBACPermissionM) TableName() string {
	return TableNameRBACPermissionM
}
