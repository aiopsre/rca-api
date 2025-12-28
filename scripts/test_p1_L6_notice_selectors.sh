#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
DEBUG="${DEBUG:-0}"
MOCK_PORT="${MOCK_PORT:-19094}"
WAIT_TIMEOUT_SEC="${WAIT_TIMEOUT_SEC:-90}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
WORKER_CONFIG="${WORKER_CONFIG:-${REPO_ROOT}/configs/rca-apiserver.yaml}"
WORKER_CMD="${WORKER_CMD:-GOLANG_PROTOBUF_REGISTRATION_CONFLICT=warn go run ./cmd/rca-apiserver --config ${WORKER_CONFIG} notice-worker --notice-worker-poll-interval=200ms --notice-worker-batch-size=8 --notice-worker-lock-timeout=5s}"

LAST_HTTP_CODE=""
LAST_BODY=""

CHANNEL_ALL_ID=""
CHANNEL_DIAG_ONLY_ID=""
INCIDENT_ID=""
JOB_ID=""
DELIVERY_COUNT="0"
MOCK_TOTAL="0"

MOCK_EVENTS_FILE="$(mktemp)"
MOCK_LOG_FILE="$(mktemp)"
WORKER_LOG_FILE="$(mktemp)"
MOCK_PID=""
WORKER_PID=""

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

