#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
SCOPES="${SCOPES:-*}"
CONFIG_SCOPES="${CONFIG_SCOPES:-config.admin,ai.read,ai.run}"
OPERATOR_ID="${OPERATOR_ID:-bootstrap_super_admin}"
OPERATOR_PASSWORD="${OPERATOR_PASSWORD:-Admin123_}"
PIPELINE_ID="${PIPELINE_ID:-basic_rca}"
SKILLSET_NAME="${SKILLSET_NAME:-prompt_first_diagnosis_enrich}"
SKILL_ID="${SKILL_ID:-claude.diagnosis.prompt_enricher}"
SKILL_VERSION="${SKILL_VERSION:-1.0.0}"
SKILL_CAPABILITY="${SKILL_CAPABILITY:-diagnosis.enrich}"
INSTANCE_ID="${INSTANCE_ID:-prompt-first-smoke-$$}"
JOB_WAIT_TIMEOUT_SEC="${JOB_WAIT_TIMEOUT_SEC:-180}"
JOB_POLL_INTERVAL_SEC="${JOB_POLL_INTERVAL_SEC:-1}"
AGENT_MODEL="${AGENT_MODEL:-}"
AGENT_BASE_URL="${AGENT_BASE_URL:-}"
AGENT_API_KEY="${AGENT_API_KEY:-}"
AGENT_TIMEOUT_SECONDS="${AGENT_TIMEOUT_SECONDS:-20}"
ORCH_CMD="${ORCH_CMD:-python3 -m orchestrator.main}"
CURL_BIN="${CURL_BIN:-curl}"
JQ_BIN="${JQ_BIN:-jq}"
ZIP_BIN="${ZIP_BIN:-zip}"
PYTHON_BIN="${PYTHON_BIN:-python3}"
ORCH_DIR="${ORCH_DIR:-/opt/workspace/study/rca-api/tools/ai-orchestrator}"
BUNDLE_DIR="${BUNDLE_DIR:-/opt/workspace/study/rca-api/tools/ai-orchestrator/skill-bundles/diagnosis-enrich}"
WORKDIR="${WORKDIR:-$(mktemp -d)}"
KEEP_WORKDIR="${KEEP_WORKDIR:-0}"
DEBUG="${DEBUG:-0}"

NATIVE_SUMMARY="Suspected root cause based on consistent available evidence."
NATIVE_ROOT_CAUSE_STATEMENT="Metrics and logs indicate correlated service degradation in the same window."

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
  "${CURL_BIN}" -sS -X POST "${url}" \
    -H "Authorization: Bearer ${token}" \
    -H "X-Scopes: ${CONFIG_SCOPES}" \
    -H 'Accept: application/json' \
    -F "bundle=@${bundle_path};type=application/zip" \
    -F "skill_id=${SKILL_ID}" \
    -F "version=${SKILL_VERSION}" \
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

