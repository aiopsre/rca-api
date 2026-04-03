#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./lib/skills_release_helpers.sh
source "${SCRIPT_DIR}/lib/skills_release_helpers.sh"

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
SCOPES="${SCOPES:-*}"
CONFIG_SCOPES="${CONFIG_SCOPES:-config.admin,ai.read,ai.run}"
OPERATOR_ID="${OPERATOR_ID:-bootstrap_super_admin}"
OPERATOR_PASSWORD="${OPERATOR_PASSWORD:-Admin123_}"
PIPELINE_ID="${PIPELINE_ID:-basic_rca}"
SKILLSET_NAME="${SKILLSET_NAME:-prompt_first_elasticsearch_evidence_plan}"
SKILL_ID="${SKILL_ID:-elasticsearch.evidence.plan}"
SKILL_VERSION="${SKILL_VERSION:-1.0.0}"
SKILL_CAPABILITY="${SKILL_CAPABILITY:-evidence.plan}"
INSTANCE_ID="${INSTANCE_ID:-prompt-first-es-evidence-smoke-$$}"
JOB_WAIT_TIMEOUT_SEC="${JOB_WAIT_TIMEOUT_SEC:-180}"
JOB_POLL_INTERVAL_SEC="${JOB_POLL_INTERVAL_SEC:-1}"
ORCH_CMD="${ORCH_CMD:-python3 -m orchestrator.main}"
CURL_BIN="${CURL_BIN:-curl}"
JQ_BIN="${JQ_BIN:-jq}"
ZIP_BIN="${ZIP_BIN:-zip}"
PYTHON_BIN="${PYTHON_BIN:-python3}"
ORCH_DIR="${ORCH_DIR:-/opt/workspace/study/rca-api/tools/ai-orchestrator}"
BUNDLE_DIR="${BUNDLE_DIR:-/opt/workspace/study/rca-api/tools/ai-orchestrator/skill-bundles/elasticsearch-evidence-plan}"
WORKDIR="${WORKDIR:-$(mktemp -d)}"
KEEP_WORKDIR="${KEEP_WORKDIR:-0}"
DEBUG="${DEBUG:-0}"
SKILL_RELEASE_MODE="${SKILL_RELEASE_MODE:-upload}"
ARTIFACT_BASE_URL="${ARTIFACT_BASE_URL:-}"
ARTIFACT_DIR="${ARTIFACT_DIR:-}"

USE_MOCK_AGENT="${USE_MOCK_AGENT:-1}"
MOCK_AGENT_HOST="${MOCK_AGENT_HOST:-127.0.0.1}"
MOCK_AGENT_PORT="${MOCK_AGENT_PORT:-19131}"
AGENT_MODEL="${AGENT_MODEL:-mock-skill-agent}"
AGENT_BASE_URL="${AGENT_BASE_URL:-http://${MOCK_AGENT_HOST}:${MOCK_AGENT_PORT}/v1}"
AGENT_API_KEY="${AGENT_API_KEY:-mock-agent-key}"
AGENT_TIMEOUT_SECONDS="${AGENT_TIMEOUT_SECONDS:-20}"
MOCK_AGENT_PID=""
MOCK_AGENT_SCRIPT=""

USE_MOCK_DATASOURCE="${USE_MOCK_DATASOURCE:-1}"
MOCK_DS_HOST="${MOCK_DS_HOST:-127.0.0.1}"
MOCK_DS_PORT="${MOCK_DS_PORT:-19132}"
DS_BASE_URL="${DS_BASE_URL:-http://${MOCK_DS_HOST}:${MOCK_DS_PORT}}"
MOCK_DS_PID=""
MOCK_DS_SCRIPT=""

