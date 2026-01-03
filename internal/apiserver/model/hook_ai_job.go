package model

import (
	"github.com/onexstack/onexstack/pkg/store/registry"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/pkg/rid"
)

// AfterCreate generates ai job business ID.
func (m *AIJobM) AfterCreate(tx *gorm.DB) error {
	m.JobID = rid.AIJobID.New(uint64(m.ID))
	return tx.Model(m).UpdateColumn("job_id", m.JobID).Error
}

// AfterCreate generates ai tool call business ID.
func (m *AIToolCallM) AfterCreate(tx *gorm.DB) error {
	m.ToolCallID = rid.AIToolCallID.New(uint64(m.ID))
	return tx.Model(m).UpdateColumn("tool_call_id", m.ToolCallID).Error
}

//nolint:gochecknoinits // Model registry hooks are intentionally init-based in this codebase.
func init() {
	registry.Register(&AIJobM{})
	registry.Register(&AIToolCallM{})
	registry.Register(&AIJobQueueSignalM{})
}
