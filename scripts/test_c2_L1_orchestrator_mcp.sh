#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
DEBUG="${DEBUG:-0}"
JOB_WAIT_TIMEOUT_SEC="${JOB_WAIT_TIMEOUT_SEC:-120}"
JOB_POLL_INTERVAL_SEC="${JOB_POLL_INTERVAL_SEC:-1}"

INCIDENT_ID=""
JOB_ID=""
TOOL_CALL_ID=""
EVIDENCE_ID=""

LAST_HTTP_CODE=""
LAST_BODY=""

debug() {
	if [[ "${DEBUG}" == "1" ]]; then
		echo "[DEBUG] $*" >&2
	fi
}

need_cmd() {
	command -v "$1" >/dev/null 2>&1
}

extract_field() {
	local json="$1"
	shift
	local keys=("$@")
	local key value

	if need_cmd jq; then
		for key in "${keys[@]}"; do
			value="$(
				printf '%s' "${json}" | jq -r --arg k "${key}" '
					(.[$k] // .data[$k] // .job[$k] // .data.job[$k] //
					 .incident[$k] // .data.incident[$k] //
					 .evidence[$k] // .data.evidence[$k]) |
					if . == null then empty
					elif type == "string" then .
					else tojson
					end
				' 2>/dev/null
			)"
			if [[ -n "${value}" ]]; then
				printf '%s' "${value}"
				return 0
			fi
		done
	else
		for key in "${keys[@]}"; do
			value="$(printf '%s' "${json}" | sed -n "s/.*\"${key}\"[[:space:]]*:[[:space:]]*\"\\([^\"]*\\)\".*/\\1/p" | head -n 1)"
			if [[ -n "${value}" ]]; then
				printf '%s' "${value}"
				return 0
			fi
		done
	fi

	return 1
}

truncate_2kb() {
	printf '%s' "$1" | head -c 2048
}

fail_step() {
	local step="$1"
	local code="${2:-${LAST_HTTP_CODE}}"
	local body="${3:-${LAST_BODY}}"

	echo "FAIL C2 step=${step}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	truncate_2kb "${body}"
	echo
	echo "EOF"
	echo "incident_id=${INCIDENT_ID:-NONE}"
	echo "job_id=${JOB_ID:-NONE}"
	echo "tool_call_id=${TOOL_CALL_ID:-NONE}"
	exit 1
}

http_json() {
	local method="$1"
	local url="$2"
	local body="${3:-}"

	local tmp_body tmp_err code rc curl_err
	tmp_body="$(mktemp)"
	tmp_err="$(mktemp)"

	local -a cmd
	cmd=("${CURL}" -sS -o "${tmp_body}" -w "%{http_code}" -X "${method}" "${url}" -H "Accept: application/json")
	if [[ -n "${SCOPES}" ]]; then
		cmd+=(-H "X-Scopes: ${SCOPES}")
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

	if ! http_json "${method}" "${url}" "${body}"; then
		fail_step "${step}"
	fi
	if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
		fail_step "${step}"
	fi
	debug "${step} code=${LAST_HTTP_CODE}"
}

wait_for_ai_job_terminal() {
	local deadline status now
	deadline="$(( $(date +%s) + JOB_WAIT_TIMEOUT_SEC ))"

	while true; do
		call_or_fail "PollAIJob" GET "${BASE_URL}/v1/ai/jobs/${JOB_ID}"
		status="$(extract_field "${LAST_BODY}" "status")" || true
		if [[ -z "${status}" ]]; then
			fail_step "PollAIJobStatusParse" "${LAST_HTTP_CODE}" "${LAST_BODY}"
		fi
		debug "job_id=${JOB_ID} status=${status}"

		case "${status}" in
			queued|running)
				now="$(date +%s)"
				if (( now > deadline )); then
					fail_step "PollAIJobTimeout" "TIMEOUT" "${LAST_BODY}"
				fi
				sleep "${JOB_POLL_INTERVAL_SEC}"
				;;
			succeeded)
				return 0
				;;
			failed|canceled)
				fail_step "PollAIJobTerminal=${status}" "${LAST_HTTP_CODE}" "${LAST_BODY}"
				;;
			*)
				now="$(date +%s)"
				if (( now > deadline )); then
					fail_step "PollAIJobTimeoutUnknownStatus=${status}" "TIMEOUT" "${LAST_BODY}"
				fi
				sleep "${JOB_POLL_INTERVAL_SEC}"
				;;
		esac
	done
}

