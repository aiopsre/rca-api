//nolint:gocognit,gocyclo,nestif,nilerr,nilprotogetter,modernize,whitespace
package playbook

//go:generate mockgen -destination mock_playbook.go -package playbook github.com/aiopsre/rca-api/internal/apiserver/biz/v1/playbook PlaybookBiz

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/onexstack/onexstack/pkg/errorsx"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	"github.com/aiopsre/rca-api/internal/pkg/contextx"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
	"github.com/aiopsre/rca-api/pkg/store/where"
)

const (
	defaultListLimit = int64(20)
	maxListLimit     = int64(200)
	minVersion       = 1
)

type PlaybookBiz interface {
	Create(ctx context.Context, req *CreateRequest) (*CreateResponse, error)
	Get(ctx context.Context, id int64) (*GetResponse, error)
	List(ctx context.Context, req *ListRequest) (*ListResponse, error)
	Update(ctx context.Context, id int64, req *UpdateRequest) (*UpdateResponse, error)
	Delete(ctx context.Context, id int64) error
	Activate(ctx context.Context, id int64, operator string) error
	Deactivate(ctx context.Context, id int64) error
	Rollback(ctx context.Context, id int64, version int, operator string) error
	GetActive(ctx context.Context) (*GetResponse, error)
	GetActiveForRuntime(ctx context.Context) (*PlaybookConfig, string, error)

	PlaybookExpansion
}

type PlaybookExpansion interface{}

type playbookBiz struct {
	store store.IStore
}

var _ PlaybookBiz = (*playbookBiz)(nil)

func New(store store.IStore) *playbookBiz {
	return &playbookBiz{store: store}
}

type CreateRequest struct {
	Name        string
	Description *string
	Config      *PlaybookConfig
	CreatedBy   string
}

type CreateResponse struct {
	Playbook *model.PlaybookM `json:"playbook"`
}

type GetResponse struct {
	Playbook *model.PlaybookM `json:"playbook"`
}

type ListRequest struct {
	Name   *string
	Active *bool
	Offset int64
	Limit  *int64
}

type ListResponse struct {
	TotalCount int64              `json:"total_count"`
	Playbooks  []*model.PlaybookM `json:"playbooks"`
}

type UpdateRequest struct {
	Name            *string
	Description     *string
	Config          *PlaybookConfig
	ExpectedVersion *int
	UpdatedBy       string
}

type UpdateResponse struct {
	Playbook *model.PlaybookM `json:"playbook"`
}

type PlaybookConfig struct {
	Version  string   `json:"version"`
	Rules    []Rule   `json:"rules,omitempty"`
	Fallback Fallback `json:"fallback,omitempty"`
}

type Rule struct {
	ID    string     `json:"id"`
	Match RuleMatch  `json:"match"`
	Items []RuleItem `json:"items"`
}

type RuleMatch struct {
	RootCauseTypes  []string `json:"root_cause_types,omitempty"`
	PatternsContain []string `json:"patterns_contains,omitempty"`
}

type RuleItem struct {
	ID           string           `json:"id"`
	Title        string           `json:"title"`
	Risk         string           `json:"risk"`
	Rationale    string           `json:"rationale"`
	Steps        []RuleStep       `json:"steps"`
	Verification RuleVerification `json:"verification"`
}

type RuleStep struct {
	Type          string `json:"type"`
	Text          string `json:"text"`
	RequiresHuman bool   `json:"requires_human,omitempty"`
}

type RuleVerification struct {
	RecommendedSteps []int  `json:"recommended_steps"`
	ExpectedOutcome  string `json:"expected_outcome"`
}

type Fallback struct {
	Items []RuleItem `json:"items"`
}

func (b *playbookBiz) Create(ctx context.Context, req *CreateRequest) (*CreateResponse, error) {
	if req == nil {
		return nil, errno.ErrInvalidArgument
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, errno.ErrInvalidArgument
	}

	configJSON, err := validatePlaybookConfig(req.Config)
	if err != nil {
		return nil, errno.ErrPlaybookInvalidConfig
	}

	createdBy := normalizeOperator(ctx, req.CreatedBy)

	exists, err := b.store.Playbook().Get(ctx, where.T(ctx).F("name", name))
	if err != nil && !errorsx.Is(err, gorm.ErrRecordNotFound) {
		return nil, errno.ErrPlaybookGetFailed
	}
	if exists != nil {
		return nil, errno.ErrPlaybookAlreadyExists
	}

	obj := &model.PlaybookM{
		Name:        name,
		Description: trimStringPtr(req.Description),
		LineageID:   newLineageID(),
		Version:     minVersion,
		ConfigJSON:  configJSON,
		Active:      false,
		CreatedBy:   createdBy,
		UpdatedBy:   &createdBy,
	}

	if err := b.store.Playbook().Create(ctx, obj); err != nil {
		return nil, errno.ErrPlaybookCreateFailed
	}

	return &CreateResponse{
		Playbook: obj,
	}, nil
}

