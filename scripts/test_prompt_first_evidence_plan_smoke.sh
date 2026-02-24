#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
SCOPES="${SCOPES:-*}"
CONFIG_SCOPES="${CONFIG_SCOPES:-config.admin,ai.read,ai.run}"
OPERATOR_ID="${OPERATOR_ID:-bootstrap_super_admin}"
OPERATOR_PASSWORD="${OPERATOR_PASSWORD:-Admin123_}"
PIPELINE_ID="${PIPELINE_ID:-basic_rca}"
SKILLSET_NAME="${SKILLSET_NAME:-prompt_first_evidence_plan}"
EXECUTOR_SKILL_ID="${EXECUTOR_SKILL_ID:-claude.evidence.prompt_planner}"
EXECUTOR_SKILL_VERSION="${EXECUTOR_SKILL_VERSION:-1.0.0}"
ELASTIC_SKILL_ID="${ELASTIC_SKILL_ID:-elasticsearch.evidence.plan}"
ELASTIC_SKILL_VERSION="${ELASTIC_SKILL_VERSION:-1.0.0}"
PROM_SKILL_ID="${PROM_SKILL_ID:-prometheus.evidence.plan}"
PROM_SKILL_VERSION="${PROM_SKILL_VERSION:-1.0.0}"
SKILL_CAPABILITY="${SKILL_CAPABILITY:-evidence.plan}"
INSTANCE_ID="${INSTANCE_ID:-prompt-first-evidence-smoke-$$}"
JOB_WAIT_TIMEOUT_SEC="${JOB_WAIT_TIMEOUT_SEC:-180}"
JOB_POLL_INTERVAL_SEC="${JOB_POLL_INTERVAL_SEC:-1}"
AGENT_MODEL="${AGENT_MODEL:-}"
AGENT_BASE_URL="${AGENT_BASE_URL:-}"
AGENT_API_KEY="${AGENT_API_KEY:-}"
AGENT_TIMEOUT_SECONDS="${AGENT_TIMEOUT_SECONDS:-20}"
DS_BASE_URL="${DS_BASE_URL:-}"
DS_TYPE="${DS_TYPE:-prometheus}"
ORCH_CMD="${ORCH_CMD:-python3 -m orchestrator.main}"
CURL_BIN="${CURL_BIN:-curl}"
JQ_BIN="${JQ_BIN:-jq}"
ZIP_BIN="${ZIP_BIN:-zip}"
PYTHON_BIN="${PYTHON_BIN:-python3}"
ORCH_DIR="${ORCH_DIR:-/opt/workspace/study/rca-api/tools/ai-orchestrator}"
EXECUTOR_BUNDLE_DIR="${EXECUTOR_BUNDLE_DIR:-/opt/workspace/study/rca-api/tools/ai-orchestrator/skill-bundles/evidence-plan}"
ELASTIC_BUNDLE_DIR="${ELASTIC_BUNDLE_DIR:-/opt/workspace/study/rca-api/tools/ai-orchestrator/skill-bundles/elasticsearch-evidence-plan}"
PROM_BUNDLE_DIR="${PROM_BUNDLE_DIR:-/opt/workspace/study/rca-api/tools/ai-orchestrator/skill-bundles/prometheus-evidence-plan}"
WORKDIR="${WORKDIR:-$(mktemp -d)}"
KEEP_WORKDIR="${KEEP_WORKDIR:-0}"
DEBUG="${DEBUG:-0}"

INCIDENT_ID=""
JOB_ID=""
ORCH_PID=""
ORCH_LOG=""

debug() {
  if [[ "${DEBUG}" == "1" ]]; then
    echo "[DEBUG] $*" >&2
  fi
}

cleanup() {
  if [[ -n "${ORCH_PID}" ]]; then
    kill "${ORCH_PID}" >/dev/null 2>&1 || true
    wait "${ORCH_PID}" >/dev/null 2>&1 || true
  fi
  if [[ "${KEEP_WORKDIR}" != "1" ]] && [[ -n "${WORKDIR:-}" ]] && [[ -d "${WORKDIR}" ]]; then
    rm -rf "${WORKDIR}"
  fi
}
trap cleanup EXIT

