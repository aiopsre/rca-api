package model

import (
	"github.com/onexstack/onexstack/pkg/store/registry"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/pkg/rid"
)

// AfterCreate generates kb entry business ID.
func (m *KBEntryM) AfterCreate(tx *gorm.DB) error {
	m.KBID = rid.KBEntryID.New(uint64(m.ID))
	return tx.Model(m).UpdateColumn("kb_id", m.KBID).Error
}

//nolint:gochecknoinits // Model registry hooks are intentionally init-based in this codebase.
func init() {
	registry.Register(&KBEntryM{})
}
