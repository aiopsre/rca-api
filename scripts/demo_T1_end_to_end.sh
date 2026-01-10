#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
METRICS_URL="${METRICS_URL:-http://127.0.0.1:9090}"
SCOPES="${SCOPES:-*}"
ALLOWED_NAMESPACES="${ALLOWED_NAMESPACES:-}"
ALLOWED_SERVICES="${ALLOWED_SERVICES:-}"
CURL="${CURL:-curl}"
DEBUG="${DEBUG:-0}"
JOB_WAIT_TIMEOUT_SEC="${JOB_WAIT_TIMEOUT_SEC:-180}"
JOB_POLL_INTERVAL_SEC="${JOB_POLL_INTERVAL_SEC:-1}"
CURL_CONNECT_TIMEOUT="${CURL_CONNECT_TIMEOUT:-5}"
CURL_MAX_TIME="${CURL_MAX_TIME:-30}"

LAST_HTTP_CODE=""
LAST_BODY=""
LAST_HEADERS=""
LAST_REQUEST_ID=""

REQUEST_ID=""
INCIDENT_ID=""
JOB_ID=""
TOOL_CALL_ID=""
DELIVERY_ID="NONE"
DELIVERY_STATUS="NONE"
DELIVERY_ATTEMPTS="0"
DATASOURCE_ID=""

QUALITY_GATE_DECISION="unknown"
ROOT_CAUSE_TYPE="unknown"
ROOT_CAUSE_SUMMARY=""
KB_REFS_COUNT="0"
VERIFICATION_PLAN_VERSION=""
PLAN_JSON=""
STEP_TOOL=""
STEP_PARAMS_JSON=""

OBSERVED="unknown"
MEETS_EXPECTATION="unknown"

TMP_FILES=()
MCP_EXTRA_HEADERS=()

need_cmd() {
	command -v "$1" >/dev/null 2>&1
}

debug() {
	if [[ "${DEBUG}" == "1" ]]; then
		echo "[DEBUG] $*" >&2
	fi
}

truncate_2kb() {
	printf '%s' "$1" | head -c 2048
}

cleanup() {
	for f in "${TMP_FILES[@]:-}"; do
		[[ -n "${f}" ]] && rm -f "${f}" >/dev/null 2>&1 || true
	done
}
trap cleanup EXIT

fail_step() {
	local step="$1"
	local code="${2:-${LAST_HTTP_CODE}}"
	local body="${3:-${LAST_BODY}}"

	echo "FAIL T1 step=${step}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	truncate_2kb "${body}"
	echo
	echo "EOF"
	echo "request_id=${REQUEST_ID:-NONE}"
	echo "incident_id=${INCIDENT_ID:-NONE}"
	echo "job_id=${JOB_ID:-NONE}"
	echo "tool_call_id=${TOOL_CALL_ID:-NONE}"
	echo "delivery_id=${DELIVERY_ID:-NONE}"
	echo "datasource_id=${DATASOURCE_ID:-NONE}"
	exit 1
}

assert_no_sensitive() {
	local step="$1"
	local body="${2:-${LAST_BODY}}"
	if printf '%s' "${body}" | grep -Eiq '("secret"[[:space:]]*:|\\"secret\\"[[:space:]]*:|"authorization"[[:space:]]*:|\\"authorization\\"[[:space:]]*:|"Authorization"[[:space:]]*:|\\"Authorization\\"[[:space:]]*:|"token"[[:space:]]*:|\\"token\\"[[:space:]]*:|"headers"[[:space:]]*:|\\"headers\\"[[:space:]]*:)'; then
		fail_step "${step}.SensitiveLeak" "${LAST_HTTP_CODE}" "${body}"
	fi
}

parse_request_id_from_headers() {
	local headers_file="$1"
	local rid
	rid="$(awk 'BEGIN{IGNORECASE=1} /^X-Request-Id:/{gsub("\r", "", $2); print $2; exit}' "${headers_file}" || true)"
	if [[ -z "${rid}" ]]; then
		rid="$(awk 'BEGIN{IGNORECASE=1} /^X-Trace-Id:/{gsub("\r", "", $2); print $2; exit}' "${headers_file}" || true)"
	fi
	printf '%s' "${rid}"
}

