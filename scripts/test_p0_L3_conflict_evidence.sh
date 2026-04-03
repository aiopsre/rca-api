#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
RUN_QUERY="${RUN_QUERY:-0}"
FORCE_CONFLICT="${FORCE_CONFLICT:-1}"
DEBUG="${DEBUG:-0}"
JOB_WAIT_TIMEOUT_SEC="${JOB_WAIT_TIMEOUT_SEC:-120}"
JOB_POLL_INTERVAL_SEC="${JOB_POLL_INTERVAL_SEC:-1}"

INCIDENT_ID=""
DATASOURCE_ID="${DATASOURCE_ID:-}"
EVIDENCE_IDS=""
JOB_ID=""
TOOL_CALL_ID_1=""
TOOL_CALL_ID_2=""
EVENT_ID=""

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
			value="$(printf '%s' "${json}" | sed -n "s/.*\"${key}\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" | head -n 1)"
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

	echo "FAIL L3-CONFLICT step=${step}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	truncate_2kb "${body}"
	echo
	echo "EOF"
	echo "incident_id=${INCIDENT_ID:-NONE}"
	echo "job_id=${JOB_ID:-NONE}"
	echo "datasource_id=${DATASOURCE_ID:-NONE}"
	echo "evidence_ids=${EVIDENCE_IDS:-NONE}"
	echo "tool_call_id=${TOOL_CALL_ID_1:-NONE},${TOOL_CALL_ID_2:-NONE}"
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

