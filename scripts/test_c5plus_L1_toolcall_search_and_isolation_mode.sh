#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
DENY_BASE_URL="${DENY_BASE_URL:-${BASE_URL}}"
NOT_FOUND_BASE_URL="${NOT_FOUND_BASE_URL:-${BASE_URL}}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
DEBUG="${DEBUG:-0}"
CURL_CONNECT_TIMEOUT="${CURL_CONNECT_TIMEOUT:-5}"
CURL_MAX_TIME="${CURL_MAX_TIME:-30}"

LAST_HTTP_CODE=""
LAST_BODY=""

INCIDENT_ALLOWED_ID=""
INCIDENT_DENIED_ID=""
TOOL_CALL_ID=""
SEARCH_MATCH_TOOL=""

need_cmd() {
	command -v "$1" >/dev/null 2>&1
}

debug() {
	if [[ "${DEBUG}" == "1" ]]; then
		echo "[DEBUG] $*" >&2
	fi
}

info() {
	echo "[INFO] $*" >&2
}

truncate_2kb() {
	printf '%s' "$1" | head -c 2048
}

fail_step() {
	local step="$1"
	local code="${2:-${LAST_HTTP_CODE}}"
	local body="${3:-${LAST_BODY}}"

	echo "FAIL C5+ step=${step}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	truncate_2kb "${body}"
	echo
	echo "EOF"
	echo "incident_allowed_id=${INCIDENT_ALLOWED_ID:-NONE}"
	echo "incident_denied_id=${INCIDENT_DENIED_ID:-NONE}"
	echo "tool_call_id=${TOOL_CALL_ID:-NONE}"
	echo "search_match_tool=${SEARCH_MATCH_TOOL:-NONE}"
	exit 1
}

