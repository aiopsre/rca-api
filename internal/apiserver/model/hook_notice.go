//nolint:dupl
package model

import (
	"github.com/onexstack/onexstack/pkg/store/registry"
	"gorm.io/gorm"

	"zk8s.com/rca-api/internal/pkg/rid"
)

// AfterCreate generates notice channel business ID.
func (m *NoticeChannelM) AfterCreate(tx *gorm.DB) error {
	m.ChannelID = rid.NoticeChannelID.New(uint64(m.ID))
	return tx.Model(m).UpdateColumn("channel_id", m.ChannelID).Error
}

// AfterCreate generates notice delivery business ID.
func (m *NoticeDeliveryM) AfterCreate(tx *gorm.DB) error {
	m.DeliveryID = rid.NoticeDeliveryID.New(uint64(m.ID))
	return tx.Model(m).UpdateColumn("delivery_id", m.DeliveryID).Error
}

//nolint:gochecknoinits // Model registry hooks are intentionally init-based in this codebase.
func init() {
	registry.Register(&NoticeChannelM{})
	registry.Register(&NoticeDeliveryM{})
}
