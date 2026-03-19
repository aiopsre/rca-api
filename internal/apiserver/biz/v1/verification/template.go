//nolint:gocognit,gocyclo,nestif,nilerr,nilprotogetter,modernize,whitespace
package verification

//go:generate mockgen -destination mock_verification.go -package verification github.com/aiopsre/rca-api/internal/apiserver/biz/v1/verification VerificationTemplateBiz

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/onexstack/onexstack/pkg/errorsx"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	"github.com/aiopsre/rca-api/internal/pkg/contextx"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
	v1 "github.com/aiopsre/rca-api/pkg/api/apiserver/v1"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

const (
	defaultListLimit = int64(20)
	maxListLimit     = int64(200)
	minVersion       = 1
)

// VerificationTemplateBiz defines verification template management use-cases.
type VerificationTemplateBiz interface {
	Create(ctx context.Context, rq *v1.CreateVerificationTemplateRequest) (*v1.CreateVerificationTemplateResponse, error)
	Get(ctx context.Context, rq *v1.GetVerificationTemplateRequest) (*v1.GetVerificationTemplateResponse, error)
	List(ctx context.Context, rq *v1.ListVerificationTemplatesRequest) (*v1.ListVerificationTemplatesResponse, error)
	Update(ctx context.Context, rq *v1.UpdateVerificationTemplateRequest) (*v1.UpdateVerificationTemplateResponse, error)
	Delete(ctx context.Context, rq *v1.DeleteVerificationTemplateRequest) (*v1.DeleteVerificationTemplateResponse, error)
	Activate(ctx context.Context, rq *v1.ActivateVerificationTemplateRequest) (*v1.ActivateVerificationTemplateResponse, error)
	Deactivate(ctx context.Context, rq *v1.DeactivateVerificationTemplateRequest) (*v1.DeactivateVerificationTemplateResponse, error)
	GetActive(ctx context.Context, rq *v1.GetActiveVerificationTemplateRequest) (*v1.GetActiveVerificationTemplateResponse, error)
	MatchTemplate(ctx context.Context, rootCauseType string, patterns []string, confidence float64) (*VerificationTemplateConfig, error)
	GetActiveForRuntime(ctx context.Context) ([]*VerificationTemplateConfig, error)

	VerificationTemplateExpansion
}

type VerificationTemplateExpansion interface{}

type verificationTemplateBiz struct {
	store store.IStore
}

var _ VerificationTemplateBiz = (*verificationTemplateBiz)(nil)

func NewVerificationTemplateBiz(store store.IStore) *verificationTemplateBiz {
	return &verificationTemplateBiz{store: store}
}

// VerificationTemplateConfig represents the runtime config structure.
type VerificationTemplateConfig struct {
	ID        int64                 `json:"id"`
	Name      string                `json:"name"`
	Match     VerificationMatch    `json:"match"`
	Steps     VerificationSteps    `json:"steps"`
	Active    bool                  `json:"active"`
	Version   int                   `json:"version"`
	CreatedAt time.Time             `json:"created_at"`
}

// VerificationMatch represents match conditions for template selection.
type VerificationMatch struct {
	RootCauseTypes  []string `json:"root_cause_types,omitempty"`
	PatternsContain []string `json:"patterns_contain,omitempty"`
	ConfidenceMin   float64  `json:"confidence_min,omitempty"`
}

// VerificationSteps represents the verification steps structure.
type VerificationSteps struct {
	Version  string                   `json:"version"`
	Steps    []VerificationStepConfig `json:"steps"`
	Warnings []string                 `json:"warnings,omitempty"`
}

// VerificationStepConfig represents a single verification step.
type VerificationStepConfig struct {
	ID       string                 `json:"id"`
	Tool     string                 `json:"tool"`
	Why      string                 `json:"why"`
	Params   map[string]any         `json:"params"`
	Expected VerificationExpected   `json:"expected"`
}

// VerificationExpected represents expected outcome for verification step.
type VerificationExpected struct {
	Type    string  `json:"type"`
	Field   string  `json:"field,omitempty"`
	Value   float64 `json:"value,omitempty"`
	Keyword string  `json:"keyword,omitempty"`
}