require_bin() {
  local bin="$1"
  if ! command -v "${bin}" >/dev/null 2>&1; then
    echo "missing required binary: ${bin}" >&2
    exit 1
  fi
}

fail_step() {
  local step="$1"
  local detail="${2:-}"
  echo "FAIL ${step}" >&2
  if [[ -n "${detail}" ]]; then
    echo "${detail}" >&2
  fi
  echo "incident_id=${INCIDENT_ID:-NONE}" >&2
  echo "job_id=${JOB_ID:-NONE}" >&2
  echo "worker_log=${ORCH_LOG:-NONE}" >&2
  echo "workdir=${WORKDIR:-NONE}" >&2
  if [[ -n "${ORCH_LOG}" ]] && [[ -f "${ORCH_LOG}" ]]; then
    echo "worker_log_tail<<EOF" >&2
    tail -n 120 "${ORCH_LOG}" >&2 || true
    echo "EOF" >&2
  fi
  exit 1
}

json_escape() {
  printf '%s' "$1" | "${JQ_BIN}" -Rs .
}

login_token() {
  local payload
  payload='{"operator_id":"'"${OPERATOR_ID}"'","password":"'"${OPERATOR_PASSWORD}"'","scopes":["config.admin","ai.read","ai.run"]}'
  "${CURL_BIN}" -sS -X POST "${BASE_URL}/v1/auth/login" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json' \
    -d "${payload}" | "${JQ_BIN}" -r '.token // .access_token // .data.token // empty'
}

call_token_json() {
  local method="$1"
  local url="$2"
  local token="$3"
  local body="${4:-}"
  local extra_header_name="${5:-}"
  local extra_header_value="${6:-}"
  local -a cmd
  cmd=(
    "${CURL_BIN}" -sS -X "${method}" "${url}"
    -H "Authorization: Bearer ${token}"
    -H "X-Scopes: ${CONFIG_SCOPES}"
    -H 'Accept: application/json'
  )
  if [[ -n "${extra_header_name}" && -n "${extra_header_value}" ]]; then
    cmd+=(-H "${extra_header_name}: ${extra_header_value}")
  fi
  if [[ -n "${body}" ]]; then
    cmd+=(-H 'Content-Type: application/json' -d "${body}")
  fi
  "${cmd[@]}"
}

call_token_multipart() {
  local url="$1"
  local token="$2"
  local bundle_path="$3"
  local skill_id="$4"
  local version="$5"
  "${CURL_BIN}" -sS -X POST "${url}" \
    -H "Authorization: Bearer ${token}" \
    -H "X-Scopes: ${CONFIG_SCOPES}" \
    -H 'Accept: application/json' \
    -F "bundle=@${bundle_path};type=application/zip" \
    -F "skill_id=${skill_id}" \
    -F "version=${version}" \
    -F "status=active"
}

assert_json_success() {
  local step="$1"
  local json="$2"
  if ! printf '%s' "${json}" | "${JQ_BIN}" -e '((has("code") | not) or .code == 0)' >/dev/null; then
    fail_step "${step}" "$(printf '%s\n' "${json}" | "${JQ_BIN}" .)"
  fi
}

assert_json_expr() {
  local step="$1"
  local json="$2"
  local expr="$3"
  if ! printf '%s' "${json}" | "${JQ_BIN}" -e "${expr}" >/dev/null; then
    fail_step "${step}" "jq assertion failed: ${expr}\n$(printf '%s\n' "${json}" | "${JQ_BIN}" .)"
  fi
}

