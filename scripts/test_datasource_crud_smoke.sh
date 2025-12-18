#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"

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

rand="${RAND:-$RANDOM}"
ds_type="${DS_TYPE:-prometheus}"
ds_name="${DS_NAME:-smoke-ds-${rand}}"
ds_base_url="${DS_BASE_URL:-http://127.0.0.1:9090}"

echo "BASE_URL=${BASE_URL}"
echo "SCOPES=${SCOPES}"
echo "datasource=${ds_name} type=${ds_type}"

create_body=$(cat <<EOF
{"type":"${ds_type}","name":"${ds_name}","baseURL":"${ds_base_url}","authType":"none","timeoutMs":5000,"isEnabled":true}
EOF
)

create_resp="$(http_json POST "${BASE_URL}/v1/datasources" "$create_body")"
assert_2xx "CreateDatasource" "$create_resp"
create_json="$(echo "$create_resp" | body_only)"
datasource_id="$(extract_field "$create_json" "datasourceID")"

if [[ -z "${datasource_id}" ]]; then
  echo "FAIL CreateDatasource: cannot parse datasourceID"
  echo "$create_json"
  exit 1
fi
echo "PASS CreateDatasource datasource_id=${datasource_id}"

get_resp="$(http_json GET "${BASE_URL}/v1/datasources/${datasource_id}")"
assert_2xx "GetDatasource" "$get_resp"
get_json="$(echo "$get_resp" | body_only)"
assert_contains "GetDatasource" "$get_json" "\"datasourceID\":\"${datasource_id}\""
assert_contains "GetDatasource" "$get_json" "\"name\":\"${ds_name}\""

list_resp="$(http_json GET "${BASE_URL}/v1/datasources?offset=0&limit=50&type=${ds_type}")"
assert_2xx "ListDatasource" "$list_resp"
list_json="$(echo "$list_resp" | body_only)"
assert_contains "ListDatasource" "$list_json" "\"datasourceID\":\"${datasource_id}\""

updated_name="${ds_name}-updated"
patch_body=$(cat <<EOF
{"name":"${updated_name}","timeoutMs":8000}
EOF
)

patch_resp="$(http_json PATCH "${BASE_URL}/v1/datasources/${datasource_id}" "$patch_body")"
assert_2xx "PatchDatasource" "$patch_resp"

get2_resp="$(http_json GET "${BASE_URL}/v1/datasources/${datasource_id}")"
assert_2xx "GetDatasourceAfterPatch" "$get2_resp"
get2_json="$(echo "$get2_resp" | body_only)"
assert_contains "GetDatasourceAfterPatch" "$get2_json" "\"name\":\"${updated_name}\""
assert_contains "GetDatasourceAfterPatch" "$get2_json" "\"timeoutMs\":8000"

# DELETE is soft-delete (disable) in current implementation.
delete_resp="$(http_json DELETE "${BASE_URL}/v1/datasources/${datasource_id}")"
assert_2xx "DeleteDatasource" "$delete_resp"

get3_resp="$(http_json GET "${BASE_URL}/v1/datasources/${datasource_id}")"
assert_2xx "GetDatasourceAfterDelete" "$get3_resp"
get3_json="$(echo "$get3_resp" | body_only)"
assert_contains "GetDatasourceAfterDelete" "$get3_json" "\"isEnabled\":false"

echo "PASS datasource CRUD smoke datasource_id=${datasource_id}"