fail_l6() {
	local step="$1"
	local detail="${2:-non-2xx response}"
	local code="${3:-${LAST_HTTP_CODE}}"
	local body="${4:-${LAST_BODY}}"

	echo "FAIL L6-NOTICE-SELECTORS step=${step}"
	echo "detail=${detail}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	trim_2kb "${body}"
	echo
	echo "EOF"
	echo "channel_all_id=${CHANNEL_ALL_ID:-NONE}"
	echo "channel_diag_only_id=${CHANNEL_DIAG_ONLY_ID:-NONE}"
	echo "incident_id=${INCIDENT_ID:-NONE}"
	echo "job_id=${JOB_ID:-NONE}"
	echo "delivery_count=${DELIVERY_COUNT:-0}"
	echo "mock_total=${MOCK_TOTAL:-0}"
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
		fail_l6 "${step}" "curl failed"
	fi
	if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
		fail_l6 "${step}"
	fi
	debug "${step} code=${LAST_HTTP_CODE}"
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

parse_total_count() {
	local raw="$1"
	if need_cmd jq; then
		printf '%s' "${raw}" | jq -r '.totalCount // .data.totalCount // ((.noticeDeliveries // .data.noticeDeliveries // []) | length)' 2>/dev/null || true
	else
		printf '%s' "${raw}" | sed -n 's/.*"totalCount"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' | head -n 1
	fi
}

count_mock_total() {
	if [[ ! -s "${MOCK_EVENTS_FILE}" ]]; then
		echo "0"
		return
	fi
	wc -l <"${MOCK_EVENTS_FILE}" | tr -d ' '
}

count_mock_event_path() {
	local event_type="$1"
	local path="$2"
	if [[ ! -s "${MOCK_EVENTS_FILE}" ]]; then
		echo "0"
		return
	fi
	if need_cmd jq; then
		jq -r --arg ev "${event_type}" --arg p "${path}" 'select((.event_type // "") == $ev and (.path // "") == $p) | .event_type' "${MOCK_EVENTS_FILE}" 2>/dev/null | wc -l | tr -d ' '
	else
		grep -c "\"event_type\"[[:space:]]*:[[:space:]]*\"${event_type}\".*\"path\"[[:space:]]*:[[:space:]]*\"${path}\"" "${MOCK_EVENTS_FILE}" || true
	fi
}

assert_worker_alive() {
	if [[ -n "${WORKER_PID}" ]] && ! kill -0 "${WORKER_PID}" >/dev/null 2>&1; then
		LAST_HTTP_CODE="WORKER_EXITED"
		LAST_BODY="$(cat "${WORKER_LOG_FILE}" 2>/dev/null || true)"
		fail_l6 "$1" "worker exited unexpectedly"
	fi
}

wait_mock_event_or_fail() {
	local step="$1"
	local event_type="$2"
	local path="$3"
	local expected="$4"
	local deadline now cnt
	deadline="$(( $(date +%s) + WAIT_TIMEOUT_SEC ))"

	while true; do
		assert_worker_alive "${step}"
		cnt="$(count_mock_event_path "${event_type}" "${path}")"
		if [[ "${cnt}" =~ ^[0-9]+$ ]] && (( cnt >= expected )); then
			return 0
		fi
		now="$(date +%s)"
		if (( now > deadline )); then
			fail_l6 "${step}" "mock event=${event_type} path=${path} expected>=${expected}, got=${cnt:-EMPTY}"
		fi
		sleep 1
	done
}

wait_delivery_count_or_fail() {
	local step="$1"
	local channel_id="$2"
	local event_type="$3"
	local status="$4"
	local expected="$5"
	local deadline count url
	deadline="$(( $(date +%s) + WAIT_TIMEOUT_SEC ))"

	url="${BASE_URL}/v1/notice-deliveries?incident_id=${INCIDENT_ID}&channel_id=${channel_id}&event_type=${event_type}&offset=0&limit=50"
	if [[ -n "${status}" ]]; then
		url="${url}&status=${status}"
	fi

	while true; do
		assert_worker_alive "${step}"
		call_or_fail "${step}List" "GET" "${url}"
		count="$(parse_total_count "${LAST_BODY}")"
		if [[ "${count}" =~ ^[0-9]+$ ]] && (( count >= expected )); then
			return 0
		fi
		if (( $(date +%s) > deadline )); then
			fail_l6 "${step}" "delivery count expected>=${expected}, got=${count:-EMPTY}, channel=${channel_id}, event=${event_type}, status=${status:-ANY}"
		fi
		sleep 1
	done
}

query_delivery_count_or_fail() {
	local step="$1"
	local channel_id="$2"
	local event_type="$3"
	local status="${4:-}"
	local url count

	url="${BASE_URL}/v1/notice-deliveries?incident_id=${INCIDENT_ID}&channel_id=${channel_id}&event_type=${event_type}&offset=0&limit=50"
	if [[ -n "${status}" ]]; then
		url="${url}&status=${status}"
	fi
	call_or_fail "${step}" "GET" "${url}"
	count="$(parse_total_count "${LAST_BODY}")"
	if [[ ! "${count}" =~ ^[0-9]+$ ]]; then
		fail_l6 "${step}" "unable to parse delivery count"
	fi
	printf '%s' "${count}"
}

start_ai_job_or_skip() {
	local step="$1"
	local job_id="$2"
	if http_json "POST" "${BASE_URL}/v1/ai/jobs/${job_id}/start" '{}'; then
		if [[ "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
			debug "${step} code=${LAST_HTTP_CODE}"
			return 0
		fi
	fi

	if [[ "${LAST_HTTP_CODE}" != "409" ]]; then
		fail_l6 "${step}"
	fi

	call_or_fail "${step}GetJobAfter409" "GET" "${BASE_URL}/v1/ai/jobs/${job_id}"
	local job_status
	job_status="$(extract_field "${LAST_BODY}" "status" || true)"
	case "${job_status}" in
		running | succeeded | failed | canceled)
			debug "${step} skip start due current status=${job_status}"
			return 0
			;;
		*)
			fail_l6 "${step}" "status after 409 is unexpected: ${job_status:-EMPTY}"
			;;
	esac
}

cleanup() {
	if [[ -n "${WORKER_PID}" ]]; then
		kill "${WORKER_PID}" >/dev/null 2>&1 || true
		wait "${WORKER_PID}" >/dev/null 2>&1 || true
	fi
	if [[ -n "${MOCK_PID}" ]]; then
		kill "${MOCK_PID}" >/dev/null 2>&1 || true
		wait "${MOCK_PID}" >/dev/null 2>&1 || true
	fi
	rm -f "${MOCK_EVENTS_FILE}" "${MOCK_LOG_FILE}" "${WORKER_LOG_FILE}"
}
trap cleanup EXIT

start_mock() {
	local pybin
	if need_cmd python3; then
		pybin="python3"
	elif need_cmd python; then
		pybin="python"
	else
		fail_l6 "StartMock" "python3/python not found"
	fi

	"${pybin}" -u - <<'PY' "${MOCK_PORT}" "${MOCK_EVENTS_FILE}" >"${MOCK_LOG_FILE}" 2>&1 &
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
        if self.path not in ("/webhook/all", "/webhook/diag"):
            self.send_response(404)
            self.end_headers()
            return

        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length)
        try:
            payload = json.loads(body.decode("utf-8"))
        except Exception:
            payload = {}

        item = {
            "path": self.path,
            "event_type": payload.get("event_type", ""),
            "idempotency_key": self.headers.get("Idempotency-Key", ""),
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
			fail_l6 "StartMock" "mock webhook start timeout"
		fi
		sleep 0.5
	done
}

start_worker() {
	local worker_id="p1-l6-notice-worker-${rand}-$$"
	(
		cd "${REPO_ROOT}" && bash -lc "${WORKER_CMD} --notice-worker-id ${worker_id}"
	) >"${WORKER_LOG_FILE}" 2>&1 &
	WORKER_PID="$!"
	sleep 0.3
	if ! kill -0 "${WORKER_PID}" >/dev/null 2>&1; then
		LAST_HTTP_CODE="WORKER_EXITED"
		LAST_BODY="$(cat "${WORKER_LOG_FILE}" 2>/dev/null || true)"
		fail_l6 "StartWorker" "worker exited immediately"
	fi
}

json_string() {
	local raw="$1"
	if need_cmd jq; then
		printf '%s' "$raw" | jq -Rs .
	else
		printf '"%s"' "$(printf '%s' "$raw" | sed 's/\\/\\\\/g; s/"/\\"/g')"
	fi
}

start_mock

rand="${RAND:-$RANDOM}"
now_epoch="$(date -u +%s)"
start_epoch="$((now_epoch - 1800))"
fingerprint="p1-l6-notice-selectors-fp-${rand}"
service="p1-l6-notice-selectors-svc-${rand}"
endpoint_all="http://127.0.0.1:${MOCK_PORT}/webhook/all"
endpoint_diag="http://127.0.0.1:${MOCK_PORT}/webhook/diag"

create_channel_all_body=$(cat <<EOF
{"name":"p1-l6-channel-all-${rand}","type":"webhook","enabled":true,"endpointURL":"${endpoint_all}","timeoutMs":1200,"maxRetries":2}
EOF
)
call_or_fail "CreateChannelAll" POST "${BASE_URL}/v1/notice-channels" "${create_channel_all_body}"
CHANNEL_ALL_ID="$(extract_field "${LAST_BODY}" "channelID" "channel_id" || true)"
if [[ -z "${CHANNEL_ALL_ID}" ]]; then
	fail_l6 "CreateChannelAllParse" "channel_all_id missing"
fi

create_channel_diag_body=$(cat <<EOF
{"name":"p1-l6-channel-diag-${rand}","type":"webhook","enabled":true,"endpointURL":"${endpoint_diag}","timeoutMs":1200,"maxRetries":2,"selectors":{"eventTypes":["diagnosis_written"]}}
EOF
)
call_or_fail "CreateChannelDiagOnly" POST "${BASE_URL}/v1/notice-channels" "${create_channel_diag_body}"
CHANNEL_DIAG_ONLY_ID="$(extract_field "${LAST_BODY}" "channelID" "channel_id" || true)"
if [[ -z "${CHANNEL_DIAG_ONLY_ID}" ]]; then
	fail_l6 "CreateChannelDiagOnlyParse" "channel_diag_only_id missing"
fi

ingest_body=$(cat <<EOF
{"idempotencyKey":"idem-p1-l6-notice-ingest-${rand}","fingerprint":"${fingerprint}","status":"firing","severity":"P1","service":"${service}","cluster":"prod-l6","namespace":"default","workload":"checkout-api","lastSeenAt":{"seconds":${now_epoch},"nanos":0}}
EOF
)
call_or_fail "IngestAlertEvent" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_body}"
INCIDENT_ID="$(extract_field "${LAST_BODY}" "incidentID" "incident_id" || true)"
if [[ -z "${INCIDENT_ID}" ]]; then
	fail_l6 "IngestAlertEventParse" "incident_id missing"
