package internalstrategycfg

import "strings"

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

func BuiltinPipelineDefaults() []PipelineDefault {
	return normalizePipelineDefaults(builtinPipelineDefaults)
}

func FindBuiltinPipelineDefault(alertSource string, service string, namespace string) *PipelineDefault {
	list := BuiltinPipelineDefaults()
	alertSource = strings.TrimSpace(alertSource)
	service = strings.TrimSpace(service)
	namespace = strings.TrimSpace(namespace)
	for _, item := range list {
		if item.AlertSource == alertSource && item.Service == service && item.Namespace == namespace {
			out := item
			return &out
		}
	}
	for _, item := range list {
		if item.AlertSource == alertSource && item.Service == service && item.Namespace == "" {
			out := item
			return &out
		}
	}
	for _, item := range list {
		if item.AlertSource == alertSource && item.Service == "" && item.Namespace == "" {
			out := item
			return &out
		}
	}
	return nil
}

func BuiltinTriggerDefaults() []TriggerDefault {
	return normalizeTriggerDefaults(builtinTriggerDefaults)
}

func FindBuiltinTriggerDefault(triggerType string) *TriggerDefault {
	list := BuiltinTriggerDefaults()
	triggerType = strings.ToLower(strings.TrimSpace(triggerType))
	for _, item := range list {
		if item.TriggerType == triggerType {
			out := item
			return &out
		}
	}
	return nil
}

func BuiltinToolsetDefaults() []ToolsetDefault {
	return normalizeToolsetDefaults(builtinToolsetDefaults)
}

func ListBuiltinToolsetDefaultsByPipeline(pipelineID string) []ToolsetDefault {
	list := BuiltinToolsetDefaults()
	pipelineID = strings.ToLower(strings.TrimSpace(pipelineID))
	out := make([]ToolsetDefault, 0, len(list))
	for _, item := range list {
		if item.PipelineID != pipelineID {
			continue
		}
		out = append(out, item)
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
