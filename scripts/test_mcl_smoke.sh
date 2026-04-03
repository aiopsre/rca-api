#!/usr/bin/env bash
# Minimal Closed Loop Smoke Test
#
# This script tests the 8 core MCL steps from the curl runbook:
# 0. Bootstrap Tempo skill release and tool surface
# 1. Create notice channel
# 2. Ingest alert event
# 3. Trigger AI job
# 4. Poll job to terminal state
# 5. Read session context
# 6. View tool call audit
# 7. View diagnosis writeback
# 8. Poll notice delivery
#
# Usage:
#   ./scripts/test_mcl_smoke.sh
#
# Environment variables:
#   BASE_URL - API server URL (default: http://127.0.0.1:5555)
#   SCOPES - Scopes header (default: *)
#   ORCH_INSTANCE_ID - Orchestrator instance ID (default: mcl-smoke-test)
#   BOOTSTRAP_TEMPO_SMOKE - Run Tempo skill release bootstrap first (default: 1)
#   MCL_INGEST_BODY_FILE - Optional path to a JSON file used as the ingest body
#   MCL_INGEST_BODY_JSON - Optional raw JSON string used as the ingest body
#
set -euo pipefail

# Configuration
BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
SCOPES="${SCOPES:-*}"
ORCH_INSTANCE_ID="${ORCH_INSTANCE_ID:-mcl-smoke-test}"
BOOTSTRAP_TEMPO_SMOKE="${BOOTSTRAP_TEMPO_SMOKE:-1}"
RAND="${RAND:-$RANDOM}"
WORKDIR="${WORKDIR:-/tmp/rca-mcl-smoke-${RAND}}"

# ============================================================================
# Helper functions (from test_ai_job_smoke.sh)
# ============================================================================

need_cmd() { command -v "$1" >/dev/null 2>&1; }

http_json() {
  local method="$1"; shift
  local url="$1"; shift
  local body="${1:-}"
  local -a headers
  headers=(-H "X-Scopes: ${SCOPES}")
  if [[ "${method}" == "POST" ]] && [[ "${url}" =~ /v1/ai/jobs/[^/]+/(start|tool-calls|finalize|cancel|heartbeat|session-context)$ ]]; then
    headers+=(-H "X-Orchestrator-Instance-ID: ${ORCH_INSTANCE_ID}")
  fi
  # Also add for session-context GET
  if [[ "${method}" == "GET" ]] && [[ "${url}" =~ /session-context$ ]]; then
    headers+=(-H "X-Orchestrator-Instance-ID: ${ORCH_INSTANCE_ID}")
  fi

  if [[ -n "$body" ]]; then
    curl -sS -i -X "$method" "$url" \
      -H 'Content-Type: application/json' \
      "${headers[@]}" \
      -d "$body"
  else
    curl -sS -i -X "$method" "$url" \
      "${headers[@]}"
  fi
}

status_code() { awk 'NR==1 {print $2}'; }

body_only() { awk 'BEGIN{p=0} /^\r?$/{p=1; next} {if(p) print}'; }

extract_field() {
  local json="$1"
  local field="$2"
  if need_cmd jq; then
    echo "$json" | jq -r ".${field} // .data.${field} // empty"
  else
    echo "$json" | sed -n "s/.*\"${field}\":\"\\([^\"]*\\)\".*/\\1/p"
  fi
}

json_string() {
  local raw="$1"
  if need_cmd jq; then
    printf '%s' "$raw" | jq -Rs .
  else
    printf '"%s"' "$(printf '%s' "$raw" | sed 's/\\/\\\\/g; s/"/\\"/g')"
  fi
}

assert_2xx() {
  local step="$1"
  local resp="$2"
  local code
  code="$(echo "$resp" | status_code)"
  if [[ ! "$code" =~ ^2 ]]; then
    echo "FAIL ${step}: HTTP ${code}"
    echo "---- response ----"
    echo "$resp"
    echo "------------------"
    exit 1
  fi
}

assert_contains() {
  local step="$1"
  local text="$2"
  local pattern="$3"
  if ! echo "$text" | grep -q "$pattern"; then
    echo "FAIL ${step}: expected pattern ${pattern}"
    echo "---- body ----"
    echo "$text"
    echo "-------------"
    exit 1
  fi
}