preflight_no_conflicting_diagnosis_skill() {
  local token="$1"
  local existing_json
  existing_json="$(call_token_json GET "${BASE_URL}/v1/config/skillset/${PIPELINE_ID}" "${token}")"
  assert_json_success "GetSkillsetConfig" "${existing_json}"
  local conflict_count
  conflict_count="$(printf '%s' "${existing_json}" | "${JQ_BIN}" -r --arg skillset "${SKILLSET_NAME}" --arg skill_id "${SKILL_ID}" --arg version "${SKILL_VERSION}" '
    [
      (.data.items // .items // [])[]? as $item
      | ($item.skills // [])[]?
      | select((.capability // "") == "diagnosis.enrich")
      | select(
          ($item.skillset_name // "") != $skillset
          or (.skill_id // "") != $skill_id
          or (.version // "") != $version
        )
    ] | length
  ')"
  if [[ "${conflict_count}" != "0" ]]; then
    fail_step "PreflightDiagnosisSkillConflict" "$(printf '%s\n' "${existing_json}" | "${JQ_BIN}" .)"
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
      RUN_QUERY=0 \
      RUN_VERIFICATION=0 \
      POST_FINALIZE_OBSERVE=0 \
      SKILLS_EXECUTION_MODE=prompt_first \
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
if [[ ! -d "${BUNDLE_DIR}" ]]; then
  fail_step "SkillBundlePath" "bundle dir not found: ${BUNDLE_DIR}"
fi
if [[ "$("${CURL_BIN}" -sS -o /dev/null -w '%{http_code}' "${BASE_URL}/healthz")" != "200" ]]; then
  fail_step "Healthz" "health check failed: ${BASE_URL}/healthz"
fi

mkdir -p "${WORKDIR}"
TOKEN="$(login_token)"
if [[ -z "${TOKEN}" ]]; then
  fail_step "Login" "failed to obtain config.admin token"
fi

preflight_no_conflicting_diagnosis_skill "${TOKEN}"

TEMPLATE_REGISTER_BODY='{"instanceID":"'"${INSTANCE_ID}"'","templates":[{"templateID":"basic_rca","version":""}]}'
TEMPLATE_REGISTER_RESPONSE="$(call_token_json POST "${BASE_URL}/v1/orchestrator/templates/register" "${TOKEN}" "${TEMPLATE_REGISTER_BODY}")"
assert_json_success "RegisterTemplate" "${TEMPLATE_REGISTER_RESPONSE}"
assert_json_expr "RegisterTemplate" "${TEMPLATE_REGISTER_RESPONSE}" '(.data.count // .count // 0) >= 1'

BUNDLE_PATH="${WORKDIR}/${SKILL_ID}-${SKILL_VERSION}.zip"
(cd "${BUNDLE_DIR}" && "${ZIP_BIN}" -qr "${BUNDLE_PATH}" .)

UPLOAD_RESPONSE="$(call_token_multipart "${BASE_URL}/v1/config/skill-release/upload" "${TOKEN}" "${BUNDLE_PATH}")"
assert_json_success "UploadSkillRelease" "${UPLOAD_RESPONSE}"
assert_json_expr "UploadSkillRelease" "${UPLOAD_RESPONSE}" '(.data.skill_id // .skill_id) == "'"${SKILL_ID}"'"'
assert_json_expr "UploadSkillRelease" "${UPLOAD_RESPONSE}" '(.data.version // .version) == "'"${SKILL_VERSION}"'"'

SKILLSET_UPDATE_BODY="$(cat <<JSON
{
  "pipeline_id": "${PIPELINE_ID}",
  "skillset_name": "${SKILLSET_NAME}",
  "skills": [
    {
      "skill_id": "${SKILL_ID}",
      "version": "${SKILL_VERSION}",
      "capability": "${SKILL_CAPABILITY}",
      "allowed_tools": [],
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
  any((.data.skillsets // .skillsets // [])[]?.skills[]?;
    (.skillID // .skill_id // "") == "'"${SKILL_ID}"'"
    and (.capability // "") == "'"${SKILL_CAPABILITY}"'"
  )
'

start_orchestrator

INCIDENT_CREATE_BODY='{"namespace":"default","workloadKind":"Deployment","workloadName":"prompt-first-smoke","service":"prompt-first-svc","severity":"P1"}'
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
  "idempotencyKey": "idem-prompt-first-${INSTANCE_ID}",
  "pipeline": "${PIPELINE_ID}",
  "trigger": "manual",
  "timeRangeStart": {"seconds": ${START_EPOCH}, "nanos": 0},
  "timeRangeEnd": {"seconds": ${NOW_EPOCH}, "nanos": 0},
  "inputHintsJSON": "{\"scenario\":\"prompt_first_diagnosis_enrich_smoke\"}",
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

INCIDENT_GET_RESPONSE="$(call_token_json GET "${BASE_URL}/v1/incidents/${INCIDENT_ID}" "${TOKEN}")"
assert_json_success "GetIncident" "${INCIDENT_GET_RESPONSE}"
DIAGNOSIS_RAW="$(printf '%s' "${INCIDENT_GET_RESPONSE}" | "${JQ_BIN}" -r '
  .data.incident.diagnosisJSON // .data.incident.diagnosis_json // .data.diagnosisJSON // .data.diagnosis_json // .incident.diagnosisJSON // .incident.diagnosis_json // .diagnosisJSON // .diagnosis_json // empty
')"
if [[ -z "${DIAGNOSIS_RAW}" ]]; then
  fail_step "GetIncidentDiagnosis" "$(printf '%s\n' "${INCIDENT_GET_RESPONSE}" | "${JQ_BIN}" .)"
fi
assert_json_expr "DiagnosisSummaryChanged" "${DIAGNOSIS_RAW}" '.summary != "'"${NATIVE_SUMMARY}"'"'
assert_json_expr "DiagnosisRootCauseChanged" "${DIAGNOSIS_RAW}" '.root_cause.statement != "'"${NATIVE_ROOT_CAUSE_STATEMENT}"'"'
assert_json_expr "DiagnosisRecommendations" "${DIAGNOSIS_RAW}" '(.recommendations // []) | length > 0'
assert_json_expr "DiagnosisNextSteps" "${DIAGNOSIS_RAW}" '(.next_steps // []) | length > 0'

SESSION_RESPONSE="$(call_token_json GET "${BASE_URL}/v1/ai/jobs/${JOB_ID}/session-context" "${TOKEN}" "" "X-Orchestrator-Instance-ID" "${INSTANCE_ID}")"
assert_json_success "GetJobSessionContext" "${SESSION_RESPONSE}"
assert_json_expr "SessionSkillMarker" "${SESSION_RESPONSE}" '
  (.data.context_state.skills.diagnosis_enrich.applied // .context_state.skills.diagnosis_enrich.applied) == true
'

TOOLCALLS_RESPONSE="$(call_token_json GET "${BASE_URL}/v1/ai/jobs/${JOB_ID}/tool-calls?offset=0&limit=200" "${TOKEN}")"
assert_json_success "ListAIToolCalls" "${TOOLCALLS_RESPONSE}"
assert_json_expr "SkillSelectObservation" "${TOOLCALLS_RESPONSE}" '
  any((.data.toolCalls // .toolCalls // [])[]?;
    (.toolName // .tool_name // "") == "skill.select"
  )
'
assert_json_expr "SkillConsumeObservation" "${TOOLCALLS_RESPONSE}" '
  any((.data.toolCalls // .toolCalls // [])[]?;
    (.toolName // .tool_name // "") == "skill.consume"
  )
'

REPORT_PATH="${WORKDIR}/prompt_first_diagnosis_enrich_smoke_report.json"
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
  "skill_id": $(json_escape "${SKILL_ID}"),
  "skill_version": $(json_escape "${SKILL_VERSION}"),
  "instance_id": $(json_escape "${INSTANCE_ID}"),
  "incident_id": $(json_escape "${INCIDENT_ID}"),
  "job_id": $(json_escape "${JOB_ID}"),
  "worker_log": $(json_escape "${ORCH_LOG}"),
  "report": "prompt-first diagnosis.enrich smoke passed"
}
JSON

echo "PASS prompt-first diagnosis.enrich smoke"
echo "incident_id=${INCIDENT_ID}"
echo "job_id=${JOB_ID}"
echo "worker_log=${ORCH_LOG}"
echo "report_path=${REPORT_PATH}"
