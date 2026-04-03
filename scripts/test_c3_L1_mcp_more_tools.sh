#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
DEBUG="${DEBUG:-0}"
TRUNC_ALERT_COUNT="${TRUNC_ALERT_COUNT:-24}"
SEED_PROGRESS_EVERY="${SEED_PROGRESS_EVERY:-10}"
MCP_TOOLS_VERSION="${MCP_TOOLS_VERSION:-c1}"
CURL_CONNECT_TIMEOUT="${CURL_CONNECT_TIMEOUT:-5}"
CURL_MAX_TIME="${CURL_MAX_TIME:-30}"

LAST_HTTP_CODE=""
LAST_BODY=""

INCIDENT_ID=""
JOB_ID=""
TOOL_CALL_ID=""
DATASOURCE_ID=""
SILENCE_ID=""

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

	echo "FAIL C3 step=${step}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	truncate_2kb "${body}"
	echo
	echo "EOF"
	echo "incident_id=${INCIDENT_ID:-NONE}"
	echo "job_id=${JOB_ID:-NONE}"
	echo "tool_call_id=${TOOL_CALL_ID:-NONE}"
	echo "datasource_id=${DATASOURCE_ID:-NONE}"
	echo "silence_id=${SILENCE_ID:-NONE}"
	exit 1
}

extract_field() {
	local json="$1"
	shift
	local keys=("$@")
	local key value

	if need_cmd jq; then
		for key in "${keys[@]}"; do
			value="$({
				printf '%s' "${json}" | jq -r --arg k "${key}" '
					(.[$k] // .data[$k] // .output[$k] // .error[$k] // .details[$k] //
					 .incident[$k] // .data.incident[$k] // .job[$k] // .data.job[$k]) |
					if . == null then empty
					elif type == "string" then .
					else tojson
					end
				' 2>/dev/null
			} || true)"
			if [[ -n "${value}" ]]; then
				printf '%s' "${value}"
				return 0
			fi
		done
	else
		for key in "${keys[@]}"; do
			value="$(printf '%s' "${json}" | sed -n "s/.*\"${key}\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" | head -n 1)"
			if [[ -n "${value}" ]]; then
				printf '%s' "${value}"
				return 0
			fi
		done
	fi
	return 1
}

http_json() {
	local method="$1"
	local url="$2"
	local body="${3:-}"
	local scopes="${4:-${SCOPES}}"

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
		-H "Accept: application/json"
	)
	if [[ -n "${scopes}" ]]; then
		cmd+=(-H "X-Scopes: ${scopes}")
	fi
	if [[ -n "${body}" ]]; then
		cmd+=(-H "Content-Type: application/json" -d "${body}")
	fi

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

	if ! http_json "${method}" "${url}" "${body}" "${scopes}"; then
		fail_step "${step}"
	fi
	if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
		fail_step "${step}"
	fi
	debug "${step} code=${LAST_HTTP_CODE}"
}

assert_error_shape_or_fail() {
	local step="$1"
	local expect_code="$2"
	local expect_http="$3"

	if [[ "${LAST_HTTP_CODE}" != "${expect_http}" ]]; then
		fail_step "${step}.HTTP" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi

	if need_cmd jq; then
		local code details_step
		code="$(printf '%s' "${LAST_BODY}" | jq -r '.error.code // empty' 2>/dev/null || true)"
		details_step="$(printf '%s' "${LAST_BODY}" | jq -r '.error.details.step // empty' 2>/dev/null || true)"
		if [[ "${code}" != "${expect_code}" ]]; then
			fail_step "${step}.ErrorCode" "${LAST_HTTP_CODE}" "${LAST_BODY}"
		fi
		if [[ "${details_step}" != "mcp.call" ]]; then
			fail_step "${step}.ErrorStep" "${LAST_HTTP_CODE}" "${LAST_BODY}"
		fi
	else
		if [[ "${LAST_BODY}" != *"\"code\":\"${expect_code}\""* ]]; then
			fail_step "${step}.ErrorCode" "${LAST_HTTP_CODE}" "${LAST_BODY}"
		fi
		if [[ "${LAST_BODY}" != *"\"step\":\"mcp.call\""* ]]; then
			fail_step "${step}.ErrorStep" "${LAST_HTTP_CODE}" "${LAST_BODY}"
		fi
	fi
}

