package model

import (
	"github.com/onexstack/onexstack/pkg/store/registry"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/pkg/rid"
)

// AfterCreate generates session history event business ID.
func (m *SessionHistoryEventM) AfterCreate(tx *gorm.DB) error {
	m.EventID = rid.SessionHistoryEventID.New(uint64(m.ID))
	return tx.Model(m).UpdateColumn("event_id", m.EventID).Error
}

//nolint:gochecknoinits // Model registry hooks are intentionally init-based in this codebase.
func init() {
	registry.Register(&SessionHistoryEventM{})
}