fi

run_body=$(cat <<EOF
{"incidentID":"${INCIDENT_ID}","idempotencyKey":"idem-p1-l6-notice-run-${rand}","pipeline":"basic_rca","trigger":"manual","timeRangeStart":{"seconds":${start_epoch},"nanos":0},"timeRangeEnd":{"seconds":${now_epoch},"nanos":0}}
EOF
)
call_or_fail "RunAIJob" POST "${BASE_URL}/v1/incidents/${INCIDENT_ID}/ai:run" "${run_body}"
JOB_ID="$(extract_field "${LAST_BODY}" "jobID" "job_id" || true)"
if [[ -z "${JOB_ID}" ]]; then
	fail_l6 "RunAIJobParse" "job_id missing"
fi

start_ai_job_or_skip "StartAIJob" "${JOB_ID}"

diagnosis_json='{"summary":"db pool exhausted","root_cause":{"category":"db","statement":"connection pool saturated","confidence":0.9,"evidence_ids":["evidence-l6-1","evidence-l6-2"]},"timeline":[{"t":"2026-02-08T00:00:00Z","event":"alert_fired","ref":"alert-l6"}],"hypotheses":[{"statement":"db pool limit reached","confidence":0.9,"supporting_evidence_ids":["evidence-l6-1","evidence-l6-2"],"missing_evidence":[]}],"recommendations":[{"type":"readonly_check","action":"inspect db pool","risk":"low"}],"unknowns":[],"next_steps":["increase max open connections"]}'
finalize_body=$(cat <<EOF
{"jobID":"${JOB_ID}","status":"succeeded","diagnosisJSON":$(json_string "${diagnosis_json}")}
EOF
)
call_or_fail "FinalizeAIJob" POST "${BASE_URL}/v1/ai/jobs/${JOB_ID}/finalize" "${finalize_body}"

