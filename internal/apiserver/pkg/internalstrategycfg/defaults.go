package internalstrategycfg

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	envDefaultPipelineConfigPath = "RCA_DEFAULT_PIPELINE_CONFIG_PATH"
	envDefaultToolsetConfigPath  = "RCA_DEFAULT_TOOLSET_CONFIG_PATH"
	envDefaultTriggerConfigPath  = "RCA_DEFAULT_TRIGGER_CONFIG_PATH"
	envCacheConfigPath           = "RCA_CACHE_CONFIG_PATH"

	defaultPipelineConfigPath = "configs/default_pipeline.yml"
	defaultToolsetConfigPath  = "configs/default_toolset.yml"
	defaultTriggerConfigPath  = "configs/default_trigger.yml"
	defaultCacheConfigPath    = "configs/cache_config.yml"
)

type PipelineDefault struct {
	AlertSource string   `json:"alert_source" yaml:"alert_source"`
	Service     string   `json:"service" yaml:"service"`
	Namespace   string   `json:"namespace" yaml:"namespace"`
	PipelineID  string   `json:"pipeline_id" yaml:"pipeline_id"`
	GraphID     string   `json:"graph_id" yaml:"graph_id"`
	ToolsetList []string `json:"toolset_list" yaml:"toolset_list"`
}

type TriggerDefault struct {
	TriggerType string `json:"trigger_type" yaml:"trigger_type"`
	PipelineID  string `json:"pipeline_id" yaml:"pipeline_id"`
	SessionType string `json:"session_type" yaml:"session_type"`
	Fallback    bool   `json:"fallback" yaml:"fallback"`
}

type ToolsetDefault struct {
	PipelineID   string   `json:"pipeline_id" yaml:"pipeline_id"`
	ToolsetName  string   `json:"toolset_name" yaml:"toolset_name"`
	AllowedTools []string `json:"allowed_tools" yaml:"allowed_tools"`
}

type CacheConfig struct {
	InboxTTLSeconds        int64 `json:"inbox_ttl_seconds" yaml:"inbox_ttl_seconds"`
	WorkbenchTTLSeconds    int64 `json:"workbench_ttl_seconds" yaml:"workbench_ttl_seconds"`
	DashboardTTLSeconds    int64 `json:"dashboard_ttl_seconds" yaml:"dashboard_ttl_seconds"`
	TraceTTLSeconds        int64 `json:"trace_ttl_seconds" yaml:"trace_ttl_seconds"`
	CompareTTLSeconds      int64 `json:"compare_ttl_seconds" yaml:"compare_ttl_seconds"`
	HistoryTTLSeconds      int64 `json:"history_ttl_seconds" yaml:"history_ttl_seconds"`
	SessionStateTTLSeconds int64 `json:"session_state_ttl_seconds" yaml:"session_state_ttl_seconds"`
	BatchSize              int64 `json:"batch_size" yaml:"batch_size"`
	MaxDelete              int64 `json:"max_delete" yaml:"max_delete"`
}

var builtinPipelineDefaults = []PipelineDefault{
	{
		AlertSource: "alert",
		PipelineID:  "basic_rca",
		GraphID:     "basic_rca",
		ToolsetList: []string{"canonical_default"},
	},
	{
		AlertSource: "manual",
		PipelineID:  "basic_rca",
		GraphID:     "basic_rca",
		ToolsetList: []string{"canonical_default"},
	},
}

var builtinTriggerDefaults = []TriggerDefault{
	{TriggerType: "alert", PipelineID: "basic_rca", SessionType: "incident", Fallback: true},
	{TriggerType: "manual", PipelineID: "basic_rca", SessionType: "incident", Fallback: true},
	{TriggerType: "replay", PipelineID: "basic_rca", SessionType: "incident", Fallback: true},
	{TriggerType: "follow_up", PipelineID: "basic_rca", SessionType: "incident", Fallback: true},
	{TriggerType: "cron", PipelineID: "basic_rca", SessionType: "service", Fallback: true},
	{TriggerType: "change", PipelineID: "basic_rca", SessionType: "change", Fallback: true},
}

var builtinToolsetDefaults = []ToolsetDefault{
	{
		PipelineID:  "basic_rca",
		ToolsetName: "canonical_default",
		AllowedTools: []string{
			"metrics.query_range",
			"logs.search",
			"k8s.get_objects",
		},
	},
}

func LoadPipelineDefaults() ([]PipelineDefault, error) {
	var payload struct {
		Pipelines []PipelineDefault `json:"pipelines" yaml:"pipelines"`
	}
	if err := decodeOptionalConfigFile(resolveConfigPath(envDefaultPipelineConfigPath, defaultPipelineConfigPath), &payload); err != nil {
		return nil, err
	}
	if len(payload.Pipelines) == 0 {
		return normalizePipelineDefaults(builtinPipelineDefaults), nil
	}
	return normalizePipelineDefaults(payload.Pipelines), nil
}

func LoadTriggerDefaults() ([]TriggerDefault, error) {
	var payload struct {
		Triggers []TriggerDefault `json:"triggers" yaml:"triggers"`
	}
	if err := decodeOptionalConfigFile(resolveConfigPath(envDefaultTriggerConfigPath, defaultTriggerConfigPath), &payload); err != nil {
		return nil, err
	}
	if len(payload.Triggers) == 0 {
		return normalizeTriggerDefaults(builtinTriggerDefaults), nil
	}
	return normalizeTriggerDefaults(payload.Triggers), nil
}

