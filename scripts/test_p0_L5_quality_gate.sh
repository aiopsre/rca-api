#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
RUN_QUERY="${RUN_QUERY:-0}"
FORCE_NO_EVIDENCE="${FORCE_NO_EVIDENCE:-0}"
FORCE_CONFLICT="${FORCE_CONFLICT:-0}"
DEBUG="${DEBUG:-0}"
RUN_PASS_CASE="${RUN_PASS_CASE:-0}"
JOB_WAIT_TIMEOUT_SEC="${JOB_WAIT_TIMEOUT_SEC:-120}"
JOB_POLL_INTERVAL_SEC="${JOB_POLL_INTERVAL_SEC:-1}"

LAST_HTTP_CODE=""
LAST_BODY=""

CURRENT_CASE=""
CURRENT_INCIDENT_ID=""
CURRENT_JOB_ID=""
CURRENT_EVENT_ID=""
CURRENT_EVIDENCE_IDS=""
CURRENT_TOOL_CALL_IDS=""

CASE_SUMMARY=""

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

fail_step() {
	local step="$1"
	local code="${2:-${LAST_HTTP_CODE}}"
	local body="${3:-${LAST_BODY}}"

	echo "FAIL L5 step=${step}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	truncate_2kb "${body}"
	echo
	echo "EOF"
	echo "case=${CURRENT_CASE:-NONE}"
	echo "incident_id=${CURRENT_INCIDENT_ID:-NONE}"
	echo "job_id=${CURRENT_JOB_ID:-NONE}"
	echo "event_id=${CURRENT_EVENT_ID:-NONE}"
	echo "evidence_ids=${CURRENT_EVIDENCE_IDS:-NONE}"
	echo "tool_call_ids=${CURRENT_TOOL_CALL_IDS:-NONE}"
	if [[ -n "${CASE_SUMMARY}" ]]; then
		echo "case_ids<<EOF"
		printf '%s' "${CASE_SUMMARY}"
		echo "EOF"
	fi
	exit 1
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
					 .event[$k] // .data.event[$k]) |
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
	local scenario="$1"
	local deadline status now
	deadline="$(( $(date +%s) + JOB_WAIT_TIMEOUT_SEC ))"

	while true; do
		call_or_fail "${scenario}.PollAIJob" GET "${BASE_URL}/v1/ai/jobs/${CURRENT_JOB_ID}"
		status="$(extract_field "${LAST_BODY}" "status")" || true
		if [[ -z "${status}" ]]; then
			fail_step "${scenario}.PollAIJobStatusParse" "${LAST_HTTP_CODE}" "${LAST_BODY}"
		fi

		case "${status}" in
			queued|running)
				now="$(date +%s)"
				if (( now > deadline )); then
					fail_step "${scenario}.PollAIJobTimeout" "TIMEOUT" "${LAST_BODY}"
				fi
				sleep "${JOB_POLL_INTERVAL_SEC}"
				;;
			succeeded)
				return 0
				;;
			failed|canceled)
				fail_step "${scenario}.PollAIJobTerminal=${status}" "${LAST_HTTP_CODE}" "${LAST_BODY}"
				;;
			*)
				now="$(date +%s)"
				if (( now > deadline )); then
					fail_step "${scenario}.PollAIJobUnknownStatus=${status}" "TIMEOUT" "${LAST_BODY}"
				fi
				sleep "${JOB_POLL_INTERVAL_SEC}"
				;;
		esac
	done
}

