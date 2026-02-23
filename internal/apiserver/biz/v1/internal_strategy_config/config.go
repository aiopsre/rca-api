package internal_strategy_config

//go:generate mockgen -destination mock_config.go -package internal_strategy_config github.com/aiopsre/rca-api/internal/apiserver/biz/v1/internal_strategy_config ConfigBiz

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/onexstack/onexstack/pkg/errorsx"
	"gorm.io/gorm"

	sessionbiz "github.com/aiopsre/rca-api/internal/apiserver/biz/v1/session"
	"github.com/aiopsre/rca-api/internal/apiserver/model"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/internalstrategycfg"
	"github.com/aiopsre/rca-api/internal/apiserver/pkg/skillartifact"
	"github.com/aiopsre/rca-api/internal/apiserver/store"
	"github.com/aiopsre/rca-api/internal/pkg/errno"
)

const (
	defaultSLADueSeconds         = int64(7200)
	defaultEscalatedThresholdSec = int64(14400)
)

type ConfigBiz interface {
	GetPipeline(ctx context.Context, rq *GetPipelineConfigRequest) (*PipelineConfigView, error)
	UpsertPipeline(ctx context.Context, rq *UpsertPipelineConfigRequest) (*PipelineConfigView, error)

	GetTrigger(ctx context.Context, rq *GetTriggerConfigRequest) (*TriggerConfigView, error)
	UpsertTrigger(ctx context.Context, rq *UpsertTriggerConfigRequest) (*TriggerConfigView, error)

	GetToolsets(ctx context.Context, rq *GetToolsetConfigRequest) (*ToolsetConfigView, error)
	UpsertToolset(ctx context.Context, rq *UpsertToolsetConfigRequest) (*ToolsetConfigView, error)

	GetSkillsets(ctx context.Context, rq *GetSkillsetConfigRequest) (*SkillsetConfigView, error)
	UpsertSkillset(ctx context.Context, rq *UpsertSkillsetConfigRequest) (*SkillsetConfigView, error)
	GetSkillRelease(ctx context.Context, rq *GetSkillReleaseRequest) (*SkillReleaseView, error)
	RegisterSkillRelease(ctx context.Context, rq *RegisterSkillReleaseRequest) (*SkillReleaseView, error)
	UploadSkillRelease(ctx context.Context, rq *UploadSkillReleaseRequest) (*SkillReleaseView, error)

	GetSLA(ctx context.Context, rq *GetSLAConfigRequest) (*SLAConfigView, error)
	UpsertSLA(ctx context.Context, rq *UpsertSLAConfigRequest) (*SLAConfigView, error)

	GetSessionAssignment(ctx context.Context, rq *GetSessionAssignmentRequest) (*SessionAssignmentView, error)
	AssignSession(ctx context.Context, rq *AssignSessionRequest) (*SessionAssignmentView, error)

	ResolveTriggerPipeline(ctx context.Context, triggerType string) (pipelineID string, sessionType string, source string, err error)
	ResolveToolsetByPipeline(ctx context.Context, pipelineID string) ([]*ToolsetItem, string, error)
	ResolveSkillsetByPipeline(ctx context.Context, pipelineID string) ([]*SkillsetItem, string, error)
	ResolveSLABySessionType(ctx context.Context, sessionType string) (*SLAConfigView, string, error)

	ConfigExpansion
}

//nolint:modernize // Keep explicit placeholder for future extensions.
type ConfigExpansion interface{}

type configBiz struct {
	store      store.IStore
	sessionBiz sessionAdapter
	bootstrap  dynamicDefaultBootstrap
}

type sessionAdapter interface {
	UpdateAssignment(ctx context.Context, rq *sessionbiz.UpdateAssignmentRequest) (*sessionbiz.UpdateAssignmentResponse, error)
	Get(ctx context.Context, rq *sessionbiz.GetSessionContextRequest) (*sessionbiz.GetSessionContextResponse, error)
}

var _ ConfigBiz = (*configBiz)(nil)

func New(store store.IStore) *configBiz {
	return &configBiz{
		store:      store,
		sessionBiz: sessionbiz.New(store),
	}
}

func newWithDeps(store store.IStore, sessionBiz sessionAdapter) *configBiz {
	return &configBiz{store: store, sessionBiz: sessionBiz}
}

type dynamicDefaultBootstrap struct {
	mu   sync.Mutex
	done bool
}