func (b *playbookBiz) Get(ctx context.Context, id int64) (*GetResponse, error) {
	if id <= 0 {
		return nil, errno.ErrInvalidArgument
	}

	obj, err := b.store.Playbook().Get(ctx, where.T(ctx).F("id", id))
	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrPlaybookNotFound
		}
		return nil, errno.ErrPlaybookGetFailed
	}

	return &GetResponse{
		Playbook: obj,
	}, nil
}

func (b *playbookBiz) List(ctx context.Context, req *ListRequest) (*ListResponse, error) {
	if req == nil {
		req = &ListRequest{}
	}

	limit := normalizeListLimit(req.Limit)
	whr := where.T(ctx).O(int(req.Offset)).L(int(limit))

	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name != "" {
			whr = whr.F("name", name)
		}
	}

	if req.Active != nil {
		whr = whr.F("active", *req.Active)
	}

	total, list, err := b.store.Playbook().List(ctx, whr)
	if err != nil {
		return nil, errno.ErrPlaybookListFailed
	}

	return &ListResponse{
		TotalCount: total,
		Playbooks:  list,
	}, nil
}

func (b *playbookBiz) Update(ctx context.Context, id int64, req *UpdateRequest) (*UpdateResponse, error) {
	if req == nil || id <= 0 {
		return nil, errno.ErrInvalidArgument
	}

	updatedBy := normalizeOperator(ctx, req.UpdatedBy)
	var updated *model.PlaybookM

	err := b.store.TX(ctx, func(txCtx context.Context) error {
		obj, err := b.store.Playbook().Get(txCtx, where.T(txCtx).F("id", id))
		if err != nil {
			if errorsx.Is(err, gorm.ErrRecordNotFound) {
				return errno.ErrPlaybookNotFound
			}
			return errno.ErrPlaybookGetFailed
		}

		if req.ExpectedVersion != nil && *req.ExpectedVersion > 0 && obj.Version != *req.ExpectedVersion {
			return errno.ErrPlaybookVersionMismatch
		}

		if err := b.ensurePlaybookLineageID(txCtx, obj); err != nil {
			return err
		}

		oldName := obj.Name
		oldDescription := cloneStringPtr(obj.Description)
		oldConfigJSON := obj.ConfigJSON
		oldActive := obj.Active
		oldActivatedAt := cloneTimePtr(obj.ActivatedAt)
		oldActivatedBy := cloneStringPtr(obj.ActivatedBy)
		oldUpdatedBy := cloneStringPtr(obj.UpdatedBy)
		oldVersion := obj.Version
		oldPreviousVersion := cloneIntPtr(obj.PreviousVersion)
		oldLineageID := obj.LineageID

		if req.Name != nil {
			name := strings.TrimSpace(*req.Name)
			if name == "" {
				return errno.ErrInvalidArgument
			}
			if name != obj.Name {
				available, err := b.isPlaybookNameAvailable(txCtx, name, obj.LineageID)
				if err != nil {
					return err
				}
				if !available {
					return errno.ErrPlaybookAlreadyExists
				}
			}
			obj.Name = name
		}

		if req.Description != nil {
			obj.Description = trimStringPtr(req.Description)
		}

		if req.Config != nil {
			configJSON, err := validatePlaybookConfig(req.Config)
			if err != nil {
				return errno.ErrPlaybookInvalidConfig
			}
			obj.ConfigJSON = configJSON
		}

		snapshot := &model.PlaybookM{
			Name:            oldName,
			Description:     oldDescription,
			LineageID:       oldLineageID,
			Version:         oldVersion,
			PreviousVersion: oldPreviousVersion,
			ConfigJSON:      oldConfigJSON,
			Active:          oldActive,
			ActivatedAt:     oldActivatedAt,
			ActivatedBy:     oldActivatedBy,
			CreatedBy:       obj.CreatedBy,
			UpdatedBy:       oldUpdatedBy,
		}

		if err := b.store.Playbook().Create(txCtx, snapshot); err != nil {
			return errno.ErrPlaybookCreateFailed
		}

		obj.Version = oldVersion + 1
		obj.PreviousVersion = intPtr(oldVersion)
		obj.UpdatedBy = &updatedBy

		if err := b.store.Playbook().Update(txCtx, obj); err != nil {
			return errno.ErrPlaybookUpdateFailed
		}

		updated = obj
		return nil
	})
	if err != nil {
		return nil, err
	}

	return &UpdateResponse{Playbook: updated}, nil
}

