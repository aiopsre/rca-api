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
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	"github.com/aiopsre/rca-api/internal/apiserver/model"
	alertingruntime "github.com/aiopsre/rca-api/internal/apiserver/pkg/alerting/policy"
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

// AlertingPolicyBiz defines alerting policy management use-cases.
type AlertingPolicyBiz interface {
	Create(ctx context.Context, rq *v1.CreateAlertingPolicyRequest) (*v1.CreateAlertingPolicyResponse, error)
	Get(ctx context.Context, rq *v1.GetAlertingPolicyRequest) (*v1.GetAlertingPolicyResponse, error)
	List(ctx context.Context, rq *v1.ListAlertingPoliciesRequest) (*v1.ListAlertingPoliciesResponse, error)
	Update(ctx context.Context, rq *v1.UpdateAlertingPolicyRequest) (*v1.UpdateAlertingPolicyResponse, error)
	Delete(ctx context.Context, rq *v1.DeleteAlertingPolicyRequest) (*v1.DeleteAlertingPolicyResponse, error)
	Activate(ctx context.Context, rq *v1.ActivateAlertingPolicyRequest) (*v1.ActivateAlertingPolicyResponse, error)
	Deactivate(ctx context.Context, rq *v1.DeactivateAlertingPolicyRequest) (*v1.DeactivateAlertingPolicyResponse, error)
	Rollback(ctx context.Context, rq *v1.RollbackAlertingPolicyRequest) (*v1.RollbackAlertingPolicyResponse, error)
	GetActive(ctx context.Context, rq *v1.GetActiveAlertingPolicyRequest) (*v1.GetActiveAlertingPolicyResponse, error)

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

// AlertingPolicyConfig represents the config structure for validation.
type AlertingPolicyConfig struct {
	Version  int            `json:"version"`
	Defaults map[string]any `json:"defaults,omitempty"`
	Triggers map[string]any `json:"triggers,omitempty"`
	Extra    map[string]any `json:"extra,omitempty"`
}

func (b *alertingPolicyBiz) Create(ctx context.Context, rq *v1.CreateAlertingPolicyRequest) (*v1.CreateAlertingPolicyResponse, error) {
	if rq == nil {
		return nil, errno.ErrInvalidArgument
	}

	name := strings.TrimSpace(rq.GetName())
	if name == "" {
		return nil, errno.ErrInvalidArgument
	}

	configJSON := strings.TrimSpace(rq.GetConfigJSON())
	if configJSON == "" {
		return nil, errno.ErrAlertingPolicyInvalidConfig
	}

	// Validate config JSON is valid JSON structure
	if _, err := validateConfigJSON(configJSON); err != nil {
		return nil, errno.ErrAlertingPolicyInvalidConfig
	}

	createdBy := normalizeOperator(ctx, "")

	exists, err := b.store.AlertingPolicy().Get(ctx, where.T(ctx).F("name", name))
	if err != nil && !errorsx.Is(err, gorm.ErrRecordNotFound) {
		return nil, errno.ErrAlertingPolicyGetFailed
	}
	if exists != nil {
		return nil, errno.ErrAlertingPolicyAlreadyExists
	}

	obj := &model.AlertingPolicyM{
		Name:        name,
		Description: trimStringPtr(rq.Description),
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

	return &v1.CreateAlertingPolicyResponse{
		AlertingPolicy: modelToProto(obj),
	}, nil
}

func (b *alertingPolicyBiz) Get(ctx context.Context, rq *v1.GetAlertingPolicyRequest) (*v1.GetAlertingPolicyResponse, error) {
	if rq == nil || rq.GetId() <= 0 {
		return nil, errno.ErrInvalidArgument
	}

	obj, err := b.store.AlertingPolicy().Get(ctx, where.T(ctx).F("id", rq.GetId()))
	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrAlertingPolicyNotFound
		}
		return nil, errno.ErrAlertingPolicyGetFailed
	}

	return &v1.GetAlertingPolicyResponse{
		AlertingPolicy: modelToProto(obj),
	}, nil
}