func (b *configBiz) ensureBuiltinDynamicConfigs(ctx context.Context) error {
	if b == nil || b.store == nil {
		return errno.ErrInvalidArgument
	}
	if b.bootstrap.done {
		return nil
	}

	b.bootstrap.mu.Lock()
	defer b.bootstrap.mu.Unlock()
	if b.bootstrap.done {
		return nil
	}

	err := b.store.TX(ctx, func(txCtx context.Context) error {
		if err := b.seedBuiltinPipelineConfigs(txCtx); err != nil {
			return err
		}
		if err := b.seedBuiltinTriggerConfigs(txCtx); err != nil {
			return err
		}
		if err := b.seedBuiltinToolsetConfigs(txCtx); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return errno.ErrInternal
	}

	b.bootstrap.done = true
	return nil
}

func (b *configBiz) seedBuiltinPipelineConfigs(ctx context.Context) error {
	for _, item := range internalstrategycfg.BuiltinPipelineDefaults() {
		obj := &model.PipelineConfigM{
			AlertSource: item.AlertSource,
			Service:     item.Service,
			Namespace:   item.Namespace,
			PipelineID:  item.PipelineID,
			GraphID:     emptyStringToNil(item.GraphID),
		}
		if err := b.store.InternalStrategyConfig().UpsertPipelineConfig(ctx, obj); err != nil {
			return err
		}
	}
	return nil
}

func (b *configBiz) seedBuiltinTriggerConfigs(ctx context.Context) error {
	for _, item := range internalstrategycfg.BuiltinTriggerDefaults() {
		obj := &model.TriggerConfigM{
			TriggerType: item.TriggerType,
			PipelineID:  item.PipelineID,
			SessionType: item.SessionType,
			Fallback:    item.Fallback,
		}
		if err := b.store.InternalStrategyConfig().UpsertTriggerConfig(ctx, obj); err != nil {
			return err
		}
	}
	return nil
}

func (b *configBiz) seedBuiltinToolsetConfigs(ctx context.Context) error {
	for _, item := range internalstrategycfg.BuiltinToolsetDefaults() {
		rawAllowedTools, _ := json.Marshal(normalizeListLower(item.AllowedTools))
		rawAllowedToolsString := string(rawAllowedTools)
		obj := &model.ToolsetConfigDynamicM{
			PipelineID:       item.PipelineID,
			ToolsetName:      item.ToolsetName,
			AllowedToolsJSON: &rawAllowedToolsString,
		}
		if err := b.store.InternalStrategyConfig().UpsertToolsetConfig(ctx, obj); err != nil {
			return err
		}
	}
	return nil
}

type GetPipelineConfigRequest struct {
	AlertSource string
	Service     string
	Namespace   string
}

type UpsertPipelineConfigRequest struct {
	AlertSource string
	Service     string
	Namespace   string
	PipelineID  string
	GraphID     *string
}

type PipelineConfigView struct {
	AlertSource string `json:"alert_source"`
	Service     string `json:"service,omitempty"`
	Namespace   string `json:"namespace,omitempty"`
	PipelineID  string `json:"pipeline_id"`
	GraphID     string `json:"graph_id,omitempty"`
	Source      string `json:"source"`
}

func (b *configBiz) GetPipeline(ctx context.Context, rq *GetPipelineConfigRequest) (*PipelineConfigView, error) {
	if rq == nil || b == nil || b.store == nil {
		return nil, errno.ErrInvalidArgument
	}
	alertSource := strings.TrimSpace(rq.AlertSource)
	if alertSource == "" {
		return nil, errno.ErrInvalidArgument
	}
	service := strings.TrimSpace(rq.Service)
	namespace := strings.TrimSpace(rq.Namespace)
	if err := b.ensureBuiltinDynamicConfigs(ctx); err != nil {
		return nil, err
	}
	if storeObj, err := b.getPipelineFromStore(ctx, alertSource, service, namespace); err == nil && storeObj != nil {
		return mapPipelineModel(storeObj, "dynamic_db"), nil
	} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errno.ErrInternal
	}
	return nil, errno.ErrNotFound
}

func (b *configBiz) UpsertPipeline(ctx context.Context, rq *UpsertPipelineConfigRequest) (*PipelineConfigView, error) {
	if rq == nil || b == nil || b.store == nil {
		return nil, errno.ErrInvalidArgument
	}
	obj := &model.PipelineConfigM{
		AlertSource: strings.TrimSpace(rq.AlertSource),
		Service:     strings.TrimSpace(rq.Service),
		Namespace:   strings.TrimSpace(rq.Namespace),
		PipelineID:  strings.ToLower(strings.TrimSpace(rq.PipelineID)),
		GraphID:     trimStringPtr(rq.GraphID),
	}
	if obj.AlertSource == "" || obj.PipelineID == "" {
		return nil, errno.ErrInvalidArgument
	}
	if err := b.store.InternalStrategyConfig().UpsertPipelineConfig(ctx, obj); err != nil {
		return nil, errno.ErrInternal
	}
	out, err := b.getPipelineFromStore(ctx, obj.AlertSource, obj.Service, obj.Namespace)
	if err != nil {
		return nil, errno.ErrInternal
	}
	return mapPipelineModel(out, "dynamic_db"), nil
}

