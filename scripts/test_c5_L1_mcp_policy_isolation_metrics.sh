#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
DEBUG="${DEBUG:-0}"
CURL_CONNECT_TIMEOUT="${CURL_CONNECT_TIMEOUT:-5}"
CURL_MAX_TIME="${CURL_MAX_TIME:-30}"
MCP_TOOLS_VERSION="${MCP_TOOLS_VERSION:-c1}"
POLICY_DISABLED_TOOL="${POLICY_DISABLED_TOOL:-}"
POLICY_LIMIT_TOOL="${POLICY_LIMIT_TOOL:-search_incidents}"
TRUNC_SEED_COUNT="${TRUNC_SEED_COUNT:-24}"
SEED_PROGRESS_EVERY="${SEED_PROGRESS_EVERY:-8}"

LAST_HTTP_CODE=""
LAST_BODY=""

INCIDENT_ALLOWED_ID=""
INCIDENT_DENIED_ID=""
TOOL_CALL_ID=""
DISABLED_TOOL_USED=""

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

	echo "FAIL C5 step=${step}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	truncate_2kb "${body}"
	echo
	echo "EOF"
	echo "incident_allowed_id=${INCIDENT_ALLOWED_ID:-NONE}"
	echo "incident_denied_id=${INCIDENT_DENIED_ID:-NONE}"
	echo "tool_call_id=${TOOL_CALL_ID:-NONE}"
	echo "disabled_tool=${DISABLED_TOOL_USED:-NONE}"
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

metric_sum_prefix() {
	local payload="$1"
	local prefix="$2"
	printf '%s\n' "${payload}" | awk -v p="${prefix}" '
		index($0, p) == 1 {sum += $NF}
		END {printf "%.0f", sum + 0}
	'
}

if ! need_cmd jq; then
	fail_step "Precheck.MissingJQ" "PRECHECK" "jq command not found"
fi

rand="${RAND:-$RANDOM}"
now_epoch="$(date -u +%s)"
allowed_namespace="c5-allowed-ns-${rand}"
denied_namespace="c5-denied-ns-${rand}"
allowed_service="c5-allowed-svc-${rand}"
denied_service="c5-denied-svc-${rand}"

call_or_fail "MCPListTools" GET "${BASE_URL}/v1/mcp/tools" "" "${SCOPES}"
version="$(printf '%s' "${LAST_BODY}" | jq -r '.version // empty' 2>/dev/null || true)"
if [[ "${version}" != "${MCP_TOOLS_VERSION}" ]]; then
	fail_step "MCPListTools.Version" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

if [[ -n "${POLICY_DISABLED_TOOL}" ]]; then
	disabled_enabled="$(printf '%s' "${LAST_BODY}" | jq -r --arg t "${POLICY_DISABLED_TOOL}" '
		.tools[]
		| select(.name == $t)
		| if (.metadata.policy | type == "object") and (.metadata.policy | has("enabled"))
		  then (.metadata.policy.enabled | tostring)
		  else empty
		  end
	' 2>/dev/null || true)"
	if [[ "${disabled_enabled}" != "false" ]]; then
		fail_step "PolicyDisabled.Precondition" "PRECHECK" "tool=${POLICY_DISABLED_TOOL} is not disabled; configure mcp.tools.${POLICY_DISABLED_TOOL}.enabled=false and restart rca-apiserver"
	fi
	DISABLED_TOOL_USED="${POLICY_DISABLED_TOOL}"
else
	DISABLED_TOOL_USED="$(printf '%s' "${LAST_BODY}" | jq -r '
		.tools
		| map(select((.metadata.policy | type == "object") and (.metadata.policy.enabled == false)))
		| .[0].name // empty
	' 2>/dev/null || true)"
	if [[ -z "${DISABLED_TOOL_USED}" ]]; then
		fail_step "PolicyDisabled.Precondition" "PRECHECK" "no disabled tool found in /v1/mcp/tools metadata.policy; set one tool enabled=false in config (e.g. mcp.tools.query_logs.enabled=false) and restart"
	fi
fi

limit_max="$(printf '%s' "${LAST_BODY}" | jq -r --arg t "${POLICY_LIMIT_TOOL}" '
	(.tools[] | select(.name == $t) | .metadata.policy.limits.max_limit) // empty
' 2>/dev/null || true)"
if [[ -z "${limit_max}" || "${limit_max}" == "null" ]]; then
	fail_step "PolicyLimit.Precondition" "PRECHECK" "tool=${POLICY_LIMIT_TOOL} has no metadata.policy.limits.max_limit"
fi
limit_exceed=$((limit_max + 1))

if ! http_any POST "${BASE_URL}/v1/mcp/tools/call" "{\"tool\":\"${DISABLED_TOOL_USED}\",\"input\":{}}" "${SCOPES}"; then
	fail_step "PolicyDisabled.Call"
fi
assert_error_shape_or_fail "PolicyDisabled" "SCOPE_DENIED" "403"

