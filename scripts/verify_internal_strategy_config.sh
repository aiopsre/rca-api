#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
OPERATOR_ID="${OPERATOR_ID:-operator:config-admin}"
SESSION_ID="${SESSION_ID:-}"
REPORT_JSON="${REPORT_JSON:-./_output/strategy_config_verify_report.json}"
REPORT_CSV="${REPORT_CSV:-./_output/strategy_config_verify_report.csv}"

CURL_BIN="${CURL_BIN:-curl}"
JQ_BIN="${JQ_BIN:-jq}"

require_bin() {
  local bin="$1"
  if ! command -v "${bin}" >/dev/null 2>&1; then
    echo "missing required binary: ${bin}" >&2
    exit 1
  fi
}

json_escape() {
  printf '%s' "$1" | ${JQ_BIN} -Rs .
}

login_token() {
  local payload
  payload='{"operator_id":"'"${OPERATOR_ID}"'","scopes":["config.admin","ai.read","ai.run"]}'
  ${CURL_BIN} -sS -X POST "${BASE_URL}/v1/auth/login" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json' \
    -d "${payload}" | ${JQ_BIN} -r '.data.token // empty'
}

call_json() {
  local method="$1"
  local url="$2"
  local token="$3"
  local body="${4:-}"
  if [[ -n "${body}" ]]; then
    ${CURL_BIN} -sS -X "${method}" "${url}" \
      -H "Authorization: Bearer ${token}" \
      -H 'Accept: application/json' \
      -H 'Content-Type: application/json' \
      -d "${body}"
  else
    ${CURL_BIN} -sS -X "${method}" "${url}" \
      -H "Authorization: Bearer ${token}" \
      -H 'Accept: application/json'
  fi
}

status_and_body() {
  local method="$1"
  local url="$2"
  local token="$3"
  local body="${4:-}"
  local out_file
  out_file="$(mktemp)"
  local code
  if [[ -n "${body}" ]]; then
    code="$(${CURL_BIN} -sS -o "${out_file}" -w '%{http_code}' -X "${method}" "${url}" \
      -H "Authorization: Bearer ${token}" \
      -H 'Accept: application/json' \
      -H 'Content-Type: application/json' \
      -d "${body}")"
  else
    code="$(${CURL_BIN} -sS -o "${out_file}" -w '%{http_code}' -X "${method}" "${url}" \
      -H "Authorization: Bearer ${token}" \
      -H 'Accept: application/json')"
  fi
  printf '%s\n' "${code}"
  cat "${out_file}"
  rm -f "${out_file}"
}

require_bin "${CURL_BIN}"
require_bin "${JQ_BIN}"
mkdir -p "$(dirname "${REPORT_JSON}")"
mkdir -p "$(dirname "${REPORT_CSV}")"

if [[ "$(${CURL_BIN} -sS -o /dev/null -w '%{http_code}' "${BASE_URL}/healthz")" != "200" ]]; then
  echo "health check failed: ${BASE_URL}/healthz" >&2
  exit 1
fi

TOKEN="$(login_token)"
if [[ -z "${TOKEN}" ]]; then
  echo "failed to login config admin" >&2
  exit 1
fi

# Read bootstrap dynamic defaults then dynamic trigger mapping.
trigger_before="$(call_json GET "${BASE_URL}/v1/config/trigger/manual" "${TOKEN}")"
trigger_update_body='{"trigger_type":"manual","pipeline_id":"advanced_rca","session_type":"incident","fallback":false}'
trigger_update="$(call_json POST "${BASE_URL}/v1/config/trigger/update" "${TOKEN}" "${trigger_update_body}")"
trigger_after="$(call_json GET "${BASE_URL}/v1/config/trigger/manual" "${TOKEN}")"

# Read fallback then dynamic SLA config.
sla_before="$(call_json GET "${BASE_URL}/v1/config/sla/incident" "${TOKEN}")"
sla_update_body='{"session_type":"incident","due_seconds":1800,"escalation_thresholds":[1800,3600]}'
sla_update="$(call_json POST "${BASE_URL}/v1/config/sla/update" "${TOKEN}" "${sla_update_body}")"
sla_after="$(call_json GET "${BASE_URL}/v1/config/sla/incident" "${TOKEN}")"

# Toolset dynamic update.
toolset_update_body='{"pipeline_id":"advanced_rca","toolset_name":"dynamic_config_toolset","allowed_tools":["logs.search","metrics.query_range"]}'
toolset_update="$(call_json POST "${BASE_URL}/v1/config/toolset/update" "${TOKEN}" "${toolset_update_body}")"
toolset_after="$(call_json GET "${BASE_URL}/v1/config/toolset/advanced_rca" "${TOKEN}")"

