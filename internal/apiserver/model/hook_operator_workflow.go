//nolint:dupl
package model

import (
	"github.com/onexstack/onexstack/pkg/store/registry"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/pkg/rid"
)

// AfterCreate generates operator action log business ID.
func (m *IncidentActionLogM) AfterCreate(tx *gorm.DB) error {
	m.ActionID = rid.OperatorActionID.New(uint64(m.ID))
	return tx.Model(m).UpdateColumn("action_id", m.ActionID).Error
}

//nolint:gochecknoinits // Model registry hooks are intentionally init-based in this codebase.
func init() {
	registry.Register(&IncidentActionLogM{})
}
