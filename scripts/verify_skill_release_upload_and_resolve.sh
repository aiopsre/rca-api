#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
SKILL_ARTIFACT_ENDPOINT="${SKILL_ARTIFACT_ENDPOINT:-http://192.168.39.3:9000}"
SKILL_ARTIFACT_BUCKET="${SKILL_ARTIFACT_BUCKET:-rca-skills-dev}"
OPERATOR_ID="${OPERATOR_ID:-bootstrap_super_admin}"
OPERATOR_PASSWORD="${OPERATOR_PASSWORD:-Admin123_}"
PIPELINE_ID="${PIPELINE_ID:-basic_rca}"
TEMPLATE_ID="${TEMPLATE_ID:-basic_rca}"
ORCHESTRATOR_INSTANCE_ID="${ORCHESTRATOR_INSTANCE_ID:-dev-verify}"
SKILLSET_NAME="${SKILLSET_NAME:-claude_default}"
SKILL_ID="${SKILL_ID:-claude.analysis}"
SKILL_VERSION="${SKILL_VERSION:-1.0.0}"
SKILL_CAPABILITY="${SKILL_CAPABILITY:-diagnosis.enrich}"
ALLOWED_TOOL="${ALLOWED_TOOL:-query_logs}"
SCOPES_HEADER="${SCOPES_HEADER:-config.admin,ai.read,ai.run}"
CURL_BIN="${CURL_BIN:-curl}"
JQ_BIN="${JQ_BIN:-jq}"
ZIP_BIN="${ZIP_BIN:-zip}"
PYTHON_BIN="${PYTHON_BIN:-python3}"
WORKDIR="${WORKDIR:-$(mktemp -d)}"
KEEP_WORKDIR="${KEEP_WORKDIR:-0}"

cleanup() {
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

login_token() {
  local payload
  if [[ -n "${OPERATOR_PASSWORD}" ]]; then
    payload='{"operator_id":"'"${OPERATOR_ID}"'","password":"'"${OPERATOR_PASSWORD}"'","scopes":["config.admin","ai.read","ai.run"]}'
  else
    payload='{"operator_id":"'"${OPERATOR_ID}"'","scopes":["config.admin","ai.read","ai.run"]}'
  fi
  ${CURL_BIN} -sS -X POST "${BASE_URL}/v1/auth/login" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json' \
    -d "${payload}" | ${JQ_BIN} -r '.token // .access_token // .data.token // empty'
}

call_json() {
  local method="$1"
  local url="$2"
  local token="$3"
  local body="${4:-}"
  if [[ -n "${body}" ]]; then
    ${CURL_BIN} -sS -X "${method}" "${url}" \
      -H "Authorization: Bearer ${token}" \
      -H "X-Scopes: ${SCOPES_HEADER}" \
      -H 'Accept: application/json' \
      -H 'Content-Type: application/json' \
      -d "${body}"
  else
    ${CURL_BIN} -sS -X "${method}" "${url}" \
      -H "Authorization: Bearer ${token}" \
      -H "X-Scopes: ${SCOPES_HEADER}" \
      -H 'Accept: application/json'
  fi
}

call_multipart() {
  local url="$1"
  local token="$2"
  local bundle_path="$3"
  ${CURL_BIN} -sS -X POST "${url}" \
    -H "Authorization: Bearer ${token}" \
    -H "X-Scopes: ${SCOPES_HEADER}" \
    -H 'Accept: application/json' \
    -F "bundle=@${bundle_path};type=application/zip" \
    -F "skill_id=${SKILL_ID}" \
    -F "version=${SKILL_VERSION}" \
    -F "status=active"
}

json_escape() {
  printf '%s' "$1" | ${JQ_BIN} -Rs .
}

assert_json_expr() {
  local step="$1"
  local json="$2"
  local expr="$3"
  if ! printf '%s' "${json}" | ${JQ_BIN} -e "${expr}" >/dev/null; then
    echo "FAIL ${step}: jq assertion failed: ${expr}" >&2
    printf '%s\n' "${json}" | ${JQ_BIN} .
    exit 1
  fi
}

assert_json_success() {
  local step="$1"
  local json="$2"
  if ! printf '%s' "${json}" | ${JQ_BIN} -e '((has("code") | not) or .code == 0)' >/dev/null; then
    echo "FAIL ${step}: response indicated failure" >&2
    printf '%s\n' "${json}" | ${JQ_BIN} .
    exit 1
  fi
}

require_bin "${CURL_BIN}"
require_bin "${JQ_BIN}"
require_bin "${ZIP_BIN}"
require_bin "${PYTHON_BIN}"
mkdir -p "${WORKDIR}"

if [[ "$(${CURL_BIN} -sS -o /dev/null -w '%{http_code}' "${BASE_URL}/healthz")" != "200" ]]; then
  echo "health check failed: ${BASE_URL}/healthz" >&2
  exit 1
fi

TOKEN="$(login_token)"
if [[ -z "${TOKEN}" ]]; then
  echo "failed to login config admin" >&2
  exit 1
fi

BUNDLE_ROOT="${WORKDIR}/bundle"
mkdir -p "${BUNDLE_ROOT}"

cat > "${BUNDLE_ROOT}/SKILL.md" <<'MD'
---
name: Claude Analysis Skill
description: Development verification bundle for SKILL.md upload and resolve checks.
compatibility: Prompt-only verification skill. Do not call tools.
---

This bundle exists only to verify upload, binding, and resolve flow for standard Claude-style Skills.
MD

BUNDLE_PATH="${WORKDIR}/${SKILL_ID}-${SKILL_VERSION}.zip"
(cd "${BUNDLE_ROOT}" && ${ZIP_BIN} -qr "${BUNDLE_PATH}" .)

UPLOAD_RESPONSE="$(call_multipart "${BASE_URL}/v1/config/skill-release/upload" "${TOKEN}" "${BUNDLE_PATH}")"
assert_json_success "UploadSkillRelease" "${UPLOAD_RESPONSE}"
assert_json_expr "UploadSkillRelease" "${UPLOAD_RESPONSE}" '(.data.skill_id // .skill_id) == "'"${SKILL_ID}"'"'
assert_json_expr "UploadSkillRelease" "${UPLOAD_RESPONSE}" '(.data.version // .version) == "'"${SKILL_VERSION}"'"'
assert_json_expr "UploadSkillRelease" "${UPLOAD_RESPONSE}" '(.data.artifact_url // .artifact_url) | startswith("s3://'"${SKILL_ARTIFACT_BUCKET}"'/skills/")'

SKILLSET_UPDATE_BODY="$(cat <<JSON
{
  "pipeline_id": "${PIPELINE_ID}",
  "skillset_name": "${SKILLSET_NAME}",
  "skills": [
    {
      "skill_id": "${SKILL_ID}",
      "version": "${SKILL_VERSION}",
      "capability": "${SKILL_CAPABILITY}",
      "allowed_tools": ["${ALLOWED_TOOL}"],
      "priority": 120,
      "enabled": true
    }
  ]
}
JSON
)"

