#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
DEBUG="${DEBUG:-0}"
MOCK_PORT="${MOCK_PORT:-19091}"
MOCK_WAIT_TIMEOUT_SEC="${MOCK_WAIT_TIMEOUT_SEC:-20}"
WAIT_TIMEOUT_SEC="${WAIT_TIMEOUT_SEC:-60}"
RUN_WORKER="${RUN_WORKER:-0}"
WORKER_DURATION_SECONDS="${WORKER_DURATION_SECONDS:-6}"
WORKER_CONCURRENCY="${WORKER_CONCURRENCY:-1}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
WORKER_CONFIG="${WORKER_CONFIG:-${REPO_ROOT}/configs/rca-apiserver.yaml}"
WORKER_CMD="${WORKER_CMD:-GOLANG_PROTOBUF_REGISTRATION_CONFLICT=warn go run ./cmd/rca-apiserver --config ${WORKER_CONFIG} notice-worker --notice-worker-poll-interval=200ms --notice-worker-batch-size=8 --notice-worker-lock-timeout=5s}"

LAST_HTTP_CODE=""
LAST_BODY=""

CHANNEL_ID=""
INCIDENT_ID=""
JOB_ID=""
EVENT_ID=""
DELIVERY_COUNT="0"
MOCK_TOTAL="0"

MOCK_EVENTS_FILE="$(mktemp)"
MOCK_LOG_FILE="$(mktemp)"
WORKER_LOG_FILE="$(mktemp)"
MOCK_PID=""
WORKER_PIDS=()
WORKER_STOPPER_PIDS=()

debug() {
	if [[ "${DEBUG}" == "1" ]]; then
		echo "[DEBUG] $*" >&2
	fi
}

need_cmd() {
	command -v "$1" >/dev/null 2>&1
}

is_positive_int() {
	[[ "$1" =~ ^[0-9]+$ ]] && (( "$1" > 0 ))
}

validate_runtime_flags() {
	case "${RUN_WORKER}" in
		0 | 1) ;;
		*)
			fail_l3_notice "ValidateFlags" "RUN_WORKER must be 0 or 1, got=${RUN_WORKER}"
			;;
	esac

	if ! is_positive_int "${WORKER_DURATION_SECONDS}"; then
		fail_l3_notice "ValidateFlags" "WORKER_DURATION_SECONDS must be >0 integer, got=${WORKER_DURATION_SECONDS}"
	fi

	if ! is_positive_int "${WORKER_CONCURRENCY}"; then
		fail_l3_notice "ValidateFlags" "WORKER_CONCURRENCY must be >0 integer, got=${WORKER_CONCURRENCY}"
	fi
}

