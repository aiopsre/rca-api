package model

import (
	"github.com/onexstack/onexstack/pkg/store/registry"
	"gorm.io/gorm"

	"zk8s.com/rca-api/internal/pkg/rid"
)

// AfterCreate generates alert event business ID.
func (m *AlertEventM) AfterCreate(tx *gorm.DB) error {
	m.EventID = rid.AlertEventID.New(uint64(m.ID))
	return tx.Model(m).UpdateColumn("event_id", m.EventID).Error
}

func init() {
	registry.Register(&AlertEventM{})
}