func (b *playbookBiz) Delete(ctx context.Context, id int64) error {
	if id <= 0 {
		return errno.ErrInvalidArgument
	}

	_, err := b.store.Playbook().Get(ctx, where.T(ctx).F("id", id))
	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return errno.ErrPlaybookNotFound
		}
		return errno.ErrPlaybookGetFailed
	}

	if err := b.store.Playbook().Delete(ctx, where.T(ctx).F("id", id)); err != nil {
		return errno.ErrPlaybookDeleteFailed
	}

	return nil
}

func (b *playbookBiz) Activate(ctx context.Context, id int64, operator string) error {
	if id <= 0 {
		return errno.ErrInvalidArgument
	}

	obj, err := b.store.Playbook().Get(ctx, where.T(ctx).F("id", id))
	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return errno.ErrPlaybookNotFound
		}
		return errno.ErrPlaybookGetFailed
	}

	if obj.Active {
		return nil
	}

	op := normalizeOperator(ctx, operator)

	if err := b.store.Playbook().Activate(ctx, id, op); err != nil {
		return errno.ErrPlaybookActivateFailed
	}

	return nil
}

func (b *playbookBiz) Deactivate(ctx context.Context, id int64) error {
	if id <= 0 {
		return errno.ErrInvalidArgument
	}

	obj, err := b.store.Playbook().Get(ctx, where.T(ctx).F("id", id))
	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return errno.ErrPlaybookNotFound
		}
		return errno.ErrPlaybookGetFailed
	}

	if !obj.Active {
		return nil
	}

	if err := b.store.Playbook().Deactivate(ctx, id); err != nil {
		return errno.ErrPlaybookDeactivateFailed
	}

	return nil
}

func (b *playbookBiz) Rollback(ctx context.Context, id int64, version int, operator string) error {
	if id <= 0 || version < minVersion {
		return errno.ErrInvalidArgument
	}

	op := normalizeOperator(ctx, operator)
	return b.store.TX(ctx, func(txCtx context.Context) error {
		currentObj, err := b.store.Playbook().Get(txCtx, where.T(txCtx).F("id", id))
		if err != nil {
			if errorsx.Is(err, gorm.ErrRecordNotFound) {
				return errno.ErrPlaybookNotFound
			}
			return errno.ErrPlaybookGetFailed
		}

		if version >= currentObj.Version {
			return errno.ErrInvalidArgument
		}

		if err := b.ensurePlaybookLineageID(txCtx, currentObj); err != nil {
			return err
		}

		historyObj, err := b.getPlaybookVersion(txCtx, currentObj.LineageID, version)
		if err != nil {
			if errorsx.Is(err, gorm.ErrRecordNotFound) {
				return errno.ErrPlaybookNotFound
			}
			return errno.ErrPlaybookGetFailed
		}

		rollbackObj := &model.PlaybookM{
			Name:            historyObj.Name,
			Description:     cloneStringPtr(historyObj.Description),
			LineageID:       currentObj.LineageID,
			Version:         currentObj.Version + 1,
			PreviousVersion: intPtr(currentObj.Version),
			ConfigJSON:      historyObj.ConfigJSON,
			Active:          false,
			ActivatedAt:     nil,
			ActivatedBy:     nil,
			CreatedBy:       currentObj.CreatedBy,
			UpdatedBy:       &op,
		}

		if currentObj.Active {
			if err := b.store.Playbook().Deactivate(txCtx, id); err != nil {
				return errno.ErrPlaybookDeactivateFailed
			}
			now := time.Now()
			rollbackObj.Active = true
			rollbackObj.ActivatedAt = &now
			rollbackObj.ActivatedBy = &op
		}

		if err := b.store.Playbook().Create(txCtx, rollbackObj); err != nil {
			return errno.ErrPlaybookCreateFailed
		}

		return nil
	})
}

