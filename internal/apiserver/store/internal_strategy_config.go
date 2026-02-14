package store

import (
	"context"
	"strings"

	"gorm.io/gorm/clause"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
)

type InternalStrategyConfigStore interface {
	GetPipelineConfig(ctx context.Context, alertSource string, service string, namespace string) (*model.PipelineConfigM, error)
	UpsertPipelineConfig(ctx context.Context, obj *model.PipelineConfigM) error

	GetTriggerConfig(ctx context.Context, triggerType string) (*model.TriggerConfigM, error)
	UpsertTriggerConfig(ctx context.Context, obj *model.TriggerConfigM) error

	ListToolsetConfigsByPipeline(ctx context.Context, pipelineID string) ([]*model.ToolsetConfigDynamicM, error)
	UpsertToolsetConfig(ctx context.Context, obj *model.ToolsetConfigDynamicM) error

	GetSLAEscalationConfig(ctx context.Context, sessionType string) (*model.SLAEscalationConfigM, error)
	UpsertSLAEscalationConfig(ctx context.Context, obj *model.SLAEscalationConfigM) error

	GetSessionAssignment(ctx context.Context, sessionID string) (*model.SessionAssignmentM, error)
	UpsertSessionAssignment(ctx context.Context, obj *model.SessionAssignmentM) error
}

type internalStrategyConfigStore struct {
	s *store
}

func newInternalStrategyConfigStore(s *store) *internalStrategyConfigStore {
	return &internalStrategyConfigStore{s: s}
}

func (isc *internalStrategyConfigStore) GetPipelineConfig(
	ctx context.Context,
	alertSource string,
	service string,
	namespace string,
) (*model.PipelineConfigM, error) {
	query := isc.s.DB(ctx).Model(&model.PipelineConfigM{})
	query = query.Where("alert_source = ?", strings.TrimSpace(alertSource))
	query = query.Where("service = ?", strings.TrimSpace(service))
	query = query.Where("namespace = ?", strings.TrimSpace(namespace))
	out := &model.PipelineConfigM{}
	if err := query.Order("id DESC").First(out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

func (isc *internalStrategyConfigStore) UpsertPipelineConfig(ctx context.Context, obj *model.PipelineConfigM) error {
	if obj == nil {
		return nil
	}
	return isc.s.DB(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "alert_source"}, {Name: "service"}, {Name: "namespace"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"pipeline_id",
				"graph_id",
				"updated_at",
			}),
		}).
		Create(obj).Error
}

func (isc *internalStrategyConfigStore) GetTriggerConfig(ctx context.Context, triggerType string) (*model.TriggerConfigM, error) {
	out := &model.TriggerConfigM{}
	if err := isc.s.DB(ctx).Model(&model.TriggerConfigM{}).
		Where("trigger_type = ?", strings.TrimSpace(triggerType)).
		First(out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

func (isc *internalStrategyConfigStore) UpsertTriggerConfig(ctx context.Context, obj *model.TriggerConfigM) error {
	if obj == nil {
		return nil
	}
	return isc.s.DB(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "trigger_type"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"pipeline_id",
				"session_type",
				"fallback",
				"updated_at",
			}),
		}).
		Create(obj).Error
}

func (isc *internalStrategyConfigStore) ListToolsetConfigsByPipeline(
	ctx context.Context,
	pipelineID string,
) ([]*model.ToolsetConfigDynamicM, error) {
	var out []*model.ToolsetConfigDynamicM
	err := isc.s.DB(ctx).Model(&model.ToolsetConfigDynamicM{}).
		Where("pipeline_id = ?", strings.TrimSpace(pipelineID)).
		Order("id ASC").
		Find(&out).Error
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return []*model.ToolsetConfigDynamicM{}, nil
	}
	return out, nil
}

func (isc *internalStrategyConfigStore) UpsertToolsetConfig(ctx context.Context, obj *model.ToolsetConfigDynamicM) error {
	if obj == nil {
		return nil
	}
	return isc.s.DB(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "pipeline_id"}, {Name: "toolset_name"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"allowed_tools_json",
				"updated_at",
			}),
		}).
		Create(obj).Error
}

func (isc *internalStrategyConfigStore) GetSLAEscalationConfig(
	ctx context.Context,
	sessionType string,
) (*model.SLAEscalationConfigM, error) {
	out := &model.SLAEscalationConfigM{}
	if err := isc.s.DB(ctx).Model(&model.SLAEscalationConfigM{}).
		Where("session_type = ?", strings.TrimSpace(sessionType)).
		First(out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

func (isc *internalStrategyConfigStore) UpsertSLAEscalationConfig(ctx context.Context, obj *model.SLAEscalationConfigM) error {
	if obj == nil {
		return nil
	}
	return isc.s.DB(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "session_type"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"due_seconds",
				"escalation_thresholds_json",
				"updated_at",
			}),
		}).
		Create(obj).Error
}

func (isc *internalStrategyConfigStore) GetSessionAssignment(ctx context.Context, sessionID string) (*model.SessionAssignmentM, error) {
	out := &model.SessionAssignmentM{}
	if err := isc.s.DB(ctx).Model(&model.SessionAssignmentM{}).
		Where("session_id = ?", strings.TrimSpace(sessionID)).
		First(out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

func (isc *internalStrategyConfigStore) UpsertSessionAssignment(ctx context.Context, obj *model.SessionAssignmentM) error {
	if obj == nil {
		return nil
	}
	return isc.s.DB(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "session_id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"assignee",
				"assigned_by",
				"assigned_at",
				"note",
				"updated_at",
			}),
		}).
		Create(obj).Error
}
