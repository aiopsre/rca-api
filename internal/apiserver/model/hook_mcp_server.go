package model

import (
	"github.com/onexstack/onexstack/pkg/store/registry"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/pkg/rid"
)

// AfterCreate generates mcp server business ID.
func (m *McpServerM) AfterCreate(tx *gorm.DB) error {
	m.McpServerID = rid.McpServerID.New(uint64(m.ID))
	return tx.Model(m).UpdateColumn("mcp_server_id", m.McpServerID).Error
}

//nolint:gochecknoinits // Model registry hooks are intentionally init-based in this codebase.
func init() {
	registry.Register(&McpServerM{})
	registry.Register(&McpServerConfigM{})
}