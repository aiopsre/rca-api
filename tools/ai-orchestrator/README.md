# AI Orchestrator

LangGraph-based orchestrator for Root Cause Analysis (RCA) workflows with dynamic tool discovery and LLM-driven planning.

## Architecture Overview

The orchestrator executes a dynamic workflow that discovers available tools at runtime and uses LLM-powered Skills to plan evidence collection and diagnosis.

```
┌─────────────────────────────────────────────────────────────────────────┐
│                           Execution Flow                                  │
│                                                                          │
│  load_job_and_start                                                      │
│         │                                                                │
│         ▼                                                                │
│  plan_evidence  ◄── evidence.plan capability (LLM)                      │
│         │                                                                │
│         ▼                                                                │
│  plan_tool_calls  ◄── tool.plan capability (LLM)                        │
│         │                                                                │
│         ▼                                                                │
│  execute_tool_calls  ◄── Dynamic tool execution via MCP                 │
│         │                                                                │
│         ▼                                                                │
│  merge_evidence                                                          │
│         │                                                                │
│         ▼                                                                │
│  quality_gate                                                            │
│         │                                                                │
│         ▼                                                                │
│  summarize_diagnosis  ◄── diagnosis.enrich capability (LLM)             │
│         │                                                                │
│         ▼                                                                │
│  finalize_job                                                            │
│         │                                                                │
│         ▼                                                                │
│  post_finalize_observe ──► run_verification ──► END                     │
│                                                                          │
└─────────────────────────────────────────────────────────────────────────┘
```

### Key Components

| Component | Description |
|-----------|-------------|
| **ToolDiscovery** | Discovers available tools from MCP Servers at runtime |
| **ToolCallPlan** | Dynamic plan for tool execution with parallel groups |
| **Capabilities** | LLM intervention points: `evidence.plan`, `tool.plan`, `diagnosis.enrich` |
| **PromptSkillAgent** | LangChain-OpenAI based agent for skill execution |
| **Skill Bundles** | Declarative skill definitions with prompts and resources |

## Quick Start

### Installation

```bash
cd tools/ai-orchestrator
python3 -m venv .venv
source .venv/bin/activate
pip install -e .
```

### Basic Run (No LLM)

```bash
SCOPES='*' RUN_QUERY=0 python -m orchestrator.main
```

### With LLM-Driven Skills

```bash
SCOPES='*' \
SKILLS_EXECUTION_MODE=prompt_first \
AGENT_MODEL=gpt-4o \
AGENT_BASE_URL=https://api.openai.com/v1 \
AGENT_API_KEY=sk-xxx \
python -m orchestrator.main
```

## LLM Intervention Points

The orchestrator provides three capability hooks for LLM intervention:

### `evidence.plan` (Stage: plan_evidence)

Plans what evidence to collect based on incident context.

**Input**:
- `incident_id`, `incident_context`, `input_hints`
- `evidence_mode`, `evidence_candidates`

**Output**:
- `evidence_plan_patch` - Patch for evidence plan
- `metrics_branch_meta` - Metrics query parameters
- `logs_branch_meta` - Logs query parameters

### `tool.plan` (Stage: plan_tool_calls)

Generates dynamic tool call plan based on available tools.

**Input**:
- `incident_id`, `incident_context`
- `existing_evidence_ids`

**Output**:
```json
{
  "tool_call_plan": {
    "items": [
      {"tool": "prometheus_query", "params": {...}, "query_type": "metrics", "purpose": "..."},
      {"tool": "loki_search", "params": {...}, "query_type": "logs", "purpose": "..."}
    ],
    "parallel_groups": [[0, 1]]
  }
}
```

### `diagnosis.enrich` (Stage: summarize_diagnosis)

Enriches diagnosis with LLM analysis.

**Input**:
- `incident_context`, `quality_gate_decision`
- `evidence_ids`, `evidence_meta`, `diagnosis_json`

**Output**:
- `diagnosis_patch` - Summary, root_cause, recommendations

## Dynamic Tool Execution

The orchestrator discovers tools at runtime from MCP Servers, rather than hardcoding tool names.

### Tool Discovery

```python
from orchestrator.runtime.tool_discovery import discover_tools

discovery = discover_tools(runtime)
# discovery.tools - All available tools
# discovery.find_by_tag("metrics") - Tools tagged as metrics
# discovery.find_by_pattern("prometheus_*") - Pattern matching
```

### Tool Call Plan

```python
from orchestrator.state.tool_call_plan import ToolCallPlan, ToolCallItem

plan = ToolCallPlan(items=[
    ToolCallItem(tool="prometheus_query", params={"promql": "up"}, query_type="metrics"),
    ToolCallItem(tool="loki_search", params={"query": "error"}, query_type="logs"),
], parallel_groups=[[0, 1]])  # Both can run in parallel
```

### Runtime Tool Calling

