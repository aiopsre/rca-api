#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
DEBUG="${DEBUG:-0}"
MOCK_PORT="${MOCK_PORT:-}"
MOCK_ENDPOINT_HOST="${MOCK_ENDPOINT_HOST:-}"
MOCK_ENDPOINT_HOSTS="${MOCK_ENDPOINT_HOSTS:-}"
MISMATCH_BLACKHOLE_ENDPOINT="${MISMATCH_BLACKHOLE_ENDPOINT:-http://127.0.0.1:1/webhook}"
WAIT_TIMEOUT_SEC="${WAIT_TIMEOUT_SEC:-90}"
ALLOW_CONCURRENT_WORKERS="${ALLOW_CONCURRENT_WORKERS:-0}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
WORKER_CONFIG="${WORKER_CONFIG:-${REPO_ROOT}/configs/rca-apiserver.yaml}"
WORKER_CMD="${WORKER_CMD:-GOLANG_PROTOBUF_REGISTRATION_CONFLICT=warn go run ./cmd/rca-apiserver --config ${WORKER_CONFIG} notice-worker --notice-worker-poll-interval=200ms --notice-worker-batch-size=8 --notice-worker-lock-timeout=5s}"

LAST_HTTP_CODE=""
LAST_BODY=""

CHANNEL_ID=""
INCIDENT_ID=""
INCIDENT_SUCCESS_ID=""
DELIVERY_ID=""
DELIVERY_SUCCESS_ID=""
DELIVERY_IDEMPOTENCY_KEY=""
SUCCESS_IDEMPOTENCY_KEY=""
ATTEMPTS="0"
DELIVERY_STATUS=""
DELIVERY_ERROR=""
MOCK_SUCCESS_HITS="0"
MOCK_MISMATCH_BEFORE_REPLAY_HITS="0"
MOCK_MISMATCH_AFTER_REPLAY_HITS="0"

MOCK_EVENTS_FILE="$(mktemp)"
MOCK_LOG_FILE="$(mktemp)"
WORKER_LOG_FILE="$(mktemp)"
MOCK_PID=""
WORKER_PID=""
PYBIN=""
CURRENT_ENDPOINT=""
CURRENT_ENDPOINT_HOST=""
CURRENT_ENDPOINT_INDEX=0
declare -a ENDPOINT_HOST_CANDIDATES=()

debug() {
	if [[ "${DEBUG}" == "1" ]]; then
		echo "[DEBUG] $*" >&2
	fi
}

need_cmd() {
	command -v "$1" >/dev/null 2>&1
}

resolve_python_bin() {
	if [[ -n "${PYBIN}" ]]; then
		return 0
	fi
	if need_cmd python3; then
		PYBIN="python3"
		return 0
	fi
	if need_cmd python; then
		PYBIN="python"
		return 0
	fi
	LAST_HTTP_CODE="PYTHON_NOT_FOUND"
	LAST_BODY="python3/python not found"
	fail_l9 "ResolvePython" "python3/python not found"
}

ensure_mock_port() {
	if [[ -n "${MOCK_PORT}" ]] && [[ "${MOCK_PORT}" != "0" ]]; then
		return 0
	fi
	resolve_python_bin
	MOCK_PORT="$("${PYBIN}" - <<'PY'
import socket
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
	)"
}

init_endpoint_hosts() {
	if [[ -n "${MOCK_ENDPOINT_HOSTS}" ]]; then
		local IFS=','
		# shellcheck disable=SC2206
		ENDPOINT_HOST_CANDIDATES=(${MOCK_ENDPOINT_HOSTS})
	elif [[ -n "${MOCK_ENDPOINT_HOST}" ]]; then
		ENDPOINT_HOST_CANDIDATES=("${MOCK_ENDPOINT_HOST}")
	else
		ENDPOINT_HOST_CANDIDATES=("127.0.0.1" "host.docker.internal" "gateway.docker.internal" "host.containers.internal")
	fi
	CURRENT_ENDPOINT_INDEX=0
	CURRENT_ENDPOINT_HOST="${ENDPOINT_HOST_CANDIDATES[${CURRENT_ENDPOINT_INDEX}]}"
	CURRENT_ENDPOINT="http://${CURRENT_ENDPOINT_HOST}:${MOCK_PORT}/webhook"
}

