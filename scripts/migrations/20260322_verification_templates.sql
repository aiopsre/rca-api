-- Task 8B — Verification Templates Table
-- This migration creates the verification_templates table for storing
-- configurable verification step templates matched by root_cause_type.

CREATE TABLE IF NOT EXISTS verification_templates (
  id BIGINT PRIMARY KEY AUTO_INCREMENT,
  name VARCHAR(128) NOT NULL COMMENT '模板名称',
  description TEXT NULL COMMENT '模板描述',
  lineage_id VARCHAR(64) NOT NULL DEFAULT '' COMMENT '逻辑模板版本链归属 ID',
  version INT NOT NULL DEFAULT 1 COMMENT '版本号（自动递增）',
  match_json TEXT NOT NULL COMMENT '匹配条件 JSON（root_cause_types, patterns_contain, confidence_min）',
  steps_json LONGTEXT NOT NULL COMMENT '验证步骤 JSON（复用 verification.Plan 格式）',
  active TINYINT(1) NOT NULL DEFAULT 0 COMMENT '是否激活',
  activated_at DATETIME NULL COMMENT '激活时间',
  activated_by VARCHAR(128) NULL COMMENT '激活人',
  previous_version INT NULL COMMENT '前一个版本号（用于回滚）',
  created_by VARCHAR(128) NOT NULL COMMENT '创建人',
  updated_by VARCHAR(128) NULL COMMENT '更新人',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  UNIQUE KEY uniq_verification_templates_name (name),
  INDEX idx_verification_templates_lineage_version (lineage_id, version),
  INDEX idx_verification_templates_active (active)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;