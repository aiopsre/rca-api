-- Task: MCP Provider Boundary Redesign - Phase 1
-- This migration extends tool_metadata table with tool_class and visibility fields.

ALTER TABLE tool_metadata
  ADD COLUMN IF NOT EXISTS tool_class VARCHAR(32) NOT NULL DEFAULT 'fc_selectable' AFTER risk_level,
  ADD COLUMN IF NOT EXISTS allowed_for_prompt_skill BOOLEAN NOT NULL DEFAULT TRUE AFTER tool_class,
  ADD COLUMN IF NOT EXISTS allowed_for_graph_agent BOOLEAN NOT NULL DEFAULT TRUE AFTER allowed_for_prompt_skill,
  ADD COLUMN IF NOT EXISTS aliases_json LONGTEXT NULL AFTER tags_json;

-- Add index for tool_class
CREATE INDEX IF NOT EXISTS idx_tool_metadata_tool_class ON tool_metadata(tool_class);

-- Update existing tools to use A-class (fc_selectable) by default
UPDATE tool_metadata SET tool_class = 'fc_selectable' WHERE tool_class = 'fc_selectable';

-- Insert initial A-class tool metadata with dotted canonical names
INSERT INTO tool_metadata (
  tool_name, kind, domain, read_only, risk_level,
  tool_class, allowed_for_prompt_skill, allowed_for_graph_agent,
  aliases_json, mcp_server_id, status, description
) VALUES
  ('incident.get', 'incident', 'platform', TRUE, 'low', 'fc_selectable', TRUE, TRUE, '["get_incident"]', 'rca-api-builtin-readonly', 'active', 'Get incident details by ID'),
  ('incident.list', 'incident', 'platform', TRUE, 'low', 'fc_selectable', TRUE, TRUE, '["list_incidents"]', 'rca-api-builtin-readonly', 'active', 'List incidents with optional filters'),
  ('evidence.get', 'evidence', 'platform', TRUE, 'low', 'fc_selectable', TRUE, TRUE, '["get_evidence"]', 'rca-api-builtin-readonly', 'active', 'Get evidence by ID'),
  ('evidence.search', 'evidence', 'platform', TRUE, 'low', 'fc_selectable', TRUE, TRUE, '["search_evidence"]', 'rca-api-builtin-readonly', 'active', 'Search evidence with optional filters'),
  ('job.get', 'job', 'platform', TRUE, 'low', 'fc_selectable', TRUE, TRUE, '["get_ai_job"]', 'rca-api-builtin-readonly', 'active', 'Get AI job details by ID'),
  ('tool_call.list', 'tool_call', 'platform', TRUE, 'low', 'fc_selectable', TRUE, TRUE, '["list_tool_calls"]', 'rca-api-builtin-readonly', 'active', 'List tool calls for a job'),
  ('logs.query', 'logs', 'observability', TRUE, 'low', 'fc_selectable', TRUE, TRUE, '["query_logs"]', NULL, 'active', 'Query logs from observability backend'),
  ('metrics.query', 'metrics', 'observability', TRUE, 'low', 'fc_selectable', TRUE, TRUE, '["query_metrics"]', NULL, 'active', 'Query metrics from observability backend'),
  ('metrics.query_range', 'metrics', 'observability', TRUE, 'low', 'fc_selectable', TRUE, TRUE, '["query_range"]', NULL, 'active', 'Query metrics over a time range'),
  ('traces.query', 'traces', 'observability', TRUE, 'low', 'fc_selectable', TRUE, TRUE, '["query_traces"]', NULL, 'active', 'Query traces from observability backend')
ON DUPLICATE KEY UPDATE
  aliases_json = VALUES(aliases_json),
  tool_class = VALUES(tool_class),
  mcp_server_id = VALUES(mcp_server_id),
  description = VALUES(description);

-- Insert B-class (runtime_owned) tool metadata
INSERT INTO tool_metadata (
  tool_name, kind, domain, read_only, risk_level,
  tool_class, allowed_for_prompt_skill, allowed_for_graph_agent,
  aliases_json, mcp_server_id, status, description
) VALUES
  ('session.patch', 'session', 'platform', FALSE, 'medium', 'runtime_owned', FALSE, FALSE, '["patch_session_context"]', NULL, 'active', 'Patch session context (runtime-owned)'),
  ('knowledge_base.save', 'knowledge', 'platform', FALSE, 'medium', 'runtime_owned', FALSE, FALSE, '["save_knowledge_base_entry"]', NULL, 'active', 'Save knowledge base entry (runtime-owned)')
ON DUPLICATE KEY UPDATE
  aliases_json = VALUES(aliases_json),
  tool_class = VALUES(tool_class),
  description = VALUES(description);