assert_no_sensitive() {
	local step="$1"
	local body="${2:-${LAST_BODY}}"

	if printf '%s' "${body}" | grep -Eiq '("secret"[[:space:]]*:|\\\"secret\\\"[[:space:]]*:|"authorization"[[:space:]]*:|\\\"authorization\\\"[[:space:]]*:|"Authorization"[[:space:]]*:|\\\"Authorization\\\"[[:space:]]*:|"token"[[:space:]]*:|\\\"token\\\"[[:space:]]*:|"headers"[[:space:]]*:|\\\"headers\\\"[[:space:]]*:)'; then
		fail_step "${step}.SensitiveLeak" "${LAST_HTTP_CODE}" "${body}"
	fi
}

assert_list_tools_or_fail() {
	if need_cmd jq; then
		local version missing_count
		version="$(printf '%s' "${LAST_BODY}" | jq -r '.version // empty' 2>/dev/null || true)"
		if [[ "${version}" != "${MCP_TOOLS_VERSION}" ]]; then
			fail_step "MCPListTools.Version" "${LAST_HTTP_CODE}" "${LAST_BODY}"
		fi
		missing_count="$(printf '%s' "${LAST_BODY}" | jq -r '
			[
				"list_incidents",
				"list_alert_events_history",
				"list_datasources",
				"get_datasource",
				"get_ai_job",
				"list_ai_jobs",
				"list_tool_calls",
				"list_silences",
				"list_notice_deliveries"
			] as $expected
			| (.tools // [] | map(.name)) as $actual
			| [$expected[] | select(($actual | index(.)) == null)]
			| length
		' 2>/dev/null || true)"
		if [[ -z "${missing_count}" ]] || (( missing_count > 0 )); then
			fail_step "MCPListTools.RequiredTools" "${LAST_HTTP_CODE}" "${LAST_BODY}"
		fi
	else
		if [[ "${LAST_BODY}" != *'"version":"'"${MCP_TOOLS_VERSION}"'"'* ]] && [[ "${LAST_BODY}" != *'"version": "'"${MCP_TOOLS_VERSION}"'"'* ]]; then
			fail_step "MCPListTools.Version" "${LAST_HTTP_CODE}" "${LAST_BODY}"
		fi
		for tool in list_incidents list_alert_events_history list_datasources get_datasource get_ai_job list_ai_jobs list_tool_calls list_silences list_notice_deliveries; do
			if [[ "${LAST_BODY}" != *"\"name\":\"${tool}\""* ]]; then
				fail_step "MCPListTools.RequiredTools.${tool}" "${LAST_HTTP_CODE}" "${LAST_BODY}"
			fi
		done
	fi
}

rand="${RAND:-$RANDOM}"
now_epoch="$(date -u +%s)"
start_epoch="$((now_epoch - 1800))"
test_namespace="c3-ns-${rand}"

call_or_fail "MCPListTools" GET "${BASE_URL}/v1/mcp/tools" "" "${SCOPES}"
assert_no_sensitive "MCPListTools.NoSensitive" "${LAST_BODY}"
assert_list_tools_or_fail

fingerprint="c3-l1-fp-${rand}"
ingest_body=$(cat <<JSON
{"idempotencyKey":"idem-c3-l1-ingest-${rand}","fingerprint":"${fingerprint}","status":"firing","severity":"P1","service":"c3-l1-svc","cluster":"prod-c3","namespace":"${test_namespace}","workload":"demo-c3","lastSeenAt":{"seconds":${now_epoch},"nanos":0}}
JSON
)
call_or_fail "IngestAlertEvent" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_body}" "alert.ingest"
INCIDENT_ID="$(extract_field "${LAST_BODY}" "incidentID" "incident_id")" || true
if [[ -z "${INCIDENT_ID}" ]]; then
	fail_step "IngestAlertEvent.ParseIncidentID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

resolved_body=$(cat <<JSON
{"idempotencyKey":"idem-c3-l1-ingest-resolved-${rand}","fingerprint":"${fingerprint}","status":"resolved","severity":"P1","service":"c3-l1-svc","cluster":"prod-c3","namespace":"${test_namespace}","workload":"demo-c3","lastSeenAt":{"seconds":$((now_epoch + 1)),"nanos":0}}
JSON
)
call_or_fail "IngestAlertEventResolved" POST "${BASE_URL}/v1/alert-events:ingest" "${resolved_body}" "alert.ingest"