func LoadToolsetDefaults() ([]ToolsetDefault, error) {
	var payload struct {
		Toolsets []ToolsetDefault `json:"toolsets" yaml:"toolsets"`
	}
	if err := decodeOptionalConfigFile(resolveConfigPath(envDefaultToolsetConfigPath, defaultToolsetConfigPath), &payload); err != nil {
		return nil, err
	}
	if len(payload.Toolsets) == 0 {
		return normalizeToolsetDefaults(builtinToolsetDefaults), nil
	}
	return normalizeToolsetDefaults(payload.Toolsets), nil
}

func LoadCacheConfig() (*CacheConfig, error) {
	payload := &CacheConfig{}
	if err := decodeOptionalConfigFile(resolveConfigPath(envCacheConfigPath, defaultCacheConfigPath), payload); err != nil {
		return nil, err
	}
	if payload.InboxTTLSeconds == 0 &&
		payload.WorkbenchTTLSeconds == 0 &&
		payload.DashboardTTLSeconds == 0 &&
		payload.TraceTTLSeconds == 0 &&
		payload.CompareTTLSeconds == 0 &&
		payload.HistoryTTLSeconds == 0 &&
		payload.SessionStateTTLSeconds == 0 &&
		payload.BatchSize == 0 &&
		payload.MaxDelete == 0 {
		return nil, nil
	}
	return payload, nil
}

func FindPipelineDefault(alertSource string, service string, namespace string) (*PipelineDefault, error) {
	list, err := LoadPipelineDefaults()
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, nil
	}
	alertSource = strings.TrimSpace(alertSource)
	service = strings.TrimSpace(service)
	namespace = strings.TrimSpace(namespace)
	for _, item := range list {
		if item.AlertSource == alertSource && item.Service == service && item.Namespace == namespace {
			out := item
			return &out, nil
		}
	}
	for _, item := range list {
		if item.AlertSource == alertSource && item.Service == service && item.Namespace == "" {
			out := item
			return &out, nil
		}
	}
	for _, item := range list {
		if item.AlertSource == alertSource && item.Service == "" && item.Namespace == "" {
			out := item
			return &out, nil
		}
	}
	return nil, nil
}

func FindTriggerDefault(triggerType string) (*TriggerDefault, error) {
	list, err := LoadTriggerDefaults()
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, nil
	}
	triggerType = strings.ToLower(strings.TrimSpace(triggerType))
	for _, item := range list {
		if item.TriggerType == triggerType {
			out := item
			return &out, nil
		}
	}
	return nil, nil
}

func ListToolsetDefaultsByPipeline(pipelineID string) ([]ToolsetDefault, error) {
	list, err := LoadToolsetDefaults()
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, nil
	}
	pipelineID = strings.ToLower(strings.TrimSpace(pipelineID))
	out := make([]ToolsetDefault, 0, len(list))
	for _, item := range list {
		if item.PipelineID != pipelineID {
			continue
		}
		out = append(out, item)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func resolveConfigPath(envKey string, defaultPath string) string {
	if envPath := strings.TrimSpace(os.Getenv(envKey)); envPath != "" {
		return envPath
	}
	return defaultPath
}

func decodeOptionalConfigFile(path string, out any) error {
	path = strings.TrimSpace(path)
	if path == "" || out == nil {
		return nil
	}
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	return yaml.Unmarshal(raw, out)
}

func normalizePipelineDefaults(in []PipelineDefault) []PipelineDefault {
	out := make([]PipelineDefault, 0, len(in))
	for _, item := range in {
		item.AlertSource = strings.TrimSpace(item.AlertSource)
		item.Service = strings.TrimSpace(item.Service)
		item.Namespace = strings.TrimSpace(item.Namespace)
		item.PipelineID = strings.ToLower(strings.TrimSpace(item.PipelineID))
		item.GraphID = strings.TrimSpace(item.GraphID)
		item.ToolsetList = normalizeList(item.ToolsetList, false)
		if item.AlertSource == "" || item.PipelineID == "" {
			continue
		}
		out = append(out, item)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeTriggerDefaults(in []TriggerDefault) []TriggerDefault {
	out := make([]TriggerDefault, 0, len(in))
	for _, item := range in {
		item.TriggerType = strings.ToLower(strings.TrimSpace(item.TriggerType))
		item.PipelineID = strings.ToLower(strings.TrimSpace(item.PipelineID))
		item.SessionType = strings.ToLower(strings.TrimSpace(item.SessionType))
		if item.TriggerType == "" || item.PipelineID == "" {
			continue
		}
		out = append(out, item)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeToolsetDefaults(in []ToolsetDefault) []ToolsetDefault {
	out := make([]ToolsetDefault, 0, len(in))
	for _, item := range in {
		item.PipelineID = strings.ToLower(strings.TrimSpace(item.PipelineID))
		item.ToolsetName = strings.TrimSpace(item.ToolsetName)
		item.AllowedTools = normalizeList(item.AllowedTools, true)
		if item.PipelineID == "" || item.ToolsetName == "" || len(item.AllowedTools) == 0 {
			continue
		}
		out = append(out, item)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeList(in []string, lower bool) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, item := range in {
		value := strings.TrimSpace(item)
		if lower {
			value = strings.ToLower(value)
		}
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