```python
# Execute any discovered tool
result = runtime.call_tool(tool="prometheus_query", params={"promql": "up"})
```

## Skill Bundles

Skills are defined as bundles with prompts, templates, and resources:

```
skill-bundles/
├── evidence-plan/
│   └── SKILL.md              # Prompt executor
├── prometheus-evidence-plan/
│   ├── SKILL.md
│   ├── references/           # Knowledge resources
│   └── examples/             # Few-shot examples
├── diagnosis-enrich/
│   └── SKILL.md
└── diagnosis-script-enrich/
    ├── SKILL.md
    ├── scripts/executor.py   # Script executor
    └── templates/
```

### Skill Modes

| Mode | Description |
|------|-------------|
| `prompt_first` | Uses PromptSkillAgent with OpenAI-compatible API |
| `catalog` | Platform-resolved skills (fallback mode) |

## Environment Variables

### Required

| Variable | Description | Default |
|----------|-------------|---------|
| `BASE_URL` | RCA API base URL | `http://127.0.0.1:5555` |
| `SCOPES` | Tenant scopes for API requests | (required) |

### LLM Configuration (for prompt_first mode)

| Variable | Description | Default |
|----------|-------------|---------|
| `SKILLS_EXECUTION_MODE` | `prompt_first` or `catalog` | `prompt_first` |
| `AGENT_MODEL` | Model name (e.g., `gpt-4o`) | (required for prompt_first) |
| `AGENT_BASE_URL` | OpenAI-compatible API URL | (required for prompt_first) |
| `AGENT_API_KEY` | API key | (required for prompt_first) |
| `AGENT_TIMEOUT_SECONDS` | Request timeout | `20.0` |

### Optional

| Variable | Description | Default |
|----------|-------------|---------|
| `INSTANCE_ID` | Worker instance ID | `{hostname}-{pid}` |
| `CONCURRENCY` | Max concurrent jobs | `1` |
| `POLL_INTERVAL_MS` | Poll interval for job queue | `1000` |
| `LONG_POLL_WAIT_SECONDS` | Long poll wait time | `20` |
| `LEASE_HEARTBEAT_INTERVAL_SECONDS` | Lease heartbeat interval | `10` |
| `RCA_API_SCOPES` | Scopes for MCP shim calls | (empty) |
| `MCP_VERIFY_REMOTE_TOOLS` | Verify MCP tool registry | `0` |
| `DEBUG` | Enable debug logging | `0` |
| `HEALTH_PORT` | Health endpoint port | `8080` |
| `HEALTH_HOST` | Health endpoint host | `0.0.0.0` |

### Query Execution

| Variable | Description | Default |
|----------|-------------|---------|
| `RUN_QUERY` | Execute real queries vs mock | `0` |
| `DS_BASE_URL` | Datasource URL | (empty) |
| `DS_TYPE` | Datasource type | `prometheus` |
| `AUTO_CREATE_DATASOURCE` | Auto-create datasource | `1` |

### Budget Controls

| Variable | Description | Default |
|----------|-------------|---------|
| `A3_MAX_CALLS` | Max tool calls per job | `6` |
| `A3_MAX_TOTAL_BYTES` | Max response bytes | `2MB` |
| `A3_MAX_TOTAL_LATENCY_MS` | Max total latency | `8000` |

### Verification

| Variable | Description | Default |
|----------|-------------|---------|
| `RUN_VERIFICATION` | Enable post-finalize verification | `0` |
| `VERIFICATION_SOURCE` | Verification source | `ai_job` |
| `VERIFICATION_MAX_STEPS` | Max verification steps | `20` |

### Skill Paths

| Variable | Description | Default |
|----------|-------------|---------|
| `SKILLS_CACHE_DIR` | Skill bundle cache | `/tmp/rca-ai-orchestrator/skills-cache` |
| `SKILLS_LOCAL_PATHS` | Local skill override paths | (empty) |
| `SKILLS_TOOL_CALLING_MODE` | Controlled tool calling | `disabled` |

## Health Endpoints

The orchestrator exposes health and metrics endpoints:

```bash
# Health check
curl http://localhost:8080/health

# Prometheus metrics
curl http://localhost:8080/metrics
```

## Testing

```bash
# Run all tests
pytest tests/ -v

# Run with coverage
pytest tests/ --cov=orchestrator --cov-report=html
```

## Skill Smoke Suite

```bash
# Run skill smoke tests
./scripts/run_skills_smoke_suite.sh

# Keep workdir for debugging
SKILLS_SMOKE_KEEP_WORKDIR=1 ./scripts/run_skills_smoke_suite.sh
```

## See Also

- [docs/concepts.md](docs/concepts.md) - Core concepts and data structures
- [docs/runtime/dynamic-tool-execution-plan.md](docs/runtime/dynamic-tool-execution-plan.md) - Dynamic tool execution design