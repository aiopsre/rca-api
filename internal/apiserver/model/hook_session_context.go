package model

import (
	"github.com/onexstack/onexstack/pkg/store/registry"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/pkg/rid"
)

// AfterCreate generates session context business ID.
func (m *SessionContextM) AfterCreate(tx *gorm.DB) error {
	m.SessionID = rid.SessionID.New(uint64(m.ID))
	return tx.Model(m).UpdateColumn("session_id", m.SessionID).Error
}

//nolint:gochecknoinits // Model registry hooks are intentionally init-based in this codebase.
func init() {
	registry.Register(&SessionContextM{})
}
