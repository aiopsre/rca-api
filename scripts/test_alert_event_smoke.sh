#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
ACK_PATH_TEMPLATE="${ACK_PATH_TEMPLATE:-/v1/alert-events/%s/ack}"

need_cmd() { command -v "$1" >/dev/null 2>&1; }

http_json() {
  local method="$1"; shift
  local url="$1"; shift
  local body="${1:-}"

  if [[ -n "$body" ]]; then
    $CURL -sS -i -X "$method" "$url" \
      -H 'Content-Type: application/json' \
      -H "X-Scopes: ${SCOPES}" \
      -d "$body"
  else
    $CURL -sS -i -X "$method" "$url" \
      -H "X-Scopes: ${SCOPES}"
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

extract_bool() {
  local json="$1"
  local field="$2"
  if need_cmd jq; then
    echo "$json" | jq -r ".${field} // .data.${field} // false"
  else
    echo "$json" | grep -q "\"${field}\":true" && echo "true" || echo "false"
  fi
}

extract_total_count() {
  local json="$1"
  if need_cmd jq; then
    echo "$json" | jq -r '.totalCount // .data.totalCount // 0'
  else
    echo "$json" | sed -n 's/.*"totalCount":\([0-9]*\).*/\1/p'
  fi
}

assert_2xx() {
  local step="$1"
  local resp="$2"
  local code
  code="$(echo "$resp" | status_code)"
  if [[ ! "$code" =~ ^2 ]]; then
    echo "FAIL ${step}: HTTP ${code}"
    echo "$resp"
    exit 1
  fi
}

echo "BASE_URL=${BASE_URL}"
echo "SCOPES=${SCOPES}"
echo "ACK_PATH_TEMPLATE=${ACK_PATH_TEMPLATE}"

if [[ "${ACK_PATH_TEMPLATE}" != *"%s"* ]]; then
  echo "FAIL ACK_PATH_TEMPLATE must contain %s placeholder"
  exit 1
fi

rand="${RAND:-$RANDOM}"
fingerprint="${FINGERPRINT:-smoke-fp-${rand}}"
idem1="idem-alert-smoke-${rand}-1"
idem2="idem-alert-smoke-${rand}-2"
now_epoch="$(date -u +%s)"
event_epoch_1="${EVENT_EPOCH_1:-$((now_epoch - 300))}"
event_epoch_2="${EVENT_EPOCH_2:-$((now_epoch))}"

ingest_body_1=$(cat <<EOF
{"idempotencyKey":"${idem1}","fingerprint":"${fingerprint}","status":"firing","severity":"P1","service":"smoke-svc","cluster":"prod-smoke","namespace":"smoke","workload":"smoke-api","lastSeenAt":{"seconds":${event_epoch_1},"nanos":0},"labelsJSON":"{\"alertname\":\"HTTP5xxHigh\",\"service\":\"smoke-svc\",\"cluster\":\"prod-smoke\",\"namespace\":\"smoke\"}"}
EOF
)

ingest_resp_1="$(http_json POST "${BASE_URL}/v1/alert-events:ingest" "$ingest_body_1")"
assert_2xx "Ingest#1" "$ingest_resp_1"
ingest_json_1="$(echo "$ingest_resp_1" | body_only)"
event_id_1="$(extract_field "$ingest_json_1" "eventID")"
incident_id_1="$(extract_field "$ingest_json_1" "incidentID")"
if [[ -z "${event_id_1}" ]] || [[ -z "${incident_id_1}" ]]; then
  echo "FAIL Ingest#1: missing eventID/incidentID"
  echo "$ingest_json_1"
  exit 1
fi
echo "PASS Ingest#1 event_id=${event_id_1} incident_id=${incident_id_1}"

ingest_resp_1_retry="$(http_json POST "${BASE_URL}/v1/alert-events:ingest" "$ingest_body_1")"
assert_2xx "Ingest#1Retry" "$ingest_resp_1_retry"
ingest_json_1_retry="$(echo "$ingest_resp_1_retry" | body_only)"
event_id_1_retry="$(extract_field "$ingest_json_1_retry" "eventID")"
reused="$(extract_bool "$ingest_json_1_retry" "reused")"
if [[ "${event_id_1}" != "${event_id_1_retry}" ]] || [[ "${reused}" != "true" ]]; then
  echo "FAIL Ingest#1Retry: expected same eventID and reused=true"
  echo "$ingest_json_1_retry"
  exit 1
fi

ingest_body_2=$(cat <<EOF
{"idempotencyKey":"${idem2}","fingerprint":"${fingerprint}","status":"firing","severity":"critical","service":"smoke-svc","cluster":"prod-smoke","namespace":"smoke","workload":"smoke-api","lastSeenAt":{"seconds":${event_epoch_2},"nanos":0}}
EOF
)
ingest_resp_2="$(http_json POST "${BASE_URL}/v1/alert-events:ingest" "$ingest_body_2")"
assert_2xx "Ingest#2Merge" "$ingest_resp_2"

current_resp="$(http_json GET "${BASE_URL}/v1/alert-events:current?fingerprint=${fingerprint}&offset=0&limit=20")"
assert_2xx "CurrentList" "$current_resp"
current_json="$(echo "$current_resp" | body_only)"
current_total="$(extract_total_count "$current_json")"
if [[ "${current_total}" != "1" ]]; then
  echo "FAIL CurrentList: expected totalCount=1"
  echo "$current_json"
  exit 1
fi

history_resp="$(http_json GET "${BASE_URL}/v1/alert-events:history?fingerprint=${fingerprint}&offset=0&limit=20")"
assert_2xx "HistoryList" "$history_resp"
history_json="$(echo "$history_resp" | body_only)"
history_total="$(extract_total_count "$history_json")"
if [[ "${history_total}" != "2" ]]; then
  echo "FAIL HistoryList: expected totalCount=2"
  echo "$history_json"
  exit 1
fi

ack_body='{"ackedBy":"user:smoke"}'
ack_path="$(printf "${ACK_PATH_TEMPLATE}" "${event_id_1}")"
ack_resp="$(http_json POST "${BASE_URL}${ack_path}" "$ack_body")"
assert_2xx "AckEvent" "$ack_resp"

echo "PASS alert-event smoke fingerprint=${fingerprint}"