func (b *verificationTemplateBiz) Create(ctx context.Context, rq *v1.CreateVerificationTemplateRequest) (*v1.CreateVerificationTemplateResponse, error) {
	if rq == nil {
		return nil, errno.ErrInvalidArgument
	}

	name := strings.TrimSpace(rq.GetName())
	if name == "" {
		return nil, errno.ErrInvalidArgument
	}

	matchJSON := strings.TrimSpace(rq.GetMatchJSON())
	if matchJSON == "" {
		return nil, errno.ErrVerificationTemplateInvalidMatch
	}

	stepsJSON := strings.TrimSpace(rq.GetStepsJSON())
	if stepsJSON == "" {
		return nil, errno.ErrVerificationTemplateInvalidSteps
	}

	// Validate match JSON is valid JSON structure
	if _, err := validateMatchJSON(matchJSON); err != nil {
		return nil, errno.ErrVerificationTemplateInvalidMatch
	}

	// Validate steps JSON is valid JSON structure
	if _, err := validateStepsJSON(stepsJSON); err != nil {
		return nil, errno.ErrVerificationTemplateInvalidSteps
	}

	createdBy := normalizeOperator(ctx, "")

	exists, err := b.store.VerificationTemplate().Get(ctx, where.T(ctx).F("name", name))
	if err != nil && !errorsx.Is(err, gorm.ErrRecordNotFound) {
		return nil, errno.ErrVerificationTemplateGetFailed
	}
	if exists != nil {
		return nil, errno.ErrVerificationTemplateAlreadyExists
	}

	obj := &model.VerificationTemplateM{
		Name:        name,
		Description: trimStringPtr(rq.Description),
		LineageID:   newLineageID(),
		Version:     minVersion,
		MatchJSON:   matchJSON,
		StepsJSON:   stepsJSON,
		Active:      false,
		CreatedBy:   createdBy,
		UpdatedBy:   &createdBy,
	}

	if err := b.store.VerificationTemplate().Create(ctx, obj); err != nil {
		return nil, errno.ErrVerificationTemplateCreateFailed
	}

	return &v1.CreateVerificationTemplateResponse{
		VerificationTemplate: modelToProto(obj),
	}, nil
}

func (b *verificationTemplateBiz) Get(ctx context.Context, rq *v1.GetVerificationTemplateRequest) (*v1.GetVerificationTemplateResponse, error) {
	if rq == nil || rq.GetId() <= 0 {
		return nil, errno.ErrInvalidArgument
	}

	obj, err := b.store.VerificationTemplate().Get(ctx, where.T(ctx).F("id", rq.GetId()))
	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrVerificationTemplateNotFound
		}
		return nil, errno.ErrVerificationTemplateGetFailed
	}

	return &v1.GetVerificationTemplateResponse{
		VerificationTemplate: modelToProto(obj),
	}, nil
}

func (b *verificationTemplateBiz) List(ctx context.Context, rq *v1.ListVerificationTemplatesRequest) (*v1.ListVerificationTemplatesResponse, error) {
	if rq == nil {
		rq = &v1.ListVerificationTemplatesRequest{}
	}

	limit := normalizeListLimit(rq.GetLimit())
	whr := where.T(ctx).O(int(rq.GetOffset())).L(int(limit))

	if rq.Name != nil {
		name := strings.TrimSpace(*rq.Name)
		if name != "" {
			whr = whr.F("name", name)
		}
	}

	if rq.Active != nil {
		whr = whr.F("active", *rq.Active)
	}

	total, list, err := b.store.VerificationTemplate().List(ctx, whr)
	if err != nil {
		return nil, errno.ErrVerificationTemplateListFailed
	}

	protoList := make([]*v1.VerificationTemplate, 0, len(list))
	for _, m := range list {
		protoList = append(protoList, modelToProto(m))
	}

	return &v1.ListVerificationTemplatesResponse{
		TotalCount:           total,
		VerificationTemplates: protoList,
	}, nil
}

