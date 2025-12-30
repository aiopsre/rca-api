#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
DEBUG="${DEBUG:-0}"
WAIT_TIMEOUT_SEC="${WAIT_TIMEOUT_SEC:-60}"
CHANNEL_LINKS_BASE_URL="${CHANNEL_LINKS_BASE_URL:-https://rca.example.test}"

LAST_HTTP_CODE=""
LAST_BODY=""

CHANNEL_ID=""
INCIDENT_ID=""
JOB_ID=""
DELIVERY_INCIDENT_ID=""
DELIVERY_DIAGNOSIS_ID=""

debug() {
	if [[ "${DEBUG}" == "1" ]]; then
		echo "[DEBUG] $*" >&2
	fi
}

trim_2kb() {
	printf '%s' "$1" | head -c 2048
}

fail_l11() {
	local step="$1"
	local detail="${2:-non-2xx response}"
	local code="${3:-${LAST_HTTP_CODE}}"
	local body="${4:-${LAST_BODY}}"

	echo "FAIL L11-NOTICE-LINKS-SUMMARY"
	echo "step=${step}"
	echo "detail=${detail}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	trim_2kb "${body}"
	echo
	echo "EOF"
	echo "channel_id=${CHANNEL_ID:-NONE}"
	echo "incident_id=${INCIDENT_ID:-NONE}"
	echo "job_id=${JOB_ID:-NONE}"
	echo "delivery_incident_id=${DELIVERY_INCIDENT_ID:-NONE}"
	echo "delivery_diagnosis_id=${DELIVERY_DIAGNOSIS_ID:-NONE}"
	exit 1
}

need_cmd() {
	command -v "$1" >/dev/null 2>&1
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
	return 0
}

call_or_fail() {
	local step="$1"
	local method="$2"
	local url="$3"
	local body="${4:-}"

	if ! http_json "${method}" "${url}" "${body}"; then
		fail_l11 "${step}" "curl failed"
	fi
	if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
		fail_l11 "${step}"
	fi
	debug "${step} code=${LAST_HTTP_CODE}"
}

extract_field() {
	local json="$1"
	shift
	local keys=("$@")
	local key value

	for key in "${keys[@]}"; do
		value="$(
			printf '%s' "${json}" | jq -r --arg k "${key}" '
				(.[$k] // .data[$k] // .noticeChannel[$k] // .data.noticeChannel[$k] //
				 .noticeDelivery[$k] // .data.noticeDelivery[$k] //
				 .job[$k] // .data.job[$k]) |
				if . == null then empty
				elif type == "string" then .
				else tostring
				end
			' 2>/dev/null
		)"
		if [[ -n "${value}" ]]; then
			printf '%s' "${value}"
			return 0
		fi
	done
	return 1
}

wait_delivery_count_or_fail() {
	local step="$1"
	local event_type="$2"
	local expect_count="$3"
	local deadline count
	deadline="$(( $(date +%s) + WAIT_TIMEOUT_SEC ))"

	while true; do
		call_or_fail "${step}List" GET "${BASE_URL}/v1/notice-deliveries?incident_id=${INCIDENT_ID}&event_type=${event_type}&offset=0&limit=50"
		count="$(printf '%s' "${LAST_BODY}" | jq -r '(.noticeDeliveries // .data.noticeDeliveries // []) | length' 2>/dev/null || true)"
		if [[ "${count}" =~ ^[0-9]+$ ]] && (( count >= expect_count )); then
			return 0
		fi
		if (( $(date +%s) > deadline )); then
			fail_l11 "${step}" "expect delivery count>=${expect_count} for event=${event_type}, got=${count:-EMPTY}"
		fi
		sleep 1
	done
}