extract_field() {
	local json="$1"
	shift
	local keys=("$@")
	local key value
	for key in "${keys[@]}"; do
		value="$(printf '%s' "${json}" | jq -r --arg k "${key}" '
			(.[$k] // .data[$k] // .output[$k] // .incident[$k] // .error[$k] // .details[$k]) |
			if . == null then empty
			elif type == "string" then .
			else tostring
			end
		' 2>/dev/null || true)"
		if [[ -n "${value}" ]]; then
			printf '%s' "${value}"
			return 0
		fi
	done
	return 1
}

http_any() {
	local method="$1"
	local url="$2"
	local body="${3:-}"
	local scopes="${4:-${SCOPES}}"
	shift 4 || true
	local extra_headers=("$@")

	local tmp_body tmp_err code rc curl_err
	tmp_body="$(mktemp)"
	tmp_err="$(mktemp)"

	local -a cmd
	cmd=(
		"${CURL}"
		-sS
		--connect-timeout "${CURL_CONNECT_TIMEOUT}"
		--max-time "${CURL_MAX_TIME}"
		-o "${tmp_body}"
		-w "%{http_code}"
		-X "${method}"
		"${url}"
		-H "Accept: */*"
	)
	if [[ -n "${scopes}" ]]; then
		cmd+=(-H "X-Scopes: ${scopes}")
	fi
	if [[ -n "${body}" ]]; then
		cmd+=(-H "Content-Type: application/json" -d "${body}")
	fi
	for header in "${extra_headers[@]}"; do
		if [[ -n "${header}" ]]; then
			cmd+=(-H "${header}")
		fi
	done

	set +e
	code="$("${cmd[@]}" 2>"${tmp_err}")"
	rc=$?
	set -e

	LAST_BODY="$(cat "${tmp_body}")"
	curl_err="$(cat "${tmp_err}")"
	rm -f "${tmp_body}" "${tmp_err}"

	if (( rc != 0 )); then
		LAST_HTTP_CODE="CURL_${rc}"
		if [[ -n "${curl_err}" ]]; then
			if [[ -n "${LAST_BODY}" ]]; then
				LAST_BODY="${LAST_BODY}"$'\n'"${curl_err}"
			else
				LAST_BODY="${curl_err}"
			fi
		fi
		return 1
	fi

	LAST_HTTP_CODE="${code}"
	if [[ -n "${curl_err}" ]]; then
		debug "curl stderr: ${curl_err}"
	fi
	return 0
}

call_or_fail() {
	local step="$1"
	local method="$2"
	local url="$3"
	local body="${4:-}"
	local scopes="${5:-${SCOPES}}"
	shift 5 || true
	local extra_headers=("$@")

	if ! http_any "${method}" "${url}" "${body}" "${scopes}" "${extra_headers[@]}"; then
		fail_step "${step}"
	fi
	if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
		fail_step "${step}"
	fi
}

assert_error_shape_or_fail() {
	local step="$1"
	local expect_code="$2"
	local expect_http="$3"
	if [[ "${LAST_HTTP_CODE}" != "${expect_http}" ]]; then
		fail_step "${step}.HTTP" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
	local code details_step
	code="$(printf '%s' "${LAST_BODY}" | jq -r '.error.code // empty' 2>/dev/null || true)"
	details_step="$(printf '%s' "${LAST_BODY}" | jq -r '.error.details.step // empty' 2>/dev/null || true)"
	if [[ "${code}" != "${expect_code}" ]]; then
		fail_step "${step}.ErrorCode" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
	if [[ "${details_step}" != "mcp.call" ]]; then
		fail_step "${step}.ErrorStep" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
}

assert_no_sensitive_or_fail() {
	local step="$1"
	local lower
	lower="$(printf '%s' "${LAST_BODY}" | tr '[:upper:]' '[:lower:]')"
	if [[ "${lower}" == *"\"secret\""* ]] || [[ "${lower}" == *"\"headers\""* ]] || [[ "${lower}" == *"\"token\""* ]] || [[ "${lower}" == *"\"authorization\""* ]]; then
		fail_step "${step}.SensitiveLeak" "ASSERT" "search_tool_calls response contains sensitive fields"
	fi
}

if ! need_cmd jq; then
	fail_step "Precheck.MissingJQ" "PRECHECK" "jq command not found"
fi

rand="${RAND:-$RANDOM}"
now_epoch="$(date -u +%s)"
allowed_namespace="c5plus-allowed-ns-${rand}"
denied_namespace="c5plus-denied-ns-${rand}"
allowed_service="c5plus-allowed-svc-${rand}"
denied_service="c5plus-denied-svc-${rand}"

ingest_allowed_body=$(cat <<JSON
{"idempotencyKey":"idem-c5plus-allow-${rand}","fingerprint":"c5plus-fp-allow-${rand}","status":"firing","severity":"P1","service":"${allowed_service}","cluster":"prod-c5plus","namespace":"${allowed_namespace}","workload":"demo-c5plus","lastSeenAt":{"seconds":${now_epoch},"nanos":0}}
JSON
)
call_or_fail "IngestAllowed" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_allowed_body}" "alert.ingest"
INCIDENT_ALLOWED_ID="$(extract_field "${LAST_BODY}" "incidentID" "incident_id")" || true
if [[ -z "${INCIDENT_ALLOWED_ID}" ]]; then
	fail_step "IngestAllowed.ParseIncidentID"
fi

ingest_denied_body=$(cat <<JSON
{"idempotencyKey":"idem-c5plus-deny-${rand}","fingerprint":"c5plus-fp-deny-${rand}","status":"firing","severity":"P2","service":"${denied_service}","cluster":"prod-c5plus","namespace":"${denied_namespace}","workload":"demo-c5plus","lastSeenAt":{"seconds":${now_epoch},"nanos":0}}
JSON
)
call_or_fail "IngestDenied" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_denied_body}" "alert.ingest"
INCIDENT_DENIED_ID="$(extract_field "${LAST_BODY}" "incidentID" "incident_id")" || true
if [[ -z "${INCIDENT_DENIED_ID}" ]]; then
	fail_step "IngestDenied.ParseIncidentID"
fi

call_or_fail \
	"Seed.SearchIncidents" \
	POST \
	"${BASE_URL}/v1/mcp/tools/call" \
	'{"tool":"search_incidents","input":{"limit":20,"page":1}}' \
	"incident.read"

call_or_fail \
	"Seed.GetIncident" \
	POST \
	"${BASE_URL}/v1/mcp/tools/call" \
	"{\"tool\":\"get_incident\",\"input\":{\"incident_id\":\"${INCIDENT_ALLOWED_ID}\"}}" \
	"incident.read"

search_limit=5
search_body_head=$(cat <<JSON
{"tool":"search_tool_calls","input":{"tool_prefix":"mcp.","limit":${search_limit},"page":1}}
JSON
)
call_or_fail "ToolCallSearch.Allow.Head" POST "${BASE_URL}/v1/mcp/tools/call" "${search_body_head}" "toolcall.read"
TOOL_CALL_ID="$(extract_field "${LAST_BODY}" "tool_call_id" "toolCallID")" || true
assert_no_sensitive_or_fail "ToolCallSearch.Allow.Head"

