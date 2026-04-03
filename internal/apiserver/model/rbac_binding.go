package model

import "time"

const (
	TableNameRBACUserRoleM       = "rbac_user_roles"
	TableNameRBACRolePermissionM = "rbac_role_permissions"
)

//nolint:tagalign // Keep explicit column tags consistent with existing model style.
type RBACUserRoleM struct {
	ID        int64     `json:"id" gorm:"column:id;primaryKey;autoIncrement:true"`
	UserID    string    `json:"user_id" gorm:"column:user_id;type:varchar(64);uniqueIndex:uniq_rbac_user_role,priority:1;index;not null"`
	RoleID    string    `json:"role_id" gorm:"column:role_id;type:varchar(64);uniqueIndex:uniq_rbac_user_role,priority:2;index;not null"`
	CreatedAt time.Time `json:"created_at" gorm:"column:created_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
	UpdatedAt time.Time `json:"updated_at" gorm:"column:updated_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
}

func (*RBACUserRoleM) TableName() string {
	return TableNameRBACUserRoleM
}

//nolint:tagalign // Keep explicit column tags consistent with existing model style.
type RBACRolePermissionM struct {
	ID           int64     `json:"id" gorm:"column:id;primaryKey;autoIncrement:true"`
	RoleID       string    `json:"role_id" gorm:"column:role_id;type:varchar(64);uniqueIndex:uniq_rbac_role_permission,priority:1;index;not null"`
	PermissionID string    `json:"permission_id" gorm:"column:permission_id;type:varchar(96);uniqueIndex:uniq_rbac_role_permission,priority:2;index;not null"`
	CreatedAt    time.Time `json:"created_at" gorm:"column:created_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
	UpdatedAt    time.Time `json:"updated_at" gorm:"column:updated_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`
}

func (*RBACRolePermissionM) TableName() string {
	return TableNameRBACRolePermissionM
}
