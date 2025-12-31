package model

import (
	"github.com/onexstack/onexstack/pkg/store/registry"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/pkg/rid"
)

// AfterCreate generates datasource business ID.
func (m *DatasourceM) AfterCreate(tx *gorm.DB) error {
	m.DatasourceID = rid.DatasourceID.New(uint64(m.ID))
	return tx.Model(m).UpdateColumn("datasource_id", m.DatasourceID).Error
}

// AfterCreate generates evidence business ID.
func (m *EvidenceM) AfterCreate(tx *gorm.DB) error {
	m.EvidenceID = rid.EvidenceID.New(uint64(m.ID))
	return tx.Model(m).UpdateColumn("evidence_id", m.EvidenceID).Error
}

func init() {
	registry.Register(&DatasourceM{})
	registry.Register(&EvidenceM{})
}