preflight_no_conflicting_evidence_skill() {
  local token="$1"
  local existing_json
  existing_json="$(call_token_json GET "${BASE_URL}/v1/config/skillset/${PIPELINE_ID}" "${token}")"
  assert_json_success "GetSkillsetConfig" "${existing_json}"
  local conflict_count
  conflict_count="$(printf '%s' "${existing_json}" | "${JQ_BIN}" -r \
    --arg skillset "${SKILLSET_NAME}" \
    --arg exec_skill_id "${EXECUTOR_SKILL_ID}" \
    --arg exec_version "${EXECUTOR_SKILL_VERSION}" \
    --arg elastic_skill_id "${ELASTIC_SKILL_ID}" \
    --arg elastic_version "${ELASTIC_SKILL_VERSION}" \
    --arg prom_skill_id "${PROM_SKILL_ID}" \
    --arg prom_version "${PROM_SKILL_VERSION}" '
    [
      (.data.items // .items // [])[]? as $item
      | ($item.skills // [])[]?
      | select((.enabled? == false) | not)
      | select((.capability // "") == "evidence.plan")
      | select(
          ($item.skillset_name // "") != $skillset
          or (
            [(.skill_id // ""), (.version // "")]
            != [$exec_skill_id, $exec_version]
            and [(.skill_id // ""), (.version // "")]
            != [$elastic_skill_id, $elastic_version]
            and [(.skill_id // ""), (.version // "")]
            != [$prom_skill_id, $prom_version]
          )
        )
    ] | length
  ')"
  if [[ "${conflict_count}" != "0" ]]; then
    fail_step "PreflightEvidencePlanConflict" "$(printf '%s\n' "${existing_json}" | "${JQ_BIN}" .)"
  fi
}

start_orchestrator() {
  ORCH_LOG="${WORKDIR}/orchestrator.log"
  (
    cd "${ORCH_DIR}" && \
      BASE_URL="${BASE_URL}" \
      SCOPES="${SCOPES}" \
      RCA_API_SCOPES="${SCOPES}" \
      INSTANCE_ID="${INSTANCE_ID}" \
      CONCURRENCY=1 \
      POLL_INTERVAL_MS=200 \
      LONG_POLL_WAIT_SECONDS=2 \
      LEASE_HEARTBEAT_INTERVAL_SECONDS=3 \
      RUN_QUERY=1 \
      DS_BASE_URL="${DS_BASE_URL}" \
      DS_TYPE="${DS_TYPE}" \
      RUN_VERIFICATION=0 \
      POST_FINALIZE_OBSERVE=0 \
      SKILLS_EXECUTION_MODE=prompt_first \
      SKILLS_TOOL_CALLING_MODE=evidence_plan_dual_tool \
      AGENT_MODEL="${AGENT_MODEL}" \
      AGENT_BASE_URL="${AGENT_BASE_URL}" \
      AGENT_API_KEY="${AGENT_API_KEY}" \
      AGENT_TIMEOUT_SECONDS="${AGENT_TIMEOUT_SECONDS}" \
      DEBUG="${DEBUG}" \
      bash -lc "${ORCH_CMD}"
  ) >"${ORCH_LOG}" 2>&1 &
  ORCH_PID="$!"
  sleep 1
  if ! kill -0 "${ORCH_PID}" >/dev/null 2>&1; then
    fail_step "StartOrchestrator" "worker exited early"
  fi
}

wait_for_job_succeeded() {
  local token="$1"
  local deadline status body
  deadline="$(( $(date +%s) + JOB_WAIT_TIMEOUT_SEC ))"
  while true; do
    body="$(call_token_json GET "${BASE_URL}/v1/ai/jobs/${JOB_ID}" "${token}")"
    assert_json_success "PollAIJob" "${body}"
    status="$(printf '%s' "${body}" | "${JQ_BIN}" -r '.data.job.status // .data.status // .job.status // .status // empty')"
    case "${status}" in
      succeeded)
        return 0
        ;;
      failed|canceled)
        fail_step "AIJobTerminal.${status}" "$(printf '%s\n' "${body}" | "${JQ_BIN}" .)"
        ;;
    esac
    if (( $(date +%s) > deadline )); then
      fail_step "AIJobTimeout" "$(printf '%s\n' "${body}" | "${JQ_BIN}" .)"
    fi
    sleep "${JOB_POLL_INTERVAL_SEC}"
  done
}

require_bin "${CURL_BIN}"
require_bin "${JQ_BIN}"
require_bin "${ZIP_BIN}"
require_bin "${PYTHON_BIN}"

if [[ -z "${AGENT_MODEL}" || -z "${AGENT_BASE_URL}" || -z "${AGENT_API_KEY}" ]]; then
  fail_step "PromptFirstConfig" "AGENT_MODEL, AGENT_BASE_URL, and AGENT_API_KEY are required"
fi
if [[ -z "${DS_BASE_URL}" ]]; then
  fail_step "DatasourceConfig" "DS_BASE_URL is required for dual-tool evidence.plan smoke"
fi
for bundle_dir in "${EXECUTOR_BUNDLE_DIR}" "${ELASTIC_BUNDLE_DIR}" "${PROM_BUNDLE_DIR}"; do
  if [[ ! -d "${bundle_dir}" ]]; then
    fail_step "SkillBundlePath" "bundle dir not found: ${bundle_dir}"
  fi
done
if [[ "$("${CURL_BIN}" -sS -o /dev/null -w '%{http_code}' "${BASE_URL}/healthz")" != "200" ]]; then
  fail_step "Healthz" "health check failed: ${BASE_URL}/healthz"
fi

mkdir -p "${WORKDIR}"
TOKEN="$(login_token)"
if [[ -z "${TOKEN}" ]]; then
  fail_step "Login" "failed to obtain config.admin token"
fi

preflight_no_conflicting_evidence_skill "${TOKEN}"

TEMPLATE_REGISTER_BODY='{"instanceID":"'"${INSTANCE_ID}"'","templates":[{"templateID":"basic_rca","version":""}]}'
TEMPLATE_REGISTER_RESPONSE="$(call_token_json POST "${BASE_URL}/v1/orchestrator/templates/register" "${TOKEN}" "${TEMPLATE_REGISTER_BODY}")"
assert_json_success "RegisterTemplate" "${TEMPLATE_REGISTER_RESPONSE}"
assert_json_expr "RegisterTemplate" "${TEMPLATE_REGISTER_RESPONSE}" '(.data.count // .count // 0) >= 1'

upload_skill() {
  local step="$1"
  local skill_id="$2"
  local version="$3"
  local bundle_dir="$4"
  local bundle_path="${WORKDIR}/${skill_id}-${version}.zip"
  (cd "${bundle_dir}" && "${ZIP_BIN}" -qr "${bundle_path}" .)
  local upload_response
  upload_response="$(call_token_multipart "${BASE_URL}/v1/config/skill-release/upload" "${TOKEN}" "${bundle_path}" "${skill_id}" "${version}")"
  assert_json_success "${step}" "${upload_response}"
  assert_json_expr "${step}" "${upload_response}" '(.data.skill_id // .skill_id) == "'"${skill_id}"'"'
  assert_json_expr "${step}" "${upload_response}" '(.data.version // .version) == "'"${version}"'"'
}

upload_skill "UploadExecutorSkillRelease" "${EXECUTOR_SKILL_ID}" "${EXECUTOR_SKILL_VERSION}" "${EXECUTOR_BUNDLE_DIR}"
upload_skill "UploadElasticKnowledgeSkillRelease" "${ELASTIC_SKILL_ID}" "${ELASTIC_SKILL_VERSION}" "${ELASTIC_BUNDLE_DIR}"
upload_skill "UploadPromKnowledgeSkillRelease" "${PROM_SKILL_ID}" "${PROM_SKILL_VERSION}" "${PROM_BUNDLE_DIR}"

SKILLSET_UPDATE_BODY="$(cat <<JSON
{
  "pipeline_id": "${PIPELINE_ID}",
  "skillset_name": "${SKILLSET_NAME}",
  "skills": [
    {
      "skill_id": "${ELASTIC_SKILL_ID}",
      "version": "${ELASTIC_SKILL_VERSION}",
      "capability": "${SKILL_CAPABILITY}",
      "role": "knowledge",
      "allowed_tools": [],
      "priority": 150,
      "enabled": true
    },
    {
      "skill_id": "${PROM_SKILL_ID}",
      "version": "${PROM_SKILL_VERSION}",
      "capability": "${SKILL_CAPABILITY}",
      "role": "knowledge",
      "allowed_tools": [],
      "priority": 140,
      "enabled": true
    },
    {
      "skill_id": "${EXECUTOR_SKILL_ID}",
      "version": "${EXECUTOR_SKILL_VERSION}",
      "capability": "${SKILL_CAPABILITY}",
      "role": "executor",
      "allowed_tools": ["query_logs","query_metrics"],
      "priority": 100,
      "enabled": true
    }
  ]
}
JSON
)"
SKILLSET_UPDATE_RESPONSE="$(call_token_json POST "${BASE_URL}/v1/config/skillset/update" "${TOKEN}" "${SKILLSET_UPDATE_BODY}")"
assert_json_success "UpsertSkillset" "${SKILLSET_UPDATE_RESPONSE}"
assert_json_expr "UpsertSkillset" "${SKILLSET_UPDATE_RESPONSE}" '
  any((.data.items // .items // [])[]?; (.skillset_name // "") == "'"${SKILLSET_NAME}"'")
'

STRATEGY_RESOLVE_RESPONSE="$(call_token_json GET "${BASE_URL}/v1/orchestrator/strategies/resolve?pipeline=${PIPELINE_ID}" "${TOKEN}")"
assert_json_success "ResolveStrategy" "${STRATEGY_RESOLVE_RESPONSE}"
assert_json_expr "ResolveStrategy" "${STRATEGY_RESOLVE_RESPONSE}" '
  ((.data.strategy.skillsetIDs // .data.strategy.skillset_ids // .strategy.skillsetIDs // .strategy.skillset_ids // []) | index("'"${SKILLSET_NAME}"'")) != null
'

SKILLSET_RESOLVE_RESPONSE="$(call_token_json GET "${BASE_URL}/v1/orchestrator/skillsets/resolve?pipeline=${PIPELINE_ID}" "${TOKEN}")"
assert_json_success "ResolveSkillsets" "${SKILLSET_RESOLVE_RESPONSE}"
assert_json_expr "ResolveSkillsets" "${SKILLSET_RESOLVE_RESPONSE}" '
  any((.data.skillsets // .skillsets // [])[]?; (.skillsetID // .skillset_id // "") == "'"${SKILLSET_NAME}"'")
'
assert_json_expr "ResolveSkillsets" "${SKILLSET_RESOLVE_RESPONSE}" '
  [
    (.data.skillsets // .skillsets // [])[]?
    | select((.skillsetID // .skillset_id // "") == "'"${SKILLSET_NAME}"'")
    | (.skills // [])[]
    | {skill_id:(.skillID // .skill_id // ""), capability:(.capability // ""), role:(.role // "executor")}
  ]
  | any(.[]; .skill_id == "'"${ELASTIC_SKILL_ID}"'" and .capability == "'"${SKILL_CAPABILITY}"'" and .role == "knowledge")
  and any(.[]; .skill_id == "'"${PROM_SKILL_ID}"'" and .capability == "'"${SKILL_CAPABILITY}"'" and .role == "knowledge")
  and any(.[]; .skill_id == "'"${EXECUTOR_SKILL_ID}"'" and .capability == "'"${SKILL_CAPABILITY}"'" and .role == "executor")
'

start_orchestrator

INCIDENT_CREATE_BODY='{"namespace":"default","workloadKind":"Deployment","workloadName":"prompt-first-evidence-smoke","service":"prompt-first-evidence-svc","severity":"P1"}'
INCIDENT_CREATE_RESPONSE="$(call_token_json POST "${BASE_URL}/v1/incidents" "${TOKEN}" "${INCIDENT_CREATE_BODY}")"
assert_json_success "CreateIncident" "${INCIDENT_CREATE_RESPONSE}"
INCIDENT_ID="$(printf '%s' "${INCIDENT_CREATE_RESPONSE}" | "${JQ_BIN}" -r '.data.incidentID // .data.incident_id // .incidentID // .incident_id // empty')"
if [[ -z "${INCIDENT_ID}" ]]; then
  fail_step "CreateIncidentParse" "$(printf '%s\n' "${INCIDENT_CREATE_RESPONSE}" | "${JQ_BIN}" .)"
fi

NOW_EPOCH="$(date -u +%s)"
START_EPOCH="$((NOW_EPOCH - 1800))"
RUN_BODY="$(cat <<JSON
{
  "incidentID": "${INCIDENT_ID}",
  "idempotencyKey": "idem-prompt-first-evidence-${INSTANCE_ID}",
  "pipeline": "${PIPELINE_ID}",
  "trigger": "manual",
  "timeRangeStart": {"seconds": ${START_EPOCH}, "nanos": 0},
  "timeRangeEnd": {"seconds": ${NOW_EPOCH}, "nanos": 0},
  "inputHintsJSON": "{\"scenario\":\"prompt_first_evidence_plan_smoke\"}",
  "createdBy": "system"
}
JSON
)"
RUN_RESPONSE="$(call_token_json POST "${BASE_URL}/v1/incidents/${INCIDENT_ID}/ai:run" "${TOKEN}" "${RUN_BODY}")"
assert_json_success "RunAIJob" "${RUN_RESPONSE}"
JOB_ID="$(printf '%s' "${RUN_RESPONSE}" | "${JQ_BIN}" -r '.data.jobID // .data.job_id // .jobID // .job_id // empty')"
if [[ -z "${JOB_ID}" ]]; then
  fail_step "RunAIJobParse" "$(printf '%s\n' "${RUN_RESPONSE}" | "${JQ_BIN}" .)"
fi

wait_for_job_succeeded "${TOKEN}"

TOOLCALLS_RESPONSE="$(call_token_json GET "${BASE_URL}/v1/ai/jobs/${JOB_ID}/tool-calls?offset=0&limit=200" "${TOKEN}")"
assert_json_success "ListAIToolCalls" "${TOOLCALLS_RESPONSE}"
assert_json_expr "SkillSelectObservation" "${TOOLCALLS_RESPONSE}" '
  any((.data.toolCalls // .toolCalls // [])[]?;
    (.toolName // .tool_name // "") == "skill.select"
    and (
      ((.responseJSON // .response_json // {}) | if type == "string" then (try fromjson catch {}) else . end)
      | ((.selection_role // "") == "knowledge" and ((.selected_skill_ids // []) | length) >= 2)
        or ((.selection_role // "") == "executor" and (.selected_binding_key // "") != "")
    )
  )
'
assert_json_expr "SkillConsumeObservation" "${TOOLCALLS_RESPONSE}" '
  any((.data.toolCalls // .toolCalls // [])[]?;
    (.toolName // .tool_name // "") == "skill.consume"
  )
'
assert_json_expr "KnowledgeResourceSelectObservation" "${TOOLCALLS_RESPONSE}" '
  any((.data.toolCalls // .toolCalls // [])[]?;
    (.toolName // .tool_name // "") == "skill.resource_select"
    and (
      ((.responseJSON // .response_json // {}) | if type == "string" then (try fromjson catch {}) else . end)
      | (.selected_resource_ids | type == "array")
      and (.selected_resource_ids | length) >= 1
    )
    and (.requestJSON // .request_json // {} | if type == "string" then (try fromjson catch {}) else . end | (.role // "") == "knowledge")
  )
'
assert_json_expr "ExecutorResourceSelectObservation" "${TOOLCALLS_RESPONSE}" '
  any((.data.toolCalls // .toolCalls // [])[]?;
    (.toolName // .tool_name // "") == "skill.resource_select"
    and (
      ((.responseJSON // .response_json // {}) | if type == "string" then (try fromjson catch {}) else . end)
      | (.selected_resource_ids | type == "array")
      and (.selected_resource_ids | length) >= 1
    )
    and (.requestJSON // .request_json // {} | if type == "string" then (try fromjson catch {}) else . end | (.role // "") == "executor")
  )
'
assert_json_expr "KnowledgeResourceLoadObservation" "${TOOLCALLS_RESPONSE}" '
  any((.data.toolCalls // .toolCalls // [])[]?;
    (.toolName // .tool_name // "") == "skill.resource_load"
    and (
      ((.responseJSON // .response_json // {}) | if type == "string" then (try fromjson catch {}) else . end)
      | (.resource_count // 0) >= 1
    )
    and (.requestJSON // .request_json // {} | if type == "string" then (try fromjson catch {}) else . end | (.role // "") == "knowledge")
  )
'
assert_json_expr "ExecutorResourceLoadObservation" "${TOOLCALLS_RESPONSE}" '
  any((.data.toolCalls // .toolCalls // [])[]?;
    (.toolName // .tool_name // "") == "skill.resource_load"
    and (
      ((.responseJSON // .response_json // {}) | if type == "string" then (try fromjson catch {}) else . end)
      | (.resource_count // 0) >= 1
    )
    and (.requestJSON // .request_json // {} | if type == "string" then (try fromjson catch {}) else . end | (.role // "") == "executor")
  )
'
assert_json_expr "PromptPlannerQueryMetricsToolCall" "${TOOLCALLS_RESPONSE}" '
  any((.data.toolCalls // .toolCalls // [])[]?;
    (.toolName // .tool_name // "") == "mcp.query_metrics"
    and (.nodeName // .node_name // "") == "skill.evidence.plan"
  )
'
assert_json_expr "PromptPlannerQueryLogsToolCall" "${TOOLCALLS_RESPONSE}" '
  any((.data.toolCalls // .toolCalls // [])[]?;
    (.toolName // .tool_name // "") == "mcp.query_logs"
    and (.nodeName // .node_name // "") == "skill.evidence.plan"
  )
'
assert_json_expr "MetricsReuseToolCall" "${TOOLCALLS_RESPONSE}" '
  any((.data.toolCalls // .toolCalls // [])[]?;
    (.toolName // .tool_name // "") == "evidence.metrics.reuse"
  )
'
assert_json_expr "LogsReuseToolCall" "${TOOLCALLS_RESPONSE}" '
  any((.data.toolCalls // .toolCalls // [])[]?;
    (.toolName // .tool_name // "") == "evidence.logs.reuse"
  )
'
assert_json_expr "PlanEvidenceSkillApplied" "${TOOLCALLS_RESPONSE}" '
  any((.data.toolCalls // .toolCalls // [])[]?;
    (.toolName // .tool_name // "") == "evidence.plan"
    and (
      ((.responseJSON // .response_json // {}) | if type == "string" then (try fromjson catch {}) else . end)
      | (.skill.status // "") == "applied"
      and (.evidence_plan.metadata.prompt_skill // "") == "'"${EXECUTOR_SKILL_ID}"'"
    )
  )
'

REPORT_PATH="${WORKDIR}/prompt_first_evidence_plan_smoke_report.json"
cat > "${REPORT_PATH}" <<JSON
{
  "generated_at": "$("${PYTHON_BIN}" - <<'PY'
from datetime import datetime, timezone
print(datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"))
PY
)",
  "base_url": $(json_escape "${BASE_URL}"),
  "pipeline_id": $(json_escape "${PIPELINE_ID}"),
  "skillset_name": $(json_escape "${SKILLSET_NAME}"),
  "knowledge_skill_ids": [$(json_escape "${ELASTIC_SKILL_ID}"), $(json_escape "${PROM_SKILL_ID}")],
  "executor_skill_id": $(json_escape "${EXECUTOR_SKILL_ID}"),
  "executor_skill_version": $(json_escape "${EXECUTOR_SKILL_VERSION}"),
  "instance_id": $(json_escape "${INSTANCE_ID}"),
  "incident_id": $(json_escape "${INCIDENT_ID}"),
  "job_id": $(json_escape "${JOB_ID}"),
  "worker_log": $(json_escape "${ORCH_LOG}"),
  "report": "prompt-first evidence.plan smoke passed with selective skill resource loading"
}
JSON

echo "PASS prompt-first evidence.plan smoke"
echo "incident_id=${INCIDENT_ID}"
echo "job_id=${JOB_ID}"
echo "worker_log=${ORCH_LOG}"
echo "report_path=${REPORT_PATH}"
