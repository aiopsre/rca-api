#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
RUN_QUERY="${RUN_QUERY:-0}"
AUTO_CREATE_DATASOURCE="${AUTO_CREATE_DATASOURCE:-1}"

need_cmd() { command -v "$1" >/dev/null 2>&1; }

http_json() {
  # Usage: http_json METHOD URL [JSON_BODY]
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
    echo "$json" | jq -r ".${field} // empty"
  else
    echo "$json" | sed -n "s/.*\"${field}\":\"\\([^\"]*\\)\".*/\\1/p"
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

create_incident() {
  local rand="$1"
  local body
  body=$(cat <<EOF
{"namespace":"default","workloadKind":"Deployment","workloadName":"evidence-smoke-${rand}","service":"evidence-svc-${rand}","severity":"P1"}
EOF
)
  local resp json id
  resp="$(http_json POST "${BASE_URL}/v1/incidents" "$body")"
  assert_2xx "CreateIncident" "$resp"
  json="$(echo "$resp" | body_only)"
  id="$(extract_field "$json" "incidentID")"
  if [[ -z "$id" ]]; then
    echo "FAIL CreateIncident: cannot parse incidentID"
    echo "$json"
    exit 1
  fi
  echo "$id"
}

create_datasource() {
  local rand="$1"
  local name="evidence-ds-${rand}"
  local base_url="${DS_BASE_URL:-http://127.0.0.1:9090}"
  local body
  body=$(cat <<EOF
{"type":"prometheus","name":"${name}","baseURL":"${base_url}","authType":"none","timeoutMs":5000,"isEnabled":true}
EOF
)
  local resp json id
  resp="$(http_json POST "${BASE_URL}/v1/datasources" "$body")"
  assert_2xx "CreateDatasource" "$resp"
  json="$(echo "$resp" | body_only)"
  id="$(extract_field "$json" "datasourceID")"
  if [[ -z "$id" ]]; then
    echo "FAIL CreateDatasource: cannot parse datasourceID"
    echo "$json"
    exit 1
  fi
  echo "$id"
}

echo "BASE_URL=${BASE_URL}"
echo "SCOPES=${SCOPES}"
echo "RUN_QUERY=${RUN_QUERY}"

rand="${RAND:-$RANDOM}"
incident_id="${INCIDENT_ID:-}"
datasource_id="${DATASOURCE_ID:-}"

if [[ -z "${incident_id}" ]]; then
  incident_id="$(create_incident "$rand")"
fi
echo "incident_id=${incident_id}"

if [[ -z "${datasource_id}" ]] && [[ "${AUTO_CREATE_DATASOURCE}" == "1" ]]; then
  datasource_id="$(create_datasource "$rand")"
fi
echo "datasource_id=${datasource_id:-<empty>}"

now_epoch="$(date -u +%s)"
start_epoch="$((now_epoch - 1800))"

query_result='{"status":"skipped","data":{"reason":"RUN_QUERY=0"}}'
if [[ "${RUN_QUERY}" == "1" ]]; then
  if [[ -z "${datasource_id}" ]]; then
    echo "FAIL QueryMetrics: datasource_id is empty, set DATASOURCE_ID or AUTO_CREATE_DATASOURCE=1"
    exit 1
  fi
  query_body=$(cat <<EOF
{"datasourceID":"${datasource_id}","promql":"up","timeRangeStart":{"seconds":${start_epoch},"nanos":0},"timeRangeEnd":{"seconds":${now_epoch},"nanos":0},"stepSeconds":30}
EOF
)
  query_resp="$(http_json POST "${BASE_URL}/v1/evidence:queryMetrics" "$query_body")"
  assert_2xx "QueryMetrics" "$query_resp"
  query_json="$(echo "$query_resp" | body_only)"
  if need_cmd jq; then
    query_result="$(echo "$query_json" | jq -c '.queryResultJSON // "{}"')"
    query_result="$(echo "$query_result" | sed 's/^"//;s/"$//' | sed 's/\\"/"/g')"
  else
    query_result='{"status":"ok"}'
  fi
fi

idem_key="idem-evidence-${rand}"
save_body=$(cat <<EOF
{"incidentID":"${incident_id}","idempotencyKey":"${idem_key}","type":"metrics","datasourceID":"${datasource_id}","queryText":"up","queryJSON":"{\"promql\":\"up\"}","timeRangeStart":{"seconds":${start_epoch},"nanos":0},"timeRangeEnd":{"seconds":${now_epoch},"nanos":0},"resultJSON":$(printf '%s' "$query_result" | jq -Rs . 2>/dev/null || printf '"{\"status\":\"ok\"}"'),"summary":"evidence smoke","createdBy":"system"}
EOF
)

save_resp="$(http_json POST "${BASE_URL}/v1/incidents/${incident_id}/evidence" "$save_body")"
assert_2xx "SaveEvidence" "$save_resp"
save_json="$(echo "$save_resp" | body_only)"
evidence_id="$(extract_field "$save_json" "evidenceID")"
if [[ -z "${evidence_id}" ]]; then
  echo "FAIL SaveEvidence: cannot parse evidenceID"
  echo "$save_json"
  exit 1
fi
echo "PASS SaveEvidence evidence_id=${evidence_id}"

# Idempotency retry should return same evidence_id.
save2_resp="$(http_json POST "${BASE_URL}/v1/incidents/${incident_id}/evidence" "$save_body")"
assert_2xx "SaveEvidenceIdempotent" "$save2_resp"
save2_json="$(echo "$save2_resp" | body_only)"
evidence_id2="$(extract_field "$save2_json" "evidenceID")"
if [[ "${evidence_id}" != "${evidence_id2}" ]]; then
  echo "FAIL SaveEvidenceIdempotent: id changed ${evidence_id} -> ${evidence_id2}"
  exit 1
fi

list_resp="$(http_json GET "${BASE_URL}/v1/incidents/${incident_id}/evidence?offset=0&limit=50")"
assert_2xx "ListEvidenceByIncident" "$list_resp"
list_json="$(echo "$list_resp" | body_only)"
assert_contains "ListEvidenceByIncident" "$list_json" "\"evidenceID\":\"${evidence_id}\""

get_resp="$(http_json GET "${BASE_URL}/v1/evidence/${evidence_id}")"
assert_2xx "GetEvidence" "$get_resp"
get_json="$(echo "$get_resp" | body_only)"
assert_contains "GetEvidence" "$get_json" "\"evidenceID\":\"${evidence_id}\""
assert_contains "GetEvidence" "$get_json" "\"incidentID\":\"${incident_id}\""

echo "PASS evidence smoke incident_id=${incident_id} evidence_id=${evidence_id}"
