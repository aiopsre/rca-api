//nolint:gocognit,gocyclo,nestif,nilerr,nilprotogetter,modernize,whitespace
package alerting_policy

//go:generate mockgen -destination mock_alerting_policy.go -package alerting_policy github.com/aiopsre/rca-api/internal/apiserver/biz/v1/alerting_policy AlertingPolicyBiz

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/onexstack/onexstack/pkg/errorsx"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	alertingruntime "github.com/aiopsre/rca-api/internal/apiserver/pkg/alerting/policy"
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

type AlertingPolicyBiz interface {
	Create(ctx context.Context, req *CreateRequest) (*CreateResponse, error)
	Get(ctx context.Context, id int64) (*GetResponse, error)
	List(ctx context.Context, req *ListRequest) (*ListResponse, error)
	Update(ctx context.Context, id int64, req *UpdateRequest) (*UpdateResponse, error)
	Delete(ctx context.Context, id int64) error
	Activate(ctx context.Context, id int64, operator string) error
	Deactivate(ctx context.Context, id int64) error
	Rollback(ctx context.Context, id int64, version int, operator string) error
	GetActive(ctx context.Context) (*GetResponse, error)

	AlertingPolicyExpansion
}

type AlertingPolicyExpansion interface{}

type alertingPolicyBiz struct {
	store store.IStore
}

var _ AlertingPolicyBiz = (*alertingPolicyBiz)(nil)

func New(store store.IStore) *alertingPolicyBiz {
	return &alertingPolicyBiz{store: store}
}

func (b *alertingPolicyBiz) refreshRuntimeConfig(ctx context.Context) {
	if b == nil || b.store == nil {
		return
	}
	if err := alertingruntime.SyncRuntimeConfig(ctx, b.store); err != nil {
		slog.WarnContext(ctx, "refresh alerting policy runtime config failed", "err", err)
	}
}

type CreateRequest struct {
	Name        string
	Description *string
	Config      *AlertingPolicyConfig
	CreatedBy   string
}

type CreateResponse struct {
	AlertingPolicy *model.AlertingPolicyM `json:"alerting_policy"`
}

type GetResponse struct {
	AlertingPolicy *model.AlertingPolicyM `json:"alerting_policy"`
}

type ListRequest struct {
	Name   *string
	Active *bool
	Offset int64
	Limit  *int64
}

type ListResponse struct {
	TotalCount       int64                    `json:"total_count"`
	AlertingPolicies []*model.AlertingPolicyM `json:"alerting_policies"`
}

type UpdateRequest struct {
	Name            *string
	Description     *string
	Config          *AlertingPolicyConfig
	ExpectedVersion *int
	UpdatedBy       string
}

type UpdateResponse struct {
	AlertingPolicy *model.AlertingPolicyM `json:"alerting_policy"`
}

type AlertingPolicyConfig struct {
	Version  int            `json:"version"`
	Defaults map[string]any `json:"defaults,omitempty"`
	Triggers map[string]any `json:"triggers,omitempty"`
	Extra    map[string]any `json:"extra,omitempty"`
}

func (b *alertingPolicyBiz) Create(ctx context.Context, req *CreateRequest) (*CreateResponse, error) {
	if req == nil {
		return nil, errno.ErrInvalidArgument
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, errno.ErrInvalidArgument
	}

	configJSON, err := validateAlertingPolicyConfig(req.Config)
	if err != nil {
		return nil, errno.ErrAlertingPolicyInvalidConfig
	}

	createdBy := normalizeOperator(ctx, req.CreatedBy)

	exists, err := b.store.AlertingPolicy().Get(ctx, where.T(ctx).F("name", name))
	if err != nil && !errorsx.Is(err, gorm.ErrRecordNotFound) {
		return nil, errno.ErrAlertingPolicyGetFailed
	}
	if exists != nil {
		return nil, errno.ErrAlertingPolicyAlreadyExists
	}

	obj := &model.AlertingPolicyM{
		Name:        name,
		Description: trimStringPtr(req.Description),
		LineageID:   newLineageID(),
		Version:     minVersion,
		ConfigJSON:  configJSON,
		Active:      false,
		CreatedBy:   createdBy,
		UpdatedBy:   &createdBy,
	}

	if err := b.store.AlertingPolicy().Create(ctx, obj); err != nil {
		return nil, errno.ErrAlertingPolicyCreateFailed
	}

	return &CreateResponse{
		AlertingPolicy: obj,
	}, nil
}

