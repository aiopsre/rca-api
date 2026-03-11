-- Task: MCP Provider Boundary Redesign - Phase 1
-- This migration extends mcp_servers table with new fields for provider classification.

ALTER TABLE mcp_servers
  ADD COLUMN IF NOT EXISTS server_kind VARCHAR(32) NOT NULL DEFAULT 'external' AFTER name,
  ADD COLUMN IF NOT EXISTS is_system BOOLEAN NOT NULL DEFAULT FALSE AFTER server_kind,
  ADD COLUMN IF NOT EXISTS builtin_key VARCHAR(64) NULL AFTER is_system,
  ADD COLUMN IF NOT EXISTS tenant_id VARCHAR(64) NULL AFTER builtin_key;

-- Add index for builtin_key lookup
CREATE INDEX IF NOT EXISTS idx_mcp_servers_builtin_key ON mcp_servers(builtin_key);

-- Add index for tenant_id lookup
CREATE INDEX IF NOT EXISTS idx_mcp_servers_tenant ON mcp_servers(tenant_id);

-- Seed builtin readonly provider
INSERT INTO mcp_servers (
  mcp_server_id, name, server_kind, is_system, builtin_key,
  base_url, auth_type, timeout_sec, status
) VALUES (
  'rca-api-builtin-readonly',
  'RCA API Builtin Readonly',
  'builtin',
  TRUE,
  'rca_api_readonly',
  'https://rca-api-internal',
  'none',
  10,
  'active'
) ON DUPLICATE KEY UPDATE
  server_kind = VALUES(server_kind),
  is_system = VALUES(is_system),
  builtin_key = VALUES(builtin_key);