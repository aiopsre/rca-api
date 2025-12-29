#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
DEBUG="${DEBUG:-0}"
MOCK_PORT="${MOCK_PORT:-19098}"
WAIT_TIMEOUT_SEC="${WAIT_TIMEOUT_SEC:-60}"

LAST_HTTP_CODE=""
LAST_BODY=""

CHANNEL_COMPACT_ID=""
CHANNEL_FULL_ID=""
INCIDENT_ID=""
JOB_ID=""
DELIVERY_INCIDENT_COMPACT_ID=""
DELIVERY_INCIDENT_FULL_ID=""
DELIVERY_DIAG_COMPACT_ID=""
DELIVERY_DIAG_FULL_ID=""

MOCK_EVENTS_FILE="$(mktemp)"
MOCK_LOG_FILE="$(mktemp)"
MOCK_PID=""
PYBIN=""

debug() {
	if [[ "${DEBUG}" == "1" ]]; then
		echo "[DEBUG] $*" >&2
	fi
}

need_cmd() {
	command -v "$1" >/dev/null 2>&1
}

trim_2kb() {
	printf '%s' "$1" | head -c 2048
}

fail_l10() {
	local step="$1"
	local detail="${2:-non-2xx response}"
	local code="${3:-${LAST_HTTP_CODE}}"
	local body="${4:-${LAST_BODY}}"

	echo "FAIL L10-NOTICE-TEMPLATE step=${step}"
	echo "detail=${detail}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	trim_2kb "${body}"
	echo
	echo "EOF"
	echo "channel_compact_id=${CHANNEL_COMPACT_ID:-NONE}"
	echo "channel_full_id=${CHANNEL_FULL_ID:-NONE}"
	echo "incident_id=${INCIDENT_ID:-NONE}"
	echo "job_id=${JOB_ID:-NONE}"
	echo "delivery_incident_compact_id=${DELIVERY_INCIDENT_COMPACT_ID:-NONE}"
	echo "delivery_incident_full_id=${DELIVERY_INCIDENT_FULL_ID:-NONE}"
	echo "delivery_diag_compact_id=${DELIVERY_DIAG_COMPACT_ID:-NONE}"
	echo "delivery_diag_full_id=${DELIVERY_DIAG_FULL_ID:-NONE}"
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
	return 0
}

call_or_fail() {
	local step="$1"
	local method="$2"
	local url="$3"
	local body="${4:-}"

	if ! http_json "${method}" "${url}" "${body}"; then
		fail_l10 "${step}" "curl failed"
	fi
	if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
		fail_l10 "${step}"
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

json_string() {
	printf '%s' "$1" | jq -Rs .
}

cleanup() {
	if [[ -n "${MOCK_PID}" ]]; then
		kill "${MOCK_PID}" >/dev/null 2>&1 || true
		wait "${MOCK_PID}" >/dev/null 2>&1 || true
	fi
	rm -f "${MOCK_EVENTS_FILE}" "${MOCK_LOG_FILE}"
}
trap cleanup EXIT

resolve_python() {
	if need_cmd python3; then
		PYBIN="python3"
		return
	fi
	if need_cmd python; then
		PYBIN="python"
		return
	fi
	fail_l10 "PrecheckTools" "python3/python not found"
}

start_mock() {
	"${PYBIN}" -u - <<'PY' "${MOCK_PORT}" "${MOCK_EVENTS_FILE}" >"${MOCK_LOG_FILE}" 2>&1 &
import json
import sys
from http.server import ThreadingHTTPServer, BaseHTTPRequestHandler

port = int(sys.argv[1])
events_path = sys.argv[2]

class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/healthz":
            self.send_response(200)
            self.end_headers()
            self.wfile.write(b"ok")
            return
        self.send_response(404)
        self.end_headers()

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length)
        try:
            payload = json.loads(body.decode("utf-8"))
        except Exception:
            payload = {"raw": body.decode("utf-8", errors="replace")}
        item = {
            "path": self.path,
            "event_type": payload.get("event_type", ""),
        }
        with open(events_path, "a", encoding="utf-8") as fp:
            fp.write(json.dumps(item, ensure_ascii=False) + "\n")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{"ok":true}')

    def log_message(self, fmt, *args):
        return

server = ThreadingHTTPServer(("127.0.0.1", port), Handler)
server.serve_forever()
PY
	MOCK_PID="$!"

	local deadline now
	deadline="$(( $(date +%s) + 10 ))"
	while true; do
		if "${CURL}" -sS "http://127.0.0.1:${MOCK_PORT}/healthz" >/dev/null 2>&1; then
			return 0
		fi
		now="$(date +%s)"
		if (( now > deadline )); then
			LAST_HTTP_CODE="MOCK_START_TIMEOUT"
			LAST_BODY="$(cat "${MOCK_LOG_FILE}" 2>/dev/null || true)"
			fail_l10 "StartMock" "mock webhook start timeout"
		fi
		sleep 0.5
	done
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
			fail_l10 "${step}" "expect delivery count>=${expect_count} for event=${event_type}, got=${count:-EMPTY}"
		fi
		sleep 1
	done
}

