package model

import (
	"github.com/onexstack/onexstack/pkg/store/registry"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/pkg/rid"
)

// AfterCreate generates evidence business ID.
func (m *EvidenceM) AfterCreate(tx *gorm.DB) error {
	m.EvidenceID = rid.EvidenceID.New(uint64(m.ID))
	return tx.Model(m).UpdateColumn("evidence_id", m.EvidenceID).Error
}

func init() {
	registry.Register(&EvidenceM{})
}
