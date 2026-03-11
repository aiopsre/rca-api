-- Task: MCP Provider Boundary Redesign - Phase 1
-- This migration creates toolset_provider_bindings table for toolset-to-provider binding governance.

CREATE TABLE IF NOT EXISTS toolset_provider_bindings (
  id BIGINT PRIMARY KEY AUTO_INCREMENT,
  toolset_name VARCHAR(128) NOT NULL,
  mcp_server_id VARCHAR(64) NOT NULL,
  allowed_tools_json LONGTEXT NULL,
  priority INT NOT NULL DEFAULT 0,
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  UNIQUE KEY uniq_toolset_provider (toolset_name, mcp_server_id),
  KEY idx_toolset_provider_toolset (toolset_name),
  KEY idx_toolset_provider_server (mcp_server_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;