func (b *alertingPolicyBiz) List(ctx context.Context, rq *v1.ListAlertingPoliciesRequest) (*v1.ListAlertingPoliciesResponse, error) {
	if rq == nil {
		rq = &v1.ListAlertingPoliciesRequest{}
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

	total, list, err := b.store.AlertingPolicy().List(ctx, whr)
	if err != nil {
		return nil, errno.ErrAlertingPolicyListFailed
	}

	protoList := make([]*v1.AlertingPolicy, 0, len(list))
	for _, m := range list {
		protoList = append(protoList, modelToProto(m))
	}

	return &v1.ListAlertingPoliciesResponse{
		TotalCount:       total,
		AlertingPolicies: protoList,
	}, nil
}

func (b *alertingPolicyBiz) Update(ctx context.Context, rq *v1.UpdateAlertingPolicyRequest) (*v1.UpdateAlertingPolicyResponse, error) {
	if rq == nil || rq.GetId() <= 0 {
		return nil, errno.ErrInvalidArgument
	}

	updatedBy := normalizeOperator(ctx, "")
	var updated *model.AlertingPolicyM

	err := b.store.TX(ctx, func(txCtx context.Context) error {
		obj, err := b.store.AlertingPolicy().Get(txCtx, where.T(txCtx).F("id", rq.GetId()))
		if err != nil {
			if errorsx.Is(err, gorm.ErrRecordNotFound) {
				return errno.ErrAlertingPolicyNotFound
			}
			return errno.ErrAlertingPolicyGetFailed
		}

		if rq.ExpectedVersion != nil && *rq.ExpectedVersion > 0 && obj.Version != int(*rq.ExpectedVersion) {
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

		if rq.Name != nil {
			name := strings.TrimSpace(*rq.Name)
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

		if rq.Description != nil {
			obj.Description = trimStringPtr(rq.Description)
		}

		if rq.ConfigJSON != nil {
			configJSON := strings.TrimSpace(*rq.ConfigJSON)
			if configJSON == "" {
				return errno.ErrAlertingPolicyInvalidConfig
			}
			if _, err := validateConfigJSON(configJSON); err != nil {
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

	return &v1.UpdateAlertingPolicyResponse{AlertingPolicy: modelToProto(updated)}, nil
}

func (b *alertingPolicyBiz) Delete(ctx context.Context, rq *v1.DeleteAlertingPolicyRequest) (*v1.DeleteAlertingPolicyResponse, error) {
	if rq == nil || rq.GetId() <= 0 {
		return nil, errno.ErrInvalidArgument
	}

	_, err := b.store.AlertingPolicy().Get(ctx, where.T(ctx).F("id", rq.GetId()))
	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrAlertingPolicyNotFound
		}
		return nil, errno.ErrAlertingPolicyGetFailed
	}

	if err := b.store.AlertingPolicy().Delete(ctx, where.T(ctx).F("id", rq.GetId())); err != nil {
		return nil, errno.ErrAlertingPolicyDeleteFailed
	}
	b.refreshRuntimeConfig(ctx)

	return &v1.DeleteAlertingPolicyResponse{}, nil
}

func (b *alertingPolicyBiz) Activate(ctx context.Context, rq *v1.ActivateAlertingPolicyRequest) (*v1.ActivateAlertingPolicyResponse, error) {
	if rq == nil || rq.GetId() <= 0 {
		return nil, errno.ErrInvalidArgument
	}

	obj, err := b.store.AlertingPolicy().Get(ctx, where.T(ctx).F("id", rq.GetId()))
	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrAlertingPolicyNotFound
		}
		return nil, errno.ErrAlertingPolicyGetFailed
	}

	if obj.Active {
		return &v1.ActivateAlertingPolicyResponse{}, nil
	}

	op := normalizeOperator(ctx, rq.GetOperator())

	if err := b.store.AlertingPolicy().Activate(ctx, rq.GetId(), op); err != nil {
		return nil, errno.ErrAlertingPolicyActivateFailed
	}
	b.refreshRuntimeConfig(ctx)

	return &v1.ActivateAlertingPolicyResponse{}, nil
}

func (b *alertingPolicyBiz) Deactivate(ctx context.Context, rq *v1.DeactivateAlertingPolicyRequest) (*v1.DeactivateAlertingPolicyResponse, error) {
	if rq == nil || rq.GetId() <= 0 {
		return nil, errno.ErrInvalidArgument
	}

	obj, err := b.store.AlertingPolicy().Get(ctx, where.T(ctx).F("id", rq.GetId()))
	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrAlertingPolicyNotFound
		}
		return nil, errno.ErrAlertingPolicyGetFailed
	}

	if !obj.Active {
		return &v1.DeactivateAlertingPolicyResponse{}, nil
	}

	if err := b.store.AlertingPolicy().Deactivate(ctx, rq.GetId()); err != nil {
		return nil, errno.ErrAlertingPolicyDeactivateFailed
	}
	b.refreshRuntimeConfig(ctx)

	return &v1.DeactivateAlertingPolicyResponse{}, nil
}

func (b *alertingPolicyBiz) Rollback(ctx context.Context, rq *v1.RollbackAlertingPolicyRequest) (*v1.RollbackAlertingPolicyResponse, error) {
	if rq == nil || rq.GetId() <= 0 || rq.GetVersion() < minVersion {
		return nil, errno.ErrInvalidArgument
	}

	op := normalizeOperator(ctx, rq.GetOperator())
	err := b.store.TX(ctx, func(txCtx context.Context) error {
		currentObj, err := b.store.AlertingPolicy().Get(txCtx, where.T(txCtx).F("id", rq.GetId()))
		if err != nil {
			if errorsx.Is(err, gorm.ErrRecordNotFound) {
				return errno.ErrAlertingPolicyNotFound
			}
			return errno.ErrAlertingPolicyGetFailed
		}

		if int(rq.GetVersion()) >= currentObj.Version {
			return errno.ErrInvalidArgument
		}

		if err := b.ensurePolicyLineageID(txCtx, currentObj); err != nil {
			return err
		}

		historyObj, err := b.getPolicyVersion(txCtx, currentObj.LineageID, int(rq.GetVersion()))
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
			if err := b.store.AlertingPolicy().Deactivate(txCtx, rq.GetId()); err != nil {
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
		return nil, err
	}
	b.refreshRuntimeConfig(ctx)
	return &v1.RollbackAlertingPolicyResponse{}, nil
}

func (b *alertingPolicyBiz) GetActive(ctx context.Context, rq *v1.GetActiveAlertingPolicyRequest) (*v1.GetActiveAlertingPolicyResponse, error) {
	obj, err := b.store.AlertingPolicy().GetActive(ctx)
	if err != nil {
		if errorsx.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrAlertingPolicyNotFound
		}
		return nil, errno.ErrAlertingPolicyGetFailed
	}

	return &v1.GetActiveAlertingPolicyResponse{
		AlertingPolicy: modelToProto(obj),
	}, nil
}

func (b *alertingPolicyBiz) getPolicyVersion(ctx context.Context, lineageID string, version int) (*model.AlertingPolicyM, error) {
	if strings.TrimSpace(lineageID) == "" {
		return nil, gorm.ErrRecordNotFound
	}
	return b.store.AlertingPolicy().Get(ctx, where.T(ctx).F("lineage_id", lineageID).F("version", version))
}

func validateConfigJSON(configJSON string) (*AlertingPolicyConfig, error) {
	var config AlertingPolicyConfig
	if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
		return nil, err
	}
	if config.Version <= 0 {
		return nil, errno.ErrAlertingPolicyInvalidConfig
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

func int32Ptr(v int32) *int32 {
	return &v
}

func newLineageID() string {
	return uuid.NewString()
}

func modelToProto(m *model.AlertingPolicyM) *v1.AlertingPolicy {
	if m == nil {
		return nil
	}
	pb := &v1.AlertingPolicy{
		Id:          m.ID,
		Name:        m.Name,
		LineageID:   m.LineageID,
		Version:     int32(m.Version),
		ConfigJSON:  m.ConfigJSON,
		Active:      m.Active,
		CreatedBy:   m.CreatedBy,
		CreatedAt:   timestamppb.New(m.CreatedAt),
		UpdatedAt:   timestamppb.New(m.UpdatedAt),
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