SKILLSET_UPDATE_RESPONSE="$(call_json POST "${BASE_URL}/v1/config/skillset/update" "${TOKEN}" "${SKILLSET_UPDATE_BODY}")"
assert_json_success "UpsertSkillset" "${SKILLSET_UPDATE_RESPONSE}"
assert_json_expr "UpsertSkillset" "${SKILLSET_UPDATE_RESPONSE}" '(.data.pipeline_id // .pipeline_id) == "'"${PIPELINE_ID}"'"'

SKILL_RELEASE_GET_RESPONSE="$(call_json GET "${BASE_URL}/v1/config/skill-release/${SKILL_ID}/${SKILL_VERSION}" "${TOKEN}")"
assert_json_success "GetSkillRelease" "${SKILL_RELEASE_GET_RESPONSE}"
assert_json_expr "GetSkillRelease" "${SKILL_RELEASE_GET_RESPONSE}" '(.data.bundle_digest // .bundle_digest) | length == 64'

SKILLSET_GET_RESPONSE="$(call_json GET "${BASE_URL}/v1/config/skillset/${PIPELINE_ID}" "${TOKEN}")"
assert_json_success "GetSkillset" "${SKILLSET_GET_RESPONSE}"
assert_json_expr "GetSkillset" "${SKILLSET_GET_RESPONSE}" '(.data.items // .items)[0].skillset_name == "'"${SKILLSET_NAME}"'"'

