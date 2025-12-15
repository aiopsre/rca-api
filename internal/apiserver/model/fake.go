package model

import (
	"time"
)

const TableNameFakeM = "rca_fake"

// FakeM mapped from table <fake>
type FakeM struct {
	ID        int64     `gorm:"column:id;primaryKey;autoIncrement:true" json:"id"`
	FakeID    string    `gorm:"column:fake_id;not null;comment:资源唯一 ID" json:"fake_id"`                                // 资源唯一 ID
	CreatedAt time.Time `gorm:"column:createdAt;not null;default:current_timestamp;comment:资源创建时间" json:"createdAt"`   // 资源创建时间
	UpdatedAt time.Time `gorm:"column:updatedAt;not null;default:current_timestamp;comment:资源最后修改时间" json:"updatedAt"` // 资源最后修改时间
}

// TableName FakeM's table name
func (*FakeM) TableName() string {
	return TableNameFakeM
}