extract_delivery_id_or_fail() {
	local step="$1"
	local event_type="$2"
	local channel_id="$3"
	local out
	out="$(
		printf '%s' "${LAST_BODY}" | jq -r --arg ev "${event_type}" --arg cid "${channel_id}" '
			(.noticeDeliveries // .data.noticeDeliveries // [])[]
			| select((.eventType // "") == $ev and (.channelID // "") == $cid)
			| .deliveryID
		' 2>/dev/null | head -n 1
	)"
	if [[ -z "${out}" ]]; then
		fail_l10 "${step}" "delivery_id missing for event=${event_type}, channel_id=${channel_id}"
	fi
	printf '%s' "${out}"
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
		fail_l10 "${step}" "start job failed with unexpected code=${LAST_HTTP_CODE}"
	fi
	call_or_fail "${step}GetJobAfter409" "GET" "${BASE_URL}/v1/ai/jobs/${job_id}"
	local status
	status="$(extract_field "${LAST_BODY}" "status" || true)"
	case "${status}" in
		running | succeeded | failed | canceled) ;;
		*)
			fail_l10 "${step}" "status after 409 is unexpected: ${status:-EMPTY}"
			;;
	esac
}

build_diagnosis_json() {
	"${PYBIN}" - <<'PY'
import json

evidence = [f"evidence-{i:03d}" for i in range(55)]
missing = [f"missing-{i:03d}" for i in range(30)]
payload = {
    "summary": "db pool exhausted",
    "root_cause": {
        "type": "database",
        "summary": "db pool exhausted",
        "confidence": 0.92,
        "evidence_ids": evidence[:3],
    },
    "missing_evidence": missing,
    "timeline": [],
    "hypotheses": [
        {
            "statement": "db pool saturation",
            "confidence": 0.6,
            "supporting_evidence_ids": evidence,
            "missing_evidence": missing,
        }
    ],
    "recommendations": [],
    "unknowns": [],
    "next_steps": ["check connection pool settings"],
}
print(json.dumps(payload, ensure_ascii=False))
PY
}

assert_compact_payload_or_fail() {
	local body="$1"
	local has_diag has_diag_min has_secret has_headers missing_len has_hypotheses

	has_diag="$(printf '%s' "${body}" | jq -r 'has("diagnosis")' 2>/dev/null || true)"
	has_diag_min="$(printf '%s' "${body}" | jq -r 'has("diagnosis_min")' 2>/dev/null || true)"
	has_secret="$(printf '%s' "${body}" | jq -r 'has("secret")' 2>/dev/null || true)"
	has_headers="$(printf '%s' "${body}" | jq -r 'has("headers")' 2>/dev/null || true)"
	missing_len="$(printf '%s' "${body}" | jq -r '(.diagnosis_min.missing_evidence // []) | length' 2>/dev/null || true)"
	has_hypotheses="$(printf '%s' "${body}" | jq -r 'has("hypotheses")' 2>/dev/null || true)"

	if [[ "${has_diag}" != "false" ]]; then
		fail_l10 "AssertCompactNoFullDiagnosis" "compact payload should not contain diagnosis key" "ASSERT_FAIL" "${body}"
	fi
	if [[ "${has_diag_min}" != "true" ]]; then
		fail_l10 "AssertCompactDiagnosisMin" "compact payload should contain diagnosis_min" "ASSERT_FAIL" "${body}"
	fi
	if [[ "${has_secret}" != "false" || "${has_headers}" != "false" ]]; then
		fail_l10 "AssertCompactNoSensitive" "compact payload should not contain secret/headers" "ASSERT_FAIL" "${body}"
	fi
	if [[ ! "${missing_len}" =~ ^[0-9]+$ ]] || (( missing_len > 20 )); then
		fail_l10 "AssertCompactMissingEvidenceCap" "compact diagnosis_min.missing_evidence expected <=20, got=${missing_len:-EMPTY}" "ASSERT_FAIL" "${body}"
	fi
	if [[ "${has_hypotheses}" != "false" ]]; then
		fail_l10 "AssertCompactNoHypotheses" "compact payload should not include full diagnosis hypotheses" "ASSERT_FAIL" "${body}"
	fi
}