extract_delivery_id_or_fail() {
	local step="$1"
	local event_type="$2"
	local out
	out="$(
		printf '%s' "${LAST_BODY}" | jq -r --arg ev "${event_type}" --arg cid "${CHANNEL_ID}" '
			(.noticeDeliveries // .data.noticeDeliveries // [])[]
			| select((.eventType // "") == $ev and (.channelID // "") == $cid)
			| .deliveryID
		' 2>/dev/null | head -n 1
	)"
	if [[ -z "${out}" ]]; then
		fail_l11 "${step}" "delivery_id missing for event=${event_type}, channel_id=${CHANNEL_ID}"
	fi
	printf '%s' "${out}"
}

json_string() {
	printf '%s' "$1" | jq -Rs .
}

start_ai_job_or_skip() {
	local step="$1"
	local job_id="$2"
	if http_json "POST" "${BASE_URL}/v1/ai/jobs/${job_id}/start" '{}'; then
		if [[ "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
			return 0
		fi
	fi
	if [[ "${LAST_HTTP_CODE}" != "409" ]]; then
		fail_l11 "${step}" "start job failed with unexpected code=${LAST_HTTP_CODE}"
	fi
	call_or_fail "${step}GetJobAfter409" "GET" "${BASE_URL}/v1/ai/jobs/${job_id}"
	local status
	status="$(extract_field "${LAST_BODY}" "status" || true)"
	case "${status}" in
		running | succeeded | failed | canceled) ;;
		*)
			fail_l11 "${step}" "status after 409 is unexpected: ${status:-EMPTY}"
			;;
	esac
}

assert_links_and_summary_or_fail() {
	local step="$1"
	local payload="$2"
	local expect_job_url="$3"

	local links_version links_base summary expected_summary
	local incident_url delivery_url channel_url evidence_list_url job_url

	links_version="$(printf '%s' "${payload}" | jq -r '.links.version // empty' 2>/dev/null || true)"
	links_base="$(printf '%s' "${payload}" | jq -r '.links.base_url // empty' 2>/dev/null || true)"
	incident_url="$(printf '%s' "${payload}" | jq -r '.links.incident_url // empty' 2>/dev/null || true)"
	delivery_url="$(printf '%s' "${payload}" | jq -r '.links.delivery_url // empty' 2>/dev/null || true)"
	channel_url="$(printf '%s' "${payload}" | jq -r '.links.channel_url // empty' 2>/dev/null || true)"
	evidence_list_url="$(printf '%s' "${payload}" | jq -r '.links.evidence_list_url // empty' 2>/dev/null || true)"
	job_url="$(printf '%s' "${payload}" | jq -r '.links.job_url // empty' 2>/dev/null || true)"
	summary="$(printf '%s' "${payload}" | jq -r '.summary // empty' 2>/dev/null || true)"
	expected_summary="$(
		printf '%s' "${payload}" | jq -r '
			"[" + (.incident.severity // "") + "] " +
			(.incident.service // "") + " " +
			(.event_type // "") + " incident=" +
			(.incident.incident_id // "")
		' 2>/dev/null || true
	)"

	if [[ "${links_version}" != "v1" ]]; then
		fail_l11 "${step}" "links.version expected v1" "ASSERT_FAIL" "${payload}"
	fi
	if [[ "${links_base}" != "${CHANNEL_LINKS_BASE_URL}" ]]; then
		fail_l11 "${step}" "links.base_url mismatch: got=${links_base:-EMPTY}" "ASSERT_FAIL" "${payload}"
	fi
	if [[ -z "${incident_url}" || "${incident_url}" != "${CHANNEL_LINKS_BASE_URL}"* ]]; then
		fail_l11 "${step}" "incident_url missing or not prefixed by base_url" "ASSERT_FAIL" "${payload}"
	fi
	if [[ -z "${delivery_url}" || "${delivery_url}" != "${CHANNEL_LINKS_BASE_URL}"* ]]; then
		fail_l11 "${step}" "delivery_url missing or not prefixed by base_url" "ASSERT_FAIL" "${payload}"
	fi
	if [[ -z "${channel_url}" || "${channel_url}" != "${CHANNEL_LINKS_BASE_URL}"* ]]; then
		fail_l11 "${step}" "channel_url missing or not prefixed by base_url" "ASSERT_FAIL" "${payload}"
	fi
	if [[ -z "${evidence_list_url}" || "${evidence_list_url}" != "${CHANNEL_LINKS_BASE_URL}"* ]]; then
		fail_l11 "${step}" "evidence_list_url missing or not prefixed by base_url" "ASSERT_FAIL" "${payload}"
	fi
	if [[ "${expect_job_url}" == "1" ]]; then
		if [[ -z "${job_url}" || "${job_url}" != "${CHANNEL_LINKS_BASE_URL}"* ]]; then
			fail_l11 "${step}" "job_url missing or not prefixed by base_url" "ASSERT_FAIL" "${payload}"
		fi
	fi
	if [[ "${summary}" != "${expected_summary}" ]]; then
		fail_l11 "${step}" "summary template replacement mismatch" "ASSERT_FAIL" "${payload}"
	fi
	if [[ "${summary}" == *'${'* ]]; then
		fail_l11 "${step}" "summary still contains unresolved template marker" "ASSERT_FAIL" "${payload}"
	fi
}

if ! need_cmd jq; then
	fail_l11 "PrecheckTools" "jq not found"
fi

rand="${RAND:-$RANDOM}"
now_epoch="$(date -u +%s)"
start_epoch="$((now_epoch - 1200))"
endpoint="http://127.0.0.1:19098/l11/${rand}"

create_channel_body=$(cat <<EOF
{"name":"p2-l11-links-${rand}","type":"webhook","enabled":true,"endpointURL":"${endpoint}","timeoutMs":1200,"maxRetries":0,"payloadMode":2,"includeDiagnosis":true,"includeEvidenceIds":true,"includeRootCause":true,"includeLinks":true,"baseURL":"${CHANNEL_LINKS_BASE_URL}","summaryTemplate":"[\${severity}] \${service} \${event_type} incident=\${incident_id}"}
EOF
)
call_or_fail "CreateChannel" POST "${BASE_URL}/v1/notice-channels" "${create_channel_body}"
CHANNEL_ID="$(extract_field "${LAST_BODY}" "channelID" "channel_id" || true)"
if [[ -z "${CHANNEL_ID}" ]]; then
	fail_l11 "CreateChannelParse" "channel_id missing"
fi

ingest_body=$(cat <<EOF
{"idempotencyKey":"idem-p2-l11-ingest-${rand}","fingerprint":"p2-l11-fp-${rand}","status":"firing","severity":"P1","service":"notice-l11","cluster":"prod-l11","namespace":"default","workload":"checkout-api","lastSeenAt":{"seconds":${now_epoch},"nanos":0}}
EOF
)
call_or_fail "IngestAlertEvent" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_body}"
INCIDENT_ID="$(extract_field "${LAST_BODY}" "incidentID" "incident_id" || true)"
if [[ -z "${INCIDENT_ID}" ]]; then
	fail_l11 "IngestAlertEventParse" "incident_id missing"
fi

wait_delivery_count_or_fail "WaitIncidentCreatedDeliveries" "incident_created" 1
DELIVERY_INCIDENT_ID="$(extract_delivery_id_or_fail "ExtractIncidentDelivery" "incident_created")"

call_or_fail "GetIncidentDelivery" GET "${BASE_URL}/v1/notice-deliveries/${DELIVERY_INCIDENT_ID}"
incident_request_body="$(printf '%s' "${LAST_BODY}" | jq -r '(.noticeDelivery // .data.noticeDelivery // {}) | (.requestBody // "")' 2>/dev/null || true)"
if [[ -z "${incident_request_body}" ]]; then
	fail_l11 "GetIncidentDeliveryParse" "incident requestBody missing"
fi
assert_links_and_summary_or_fail "AssertIncidentLinksSummary" "${incident_request_body}" "0"

run_body=$(cat <<EOF
{"incidentID":"${INCIDENT_ID}","idempotencyKey":"idem-p2-l11-run-${rand}","pipeline":"basic_rca","trigger":"manual","timeRangeStart":{"seconds":${start_epoch},"nanos":0},"timeRangeEnd":{"seconds":${now_epoch},"nanos":0}}
EOF
)
call_or_fail "RunAIJob" POST "${BASE_URL}/v1/incidents/${INCIDENT_ID}/ai:run" "${run_body}"
JOB_ID="$(extract_field "${LAST_BODY}" "jobID" "job_id" || true)"
if [[ -z "${JOB_ID}" ]]; then
	fail_l11 "RunAIJobParse" "job_id missing"
fi
start_ai_job_or_skip "StartAIJob" "${JOB_ID}"

diagnosis_json='{"summary":"db pool exhausted","root_cause":{"type":"database","category":"db","summary":"db pool exhausted","confidence":0.92,"evidence_ids":["evidence-1","evidence-2"]},"missing_evidence":[],"timeline":[],"hypotheses":[],"recommendations":[],"unknowns":[],"next_steps":["check connection pool"]}'
finalize_body=$(cat <<EOF
{"jobID":"${JOB_ID}","status":"succeeded","diagnosisJSON":$(json_string "${diagnosis_json}")}
EOF
)
call_or_fail "FinalizeAIJob" POST "${BASE_URL}/v1/ai/jobs/${JOB_ID}/finalize" "${finalize_body}"

wait_delivery_count_or_fail "WaitDiagnosisWrittenDeliveries" "diagnosis_written" 1
DELIVERY_DIAGNOSIS_ID="$(extract_delivery_id_or_fail "ExtractDiagnosisDelivery" "diagnosis_written")"

call_or_fail "GetDiagnosisDelivery" GET "${BASE_URL}/v1/notice-deliveries/${DELIVERY_DIAGNOSIS_ID}"
diagnosis_request_body="$(printf '%s' "${LAST_BODY}" | jq -r '(.noticeDelivery // .data.noticeDelivery // {}) | (.requestBody // "")' 2>/dev/null || true)"
if [[ -z "${diagnosis_request_body}" ]]; then
	fail_l11 "GetDiagnosisDeliveryParse" "diagnosis requestBody missing"
fi
assert_links_and_summary_or_fail "AssertDiagnosisLinksSummary" "${diagnosis_request_body}" "1"

echo "PASS L11-NOTICE-LINKS-SUMMARY"
echo "channel_id=${CHANNEL_ID}"
echo "incident_id=${INCIDENT_ID}"
echo "job_id=${JOB_ID}"
echo "delivery_incident_id=${DELIVERY_INCIDENT_ID}"
echo "delivery_diagnosis_id=${DELIVERY_DIAGNOSIS_ID}"