validate_incident_writeback() {
	local body="$1"
	local diagnosis_raw="" evidence_refs_raw="" rca_status="" root_cause_summary=""
	local confidence="" evidence_count=""

	if need_cmd jq; then
		diagnosis_raw="$(extract_field "${body}" "diagnosisJSON" "diagnosis_json")" || true
		evidence_refs_raw="$(extract_field "${body}" "evidenceRefsJSON" "evidence_refs_json")" || true
		rca_status="$(extract_field "${body}" "rcaStatus" "rca_status")" || true
		root_cause_summary="$(extract_field "${body}" "rootCauseSummary" "root_cause_summary")" || true

		if [[ -z "${diagnosis_raw}" ]] || [[ -z "${evidence_refs_raw}" ]] || [[ -z "${rca_status}" ]] || [[ -z "${root_cause_summary}" ]]; then
			fail_step "GetIncidentValidate" "${LAST_HTTP_CODE}" "${body}"
		fi

		confidence="$(printf '%s' "${diagnosis_raw}" | jq -r '.root_cause.confidence // empty' 2>/dev/null || true)"
		evidence_count="$(printf '%s' "${diagnosis_raw}" | jq -r '[.root_cause.evidence_ids[]?] | length' 2>/dev/null || true)"
		if [[ -z "${confidence}" ]] || [[ -z "${evidence_count}" ]] || (( evidence_count < 1 )); then
			fail_step "GetIncidentDiagnosisValidation" "${LAST_HTTP_CODE}" "${body}"
		fi

		evidence_count="$(printf '%s' "${evidence_refs_raw}" | jq -r '[.evidence_ids[]?] | length' 2>/dev/null || true)"
		if [[ -z "${evidence_count}" ]] || (( evidence_count < 1 )); then
			fail_step "GetIncidentEvidenceRefsValidation" "${LAST_HTTP_CODE}" "${body}"
		fi
		EVIDENCE_ID="$(printf '%s' "${evidence_refs_raw}" | jq -r '.evidence_ids[0] // empty' 2>/dev/null || true)"
	else
		if ! printf '%s' "${body}" | grep -Eq '"diagnosisJSON"|"diagnosis_json"'; then
			fail_step "GetIncidentValidate" "${LAST_HTTP_CODE}" "${body}"
		fi
		if ! printf '%s' "${body}" | grep -Eq '"evidenceRefsJSON"|"evidence_refs_json"'; then
			fail_step "GetIncidentValidate" "${LAST_HTTP_CODE}" "${body}"
		fi
	fi
}