# Session assignment verification (best-effort if session exists).
if [[ -z "${SESSION_ID}" ]]; then
  SESSION_ID="$(call_json GET "${BASE_URL}/v1/operator/inbox?offset=0&limit=1" "${TOKEN}" | ${JQ_BIN} -r '.data.items[0].session_id // .data.items[0].sessionID // empty')"
fi

assignment_status="skipped"
assignment_assign='{}'
assignment_read='{}'
if [[ -n "${SESSION_ID}" ]]; then
  assign_body='{"assignee":"user:oncall-verify","note":"verify-script"}'
  readarray -t assign_resp < <(status_and_body POST "${BASE_URL}/v1/session/${SESSION_ID}/assign" "${TOKEN}" "${assign_body}")
  assign_code="${assign_resp[0]}"
  assign_body_raw="${assign_resp[@]:1}"
  if [[ "${assign_code}" == "200" ]]; then
    assignment_status="ok"
    assignment_assign="${assign_body_raw}"
    assignment_read="$(call_json GET "${BASE_URL}/v1/session/${SESSION_ID}/assignment" "${TOKEN}")"
  else
    assignment_status="failed"
    assignment_assign="${assign_body_raw}"
  fi
fi

report_json_payload=$(cat <<JSON
{
  "generated_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "base_url": "${BASE_URL}",
  "checks": {
    "trigger_before": ${trigger_before},
    "trigger_update": ${trigger_update},
    "trigger_after": ${trigger_after},
    "sla_before": ${sla_before},
    "sla_update": ${sla_update},
    "sla_after": ${sla_after},
    "toolset_update": ${toolset_update},
    "toolset_after": ${toolset_after},
    "assignment": {
      "session_id": "${SESSION_ID}",
      "status": "${assignment_status}",
      "assign_response": ${assignment_assign},
      "read_response": ${assignment_read}
    }
  },
  "summary": {
    "trigger_source_before": $(echo "${trigger_before}" | ${JQ_BIN} -r '.trigger_type? as $noop | .source // .data.source // empty' | json_escape),
    "trigger_pipeline_after": $(echo "${trigger_after}" | ${JQ_BIN} -r '.pipeline_id // .data.pipeline_id // empty' | json_escape),
    "sla_due_after": $(echo "${sla_after}" | ${JQ_BIN} -r '.due_seconds // .data.due_seconds // empty' | json_escape),
    "toolset_source_after": $(echo "${toolset_after}" | ${JQ_BIN} -r '.source // .data.source // empty' | json_escape),
    "assignment_status": "${assignment_status}"
  }
}
JSON
)

printf '%s\n' "${report_json_payload}" | ${JQ_BIN} . > "${REPORT_JSON}"

cat > "${REPORT_CSV}" <<CSV
check,expected,actual,status
trigger.source_before,dynamic_db,$(echo "${trigger_before}" | ${JQ_BIN} -r '.source // .data.source // empty'),$( [[ "$(echo "${trigger_before}" | ${JQ_BIN} -r '.source // .data.source // empty')" == "dynamic_db" ]] && echo pass || echo fail )
trigger.pipeline_after,advanced_rca,$(echo "${trigger_after}" | ${JQ_BIN} -r '.pipeline_id // .data.pipeline_id // empty'),$( [[ "$(echo "${trigger_after}" | ${JQ_BIN} -r '.pipeline_id // .data.pipeline_id // empty')" == "advanced_rca" ]] && echo pass || echo fail )
sla.due_seconds_after,1800,$(echo "${sla_after}" | ${JQ_BIN} -r '.due_seconds // .data.due_seconds // empty'),$( [[ "$(echo "${sla_after}" | ${JQ_BIN} -r '.due_seconds // .data.due_seconds // empty')" == "1800" ]] && echo pass || echo fail )
toolset.source_after,dynamic_db,$(echo "${toolset_after}" | ${JQ_BIN} -r '.source // .data.source // empty'),$( [[ "$(echo "${toolset_after}" | ${JQ_BIN} -r '.source // .data.source // empty')" == "dynamic_db" ]] && echo pass || echo fail )
assignment.status,ok_or_skipped,${assignment_status},$( [[ "${assignment_status}" == "ok" || "${assignment_status}" == "skipped" ]] && echo pass || echo fail )
CSV

echo "verification report json: ${REPORT_JSON}"
echo "verification report csv: ${REPORT_CSV}"