bootstrap_tempo_smoke() {
  if [[ "${BOOTSTRAP_TEMPO_SMOKE}" != "1" ]]; then
    echo "Skipping Tempo smoke bootstrap"
    return 0
  fi

  echo "Bootstrapping Tempo skill release + tool surface..."
  BASE_URL="${BASE_URL}" python3 -m pytest -q tools/ai-orchestrator/tests/smoke/test_tempo_skill_release_smoke.py -k tempo
  echo "Tempo bootstrap complete"
  echo ""
}

load_ingest_body() {
  local now_epoch
  now_epoch="$(date -u +%s)"
  if [[ -n "${MCL_INGEST_BODY_JSON:-}" ]]; then
    printf '%s' "${MCL_INGEST_BODY_JSON}"
    return 0
  fi
  if [[ -n "${MCL_INGEST_BODY_FILE:-}" ]]; then
    cat "${MCL_INGEST_BODY_FILE}"
    return 0
  fi

  cat <<EOF
{"idempotencyKey":"idem-mcl-smoke-${RAND}","fingerprint":"mcl-smoke-fp-${RAND}","status":"firing","severity":"P1","service":"mcl-smoke-svc","cluster":"mcl-smoke-cluster","namespace":"default","workload":"mcl-smoke-workload","labelsJSON":"{\"alertname\":\"MCLSmokeTest\",\"service\":\"mcl-smoke-svc\"}","lastSeenAt":{"seconds":${now_epoch},"nanos":0}}
EOF
}

# ============================================================================
# MCL-specific functions
# ============================================================================

create_notice_channel() {
  local body
  body=$(cat <<EOF
{"name":"mcl-smoke-channel-${RAND}","type":"webhook","enabled":true,"endpointURL":"http://mock-webhook:8080/mcl","timeoutMs":1000,"maxRetries":2}
EOF
)
  local resp json id
  resp="$(http_json POST "${BASE_URL}/v1/notice-channels" "$body")"
  assert_2xx "CreateNoticeChannel" "$resp"
  json="$(echo "$resp" | body_only)"
  id="$(echo "$json" | jq -r '.noticeChannel.channelID // .channelID // .data.noticeChannel.channelID // .data.channelID // empty')"
  if [[ -z "$id" ]]; then
    echo "FAIL CreateNoticeChannel: cannot parse channelID"
    echo "$json"
    exit 1
  fi
  echo "$id"
}

ingest_alert_event() {
  local now_epoch start_epoch
  now_epoch="$(date -u +%s)"
  start_epoch="$((now_epoch - 1800))"

  local body
  body="$(load_ingest_body)"
  local resp json incident_id event_id
  resp="$(http_json POST "${BASE_URL}/v1/alert-events:ingest" "$body")"
  assert_2xx "IngestAlertEvent" "$resp"
  json="$(echo "$resp" | body_only)"
  incident_id="$(echo "$json" | jq -r '.incidentID // .incident_id // .data.incidentID // .data.incident_id // empty')"
  event_id="$(echo "$json" | jq -r '.eventID // .event_id // .data.eventID // .data.event_id // empty')"
  if [[ -z "$incident_id" ]]; then
    echo "FAIL IngestAlertEvent: cannot parse incidentID"
    echo "$json"
    exit 1
  fi
  echo "${incident_id}:${event_id}"
}

trigger_ai_job() {
  local incident_id="$1"
  local event_id="${2:-}"
  local now_epoch start_epoch
  now_epoch="$(date -u +%s)"
  start_epoch="$((now_epoch - 1800))"

  local hints
  hints='{"scenario":"MCL_SMOKE"}'
  if [[ -n "$event_id" ]]; then
    hints="{\"scenario\":\"MCL_SMOKE\",\"event_id\":\"${event_id}\"}"
  fi

  local body
  body=$(cat <<EOF
{"incidentID":"${incident_id}","idempotencyKey":"idem-mcl-smoke-run-${RAND}","pipeline":"basic_rca","trigger":"manual","timeRangeStart":{"seconds":${start_epoch},"nanos":0},"timeRangeEnd":{"seconds":${now_epoch},"nanos":0},"inputHintsJSON":$(json_string "$hints"),"createdBy":"system"}
EOF
)
  local resp json job_id
  resp="$(http_json POST "${BASE_URL}/v1/incidents/${incident_id}/ai:run" "$body")"
  assert_2xx "TriggerAIJob" "$resp"
  json="$(echo "$resp" | body_only)"
  job_id="$(echo "$json" | jq -r '.jobID // .job_id // .data.jobID // .data.job_id // empty')"
  if [[ -z "$job_id" ]]; then
    echo "FAIL TriggerAIJob: cannot parse jobID"
    echo "$json"
    exit 1
  fi
  echo "$job_id"
}