truncate_2kb() {
	printf '%s' "$1" | head -c 2048
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

fail_l3_notice() {
	local step="$1"
	local detail="${2:-non-2xx response}"
	local code="${3:-${LAST_HTTP_CODE}}"
	local body="${4:-${LAST_BODY}}"

	echo "FAIL L3-NOTICE step=${step}"
	echo "detail=${detail}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	truncate_2kb "${body}"
	echo
	echo "EOF"
	echo "channel_id=${CHANNEL_ID:-NONE}"
	echo "incident_id=${INCIDENT_ID:-NONE}"
	echo "job_id=${JOB_ID:-NONE}"
	echo "event_id=${EVENT_ID:-NONE}"
	echo "mock_total=${MOCK_TOTAL:-0}"
	echo "delivery_count=${DELIVERY_COUNT:-0}"
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
		fail_l3_notice "${step}" "curl failed"
	fi
	if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
		fail_l3_notice "${step}"
	fi
	debug "${step} code=${LAST_HTTP_CODE}"
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
		fail_l3_notice "${step}"
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
			fail_l3_notice "${step}" "status after 409 is unexpected: ${job_status:-EMPTY}"
			;;
	esac
}

count_mock_event() {
	local event_type="$1"
	if [[ ! -s "${MOCK_EVENTS_FILE}" ]]; then
		echo "0"
		return
	fi
	if need_cmd jq; then
		jq -r --arg ev "${event_type}" 'select(.event_type == $ev) | .event_type' "${MOCK_EVENTS_FILE}" 2>/dev/null | wc -l | tr -d ' '
	else
		grep -c "\"event_type\"[[:space:]]*:[[:space:]]*\"${event_type}\"" "${MOCK_EVENTS_FILE}" || true
	fi
}

count_mock_total() {
	if [[ ! -s "${MOCK_EVENTS_FILE}" ]]; then
		echo "0"
		return
	fi
	wc -l <"${MOCK_EVENTS_FILE}" | tr -d ' '
}

assert_worker_alive() {
	local pid alive
	alive=0
	for pid in "${WORKER_PIDS[@]}"; do
		if kill -0 "${pid}" >/dev/null 2>&1; then
			alive=1
			break
		fi
	done
	if (( alive == 0 )); then
		LAST_HTTP_CODE="WORKER_EXITED"
		LAST_BODY="$(cat "${WORKER_LOG_FILE}" 2>/dev/null || true)"
		fail_l3_notice "$1" "worker exited before callback completed"
	fi
}

wait_for_event_or_fail() {
	local step="$1"
	local event_type="$2"
	local expected="$3"
	local deadline now cnt
	deadline="$(( $(date +%s) + WAIT_TIMEOUT_SEC ))"

	while true; do
		assert_worker_alive "${step}"
		cnt="$(count_mock_event "${event_type}")"
		if [[ "${cnt}" =~ ^[0-9]+$ ]] && (( cnt >= expected )); then
			return 0
		fi
		now="$(date +%s)"
		if (( now > deadline )); then
			fail_l3_notice "${step}" "mock event ${event_type} expected>=${expected}, got=${cnt}"
		fi
		sleep 1
	done
}

cleanup() {
	local pid
	for pid in "${WORKER_PIDS[@]}"; do
		kill "${pid}" >/dev/null 2>&1 || true
	done
	for pid in "${WORKER_PIDS[@]}"; do
		wait "${pid}" >/dev/null 2>&1 || true
	done
	for pid in "${WORKER_STOPPER_PIDS[@]}"; do
		kill "${pid}" >/dev/null 2>&1 || true
		wait "${pid}" >/dev/null 2>&1 || true
	done
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
		fail_l3_notice "StartMock" "python3/python not found"
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
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length)
        try:
            payload = json.loads(body.decode("utf-8"))
        except Exception:
            payload = {"raw": body.decode("utf-8", errors="replace")}
        with open(events_path, "a", encoding="utf-8") as fp:
            fp.write(json.dumps(payload, ensure_ascii=False) + "\n")
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
			fail_l3_notice "StartMock" "mock webhook start timeout"
		fi
		sleep 0.5
	done
}

json_string() {
	local raw="$1"
	if need_cmd jq; then
		printf '%s' "$raw" | jq -Rs .
	else
		printf '"%s"' "$(printf '%s' "$raw" | sed 's/\\/\\\\/g; s/"/\\"/g')"
	fi
}

start_worker_instance() {
	local idx="$1"
	local worker_id="p1-l3-notice-worker-${rand}-${idx}-$$"
	(
		cd "${REPO_ROOT}" && bash -lc "${WORKER_CMD} --notice-worker-id ${worker_id}"
	) >>"${WORKER_LOG_FILE}" 2>&1 &
	local worker_pid="$!"
	sleep 0.3
	if ! kill -0 "${worker_pid}" >/dev/null 2>&1; then
		LAST_HTTP_CODE="WORKER_EXITED"
		LAST_BODY="$(cat "${WORKER_LOG_FILE}" 2>/dev/null || true)"
		fail_l3_notice "StartWorker" "worker exited immediately"
	fi
	WORKER_PIDS+=("${worker_pid}")

	(
		sleep "${WORKER_DURATION_SECONDS}"
		kill "${worker_pid}" >/dev/null 2>&1 || true
		wait "${worker_pid}" >/dev/null 2>&1 || true
	) >/dev/null 2>&1 &
	WORKER_STOPPER_PIDS+=("$!")
}

