#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"

# ---- helpers ----
need_cmd() { command -v "$1" >/dev/null 2>&1; }

http_json() {
  # Usage: http_json METHOD URL [JSON_BODY]
  local method="$1"; shift
  local url="$1"; shift
  local body="${1:-}"

  if [[ -n "$body" ]]; then
    $CURL -sS -i -X "$method" "$url" \
      -H 'Content-Type: application/json' \
      -d "$body"
  else
    $CURL -sS -i -X "$method" "$url"
  fi
}

status_code() {
  # Extract first HTTP status code from curl -i output
  awk 'NR==1 {print $2}'
}

body_only() {
  # Strip headers from curl -i output
  awk 'BEGIN{p=0} /^\r?$/{p=1; next} {if(p) print}'
}

extract_incident_id() {
  # Prefer jq if present; fallback to sed regex
  local json="$1"
  if need_cmd jq; then
    echo "$json" | jq -r '.incidentID // empty'
  else
    echo "$json" | sed -n 's/.*"incidentID":"\([^"]*\)".*/\1/p'
  fi
}

assert_2xx() {
  local step="$1"
  local resp="$2"
  local code
  code="$(echo "$resp" | status_code)"
  if [[ ! "$code" =~ ^2 ]]; then
    echo "❌ ${step} failed: HTTP ${code}"
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
    echo "❌ ${step} failed: expected to contain: ${pattern}"
    echo "---- body ----"
    echo "$text"
    echo "-------------"
    exit 1
  fi
}

assert_not_contains() {
  local step="$1"
  local text="$2"
  local pattern="$3"
  if echo "$text" | grep -q "$pattern"; then
    echo "❌ ${step} failed: expected NOT to contain: ${pattern}"
    echo "---- body ----"
    echo "$text"
    echo "-------------"
    exit 1
  fi
}

# ---- test data ----
rand="${RAND:-$RANDOM}"
namespace="${NAMESPACE:-default}"
workload_kind="${WORKLOAD_KIND:-Deployment}"
workload_name="demo-${rand}"
service="demo-svc-${rand}"
severity="${SEVERITY:-P1}"

echo "BASE_URL=${BASE_URL}"
echo "service=${service} workload=${workload_kind}/${workload_name}"

# 1) Create
create_body=$(cat <<EOF
{"namespace":"${namespace}","workloadKind":"${workload_kind}","workloadName":"${workload_name}","service":"${service}","severity":"${severity}"}
EOF
)

create_resp="$(http_json POST "${BASE_URL}/v1/incidents" "$create_body")"
assert_2xx "Create" "$create_resp"
create_json="$(echo "$create_resp" | body_only)"

incident_id="$(extract_incident_id "$create_json")"
if [[ -z "${incident_id}" ]]; then
  echo "❌ Create failed: cannot parse incidentID"
  echo "---- body ----"
  echo "$create_json"
  echo "-------------"
  exit 1
fi
echo "✅ Created incident_id=${incident_id}"

# 2) Get
get_resp="$(http_json GET "${BASE_URL}/v1/incidents/${incident_id}")"
assert_2xx "Get" "$get_resp"
get_json="$(echo "$get_resp" | body_only)"
assert_contains "Get" "$get_json" "\"incidentID\":\"${incident_id}\""
assert_contains "Get" "$get_json" "\"service\":\"${service}\""

# 3) List (filter by service, ensure our incident present)
list_resp="$(http_json GET "${BASE_URL}/v1/incidents?offset=0&limit=50&service=${service}")"
assert_2xx "List" "$list_resp"
list_json="$(echo "$list_resp" | body_only)"
assert_contains "List" "$list_json" "\"incidentID\":\"${incident_id}\""

# 4) Update (PUT) status=closed
update_body='{"status":"closed"}'
update_resp="$(http_json PUT "${BASE_URL}/v1/incidents/${incident_id}" "$update_body")"
assert_2xx "Update" "$update_resp"

# Verify status changed
get2_resp="$(http_json GET "${BASE_URL}/v1/incidents/${incident_id}")"
assert_2xx "GetAfterUpdate" "$get2_resp"
get2_json="$(echo "$get2_resp" | body_only)"
assert_contains "GetAfterUpdate" "$get2_json" "\"status\":\"closed\""

# 5) Delete (batch delete by body)
delete_body=$(cat <<EOF
{"incidentIDs":["${incident_id}"]}
EOF
)
delete_resp="$(http_json DELETE "${BASE_URL}/v1/incidents" "$delete_body")"
assert_2xx "Delete" "$delete_resp"

# 6) Get should fail or not contain incident (depending on your notfound behavior)
get3_resp="$($CURL -sS -i "${BASE_URL}/v1/incidents/${incident_id}" || true)"
code3="$(echo "$get3_resp" | status_code)"
body3="$(echo "$get3_resp" | body_only)"

if [[ "$code3" =~ ^2 ]]; then
  # If your API returns 200 even when not found (not ideal), at least ensure it doesn't still show our record.
  assert_not_contains "GetAfterDelete" "$body3" "\"incidentID\":\"${incident_id}\""
else
  echo "✅ GetAfterDelete returned HTTP ${code3} (expected non-2xx)"
fi

echo "✅ OK: CRUD smoke passed (incident_id=${incident_id})"
