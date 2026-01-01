#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
DEBUG="${DEBUG:-0}"
MCP_TOOLS_VERSION="${MCP_TOOLS_VERSION:-c1}"
TRUNC_AUDIT_SEED_COUNT="${TRUNC_AUDIT_SEED_COUNT:-24}"
SEED_PROGRESS_EVERY="${SEED_PROGRESS_EVERY:-10}"
CURL_CONNECT_TIMEOUT="${CURL_CONNECT_TIMEOUT:-5}"
CURL_MAX_TIME="${CURL_MAX_TIME:-30}"
NOTICE_WAIT_TIMEOUT_SEC="${NOTICE_WAIT_TIMEOUT_SEC:-20}"
NOTICE_WAIT_INTERVAL_SEC="${NOTICE_WAIT_INTERVAL_SEC:-1}"

LAST_HTTP_CODE=""
LAST_BODY=""

INCIDENT_ID=""
DELIVERY_ID=""
TOOL_CALL_ID=""
EVIDENCE_ID=""
DATASOURCE_ID=""
CHANNEL_ID=""

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

to_rfc3339() {
	local epoch="$1"
	if date -u -d "@${epoch}" +%Y-%m-%dT%H:%M:%SZ >/dev/null 2>&1; then
		date -u -d "@${epoch}" +%Y-%m-%dT%H:%M:%SZ
		return 0
	fi
	date -u -r "${epoch}" +%Y-%m-%dT%H:%M:%SZ
}

truncate_2kb() {
	printf '%s' "$1" | head -c 2048
}

fail_step() {
	local step="$1"
	local code="${2:-${LAST_HTTP_CODE}}"
	local body="${3:-${LAST_BODY}}"

	echo "FAIL C4 step=${step}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	truncate_2kb "${body}"
	echo
	echo "EOF"
	echo "incident_id=${INCIDENT_ID:-NONE}"
	echo "delivery_id=${DELIVERY_ID:-NONE}"
	echo "tool_call_id=${TOOL_CALL_ID:-NONE}"
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
				"search_incidents",
				"get_incident_timeline",
				"search_evidence",
				"get_notice_deliveries_by_incident",
				"list_notice_deliveries_by_time",
				"get_notice_delivery"
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
		for tool in search_incidents get_incident_timeline search_evidence get_notice_deliveries_by_incident list_notice_deliveries_by_time get_notice_delivery; do
			if [[ "${LAST_BODY}" != *"\"name\":\"${tool}\""* ]]; then
				fail_step "MCPListTools.RequiredTools.${tool}" "${LAST_HTTP_CODE}" "${LAST_BODY}"
			fi
		done
	fi
}

wait_notice_delivery_or_fail() {
	local deadline now list_body delivery_count
	deadline="$(( $(date +%s) + NOTICE_WAIT_TIMEOUT_SEC ))"
	list_body="$(cat <<JSON
{"tool":"get_notice_deliveries_by_incident","input":{"incident_id":"${INCIDENT_ID}","limit":20,"page":1}}
JSON
)"

	while true; do
		call_or_fail "MCPAllowGetNoticeDeliveriesByIncident" POST "${BASE_URL}/v1/mcp/tools/call" "${list_body}" "notice.read"
		assert_no_sensitive "MCPAllowGetNoticeDeliveriesByIncident.NoSensitive" "${LAST_BODY}"

		if need_cmd jq; then
			delivery_count="$(printf '%s' "${LAST_BODY}" | jq -r '(.output.noticeDeliveries // []) | length' 2>/dev/null || true)"
			if [[ -n "${delivery_count}" ]] && (( delivery_count > 0 )); then
				DELIVERY_ID="$(printf '%s' "${LAST_BODY}" | jq -r '.output.noticeDeliveries[0].deliveryID // empty' 2>/dev/null || true)"
				TOOL_CALL_ID="$(extract_field "${LAST_BODY}" "tool_call_id" "toolCallID")" || true
				return 0
			fi
		else
			DELIVERY_ID="$(extract_field "${LAST_BODY}" "deliveryID" "delivery_id")" || true
			if [[ -n "${DELIVERY_ID}" ]]; then
				return 0
			fi
		fi

		now="$(date +%s)"
		if (( now > deadline )); then
			fail_step "MCPAllowGetNoticeDeliveriesByIncident.Timeout" "${LAST_HTTP_CODE}" "${LAST_BODY}"
		fi
		sleep "${NOTICE_WAIT_INTERVAL_SEC}"
	done
}

