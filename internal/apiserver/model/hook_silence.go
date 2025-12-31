package model

import (
	"github.com/onexstack/onexstack/pkg/store/registry"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/pkg/rid"
)

// AfterCreate generates silence business ID.
func (m *SilenceM) AfterCreate(tx *gorm.DB) error {
	m.SilenceID = rid.SilenceID.New(uint64(m.ID))
	return tx.Model(m).UpdateColumn("silence_id", m.SilenceID).Error
}

//nolint:gochecknoinits // Model registry hooks are intentionally init-based in this codebase.
func init() {
	registry.Register(&SilenceM{})
}