validate_incident_writeback_l3() {
	local body="$1"
	local diagnosis_raw="" root_type="" confidence="" missing_count="" evidence_count=""
	local -a evidence_list=()

	if ! need_cmd jq; then
		if ! printf '%s' "${body}" | grep -Eq '"diagnosisJSON"|"diagnosis_json"'; then
			fail_step "GetIncidentDiagnosisMissing" "${LAST_HTTP_CODE}" "${body}"
		fi
		if ! printf '%s' "${body}" | grep -Eq '"type"[[:space:]]*:[[:space:]]*"conflict_evidence"'; then
			fail_step "DiagnosisRootTypeInvalid" "${LAST_HTTP_CODE}" "${body}"
		fi
		if ! printf '%s' "${body}" | grep -q 'missing_evidence'; then
			fail_step "DiagnosisMissingEvidenceMissing" "${LAST_HTTP_CODE}" "${body}"
		fi
		return 0
	fi

	diagnosis_raw="$(extract_field "${body}" "diagnosisJSON" "diagnosis_json")" || true
	if [[ -z "${diagnosis_raw}" ]]; then
		fail_step "GetIncidentParseDiagnosis" "${LAST_HTTP_CODE}" "${body}"
	fi

	root_type="$(printf '%s' "${diagnosis_raw}" | jq -r '.root_cause.type // empty' 2>/dev/null || true)"
	if [[ "${root_type}" != "conflict_evidence" ]]; then
		fail_step "DiagnosisRootTypeInvalid" "${LAST_HTTP_CODE}" "${body}"
	fi

	confidence="$(printf '%s' "${diagnosis_raw}" | jq -r '.root_cause.confidence // empty' 2>/dev/null || true)"
	if [[ -z "${confidence}" ]]; then
		fail_step "DiagnosisConfidenceMissing" "${LAST_HTTP_CODE}" "${body}"
	fi
	if ! awk "BEGIN {exit !(${confidence} <= 0.3)}"; then
		fail_step "DiagnosisConfidenceTooHigh" "${LAST_HTTP_CODE}" "${body}"
	fi

	missing_count="$(printf '%s' "${diagnosis_raw}" | jq -r '[.missing_evidence[]? | select(type=="string" and (.|gsub("^\\s+|\\s+$";"")|length>0))] | length' 2>/dev/null || true)"
	if [[ -z "${missing_count}" ]] || (( missing_count < 1 )); then
		fail_step "DiagnosisMissingEvidenceEmpty" "${LAST_HTTP_CODE}" "${body}"
	fi
	if (( missing_count > 20 )); then
		fail_step "DiagnosisMissingEvidenceTooMany" "${LAST_HTTP_CODE}" "${body}"
	fi

	evidence_count="$(printf '%s' "${diagnosis_raw}" | jq -r '[.root_cause.evidence_ids[]? | select(type=="string" and (.|gsub("^\\s+|\\s+$";"")|length>0))] | length' 2>/dev/null || true)"
	if [[ -z "${evidence_count}" ]]; then
		fail_step "DiagnosisEvidenceIDsParse" "${LAST_HTTP_CODE}" "${body}"
	fi

	if (( evidence_count > 0 )); then
		mapfile -t evidence_list < <(
			printf '%s' "${diagnosis_raw}" |
				jq -r '.root_cause.evidence_ids[]? | select(type=="string" and (.|gsub("^\\s+|\\s+$";"")|length>0))' 2>/dev/null || true
		)
		if (( ${#evidence_list[@]} == 0 )); then
			fail_step "DiagnosisEvidenceIDsParse" "${LAST_HTTP_CODE}" "${body}"
		fi
		EVIDENCE_IDS="$(IFS=,; echo "${evidence_list[*]}")"
		for evidence_id in "${evidence_list[@]}"; do
			call_or_fail "GetEvidence(${evidence_id})" GET "${BASE_URL}/v1/evidence/${evidence_id}"
			ds_id="$(extract_field "${LAST_BODY}" "datasourceID" "datasource_id")" || true
			if [[ -n "${ds_id}" ]]; then
				DATASOURCE_ID="${ds_id}"
			fi
		done
	else
		EVIDENCE_IDS=""
	fi
}

if [[ "${FORCE_CONFLICT}" != "1" ]]; then
	debug "FORCE_CONFLICT=${FORCE_CONFLICT}; expected 1 for L3 conflict path."
fi
debug "RUN_QUERY=${RUN_QUERY} FORCE_CONFLICT=${FORCE_CONFLICT}"

rand="${RAND:-$RANDOM}"
now_epoch="$(date -u +%s)"
start_epoch="$((now_epoch - 1800))"
fingerprint="p0-l3-conflict-fp-${rand}"

ingest_body=$(cat <<EOF_INGEST
{"idempotencyKey":"idem-p0-l3-ingest-${rand}","fingerprint":"${fingerprint}","status":"firing","severity":"P1","service":"p0-l3-svc","cluster":"prod-l3","namespace":"default","workload":"p0-l3-workload","lastSeenAt":{"seconds":${now_epoch},"nanos":0},"labelsJSON":"{\"alertname\":\"ConflictEvidence\",\"service\":\"p0-l3-svc\"}"}
EOF_INGEST
)

call_or_fail "IngestAlertEvent" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_body}"
INCIDENT_ID="$(extract_field "${LAST_BODY}" "incidentID" "incident_id")" || true
EVENT_ID="$(extract_field "${LAST_BODY}" "eventID" "event_id")" || true
if [[ -z "${INCIDENT_ID}" ]]; then
	fail_step "IngestAlertEventParseIncidentID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

run_body=$(cat <<EOF_RUN
{"incidentID":"${INCIDENT_ID}","idempotencyKey":"idem-p0-l3-ai-run-${rand}","pipeline":"basic_rca","trigger":"manual","timeRangeStart":{"seconds":${start_epoch},"nanos":0},"timeRangeEnd":{"seconds":${now_epoch},"nanos":0},"inputHintsJSON":"{\"scenario\":\"L3-CONFLICT\",\"force_conflict\":true,\"run_query\":\"${RUN_QUERY}\",\"event_id\":\"${EVENT_ID:-unknown}\"}","createdBy":"system"}
EOF_RUN
)
call_or_fail "RunAIJob" POST "${BASE_URL}/v1/incidents/${INCIDENT_ID}/ai:run" "${run_body}"
JOB_ID="$(extract_field "${LAST_BODY}" "jobID" "job_id")" || true
if [[ -z "${JOB_ID}" ]]; then
	fail_step "RunAIJobParseJobID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

wait_for_ai_job_terminal

call_or_fail "ListToolCalls" GET "${BASE_URL}/v1/ai/jobs/${JOB_ID}/tool-calls?offset=0&limit=20"
if need_cmd jq; then
	tool_count="$(printf '%s' "${LAST_BODY}" | jq -r '(.toolCalls // .data.toolCalls // []) | length' 2>/dev/null || true)"
	if [[ -z "${tool_count}" ]] || (( tool_count < 2 )); then
		fail_step "ListToolCallsCount" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
	TOOL_CALL_ID_1="$(printf '%s' "${LAST_BODY}" | jq -r '(.toolCalls // .data.toolCalls // [])[0].toolCallID // empty' 2>/dev/null || true)"
	TOOL_CALL_ID_2="$(printf '%s' "${LAST_BODY}" | jq -r '(.toolCalls // .data.toolCalls // [])[1].toolCallID // empty' 2>/dev/null || true)"
else
	if ! printf '%s' "${LAST_BODY}" | grep -q '"seq":1'; then
		fail_step "ListToolCallsSeq1Missing" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
	if ! printf '%s' "${LAST_BODY}" | grep -q '"seq":2'; then
		fail_step "ListToolCallsSeq2Missing" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
fi

call_or_fail "GetIncident" GET "${BASE_URL}/v1/incidents/${INCIDENT_ID}"
validate_incident_writeback_l3 "${LAST_BODY}"

echo "PASS L3-CONFLICT + IDs"
echo "incident_id=${INCIDENT_ID}"
echo "job_id=${JOB_ID}"
echo "datasource_id=${DATASOURCE_ID:-NONE}"
echo "evidence_ids=${EVIDENCE_IDS:-NONE}"
echo "tool_call_id=${TOOL_CALL_ID_1:-NONE},${TOOL_CALL_ID_2:-NONE}"