http_json() {
	local method="$1"
	local url="$2"
	local body="${3:-}"
	local scopes="${4:-${SCOPES}}"
	shift 4 || true
	local extra_headers=("$@")

	local tmp_body tmp_headers tmp_err code rc curl_err
	tmp_body="$(mktemp)"
	tmp_headers="$(mktemp)"
	tmp_err="$(mktemp)"
	TMP_FILES+=("${tmp_body}" "${tmp_headers}" "${tmp_err}")

	local -a cmd
	cmd=(
		"${CURL}"
		-sS
		--connect-timeout "${CURL_CONNECT_TIMEOUT}"
		--max-time "${CURL_MAX_TIME}"
		-D "${tmp_headers}"
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
	LAST_HEADERS="$(cat "${tmp_headers}")"
	LAST_REQUEST_ID="$(parse_request_id_from_headers "${tmp_headers}")"
	curl_err="$(cat "${tmp_err}")"

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

	if ! http_json "${method}" "${url}" "${body}" "${scopes}" "${extra_headers[@]}"; then
		fail_step "${step}"
	fi
	if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
		fail_step "${step}" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
	if [[ -n "${LAST_REQUEST_ID}" ]]; then
		REQUEST_ID="${LAST_REQUEST_ID}"
	fi
}

build_mcp_extra_headers() {
	MCP_EXTRA_HEADERS=()
	if [[ -n "${ALLOWED_NAMESPACES}" ]]; then
		MCP_EXTRA_HEADERS+=("X-Allowed-Namespaces: ${ALLOWED_NAMESPACES}")
	fi
	if [[ -n "${ALLOWED_SERVICES}" ]]; then
		MCP_EXTRA_HEADERS+=("X-Allowed-Services: ${ALLOWED_SERVICES}")
	fi
}

call_mcp_tool_or_fail() {
	local step="$1"
	local tool="$2"
	local input_json="$3"
	local payload
	payload="$(jq -cn --arg tool "${tool}" --argjson input "${input_json}" '{tool:$tool,input:$input}')"
	call_or_fail "${step}" POST "${BASE_URL}/v1/mcp/tools/call" "${payload}" "${SCOPES}" "${MCP_EXTRA_HEADERS[@]}"
}

extract_str() {
	local json="$1"
	local jq_expr="$2"
	printf '%s' "${json}" | jq -r "${jq_expr} // empty" 2>/dev/null || true
}

wait_for_ai_job_terminal() {
	local deadline status now
	deadline="$(( $(date +%s) + JOB_WAIT_TIMEOUT_SEC ))"

	while true; do
		call_or_fail "PollAIJob" GET "${BASE_URL}/v1/ai/jobs/${JOB_ID}" "" "${SCOPES}"
		status="$(extract_str "${LAST_BODY}" '.job.status // .status // .data.job.status')"
		if [[ -z "${status}" ]]; then
			fail_step "PollAIJob.ParseStatus" "${LAST_HTTP_CODE}" "${LAST_BODY}"
		fi

		case "${status}" in
		queued|running)
			now="$(date +%s)"
			if (( now > deadline )); then
				fail_step "PollAIJob.Timeout" "TIMEOUT" "${LAST_BODY}"
			fi
			sleep "${JOB_POLL_INTERVAL_SEC}"
			;;
		succeeded)
			return 0
			;;
		failed|canceled)
			fail_step "PollAIJob.Terminal=${status}" "${LAST_HTTP_CODE}" "${LAST_BODY}"
			;;
		*)
			now="$(date +%s)"
			if (( now > deadline )); then
				fail_step "PollAIJob.UnknownStatus=${status}" "TIMEOUT" "${LAST_BODY}"
			fi
			sleep "${JOB_POLL_INTERVAL_SEC}"
			;;
		esac
	done
}