list_incidents_body=$(cat <<JSON
{"tool":"list_incidents","input":{"namespace":"${test_namespace}","limit":20,"page":1}}
JSON
)
call_or_fail "MCPAllowListIncidents" POST "${BASE_URL}/v1/mcp/tools/call" "${list_incidents_body}" "incident.read"
assert_no_sensitive "MCPAllowListIncidents.NoSensitive" "${LAST_BODY}"

if need_cmd jq; then
	incident_match="$(printf '%s' "${LAST_BODY}" | jq -r --arg id "${INCIDENT_ID}" '
		(.output.incidents // [])
		| map(select((.incidentID // "") == $id))
		| length
	' 2>/dev/null || true)"
	if [[ -z "${incident_match}" ]] || (( incident_match < 1 )); then
		fail_step "MCPAllowListIncidents.AssertIncidentFound" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
fi

list_history_body=$(cat <<JSON
{"tool":"list_alert_events_history","input":{"namespace":"${test_namespace}","limit":20,"page":1}}
JSON
)
call_or_fail "MCPAllowListAlertEventsHistory" POST "${BASE_URL}/v1/mcp/tools/call" "${list_history_body}" "alert_event.read"
assert_no_sensitive "MCPAllowListAlertEventsHistory.NoSensitive" "${LAST_BODY}"

ds_body=$(cat <<JSON
{"type":"prometheus","name":"c3-ds-${rand}","baseURL":"http://127.0.0.1:9090","authType":"bearer","authSecretRef":"vault://prod/c3-ds","defaultHeadersJSON":"{\"Authorization\":\"Bearer top-secret-token\",\"X-Trace\":\"c3\"}","timeoutMs":2500,"isEnabled":true}
JSON
)
call_or_fail "CreateDatasource" POST "${BASE_URL}/v1/datasources" "${ds_body}" "datasource.admin"
DATASOURCE_ID="$(extract_field "${LAST_BODY}" "datasourceID" "datasource_id")" || true
if [[ -z "${DATASOURCE_ID}" ]]; then
	fail_step "CreateDatasource.ParseID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

list_ds_body='{"tool":"list_datasources","input":{"limit":20,"page":1}}'
call_or_fail "MCPAllowListDatasources" POST "${BASE_URL}/v1/mcp/tools/call" "${list_ds_body}" "datasource.read"
assert_no_sensitive "MCPAllowListDatasources.NoSensitive" "${LAST_BODY}"

if need_cmd jq; then
	ds_match="$(printf '%s' "${LAST_BODY}" | jq -r --arg id "${DATASOURCE_ID}" '
		(.output.datasources // [])
		| map(select((.datasourceID // "") == $id))
		| length
	' 2>/dev/null || true)"
	if [[ -z "${ds_match}" ]] || (( ds_match < 1 )); then
		fail_step "MCPAllowListDatasources.AssertFound" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
fi

get_ds_body=$(cat <<JSON
{"tool":"get_datasource","input":{"datasource_id":"${DATASOURCE_ID}"}}
JSON
)
call_or_fail "MCPAllowGetDatasource" POST "${BASE_URL}/v1/mcp/tools/call" "${get_ds_body}" "datasource.read"
assert_no_sensitive "MCPAllowGetDatasource.NoSensitive" "${LAST_BODY}"

if need_cmd jq; then
	ds_id="$(printf '%s' "${LAST_BODY}" | jq -r '.output.datasourceID // empty' 2>/dev/null || true)"
	if [[ "${ds_id}" != "${DATASOURCE_ID}" ]]; then
		fail_step "MCPAllowGetDatasource.AssertID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
fi

run_body=$(cat <<JSON
{"incidentID":"${INCIDENT_ID}","idempotencyKey":"idem-c3-l1-ai-run-${rand}","pipeline":"basic_rca","trigger":"manual","timeRangeStart":{"seconds":${start_epoch},"nanos":0},"timeRangeEnd":{"seconds":${now_epoch},"nanos":0},"inputHintsJSON":"{\"scenario\":\"C3_L1_MCP_MORE_TOOLS\"}","createdBy":"system"}
JSON
)
call_or_fail "RunAIJob" POST "${BASE_URL}/v1/incidents/${INCIDENT_ID}/ai:run" "${run_body}" "ai.run"
JOB_ID="$(extract_field "${LAST_BODY}" "jobID" "job_id")" || true
if [[ -z "${JOB_ID}" ]]; then
	fail_step "RunAIJob.ParseJobID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

list_ai_jobs_body=$(cat <<JSON
{"tool":"list_ai_jobs","input":{"incident_id":"${INCIDENT_ID}","limit":20,"page":1}}
JSON
)
call_or_fail "MCPAllowListAIJobs" POST "${BASE_URL}/v1/mcp/tools/call" "${list_ai_jobs_body}" "ai_job.read"
assert_no_sensitive "MCPAllowListAIJobs.NoSensitive" "${LAST_BODY}"

if need_cmd jq; then
	job_match="$(printf '%s' "${LAST_BODY}" | jq -r --arg id "${JOB_ID}" '
		(.output.jobs // [])
		| map(select((.jobID // "") == $id))
		| length
	' 2>/dev/null || true)"
	if [[ -z "${job_match}" ]] || (( job_match < 1 )); then
		fail_step "MCPAllowListAIJobs.AssertFound" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
fi

get_ai_job_body=$(cat <<JSON
{"tool":"get_ai_job","input":{"job_id":"${JOB_ID}"}}
JSON
)
call_or_fail "MCPAllowGetAIJob" POST "${BASE_URL}/v1/mcp/tools/call" "${get_ai_job_body}" "ai_job.read"
assert_no_sensitive "MCPAllowGetAIJob.NoSensitive" "${LAST_BODY}"

mcp_get_incident_body=$(cat <<JSON
{"tool":"get_incident","input":{"incident_id":"${INCIDENT_ID}"},"idempotency_key":"mcp-c3-audit-${rand}"}
JSON
)
call_or_fail "MCPAuditSeedGetIncident" POST "${BASE_URL}/v1/mcp/tools/call" "${mcp_get_incident_body}" "incident.read"
TOOL_CALL_ID="$(extract_field "${LAST_BODY}" "tool_call_id" "toolCallID")" || true

list_tool_calls_body='{"tool":"list_tool_calls","input":{"job_id":"mcp-readonly","limit":50,"page":1}}'
call_or_fail "MCPAllowListToolCalls" POST "${BASE_URL}/v1/mcp/tools/call" "${list_tool_calls_body}" "toolcall.read"
assert_no_sensitive "MCPAllowListToolCalls.NoSensitive" "${LAST_BODY}"

if need_cmd jq; then
	truncated_flag="$(printf '%s' "${LAST_BODY}" | jq -r '.truncated // false' 2>/dev/null || true)"
	if [[ "${truncated_flag}" == "true" ]]; then
		if [[ "${LAST_BODY}" != *"mcp."* ]]; then
			fail_step "MCPAllowListToolCalls.AssertMCPPrefixTruncated" "${LAST_HTTP_CODE}" "${LAST_BODY}"
		fi
	else
		mcp_prefix_count="$(printf '%s' "${LAST_BODY}" | jq -r '
			(.output.toolCalls // [])
			| map(select((.toolName // "") | startswith("mcp.")))
			| length
		' 2>/dev/null || true)"
		if [[ -z "${mcp_prefix_count}" ]] || (( mcp_prefix_count < 1 )); then
			fail_step "MCPAllowListToolCalls.AssertMCPPrefix" "${LAST_HTTP_CODE}" "${LAST_BODY}"
		fi
	fi
fi

silence_body=$(cat <<JSON
{"namespace":"${test_namespace}","enabled":true,"startsAt":{"seconds":$((now_epoch - 60)),"nanos":0},"endsAt":{"seconds":$((now_epoch + 3600)),"nanos":0},"reason":"c3-l1","createdBy":"tester","matchers":[{"key":"service","op":"=","value":"c3-l1-svc"}]}
JSON
)
call_or_fail "CreateSilence" POST "${BASE_URL}/v1/silences" "${silence_body}" "silence.admin"
SILENCE_ID="$(extract_field "${LAST_BODY}" "silenceID" "silence_id")" || true

list_silences_body=$(cat <<JSON
{"tool":"list_silences","input":{"namespace":"${test_namespace}","limit":20,"page":1}}
JSON
)
call_or_fail "MCPAllowListSilences" POST "${BASE_URL}/v1/mcp/tools/call" "${list_silences_body}" "silence.read"
assert_no_sensitive "MCPAllowListSilences.NoSensitive" "${LAST_BODY}"

list_notice_body='{"tool":"list_notice_deliveries","input":{"limit":20,"page":1}}'
call_or_fail "MCPAllowListNoticeDeliveries" POST "${BASE_URL}/v1/mcp/tools/call" "${list_notice_body}" "notice.read"
assert_no_sensitive "MCPAllowListNoticeDeliveries.NoSensitive" "${LAST_BODY}"

if ! http_json POST "${BASE_URL}/v1/mcp/tools/call" "${list_ds_body}" "incident.read"; then
	fail_step "MCPDenyScope.Call"
fi
assert_error_shape_or_fail "MCPDenyScope" "SCOPE_DENIED" "403"

if (( TRUNC_ALERT_COUNT > 0 )); then
	info "MCPTruncationSeed total=${TRUNC_ALERT_COUNT} progress_every=${SEED_PROGRESS_EVERY}"
fi
for i in $(seq 1 "${TRUNC_ALERT_COUNT}"); do
	trunc_seed_body=$(cat <<JSON
{"tool":"get_incident","input":{"incident_id":"${INCIDENT_ID}"},"idempotency_key":"mcp-c3-trunc-${rand}-${i}"}
JSON
)
	if ! http_json POST "${BASE_URL}/v1/mcp/tools/call" "${trunc_seed_body}" "incident.read"; then
		fail_step "MCPTruncationSeed.Call.${i}"
	fi
	if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
		fail_step "MCPTruncationSeed.Call.${i}" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
	if (( i == 1 || i == TRUNC_ALERT_COUNT || (SEED_PROGRESS_EVERY > 0 && i % SEED_PROGRESS_EVERY == 0) )); then
		info "MCPTruncationSeed progress=${i}/${TRUNC_ALERT_COUNT}"
	fi
done

trunc_call_body='{"tool":"list_tool_calls","input":{"job_id":"mcp-readonly","limit":200,"page":1}}'
call_or_fail "MCPTruncationCall" POST "${BASE_URL}/v1/mcp/tools/call" "${trunc_call_body}" "toolcall.read"
assert_no_sensitive "MCPTruncationCall.NoSensitive" "${LAST_BODY}"

body_bytes="$(printf '%s' "${LAST_BODY}" | wc -c | tr -d '[:space:]')"
if [[ -z "${body_bytes}" ]] || (( body_bytes > 16384 )); then
	fail_step "MCPTruncation.BodySize" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

if need_cmd jq; then
	truncated_flag="$(printf '%s' "${LAST_BODY}" | jq -r '.truncated // false' 2>/dev/null || true)"
	warning_count="$(printf '%s' "${LAST_BODY}" | jq -r '[.warnings[]? | select(.=="TRUNCATED_OUTPUT")] | length' 2>/dev/null || true)"
	if [[ "${truncated_flag}" != "true" ]]; then
		fail_step "MCPTruncation.TruncatedFlag" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
	if [[ -z "${warning_count}" ]] || (( warning_count < 1 )); then
		fail_step "MCPTruncation.WarningFlag" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
else
	if [[ "${LAST_BODY}" != *'"truncated":true'* ]]; then
		fail_step "MCPTruncation.TruncatedFlag" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
	if [[ "${LAST_BODY}" != *'TRUNCATED_OUTPUT'* ]]; then
		fail_step "MCPTruncation.WarningFlag" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
fi

echo "PASS C3 allow list_incidents incident_id=${INCIDENT_ID}"
echo "PASS C3 allow datasource datasource_id=${DATASOURCE_ID}"
echo "PASS C3 allow ai_job job_id=${JOB_ID}"
echo "PASS C3 allow tool_calls tool_call_id=${TOOL_CALL_ID:-NONE}"
echo "PASS C3 deny scope=datasource.read"
echo "PASS C3 truncation body_bytes=${body_bytes}"
echo "PASS C3 mcp tools expansion"