func (b *verificationTemplateBiz) Update(ctx context.Context, rq *v1.UpdateVerificationTemplateRequest) (*v1.UpdateVerificationTemplateResponse, error) {
	if rq == nil || rq.GetId() <= 0 {
		return nil, errno.ErrInvalidArgument
	}

	updatedBy := normalizeOperator(ctx, "")
	var updated *model.VerificationTemplateM

	err := b.store.TX(ctx, func(txCtx context.Context) error {
		obj, err := b.store.VerificationTemplate().Get(txCtx, where.T(txCtx).F("id", rq.GetId()))
		if err != nil {
			if errorsx.Is(err, gorm.ErrRecordNotFound) {
				return errno.ErrVerificationTemplateNotFound
			}
			return errno.ErrVerificationTemplateGetFailed
		}

		if rq.ExpectedVersion != nil && *rq.ExpectedVersion > 0 && obj.Version != int(*rq.ExpectedVersion) {
			return errno.ErrVerificationTemplateVersionMismatch
		}

		if err := b.ensureLineageID(txCtx, obj); err != nil {
			return err
		}

		if rq.Name != nil {
			name := strings.TrimSpace(*rq.Name)
			if name == "" {
				return errno.ErrInvalidArgument
			}
			if name != obj.Name {
				available, err := b.isNameAvailable(txCtx, name, obj.LineageID)
				if err != nil {
					return err
				}
				if !available {
					return errno.ErrVerificationTemplateAlreadyExists
				}
			}
			obj.Name = name
		}

		if rq.Description != nil {
			obj.Description = trimStringPtr(rq.Description)
		}

		if rq.MatchJSON != nil {
			matchJSON := strings.TrimSpace(*rq.MatchJSON)
			if matchJSON == "" {
				return errno.ErrVerificationTemplateInvalidMatch
			}
			if _, err := validateMatchJSON(matchJSON); err != nil {
				return errno.ErrVerificationTemplateInvalidMatch
			}
			obj.MatchJSON = matchJSON
		}

		if rq.StepsJSON != nil {
			stepsJSON := strings.TrimSpace(*rq.StepsJSON)
			if stepsJSON == "" {
				return errno.ErrVerificationTemplateInvalidSteps
			}
			if _, err := validateStepsJSON(stepsJSON); err != nil {
				return errno.ErrVerificationTemplateInvalidSteps
			}
			obj.StepsJSON = stepsJSON
		}

		obj.Version = obj.Version + 1
		obj.UpdatedBy = &updatedBy

		if err := b.store.VerificationTemplate().Update(txCtx, obj); err != nil {
			return errno.ErrVerificationTemplateUpdateFailed
		}

		updated = obj
		return nil
	})
	if err != nil {
		return nil, err
	}

	return &v1.UpdateVerificationTemplateResponse{VerificationTemplate: modelToProto(updated)}, nil
}

func (b *verificationTemplateBiz) Delete(ctx context.Context, rq *v1.DeleteVerificationTemplateRequest) (*v1.DeleteVerificationTemplateResponse, error) {
	if rq == nil || rq.GetId() <= 0 {
		return nil, errno.ErrInvalidArgument
	}

	_, err := b.store.VerificationTemplate().Get(ctx, where.T(ctx).F("id", rq.GetId()))
	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrVerificationTemplateNotFound
		}
		return nil, errno.ErrVerificationTemplateGetFailed
	}

	if err := b.store.VerificationTemplate().Delete(ctx, where.T(ctx).F("id", rq.GetId())); err != nil {
		return nil, errno.ErrVerificationTemplateDeleteFailed
	}

	return &v1.DeleteVerificationTemplateResponse{}, nil
}

func (b *verificationTemplateBiz) Activate(ctx context.Context, rq *v1.ActivateVerificationTemplateRequest) (*v1.ActivateVerificationTemplateResponse, error) {
	if rq == nil || rq.GetId() <= 0 {
		return nil, errno.ErrInvalidArgument
	}

	obj, err := b.store.VerificationTemplate().Get(ctx, where.T(ctx).F("id", rq.GetId()))
	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrVerificationTemplateNotFound
		}
		return nil, errno.ErrVerificationTemplateGetFailed
	}

	if obj.Active {
		return &v1.ActivateVerificationTemplateResponse{}, nil
	}

	op := normalizeOperator(ctx, rq.GetOperator())

	if err := b.store.VerificationTemplate().Activate(ctx, rq.GetId(), op); err != nil {
		return nil, errno.ErrVerificationTemplateActivateFailed
	}

	return &v1.ActivateVerificationTemplateResponse{}, nil
}