poll_job_status() {
  local job_id="$1"
  local max_polls="${2:-120}"
  local resp json status

  for i in $(seq 1 "$max_polls"); do
    resp="$(http_json GET "${BASE_URL}/v1/ai/jobs/${job_id}")"
    json="$(echo "$resp" | body_only)"
    status="$(echo "$json" | jq -r '.job.status // .status // .data.job.status // .data.status // empty')"

    echo "  poll=${i} status=${status}"

    case "$status" in
      succeeded)
        echo "  Job succeeded"
        return 0
        ;;
      failed|canceled)
        echo "FAIL PollJobStatus: job terminal status=${status}"
        echo "$json" | jq .
        exit 1
        ;;
      *)
        sleep 1
        ;;
    esac
  done

  echo "FAIL PollJobStatus: timeout after ${max_polls}s"
  exit 1
}

read_session_context() {
  local job_id="$1"
  local resp json session_id

  resp="$(http_json GET "${BASE_URL}/v1/ai/jobs/${job_id}/session-context")"
  if [[ "$(echo "$resp" | status_code)" != "200" ]]; then
    echo "  WARN: session-context returned non-200"
    echo ""
    return
  fi

  json="$(echo "$resp" | body_only)"
  session_id="$(echo "$json" | jq -r '.session_id // .sessionID // .data.session_id // .data.sessionID // empty')"
  echo "$session_id"
}

count_tool_calls() {
  local job_id="$1"
  local resp json count

  resp="$(http_json GET "${BASE_URL}/v1/ai/jobs/${job_id}/tool-calls?offset=0&limit=50")"
  if [[ "$(echo "$resp" | status_code)" != "200" ]]; then
    echo "0"
    return
  fi

  json="$(echo "$resp" | body_only)"
  count="$(echo "$json" | jq -r '(.toolCalls // .data.toolCalls // []) | length')"
  echo "$count"
}

verify_diagnosis_writeback() {
  local incident_id="$1"
  local resp json rca_status root_cause_summary

  resp="$(http_json GET "${BASE_URL}/v1/incidents/${incident_id}")"
  assert_2xx "VerifyDiagnosisWriteback" "$resp"

  json="$(echo "$resp" | body_only)"
  rca_status="$(echo "$json" | jq -r '.incident.rcaStatus // .incident.rca_status // .rcaStatus // .rca_status // .data.incident.rcaStatus // .data.incident.rca_status // empty')"
  root_cause_summary="$(echo "$json" | jq -r '.incident.rootCauseSummary // .incident.root_cause_summary // .rootCauseSummary // .root_cause_summary // .data.incident.rootCauseSummary // .data.incident.root_cause_summary // empty')"

  if [[ "$rca_status" != "done" ]]; then
    echo "FAIL VerifyDiagnosisWriteback: rca_status=${rca_status}, expected 'done'"
    exit 1
  fi

  if [[ -z "$root_cause_summary" ]]; then
    echo "FAIL VerifyDiagnosisWriteback: root_cause_summary is empty"
    exit 1
  fi

  echo "  rca_status=${rca_status}"
  echo "  root_cause_summary=${root_cause_summary}"
}

poll_notice_delivery() {
  local incident_id="$1"
  local channel_id="$2"
  local event_type="$3"
  local max_polls="${4:-60}"
  local resp json status

  for i in $(seq 1 "$max_polls"); do
    resp="$(http_json GET "${BASE_URL}/v1/notice-deliveries?incident_id=${incident_id}&channel_id=${channel_id}&event_type=${event_type}")"
    json="$(echo "$resp" | body_only)"
    status="$(echo "$json" | jq -r '(.noticeDeliveries // .data.noticeDeliveries // [])[0].status // empty')"

    echo "  poll=${i} notice_status=${status:-EMPTY}"

    if [[ "$status" == "succeeded" ]]; then
      return 0
    fi
    sleep 1
  done

  echo "WARN: notice delivery not succeeded after ${max_polls}s"
  return 0  # Non-blocking: notice is optional for MCL
}

