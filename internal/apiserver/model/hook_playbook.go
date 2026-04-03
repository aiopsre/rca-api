package model

import (
	"github.com/onexstack/onexstack/pkg/store/registry"
)

//nolint:gochecknoinits // Model registry hooks are intentionally init-based in this codebase.
func init() {
	registry.Register(&PlaybookM{})
}