func (b *verificationTemplateBiz) Deactivate(ctx context.Context, rq *v1.DeactivateVerificationTemplateRequest) (*v1.DeactivateVerificationTemplateResponse, error) {
	if rq == nil || rq.GetId() <= 0 {
		return nil, errno.ErrInvalidArgument
	}

	obj, err := b.store.VerificationTemplate().Get(ctx, where.T(ctx).F("id", rq.GetId()))
	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrVerificationTemplateNotFound
		}
		return nil, errno.ErrVerificationTemplateGetFailed
	}

	if !obj.Active {
		return &v1.DeactivateVerificationTemplateResponse{}, nil
	}

	if err := b.store.VerificationTemplate().Deactivate(ctx, rq.GetId()); err != nil {
		return nil, errno.ErrVerificationTemplateDeactivateFailed
	}

	return &v1.DeactivateVerificationTemplateResponse{}, nil
}

func (b *verificationTemplateBiz) GetActive(ctx context.Context, rq *v1.GetActiveVerificationTemplateRequest) (*v1.GetActiveVerificationTemplateResponse, error) {
	list, err := b.store.VerificationTemplate().GetActive(ctx)
	if err != nil {
		return nil, errno.ErrVerificationTemplateGetFailed
	}

	if len(list) == 0 {
		return nil, errno.ErrVerificationTemplateNotFound
	}

	return &v1.GetActiveVerificationTemplateResponse{
		VerificationTemplate: modelToProto(list[0]),
	}, nil
}

// MatchTemplate finds the best matching verification template for the given diagnosis attributes.
// Matching priority:
// 1. Exact root_cause_type match
// 2. Pattern containment match
// 3. Confidence threshold match
// Returns the first matching template or nil if no match found.
func (b *verificationTemplateBiz) MatchTemplate(ctx context.Context, rootCauseType string, patterns []string, confidence float64) (*VerificationTemplateConfig, error) {
	list, err := b.store.VerificationTemplate().GetActive(ctx)
	if err != nil {
		return nil, fmt.Errorf("get active verification templates: %w", err)
	}

	rootCauseTypeLower := strings.ToLower(strings.TrimSpace(rootCauseType))
	patternsLower := make([]string, 0, len(patterns))
	for _, p := range patterns {
		trimmed := strings.ToLower(strings.TrimSpace(p))
		if trimmed != "" {
			patternsLower = append(patternsLower, trimmed)
		}
	}

	for _, obj := range list {
		if !obj.Active {
			continue
		}

		config, err := parseVerificationTemplateConfig(obj)
		if err != nil {
			continue
		}

		// Check confidence threshold
		if config.Match.ConfidenceMin > 0 && confidence < config.Match.ConfidenceMin {
			continue
		}

		// Check root_cause_type match
		if rootCauseTypeLower != "" && len(config.Match.RootCauseTypes) > 0 {
			for _, rct := range config.Match.RootCauseTypes {
				if strings.ToLower(strings.TrimSpace(rct)) == rootCauseTypeLower {
					return config, nil
				}
			}
		}

		// Check patterns containment
		if len(patternsLower) > 0 && len(config.Match.PatternsContain) > 0 {
			for _, templatePattern := range config.Match.PatternsContain {
				templatePatternLower := strings.ToLower(strings.TrimSpace(templatePattern))
				for _, inputPattern := range patternsLower {
					if templatePatternLower == inputPattern {
						return config, nil
					}
				}
			}
		}
	}

	return nil, nil
}

// GetActiveForRuntime loads all active verification templates for runtime consumption.
// Returns a list of parsed VerificationTemplateConfig objects.
func (b *verificationTemplateBiz) GetActiveForRuntime(ctx context.Context) ([]*VerificationTemplateConfig, error) {
	list, err := b.store.VerificationTemplate().GetActive(ctx)
	if err != nil {
		return nil, fmt.Errorf("get active verification templates: %w", err)
	}

	configs := make([]*VerificationTemplateConfig, 0, len(list))
	for _, obj := range list {
		if !obj.Active {
			continue
		}
		config, err := parseVerificationTemplateConfig(obj)
		if err != nil {
			continue
		}
		configs = append(configs, config)
	}

	return configs, nil
}

func validateMatchJSON(matchJSON string) (*VerificationMatch, error) {
	var match VerificationMatch
	if err := json.Unmarshal([]byte(matchJSON), &match); err != nil {
		return nil, err
	}
	return &match, nil
}

func validateStepsJSON(stepsJSON string) (*VerificationSteps, error) {
	var steps VerificationSteps
	if err := json.Unmarshal([]byte(stepsJSON), &steps); err != nil {
		return nil, err
	}
	if steps.Version == "" {
		return nil, fmt.Errorf("steps version is required")
	}
	if len(steps.Steps) == 0 {
		return nil, fmt.Errorf("at least one step is required")
	}
	return &steps, nil
}