TEMPLATE_REGISTER_BODY="$(cat <<JSON
{
  "instanceID": "${ORCHESTRATOR_INSTANCE_ID}",
  "templates": [
    {
      "templateID": "${TEMPLATE_ID}",
      "version": "dev"
    }
  ]
}
JSON
)"
TEMPLATE_REGISTER_RESPONSE="$(call_json POST "${BASE_URL}/v1/orchestrator/templates/register" "${TOKEN}" "${TEMPLATE_REGISTER_BODY}")"
assert_json_success "RegisterTemplates" "${TEMPLATE_REGISTER_RESPONSE}"
assert_json_expr "RegisterTemplates" "${TEMPLATE_REGISTER_RESPONSE}" '(.data.count // .count) >= 1'

STRATEGY_RESOLVE_RESPONSE="$(call_json GET "${BASE_URL}/v1/orchestrator/strategies/resolve?pipeline=${PIPELINE_ID}" "${TOKEN}")"
assert_json_success "ResolveStrategy" "${STRATEGY_RESOLVE_RESPONSE}"
assert_json_expr "ResolveStrategy" "${STRATEGY_RESOLVE_RESPONSE}" '(.data.strategy.skillsetIDs // .data.strategy.skillset_ids // .strategy.skillsetIDs // .strategy.skillset_ids // []) | index("'"${SKILLSET_NAME}"'") != null'

SKILLSET_RESOLVE_RESPONSE="$(call_json GET "${BASE_URL}/v1/orchestrator/skillsets/resolve?pipeline=${PIPELINE_ID}" "${TOKEN}")"
assert_json_success "ResolveSkillsets" "${SKILLSET_RESOLVE_RESPONSE}"
assert_json_expr "ResolveSkillsets" "${SKILLSET_RESOLVE_RESPONSE}" '(.data.skillsets // .skillsets)[0].skillsetID == "'"${SKILLSET_NAME}"'"'
assert_json_expr "ResolveSkillsets" "${SKILLSET_RESOLVE_RESPONSE}" '(.data.skillsets // .skillsets)[0].skills[0].skillID == "'"${SKILL_ID}"'"'
assert_json_expr "ResolveSkillsets" "${SKILLSET_RESOLVE_RESPONSE}" '(.data.skillsets // .skillsets)[0].skills[0].capability == "'"${SKILL_CAPABILITY}"'"'
assert_json_expr "ResolveSkillsets" "${SKILLSET_RESOLVE_RESPONSE}" '(.data.skillsets // .skillsets)[0].skills[0].priority == 120'
assert_json_expr "ResolveSkillsets" "${SKILLSET_RESOLVE_RESPONSE}" '(.data.skillsets // .skillsets)[0].skills[0].enabled == true'
assert_json_expr "ResolveSkillsets" "${SKILLSET_RESOLVE_RESPONSE}" '(.data.skillsets // .skillsets)[0].skills[0].artifactURL | startswith("'"${SKILL_ARTIFACT_ENDPOINT}"'/")'
assert_json_expr "ResolveSkillsets" "${SKILLSET_RESOLVE_RESPONSE}" '(.data.skillsets // .skillsets)[0].skills[0].artifactURL | contains("X-Amz-Signature=")'

REPORT_PATH="${WORKDIR}/skill_upload_resolve_report.json"
cat > "${REPORT_PATH}" <<JSON
{
  "generated_at": "$(${PYTHON_BIN} - <<'PY'
from datetime import datetime, timezone
print(datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"))
PY
)",
  "base_url": $(json_escape "${BASE_URL}"),
  "pipeline_id": $(json_escape "${PIPELINE_ID}"),
  "skill_id": $(json_escape "${SKILL_ID}"),
  "skill_version": $(json_escape "${SKILL_VERSION}"),
  "bundle_path": $(json_escape "${BUNDLE_PATH}"),
  "upload_response": ${UPLOAD_RESPONSE},
  "skillset_update_response": ${SKILLSET_UPDATE_RESPONSE},
  "skill_release_get_response": ${SKILL_RELEASE_GET_RESPONSE},
  "skillset_get_response": ${SKILLSET_GET_RESPONSE},
  "template_register_response": ${TEMPLATE_REGISTER_RESPONSE},
  "strategy_resolve_response": ${STRATEGY_RESOLVE_RESPONSE},
  "skillset_resolve_response": ${SKILLSET_RESOLVE_RESPONSE}
}
JSON

printf '%s\n' "upload ok: ${SKILL_ID}@${SKILL_VERSION}"
printf '%s\n' "skillset ok: ${SKILLSET_NAME} -> ${PIPELINE_ID}"
printf '%s\n' "resolve ok: presigned artifact url returned"
printf '%s\n' "report: ${REPORT_PATH}"
printf '%s\n' "workdir: ${WORKDIR}"