parse_incident_summary_or_fail() {
	if ! printf '%s' "${LAST_BODY}" | jq -e '.output | type == "object"' >/dev/null 2>&1; then
		fail_step "MCPGetIncident.OutputMissing" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi

	local diagnosis_json
	diagnosis_json="$(printf '%s' "${LAST_BODY}" | jq -c 'try (.output.diagnosisJSON | fromjson) catch {}' 2>/dev/null || true)"
	if [[ -z "${diagnosis_json}" || "${diagnosis_json}" == "{}" ]]; then
		fail_step "MCPGetIncident.DiagnosisMissing" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi

	assert_no_sensitive "MCPGetIncident.DiagnosisNoSensitive" "${diagnosis_json}"

	QUALITY_GATE_DECISION="$(printf '%s' "${diagnosis_json}" | jq -r '.quality_gate.decision // .qualityGate.decision // empty' 2>/dev/null || true)"
	ROOT_CAUSE_TYPE="$(printf '%s' "${diagnosis_json}" | jq -r '.root_cause.type // .root_cause_type // .rootCause.type // empty' 2>/dev/null || true)"
	ROOT_CAUSE_SUMMARY="$(printf '%s' "${diagnosis_json}" | jq -r '.root_cause.summary // .root_cause_summary // .rootCause.summary // empty' 2>/dev/null || true)"
	KB_REFS_COUNT="$(printf '%s' "${diagnosis_json}" | jq -r '(.kb_refs // []) | length' 2>/dev/null || true)"
	PLAN_JSON="$(printf '%s' "${diagnosis_json}" | jq -c '.verification_plan // empty' 2>/dev/null || true)"
	if [[ -z "${PLAN_JSON}" ]]; then
		fail_step "MCPGetIncident.VerificationPlanMissing" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
	assert_no_sensitive "MCPGetIncident.VerificationPlanNoSensitive" "${PLAN_JSON}"

	VERIFICATION_PLAN_VERSION="$(printf '%s' "${PLAN_JSON}" | jq -r '.version // empty' 2>/dev/null || true)"
	if [[ "${VERIFICATION_PLAN_VERSION}" != "a5" ]]; then
		fail_step "MCPGetIncident.VerificationPlanVersion" "${LAST_HTTP_CODE}" "${PLAN_JSON}"
	fi

	STEP_TOOL="$(printf '%s' "${PLAN_JSON}" | jq -r '.steps[0].tool // empty' 2>/dev/null || true)"
	STEP_PARAMS_JSON="$(printf '%s' "${PLAN_JSON}" | jq -c '.steps[0].params // {}' 2>/dev/null || true)"
	if [[ -z "${STEP_TOOL}" ]]; then
		fail_step "MCPGetIncident.VerificationStepToolMissing" "${LAST_HTTP_CODE}" "${PLAN_JSON}"
	fi
	if ! printf '%s' "${STEP_PARAMS_JSON}" | jq -e 'type == "object"' >/dev/null 2>&1; then
		fail_step "MCPGetIncident.VerificationStepParamsInvalid" "${LAST_HTTP_CODE}" "${PLAN_JSON}"
	fi

	if [[ -z "${QUALITY_GATE_DECISION}" ]]; then
		QUALITY_GATE_DECISION="unknown"
	fi
	if [[ -z "${ROOT_CAUSE_TYPE}" ]]; then
		ROOT_CAUSE_TYPE="$(printf '%s' "${LAST_BODY}" | jq -r '.output.rootCauseType // empty' 2>/dev/null || true)"
	fi
	if [[ -z "${ROOT_CAUSE_SUMMARY}" ]]; then
		ROOT_CAUSE_SUMMARY="$(printf '%s' "${LAST_BODY}" | jq -r '.output.rootCauseSummary // empty' 2>/dev/null || true)"
	fi
	if [[ -z "${ROOT_CAUSE_TYPE}" ]]; then
		ROOT_CAUSE_TYPE="unknown"
	fi
	if [[ -z "${KB_REFS_COUNT}" ]]; then
		KB_REFS_COUNT="0"
	fi
}