INCIDENT_SERVICE="${INCIDENT_SERVICE:-prompt-first-es-svc}"
INCIDENT_NAMESPACE="${INCIDENT_NAMESPACE:-default}"

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
  if [[ -n "${MOCK_AGENT_PID}" ]]; then
    kill "${MOCK_AGENT_PID}" >/dev/null 2>&1 || true
    wait "${MOCK_AGENT_PID}" >/dev/null 2>&1 || true
  fi
  if [[ -n "${MOCK_DS_PID}" ]]; then
    kill "${MOCK_DS_PID}" >/dev/null 2>&1 || true
    wait "${MOCK_DS_PID}" >/dev/null 2>&1 || true
  fi
  if [[ -n "${MOCK_AGENT_SCRIPT}" ]]; then
    rm -f "${MOCK_AGENT_SCRIPT}" >/dev/null 2>&1 || true
  fi
  if [[ -n "${MOCK_DS_SCRIPT}" ]]; then
    rm -f "${MOCK_DS_SCRIPT}" >/dev/null 2>&1 || true
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

wait_http_ok() {
  local url="$1"
  local attempts="${2:-30}"
  local delay="${3:-1}"
  local i
  for ((i = 1; i <= attempts; i++)); do
    if [[ "$("${CURL_BIN}" -sS -o /dev/null -w '%{http_code}' "${url}")" == "200" ]]; then
      return 0
    fi
    sleep "${delay}"
  done
  return 1
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
  local -a cmd
  cmd=(
    "${CURL_BIN}" -sS -X "${method}" "${url}"
    -H "Authorization: Bearer ${token}"
    -H "X-Scopes: ${CONFIG_SCOPES}"
    -H 'Accept: application/json'
  )
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

publish_skill_release() {
  local bundle_path="$1"
  skills_release_publish \
    "${SKILL_RELEASE_MODE}" \
    "${BASE_URL}" \
    "${TOKEN}" \
    "${CONFIG_SCOPES}" \
    "${CURL_BIN}" \
    "${JQ_BIN}" \
    "${PYTHON_BIN}" \
    "${BUNDLE_DIR}" \
    "${bundle_path}" \
    "${SKILL_ID}" \
    "${SKILL_VERSION}" \
    "${ARTIFACT_BASE_URL}" \
    "${ARTIFACT_DIR}" \
    "active"
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
  conflict_count="$(printf '%s' "${existing_json}" | "${JQ_BIN}" -r --arg skillset "${SKILLSET_NAME}" --arg skill_id "${SKILL_ID}" --arg version "${SKILL_VERSION}" '
    [
      (.data.items // .items // [])[]? as $item
      | ($item.skills // [])[]?
      | select((.enabled? == false) | not)
      | select((.capability // "") == "evidence.plan")
      | select(
          ($item.skillset_name // "") != $skillset
          or (.skill_id // "") != $skill_id
          or (.version // "") != $version
        )
    ] | length
  ')"
  if [[ "${conflict_count}" != "0" ]]; then
    fail_step "PreflightEvidencePlanConflict" "$(printf '%s\n' "${existing_json}" | "${JQ_BIN}" .)"
  fi
}

start_mock_agent() {
  [[ "${USE_MOCK_AGENT}" == "1" ]] || return 0

  MOCK_AGENT_SCRIPT="$(mktemp)"
  cat >"${MOCK_AGENT_SCRIPT}" <<'PY'
import json
import sys
import time
from http.server import BaseHTTPRequestHandler, HTTPServer

HOST = sys.argv[1]
PORT = int(sys.argv[2])
TARGET_SKILL_ID = sys.argv[3]


def _write(handler, status, payload):
    raw = json.dumps(payload).encode("utf-8")
    handler.send_response(status)
    handler.send_header("Content-Type", "application/json")
    handler.send_header("Content-Length", str(len(raw)))
    handler.end_headers()
    handler.wfile.write(raw)


def _extract_payload(body):
    messages = body.get("messages") if isinstance(body, dict) else None
    if not isinstance(messages, list) or not messages:
        return {}
    content = messages[-1].get("content")
    if isinstance(content, str):
        return json.loads(content)
    return {}


def _ecs_query(service, namespace):
    clauses = []
    if service:
        clauses.append(f'service.name:"{service}"')
    if namespace:
        clauses.append(f'(kubernetes.namespace_name:"{namespace}" OR service.namespace:"{namespace}")')
    clauses.append('(log.level:(error OR fatal) OR error.type:* OR error.message:* OR message:(*exception* OR *timeout* OR *panic*))')
    if not clauses:
        return 'message:(*error* OR *exception* OR *timeout* OR *panic*)'
    return " AND ".join(clauses)


class Handler(BaseHTTPRequestHandler):
    def log_message(self, format, *args):
        return

    def do_GET(self):
        if self.path == "/healthz":
            _write(self, 200, {"ok": True})
            return
        _write(self, 404, {"error": "not_found"})

    def do_POST(self):
        if not self.path.endswith("/chat/completions"):
            _write(self, 404, {"error": "not_found"})
            return
        length = int(self.headers.get("Content-Length", "0") or "0")
        body = json.loads(self.rfile.read(length) or b"{}")
        payload = _extract_payload(body)
        response_obj = {}
        if isinstance(payload.get("candidates"), list):
            selected_binding_key = ""
            for item in payload.get("candidates") or []:
                if not isinstance(item, dict):
                    continue
                if str(item.get("skill_id") or "").strip() == TARGET_SKILL_ID:
                    selected_binding_key = str(item.get("binding_key") or "").strip()
                    break
            if not selected_binding_key:
                first = next((item for item in payload.get("candidates") or [] if isinstance(item, dict)), {})
                selected_binding_key = str(first.get("binding_key") or "").strip()
            response_obj = {
                "selected_binding_key": selected_binding_key,
                "reason": "prefer Elasticsearch-specific evidence planning knowledge",
            }
        elif isinstance(payload.get("available_tools"), list):
            input_payload = payload.get("input") if isinstance(payload.get("input"), dict) else {}
            incident_context = input_payload.get("incident_context") if isinstance(input_payload.get("incident_context"), dict) else {}
            logs_branch_meta = input_payload.get("logs_branch_meta") if isinstance(input_payload.get("logs_branch_meta"), dict) else {}
            request_payload = logs_branch_meta.get("request_payload") if isinstance(logs_branch_meta.get("request_payload"), dict) else {}
            service = str(incident_context.get("service") or "").strip()
            namespace = str(incident_context.get("namespace") or "").strip()
            query_text = _ecs_query(service, namespace)
            response_obj = {
                "tool": "mcp.query_logs",
                "input": {
                    "datasource_id": str(request_payload.get("datasource_id") or ""),
                    "query": query_text,
                    "start_ts": int(request_payload.get("start_ts") or 0),
                    "end_ts": int(request_payload.get("end_ts") or 0),
                    "limit": int(request_payload.get("limit") or 200),
                },
                "reason": "use one ECS-shaped log query to warm the logs branch before native query reuse",
            }
        elif isinstance(payload.get("tool_request"), dict) and isinstance(payload.get("tool_result"), dict):
            tool_request = payload.get("tool_request") if isinstance(payload.get("tool_request"), dict) else {}
            query_text = str(tool_request.get("query") or "").strip()
            response_obj = {
                "payload": {
                    "evidence_plan_patch": {
                        "metadata": {
                            "prompt_skill": TARGET_SKILL_ID,
                            "query_style": "ecs_query_string",
                            "planning_note": "narrow logs query using ECS-style service/namespace/error filters",
                        }
                    },
                    "metrics_branch_meta": {
                        "mode": "skip",
                        "query_type": "metrics",
                        "reason": "elasticsearch-specific prompt skill is validating logs only in this smoke",
                    },
                    "logs_branch_meta": {
                        "mode": "query",
                        "query_type": "logs",
                        "request_payload": {
                            "query": query_text,
                        },
                        "query_request": {
                            "queryText": query_text,
                        },
                    },
                },
                "observations": [
                    {
                        "kind": "note",
                        "message": "elasticsearch ecs query plan applied after tool result",
                    }
                ],
            }
        else:
            input_payload = payload.get("input") if isinstance(payload.get("input"), dict) else {}
            incident_context = input_payload.get("incident_context") if isinstance(input_payload.get("incident_context"), dict) else {}
            service = str(incident_context.get("service") or "").strip()
            namespace = str(incident_context.get("namespace") or "").strip()
            query_text = _ecs_query(service, namespace)
            response_obj = {
                "payload": {
                    "evidence_plan_patch": {
                        "metadata": {
                            "prompt_skill": TARGET_SKILL_ID,
                            "query_style": "ecs_query_string",
                            "planning_note": "narrow logs query using ECS-style service/namespace/error filters",
                        }
                    },
                    "logs_branch_meta": {
                        "mode": "query",
                        "query_type": "logs",
                        "request_payload": {
                            "query": query_text,
                            "datasource_id": "forbidden-ds",
                            "limit": 10,
                        },
                        "query_request": {
                            "queryText": query_text,
                            "queryJSON": "{\"forbidden\":true}",
                        },
                    },
                },
                "observations": [
                    {
                        "kind": "note",
                        "message": "elasticsearch ecs query plan applied",
                    }
                ],
            }
        response = {
            "id": f"chatcmpl-mock-{int(time.time())}",
            "object": "chat.completion",
            "created": int(time.time()),
            "model": str(body.get("model") or "mock-skill-agent"),
            "choices": [
                {
                    "index": 0,
                    "message": {
                        "role": "assistant",
                        "content": json.dumps(response_obj, ensure_ascii=False),
                    },
                    "finish_reason": "stop",
                }
            ],
            "usage": {
                "prompt_tokens": 1,
                "completion_tokens": 1,
                "total_tokens": 2,
            },
        }
        _write(self, 200, response)


HTTPServer((HOST, PORT), Handler).serve_forever()
PY
  "${PYTHON_BIN}" "${MOCK_AGENT_SCRIPT}" "${MOCK_AGENT_HOST}" "${MOCK_AGENT_PORT}" "${SKILL_ID}" >/dev/null 2>&1 &
  MOCK_AGENT_PID="$!"
  if ! wait_http_ok "http://${MOCK_AGENT_HOST}:${MOCK_AGENT_PORT}/healthz" 20 1; then
    fail_step "MockAgentStart" "mock agent failed to start on ${MOCK_AGENT_HOST}:${MOCK_AGENT_PORT}"
  fi
}

start_mock_datasource() {
  [[ "${USE_MOCK_DATASOURCE}" == "1" ]] || return 0

  MOCK_DS_SCRIPT="$(mktemp)"
  cat >"${MOCK_DS_SCRIPT}" <<'PY'
import json
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer

HOST = sys.argv[1]
PORT = int(sys.argv[2])


def _write(handler, status, payload):
    raw = json.dumps(payload).encode("utf-8")
    handler.send_response(status)
    handler.send_header("Content-Type", "application/json")
    handler.send_header("Content-Length", str(len(raw)))
    handler.end_headers()
    handler.wfile.write(raw)


class Handler(BaseHTTPRequestHandler):
    def log_message(self, format, *args):
        return

    def do_GET(self):
        if self.path == "/healthz":
            _write(self, 200, {"ok": True})
            return
        if self.path.startswith("/api/v1/query_range"):
            _write(
                self,
                200,
                {
                    "status": "success",
                    "data": {
                        "resultType": "matrix",
                        "result": [
                            {
                                "metric": {"__name__": "up", "job": "mock"},
                                "values": [[1710000000, "1"]],
                            }
                        ],
                    },
                },
            )
            return
        _write(self, 404, {"error": "not_found"})

    def do_POST(self):
        if self.path.startswith("/_search"):
            _write(
                self,
                200,
                {
                    "hits": {
                        "hits": [
                            {
                                "_source": {
                                    "@timestamp": "2026-03-14T00:00:00Z",
                                    "message": "mock elasticsearch result",
                                }
                            }
                        ]
                    }
                },
            )
            return
        _write(self, 404, {"error": "not_found"})


HTTPServer((HOST, PORT), Handler).serve_forever()
PY
  "${PYTHON_BIN}" "${MOCK_DS_SCRIPT}" "${MOCK_DS_HOST}" "${MOCK_DS_PORT}" >/dev/null 2>&1 &
  MOCK_DS_PID="$!"
  if ! wait_http_ok "http://${MOCK_DS_HOST}:${MOCK_DS_PORT}/healthz" 20 1; then
    fail_step "MockDatasourceStart" "mock datasource failed to start on ${MOCK_DS_HOST}:${MOCK_DS_PORT}"
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
      DS_TYPE=elasticsearch \
      AUTO_CREATE_DATASOURCE=1 \
      SKILLS_TOOL_CALLING_MODE=evidence_plan_single_hop \
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

if [[ ! -d "${BUNDLE_DIR}" ]]; then
  fail_step "SkillBundlePath" "bundle dir not found: ${BUNDLE_DIR}"
fi
if [[ "$("${CURL_BIN}" -sS -o /dev/null -w '%{http_code}' "${BASE_URL}/healthz")" != "200" ]]; then
  fail_step "Healthz" "health check failed: ${BASE_URL}/healthz"
fi

mkdir -p "${WORKDIR}"
start_mock_agent
start_mock_datasource

TOKEN="$(login_token)"
if [[ -z "${TOKEN}" ]]; then
  fail_step "Login" "failed to obtain config.admin token"
fi

preflight_no_conflicting_evidence_skill "${TOKEN}"

TEMPLATE_REGISTER_BODY='{"instanceID":"'"${INSTANCE_ID}"'","templates":[{"templateID":"basic_rca","version":""}]}'
TEMPLATE_REGISTER_RESPONSE="$(call_token_json POST "${BASE_URL}/v1/orchestrator/templates/register" "${TOKEN}" "${TEMPLATE_REGISTER_BODY}")"
assert_json_success "RegisterTemplate" "${TEMPLATE_REGISTER_RESPONSE}"
assert_json_expr "RegisterTemplate" "${TEMPLATE_REGISTER_RESPONSE}" '(.data.count // .count // 0) >= 1'

BUNDLE_PATH="${WORKDIR}/${SKILL_ID}-${SKILL_VERSION}.zip"
(cd "${BUNDLE_DIR}" && "${ZIP_BIN}" -qr "${BUNDLE_PATH}" .)

UPLOAD_RESPONSE="$(publish_skill_release "${BUNDLE_PATH}")"
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
      "allowed_tools": ["query_logs"],
      "priority": 150,
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

INCIDENT_CREATE_BODY='{"namespace":"'"${INCIDENT_NAMESPACE}"'","workloadKind":"Deployment","workloadName":"prompt-first-es-evidence-smoke","service":"'"${INCIDENT_SERVICE}"'","severity":"P1"}'
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
  "idempotencyKey": "idem-prompt-first-es-evidence-${INSTANCE_ID}",
  "pipeline": "${PIPELINE_ID}",
  "trigger": "manual",
  "timeRangeStart": {"seconds": ${START_EPOCH}, "nanos": 0},
  "timeRangeEnd": {"seconds": ${NOW_EPOCH}, "nanos": 0},
  "inputHintsJSON": "{\"scenario\":\"prompt_first_elasticsearch_evidence_plan_smoke\"}",
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
  )
'
assert_json_expr "SkillConsumeObservation" "${TOOLCALLS_RESPONSE}" '
  any((.data.toolCalls // .toolCalls // [])[]?;
    (.toolName // .tool_name // "") == "skill.consume"
  )
'
assert_json_expr "SkillToolPlanObservation" "${TOOLCALLS_RESPONSE}" '
  any((.data.toolCalls // .toolCalls // [])[]?;
    (.toolName // .tool_name // "") == "skill.tool_plan"
  )
'
assert_json_expr "ElasticsearchEvidencePlanApplied" "${TOOLCALLS_RESPONSE}" '
  any((.data.toolCalls // .toolCalls // [])[]?;
    (.toolName // .tool_name // "") == "evidence.plan"
    and (
      ((.responseJSON // .response_json // {}) | if type == "string" then (try fromjson catch {}) else . end)
      | (.skill.status // "") == "applied"
      and (.skill.skill_id // "") == "'"${SKILL_ID}"'"
      and (.evidence_plan.metadata.prompt_skill // "") == "'"${SKILL_ID}"'"
      and (.evidence_plan.metadata.query_style // "") == "ecs_query_string"
    )
  )
'
assert_json_expr "QueryLogsIncludesECSService" "${TOOLCALLS_RESPONSE}" '
  any((.data.toolCalls // .toolCalls // [])[]?;
    (.toolName // .tool_name // "") == "mcp.query_logs"
    and (.nodeName // .node_name // "") == "skill.evidence.plan"
    and (
      ((.requestJSON // .request_json // {}) | if type == "string" then (try fromjson catch {}) else . end)
      | (.query // "")
      | test("service\\.name:\\\"" + "'"${INCIDENT_SERVICE}"'" + "\\\"")
    )
  )
'
assert_json_expr "QueryLogsIncludesECSNamespace" "${TOOLCALLS_RESPONSE}" '
  any((.data.toolCalls // .toolCalls // [])[]?;
    (.toolName // .tool_name // "") == "mcp.query_logs"
    and (.nodeName // .node_name // "") == "skill.evidence.plan"
    and (
      ((.requestJSON // .request_json // {}) | if type == "string" then (try fromjson catch {}) else . end)
      | (.query // "")
      | test("kubernetes\\.namespace_name:\\\"" + "'"${INCIDENT_NAMESPACE}"'" + "\\\"")
    )
  )
'
assert_json_expr "QueryLogsIncludesECSErrorFilter" "${TOOLCALLS_RESPONSE}" '
  any((.data.toolCalls // .toolCalls // [])[]?;
    (.toolName // .tool_name // "") == "mcp.query_logs"
    and (.nodeName // .node_name // "") == "skill.evidence.plan"
    and (
      ((.requestJSON // .request_json // {}) | if type == "string" then (try fromjson catch {}) else . end)
      | (.query // "")
      | test("log\\.level:\\(error OR fatal\\)")
    )
  )
'
assert_json_expr "QueryLogsReuseObserved" "${TOOLCALLS_RESPONSE}" '
  any((.data.toolCalls // .toolCalls // [])[]?;
    (.toolName // .tool_name // "") == "evidence.logs.reuse"
    and (
      ((.responseJSON // .response_json // {}) | if type == "string" then (try fromjson catch {}) else . end)
      | (.source // "") == "skill_prompt_first"
    )
  )
'

REPORT_PATH="${WORKDIR}/prompt_first_elasticsearch_evidence_plan_smoke_report.json"
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
  "agent_base_url": $(json_escape "${AGENT_BASE_URL}"),
  "datasource_base_url": $(json_escape "${DS_BASE_URL}"),
  "report": "prompt-first elasticsearch evidence.plan smoke passed"
}
JSON

echo "PASS prompt-first elasticsearch evidence.plan smoke"
echo "incident_id=${INCIDENT_ID}"
echo "job_id=${JOB_ID}"
echo "worker_log=${ORCH_LOG}"
echo "report_path=${REPORT_PATH}"
