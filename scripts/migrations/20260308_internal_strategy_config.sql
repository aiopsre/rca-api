-- Task: internal strategy domain configurable closure
-- This migration is additive and does not change existing API paths.

CREATE TABLE IF NOT EXISTS pipeline_configs (
  id BIGINT PRIMARY KEY AUTO_INCREMENT,
  alert_source VARCHAR(64) NOT NULL DEFAULT '',
  service VARCHAR(128) NOT NULL DEFAULT '',
  namespace VARCHAR(128) NOT NULL DEFAULT '',
  pipeline_id VARCHAR(64) NOT NULL,
  graph_id VARCHAR(128) NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  UNIQUE KEY uniq_pipeline_configs_match (alert_source, service, namespace),
  KEY idx_pipeline_configs_lookup (alert_source, service, namespace)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS trigger_configs (
  id BIGINT PRIMARY KEY AUTO_INCREMENT,
  trigger_type VARCHAR(64) NOT NULL,
  pipeline_id VARCHAR(64) NOT NULL,
  session_type VARCHAR(64) NOT NULL DEFAULT '',
  fallback TINYINT(1) NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  UNIQUE KEY uniq_trigger_configs_trigger_type (trigger_type)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS toolset_configs_dynamic (
  id BIGINT PRIMARY KEY AUTO_INCREMENT,
  pipeline_id VARCHAR(64) NOT NULL,
  toolset_name VARCHAR(128) NOT NULL,
  allowed_tools_json LONGTEXT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  UNIQUE KEY uniq_toolset_configs_dynamic_pipeline_toolset (pipeline_id, toolset_name),
  KEY idx_toolset_configs_dynamic_pipeline (pipeline_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS sla_escalation_configs (
  id BIGINT PRIMARY KEY AUTO_INCREMENT,
  session_type VARCHAR(64) NOT NULL,
  due_seconds BIGINT NOT NULL DEFAULT 7200,
  escalation_thresholds_json LONGTEXT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  UNIQUE KEY uniq_sla_escalation_configs_session_type (session_type)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS session_assignments (
  id BIGINT PRIMARY KEY AUTO_INCREMENT,
  session_id VARCHAR(64) NOT NULL,
  assignee VARCHAR(128) NOT NULL DEFAULT '',
  assigned_by VARCHAR(128) NOT NULL DEFAULT '',
  assigned_at DATETIME NULL,
  note TEXT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  UNIQUE KEY uniq_session_assignments_session_id (session_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
