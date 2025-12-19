#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
RUN_QUERY="${RUN_QUERY:-0}"
DS_BASE_URL="${DS_BASE_URL:-}"
AUTO_CREATE_DATASOURCE="${AUTO_CREATE_DATASOURCE:-1}"
DEBUG="${DEBUG:-0}"

INCIDENT_ID=""
DATASOURCE_ID="${DATASOURCE_ID:-}"
EVIDENCE_ID=""
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

json_quote() {
	local raw="$1"
	if need_cmd jq; then
		printf '%s' "${raw}" | jq -Rs .
	else
		printf '"%s"' "$(printf '%s' "${raw}" | sed 's/\\/\\\\/g; s/"/\\"/g; s/\r/\\r/g; s/\n/\\n/g')"
	fi
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
					(.[$k] // .data[$k] // .incident[$k] // .data.incident[$k]) |
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

	echo "FAIL L1 step=${step}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	truncate_2kb "${body}"
	echo
	echo "EOF"
	echo "incident_id=${INCIDENT_ID:-NONE}"
	echo "job_id=${JOB_ID:-NONE}"
	echo "datasource_id=${DATASOURCE_ID:-NONE}"
	echo "evidence_id=${EVIDENCE_ID:-NONE}"
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

create_datasource() {
	local rand="$1"
	local base_url="$2"
	local body
	body=$(cat <<EOF
{"type":"prometheus","name":"p0-l1-ds-${rand}","baseURL":"${base_url}","authType":"none","timeoutMs":5000,"isEnabled":true}
EOF
)
	call_or_fail "CreateDatasource" POST "${BASE_URL}/v1/datasources" "${body}"
	DATASOURCE_ID="$(extract_field "${LAST_BODY}" "datasourceID" "datasource_id")" || true
	if [[ -z "${DATASOURCE_ID}" ]]; then
		fail_step "CreateDatasource" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
	export DATASOURCE_ID
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
	else
		if ! printf '%s' "${body}" | grep -Eq '"diagnosisJSON"|"diagnosis_json"'; then
			fail_step "GetIncidentValidate" "${LAST_HTTP_CODE}" "${body}"
		fi
		if ! printf '%s' "${body}" | grep -Eq '"evidenceRefsJSON"|"evidence_refs_json"'; then
			fail_step "GetIncidentValidate" "${LAST_HTTP_CODE}" "${body}"
		fi
		if ! printf '%s' "${body}" | grep -Eq '"rcaStatus"|"rca_status"'; then
			fail_step "GetIncidentValidate" "${LAST_HTTP_CODE}" "${body}"
		fi
		if ! printf '%s' "${body}" | grep -Eq '"rootCauseSummary"|"root_cause_summary"'; then
			fail_step "GetIncidentValidate" "${LAST_HTTP_CODE}" "${body}"
		fi
		if ! printf '%s' "${body}" | grep -q 'root_cause'; then
			fail_step "GetIncidentDiagnosisValidation" "${LAST_HTTP_CODE}" "${body}"
		fi
		if ! printf '%s' "${body}" | grep -q 'confidence'; then
			fail_step "GetIncidentDiagnosisValidation" "${LAST_HTTP_CODE}" "${body}"
		fi
		if ! printf '%s' "${body}" | grep -q 'evidence_ids'; then
			fail_step "GetIncidentDiagnosisValidation" "${LAST_HTTP_CODE}" "${body}"
		fi
		if ! printf '%s' "${body}" | grep -q 'evidence_ids'; then
			fail_step "GetIncidentEvidenceRefsValidation" "${LAST_HTTP_CODE}" "${body}"
		fi
	fi
}

rand="${RAND:-$RANDOM}"
now_epoch="$(date -u +%s)"
start_epoch="$((now_epoch - 1800))"
fingerprint="p0-l1-fp-${rand}"

ingest_body=$(cat <<EOF
{"idempotencyKey":"idem-p0-l1-ingest-${rand}","fingerprint":"${fingerprint}","status":"firing","severity":"P1","service":"p0-l1-svc","cluster":"prod-l1","namespace":"default","workload":"p0-l1-workload","lastSeenAt":{"seconds":${now_epoch},"nanos":0},"labelsJSON":"{\"alertname\":\"HTTP5xxHigh\",\"service\":\"p0-l1-svc\"}"}
EOF
)

call_or_fail "IngestAlertEvent" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_body}"
INCIDENT_ID="$(extract_field "${LAST_BODY}" "incidentID" "incident_id")" || true
EVENT_ID="$(extract_field "${LAST_BODY}" "eventID" "event_id")" || true
if [[ -z "${INCIDENT_ID}" ]]; then
	fail_step "IngestAlertEventParseIncidentID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

if [[ "${RUN_QUERY}" == "1" ]]; then
	if [[ -z "${DATASOURCE_ID}" ]] && [[ "${AUTO_CREATE_DATASOURCE}" == "1" ]]; then
		if [[ -z "${DS_BASE_URL}" ]]; then
			fail_step "ConfigDSBaseURLRequired" "CONFIG" "RUN_QUERY=1 requires DS_BASE_URL when AUTO_CREATE_DATASOURCE=1 and DATASOURCE_ID is empty"
		fi
		create_datasource "${rand}" "${DS_BASE_URL}"
	fi
	if [[ -z "${DATASOURCE_ID}" ]]; then
		fail_step "DatasourceMissingForQuery" "CONFIG" "RUN_QUERY=1 requires DATASOURCE_ID or AUTO_CREATE_DATASOURCE=1 with DS_BASE_URL"
	fi
	export DATASOURCE_ID
fi

query_result='{"status":"synthetic","reason":"RUN_QUERY=0"}'
if [[ "${RUN_QUERY}" == "1" ]]; then
	query_body=$(cat <<EOF
{"datasourceID":"${DATASOURCE_ID}","promql":"up","timeRangeStart":{"seconds":${start_epoch},"nanos":0},"timeRangeEnd":{"seconds":${now_epoch},"nanos":0},"stepSeconds":30}
EOF
)
	call_or_fail "QueryMetrics" POST "${BASE_URL}/v1/evidence:queryMetrics" "${query_body}"
	query_result="$(extract_field "${LAST_BODY}" "queryResultJSON" "query_result_json")" || true
	if [[ -z "${query_result}" ]]; then
		fail_step "QueryMetricsParseResult" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
fi

datasource_field=""
if [[ -n "${DATASOURCE_ID}" ]]; then
	datasource_field=",\"datasourceID\":\"${DATASOURCE_ID}\""
fi

save_body=$(cat <<EOF
{"incidentID":"${INCIDENT_ID}","idempotencyKey":"idem-p0-l1-evidence-${rand}","type":"metrics"${datasource_field},"queryText":"sum(rate(http_requests_total[5m]))","queryJSON":"{}","timeRangeStart":{"seconds":${start_epoch},"nanos":0},"timeRangeEnd":{"seconds":${now_epoch},"nanos":0},"resultJSON":$(json_quote "${query_result}"),"summary":"p0 l1 synthetic evidence","createdBy":"system"}
EOF
)

call_or_fail "SaveEvidence" POST "${BASE_URL}/v1/incidents/${INCIDENT_ID}/evidence" "${save_body}"
EVIDENCE_ID="$(extract_field "${LAST_BODY}" "evidenceID" "evidence_id")" || true
if [[ -z "${EVIDENCE_ID}" ]]; then
	fail_step "SaveEvidenceParseEvidenceID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

call_or_fail "ListIncidentEvidence" GET "${BASE_URL}/v1/incidents/${INCIDENT_ID}/evidence?offset=0&limit=20"
if ! printf '%s' "${LAST_BODY}" | grep -q "\"evidenceID\":\"${EVIDENCE_ID}\""; then
	fail_step "ListIncidentEvidenceValidate" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

run_body=$(cat <<EOF
{"incidentID":"${INCIDENT_ID}","idempotencyKey":"idem-p0-l1-ai-run-${rand}","pipeline":"basic_rca","trigger":"manual","timeRangeStart":{"seconds":${start_epoch},"nanos":0},"timeRangeEnd":{"seconds":${now_epoch},"nanos":0},"inputHintsJSON":"{\"scenario\":\"L1\"}","createdBy":"system"}
EOF
)
call_or_fail "RunAIJob" POST "${BASE_URL}/v1/incidents/${INCIDENT_ID}/ai:run" "${run_body}"
JOB_ID="$(extract_field "${LAST_BODY}" "jobID" "job_id")" || true
if [[ -z "${JOB_ID}" ]]; then
	fail_step "RunAIJobParseJobID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

call_or_fail "StartAIJob" POST "${BASE_URL}/v1/ai/jobs/${JOB_ID}/start"

tool_call_1_body=$(cat <<EOF
{"jobID":"${JOB_ID}","seq":1,"nodeName":"metrics_specialist","toolName":"evidence.queryMetrics","requestJSON":$(json_quote "{\"incident_id\":\"${INCIDENT_ID}\",\"query\":\"up\"}"),"responseJSON":$(json_quote "{\"status\":\"ok\",\"rows\":12}"),"status":"ok","latencyMs":18,"errorMessage":"","evidenceIDs":["${EVIDENCE_ID}"]}
EOF
)
call_or_fail "CreateToolCallSeq1" POST "${BASE_URL}/v1/ai/jobs/${JOB_ID}/tool-calls" "${tool_call_1_body}"
TOOL_CALL_ID_1="$(extract_field "${LAST_BODY}" "toolCallID" "tool_call_id")" || true
if [[ -z "${TOOL_CALL_ID_1}" ]]; then
	fail_step "CreateToolCallSeq1ParseID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

tool_call_2_body=$(cat <<EOF
{"jobID":"${JOB_ID}","seq":2,"nodeName":"logs_specialist","toolName":"evidence.queryLogs","requestJSON":$(json_quote "{\"incident_id\":\"${INCIDENT_ID}\",\"query\":\"error|exception\"}"),"responseJSON":$(json_quote "{\"status\":\"error\",\"reason\":\"mock timeout\"}"),"status":"error","latencyMs":31,"errorMessage":"mock timeout","evidenceIDs":["${EVIDENCE_ID}"]}
EOF
)
call_or_fail "CreateToolCallSeq2" POST "${BASE_URL}/v1/ai/jobs/${JOB_ID}/tool-calls" "${tool_call_2_body}"
TOOL_CALL_ID_2="$(extract_field "${LAST_BODY}" "toolCallID" "tool_call_id")" || true
if [[ -z "${TOOL_CALL_ID_2}" ]]; then
	fail_step "CreateToolCallSeq2ParseID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

call_or_fail "ListToolCalls" GET "${BASE_URL}/v1/ai/jobs/${JOB_ID}/tool-calls?offset=0&limit=20"
if need_cmd jq; then
	tool_count="$(printf '%s' "${LAST_BODY}" | jq -r '(.toolCalls // .data.toolCalls // []) | length' 2>/dev/null || true)"
	if [[ "${tool_count}" != "2" ]]; then
		fail_step "ListToolCallsCount" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
else
	if ! printf '%s' "${LAST_BODY}" | grep -q '"seq":1'; then
		fail_step "ListToolCallsSeq1Missing" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
	if ! printf '%s' "${LAST_BODY}" | grep -q '"seq":2'; then
		fail_step "ListToolCallsSeq2Missing" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
fi

diagnosis_json=$(cat <<EOF
{"summary":"P0 L1 synthetic RCA summary","root_cause":{"category":"app","statement":"http 5xx ratio elevated in service","confidence":0.4,"evidence_ids":["${EVIDENCE_ID}"]},"timeline":[{"t":"2026-02-07T00:00:00Z","event":"alert_fired","ref":"${EVENT_ID:-alert-event}"}],"hypotheses":[{"statement":"application regression drives 5xx increase","confidence":0.4,"supporting_evidence_ids":["${EVIDENCE_ID}"],"missing_evidence":["need request trace sample"]}],"recommendations":[{"type":"readonly_check","action":"check recent deployment and error logs","risk":"low"}],"unknowns":["upstream dependency status"],"next_steps":["collect trace sample for top failing endpoint"]}
EOF
)

finalize_body=$(cat <<EOF
{"jobID":"${JOB_ID}","status":"succeeded","outputSummary":"P0 L1 synthetic RCA summary","diagnosisJSON":$(json_quote "${diagnosis_json}"),"evidenceIDs":["${EVIDENCE_ID}"]}
EOF
)
call_or_fail "FinalizeAIJob" POST "${BASE_URL}/v1/ai/jobs/${JOB_ID}/finalize" "${finalize_body}"

call_or_fail "GetIncident" GET "${BASE_URL}/v1/incidents/${INCIDENT_ID}"
validate_incident_writeback "${LAST_BODY}"

echo "PASS L1"
echo "incident_id=${INCIDENT_ID}"
echo "datasource_id=${DATASOURCE_ID:-NONE}"
echo "evidence_id=${EVIDENCE_ID}"
echo "job_id=${JOB_ID}"
echo "tool_calls=2"