search_query_toolcall_or_fail() {
	local step="$1"
	local input_json match mode
	local modes=()

	if [[ -n "${REQUEST_ID}" ]]; then
		modes+=("request_id")
	fi
	modes+=("incident_id")

	for mode in "${modes[@]}"; do
		if [[ "${mode}" == "request_id" ]]; then
			input_json="$(jq -cn --arg rid "${REQUEST_ID}" '{request_id:$rid,tool_prefix:"mcp.",limit:50,page:1}')"
		else
			input_json="$(jq -cn --arg incident_id "${INCIDENT_ID}" '{incident_id:$incident_id,tool_prefix:"mcp.",limit:50,page:1}')"
		fi

		call_mcp_tool_or_fail "${step}.${mode}" "search_tool_calls" "${input_json}"
		assert_no_sensitive "${step}.${mode}.NoSensitive" "${LAST_BODY}"

		match="$(printf '%s' "${LAST_BODY}" | jq -c '
			(.output.toolCalls // [])
			| map(
				select(
					((.toolName // .tool_name // "")
						| (startswith("mcp.query_") or startswith("mcp.mcp.query_")))
				)
			)
			| .[0] // empty
		' 2>/dev/null || true)"
		if [[ -z "${match}" ]]; then
			continue
		fi

		TOOL_CALL_ID="$(printf '%s' "${match}" | jq -r '.toolCallID // empty' 2>/dev/null || true)"
		if [[ -z "${JOB_ID}" ]]; then
			JOB_ID="$(printf '%s' "${match}" | jq -r '.jobID // empty' 2>/dev/null || true)"
		fi
		if [[ -n "${TOOL_CALL_ID}" ]]; then
			return 0
		fi
	done

	return 1
}

query_notice_deliveries_optional() {
	local input_json payload
	input_json="$(jq -cn --arg incident_id "${INCIDENT_ID}" '{incident_id:$incident_id,limit:20,page:1}')"
	payload="$(jq -cn --arg tool "get_notice_deliveries_by_incident" --argjson input "${input_json}" '{tool:$tool,input:$input}')"

	if ! http_json POST "${BASE_URL}/v1/mcp/tools/call" "${payload}" "${SCOPES}" "${MCP_EXTRA_HEADERS[@]}"; then
		fail_step "MCPGetNoticeDeliveriesByIncident.Call"
	fi

	if [[ "${LAST_HTTP_CODE}" == "403" || "${LAST_HTTP_CODE}" == "404" ]]; then
		debug "notice path skipped by status=${LAST_HTTP_CODE}"
		return 0
	fi
	if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
		fail_step "MCPGetNoticeDeliveriesByIncident" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi

	assert_no_sensitive "MCPGetNoticeDeliveriesByIncident.NoSensitive" "${LAST_BODY}"

	local first_delivery
	first_delivery="$(printf '%s' "${LAST_BODY}" | jq -c '(.output.noticeDeliveries // [])[0] // empty' 2>/dev/null || true)"
	if [[ -z "${first_delivery}" ]]; then
		DELIVERY_ID="NONE"
		DELIVERY_STATUS="NONE"
		DELIVERY_ATTEMPTS="0"
		return 0
	fi

	DELIVERY_ID="$(printf '%s' "${first_delivery}" | jq -r '.deliveryID // empty' 2>/dev/null || true)"
	DELIVERY_STATUS="$(printf '%s' "${first_delivery}" | jq -r '.status // empty' 2>/dev/null || true)"
	DELIVERY_ATTEMPTS="$(printf '%s' "${first_delivery}" | jq -r '.attempts // 0' 2>/dev/null || true)"
	if [[ -z "${DELIVERY_ID}" ]]; then
		DELIVERY_ID="NONE"
	fi
	if [[ -z "${DELIVERY_STATUS}" ]]; then
		DELIVERY_STATUS="UNKNOWN"
	fi
	if [[ -z "${DELIVERY_ATTEMPTS}" ]]; then
		DELIVERY_ATTEMPTS="0"
	fi
}

evaluate_expectation_best_effort() {
	local plan_json="$1"
	local mcp_resp="$2"

	local expected_type keyword threshold row_count result_lc
	expected_type="$(printf '%s' "${plan_json}" | jq -r '.steps[0].expected.type // "exists"' 2>/dev/null || true)"
	keyword="$(printf '%s' "${plan_json}" | jq -r '.steps[0].expected.keyword // empty' 2>/dev/null || true)"
	threshold="$(printf '%s' "${plan_json}" | jq -r '.steps[0].expected.value // empty' 2>/dev/null || true)"
	row_count="$(printf '%s' "${mcp_resp}" | jq -r '.output.rowCount // -1' 2>/dev/null || true)"
	result_lc="$(printf '%s' "${mcp_resp}" | jq -r '(.output.queryResultJSON // "" | ascii_downcase)' 2>/dev/null || true)"

	OBSERVED="row_count=${row_count}"
	MEETS_EXPECTATION="unknown"

	case "${expected_type}" in
	exists)
		if [[ "${row_count}" =~ ^[0-9]+$ ]] && (( row_count > 0 )); then
			MEETS_EXPECTATION="true"
		elif [[ -n "${result_lc}" && "${result_lc}" != "null" ]]; then
			MEETS_EXPECTATION="true"
		else
			MEETS_EXPECTATION="false"
		fi
		;;
	contains_keyword)
		if [[ -n "${keyword}" ]] && printf '%s' "${result_lc}" | grep -Fqi "${keyword}"; then
			MEETS_EXPECTATION="true"
		else
			MEETS_EXPECTATION="false"
		fi
		;;
	threshold_above)
		if [[ "${row_count}" =~ ^[0-9]+$ ]] && [[ "${threshold}" =~ ^[0-9]+([.][0-9]+)?$ ]]; then
			if awk "BEGIN {exit !(${row_count} > ${threshold})}"; then
				MEETS_EXPECTATION="true"
			else
				MEETS_EXPECTATION="false"
			fi
		fi
		;;
	threshold_below)
		if [[ "${row_count}" =~ ^[0-9]+$ ]] && [[ "${threshold}" =~ ^[0-9]+([.][0-9]+)?$ ]]; then
			if awk "BEGIN {exit !(${row_count} < ${threshold})}"; then
				MEETS_EXPECTATION="true"
			else
				MEETS_EXPECTATION="false"
			fi
		fi
		;;
	esac
}

