package model

import (
	"time"
)

const TableNameIncidentM = "incidents"

// IncidentM RCA 事件单（Incident）：告警聚合后的事件元数据 + 诊断/处置闭环的核心索引表
type IncidentM struct {
	ID           int64   `json:"id" gorm:"column:id;primaryKey;autoIncrement:true"`
	IncidentID   string  `json:"incident_id" gorm:"column:incident_id;type:varchar(64);uniqueIndex;not null;comment:事件单ID（自增主键）"`                                 // 事件单ID（自增主键）
	TenantID     string  `json:"tenant_id" gorm:"column:tenant_id;not null;default:default;comment:租户/业务线标识（多租户预留）"`                                              // 租户/业务线标识（多租户预留）
	Cluster      string  `json:"cluster" gorm:"column:cluster;not null;default:default;comment:集群标识（如 prod-cn1 / staging-us1）"`                                   // 集群标识（如 prod-cn1 / staging-us1）
	Namespace    string  `json:"namespace" gorm:"column:namespace;not null;comment:K8s 命名空间"`                                                                     // K8s 命名空间
	WorkloadKind string  `json:"workload_kind" gorm:"column:workload_kind;not null;default:Deployment;comment:工作负载类型（Deployment/StatefulSet/DaemonSet/Job/Pod等）"` // 工作负载类型（Deployment/StatefulSet/DaemonSet/Job/Pod等）
	WorkloadName string  `json:"workload_name" gorm:"column:workload_name;not null;comment:工作负载名称（如 demo、db、nginx-gw）"`                                           // 工作负载名称（如 demo、db、nginx-gw）
	Pod          *string `json:"pod" gorm:"column:pod;comment:Pod 名称（定位到具体实例，可为空）"`                                                                               // Pod 名称（定位到具体实例，可为空）
	Node         *string `json:"node" gorm:"column:node;comment:Node 名称（节点级问题定位，可为空）"`                                                                            // Node 名称（节点级问题定位，可为空）
	Service      string  `json:"service" gorm:"column:service;not null;comment:服务名（如 springboot-dubbo 服务；用于聚合、看板、SLO维度）"`                                         // 服务名（如 springboot-dubbo 服务；用于聚合、看板、SLO维度）
	Environment  string  `json:"environment" gorm:"column:environment;not null;default:prod;comment:环境（prod/staging/dev 等）"`                                      // 环境（prod/staging/dev 等）
	Version      *string `json:"version" gorm:"column:version;comment:版本/发布标识（如 git sha、镜像 tag、release 号）"`                                                       // 版本/发布标识（如 git sha、镜像 tag、release 号）
	Source       string  `json:"source" gorm:"column:source;not null;default:alertmanager;comment:事件来源（alertmanager/grafana/manual/api 等）"`                       // 事件来源（alertmanager/grafana/manual/api 等）
	AlertName    *string `json:"alertname" gorm:"column:alertname;comment:告警名称（Alertmanager alertname）"`                                                          // 告警名称（Alertmanager alertname）
	Fingerprint  *string `json:"fingerprint" gorm:"column:fingerprint;comment:告警聚合指纹（同类告警合并用，建议用 hash）"`                                                          // 告警聚合指纹（同类告警合并用，建议用 hash）
	// active_fingerprint_key is nullable and uniquely indexed:
	// one fingerprint can only bind one non-closed incident in P0 strategy A.
	ActiveFingerprintKey *string    `json:"active_fingerprint_key" gorm:"column:active_fingerprint_key;type:varchar(128);uniqueIndex:uniq_incidents_active_fingerprint_key;comment:P0策略A-进行中incident绑定key"`
	RuleID               *string    `json:"rule_id" gorm:"column:rule_id;comment:告警规则ID（来自告警系统或自研规则库）"`                                                                 // 告警规则ID（来自告警系统或自研规则库）
	LabelsJSON           *string    `json:"labels_json" gorm:"column:labels_json;comment:告警 labels 原始 JSON（便于回放与诊断；可包含 instance、job、pod、container 等）"`                  // 告警 labels 原始 JSON（便于回放与诊断；可包含 instance、job、pod、container 等）
	AnnotationsJSON      *string    `json:"annotations_json" gorm:"column:annotations_json;comment:告警 annotations 原始 JSON（包含 summary/description/runbook 等）"`           // 告警 annotations 原始 JSON（包含 summary/description/runbook 等）
	Severity             string     `json:"severity" gorm:"column:severity;not null;comment:严重级别（P0/P1/P2 或 critical/warn/info）"`                                       // 严重级别（P0/P1/P2 或 critical/warn/info）
	Status               string     `json:"status" gorm:"column:status;not null;default:open;comment:事件状态（open/ack/mitigating/closed/silenced 等）"`                      // 事件状态（open/ack/mitigating/closed/silenced 等）
	StartAt              *time.Time `json:"start_at" gorm:"column:start_at;comment:事件开始时间（告警首次触发/事件窗口开始）"`                                                              // 事件开始时间（告警首次触发/事件窗口开始）
	EndAt                *time.Time `json:"end_at" gorm:"column:end_at;comment:事件结束时间（恢复/关闭时间）"`                                                                        // 事件结束时间（恢复/关闭时间）
	RCAStatus            string     `json:"rca_status" gorm:"column:rca_status;not null;default:pending;comment:RCA 状态（pending/running/done/failed）"`                   // RCA 状态（pending/running/done/failed）
	RootCauseType        *string    `json:"root_cause_type" gorm:"column:root_cause_type;comment:根因类型（如 capacity/network/dependency/code/config/release）"`              // 根因类型（如 capacity/network/dependency/code/config/release）
	RootCauseSummary     *string    `json:"root_cause_summary" gorm:"column:root_cause_summary;comment:根因摘要（一句话结论，便于列表检索）"`                                             // 根因摘要（一句话结论，便于列表检索）
	DiagnosisJSON        *string    `json:"diagnosis_json" gorm:"column:diagnosis_json;comment:诊断详情 JSON（多Agent 输出、证据引用、推理链路摘要等）"`                                      // 诊断详情 JSON（多Agent 输出、证据引用、推理链路摘要等）
	EvidenceRefsJSON     *string    `json:"evidence_refs_json" gorm:"column:evidence_refs_json;comment:证据引用 JSON（如 prom 查询、log 查询、trace 链接、K8s 对象快照的引用）"`               // 证据引用 JSON（如 prom 查询、log 查询、trace 链接、K8s 对象快照的引用）
	ActionStatus         string     `json:"action_status" gorm:"column:action_status;not null;default:none;comment:处置状态（none/proposed/approved/executing/done/failed）"` // 处置状态（none/proposed/approved/executing/done/failed）
	ActionSummary        *string    `json:"action_summary" gorm:"column:action_summary;comment:处置建议/执行摘要（如扩容、回滚、重启、熔断、限流）"`                                             // 处置建议/执行摘要（如扩容、回滚、重启、熔断、限流）
	TraceID              *string    `json:"trace_id" gorm:"column:trace_id;comment:关联 TraceID（如定位到某次请求链路，可为空）"`                                                         // 关联 TraceID（如定位到某次请求链路，可为空）
	LogTraceKey          *string    `json:"log_trace_key" gorm:"column:log_trace_key;comment:日志检索关键字/trace key（ELK/Tempo/自研日志平台）"`                                      // 日志检索关键字/trace key（ELK/Tempo/自研日志平台）
	ChangeID             *string    `json:"change_id" gorm:"column:change_id;comment:关联变更ID（如发布单/工单ID/CI/CD pipeline run id）"`                                          // 关联变更ID（如发布单/工单ID/CI/CD pipeline run id）
	CreatedBy            *string    `json:"created_by" gorm:"column:created_by;comment:创建人（预留：用户体系接入后填充）"`                                                              // 创建人（预留：用户体系接入后填充）
	ApprovedBy           *string    `json:"approved_by" gorm:"column:approved_by;comment:审批人（预留：处置需要审批时填充）"`                                                            // 审批人（预留：处置需要审批时填充）
	ClosedBy             *string    `json:"closed_by" gorm:"column:closed_by;comment:关闭人（预留：手动关闭时填充）"`                                                                  // 关闭人（预留：手动关闭时填充）
	CreatedAt            time.Time  `json:"created_at" gorm:"column:created_at;not null;default:CURRENT_TIMESTAMP;comment:创建时间（毫秒精度）"`                                  // 创建时间（毫秒精度）
	UpdatedAt            time.Time  `json:"updated_at" gorm:"column:updated_at;not null;default:CURRENT_TIMESTAMP;comment:更新时间（毫秒精度）"`                                  // 更新时间（毫秒精度）
}

// TableName IncidentM's table name
func (*IncidentM) TableName() string {
	return TableNameIncidentM
}