func parseVerificationTemplateConfig(obj *model.VerificationTemplateM) (*VerificationTemplateConfig, error) {
	if obj == nil {
		return nil, fmt.Errorf("nil template")
	}

	match, err := validateMatchJSON(obj.MatchJSON)
	if err != nil {
		return nil, fmt.Errorf("parse match JSON: %w", err)
	}

	steps, err := validateStepsJSON(obj.StepsJSON)
	if err != nil {
		return nil, fmt.Errorf("parse steps JSON: %w", err)
	}

	return &VerificationTemplateConfig{
		ID:        obj.ID,
		Name:      obj.Name,
		Match:     *match,
		Steps:     *steps,
		Active:    obj.Active,
		Version:   obj.Version,
		CreatedAt: obj.CreatedAt,
	}, nil
}

func normalizeOperator(ctx context.Context, fallback string) string {
	if fallback != "" {
		return strings.TrimSpace(fallback)
	}
	if user := contextx.Username(ctx); user != "" {
		return user
	}
	return "system"
}

func normalizeListLimit(limit int64) int64 {
	if limit <= 0 {
		return defaultListLimit
	}
	if limit > maxListLimit {
		return maxListLimit
	}
	return limit
}

func trimStringPtr(in *string) *string {
	if in == nil {
		return nil
	}
	value := strings.TrimSpace(*in)
	if value == "" {
		return nil
	}
	return &value
}

func (b *verificationTemplateBiz) ensureLineageID(ctx context.Context, obj *model.VerificationTemplateM) error {
	if obj == nil {
		return errno.ErrInvalidArgument
	}
	if strings.TrimSpace(obj.LineageID) != "" {
		return nil
	}

	lineageID := newLineageID()
	db := b.store.DB(ctx)
	if err := db.Model(&model.VerificationTemplateM{}).
		Where("name = ? AND (lineage_id = '' OR lineage_id IS NULL)", obj.Name).
		Update("lineage_id", lineageID).Error; err != nil {
		return errno.ErrVerificationTemplateUpdateFailed
	}

	if err := db.Model(&model.VerificationTemplateM{}).
		Where("id = ? AND (lineage_id = '' OR lineage_id IS NULL)", obj.ID).
		Update("lineage_id", lineageID).Error; err != nil {
		return errno.ErrVerificationTemplateUpdateFailed
	}

	obj.LineageID = lineageID
	return nil
}

func (b *verificationTemplateBiz) isNameAvailable(ctx context.Context, name string, currentLineageID string) (bool, error) {
	page := 0
	for {
		_, list, err := b.store.VerificationTemplate().List(ctx, where.T(ctx).F("name", name).O(page).L(100))
		if err != nil {
			return false, errno.ErrVerificationTemplateGetFailed
		}
		if len(list) == 0 {
			return true, nil
		}
		for _, item := range list {
			if currentLineageID != "" && item.LineageID == currentLineageID {
				continue
			}
			return false, nil
		}
		if len(list) < 100 {
			return true, nil
		}
		page += 100
	}
}

func newLineageID() string {
	return uuid.NewString()
}

func modelToProto(m *model.VerificationTemplateM) *v1.VerificationTemplate {
	if m == nil {
		return nil
	}
	pb := &v1.VerificationTemplate{
		Id:         m.ID,
		Name:       m.Name,
		LineageID:  m.LineageID,
		Version:    int32(m.Version),
		MatchJSON:  m.MatchJSON,
		StepsJSON:  m.StepsJSON,
		Active:     m.Active,
		CreatedBy:  m.CreatedBy,
		CreatedAt:  timestamppb.New(m.CreatedAt),
		UpdatedAt:  timestamppb.New(m.UpdatedAt),
	}
	if m.Description != nil {
		pb.Description = m.Description
	}
	if m.ActivatedAt != nil {
		pb.ActivatedAt = timestamppb.New(*m.ActivatedAt)
	}
	if m.ActivatedBy != nil {
		pb.ActivatedBy = m.ActivatedBy
	}
	if m.PreviousVersion != nil {
		pb.PreviousVersion = int32Ptr(int32(*m.PreviousVersion))
	}
	if m.UpdatedBy != nil {
		pb.UpdatedBy = m.UpdatedBy
	}
	return pb
}

func int32Ptr(v int32) *int32 {
	return &v
}