if ! need_cmd jq; then
	fail_step "Precheck.MissingJQ" "PRECHECK" "jq command not found"
fi
if ! need_cmd "${CURL}"; then
	fail_step "Precheck.MissingCurl" "PRECHECK" "curl command not found"
fi

build_mcp_extra_headers

call_or_fail "Healthz" GET "${BASE_URL}/healthz" "" "${SCOPES}"

rand="${RAND:-$RANDOM}"
now_epoch="$(date -u +%s)"
start_epoch="$((now_epoch - 1800))"
namespace="t1-demo-ns-${rand}"
service="t1-demo-svc"
fingerprint="t1-demo-fp-${rand}"

ds_body="$(jq -cn \
	--arg name "t1-demo-prom-${rand}" \
	--arg base_url "${METRICS_URL}" \
	'{type:"prometheus",name:$name,baseURL:$base_url,authType:"none",timeoutMs:3000,isEnabled:true}'
)"
call_or_fail "CreateDatasource" POST "${BASE_URL}/v1/datasources" "${ds_body}" "${SCOPES}"
assert_no_sensitive "CreateDatasource.NoSensitive" "${LAST_BODY}"
DATASOURCE_ID="$(extract_str "${LAST_BODY}" '.datasourceID // .datasource_id // .datasource.datasourceID // .datasource.datasource_id')"
if [[ -z "${DATASOURCE_ID}" ]]; then
	fail_step "CreateDatasource.ParseDatasourceID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

