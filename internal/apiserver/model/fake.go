package model

import (
	"time"
)

const TableNameFakeM = "rca_fake"

// FakeM mapped from table <fake>
type FakeM struct {
	ID        int64     `json:"id" gorm:"column:id;primaryKey;autoIncrement:true"`
	FakeID    string    `json:"fake_id" gorm:"column:fake_id;not null;comment:资源唯一 ID"`                                // 资源唯一 ID
	CreatedAt time.Time `json:"createdAt" gorm:"column:createdAt;not null;default:CURRENT_TIMESTAMP;comment:资源创建时间"`   // 资源创建时间
	UpdatedAt time.Time `json:"updatedAt" gorm:"column:updatedAt;not null;default:CURRENT_TIMESTAMP;comment:资源最后修改时间"` // 资源最后修改时间
}

// TableName FakeM's table name
func (*FakeM) TableName() string {
	return TableNameFakeM
}
