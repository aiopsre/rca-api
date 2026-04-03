package model

import "github.com/onexstack/onexstack/pkg/store/registry"

//nolint:gochecknoinits // Model registry hooks are intentionally init-based in this codebase.
func init() {
	registry.Register(&PipelineConfigM{})
	registry.Register(&TriggerConfigM{})
	registry.Register(&ToolsetConfigDynamicM{})
	registry.Register(&SkillReleaseM{})
	registry.Register(&SkillsetConfigDynamicM{})
	registry.Register(&SLAEscalationConfigM{})
	registry.Register(&SessionAssignmentM{})
}