assert_mcp_search_incidents_audit_or_fail() {
	local body="$1"
	local matched_count

	if need_cmd jq; then
		matched_count="$(printf '%s' "${body}" | jq -r '
			(.toolCalls // .data.toolCalls // [])
			| map(select((.toolName // "") == "mcp.search_incidents"))
			| length
		' 2>/dev/null || true)"
		if [[ -z "${matched_count}" ]] || (( matched_count < 1 )); then
			fail_step "MCPAudit.MissingSearchIncidents" "${LAST_HTTP_CODE}" "${body}"
		fi
		TOOL_CALL_ID="$(printf '%s' "${body}" | jq -r '
			(.toolCalls // .data.toolCalls // [])
			| map(select((.toolName // "") == "mcp.search_incidents"))
			| .[0].toolCallID // empty
		' 2>/dev/null || true)"
	else
		if [[ "${body}" != *'"toolName":"mcp.search_incidents"'* ]]; then
			fail_step "MCPAudit.MissingSearchIncidents" "${LAST_HTTP_CODE}" "${body}"
		fi
	fi
}

rand="${RAND:-$RANDOM}"
now_epoch="$(date -u +%s)"
start_epoch="$((now_epoch - 1800))"
timeline_from_epoch="$((now_epoch - 3600))"
timeline_to_epoch="$((now_epoch + 60))"
notice_time_from_epoch="$((now_epoch - 2505600))"
notice_time_to_epoch="$((now_epoch + 86400))"
test_namespace="c4-ns-${rand}"
fingerprint="c4-l1-fp-${rand}"
timeline_from_rfc3339="$(to_rfc3339 "${timeline_from_epoch}")"
timeline_to_rfc3339="$(to_rfc3339 "${timeline_to_epoch}")"
start_rfc3339="$(to_rfc3339 "${start_epoch}")"
now_rfc3339="$(to_rfc3339 "${now_epoch}")"
notice_time_from_rfc3339="$(to_rfc3339 "${notice_time_from_epoch}")"
notice_time_to_rfc3339="$(to_rfc3339 "${notice_time_to_epoch}")"

call_or_fail "MCPListTools" GET "${BASE_URL}/v1/mcp/tools" "" "${SCOPES}"
assert_no_sensitive "MCPListTools.NoSensitive" "${LAST_BODY}"
assert_list_tools_or_fail

create_channel_body=$(cat <<JSON
{"name":"c4-channel-${rand}","type":"webhook","endpointURL":"http://127.0.0.1:18080/notice-c4","enabled":true,"maxRetries":1}
JSON
)
call_or_fail "CreateNoticeChannel" POST "${BASE_URL}/v1/notice-channels" "${create_channel_body}" "notice.admin"
CHANNEL_ID="$(extract_field "${LAST_BODY}" "channelID" "channel_id")" || true

ingest_body=$(cat <<JSON
{"idempotencyKey":"idem-c4-l1-ingest-${rand}","fingerprint":"${fingerprint}","status":"firing","severity":"P1","service":"c4-l1-svc","cluster":"prod-c4","namespace":"${test_namespace}","workload":"demo-c4","lastSeenAt":{"seconds":${now_epoch},"nanos":0}}
JSON
)
call_or_fail "IngestAlertEvent" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_body}" "alert.ingest"
INCIDENT_ID="$(extract_field "${LAST_BODY}" "incidentID" "incident_id")" || true
if [[ -z "${INCIDENT_ID}" ]]; then
	fail_step "IngestAlertEvent.ParseIncidentID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

search_incidents_body=$(cat <<JSON
{"tool":"search_incidents","input":{"namespace":"${test_namespace}","limit":20,"page":1}}
JSON
)
call_or_fail "MCPAllowSearchIncidents" POST "${BASE_URL}/v1/mcp/tools/call" "${search_incidents_body}" "incident.read"
assert_no_sensitive "MCPAllowSearchIncidents.NoSensitive" "${LAST_BODY}"
TOOL_CALL_ID="$(extract_field "${LAST_BODY}" "tool_call_id" "toolCallID")" || true

if need_cmd jq; then
	incident_match="$(printf '%s' "${LAST_BODY}" | jq -r --arg id "${INCIDENT_ID}" '
		(.output.incidents // [])
		| map(select((.incidentID // "") == $id))
		| length
	' 2>/dev/null || true)"
	if [[ -z "${incident_match}" ]] || (( incident_match < 1 )); then
		fail_step "MCPAllowSearchIncidents.AssertFound" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
fi

timeline_body=$(cat <<JSON
{"tool":"get_incident_timeline","input":{"incident_id":"${INCIDENT_ID}","limit":20,"page":1}}
JSON
)
call_or_fail "MCPAllowGetIncidentTimeline" POST "${BASE_URL}/v1/mcp/tools/call" "${timeline_body}" "incident.read"
assert_no_sensitive "MCPAllowGetIncidentTimeline.NoSensitive" "${LAST_BODY}"

create_ds_body=$(cat <<JSON
{"type":"prometheus","name":"c4-ds-${rand}","baseURL":"http://127.0.0.1:9090","authType":"none","timeoutMs":2000,"isEnabled":true}
JSON
)
call_or_fail "CreateDatasource" POST "${BASE_URL}/v1/datasources" "${create_ds_body}" "datasource.admin"
DATASOURCE_ID="$(extract_field "${LAST_BODY}" "datasourceID" "datasource_id")" || true
if [[ -z "${DATASOURCE_ID}" ]]; then
	fail_step "CreateDatasource.ParseID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

save_evidence_body=$(cat <<JSON
{"idempotencyKey":"idem-c4-evidence-${rand}","type":"metrics","datasourceID":"${DATASOURCE_ID}","queryText":"up{service=\"c4-l1-svc\"}","timeRangeStart":{"seconds":${start_epoch},"nanos":0},"timeRangeEnd":{"seconds":${now_epoch},"nanos":0},"resultJSON":"{\"data\":{\"result\":[{\"metric\":{\"__name__\":\"up\"},\"values\":[[${now_epoch},\"1\"]]}]}}","summary":"c4 evidence seed","createdBy":"script"}
JSON
)
call_or_fail "SaveEvidence" POST "${BASE_URL}/v1/incidents/${INCIDENT_ID}/evidence" "${save_evidence_body}" "evidence.save"
EVIDENCE_ID="$(extract_field "${LAST_BODY}" "evidenceID" "evidence_id")" || true
if [[ -z "${EVIDENCE_ID}" ]]; then
	fail_step "SaveEvidence.ParseID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

search_evidence_body=$(cat <<JSON
{"tool":"search_evidence","input":{"incident_id":"${INCIDENT_ID}","limit":20,"page":1}}
JSON
)
call_or_fail "MCPAllowSearchEvidence" POST "${BASE_URL}/v1/mcp/tools/call" "${search_evidence_body}" "evidence.read"
assert_no_sensitive "MCPAllowSearchEvidence.NoSensitive" "${LAST_BODY}"

if need_cmd jq; then
	evidence_match="$(printf '%s' "${LAST_BODY}" | jq -r --arg id "${EVIDENCE_ID}" '
		(.output.evidence // [])
		| map(select((.evidenceID // "") == $id))
		| length
	' 2>/dev/null || true)"
	if [[ -z "${evidence_match}" ]] || (( evidence_match < 1 )); then
		fail_step "MCPAllowSearchEvidence.AssertFound" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
fi

wait_notice_delivery_or_fail
if [[ -z "${DELIVERY_ID}" ]]; then
	fail_step "MCPAllowGetNoticeDeliveriesByIncident.ParseDeliveryID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

get_notice_delivery_body=$(cat <<JSON
{"tool":"get_notice_delivery","input":{"delivery_id":"${DELIVERY_ID}"}}
JSON
)
call_or_fail "MCPAllowGetNoticeDelivery" POST "${BASE_URL}/v1/mcp/tools/call" "${get_notice_delivery_body}" "notice.read"
assert_no_sensitive "MCPAllowGetNoticeDelivery.NoSensitive" "${LAST_BODY}"

if need_cmd jq; then
	got_delivery_id="$(printf '%s' "${LAST_BODY}" | jq -r '.output.deliveryID // empty' 2>/dev/null || true)"
	if [[ "${got_delivery_id}" != "${DELIVERY_ID}" ]]; then
		fail_step "MCPAllowGetNoticeDelivery.AssertID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
fi

list_notice_by_time_body=$(cat <<JSON
{"tool":"list_notice_deliveries_by_time","input":{"time_from":"${notice_time_from_rfc3339}","time_to":"${notice_time_to_rfc3339}","incident_id":"${INCIDENT_ID}","limit":20,"page":1}}
JSON
)
call_or_fail "MCPAllowListNoticeDeliveriesByTime" POST "${BASE_URL}/v1/mcp/tools/call" "${list_notice_by_time_body}" "notice.read"
assert_no_sensitive "MCPAllowListNoticeDeliveriesByTime.NoSensitive" "${LAST_BODY}"

if ! http_json POST "${BASE_URL}/v1/mcp/tools/call" "${search_incidents_body}" "datasource.read"; then
	fail_step "MCPDenyScope.Call"
fi
assert_error_shape_or_fail "MCPDenyScope" "SCOPE_DENIED" "403"

call_or_fail "MCPAuditListToolCalls" GET "${BASE_URL}/v1/ai/jobs/mcp-readonly/tool-calls?offset=0&limit=200" "" "ai.read"
assert_mcp_search_incidents_audit_or_fail "${LAST_BODY}"

if (( TRUNC_AUDIT_SEED_COUNT > 0 )); then
	info "MCPTruncationSeed total=${TRUNC_AUDIT_SEED_COUNT} progress_every=${SEED_PROGRESS_EVERY}"
fi
for i in $(seq 1 "${TRUNC_AUDIT_SEED_COUNT}"); do
	trunc_seed_body=$(cat <<JSON
{"tool":"get_incident","input":{"incident_id":"${INCIDENT_ID}"},"idempotency_key":"mcp-c4-trunc-${rand}-${i}"}
JSON
)
	if ! http_json POST "${BASE_URL}/v1/mcp/tools/call" "${trunc_seed_body}" "incident.read"; then
		fail_step "MCPTruncationSeed.Call.${i}"
	fi
	if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
		fail_step "MCPTruncationSeed.Call.${i}" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
	if (( i == 1 || i == TRUNC_AUDIT_SEED_COUNT || (SEED_PROGRESS_EVERY > 0 && i % SEED_PROGRESS_EVERY == 0) )); then
		info "MCPTruncationSeed progress=${i}/${TRUNC_AUDIT_SEED_COUNT}"
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

echo "PASS C4 allow search_incidents incident_id=${INCIDENT_ID}"
echo "PASS C4 allow timeline incident_id=${INCIDENT_ID}"
echo "PASS C4 allow search_evidence evidence_id=${EVIDENCE_ID}"
echo "PASS C4 allow notice delivery_id=${DELIVERY_ID}"
echo "PASS C4 deny scope=incident.read"
echo "PASS C4 audit tool_call_id=${TOOL_CALL_ID:-NONE}"
echo "PASS C4 truncation body_bytes=${body_bytes}"
echo "PASS C4 mcp advanced views"