func (b *playbookBiz) GetActive(ctx context.Context) (*GetResponse, error) {
	obj, err := b.store.Playbook().GetActive(ctx)
	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrPlaybookNotFound
		}
		return nil, errno.ErrPlaybookGetFailed
	}

	return &GetResponse{
		Playbook: obj,
	}, nil
}

// GetActiveForRuntime loads active playbook for runtime with database-first precedence.
// Priority:
// 1. Active Playbook from database
// 2. Returns nil with not found error if no active playbook exists
//
// Returns the parsed PlaybookConfig and source ("dynamic_db" if from database).
// This method is designed for runtime consumption where database config takes precedence.
func (b *playbookBiz) GetActiveForRuntime(ctx context.Context) (*PlaybookConfig, string, error) {
	obj, err := b.store.Playbook().GetActive(ctx)
	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", errno.ErrPlaybookNotFound
		}
		return nil, "", errno.ErrPlaybookGetFailed
	}

	if !obj.Active {
		return nil, "", errno.ErrPlaybookNotFound
	}

	config, err := parsePlaybookConfig(obj.ConfigJSON)
	if err != nil {
		return nil, "", fmt.Errorf("parse active playbook config: %w", err)
	}

	return config, "dynamic_db", nil
}

func (b *playbookBiz) getPlaybookVersion(ctx context.Context, lineageID string, version int) (*model.PlaybookM, error) {
	if strings.TrimSpace(lineageID) == "" {
		return nil, gorm.ErrRecordNotFound
	}
	return b.store.Playbook().Get(ctx, where.T(ctx).F("lineage_id", lineageID).F("version", version))
}

func validatePlaybookConfig(config *PlaybookConfig) (string, error) {
	if config == nil {
		return "", errno.ErrPlaybookInvalidConfig
	}

	if config.Version == "" {
		return "", errno.ErrPlaybookInvalidConfig
	}

	configJSON, err := json.Marshal(config)
	if err != nil {
		return "", errno.ErrPlaybookInvalidConfig
	}

	return string(configJSON), nil
}

// parsePlaybookConfig parses JSON config string from database into PlaybookConfig.
func parsePlaybookConfig(configJSON string) (*PlaybookConfig, error) {
	var config PlaybookConfig
	if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
		return nil, fmt.Errorf("unmarshal playbook config: %w", err)
	}
	return &config, nil
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