total_count="$(printf '%s' "${LAST_BODY}" | jq -r '.output.totalCount // 0' 2>/dev/null || true)"
if [[ -z "${total_count}" || ! "${total_count}" =~ ^[0-9]+$ ]]; then
	fail_step "ToolCallSearch.TotalCount" "ASSERT" "search_tool_calls output.totalCount is missing"
fi
last_page=$(( (total_count + search_limit - 1) / search_limit ))
if (( last_page < 1 )); then
	last_page=1
fi

search_body_tail=$(cat <<JSON
{"tool":"search_tool_calls","input":{"tool_prefix":"mcp.","limit":${search_limit},"page":${last_page}}}
JSON
)
call_or_fail "ToolCallSearch.Allow.Tail" POST "${BASE_URL}/v1/mcp/tools/call" "${search_body_tail}" "toolcall.read"
assert_no_sensitive_or_fail "ToolCallSearch.Allow.Tail"

match_count="$(printf '%s' "${LAST_BODY}" | jq -r '
	(.output.toolCalls // [])
	| map(select((.toolName // "") == "mcp.search_incidents" or (.toolName // "") == "mcp.get_incident_timeline"))
	| length
' 2>/dev/null || true)"
if [[ -z "${match_count}" ]] || (( match_count < 1 )); then
	fail_step "ToolCallSearch.MatchTool" "ASSERT" "search_tool_calls missing mcp.search_incidents or mcp.get_incident_timeline"
fi
SEARCH_MATCH_TOOL="$(printf '%s' "${LAST_BODY}" | jq -r '
	(.output.toolCalls // [])
	| map(select((.toolName // "") == "mcp.search_incidents" or (.toolName // "") == "mcp.get_incident_timeline"))
	| .[0].toolName // empty
' 2>/dev/null || true)"

if ! http_any POST "${BASE_URL}/v1/mcp/tools/call" "${search_body_head}" "incident.read"; then
	fail_step "ToolCallSearch.ScopeDenied.Call"
fi
assert_error_shape_or_fail "ToolCallSearch.ScopeDenied" "SCOPE_DENIED" "403"

deny_body=$(cat <<JSON
{"tool":"search_incidents","input":{"namespace":"${denied_namespace}","limit":20,"page":1}}
JSON
)
if ! http_any \
	POST \
	"${DENY_BASE_URL}/v1/mcp/tools/call" \
	"${deny_body}" \
	"incident.read" \
	"X-Allowed-Namespaces: ${allowed_namespace}"; then
	fail_step "IsolationModeDeny.Call"
fi
assert_error_shape_or_fail "IsolationModeDeny" "SCOPE_DENIED" "403"

if ! http_any \
	POST \
	"${NOT_FOUND_BASE_URL}/v1/mcp/tools/call" \
	"${deny_body}" \
	"incident.read" \
	"X-Allowed-Namespaces: ${allowed_namespace}"; then
	fail_step "IsolationModeNotFound.Call"
fi
assert_error_shape_or_fail "IsolationModeNotFound" "NOT_FOUND" "404"

call_or_fail "AuditListToolCalls" GET "${BASE_URL}/v1/ai/jobs/mcp-readonly/tool-calls?offset=0&limit=200" "" "ai.read"
audit_match_count="$(printf '%s' "${LAST_BODY}" | jq -r '
	(.toolCalls // .data.toolCalls // [])
	| map(select((.toolName // "") == "mcp.search_incidents"))
	| length
' 2>/dev/null || true)"
if [[ -z "${audit_match_count}" ]] || (( audit_match_count < 1 )); then
	fail_step "Audit.SearchIncidentsMissing" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

info "如果 deny/not_found 使用不同服务地址，请分别设置 DENY_BASE_URL 与 NOT_FOUND_BASE_URL。"
echo "PASS C5+ search_tool_calls matched=${SEARCH_MATCH_TOOL:-NONE}"
echo "PASS C5+ scope denied search_tool_calls"
echo "PASS C5+ isolation deny/not_found"
echo "PASS C5+ audit tool_call_id=${TOOL_CALL_ID:-NONE}"
echo "PASS C5+ toolcall search and isolation mode"