assert_toolcalls_quality_gate() {
	local scenario="$1"
	local expected_decision="$2"
	local body="$3"

	if need_cmd jq; then
		local tool_count gate_count
		tool_count="$(printf '%s' "${body}" | jq -r '(.toolCalls // .data.toolCalls // []) | length' 2>/dev/null || true)"
		if [[ -z "${tool_count}" ]] || (( tool_count < 2 )); then
			fail_step "${scenario}.ListToolCallsCount" "${LAST_HTTP_CODE}" "${body}"
		fi

		gate_count="$(printf '%s' "${body}" | jq -r --arg decision "${expected_decision}" '
			(.toolCalls // .data.toolCalls // [])
			| map(
				((.responseJSON // .response_json // "") as $raw |
					if ($raw|type) == "string" and ($raw|length) > 0
					then (try ($raw|fromjson) catch {})
					else {}
					end
				)
				| .quality_gate as $gate
				| select(($gate.decision // "") == $decision)
				| select((($gate.reasons // []) | type) == "array" and (($gate.reasons // []) | length) >= 1)
				| select((($gate.evidence_summary // {}) | type) == "object")
			)
			| length
		' 2>/dev/null || true)"
		if [[ -z "${gate_count}" ]] || (( gate_count < 1 )); then
			fail_step "${scenario}.ToolCallsQualityGateMissing" "${LAST_HTTP_CODE}" "${body}"
		fi

		CURRENT_TOOL_CALL_IDS="$(printf '%s' "${body}" | jq -r '(.toolCalls // .data.toolCalls // []) | map(.toolCallID // .tool_call_id // empty) | join(",")' 2>/dev/null || true)"
	else
		if ! printf '%s' "${body}" | grep -Eq 'quality_gate'; then
			fail_step "${scenario}.ToolCallsQualityGateMissing" "${LAST_HTTP_CODE}" "${body}"
		fi
		if ! printf '%s' "${body}" | grep -Eq "\"decision\"[[:space:]]*:[[:space:]]*\"${expected_decision}\""; then
			fail_step "${scenario}.ToolCallsQualityGateDecision" "${LAST_HTTP_CODE}" "${body}"
		fi
	fi

	if [[ -z "${CURRENT_TOOL_CALL_IDS}" ]]; then
		CURRENT_TOOL_CALL_IDS="NONE"
	fi
}

validate_incident_diagnosis() {
	local scenario="$1"
	local expected_type="$2"
	local body="$3"

	local diagnosis_raw=""
	if need_cmd jq; then
		diagnosis_raw="$(extract_field "${body}" "diagnosisJSON" "diagnosis_json")" || true
		if [[ -z "${diagnosis_raw}" ]]; then
			fail_step "${scenario}.DiagnosisMissing" "${LAST_HTTP_CODE}" "${body}"
		fi

		local root_type confidence
		root_type="$(printf '%s' "${diagnosis_raw}" | jq -r '.root_cause.type // empty' 2>/dev/null || true)"
		confidence="$(printf '%s' "${diagnosis_raw}" | jq -r '.root_cause.confidence // empty' 2>/dev/null || true)"
		if [[ -z "${confidence}" ]]; then
			fail_step "${scenario}.DiagnosisConfidenceMissing" "${LAST_HTTP_CODE}" "${body}"
		fi

		if [[ "${expected_type}" == "pass" ]]; then
			if awk "BEGIN {exit !(${confidence} > 0.60)}"; then
				if [[ "${root_type}" == "missing_evidence" || "${root_type}" == "conflict_evidence" ]]; then
					fail_step "${scenario}.DiagnosisHighConfidenceTypeInvalid" "${LAST_HTTP_CODE}" "${body}"
				fi
			fi
		else
			if [[ "${root_type}" != "${expected_type}" ]]; then
				fail_step "${scenario}.DiagnosisTypeInvalid" "${LAST_HTTP_CODE}" "${body}"
			fi
			if ! awk "BEGIN {exit !(${confidence} <= 0.30)}"; then
				fail_step "${scenario}.DiagnosisConfidenceTooHigh" "${LAST_HTTP_CODE}" "${body}"
			fi
			local missing_count
			missing_count="$(printf '%s' "${diagnosis_raw}" | jq -r '[.missing_evidence[]? | select(type=="string" and (.|gsub("^\\s+|\\s+$";"")|length>0))] | length' 2>/dev/null || true)"
			if [[ -z "${missing_count}" ]] || (( missing_count < 1 )); then
				fail_step "${scenario}.DiagnosisMissingEvidenceEmpty" "${LAST_HTTP_CODE}" "${body}"
			fi
			if (( missing_count > 20 )); then
				fail_step "${scenario}.DiagnosisMissingEvidenceTooMany" "${LAST_HTTP_CODE}" "${body}"
			fi
		fi

		CURRENT_EVIDENCE_IDS="$(printf '%s' "${diagnosis_raw}" | jq -r '[.root_cause.evidence_ids[]? | select(type=="string" and (.|gsub("^\\s+|\\s+$";"")|length>0))] | join(",")' 2>/dev/null || true)"
	else
		if ! printf '%s' "${body}" | grep -Eq '"diagnosisJSON"|"diagnosis_json"'; then
			fail_step "${scenario}.DiagnosisMissing" "${LAST_HTTP_CODE}" "${body}"
		fi
		if [[ "${expected_type}" != "pass" ]]; then
			if ! printf '%s' "${body}" | grep -Eq "\"type\"[[:space:]]*:[[:space:]]*\"${expected_type}\""; then
				fail_step "${scenario}.DiagnosisTypeInvalid" "${LAST_HTTP_CODE}" "${body}"
			fi
			if ! printf '%s' "${body}" | grep -q 'missing_evidence'; then
				fail_step "${scenario}.DiagnosisMissingEvidenceEmpty" "${LAST_HTTP_CODE}" "${body}"
			fi
		fi
	fi

	if [[ -z "${CURRENT_EVIDENCE_IDS}" ]]; then
		CURRENT_EVIDENCE_IDS="NONE"
	fi
}

record_case_summary() {
	local scenario="$1"
	CASE_SUMMARY+="${scenario}:incident_id=${CURRENT_INCIDENT_ID:-NONE},job_id=${CURRENT_JOB_ID:-NONE},event_id=${CURRENT_EVENT_ID:-NONE},evidence_ids=${CURRENT_EVIDENCE_IDS:-NONE},tool_call_ids=${CURRENT_TOOL_CALL_IDS:-NONE}"$'\n'
}

run_case() {
	local scenario="$1"
	local force_no="$2"
	local force_conflict="$3"
	local expected_decision="$4"
	local expected_type="$5"

	CURRENT_CASE="${scenario}"
	CURRENT_INCIDENT_ID=""
	CURRENT_JOB_ID=""
	CURRENT_EVENT_ID=""
	CURRENT_EVIDENCE_IDS=""
	CURRENT_TOOL_CALL_IDS=""

	local rand now_epoch start_epoch fingerprint ingest_body run_body
	rand="${RANDOM}"
	now_epoch="$(date -u +%s)"
	start_epoch="$((now_epoch - 1800))"
	fingerprint="p0-l5-${scenario}-fp-${rand}"

	ingest_body=$(cat <<EOF_INGEST
{"idempotencyKey":"idem-p0-l5-${scenario}-ingest-${rand}","fingerprint":"${fingerprint}","status":"firing","severity":"P1","service":"p0-l5-${scenario}-svc","cluster":"prod-l5","namespace":"default","workload":"p0-l5-${scenario}-workload","lastSeenAt":{"seconds":${now_epoch},"nanos":0},"labelsJSON":"{\"alertname\":\"L5QualityGate\",\"service\":\"p0-l5-${scenario}-svc\"}"}
EOF_INGEST
)

	call_or_fail "${scenario}.IngestAlertEvent" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_body}"
	CURRENT_INCIDENT_ID="$(extract_field "${LAST_BODY}" "incidentID" "incident_id")" || true
	CURRENT_EVENT_ID="$(extract_field "${LAST_BODY}" "eventID" "event_id")" || true
	if [[ -z "${CURRENT_INCIDENT_ID}" ]]; then
		fail_step "${scenario}.IngestParseIncidentID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi

	run_body=$(cat <<EOF_RUN
{"incidentID":"${CURRENT_INCIDENT_ID}","idempotencyKey":"idem-p0-l5-${scenario}-ai-run-${rand}","pipeline":"basic_rca","trigger":"manual","timeRangeStart":{"seconds":${start_epoch},"nanos":0},"timeRangeEnd":{"seconds":${now_epoch},"nanos":0},"inputHintsJSON":"{\"scenario\":\"L5-${scenario}\",\"FORCE_NO_EVIDENCE\":${force_no},\"FORCE_CONFLICT\":${force_conflict},\"run_query\":\"${RUN_QUERY}\"}","createdBy":"system"}
EOF_RUN
)

	call_or_fail "${scenario}.RunAIJob" POST "${BASE_URL}/v1/incidents/${CURRENT_INCIDENT_ID}/ai:run" "${run_body}"
	CURRENT_JOB_ID="$(extract_field "${LAST_BODY}" "jobID" "job_id")" || true
	if [[ -z "${CURRENT_JOB_ID}" ]]; then
		fail_step "${scenario}.RunParseJobID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi

	wait_for_ai_job_terminal "${scenario}"

	call_or_fail "${scenario}.ListToolCalls" GET "${BASE_URL}/v1/ai/jobs/${CURRENT_JOB_ID}/tool-calls?offset=0&limit=20"
	assert_toolcalls_quality_gate "${scenario}" "${expected_decision}" "${LAST_BODY}"

	call_or_fail "${scenario}.GetIncident" GET "${BASE_URL}/v1/incidents/${CURRENT_INCIDENT_ID}"
	validate_incident_diagnosis "${scenario}" "${expected_type}" "${LAST_BODY}"

	record_case_summary "${scenario}"
}

# S1: missing (FORCE_NO_EVIDENCE=1)
run_case "missing" 1 0 "missing" "missing_evidence"

# S2: conflict (FORCE_CONFLICT=1 and FORCE_NO_EVIDENCE=1 to verify conflict priority)
run_case "conflict" 1 1 "conflict" "conflict_evidence"

# S3: pass (optional)
if [[ "${RUN_PASS_CASE}" == "1" ]]; then
	run_case "pass" 0 0 "pass" "pass"
fi

echo "PASS L5-GATE + IDs"
printf '%s' "${CASE_SUMMARY}"