# ============================================================================
# Main
# ============================================================================

main() {
  echo "============================================"
  echo "Minimal Closed Loop Smoke Test"
  echo "============================================"
  echo ""
  echo "BASE_URL=${BASE_URL}"
  echo "SCOPES=${SCOPES}"
  echo "ORCH_INSTANCE_ID=${ORCH_INSTANCE_ID}"
  echo "RAND=${RAND}"
  echo ""

  # Create workdir
  mkdir -p "${WORKDIR}"
  echo "WORKDIR=${WORKDIR}"
  echo ""

  # Check health
  echo "Checking API health..."
  if ! curl -fsS "${BASE_URL}/healthz" > /dev/null 2>&1; then
    echo "FAIL: API server not healthy at ${BASE_URL}"
    echo "Start compose first:"
    echo "  docker compose -f deploy/compose/docker-compose.redis.yaml --profile mock up -d"
    exit 1
  fi
  echo "API server is healthy"
  echo ""

  # Step 0: Bootstrap Tempo skill release and tool surface.
  bootstrap_tempo_smoke

  # Step 1: Create notice channel
  echo "Step 1: Create notice channel..."
  CHANNEL_ID="$(create_notice_channel)"
  echo "  CHANNEL_ID=${CHANNEL_ID}"
  echo ""

  # Step 2: Ingest alert event
  echo "Step 2: Ingest alert event..."
  RESULT="$(ingest_alert_event)"
  INCIDENT_ID="${RESULT%%:*}"
  EVENT_ID="${RESULT#*:}"
  echo "  INCIDENT_ID=${INCIDENT_ID}"
  echo "  EVENT_ID=${EVENT_ID}"
  echo ""

  # Step 3: Trigger AI job
  echo "Step 3: Trigger AI job..."
  JOB_ID="$(trigger_ai_job "$INCIDENT_ID" "$EVENT_ID")"
  echo "  JOB_ID=${JOB_ID}"
  echo ""

  # Step 4: Poll job to terminal state
  echo "Step 4: Poll job to terminal state..."
  poll_job_status "$JOB_ID"
  echo ""

  # Step 5: Read session context
  echo "Step 5: Read session context..."
  SESSION_ID="$(read_session_context "$JOB_ID")"
  echo "  SESSION_ID=${SESSION_ID:-EMPTY}"
  echo ""

  # Step 6: View tool call audit
  echo "Step 6: View tool call audit..."
  TOOL_CALL_COUNT="$(count_tool_calls "$JOB_ID")"
  echo "  TOOL_CALL_COUNT=${TOOL_CALL_COUNT}"
  if [[ "$TOOL_CALL_COUNT" -lt 1 ]]; then
    echo "FAIL: Expected at least 1 tool call"
    exit 1
  fi
  echo ""

  # Step 7: Verify diagnosis writeback
  echo "Step 7: Verify diagnosis writeback..."
  verify_diagnosis_writeback "$INCIDENT_ID"
  echo ""

  # Step 8: Poll notice delivery
  echo "Step 8: Poll notice delivery..."
  if [[ -n "$CHANNEL_ID" ]]; then
    poll_notice_delivery "$INCIDENT_ID" "$CHANNEL_ID" "diagnosis_written"
  else
    echo "  Skipped: no channel_id"
  fi
  echo ""

  # Summary
  echo "============================================"
  echo "PASS MCL Smoke Test"
  echo "============================================"
  echo "  INCIDENT_ID=${INCIDENT_ID}"
  echo "  JOB_ID=${JOB_ID}"
  echo "  SESSION_ID=${SESSION_ID:-}"
  echo "  TOOL_CALL_COUNT=${TOOL_CALL_COUNT}"
  echo ""
}

# Cleanup
cleanup() {
  if [[ "${KEEP_WORKDIR:-0}" != "1" ]] && [[ -n "${WORKDIR:-}" ]] && [[ -d "${WORKDIR}" ]]; then
    rm -rf "${WORKDIR}"
  fi
}
trap cleanup EXIT

# Run
main "$@"