func (b *alertingPolicyBiz) Get(ctx context.Context, id int64) (*GetResponse, error) {
	if id <= 0 {
		return nil, errno.ErrInvalidArgument
	}

	obj, err := b.store.AlertingPolicy().Get(ctx, where.T(ctx).F("id", id))
	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrAlertingPolicyNotFound
		}
		return nil, errno.ErrAlertingPolicyGetFailed
	}

	return &GetResponse{
		AlertingPolicy: obj,
	}, nil
}

func (b *alertingPolicyBiz) List(ctx context.Context, req *ListRequest) (*ListResponse, error) {
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

	total, list, err := b.store.AlertingPolicy().List(ctx, whr)
	if err != nil {
		return nil, errno.ErrAlertingPolicyListFailed
	}

	return &ListResponse{
		TotalCount:       total,
		AlertingPolicies: list,
	}, nil
}

func (b *alertingPolicyBiz) Update(ctx context.Context, id int64, req *UpdateRequest) (*UpdateResponse, error) {
	if req == nil || id <= 0 {
		return nil, errno.ErrInvalidArgument
	}

	updatedBy := normalizeOperator(ctx, req.UpdatedBy)
	var updated *model.AlertingPolicyM

	err := b.store.TX(ctx, func(txCtx context.Context) error {
		obj, err := b.store.AlertingPolicy().Get(txCtx, where.T(txCtx).F("id", id))
		if err != nil {
			if errorsx.Is(err, gorm.ErrRecordNotFound) {
				return errno.ErrAlertingPolicyNotFound
			}
			return errno.ErrAlertingPolicyGetFailed
		}

		if req.ExpectedVersion != nil && *req.ExpectedVersion > 0 && obj.Version != *req.ExpectedVersion {
			return errno.ErrAlertingPolicyVersionMismatch
		}

		if err := b.ensurePolicyLineageID(txCtx, obj); err != nil {
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
				available, err := b.isPolicyNameAvailable(txCtx, name, obj.LineageID)
				if err != nil {
					return err
				}
				if !available {
					return errno.ErrAlertingPolicyAlreadyExists
				}
			}
			obj.Name = name
		}

		if req.Description != nil {
			obj.Description = trimStringPtr(req.Description)
		}

		if req.Config != nil {
			configJSON, err := validateAlertingPolicyConfig(req.Config)
			if err != nil {
				return errno.ErrAlertingPolicyInvalidConfig
			}
			obj.ConfigJSON = configJSON
		}

		snapshot := &model.AlertingPolicyM{
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

		if err := b.store.AlertingPolicy().Create(txCtx, snapshot); err != nil {
			return errno.ErrAlertingPolicyCreateFailed
		}

		obj.Version = oldVersion + 1
		obj.PreviousVersion = intPtr(oldVersion)
		obj.UpdatedBy = &updatedBy

		if err := b.store.AlertingPolicy().Update(txCtx, obj); err != nil {
			return errno.ErrAlertingPolicyUpdateFailed
		}

		updated = obj
		return nil
	})
	if err != nil {
		return nil, err
	}
	b.refreshRuntimeConfig(ctx)

	return &UpdateResponse{AlertingPolicy: updated}, nil
}

func (b *alertingPolicyBiz) Delete(ctx context.Context, id int64) error {
	if id <= 0 {
		return errno.ErrInvalidArgument
	}

	_, err := b.store.AlertingPolicy().Get(ctx, where.T(ctx).F("id", id))
	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return errno.ErrAlertingPolicyNotFound
		}
		return errno.ErrAlertingPolicyGetFailed
	}

	if err := b.store.AlertingPolicy().Delete(ctx, where.T(ctx).F("id", id)); err != nil {
		return errno.ErrAlertingPolicyDeleteFailed
	}
	b.refreshRuntimeConfig(ctx)

	return nil
}

func (b *alertingPolicyBiz) Activate(ctx context.Context, id int64, operator string) error {
	if id <= 0 {
		return errno.ErrInvalidArgument
	}

	obj, err := b.store.AlertingPolicy().Get(ctx, where.T(ctx).F("id", id))
	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return errno.ErrAlertingPolicyNotFound
		}
		return errno.ErrAlertingPolicyGetFailed
	}

	if obj.Active {
		return nil
	}

	op := normalizeOperator(ctx, operator)

	if err := b.store.AlertingPolicy().Activate(ctx, id, op); err != nil {
		return errno.ErrAlertingPolicyActivateFailed
	}
	b.refreshRuntimeConfig(ctx)

	return nil
}

func (b *alertingPolicyBiz) Deactivate(ctx context.Context, id int64) error {
	if id <= 0 {
		return errno.ErrInvalidArgument
	}

	obj, err := b.store.AlertingPolicy().Get(ctx, where.T(ctx).F("id", id))
	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return errno.ErrAlertingPolicyNotFound
		}
		return errno.ErrAlertingPolicyGetFailed
	}

	if !obj.Active {
		return nil
	}

	if err := b.store.AlertingPolicy().Deactivate(ctx, id); err != nil {
		return errno.ErrAlertingPolicyDeactivateFailed
	}
	b.refreshRuntimeConfig(ctx)

	return nil
}

