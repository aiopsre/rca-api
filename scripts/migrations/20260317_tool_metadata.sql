-- Task: Tool metadata platform-side management
-- This migration creates the tool_metadata table for storing tool classification metadata.
-- The metadata is synced to the Python orchestrator via McpServerRef.tool_metadata field.

CREATE TABLE IF NOT EXISTS tool_metadata (
    id BIGINT PRIMARY KEY AUTO_INCREMENT,
    tool_name VARCHAR(128) NOT NULL,
    kind VARCHAR(32) NOT NULL DEFAULT 'unknown',
    domain VARCHAR(64) NOT NULL DEFAULT 'general',
    read_only TINYINT(1) NOT NULL DEFAULT 1,
    risk_level VARCHAR(16) NOT NULL DEFAULT 'low',
    latency_tier VARCHAR(16) NOT NULL DEFAULT 'fast',
    cost_hint VARCHAR(16) NOT NULL DEFAULT 'free',
    tags_json TEXT NULL,
    description VARCHAR(512) NULL,
    mcp_server_id VARCHAR(64) NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'active',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uniq_tool_metadata_name (tool_name),
    KEY idx_tool_metadata_kind (kind),
    KEY idx_tool_metadata_mcp_server (mcp_server_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Initial data (migrated from Python _DEFAULT_TOOL_METADATA)
INSERT INTO tool_metadata (tool_name, kind, domain, tags_json, description) VALUES
('prometheus_query', 'metrics', 'observability', '["metrics","query","promql"]', 'Query Prometheus metrics'),
('prometheus_range_query', 'metrics', 'observability', '["metrics","query","promql"]', 'Query Prometheus metrics over a time range'),
('victoriametrics_query', 'metrics', 'observability', '["metrics","query"]', 'Query VictoriaMetrics metrics'),
('loki_search', 'logs', 'observability', '["logs","search"]', 'Search Loki logs'),
('elasticsearch_search', 'logs', 'observability', '["logs","search"]', 'Search Elasticsearch logs'),
('jaeger_query', 'traces', 'observability', '["traces","query"]', 'Query Jaeger traces'),
('tempo_query', 'traces', 'observability', '["traces","query"]', 'Query Tempo traces'),
('alertmanager_list', 'incidents', 'incident', '["incidents","alerts"]', 'List Alertmanager alerts'),
('get_incident', 'incidents', 'incident', '["incidents","query"]', 'Get incident details')
ON DUPLICATE KEY UPDATE updated_at = CURRENT_TIMESTAMP;