assert_toolcalls_contains_mcp() {
	local body="$1"
	local has_prefix=0 has_target=0

	if need_cmd jq; then
		has_prefix="$(printf '%s' "${body}" | jq -r '
			(.toolCalls // .data.toolCalls // [])
			| map(select((.toolName // .tool_name // "") | startswith("mcp.")))
			| length
		' 2>/dev/null || true)"
		has_target="$(printf '%s' "${body}" | jq -r '
			(.toolCalls // .data.toolCalls // [])
			| map(select((.toolName // .tool_name // "") == "mcp.query_metrics" or (.toolName // .tool_name // "") == "mcp.query_logs"))
			| length
		' 2>/dev/null || true)"
		TOOL_CALL_ID="$(printf '%s' "${body}" | jq -r '
			(.toolCalls // .data.toolCalls // [])
			| map(select((.toolName // .tool_name // "") == "mcp.query_metrics" or (.toolName // .tool_name // "") == "mcp.query_logs"))
			| .[0].toolCallID // .[0].tool_call_id // empty
		' 2>/dev/null || true)"
	else
		if printf '%s' "${body}" | grep -Eq '"toolName"[[:space:]]*:[[:space:]]*"mcp\.'; then
			has_prefix=1
		fi
		if printf '%s' "${body}" | grep -Eq '"toolName"[[:space:]]*:[[:space:]]*"mcp\.query_metrics"|"toolName"[[:space:]]*:[[:space:]]*"mcp\.query_logs"'; then
			has_target=1
		fi
	fi

	if [[ -z "${has_prefix}" ]] || (( has_prefix < 1 )); then
		fail_step "ListToolCalls.MCPPrefixMissing" "${LAST_HTTP_CODE}" "${body}"
	fi
	if [[ -z "${has_target}" ]] || (( has_target < 1 )); then
		fail_step "ListToolCalls.MCPQueryToolMissing" "${LAST_HTTP_CODE}" "${body}"
	fi
	if [[ -z "${TOOL_CALL_ID}" ]]; then
		TOOL_CALL_ID="$(extract_field "${body}" "toolCallID" "tool_call_id")" || true
	fi
}

rand="${RAND:-$RANDOM}"
now_epoch="$(date -u +%s)"
start_epoch="$((now_epoch - 1800))"
fingerprint="c2-l1-mcp-fp-${rand}"

ingest_body=$(cat <<EOF
{"idempotencyKey":"idem-c2-l1-ingest-${rand}","fingerprint":"${fingerprint}","status":"firing","severity":"P1","service":"c2-l1-svc","cluster":"prod-c2","namespace":"default","workload":"c2-l1-workload","lastSeenAt":{"seconds":${now_epoch},"nanos":0},"labelsJSON":"{\"alertname\":\"HTTP5xxHigh\",\"service\":\"c2-l1-svc\"}"}
EOF
)

call_or_fail "IngestAlertEvent" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_body}"
INCIDENT_ID="$(extract_field "${LAST_BODY}" "incidentID" "incident_id")" || true
if [[ -z "${INCIDENT_ID}" ]]; then
	fail_step "IngestAlertEventParseIncidentID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

run_body=$(cat <<EOF
{"incidentID":"${INCIDENT_ID}","idempotencyKey":"idem-c2-l1-ai-run-${rand}","pipeline":"basic_rca","trigger":"manual","timeRangeStart":{"seconds":${start_epoch},"nanos":0},"timeRangeEnd":{"seconds":${now_epoch},"nanos":0},"inputHintsJSON":"{\"scenario\":\"C2_L1_MCP_ALIGNMENT\"}","createdBy":"system"}
EOF
)
call_or_fail "RunAIJob" POST "${BASE_URL}/v1/incidents/${INCIDENT_ID}/ai:run" "${run_body}"
JOB_ID="$(extract_field "${LAST_BODY}" "jobID" "job_id")" || true
if [[ -z "${JOB_ID}" ]]; then
	fail_step "RunAIJobParseJobID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

wait_for_ai_job_terminal

call_or_fail "ListToolCalls" GET "${BASE_URL}/v1/ai/jobs/${JOB_ID}/tool-calls?offset=0&limit=50"
assert_toolcalls_contains_mcp "${LAST_BODY}"

call_or_fail "GetIncident" GET "${BASE_URL}/v1/incidents/${INCIDENT_ID}"
validate_incident_writeback "${LAST_BODY}"

if [[ -n "${EVIDENCE_ID}" ]]; then
	call_or_fail "GetEvidence" GET "${BASE_URL}/v1/evidence/${EVIDENCE_ID}"
fi

echo "PASS C2"
echo "incident_id=${INCIDENT_ID}"
echo "job_id=${JOB_ID}"
echo "tool_call_id=${TOOL_CALL_ID:-NONE}"
echo "mcp_toolcall_present=1"