func (b *alertingPolicyBiz) Rollback(ctx context.Context, id int64, version int, operator string) error {
	if id <= 0 || version < minVersion {
		return errno.ErrInvalidArgument
	}

	op := normalizeOperator(ctx, operator)
	err := b.store.TX(ctx, func(txCtx context.Context) error {
		currentObj, err := b.store.AlertingPolicy().Get(txCtx, where.T(txCtx).F("id", id))
		if err != nil {
			if errorsx.Is(err, gorm.ErrRecordNotFound) {
				return errno.ErrAlertingPolicyNotFound
			}
			return errno.ErrAlertingPolicyGetFailed
		}

		if version >= currentObj.Version {
			return errno.ErrInvalidArgument
		}

		if err := b.ensurePolicyLineageID(txCtx, currentObj); err != nil {
			return err
		}

		historyObj, err := b.getPolicyVersion(txCtx, currentObj.LineageID, version)
		if err != nil {
			if errorsx.Is(err, gorm.ErrRecordNotFound) {
				return errno.ErrAlertingPolicyNotFound
			}
			return errno.ErrAlertingPolicyGetFailed
		}

		rollbackObj := &model.AlertingPolicyM{
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
			if err := b.store.AlertingPolicy().Deactivate(txCtx, id); err != nil {
				return errno.ErrAlertingPolicyDeactivateFailed
			}
			now := time.Now()
			rollbackObj.Active = true
			rollbackObj.ActivatedAt = &now
			rollbackObj.ActivatedBy = &op
		}

		if err := b.store.AlertingPolicy().Create(txCtx, rollbackObj); err != nil {
			return errno.ErrAlertingPolicyCreateFailed
		}

		return nil
	})
	if err != nil {
		return err
	}
	b.refreshRuntimeConfig(ctx)
	return nil
}

func (b *alertingPolicyBiz) GetActive(ctx context.Context) (*GetResponse, error) {
	obj, err := b.store.AlertingPolicy().GetActive(ctx)
	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrAlertingPolicyNotFound
		}
		return nil, errno.ErrAlertingPolicyGetFailed
	}

	return &GetResponse{
		AlertingPolicy: obj,
	}, nil
}

func (b *alertingPolicyBiz) getPolicyVersion(ctx context.Context, lineageID string, version int) (*model.AlertingPolicyM, error) {
	if strings.TrimSpace(lineageID) == "" {
		return nil, gorm.ErrRecordNotFound
	}
	return b.store.AlertingPolicy().Get(ctx, where.T(ctx).F("lineage_id", lineageID).F("version", version))
}

func validateAlertingPolicyConfig(config *AlertingPolicyConfig) (string, error) {
	if config == nil {
		return "", errno.ErrAlertingPolicyInvalidConfig
	}

	if config.Version <= 0 {
		return "", errno.ErrAlertingPolicyInvalidConfig
	}

	configJSON, err := json.Marshal(config)
	if err != nil {
		return "", errno.ErrAlertingPolicyInvalidConfig
	}

	return string(configJSON), nil
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

func (b *alertingPolicyBiz) ensurePolicyLineageID(ctx context.Context, obj *model.AlertingPolicyM) error {
	if obj == nil {
		return errno.ErrInvalidArgument
	}
	if strings.TrimSpace(obj.LineageID) != "" {
		return nil
	}

	lineageID := newLineageID()
	db := b.store.DB(ctx)
	if err := db.Model(&model.AlertingPolicyM{}).
		Where("name = ? AND (lineage_id = '' OR lineage_id IS NULL)", obj.Name).
		Update("lineage_id", lineageID).Error; err != nil {
		return errno.ErrAlertingPolicyUpdateFailed
	}

	if err := db.Model(&model.AlertingPolicyM{}).
		Where("id = ? AND (lineage_id = '' OR lineage_id IS NULL)", obj.ID).
		Update("lineage_id", lineageID).Error; err != nil {
		return errno.ErrAlertingPolicyUpdateFailed
	}

	obj.LineageID = lineageID
	return nil
}

func (b *alertingPolicyBiz) isPolicyNameAvailable(ctx context.Context, name string, currentLineageID string) (bool, error) {
	page := 0
	for {
		_, list, err := b.store.AlertingPolicy().List(ctx, where.T(ctx).F("name", name).O(page).L(100))
		if err != nil {
			return false, errno.ErrAlertingPolicyGetFailed
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
