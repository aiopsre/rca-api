CREATE TABLE incidents (
    -- 1) 主键与基础定位
                           id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
                           incident_id varchar(36) NOT NULL DEFAULT '' COMMENT '事件单ID（自增主键）',
                           tenant_id VARCHAR(64)  NOT NULL DEFAULT 'default' COMMENT '租户/业务线标识（多租户预留）',
                           cluster   VARCHAR(128) NOT NULL DEFAULT 'default' COMMENT '集群标识（如 prod-cn1 / staging-us1）',
                           namespace VARCHAR(128) NOT NULL COMMENT 'K8s 命名空间',
                           workload_kind VARCHAR(32)  NOT NULL DEFAULT 'Deployment' COMMENT '工作负载类型（Deployment/StatefulSet/DaemonSet/Job/Pod等）',
                           workload_name VARCHAR(256) NOT NULL COMMENT '工作负载名称（如 demo、db、nginx-gw）',
                           pod        VARCHAR(256) NULL COMMENT 'Pod 名称（定位到具体实例，可为空）',
                           node       VARCHAR(256) NULL COMMENT 'Node 名称（节点级问题定位，可为空）',

    -- 2) 服务维度（你微服务平台讲故事最关键）
                           service      VARCHAR(256) NOT NULL COMMENT '服务名（如 springboot-dubbo 服务；用于聚合、看板、SLO维度）',
                           environment  VARCHAR(64)  NOT NULL DEFAULT 'prod' COMMENT '环境（prod/staging/dev 等）',
                           version      VARCHAR(128) NULL COMMENT '版本/发布标识（如 git sha、镜像 tag、release 号）',

    -- 3) 告警来源与聚合指纹（接 Alertmanager/Grafana/自研告警）
                           source         VARCHAR(32)  NOT NULL DEFAULT 'alertmanager' COMMENT '事件来源（alertmanager/grafana/manual/api 等）',
                           alertname      VARCHAR(256) NULL COMMENT '告警名称（Alertmanager alertname）',
                           fingerprint    VARCHAR(64)  NULL COMMENT '告警聚合指纹（同类告警合并用，建议用 hash）',
                           rule_id        VARCHAR(128) NULL COMMENT '告警规则ID（来自告警系统或自研规则库）',
                           labels_json    JSON NULL COMMENT '告警 labels 原始 JSON（便于回放与诊断；可包含 instance、job、pod、container 等）',
                           annotations_json JSON NULL COMMENT '告警 annotations 原始 JSON（包含 summary/description/runbook 等）',

    -- 4) 严重级别、状态、时间窗口
                           severity VARCHAR(16) NOT NULL COMMENT '严重级别（P0/P1/P2 或 critical/warn/info）',
                           status   VARCHAR(16) NOT NULL DEFAULT 'open' COMMENT '事件状态（open/ack/mitigating/closed/silenced 等）',
                           start_at DATETIME(3) NULL COMMENT '事件开始时间（告警首次触发/事件窗口开始）',
                           end_at   DATETIME(3) NULL COMMENT '事件结束时间（恢复/关闭时间）',

    -- 5) 诊断/RCA 输出（先摘要 + JSON，后面再拆表）
                           rca_status VARCHAR(16) NOT NULL DEFAULT 'pending' COMMENT 'RCA 状态（pending/running/done/failed）',
                           root_cause_type VARCHAR(64) NULL COMMENT '根因类型（如 capacity/network/dependency/code/config/release）',
                           root_cause_summary VARCHAR(512) NULL COMMENT '根因摘要（一句话结论，便于列表检索）',
                           diagnosis_json JSON NULL COMMENT '诊断详情 JSON（多Agent 输出、证据引用、推理链路摘要等）',
                           evidence_refs_json JSON NULL COMMENT '证据引用 JSON（如 prom 查询、log 查询、trace 链接、K8s 对象快照的引用）',

    -- 6) 处置/审批闭环（Action 以后可以单独建表；这里先留关键状态）
                           action_status VARCHAR(16) NOT NULL DEFAULT 'none' COMMENT '处置状态（none/proposed/approved/executing/done/failed）',
                           action_summary VARCHAR(512) NULL COMMENT '处置建议/执行摘要（如扩容、回滚、重启、熔断、限流）',

    -- 7) 关联与追踪（链接 trace/log/变更）
                           trace_id   VARCHAR(64)  NULL COMMENT '关联 TraceID（如定位到某次请求链路，可为空）',
                           log_trace_key VARCHAR(128) NULL COMMENT '日志检索关键字/trace key（ELK/Tempo/自研日志平台）',
                           change_id  VARCHAR(128) NULL COMMENT '关联变更ID（如发布单/工单ID/CI/CD pipeline run id）',

    -- 8) 审计字段（用户体系后续接入也不返工）
                           created_by VARCHAR(128) NULL COMMENT '创建人（预留：用户体系接入后填充）',
                           approved_by VARCHAR(128) NULL COMMENT '审批人（预留：处置需要审批时填充）',
                           closed_by  VARCHAR(128) NULL COMMENT '关闭人（预留：手动关闭时填充）',

    -- 9) 通用时间字段
                           created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT '创建时间（毫秒精度）',
                           updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
    ON UPDATE CURRENT_TIMESTAMP(3) COMMENT '更新时间（毫秒精度）',

                           PRIMARY KEY (id),
                           UNIQUE KEY `incidents.incident_id` (incident_id),
    -- 常用查询索引：列表页/看板/聚合
                           KEY idx_tenant_env_time (tenant_id, environment, created_at) COMMENT '按租户+环境+时间范围查询（事件列表/看板）',
                           KEY idx_service_status_time (service, status, created_at) COMMENT '按服务+状态+时间查询（服务级事件视图）',
                           KEY idx_cluster_ns_workload (cluster, namespace, workload_kind, workload_name) COMMENT '按集群/命名空间/工作负载定位',
                           KEY idx_severity_status (severity, status) COMMENT '按严重级别+状态筛选',
                           KEY idx_fingerprint_time (fingerprint, created_at) COMMENT '按告警指纹聚合/追溯（同类事件合并）',
                           KEY idx_alertname_time (alertname, created_at) COMMENT '按告警名追溯（规则治理）',
                           KEY idx_change_id (change_id) COMMENT '按变更ID关联查询（发布/工单导致的事件）',
                           KEY idx_trace_id (trace_id) COMMENT '按 trace_id 关联查询（链路定位）'
) ENGINE=InnoDB
  DEFAULT CHARSET=utf8mb4
  COLLATE=utf8mb4_unicode_ci
  COMMENT='RCA 事件单（Incident）：告警聚合后的事件元数据 + 诊断/处置闭环的核心索引表';