has_next_endpoint_host() {
	local next_index=$((CURRENT_ENDPOINT_INDEX + 1))
	(( next_index < ${#ENDPOINT_HOST_CANDIDATES[@]} ))
}

move_to_next_endpoint_host() {
	if ! has_next_endpoint_host; then
		return 1
	fi
	CURRENT_ENDPOINT_INDEX=$((CURRENT_ENDPOINT_INDEX + 1))
	CURRENT_ENDPOINT_HOST="${ENDPOINT_HOST_CANDIDATES[${CURRENT_ENDPOINT_INDEX}]}"
	CURRENT_ENDPOINT="http://${CURRENT_ENDPOINT_HOST}:${MOCK_PORT}/webhook"
	return 0
}

list_notice_worker_processes() {
	ps -eo pid=,command= | awk '/notice-worker/ && $0 !~ /awk/ {print}'
}

precheck_notice_workers_or_fail() {
	if [[ "${ALLOW_CONCURRENT_WORKERS}" == "1" ]]; then
		return 0
	fi
	local workers
	workers="$(list_notice_worker_processes || true)"
	if [[ -z "${workers}" ]]; then
		return 0
	fi
	LAST_HTTP_CODE="CONCURRENT_WORKERS"
	LAST_BODY="${workers}"
	fail_l9 "PrecheckWorkers" "detected running notice-worker processes; stop them or set ALLOW_CONCURRENT_WORKERS=1"
}

trim_2kb() {
	printf '%s' "$1" | head -c 2048
}

fail_l9() {
	local step="$1"
	local detail="${2:-non-2xx response}"
	local code="${3:-${LAST_HTTP_CODE}}"
	local body="${4:-${LAST_BODY}}"

	echo "FAIL L9-NOTICE-SECRET-MISMATCH step=${step}"
	echo "detail=${detail}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	trim_2kb "${body}"
	echo
	echo "EOF"
	echo "channel_id=${CHANNEL_ID:-NONE}"
	echo "incident_success_id=${INCIDENT_SUCCESS_ID:-NONE}"
	echo "incident_mismatch_id=${INCIDENT_ID:-NONE}"
	echo "delivery_success_id=${DELIVERY_SUCCESS_ID:-NONE}"
	echo "delivery_mismatch_id=${DELIVERY_ID:-NONE}"
	echo "delivery_success_idempotency_key=${SUCCESS_IDEMPOTENCY_KEY:-NONE}"
	echo "delivery_mismatch_idempotency_key=${DELIVERY_IDEMPOTENCY_KEY:-NONE}"
	echo "delivery_error=${DELIVERY_ERROR:-NONE}"
	echo "endpoint_host=${CURRENT_ENDPOINT_HOST:-NONE}"
	echo "endpoint_url=${CURRENT_ENDPOINT:-NONE}"
	echo "mock_success_hits=${MOCK_SUCCESS_HITS:-0}"
	echo "mock_mismatch_before_replay_hits=${MOCK_MISMATCH_BEFORE_REPLAY_HITS:-0}"
	echo "mock_mismatch_after_replay_hits=${MOCK_MISMATCH_AFTER_REPLAY_HITS:-0}"
	echo "attempts=${ATTEMPTS:-0}"
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
		fail_l9 "${step}" "curl failed"
	fi
	if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
		fail_l9 "${step}"
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
					 .noticeDelivery[$k] // .data.noticeDelivery[$k]) |
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

extract_delivery_id_from_list() {
	local json="$1"
	if need_cmd jq; then
		printf '%s' "${json}" | jq -r '((.noticeDeliveries // .data.noticeDeliveries // []) | .[0].deliveryID // empty)' 2>/dev/null || true
		return
	fi
	extract_field "${json}" "deliveryID" "delivery_id" || true
}

count_mock_path_by_key() {
	local path="$1"
	local idem_key="$2"
	if [[ -z "${idem_key}" ]] || [[ ! -s "${MOCK_EVENTS_FILE}" ]]; then
		echo "0"
		return
	fi
	if need_cmd jq; then
		jq -r --arg p "${path}" --arg k "${idem_key}" 'select((.path // "") == $p and (.idempotency_key // "") == $k) | .path' "${MOCK_EVENTS_FILE}" 2>/dev/null | wc -l | tr -d ' '
	else
		grep "\"path\"[[:space:]]*:[[:space:]]*\"${path}\"" "${MOCK_EVENTS_FILE}" | grep -c "\"idempotency_key\"[[:space:]]*:[[:space:]]*\"${idem_key}\"" || true
	fi
}

assert_worker_alive() {
	if [[ -n "${WORKER_PID}" ]] && ! kill -0 "${WORKER_PID}" >/dev/null 2>&1; then
		LAST_HTTP_CODE="WORKER_EXITED"
		LAST_BODY="$(cat "${WORKER_LOG_FILE}" 2>/dev/null || true)"
		fail_l9 "$1" "worker exited unexpectedly"
	fi
}

assert_mock_alive() {
	if [[ -n "${MOCK_PID}" ]] && ! kill -0 "${MOCK_PID}" >/dev/null 2>&1; then
		LAST_HTTP_CODE="MOCK_EXITED"
		LAST_BODY="$(cat "${MOCK_LOG_FILE}" 2>/dev/null || true)"
		fail_l9 "$1" "mock webhook exited unexpectedly"
	fi
}

stop_worker() {
	if [[ -n "${WORKER_PID}" ]]; then
		kill "${WORKER_PID}" >/dev/null 2>&1 || true
		wait "${WORKER_PID}" >/dev/null 2>&1 || true
		WORKER_PID=""
	fi
}

cleanup() {
	stop_worker
	if [[ -n "${MOCK_PID}" ]]; then
		kill "${MOCK_PID}" >/dev/null 2>&1 || true
		wait "${MOCK_PID}" >/dev/null 2>&1 || true
	fi
	rm -f "${MOCK_EVENTS_FILE}" "${MOCK_LOG_FILE}" "${WORKER_LOG_FILE}"
}
trap cleanup EXIT

start_mock() {
	resolve_python_bin
	ensure_mock_port
	init_endpoint_hosts

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
        if self.path != "/webhook":
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

server = ThreadingHTTPServer(("0.0.0.0", port), Handler)
server.serve_forever()
PY
	MOCK_PID="$!"
	sleep 0.2
	assert_mock_alive "StartMock"

	local deadline now
	deadline="$(( $(date +%s) + 10 ))"
	while true; do
		assert_mock_alive "StartMock"
		if "${CURL}" -sS "http://127.0.0.1:${MOCK_PORT}/healthz" >/dev/null 2>&1; then
			return 0
		fi
		now="$(date +%s)"
		if (( now > deadline )); then
			LAST_HTTP_CODE="MOCK_START_TIMEOUT"
			LAST_BODY="$(cat "${MOCK_LOG_FILE}" 2>/dev/null || true)"
			fail_l9 "StartMock" "mock webhook start timeout"
		fi
		sleep 0.5
	done
}

start_worker() {
	local worker_id="p1-l9-notice-worker-${rand}-$$"
	(
		cd "${REPO_ROOT}" && bash -lc "${WORKER_CMD} --notice-worker-id ${worker_id}"
	) >"${WORKER_LOG_FILE}" 2>&1 &
	WORKER_PID="$!"
	sleep 0.3
	if ! kill -0 "${WORKER_PID}" >/dev/null 2>&1; then
		LAST_HTTP_CODE="WORKER_EXITED"
		LAST_BODY="$(cat "${WORKER_LOG_FILE}" 2>/dev/null || true)"
		fail_l9 "StartWorker" "worker exited immediately"
	fi
}

wait_delivery_status_or_fail() {
	local step="$1"
	local delivery_id="$2"
	local expect_status="$3"
	local min_attempts="$4"
	local deadline status attempts
	deadline="$(( $(date +%s) + WAIT_TIMEOUT_SEC ))"

	while true; do
		assert_worker_alive "${step}"
		assert_mock_alive "${step}"
		call_or_fail "${step}GetDelivery" GET "${BASE_URL}/v1/notice-deliveries/${delivery_id}"
		status="$(extract_field "${LAST_BODY}" "status" || true)"
		attempts="$(extract_field "${LAST_BODY}" "attempts" || true)"
		[[ -z "${attempts}" ]] && attempts="0"

		if [[ "${status}" == "${expect_status}" ]] && [[ "${attempts}" =~ ^[0-9]+$ ]] && (( attempts >= min_attempts )); then
			DELIVERY_STATUS="${status}"
			ATTEMPTS="${attempts}"
			DELIVERY_ERROR="$(extract_field "${LAST_BODY}" "error" || true)"
			return 0
		fi

		if (( $(date +%s) > deadline )); then
			fail_l9 "${step}" "expect status=${expect_status}, got=${status:-EMPTY}, attempts=${attempts:-EMPTY}"
		fi
		sleep 1
	done
}

wait_delivery_terminal_or_fail() {
	local step="$1"
	local delivery_id="$2"
	local min_attempts="$3"
	local deadline status attempts
	deadline="$(( $(date +%s) + WAIT_TIMEOUT_SEC ))"

	while true; do
		assert_worker_alive "${step}"
		assert_mock_alive "${step}"
		call_or_fail "${step}GetDelivery" GET "${BASE_URL}/v1/notice-deliveries/${delivery_id}"
		status="$(extract_field "${LAST_BODY}" "status" || true)"
		attempts="$(extract_field "${LAST_BODY}" "attempts" || true)"
		[[ -z "${attempts}" ]] && attempts="0"

		if [[ "${status}" =~ ^(succeeded|failed)$ ]] && [[ "${attempts}" =~ ^[0-9]+$ ]] && (( attempts >= min_attempts )); then
			DELIVERY_STATUS="${status}"
			ATTEMPTS="${attempts}"
			DELIVERY_ERROR="$(extract_field "${LAST_BODY}" "error" || true)"
			return 0
		fi

		if (( $(date +%s) > deadline )); then
			fail_l9 "${step}" "expect terminal status succeeded|failed, got=${status:-EMPTY}, attempts=${attempts:-EMPTY}"
		fi
		sleep 1
	done
}

patch_channel_endpoint_or_fail() {
	local step="$1"
	local endpoint="$2"
	local patch_channel_body
	patch_channel_body=$(cat <<EOF_JSON
{"endpointURL":"${endpoint}"}
EOF_JSON
)
	call_or_fail "${step}" PATCH "${BASE_URL}/v1/notice-channels/${CHANNEL_ID}" "${patch_channel_body}"
}

replay_latest_or_fail() {
	local step="$1"
	local delivery_id="$2"
	call_or_fail "${step}" POST "${BASE_URL}/v1/notice-deliveries/${delivery_id}:replay?useLatestChannel=1" '{}'
	local replay_status replay_attempts
	replay_status="$(extract_field "${LAST_BODY}" "status" || true)"
	replay_attempts="$(extract_field "${LAST_BODY}" "attempts" || true)"
	if [[ "${replay_status}" != "pending" ]]; then
		fail_l9 "${step}AssertStatus" "replay status expected pending, got=${replay_status:-EMPTY}"
	fi
	if [[ -z "${replay_attempts}" ]]; then
		replay_attempts="0"
	fi
	if (( replay_attempts != 0 )); then
		fail_l9 "${step}AssertAttempts" "replay attempts expected 0, got=${replay_attempts}"
	fi
}

ensure_delivery_succeeded_with_endpoint_fallback() {
	local step="$1"
	local delivery_id="$2"
	while true; do
		wait_delivery_terminal_or_fail "${step}" "${delivery_id}" 1
		if [[ "${DELIVERY_STATUS}" == "succeeded" ]]; then
			return 0
		fi
		if ! is_connectivity_delivery_error "${DELIVERY_ERROR}"; then
			fail_l9 "${step}" "delivery failed by non-connectivity error: ${DELIVERY_ERROR:-EMPTY}"
		fi
		if ! move_to_next_endpoint_host; then
			fail_l9 "${step}" "delivery failed and no fallback endpoint host remains (last=${CURRENT_ENDPOINT_HOST}, error=${DELIVERY_ERROR})"
		fi
		patch_channel_endpoint_or_fail "${step}PatchEndpointFallback" "${CURRENT_ENDPOINT}"
		replay_latest_or_fail "${step}ReplayAfterFallback" "${delivery_id}"
	done
}

is_connectivity_delivery_error() {
	local err_text="$1"
	[[ "${err_text}" == *"connect: connection refused"* ]] ||
		[[ "${err_text}" == *"connection reset by peer"* ]] ||
		[[ "${err_text}" == *"i/o timeout"* ]] ||
		[[ "${err_text}" == *"no route to host"* ]] ||
		[[ "${err_text}" == *"context deadline exceeded"* ]]
}

start_mock
precheck_notice_workers_or_fail

rand="${RAND:-$RANDOM}"
now_epoch="$(date -u +%s)"
secret_s1="p1-l9-secret-s1-${rand}"
secret_s2="p1-l9-secret-s2-${rand}"

create_channel_body=$(cat <<EOF_JSON
{"name":"p1-l9-notice-secret-mismatch-${rand}","type":"webhook","enabled":true,"endpointURL":"${CURRENT_ENDPOINT}","timeoutMs":1200,"maxRetries":3,"secret":"${secret_s1}"}
EOF_JSON
)
call_or_fail "CreateNoticeChannel" POST "${BASE_URL}/v1/notice-channels" "${create_channel_body}"
CHANNEL_ID="$(extract_field "${LAST_BODY}" "channelID" "channel_id" || true)"
if [[ -z "${CHANNEL_ID}" ]]; then
	fail_l9 "CreateNoticeChannelParse" "channel_id missing"
fi

ingest_success_body=$(cat <<EOF_JSON
{"idempotencyKey":"idem-p1-l9-notice-success-${rand}","fingerprint":"p1-l9-notice-success-fp-${rand}","status":"firing","severity":"P1","service":"notice-l9-success","cluster":"prod-l9","namespace":"default","workload":"checkout-api","lastSeenAt":{"seconds":${now_epoch},"nanos":0}}
EOF_JSON
)
call_or_fail "IngestAlertEventSuccess" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_success_body}"
INCIDENT_SUCCESS_ID="$(extract_field "${LAST_BODY}" "incidentID" "incident_id" || true)"
if [[ -z "${INCIDENT_SUCCESS_ID}" ]]; then
	fail_l9 "IngestAlertEventSuccessParse" "incident_id missing"
fi

call_or_fail "ListDeliverySuccess" GET "${BASE_URL}/v1/notice-deliveries?incident_id=${INCIDENT_SUCCESS_ID}&channel_id=${CHANNEL_ID}&event_type=incident_created&offset=0&limit=20"
DELIVERY_SUCCESS_ID="$(extract_delivery_id_from_list "${LAST_BODY}")"
if [[ -z "${DELIVERY_SUCCESS_ID}" ]]; then
	fail_l9 "ListDeliverySuccessParse" "delivery_id missing"
fi

call_or_fail "GetDeliverySuccessInitial" GET "${BASE_URL}/v1/notice-deliveries/${DELIVERY_SUCCESS_ID}"
SUCCESS_IDEMPOTENCY_KEY="$(extract_field "${LAST_BODY}" "idempotencyKey" "idempotency_key" || true)"
if [[ -z "${SUCCESS_IDEMPOTENCY_KEY}" ]]; then
	fail_l9 "GetDeliverySuccessInitialParse" "idempotency_key missing"
fi

start_worker
ensure_delivery_succeeded_with_endpoint_fallback "WaitDeliverySuccessS1" "${DELIVERY_SUCCESS_ID}"
MOCK_SUCCESS_HITS="$(count_mock_path_by_key "/webhook" "${SUCCESS_IDEMPOTENCY_KEY}")"
if [[ ! "${MOCK_SUCCESS_HITS}" =~ ^[0-9]+$ ]] || (( MOCK_SUCCESS_HITS < 1 )); then
	fail_l9 "AssertSuccessHit" "mock success count expected >=1, got=${MOCK_SUCCESS_HITS:-EMPTY}"
fi
stop_worker

patch_channel_endpoint_or_fail "PatchChannelToBlackholeBeforeMismatch" "${MISMATCH_BLACKHOLE_ENDPOINT}"

ingest_mismatch_body=$(cat <<EOF_JSON
{"idempotencyKey":"idem-p1-l9-notice-mismatch-${rand}","fingerprint":"p1-l9-notice-mismatch-fp-${rand}","status":"firing","severity":"P1","service":"notice-l9-mismatch","cluster":"prod-l9","namespace":"default","workload":"checkout-api","lastSeenAt":{"seconds":$((now_epoch+1)),"nanos":0}}
EOF_JSON
)
call_or_fail "IngestAlertEventMismatch" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_mismatch_body}"
INCIDENT_ID="$(extract_field "${LAST_BODY}" "incidentID" "incident_id" || true)"
if [[ -z "${INCIDENT_ID}" ]]; then
	fail_l9 "IngestAlertEventMismatchParse" "incident_id missing"
fi

call_or_fail "ListDeliveryMismatch" GET "${BASE_URL}/v1/notice-deliveries?incident_id=${INCIDENT_ID}&channel_id=${CHANNEL_ID}&event_type=incident_created&offset=0&limit=20"
DELIVERY_ID="$(extract_delivery_id_from_list "${LAST_BODY}")"
if [[ -z "${DELIVERY_ID}" ]]; then
	fail_l9 "ListDeliveryMismatchParse" "delivery_id missing"
fi

call_or_fail "GetDeliveryMismatchInitial" GET "${BASE_URL}/v1/notice-deliveries/${DELIVERY_ID}"
DELIVERY_IDEMPOTENCY_KEY="$(extract_field "${LAST_BODY}" "idempotencyKey" "idempotency_key" || true)"
if [[ -z "${DELIVERY_IDEMPOTENCY_KEY}" ]]; then
	fail_l9 "GetDeliveryMismatchInitialParse" "idempotency_key missing"
fi

patch_channel_body=$(cat <<EOF_JSON
{"secret":"${secret_s2}"}
EOF_JSON
)
call_or_fail "PatchChannelSecretS2" PATCH "${BASE_URL}/v1/notice-channels/${CHANNEL_ID}" "${patch_channel_body}"
call_or_fail "ReplayMismatchSnapshotAfterSecretPatch" POST "${BASE_URL}/v1/notice-deliveries/${DELIVERY_ID}:replay" '{}'

start_worker
wait_delivery_status_or_fail "WaitDeliveryFailedByMismatch" "${DELIVERY_ID}" "failed" 1
if [[ "${DELIVERY_ERROR}" != *"secret_fingerprint_mismatch"* ]]; then
	fail_l9 "AssertMismatchKeyword" "delivery error missing secret_fingerprint_mismatch: ${DELIVERY_ERROR:-EMPTY}"
fi
if [[ "${DELIVERY_ERROR}" != *"useLatestChannel=1"* ]]; then
	fail_l9 "AssertReplayHintKeyword" "delivery error missing useLatestChannel=1: ${DELIVERY_ERROR:-EMPTY}"
fi
MOCK_MISMATCH_BEFORE_REPLAY_HITS="$(count_mock_path_by_key "/webhook" "${DELIVERY_IDEMPOTENCY_KEY}")"
if [[ ! "${MOCK_MISMATCH_BEFORE_REPLAY_HITS}" =~ ^[0-9]+$ ]] || (( MOCK_MISMATCH_BEFORE_REPLAY_HITS != 0 )); then
	fail_l9 "AssertFailFastNoSend" "mismatch delivery should not be sent before replay, got=${MOCK_MISMATCH_BEFORE_REPLAY_HITS:-EMPTY}"
fi

patch_channel_endpoint_or_fail "PatchChannelBackToReachableBeforeReplayLatest" "${CURRENT_ENDPOINT}"
replay_latest_or_fail "ReplayUseLatestChannel" "${DELIVERY_ID}"

wait_delivery_status_or_fail "WaitDeliverySucceededAfterReplay" "${DELIVERY_ID}" "succeeded" 1
MOCK_MISMATCH_AFTER_REPLAY_HITS="$(count_mock_path_by_key "/webhook" "${DELIVERY_IDEMPOTENCY_KEY}")"
if [[ ! "${MOCK_MISMATCH_AFTER_REPLAY_HITS}" =~ ^[0-9]+$ ]] || (( MOCK_MISMATCH_AFTER_REPLAY_HITS < 1 )); then
	fail_l9 "AssertReplaySendSuccess" "replay delivery should be sent after replay, got=${MOCK_MISMATCH_AFTER_REPLAY_HITS:-EMPTY}"
fi

stop_worker

echo "PASS L9-NOTICE-SECRET-MISMATCH"
echo "channel_id=${CHANNEL_ID}"
echo "incident_success_id=${INCIDENT_SUCCESS_ID}"
echo "incident_mismatch_id=${INCIDENT_ID}"
echo "delivery_success_id=${DELIVERY_SUCCESS_ID}"
echo "delivery_mismatch_id=${DELIVERY_ID}"
echo "delivery_success_idempotency_key=${SUCCESS_IDEMPOTENCY_KEY}"
echo "delivery_mismatch_idempotency_key=${DELIVERY_IDEMPOTENCY_KEY}"
echo "endpoint_host=${CURRENT_ENDPOINT_HOST}"
echo "endpoint_url=${CURRENT_ENDPOINT}"
echo "mock_success_hits=${MOCK_SUCCESS_HITS}"
echo "mock_mismatch_before_replay_hits=${MOCK_MISMATCH_BEFORE_REPLAY_HITS}"
echo "mock_mismatch_after_replay_hits=${MOCK_MISMATCH_AFTER_REPLAY_HITS}"
echo "attempts=${ATTEMPTS}"