func normalizeListLimit(limit *int64) int64 {
	if limit == nil || *limit <= 0 {
		return defaultListLimit
	}
	if *limit > maxListLimit {
		return maxListLimit
	}
	return *limit
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

// ============================================================================
// Legacy Export API - For backward compatibility with ai_job package
// ============================================================================

type Playbook struct {
	Version string         `json:"version"`
	Items   []PlaybookItem `json:"items"`
}

type PlaybookItem struct {
	ID           string               `json:"id"`
	Title        string               `json:"title"`
	Risk         string               `json:"risk"`
	Rationale    string               `json:"rationale"`
	Steps        []PlaybookStep       `json:"steps"`
	Verification PlaybookVerification `json:"verification"`
}

type PlaybookStep struct {
	Type          string `json:"type"`
	Text          string `json:"text"`
	RequiresHuman bool   `json:"requires_human,omitempty"`
}

type PlaybookVerification struct {
	UseVerificationPlan bool   `json:"use_verification_plan"`
	RecommendedSteps    []int  `json:"recommended_steps"`
	ExpectedOutcome     string `json:"expected_outcome"`
}

// BuildInput defines the input for building a playbook from diagnosis.
// This is kept for backward compatibility with existing ai_job code.
type BuildInput struct {
	DiagnosisJSON string
	RootCauseType string
}

// Build generates a playbook from diagnosis output.
// This is kept for backward compatibility with existing ai_job code.
//
// Deprecated: Use the new dynamic playbook management API instead.
func Build(input BuildInput) (*Playbook, bool, error) {
	if input.DiagnosisJSON == "" {
		return nil, false, fmt.Errorf("empty diagnosis")
	}

	var diagnosis map[string]interface{}
	if err := json.Unmarshal([]byte(input.DiagnosisJSON), &diagnosis); err != nil {
		return nil, false, fmt.Errorf("unmarshal diagnosis: %w", err)
	}

	playbook := &Playbook{
		Version: "t6",
		Items:   []PlaybookItem{},
	}

	switch input.RootCauseType {
	case "missing_evidence":
		playbook.Items = append(playbook.Items, legacyPlaybookItem(
			"pb-missing-evidence-collect",
			"Collect missing evidence before conclusion",
			"LOW",
			"Current diagnosis indicates missing signals. Collecting key evidence reduces false conclusions.",
			[]PlaybookStep{
				{Type: "check", Text: "Confirm logs and traces are queryable for the incident time window."},
				{Type: "check", Text: "Re-run metrics and log queries with aligned start and end timestamps."},
			},
			"Verification checks should return non-empty aligned evidence after data gaps are fixed.",
		))
	case "latency", "5xx", "timeout":
		playbook.Items = append(playbook.Items, legacyPlaybookItem(
			"pb-pattern-latency",
			"Validate latency and error spike scope",
			"LOW",
			"Pattern matches indicate request path saturation or partial dependency instability.",
			[]PlaybookStep{
				{Type: "check", Text: "Break down latency and 5xx by route or workload to identify the hottest subset."},
				{Type: "check", Text: "Verify upstream dependency health around the same timestamps."},
			},
			"Verification should confirm whether latency and error spikes stay concentrated on one subset.",
		))
	default:
		playbook.Items = append(playbook.Items, legacyPlaybookItem(
			"pb-fallback-baseline",
			"Apply generic low-risk RCA checks",
			"LOW",
			"No specific playbook rule matched; apply deterministic baseline checks.",
			[]PlaybookStep{
				{Type: "check", Text: "Review recent change history and dependency status for the incident service."},
				{Type: "check", Text: "Re-run the primary verification query to confirm issue reproducibility."},
			},
			"Verification should reproduce the same key signal before any mitigation decision.",
		))
	}

	return playbook, true, nil
}

func (b *playbookBiz) ensurePlaybookLineageID(ctx context.Context, obj *model.PlaybookM) error {
	if obj == nil {
		return errno.ErrInvalidArgument
	}
	if strings.TrimSpace(obj.LineageID) != "" {
		return nil
	}

	lineageID := newLineageID()
	db := b.store.DB(ctx)
	if err := db.Model(&model.PlaybookM{}).
		Where("name = ? AND (lineage_id = '' OR lineage_id IS NULL)", obj.Name).
		Update("lineage_id", lineageID).Error; err != nil {
		return errno.ErrPlaybookUpdateFailed
	}

	if err := db.Model(&model.PlaybookM{}).
		Where("id = ? AND (lineage_id = '' OR lineage_id IS NULL)", obj.ID).
		Update("lineage_id", lineageID).Error; err != nil {
		return errno.ErrPlaybookUpdateFailed
	}

	obj.LineageID = lineageID
	return nil
}

func (b *playbookBiz) isPlaybookNameAvailable(ctx context.Context, name string, currentLineageID string) (bool, error) {
	page := 0
	for {
		_, list, err := b.store.Playbook().List(ctx, where.T(ctx).F("name", name).O(page).L(100))
		if err != nil {
			return false, errno.ErrPlaybookGetFailed
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

func cloneStringPtr(in *string) *string {
	if in == nil {
		return nil
	}
	value := *in
	return &value
}

func cloneIntPtr(in *int) *int {
	if in == nil {
		return nil
	}
	value := *in
	return &value
}

func cloneTimePtr(in *time.Time) *time.Time {
	if in == nil {
		return nil
	}
	value := *in
	return &value
}

func intPtr(v int) *int {
	return &v
}

func newLineageID() string {
	return uuid.NewString()
}

func legacyPlaybookItem(id string, title string, risk string, rationale string, steps []PlaybookStep, expectedOutcome string) PlaybookItem {
	return PlaybookItem{
		ID:        id,
		Title:     title,
		Risk:      risk,
		Rationale: rationale,
		Steps:     steps,
		Verification: PlaybookVerification{
			UseVerificationPlan: true,
			RecommendedSteps:    []int{0},
			ExpectedOutcome:     expectedOutcome,
		},
	}
}
