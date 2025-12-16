package model

import (
	"github.com/onexstack/onexstack/pkg/rid"
	"github.com/onexstack/onexstack/pkg/store/registry"
	"gorm.io/gorm"
)

// AfterCreate generates and updates the IncidentID after the database record is created.
func (m *IncidentM) AfterCreate(tx *gorm.DB) error {
	// Generate the resource ID based on the auto-increment primary key.
	m.IncidentID = rid.NewResourceID("incident").New(uint64(m.ID))

	// Update only the IncidentID column to avoid overhead and side effects of a full Save.
	// UpdateColumn is faster as it doesn't update timestamps or trigger Update hooks.
	return tx.Model(m).UpdateColumn("incident_id", m.IncidentID).Error
}

func init() {
	registry.Register(&IncidentM{})
}