if ! http_any POST "${BASE_URL}/v1/mcp/tools/call" "{\"tool\":\"${POLICY_LIMIT_TOOL}\",\"input\":{\"limit\":${limit_exceed},\"page\":1}}" "incident.read"; then
	fail_step "PolicyLimit.Call"
fi
assert_error_shape_or_fail "PolicyLimit" "INVALID_ARGUMENT" "400"

metrics_before=""
call_or_fail "MetricsBefore" GET "${BASE_URL}/metrics" "" ""
metrics_before="${LAST_BODY}"
calls_before="$(metric_sum_prefix "${metrics_before}" "mcp_calls_total{")"
denies_before="$(metric_sum_prefix "${metrics_before}" "mcp_scope_denied_total{")"
trunc_before="$(metric_sum_prefix "${metrics_before}" "mcp_truncated_total{")"

ingest_allowed_body=$(cat <<JSON
{"idempotencyKey":"idem-c5-allow-${rand}","fingerprint":"c5-fp-allow-${rand}","status":"firing","severity":"P1","service":"${allowed_service}","cluster":"prod-c5","namespace":"${allowed_namespace}","workload":"demo-c5","lastSeenAt":{"seconds":${now_epoch},"nanos":0}}
JSON
)
call_or_fail "IngestAllowed" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_allowed_body}" "alert.ingest"
INCIDENT_ALLOWED_ID="$(extract_field "${LAST_BODY}" "incidentID" "incident_id")" || true
if [[ -z "${INCIDENT_ALLOWED_ID}" ]]; then
	fail_step "IngestAllowed.ParseIncidentID"
fi

ingest_denied_body=$(cat <<JSON
{"idempotencyKey":"idem-c5-deny-${rand}","fingerprint":"c5-fp-deny-${rand}","status":"firing","severity":"P2","service":"${denied_service}","cluster":"prod-c5","namespace":"${denied_namespace}","workload":"demo-c5","lastSeenAt":{"seconds":${now_epoch},"nanos":0}}
JSON
)
call_or_fail "IngestDenied" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_denied_body}" "alert.ingest"
INCIDENT_DENIED_ID="$(extract_field "${LAST_BODY}" "incidentID" "incident_id")" || true
if [[ -z "${INCIDENT_DENIED_ID}" ]]; then
	fail_step "IngestDenied.ParseIncidentID"
fi

search_body=$(cat <<JSON
{"tool":"search_incidents","input":{"limit":20,"page":1}}
JSON
)
call_or_fail \
	"IsolationSearchAllow" \
	POST \
	"${BASE_URL}/v1/mcp/tools/call" \
	"${search_body}" \
	"incident.read" \
	"X-Allowed-Namespaces: ${allowed_namespace}" \
	"X-Allowed-Services: ${allowed_service}"

TOOL_CALL_ID="$(extract_field "${LAST_BODY}" "tool_call_id" "toolCallID")" || true

allowed_count="$(printf '%s' "${LAST_BODY}" | jq -r --arg ns "${allowed_namespace}" --arg svc "${allowed_service}" '
	(.output.incidents // [])
	| map(select((.namespace // "") == $ns and (.service // "") == $svc))
	| length
' 2>/dev/null || true)"
denied_count="$(printf '%s' "${LAST_BODY}" | jq -r --arg ns "${denied_namespace}" --arg svc "${denied_service}" '
	(.output.incidents // [])
	| map(select((.namespace // "") == $ns or (.service // "") == $svc))
	| length
' 2>/dev/null || true)"
if [[ -z "${allowed_count}" ]] || (( allowed_count < 1 )); then
	fail_step "IsolationSearchAllow.AssertAllowed" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi
if [[ -z "${denied_count}" ]]; then
	fail_step "IsolationSearchAllow.AssertDenied" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi
if (( denied_count > 0 )); then
	fail_step "IsolationSearchAllow.AssertDenied" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

get_denied_body=$(cat <<JSON
{"tool":"get_incident","input":{"incident_id":"${INCIDENT_DENIED_ID}"}}
JSON
)
if ! http_any \
	POST \
	"${BASE_URL}/v1/mcp/tools/call" \
	"${get_denied_body}" \
	"incident.read" \
	"X-Allowed-Namespaces: ${allowed_namespace}" \
	"X-Allowed-Services: ${allowed_service}"; then
	fail_step "IsolationGetDenied.Call"
fi
assert_error_shape_or_fail "IsolationGetDenied" "SCOPE_DENIED" "403"

if (( TRUNC_SEED_COUNT > 0 )); then
	info "TruncationSeed total=${TRUNC_SEED_COUNT} progress_every=${SEED_PROGRESS_EVERY}"
fi
for i in $(seq 1 "${TRUNC_SEED_COUNT}"); do
	seed_body=$(cat <<JSON
{"tool":"get_incident","input":{"incident_id":"${INCIDENT_ALLOWED_ID}"},"idempotency_key":"c5-seed-${rand}-${i}"}
JSON
)
	if ! http_any POST "${BASE_URL}/v1/mcp/tools/call" "${seed_body}" "incident.read"; then
		fail_step "TruncationSeed.Call.${i}"
	fi
	if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
		fail_step "TruncationSeed.Call.${i}" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
	if (( i == 1 || i == TRUNC_SEED_COUNT || (SEED_PROGRESS_EVERY > 0 && i % SEED_PROGRESS_EVERY == 0) )); then
		info "TruncationSeed progress=${i}/${TRUNC_SEED_COUNT}"
	fi
done

trunc_body='{"tool":"list_tool_calls","input":{"job_id":"mcp-readonly","limit":200,"page":1}}'
call_or_fail "TruncationCall" POST "${BASE_URL}/v1/mcp/tools/call" "${trunc_body}" "toolcall.read"
truncated_flag="$(printf '%s' "${LAST_BODY}" | jq -r '.truncated // false' 2>/dev/null || true)"
trunc_warning_count="$(printf '%s' "${LAST_BODY}" | jq -r '[.warnings[]? | select(. == "TRUNCATED_OUTPUT")] | length' 2>/dev/null || true)"
if [[ "${truncated_flag}" != "true" ]]; then
	fail_step "TruncationCall.Flag" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi
if [[ -z "${trunc_warning_count}" ]] || (( trunc_warning_count < 1 )); then
	fail_step "TruncationCall.Warning" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

call_or_fail "MetricsAfter" GET "${BASE_URL}/metrics" "" ""
metrics_after="${LAST_BODY}"
calls_after="$(metric_sum_prefix "${metrics_after}" "mcp_calls_total{")"
denies_after="$(metric_sum_prefix "${metrics_after}" "mcp_scope_denied_total{")"
trunc_after="$(metric_sum_prefix "${metrics_after}" "mcp_truncated_total{")"

if (( calls_after <= calls_before )); then
	fail_step "Metrics.CallsIncrease" "ASSERT" "before=${calls_before} after=${calls_after}"
fi
if (( denies_after <= denies_before )); then
	fail_step "Metrics.DenyIncrease" "ASSERT" "before=${denies_before} after=${denies_after}"
fi
if (( trunc_after <= trunc_before )); then
	fail_step "Metrics.TruncIncrease" "ASSERT" "before=${trunc_before} after=${trunc_after}"
fi

if ! printf '%s\n' "${metrics_after}" | grep -q '^mcp_calls_total{'; then
	fail_step "Metrics.CallsExists" "ASSERT" "mcp_calls_total missing"
fi
if ! printf '%s\n' "${metrics_after}" | grep -q '^mcp_call_latency_ms_bucket{'; then
	fail_step "Metrics.LatencyExists" "ASSERT" "mcp_call_latency_ms missing"
fi
if ! printf '%s\n' "${metrics_after}" | grep -q '^mcp_truncated_total{'; then
	fail_step "Metrics.TruncatedExists" "ASSERT" "mcp_truncated_total missing"
fi
if ! printf '%s\n' "${metrics_after}" | grep -q '^mcp_scope_denied_total{'; then
	fail_step "Metrics.ScopeDeniedExists" "ASSERT" "mcp_scope_denied_total missing"
fi
if ! printf '%s\n' "${metrics_after}" | grep -q '^mcp_rate_limited_total{'; then
	fail_step "Metrics.RateLimitedExists" "ASSERT" "mcp_rate_limited_total missing"
fi
if ! printf '%s\n' "${metrics_after}" | grep -q 'mcp_calls_total{[^}]*tool="search_incidents"'; then
	fail_step "Metrics.CallsToolLabel" "ASSERT" "mcp_calls_total for search_incidents missing"
fi

call_or_fail "AuditListToolCalls" GET "${BASE_URL}/v1/ai/jobs/mcp-readonly/tool-calls?offset=0&limit=200" "" "ai.read"
audit_match_count="$(printf '%s' "${LAST_BODY}" | jq -r '
	(.toolCalls // .data.toolCalls // [])
	| map(select((.toolName // "") == "mcp.search_incidents"))
	| length
' 2>/dev/null || true)"
if [[ -z "${audit_match_count}" ]] || (( audit_match_count < 1 )); then
	fail_step "Audit.SearchIncidentsMissing" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

echo "PASS C5 policy disabled tool=${DISABLED_TOOL_USED}"
echo "PASS C5 policy limit tool=${POLICY_LIMIT_TOOL} max_limit=${limit_max}"
echo "PASS C5 isolation allowed=${INCIDENT_ALLOWED_ID} denied=${INCIDENT_DENIED_ID}"
echo "PASS C5 metrics calls=${calls_after} denies=${denies_after} trunc=${trunc_after}"
echo "PASS C5 audit tool_call_id=${TOOL_CALL_ID:-NONE}"
echo "PASS C5 mcp policy isolation metrics"