start_worker

wait_delivery_count_or_fail "WaitAllIncidentCreatedSucceeded" "${CHANNEL_ALL_ID}" "incident_created" "succeeded" 1
wait_delivery_count_or_fail "WaitAllDiagnosisWrittenSucceeded" "${CHANNEL_ALL_ID}" "diagnosis_written" "succeeded" 1
wait_delivery_count_or_fail "WaitDiagOnlyDiagnosisWrittenSucceeded" "${CHANNEL_DIAG_ONLY_ID}" "diagnosis_written" "succeeded" 1

diag_incident_count="$(query_delivery_count_or_fail "QueryDiagOnlyIncidentCreated" "${CHANNEL_DIAG_ONLY_ID}" "incident_created")"
if (( diag_incident_count != 0 )); then
	fail_l6 "AssertDiagOnlyNotIncidentCreated" "diagnosis-only channel should not receive incident_created, got=${diag_incident_count}"
fi

wait_mock_event_or_fail "WaitMockAllIncidentCreated" "incident_created" "/webhook/all" 1
wait_mock_event_or_fail "WaitMockAllDiagnosisWritten" "diagnosis_written" "/webhook/all" 1
wait_mock_event_or_fail "WaitMockDiagOnlyDiagnosisWritten" "diagnosis_written" "/webhook/diag" 1

mock_diag_incident_count="$(count_mock_event_path "incident_created" "/webhook/diag")"
if [[ ! "${mock_diag_incident_count}" =~ ^[0-9]+$ ]]; then
	fail_l6 "AssertMockDiagOnlyIncidentCreatedParse" "unable to parse mock diag incident_created count"
fi
if (( mock_diag_incident_count != 0 )); then
	fail_l6 "AssertMockDiagOnlyNotIncidentCreated" "mock diag endpoint should not receive incident_created, got=${mock_diag_incident_count}"
fi

DELIVERY_COUNT="$(query_delivery_count_or_fail "QueryTotalDeliveriesByIncident" "${CHANNEL_ALL_ID}" "incident_created")"
DELIVERY_COUNT="$(( DELIVERY_COUNT + $(query_delivery_count_or_fail "QueryTotalDeliveriesDiagWrittenAll" "${CHANNEL_ALL_ID}" "diagnosis_written") + $(query_delivery_count_or_fail "QueryTotalDeliveriesDiagWrittenDiagOnly" "${CHANNEL_DIAG_ONLY_ID}" "diagnosis_written") ))"
MOCK_TOTAL="$(count_mock_total)"

echo "PASS L6-NOTICE-SELECTORS"
echo "channel_all_id=${CHANNEL_ALL_ID}"
echo "channel_diag_only_id=${CHANNEL_DIAG_ONLY_ID}"
echo "incident_id=${INCIDENT_ID}"
echo "job_id=${JOB_ID}"
echo "delivery_count=${DELIVERY_COUNT}"
