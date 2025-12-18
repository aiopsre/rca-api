package model

import (
	"time"
)

const TableNameIncidentM = "incidents"

// IncidentM RCA 事件单（Incident）：告警聚合后的事件元数据 + 诊断/处置闭环的核心索引表
type IncidentM struct {
	ID           int64   `gorm:"column:id;primaryKey;autoIncrement:true" json:"id"`
	IncidentID   string  `gorm:"column:incident_id;uniqueIndex;not null;comment:事件单ID（自增主键）" json:"incident_id"`                                                  // 事件单ID（自增主键）
	TenantID     string  `gorm:"column:tenant_id;not null;default:default;comment:租户/业务线标识（多租户预留）" json:"tenant_id"`                                              // 租户/业务线标识（多租户预留）
	Cluster      string  `gorm:"column:cluster;not null;default:default;comment:集群标识（如 prod-cn1 / staging-us1）" json:"cluster"`                                   // 集群标识（如 prod-cn1 / staging-us1）
	Namespace    string  `gorm:"column:namespace;not null;comment:K8s 命名空间" json:"namespace"`                                                                     // K8s 命名空间
	WorkloadKind string  `gorm:"column:workload_kind;not null;default:Deployment;comment:工作负载类型（Deployment/StatefulSet/DaemonSet/Job/Pod等）" json:"workload_kind"` // 工作负载类型（Deployment/StatefulSet/DaemonSet/Job/Pod等）
	WorkloadName string  `gorm:"column:workload_name;not null;comment:工作负载名称（如 demo、db、nginx-gw）" json:"workload_name"`                                           // 工作负载名称（如 demo、db、nginx-gw）
	Pod          *string `gorm:"column:pod;comment:Pod 名称（定位到具体实例，可为空）" json:"pod"`                                                                               // Pod 名称（定位到具体实例，可为空）
	Node         *string `gorm:"column:node;comment:Node 名称（节点级问题定位，可为空）" json:"node"`                                                                            // Node 名称（节点级问题定位，可为空）
	Service      string  `gorm:"column:service;not null;comment:服务名（如 springboot-dubbo 服务；用于聚合、看板、SLO维度）" json:"service"`                                         // 服务名（如 springboot-dubbo 服务；用于聚合、看板、SLO维度）
	Environment  string  `gorm:"column:environment;not null;default:prod;comment:环境（prod/staging/dev 等）" json:"environment"`                                      // 环境（prod/staging/dev 等）
	Version      *string `gorm:"column:version;comment:版本/发布标识（如 git sha、镜像 tag、release 号）" json:"version"`                                                       // 版本/发布标识（如 git sha、镜像 tag、release 号）
	Source       string  `gorm:"column:source;not null;default:alertmanager;comment:事件来源（alertmanager/grafana/manual/api 等）" json:"source"`                       // 事件来源（alertmanager/grafana/manual/api 等）
	AlertName    *string `gorm:"column:alertname;comment:告警名称（Alertmanager alertname）" json:"alertname"`                                                          // 告警名称（Alertmanager alertname）
	Fingerprint  *string `gorm:"column:fingerprint;comment:告警聚合指纹（同类告警合并用，建议用 hash）" json:"fingerprint"`                                                          // 告警聚合指纹（同类告警合并用，建议用 hash）
	// active_fingerprint_key is nullable and uniquely indexed:
	// one fingerprint can only bind one non-closed incident in P0 strategy A.
	ActiveFingerprintKey *string    `gorm:"column:active_fingerprint_key;type:varchar(128);uniqueIndex:uniq_incidents_active_fingerprint_key;comment:P0策略A-进行中incident绑定key" json:"active_fingerprint_key"`
	RuleID               *string    `gorm:"column:rule_id;comment:告警规则ID（来自告警系统或自研规则库）" json:"rule_id"`                                                                 // 告警规则ID（来自告警系统或自研规则库）
	LabelsJSON           *string    `gorm:"column:labels_json;comment:告警 labels 原始 JSON（便于回放与诊断；可包含 instance、job、pod、container 等）" json:"labels_json"`                  // 告警 labels 原始 JSON（便于回放与诊断；可包含 instance、job、pod、container 等）
	AnnotationsJSON      *string    `gorm:"column:annotations_json;comment:告警 annotations 原始 JSON（包含 summary/description/runbook 等）" json:"annotations_json"`           // 告警 annotations 原始 JSON（包含 summary/description/runbook 等）
	Severity             string     `gorm:"column:severity;not null;comment:严重级别（P0/P1/P2 或 critical/warn/info）" json:"severity"`                                       // 严重级别（P0/P1/P2 或 critical/warn/info）
	Status               string     `gorm:"column:status;not null;default:open;comment:事件状态（open/ack/mitigating/closed/silenced 等）" json:"status"`                      // 事件状态（open/ack/mitigating/closed/silenced 等）
	StartAt              *time.Time `gorm:"column:start_at;comment:事件开始时间（告警首次触发/事件窗口开始）" json:"start_at"`                                                              // 事件开始时间（告警首次触发/事件窗口开始）
	EndAt                *time.Time `gorm:"column:end_at;comment:事件结束时间（恢复/关闭时间）" json:"end_at"`                                                                        // 事件结束时间（恢复/关闭时间）
	RCAStatus            string     `gorm:"column:rca_status;not null;default:pending;comment:RCA 状态（pending/running/done/failed）" json:"rca_status"`                   // RCA 状态（pending/running/done/failed）
	RootCauseType        *string    `gorm:"column:root_cause_type;comment:根因类型（如 capacity/network/dependency/code/config/release）" json:"root_cause_type"`              // 根因类型（如 capacity/network/dependency/code/config/release）
	RootCauseSummary     *string    `gorm:"column:root_cause_summary;comment:根因摘要（一句话结论，便于列表检索）" json:"root_cause_summary"`                                             // 根因摘要（一句话结论，便于列表检索）
	DiagnosisJSON        *string    `gorm:"column:diagnosis_json;comment:诊断详情 JSON（多Agent 输出、证据引用、推理链路摘要等）" json:"diagnosis_json"`                                      // 诊断详情 JSON（多Agent 输出、证据引用、推理链路摘要等）
	EvidenceRefsJSON     *string    `gorm:"column:evidence_refs_json;comment:证据引用 JSON（如 prom 查询、log 查询、trace 链接、K8s 对象快照的引用）" json:"evidence_refs_json"`               // 证据引用 JSON（如 prom 查询、log 查询、trace 链接、K8s 对象快照的引用）
	ActionStatus         string     `gorm:"column:action_status;not null;default:none;comment:处置状态（none/proposed/approved/executing/done/failed）" json:"action_status"` // 处置状态（none/proposed/approved/executing/done/failed）
	ActionSummary        *string    `gorm:"column:action_summary;comment:处置建议/执行摘要（如扩容、回滚、重启、熔断、限流）" json:"action_summary"`                                             // 处置建议/执行摘要（如扩容、回滚、重启、熔断、限流）
	TraceID              *string    `gorm:"column:trace_id;comment:关联 TraceID（如定位到某次请求链路，可为空）" json:"trace_id"`                                                         // 关联 TraceID（如定位到某次请求链路，可为空）
	LogTraceKey          *string    `gorm:"column:log_trace_key;comment:日志检索关键字/trace key（ELK/Tempo/自研日志平台）" json:"log_trace_key"`                                      // 日志检索关键字/trace key（ELK/Tempo/自研日志平台）
	ChangeID             *string    `gorm:"column:change_id;comment:关联变更ID（如发布单/工单ID/CI/CD pipeline run id）" json:"change_id"`                                          // 关联变更ID（如发布单/工单ID/CI/CD pipeline run id）
	CreatedBy            *string    `gorm:"column:created_by;comment:创建人（预留：用户体系接入后填充）" json:"created_by"`                                                              // 创建人（预留：用户体系接入后填充）
	ApprovedBy           *string    `gorm:"column:approved_by;comment:审批人（预留：处置需要审批时填充）" json:"approved_by"`                                                            // 审批人（预留：处置需要审批时填充）
	ClosedBy             *string    `gorm:"column:closed_by;comment:关闭人（预留：手动关闭时填充）" json:"closed_by"`                                                                  // 关闭人（预留：手动关闭时填充）
	CreatedAt            time.Time  `gorm:"column:created_at;not null;default:current_timestamp(3);comment:创建时间（毫秒精度）" json:"created_at"`                               // 创建时间（毫秒精度）
	UpdatedAt            time.Time  `gorm:"column:updated_at;not null;default:current_timestamp(3);comment:更新时间（毫秒精度）" json:"updated_at"`                               // 更新时间（毫秒精度）
}

// TableName IncidentM's table name
func (*IncidentM) TableName() string {
	return TableNameIncidentM
}