assert_full_payload_or_fail() {
	local body="$1"
	local has_diag diag_ok top_evidence_len diag_evidence_len diag_missing_len has_secret has_headers

	has_diag="$(printf '%s' "${body}" | jq -r 'has("diagnosis")' 2>/dev/null || true)"
	diag_ok="$(printf '%s' "${body}" | jq -r '(.diagnosis // {}) | (has("confidence") and has("root_cause") and has("evidence_ids"))' 2>/dev/null || true)"
	top_evidence_len="$(printf '%s' "${body}" | jq -r '(.evidence_ids // []) | length' 2>/dev/null || true)"
	diag_evidence_len="$(printf '%s' "${body}" | jq -r '(.diagnosis.evidence_ids // []) | length' 2>/dev/null || true)"
	diag_missing_len="$(printf '%s' "${body}" | jq -r '(.diagnosis.missing_evidence // []) | length' 2>/dev/null || true)"
	has_secret="$(printf '%s' "${body}" | jq -r 'has("secret")' 2>/dev/null || true)"
	has_headers="$(printf '%s' "${body}" | jq -r 'has("headers")' 2>/dev/null || true)"

	if [[ "${has_diag}" != "true" ]]; then
		fail_l10 "AssertFullHasDiagnosis" "full payload should contain diagnosis key" "ASSERT_FAIL" "${body}"
	fi
	if [[ "${diag_ok}" != "true" ]]; then
		fail_l10 "AssertFullDiagnosisFields" "full diagnosis should include confidence/root_cause/evidence_ids" "ASSERT_FAIL" "${body}"
	fi
	if [[ ! "${top_evidence_len}" =~ ^[0-9]+$ ]] || (( top_evidence_len <= 0 || top_evidence_len > 50 )); then
		fail_l10 "AssertFullTopEvidenceCap" "full payload evidence_ids expected 1..50, got=${top_evidence_len:-EMPTY}" "ASSERT_FAIL" "${body}"
	fi
	if [[ ! "${diag_evidence_len}" =~ ^[0-9]+$ ]] || (( diag_evidence_len <= 0 || diag_evidence_len > 50 )); then
		fail_l10 "AssertFullDiagnosisEvidenceCap" "full diagnosis.evidence_ids expected 1..50, got=${diag_evidence_len:-EMPTY}" "ASSERT_FAIL" "${body}"
	fi
	if [[ ! "${diag_missing_len}" =~ ^[0-9]+$ ]] || (( diag_missing_len > 20 )); then
		fail_l10 "AssertFullMissingEvidenceCap" "full diagnosis.missing_evidence expected <=20, got=${diag_missing_len:-EMPTY}" "ASSERT_FAIL" "${body}"
	fi
	if [[ "${has_secret}" != "false" || "${has_headers}" != "false" ]]; then
		fail_l10 "AssertFullNoSensitive" "full payload should not contain secret/headers" "ASSERT_FAIL" "${body}"
	fi
}

if ! need_cmd jq; then
	fail_l10 "PrecheckTools" "jq not found"
fi
resolve_python
start_mock

rand="${RAND:-$RANDOM}"
now_epoch="$(date -u +%s)"
start_epoch="$((now_epoch - 1200))"
endpoint_compact="http://127.0.0.1:${MOCK_PORT}/webhook/compact"
endpoint_full="http://127.0.0.1:${MOCK_PORT}/webhook/full"

create_compact_body=$(cat <<EOF
{"name":"p2-l10-compact-${rand}","type":"webhook","enabled":true,"endpointURL":"${endpoint_compact}","timeoutMs":1200,"maxRetries":0,"payloadMode":1,"includeDiagnosis":true,"includeEvidenceIds":false,"includeRootCause":true,"includeLinks":true}
EOF
)
call_or_fail "CreateChannelCompact" POST "${BASE_URL}/v1/notice-channels" "${create_compact_body}"
CHANNEL_COMPACT_ID="$(extract_field "${LAST_BODY}" "channelID" "channel_id" || true)"
if [[ -z "${CHANNEL_COMPACT_ID}" ]]; then
	fail_l10 "CreateChannelCompactParse" "channel_compact_id missing"
fi

create_full_body=$(cat <<EOF
{"name":"p2-l10-full-${rand}","type":"webhook","enabled":true,"endpointURL":"${endpoint_full}","timeoutMs":1200,"maxRetries":0,"payloadMode":2,"includeDiagnosis":true,"includeEvidenceIds":true,"includeRootCause":true,"includeLinks":true}
EOF
)
call_or_fail "CreateChannelFull" POST "${BASE_URL}/v1/notice-channels" "${create_full_body}"
CHANNEL_FULL_ID="$(extract_field "${LAST_BODY}" "channelID" "channel_id" || true)"
if [[ -z "${CHANNEL_FULL_ID}" ]]; then
	fail_l10 "CreateChannelFullParse" "channel_full_id missing"
fi