func (b *configBiz) getPipelineFromStore(
	ctx context.Context,
	alertSource string,
	service string,
	namespace string,
) (*model.PipelineConfigM, error) {
	configStore := b.store.InternalStrategyConfig()
	if configStore == nil {
		return nil, gorm.ErrRecordNotFound
	}
	candidates := [][3]string{
		{alertSource, service, namespace},
		{alertSource, service, ""},
		{alertSource, "", ""},
	}
	for _, item := range candidates {
		obj, err := configStore.GetPipelineConfig(ctx, item[0], item[1], item[2])
		if err == nil && obj != nil {
			return obj, nil
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	return nil, gorm.ErrRecordNotFound
}

func mapPipelineModel(obj *model.PipelineConfigM, source string) *PipelineConfigView {
	if obj == nil {
		return nil
	}
	return &PipelineConfigView{
		AlertSource: strings.TrimSpace(obj.AlertSource),
		Service:     strings.TrimSpace(obj.Service),
		Namespace:   strings.TrimSpace(obj.Namespace),
		PipelineID:  strings.ToLower(strings.TrimSpace(obj.PipelineID)),
		GraphID:     strings.TrimSpace(ptrString(obj.GraphID)),
		Source:      source,
	}
}

type GetTriggerConfigRequest struct {
	TriggerType string
}

type UpsertTriggerConfigRequest struct {
	TriggerType string
	PipelineID  string
	SessionType string
	Fallback    bool
}

type TriggerConfigView struct {
	TriggerType string `json:"trigger_type"`
	PipelineID  string `json:"pipeline_id"`
	SessionType string `json:"session_type,omitempty"`
	Fallback    bool   `json:"fallback"`
	Source      string `json:"source"`
}

func (b *configBiz) GetTrigger(ctx context.Context, rq *GetTriggerConfigRequest) (*TriggerConfigView, error) {
	if rq == nil || b == nil || b.store == nil {
		return nil, errno.ErrInvalidArgument
	}
	triggerType := strings.ToLower(strings.TrimSpace(rq.TriggerType))
	if triggerType == "" {
		return nil, errno.ErrInvalidArgument
	}
	if err := b.ensureBuiltinDynamicConfigs(ctx); err != nil {
		return nil, err
	}
	if configStore := b.store.InternalStrategyConfig(); configStore != nil {
		obj, err := configStore.GetTriggerConfig(ctx, triggerType)
		if err == nil && obj != nil {
			return mapTriggerModel(obj, "dynamic_db"), nil
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrInternal
		}
	}
	return nil, errno.ErrNotFound
}

func (b *configBiz) UpsertTrigger(ctx context.Context, rq *UpsertTriggerConfigRequest) (*TriggerConfigView, error) {
	if rq == nil || b == nil || b.store == nil {
		return nil, errno.ErrInvalidArgument
	}
	obj := &model.TriggerConfigM{
		TriggerType: strings.ToLower(strings.TrimSpace(rq.TriggerType)),
		PipelineID:  strings.ToLower(strings.TrimSpace(rq.PipelineID)),
		SessionType: strings.ToLower(strings.TrimSpace(rq.SessionType)),
		Fallback:    rq.Fallback,
	}
	if obj.TriggerType == "" || obj.PipelineID == "" {
		return nil, errno.ErrInvalidArgument
	}
	if err := b.store.InternalStrategyConfig().UpsertTriggerConfig(ctx, obj); err != nil {
		return nil, errno.ErrInternal
	}
	out, err := b.store.InternalStrategyConfig().GetTriggerConfig(ctx, obj.TriggerType)
	if err != nil {
		return nil, errno.ErrInternal
	}
	return mapTriggerModel(out, "dynamic_db"), nil
}

func mapTriggerModel(obj *model.TriggerConfigM, source string) *TriggerConfigView {
	if obj == nil {
		return nil
	}
	return &TriggerConfigView{
		TriggerType: strings.ToLower(strings.TrimSpace(obj.TriggerType)),
		PipelineID:  strings.ToLower(strings.TrimSpace(obj.PipelineID)),
		SessionType: strings.ToLower(strings.TrimSpace(obj.SessionType)),
		Fallback:    obj.Fallback,
		Source:      source,
	}
}

type GetToolsetConfigRequest struct {
	PipelineID string
}

type UpsertToolsetConfigRequest struct {
	PipelineID   string
	ToolsetName  string
	AllowedTools []string
}

type ToolsetItem struct {
	ToolsetName  string   `json:"toolset_name"`
	AllowedTools []string `json:"allowed_tools"`
}

type ToolsetConfigView struct {
	PipelineID string         `json:"pipeline_id"`
	Items      []*ToolsetItem `json:"items"`
	Source     string         `json:"source"`
}

func (b *configBiz) GetToolsets(ctx context.Context, rq *GetToolsetConfigRequest) (*ToolsetConfigView, error) {
	if rq == nil || b == nil || b.store == nil {
		return nil, errno.ErrInvalidArgument
	}
	pipelineID := strings.ToLower(strings.TrimSpace(rq.PipelineID))
	if pipelineID == "" {
		return nil, errno.ErrInvalidArgument
	}
	items, source, err := b.ResolveToolsetByPipeline(ctx, pipelineID)
	if err != nil {
		if errorsx.Is(err, errno.ErrNotFound) {
			return nil, err
		}
		return nil, errno.ErrInternal
	}
	return &ToolsetConfigView{PipelineID: pipelineID, Items: items, Source: source}, nil
}

func (b *configBiz) UpsertToolset(ctx context.Context, rq *UpsertToolsetConfigRequest) (*ToolsetConfigView, error) {
	if rq == nil || b == nil || b.store == nil {
		return nil, errno.ErrInvalidArgument
	}
	pipelineID := strings.ToLower(strings.TrimSpace(rq.PipelineID))
	toolsetName := strings.TrimSpace(rq.ToolsetName)
	allowedTools := normalizeListLower(rq.AllowedTools)
	if pipelineID == "" || toolsetName == "" || len(allowedTools) == 0 {
		return nil, errno.ErrInvalidArgument
	}
	rawAllowedTools, _ := json.Marshal(allowedTools)
	rawAllowedToolsString := string(rawAllowedTools)
	obj := &model.ToolsetConfigDynamicM{
		PipelineID:       pipelineID,
		ToolsetName:      toolsetName,
		AllowedToolsJSON: &rawAllowedToolsString,
	}
	if err := b.store.InternalStrategyConfig().UpsertToolsetConfig(ctx, obj); err != nil {
		return nil, errno.ErrInternal
	}
	return b.GetToolsets(ctx, &GetToolsetConfigRequest{PipelineID: pipelineID})
}

func (b *configBiz) ResolveToolsetByPipeline(
	ctx context.Context,
	pipelineID string,
) ([]*ToolsetItem, string, error) {
	if b == nil || b.store == nil {
		return nil, "", errno.ErrInvalidArgument
	}
	pipelineID = strings.ToLower(strings.TrimSpace(pipelineID))
	if pipelineID == "" {
		return nil, "", errno.ErrInvalidArgument
	}
	if err := b.ensureBuiltinDynamicConfigs(ctx); err != nil {
		return nil, "", err
	}
	if configStore := b.store.InternalStrategyConfig(); configStore != nil {
		list, err := configStore.ListToolsetConfigsByPipeline(ctx, pipelineID)
		if err != nil {
			return nil, "", err
		}
		mapped := mapToolsetModelList(list)
		if len(mapped) > 0 {
			return mapped, "dynamic_db", nil
		}
	}
	return nil, "", errno.ErrNotFound
}

func mapToolsetModelList(in []*model.ToolsetConfigDynamicM) []*ToolsetItem {
	if len(in) == 0 {
		return nil
	}
	out := make([]*ToolsetItem, 0, len(in))
	for _, item := range in {
		if item == nil {
			continue
		}
		allowed := decodeJSONStringList(item.AllowedToolsJSON)
		if len(allowed) == 0 {
			continue
		}
		out = append(out, &ToolsetItem{ToolsetName: strings.TrimSpace(item.ToolsetName), AllowedTools: allowed})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

type GetSkillsetConfigRequest struct {
	PipelineID string
}

type UpsertSkillsetConfigRequest struct {
	PipelineID   string
	SkillsetName string
	Skills       []*SkillRef
}

type GetSkillReleaseRequest struct {
	SkillID string
	Version string
}

type RegisterSkillReleaseRequest struct {
	SkillID      string
	Version      string
	BundleDigest string
	ArtifactURL  string
	ManifestJSON string
	Status       string
	CreatedBy    *string
}

type UploadSkillReleaseRequest struct {
	SkillID   string
	Version   string
	BundleRaw []byte
	Status    string
	CreatedBy *string
}

type SkillRef struct {
	SkillID      string   `json:"skill_id"`
	Version      string   `json:"version"`
	Capability   string   `json:"capability"`
	Role         string   `json:"role,omitempty"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
	Priority     *int     `json:"priority,omitempty"`
	Enabled      *bool    `json:"enabled,omitempty"`
}

type SkillsetItem struct {
	SkillsetName string      `json:"skillset_name"`
	Skills       []*SkillRef `json:"skills"`
}

type SkillsetConfigView struct {
	PipelineID string          `json:"pipeline_id"`
	Items      []*SkillsetItem `json:"items"`
	Source     string          `json:"source"`
}

type SkillReleaseView struct {
	SkillID      string `json:"skill_id"`
	Version      string `json:"version"`
	BundleDigest string `json:"bundle_digest"`
	ArtifactURL  string `json:"artifact_url"`
	ManifestJSON string `json:"manifest_json,omitempty"`
	Status       string `json:"status"`
	CreatedBy    string `json:"created_by,omitempty"`
	Source       string `json:"source"`
}

func (b *configBiz) GetSkillsets(ctx context.Context, rq *GetSkillsetConfigRequest) (*SkillsetConfigView, error) {
	if rq == nil || b == nil || b.store == nil {
		return nil, errno.ErrInvalidArgument
	}
	pipelineID := strings.ToLower(strings.TrimSpace(rq.PipelineID))
	if pipelineID == "" {
		return nil, errno.ErrInvalidArgument
	}
	items, source, err := b.ResolveSkillsetByPipeline(ctx, pipelineID)
	if err != nil {
		if errorsx.Is(err, errno.ErrNotFound) {
			return &SkillsetConfigView{PipelineID: pipelineID, Items: []*SkillsetItem{}, Source: "empty"}, nil
		}
		return nil, errno.ErrInternal
	}
	return &SkillsetConfigView{PipelineID: pipelineID, Items: items, Source: source}, nil
}

func (b *configBiz) UpsertSkillset(ctx context.Context, rq *UpsertSkillsetConfigRequest) (*SkillsetConfigView, error) {
	if rq == nil || b == nil || b.store == nil {
		return nil, errno.ErrInvalidArgument
	}
	pipelineID := strings.ToLower(strings.TrimSpace(rq.PipelineID))
	skillsetName := strings.TrimSpace(rq.SkillsetName)
	skills := normalizeSkillRefs(rq.Skills)
	if pipelineID == "" || skillsetName == "" || len(skills) == 0 {
		return nil, errno.ErrInvalidArgument
	}
	rawSkills, _ := json.Marshal(skills)
	rawSkillsString := string(rawSkills)
	obj := &model.SkillsetConfigDynamicM{
		PipelineID:    pipelineID,
		SkillsetName:  skillsetName,
		SkillRefsJSON: &rawSkillsString,
	}
	if err := b.store.InternalStrategyConfig().UpsertSkillsetConfig(ctx, obj); err != nil {
		return nil, errno.ErrInternal
	}
	return b.GetSkillsets(ctx, &GetSkillsetConfigRequest{PipelineID: pipelineID})
}

func (b *configBiz) GetSkillRelease(ctx context.Context, rq *GetSkillReleaseRequest) (*SkillReleaseView, error) {
	if rq == nil || b == nil || b.store == nil {
		return nil, errno.ErrInvalidArgument
	}
	skillID := strings.TrimSpace(rq.SkillID)
	version := strings.TrimSpace(rq.Version)
	if skillID == "" || version == "" {
		return nil, errno.ErrInvalidArgument
	}
	obj, err := b.store.InternalStrategyConfig().GetSkillRelease(ctx, skillID, version)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrNotFound
		}
		return nil, errno.ErrInternal
	}
	return mapSkillReleaseModel(obj, "dynamic_db"), nil
}

func (b *configBiz) RegisterSkillRelease(
	ctx context.Context,
	rq *RegisterSkillReleaseRequest,
) (*SkillReleaseView, error) {
	if rq == nil || b == nil || b.store == nil {
		return nil, errno.ErrInvalidArgument
	}
	skillID := strings.TrimSpace(rq.SkillID)
	version := strings.TrimSpace(rq.Version)
	bundleDigest := strings.TrimSpace(rq.BundleDigest)
	artifactURL := strings.TrimSpace(rq.ArtifactURL)
	manifestJSON := strings.TrimSpace(rq.ManifestJSON)
	status := strings.ToLower(strings.TrimSpace(rq.Status))
	if skillID == "" || version == "" || bundleDigest == "" || artifactURL == "" || manifestJSON == "" {
		return nil, errno.ErrInvalidArgument
	}
	if status == "" {
		status = "active"
	}
	if err := validateSkillSummaryEnvelope(manifestJSON); err != nil {
		return nil, errno.ErrInvalidArgument
	}
	obj := &model.SkillReleaseM{
		SkillID:      skillID,
		Version:      version,
		BundleDigest: bundleDigest,
		ArtifactURL:  artifactURL,
		ManifestJSON: &manifestJSON,
		Status:       status,
		CreatedBy:    trimStringPtr(rq.CreatedBy),
	}
	if err := b.store.InternalStrategyConfig().UpsertSkillRelease(ctx, obj); err != nil {
		return nil, errno.ErrInternal
	}
	return b.GetSkillRelease(ctx, &GetSkillReleaseRequest{SkillID: skillID, Version: version})
}

func (b *configBiz) UploadSkillRelease(
	ctx context.Context,
	rq *UploadSkillReleaseRequest,
) (*SkillReleaseView, error) {
	if rq == nil || b == nil || b.store == nil {
		return nil, errno.ErrInvalidArgument
	}
	skillID := strings.TrimSpace(rq.SkillID)
	version := strings.TrimSpace(rq.Version)
	if skillID == "" || version == "" {
		return nil, errno.ErrInvalidArgument
	}
	manifestJSON, err := skillartifact.ReadBundleSkillSummaryJSON(rq.BundleRaw)
	if err != nil {
		return nil, errno.ErrInvalidArgument
	}
	if err := validateSkillSummaryEnvelope(manifestJSON); err != nil {
		return nil, errno.ErrInvalidArgument
	}
	artifactRef, bundleDigest, err := skillartifact.UploadBundle(ctx, skillID, version, rq.BundleRaw)
	if err != nil {
		if errors.Is(err, skillartifact.ErrStorageDisabled) || errors.Is(err, skillartifact.ErrUploadModeDisabled) {
			return nil, errno.ErrOperationFailed
		}
		return nil, errno.ErrInternal
	}
	return b.RegisterSkillRelease(ctx, &RegisterSkillReleaseRequest{
		SkillID:      skillID,
		Version:      version,
		BundleDigest: bundleDigest,
		ArtifactURL:  artifactRef,
		ManifestJSON: manifestJSON,
		Status:       rq.Status,
		CreatedBy:    rq.CreatedBy,
	})
}

func (b *configBiz) ResolveSkillsetByPipeline(
	ctx context.Context,
	pipelineID string,
) ([]*SkillsetItem, string, error) {
	if b == nil || b.store == nil {
		return nil, "", errno.ErrInvalidArgument
	}
	pipelineID = strings.ToLower(strings.TrimSpace(pipelineID))
	if pipelineID == "" {
		return nil, "", errno.ErrInvalidArgument
	}
	configStore := b.store.InternalStrategyConfig()
	if configStore == nil {
		return nil, "", errno.ErrNotFound
	}
	list, err := configStore.ListSkillsetConfigsByPipeline(ctx, pipelineID)
	if err != nil {
		return nil, "", err
	}
	mapped := mapSkillsetModelList(list)
	if len(mapped) == 0 {
		return nil, "", errno.ErrNotFound
	}
	return mapped, "dynamic_db", nil
}

func mapSkillsetModelList(in []*model.SkillsetConfigDynamicM) []*SkillsetItem {
	if len(in) == 0 {
		return nil
	}
	out := make([]*SkillsetItem, 0, len(in))
	for _, item := range in {
		if item == nil {
			continue
		}
		skills := decodeJSONSkillRefs(item.SkillRefsJSON)
		if len(skills) == 0 {
			continue
		}
		out = append(out, &SkillsetItem{
			SkillsetName: strings.TrimSpace(item.SkillsetName),
			Skills:       skills,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func mapSkillReleaseModel(obj *model.SkillReleaseM, source string) *SkillReleaseView {
	if obj == nil {
		return nil
	}
	return &SkillReleaseView{
		SkillID:      strings.TrimSpace(obj.SkillID),
		Version:      strings.TrimSpace(obj.Version),
		BundleDigest: strings.TrimSpace(obj.BundleDigest),
		ArtifactURL:  strings.TrimSpace(obj.ArtifactURL),
		ManifestJSON: strings.TrimSpace(ptrString(obj.ManifestJSON)),
		Status:       strings.TrimSpace(obj.Status),
		CreatedBy:    strings.TrimSpace(ptrString(obj.CreatedBy)),
		Source:       source,
	}
}

type GetSLAConfigRequest struct {
	SessionType string
}

type UpsertSLAConfigRequest struct {
	SessionType          string
	DueSeconds           int64
	EscalationThresholds []int64
}

type SLAConfigView struct {
	SessionType          string  `json:"session_type"`
	DueSeconds           int64   `json:"due_seconds"`
	EscalationThresholds []int64 `json:"escalation_thresholds"`
	Source               string  `json:"source"`
}

func (b *configBiz) GetSLA(ctx context.Context, rq *GetSLAConfigRequest) (*SLAConfigView, error) {
	if rq == nil || b == nil || b.store == nil {
		return nil, errno.ErrInvalidArgument
	}
	sessionType := strings.ToLower(strings.TrimSpace(rq.SessionType))
	if sessionType == "" {
		return nil, errno.ErrInvalidArgument
	}
	cfg, source, err := b.ResolveSLABySessionType(ctx, sessionType)
	if err != nil {
		return nil, err
	}
	cfg.Source = source
	return cfg, nil
}

func (b *configBiz) UpsertSLA(ctx context.Context, rq *UpsertSLAConfigRequest) (*SLAConfigView, error) {
	if rq == nil || b == nil || b.store == nil {
		return nil, errno.ErrInvalidArgument
	}
	sessionType := strings.ToLower(strings.TrimSpace(rq.SessionType))
	dueSeconds := rq.DueSeconds
	if sessionType == "" || dueSeconds <= 0 {
		return nil, errno.ErrInvalidArgument
	}
	thresholds := normalizeInt64List(rq.EscalationThresholds)
	if len(thresholds) == 0 {
		thresholds = []int64{dueSeconds, dueSeconds * 2}
	}
	rawThresholds, _ := json.Marshal(thresholds)
	rawThresholdsString := string(rawThresholds)
	obj := &model.SLAEscalationConfigM{
		SessionType:              sessionType,
		DueSeconds:               dueSeconds,
		EscalationThresholdsJSON: &rawThresholdsString,
	}
	if err := b.store.InternalStrategyConfig().UpsertSLAEscalationConfig(ctx, obj); err != nil {
		return nil, errno.ErrInternal
	}
	cfg, source, err := b.ResolveSLABySessionType(ctx, sessionType)
	if err != nil {
		return nil, err
	}
	cfg.Source = source
	return cfg, nil
}

func (b *configBiz) ResolveSLABySessionType(
	ctx context.Context,
	sessionType string,
) (*SLAConfigView, string, error) {
	if b == nil || b.store == nil {
		return nil, "", errno.ErrInvalidArgument
	}
	sessionType = strings.ToLower(strings.TrimSpace(sessionType))
	if sessionType == "" {
		return nil, "", errno.ErrInvalidArgument
	}
	if configStore := b.store.InternalStrategyConfig(); configStore != nil {
		obj, err := configStore.GetSLAEscalationConfig(ctx, sessionType)
		if err == nil && obj != nil {
			thresholds := decodeJSONInt64List(obj.EscalationThresholdsJSON)
			if len(thresholds) == 0 {
				thresholds = []int64{obj.DueSeconds, obj.DueSeconds * 2}
			}
			return &SLAConfigView{
				SessionType:          sessionType,
				DueSeconds:           maxInt64(obj.DueSeconds, 1),
				EscalationThresholds: thresholds,
			}, "dynamic_db", nil
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", errno.ErrInternal
		}
	}
	return &SLAConfigView{
		SessionType:          sessionType,
		DueSeconds:           defaultSLADueSeconds,
		EscalationThresholds: []int64{defaultSLADueSeconds, defaultEscalatedThresholdSec},
	}, "static_fallback", nil
}

type GetSessionAssignmentRequest struct {
	SessionID string
}

type AssignSessionRequest struct {
	SessionID  string
	Assignee   string
	AssignedBy string
	Note       *string
	AssignedAt *time.Time
}

type SessionAssignmentView struct {
	SessionID  string `json:"session_id"`
	Assignee   string `json:"assignee,omitempty"`
	AssignedBy string `json:"assigned_by,omitempty"`
	AssignedAt string `json:"assigned_at,omitempty"`
	Note       string `json:"note,omitempty"`
	Source     string `json:"source"`
}

func (b *configBiz) GetSessionAssignment(
	ctx context.Context,
	rq *GetSessionAssignmentRequest,
) (*SessionAssignmentView, error) {
	if rq == nil || b == nil || b.store == nil || b.sessionBiz == nil {
		return nil, errno.ErrInvalidArgument
	}
	sessionID := strings.TrimSpace(rq.SessionID)
	if sessionID == "" {
		return nil, errno.ErrInvalidArgument
	}
	if configStore := b.store.InternalStrategyConfig(); configStore != nil {
		obj, err := configStore.GetSessionAssignment(ctx, sessionID)
		if err == nil && obj != nil {
			return mapSessionAssignmentModel(obj, "dynamic_db"), nil
		}
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrInternal
		}
	}
	resp, err := b.sessionBiz.Get(ctx, &sessionbiz.GetSessionContextRequest{SessionID: &sessionID})
	if err != nil || resp == nil || resp.Session == nil {
		if err != nil {
			return nil, err
		}
		return nil, errno.ErrNotFound
	}
	assignment := extractAssignmentFromSession(resp.Session)
	if assignment == nil {
		return &SessionAssignmentView{SessionID: sessionID, Source: "session_context"}, nil
	}
	return assignment, nil
}

func (b *configBiz) AssignSession(ctx context.Context, rq *AssignSessionRequest) (*SessionAssignmentView, error) {
	if rq == nil || b == nil || b.store == nil || b.sessionBiz == nil {
		return nil, errno.ErrInvalidArgument
	}
	sessionID := strings.TrimSpace(rq.SessionID)
	assignee := strings.TrimSpace(rq.Assignee)
	assignedBy := strings.TrimSpace(rq.AssignedBy)
	if sessionID == "" || assignee == "" {
		return nil, errno.ErrInvalidArgument
	}
	if assignedBy == "" {
		assignedBy = "operator:config_api"
	}
	updateResp, err := b.sessionBiz.UpdateAssignment(ctx, &sessionbiz.UpdateAssignmentRequest{
		SessionID:  sessionID,
		Assignee:   assignee,
		AssignedBy: &assignedBy,
		AssignNote: rq.Note,
		AssignedAt: rq.AssignedAt,
	})
	if err != nil {
		return nil, err
	}
	assignedAt := time.Now().UTC()
	if updateResp != nil && updateResp.Assignment != nil {
		if parsed, parseErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(updateResp.Assignment.AssignedAt)); parseErr == nil {
			assignedAt = parsed.UTC()
		}
	}
	assignmentNote := trimStringPtr(rq.Note)
	_ = b.store.InternalStrategyConfig().UpsertSessionAssignment(ctx, &model.SessionAssignmentM{
		SessionID:  sessionID,
		Assignee:   assignee,
		AssignedBy: assignedBy,
		AssignedAt: &assignedAt,
		Note:       assignmentNote,
	})
	return &SessionAssignmentView{
		SessionID:  sessionID,
		Assignee:   assignee,
		AssignedBy: assignedBy,
		AssignedAt: assignedAt.Format(time.RFC3339Nano),
		Note:       strings.TrimSpace(ptrString(assignmentNote)),
		Source:     "dynamic_db",
	}, nil
}

func (b *configBiz) ResolveTriggerPipeline(
	ctx context.Context,
	triggerType string,
) (pipelineID string, sessionType string, source string, err error) {
	cfg, getErr := b.GetTrigger(ctx, &GetTriggerConfigRequest{TriggerType: triggerType})
	if getErr != nil {
		return "", "", "", getErr
	}
	return strings.TrimSpace(cfg.PipelineID), strings.TrimSpace(cfg.SessionType), strings.TrimSpace(cfg.Source), nil
}

func mapSessionAssignmentModel(obj *model.SessionAssignmentM, source string) *SessionAssignmentView {
	if obj == nil {
		return nil
	}
	assignedAt := ""
	if obj.AssignedAt != nil {
		assignedAt = obj.AssignedAt.UTC().Format(time.RFC3339Nano)
	}
	return &SessionAssignmentView{
		SessionID:  strings.TrimSpace(obj.SessionID),
		Assignee:   strings.TrimSpace(obj.Assignee),
		AssignedBy: strings.TrimSpace(obj.AssignedBy),
		AssignedAt: assignedAt,
		Note:       strings.TrimSpace(ptrString(obj.Note)),
		Source:     source,
	}
}

func extractAssignmentFromSession(sessionObj *model.SessionContextM) *SessionAssignmentView {
	if sessionObj == nil || sessionObj.ContextStateJSON == nil {
		return nil
	}
	var contextState map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(ptrString(sessionObj.ContextStateJSON))), &contextState); err != nil {
		return nil
	}
	assignmentRaw, ok := contextState["assignment"]
	if !ok {
		return nil
	}
	assignmentObj, ok := assignmentRaw.(map[string]any)
	if !ok {
		return nil
	}
	return &SessionAssignmentView{
		SessionID:  strings.TrimSpace(sessionObj.SessionID),
		Assignee:   strings.TrimSpace(anyToString(assignmentObj["assignee"])),
		AssignedBy: strings.TrimSpace(anyToString(assignmentObj["assigned_by"])),
		AssignedAt: strings.TrimSpace(anyToString(assignmentObj["assigned_at"])),
		Note:       strings.TrimSpace(anyToString(assignmentObj["note"])),
		Source:     "session_context",
	}
}

func decodeJSONStringList(raw *string) []string {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(*raw), &out); err != nil {
		return nil
	}
	return normalizeListLower(out)
}

func decodeJSONInt64List(raw *string) []int64 {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil
	}
	var out []int64
	if err := json.Unmarshal([]byte(*raw), &out); err != nil {
		return nil
	}
	return normalizeInt64List(out)
}

func decodeJSONSkillRefs(raw *string) []*SkillRef {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil
	}
	var out []*SkillRef
	if err := json.Unmarshal([]byte(*raw), &out); err != nil {
		return nil
	}
	return normalizeSkillRefs(out)
}

func normalizeInt64List(in []int64) []int64 {
	if len(in) == 0 {
		return nil
	}
	out := make([]int64, 0, len(in))
	seen := map[int64]struct{}{}
	for _, item := range in {
		if item <= 0 {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeSkillRefs(in []*SkillRef) []*SkillRef {
	if len(in) == 0 {
		return nil
	}
	out := make([]*SkillRef, 0, len(in))
	seen := map[string]struct{}{}
	for _, item := range in {
		if item == nil {
			continue
		}
		skillID := strings.TrimSpace(item.SkillID)
		version := strings.TrimSpace(item.Version)
		capability := strings.TrimSpace(item.Capability)
		role := normalizeSkillRole(item.Role)
		if skillID == "" || version == "" || capability == "" {
			continue
		}
		priority := 100
		if item.Priority != nil && *item.Priority > 0 {
			priority = *item.Priority
		}
		enabled := true
		if item.Enabled != nil {
			enabled = *item.Enabled
		}
		allowedTools := normalizeListLower(item.AllowedTools)
		key := skillID + "\x00" + version + "\x00" + capability + "\x00" + role
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, &SkillRef{
			SkillID:      skillID,
			Version:      version,
			Capability:   capability,
			Role:         role,
			AllowedTools: allowedTools,
			Priority:     intPtr(priority),
			Enabled:      boolPtr(enabled),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeSkillRole(in string) string {
	switch strings.ToLower(strings.TrimSpace(in)) {
	case "", "executor":
		return "executor"
	case "knowledge":
		return "knowledge"
	default:
		return "executor"
	}
}

func normalizeListLower(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, item := range in {
		value := strings.ToLower(strings.TrimSpace(item))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
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

func emptyStringToNil(in string) *string {
	value := strings.TrimSpace(in)
	if value == "" {
		return nil
	}
	return &value
}

func ptrString(in *string) string {
	if in == nil {
		return ""
	}
	return *in
}

func anyToString(value any) string {
	switch in := value.(type) {
	case string:
		return in
	default:
		return ""
	}
}

func validateSkillSummaryEnvelope(raw string) error {
	if _, err := skillartifact.ValidateSkillSummaryEnvelopeJSON(raw); err != nil {
		return errno.ErrInvalidArgument
	}
	return nil
}

func maxInt64(value int64, fallback int64) int64 {
	if value <= 0 {
		return fallback
	}
	return value
}

func intPtr(value int) *int {
	out := value
	return &out
}

func boolPtr(value bool) *bool {
	out := value
	return &out
}