start_workers() {
	local idx
	for ((idx = 1; idx <= WORKER_CONCURRENCY; idx++)); do
		start_worker_instance "${idx}"
	done
}

validate_runtime_flags
start_mock

rand="${RAND:-$RANDOM}"
now_epoch="$(date -u +%s)"
start_epoch="$((now_epoch - 1800))"
fingerprint="p1-l3-notice-fp-${rand}"
service="p1-l3-notice-svc-${rand}"
endpoint="http://127.0.0.1:${MOCK_PORT}/webhook"

create_channel_body=$(cat <<EOF
{"name":"p1-l3-notice-${rand}","type":"webhook","enabled":true,"endpointURL":"${endpoint}","timeoutMs":1200,"maxRetries":0}
EOF
)
call_or_fail "CreateNoticeChannel" POST "${BASE_URL}/v1/notice-channels" "${create_channel_body}"
CHANNEL_ID="$(extract_field "${LAST_BODY}" "channelID" "channel_id")" || true
if [[ -z "${CHANNEL_ID}" ]]; then
	fail_l3_notice "CreateNoticeChannelParseChannelID" "channel_id missing"
fi

ingest_body=$(cat <<EOF
{"idempotencyKey":"idem-p1-l3-notice-ingest-${rand}","fingerprint":"${fingerprint}","status":"firing","severity":"P1","service":"${service}","cluster":"prod-l3","namespace":"default","workload":"checkout-api","lastSeenAt":{"seconds":${now_epoch},"nanos":0}}
EOF
)
call_or_fail "IngestAlertEvent" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_body}"
INCIDENT_ID="$(extract_field "${LAST_BODY}" "incidentID" "incident_id")" || true
EVENT_ID="$(extract_field "${LAST_BODY}" "eventID" "event_id")" || true
if [[ -z "${INCIDENT_ID}" ]]; then
	fail_l3_notice "IngestAlertEventParseIncidentID" "incident_id missing"
fi

run_body=$(cat <<EOF
{"incidentID":"${INCIDENT_ID}","idempotencyKey":"idem-p1-l3-notice-run-${rand}","pipeline":"basic_rca","trigger":"manual","timeRangeStart":{"seconds":${start_epoch},"nanos":0},"timeRangeEnd":{"seconds":${now_epoch},"nanos":0}}
EOF
)
call_or_fail "RunAIJob" POST "${BASE_URL}/v1/incidents/${INCIDENT_ID}/ai:run" "${run_body}"
JOB_ID="$(extract_field "${LAST_BODY}" "jobID" "job_id")" || true
if [[ -z "${JOB_ID}" ]]; then
	fail_l3_notice "RunAIJobParseJobID" "job_id missing"
fi

start_ai_job_or_skip "StartAIJob" "${JOB_ID}"

diagnosis_json='{"summary":"db pool exhausted","root_cause":{"category":"db","statement":"connection pool saturated","confidence":0.9,"evidence_ids":["evidence-l3-1","evidence-l3-2"]},"timeline":[{"t":"2026-02-08T00:00:00Z","event":"alert_fired","ref":"alert-l3"}],"hypotheses":[{"statement":"db pool limit reached","confidence":0.9,"supporting_evidence_ids":["evidence-l3-1","evidence-l3-2"],"missing_evidence":[]}],"recommendations":[{"type":"readonly_check","action":"inspect db pool","risk":"low"}],"unknowns":[],"next_steps":["increase max open connections"]}'
finalize_body=$(cat <<EOF
{"jobID":"${JOB_ID}","status":"succeeded","diagnosisJSON":$(json_string "${diagnosis_json}")}
EOF
)
call_or_fail "FinalizeAIJob" POST "${BASE_URL}/v1/ai/jobs/${JOB_ID}/finalize" "${finalize_body}"

if [[ "${RUN_WORKER}" == "1" ]]; then
	start_workers
	wait_for_event_or_fail "WaitIncidentCreatedCallback" "incident_created" 1
	wait_for_event_or_fail "WaitDiagnosisWrittenCallback" "diagnosis_written" 1
fi