ingest_body=$(cat <<EOF
{"idempotencyKey":"idem-p2-l10-ingest-${rand}","fingerprint":"p2-l10-fp-${rand}","status":"firing","severity":"P1","service":"notice-l10","cluster":"prod-l10","namespace":"default","workload":"checkout-api","lastSeenAt":{"seconds":${now_epoch},"nanos":0}}
EOF
)
call_or_fail "IngestAlertEvent" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_body}"
INCIDENT_ID="$(extract_field "${LAST_BODY}" "incidentID" "incident_id" || true)"
if [[ -z "${INCIDENT_ID}" ]]; then
	fail_l10 "IngestAlertEventParse" "incident_id missing"
fi

wait_delivery_count_or_fail "WaitIncidentCreatedDeliveries" "incident_created" 2
DELIVERY_INCIDENT_COMPACT_ID="$(extract_delivery_id_or_fail "ExtractIncidentCompactDelivery" "incident_created" "${CHANNEL_COMPACT_ID}")"
DELIVERY_INCIDENT_FULL_ID="$(extract_delivery_id_or_fail "ExtractIncidentFullDelivery" "incident_created" "${CHANNEL_FULL_ID}")"

run_body=$(cat <<EOF
{"incidentID":"${INCIDENT_ID}","idempotencyKey":"idem-p2-l10-run-${rand}","pipeline":"basic_rca","trigger":"manual","timeRangeStart":{"seconds":${start_epoch},"nanos":0},"timeRangeEnd":{"seconds":${now_epoch},"nanos":0}}
EOF
)
call_or_fail "RunAIJob" POST "${BASE_URL}/v1/incidents/${INCIDENT_ID}/ai:run" "${run_body}"
JOB_ID="$(extract_field "${LAST_BODY}" "jobID" "job_id" || true)"
if [[ -z "${JOB_ID}" ]]; then
	fail_l10 "RunAIJobParse" "job_id missing"
fi
start_ai_job_or_skip "StartAIJob" "${JOB_ID}"

diagnosis_json="$(build_diagnosis_json)"
finalize_body=$(cat <<EOF
{"jobID":"${JOB_ID}","status":"succeeded","diagnosisJSON":$(json_string "${diagnosis_json}")}
EOF
)
call_or_fail "FinalizeAIJob" POST "${BASE_URL}/v1/ai/jobs/${JOB_ID}/finalize" "${finalize_body}"

wait_delivery_count_or_fail "WaitDiagnosisWrittenDeliveries" "diagnosis_written" 2
DELIVERY_DIAG_COMPACT_ID="$(extract_delivery_id_or_fail "ExtractDiagnosisCompactDelivery" "diagnosis_written" "${CHANNEL_COMPACT_ID}")"
DELIVERY_DIAG_FULL_ID="$(extract_delivery_id_or_fail "ExtractDiagnosisFullDelivery" "diagnosis_written" "${CHANNEL_FULL_ID}")"

call_or_fail "GetDiagnosisCompactDelivery" GET "${BASE_URL}/v1/notice-deliveries/${DELIVERY_DIAG_COMPACT_ID}"
compact_request_body="$(printf '%s' "${LAST_BODY}" | jq -r '(.noticeDelivery // .data.noticeDelivery // {}) | (.requestBody // "")' 2>/dev/null || true)"
if [[ -z "${compact_request_body}" ]]; then
	fail_l10 "GetDiagnosisCompactDeliveryParse" "compact requestBody missing"
fi
assert_compact_payload_or_fail "${compact_request_body}"

call_or_fail "GetDiagnosisFullDelivery" GET "${BASE_URL}/v1/notice-deliveries/${DELIVERY_DIAG_FULL_ID}"
full_request_body="$(printf '%s' "${LAST_BODY}" | jq -r '(.noticeDelivery // .data.noticeDelivery // {}) | (.requestBody // "")' 2>/dev/null || true)"
if [[ -z "${full_request_body}" ]]; then
	fail_l10 "GetDiagnosisFullDeliveryParse" "full requestBody missing"
fi
assert_full_payload_or_fail "${full_request_body}"

echo "PASS L10-NOTICE-TEMPLATE"
echo "channel_compact_id=${CHANNEL_COMPACT_ID}"
echo "channel_full_id=${CHANNEL_FULL_ID}"
echo "incident_id=${INCIDENT_ID}"
echo "job_id=${JOB_ID}"
echo "delivery_incident_compact_id=${DELIVERY_INCIDENT_COMPACT_ID}"
echo "delivery_incident_full_id=${DELIVERY_INCIDENT_FULL_ID}"
echo "delivery_diag_compact_id=${DELIVERY_DIAG_COMPACT_ID}"
echo "delivery_diag_full_id=${DELIVERY_DIAG_FULL_ID}"