ingest_body="$(jq -cn \
	--arg key "idem-t1-ingest-${rand}" \
	--arg fp "${fingerprint}" \
	--arg svc "${service}" \
	--arg ns "${namespace}" \
	--argjson now "${now_epoch}" \
	'{
		idempotencyKey:$key,
		fingerprint:$fp,
		status:"firing",
		severity:"P1",
		service:$svc,
		cluster:"prod-t1",
		namespace:$ns,
		workload:"t1-demo-workload",
		lastSeenAt:{seconds:$now,nanos:0},
		labelsJSON:("{\"alertname\":\"T1Demo\",\"service\":\"" + $svc + "\"}")
	}'
)"
call_or_fail "IngestAlertEvent" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_body}" "${SCOPES}"
INCIDENT_ID="$(extract_str "${LAST_BODY}" '.incidentID // .incident_id // .incident.incidentID // .incident.incident_id')"
if [[ -z "${INCIDENT_ID}" ]]; then
	fail_step "IngestAlertEvent.ParseIncidentID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

run_body="$(jq -cn \
	--arg incident_id "${INCIDENT_ID}" \
	--arg key "idem-t1-ai-run-${rand}" \
	--argjson start "${start_epoch}" \
	--argjson end "${now_epoch}" \
	'{
		incidentID:$incident_id,
		idempotencyKey:$key,
		pipeline:"basic_rca",
		trigger:"manual",
		timeRangeStart:{seconds:$start,nanos:0},
		timeRangeEnd:{seconds:$end,nanos:0},
		inputHintsJSON:"{\"scenario\":\"T1_DEMO\"}",
		createdBy:"system"
	}'
)"
call_or_fail "RunAIJob" POST "${BASE_URL}/v1/incidents/${INCIDENT_ID}/ai:run" "${run_body}" "${SCOPES}"
JOB_ID="$(extract_str "${LAST_BODY}" '.jobID // .job_id // .job.jobID // .job.job_id')"
if [[ -z "${JOB_ID}" ]]; then
	fail_step "RunAIJob.ParseJobID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

wait_for_ai_job_terminal

mcp_get_incident_input="$(jq -cn --arg incident_id "${INCIDENT_ID}" '{incident_id:$incident_id}')"
call_mcp_tool_or_fail "MCPGetIncident" "get_incident" "${mcp_get_incident_input}"
assert_no_sensitive "MCPGetIncident.NoSensitive" "${LAST_BODY}"
parse_incident_summary_or_fail

echo "quality_gate=${QUALITY_GATE_DECISION}"
echo "root_cause=${ROOT_CAUSE_TYPE}"
echo "kb_refs=${KB_REFS_COUNT}"
echo "verification_plan_version=${VERIFICATION_PLAN_VERSION}"

if ! search_query_toolcall_or_fail "MCPSearchToolCalls.BeforeRecheck"; then
	debug "no existing mcp.query_* tool call found before re-check"
fi

query_notice_deliveries_optional

call_mcp_tool_or_fail "MCPVerificationRecheck" "${STEP_TOOL}" "${STEP_PARAMS_JSON}"
assert_no_sensitive "MCPVerificationRecheck.NoSensitive" "${LAST_BODY}"
evaluate_expectation_best_effort "${PLAN_JSON}" "${LAST_BODY}"

if [[ -z "${TOOL_CALL_ID}" ]]; then
	if ! search_query_toolcall_or_fail "MCPSearchToolCalls.AfterRecheck"; then
		fail_step "MCPSearchToolCalls.MatchQueryTool" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
fi

echo "PASS T1 + IDs"
echo "request_id=${REQUEST_ID:-NONE}"
echo "incident_id=${INCIDENT_ID}"
echo "job_id=${JOB_ID}"
echo "tool_call_id=${TOOL_CALL_ID:-NONE}"
echo "delivery_id=${DELIVERY_ID:-NONE}"
echo "delivery_status=${DELIVERY_STATUS:-NONE}"
echo "delivery_attempts=${DELIVERY_ATTEMPTS:-0}"
echo "datasource_id=${DATASOURCE_ID}"
echo "quality_gate=${QUALITY_GATE_DECISION}"
echo "root_cause=${ROOT_CAUSE_TYPE}"
echo "root_cause_summary=${ROOT_CAUSE_SUMMARY}"
echo "kb_refs=${KB_REFS_COUNT}"
echo "verification_plan_version=${VERIFICATION_PLAN_VERSION}"
echo "observed=${OBSERVED}"
echo "meets_expectation=${MEETS_EXPECTATION}"