call_or_fail "ListNoticeDeliveriesByIncident" GET "${BASE_URL}/v1/notice-deliveries?incident_id=${INCIDENT_ID}&offset=0&limit=50"
if need_cmd jq; then
	DELIVERY_COUNT="$(printf '%s' "${LAST_BODY}" | jq -r '(.noticeDeliveries // .data.noticeDeliveries // []) | length' 2>/dev/null || true)"
	if [[ -z "${DELIVERY_COUNT}" ]] || (( DELIVERY_COUNT < 2 )); then
		fail_l3_notice "ListNoticeDeliveriesAssertCount" "delivery count expected >=2, got=${DELIVERY_COUNT:-EMPTY}"
	fi
	if [[ "${RUN_WORKER}" == "1" ]]; then
		succeeded_count="$(printf '%s' "${LAST_BODY}" | jq -r '[((.noticeDeliveries // .data.noticeDeliveries // [])[] | select((.status // "")=="succeeded"))] | length' 2>/dev/null || true)"
		if [[ -z "${succeeded_count}" ]] || (( succeeded_count < 2 )); then
			fail_l3_notice "ListNoticeDeliveriesAssertSucceeded" "succeeded count expected >=2, got=${succeeded_count:-EMPTY}"
		fi
	else
		invalid_status_count="$(printf '%s' "${LAST_BODY}" | jq -r '[((.noticeDeliveries // .data.noticeDeliveries // [])[] | select((.status // "") as $s | ($s != "pending" and $s != "failed" and $s != "succeeded")))] | length' 2>/dev/null || true)"
		if [[ -z "${invalid_status_count}" ]]; then
			fail_l3_notice "ListNoticeDeliveriesAssertStatus" "cannot parse delivery statuses"
		fi
		if (( invalid_status_count > 0 )); then
			fail_l3_notice "ListNoticeDeliveriesAssertStatus" "unexpected delivery status found, invalid_count=${invalid_status_count}"
		fi
	fi
else
	DELIVERY_COUNT="$(printf '%s' "${LAST_BODY}" | grep -o '"deliveryID"' | wc -l | tr -d ' ')"
	if [[ -z "${DELIVERY_COUNT}" ]] || (( DELIVERY_COUNT < 2 )); then
		fail_l3_notice "ListNoticeDeliveriesAssertCount" "delivery count expected >=2, got=${DELIVERY_COUNT:-EMPTY}"
	fi
	if [[ "${RUN_WORKER}" == "1" ]]; then
		succeeded_count="$(printf '%s' "${LAST_BODY}" | grep -Eo '"status"[[:space:]]*:[[:space:]]*"succeeded"' | wc -l | tr -d ' ')"
		if [[ -z "${succeeded_count}" ]] || (( succeeded_count < 2 )); then
			fail_l3_notice "ListNoticeDeliveriesAssertSucceeded" "succeeded count expected >=2, got=${succeeded_count:-EMPTY}"
		fi
	else
		status_lines="$(printf '%s' "${LAST_BODY}" | tr ',' '\n' | sed -n 's/.*"status"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
		if [[ -z "${status_lines}" ]]; then
			fail_l3_notice "ListNoticeDeliveriesAssertStatus" "delivery status missing"
		fi
		while IFS= read -r st; do
			case "${st}" in
				pending | failed | succeeded) ;;
				*)
					fail_l3_notice "ListNoticeDeliveriesAssertStatus" "unexpected delivery status=${st}"
					;;
			esac
		done <<<"${status_lines}"
	fi
fi

MOCK_TOTAL="$(count_mock_total)"
if [[ "${RUN_WORKER}" == "1" ]]; then
	if [[ -z "${MOCK_TOTAL}" ]] || (( MOCK_TOTAL < 2 )); then
		fail_l3_notice "MockCallbackCount" "mock callback total expected >=2, got=${MOCK_TOTAL:-EMPTY}"
	fi
fi

echo "PASS L3-NOTICE"
echo "channel_id=${CHANNEL_ID}"
echo "incident_id=${INCIDENT_ID}"
echo "job_id=${JOB_ID}"
echo "run_worker=${RUN_WORKER}"
echo "delivery_count=${DELIVERY_COUNT}"
