#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
DEBUG="${DEBUG:-0}"
MOCK_PORT="${MOCK_PORT:-19102}"
MOCK_ENDPOINT_HOST="${MOCK_ENDPOINT_HOST:-}"
MOCK_ENDPOINT_HOSTS="${MOCK_ENDPOINT_HOSTS:-}"
MOCK_FAIL_FIRST_ATTEMPTS="${MOCK_FAIL_FIRST_ATTEMPTS:-1}"
MOCK_WEBHOOK_PATH="${MOCK_WEBHOOK_PATH:-}"
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
DELIVERY_ID=""
DELIVERY_IDEMPOTENCY_KEY=""
ATTEMPTS="0"
DELIVERY_STATUS=""
DELIVERY_ERROR=""
FIRST_NONCE=""
SECOND_NONCE=""
FIRST_SIGNATURE=""
SECOND_SIGNATURE=""

rand="${RAND:-$RANDOM}"
MOCK_EVENTS_FILE="$(mktemp)"
MOCK_LOG_FILE="$(mktemp)"
WORKER_LOG_FILE="$(mktemp)"
MOCK_PID=""
WORKER_PID=""
WORKER_ID=""
PYBIN=""
CURRENT_ENDPOINT_HOST=""
CURRENT_ENDPOINT=""
CURRENT_ENDPOINT_INDEX=0
WORKER_NO_PROXY=""
declare -a ENDPOINT_HOST_CANDIDATES=()

debug() {
	if [[ "${DEBUG}" == "1" ]]; then
		echo "[DEBUG] $*" >&2
	fi
}

trim_2kb() {
	printf '%s' "$1" | head -c 2048
}

fail_l12() {
	local step="$1"
	local detail="${2:-non-2xx response}"
	local code="${3:-${LAST_HTTP_CODE}}"
	local body="${4:-${LAST_BODY}}"

	echo "FAIL L12-NOTICE-WEBHOOK-SIGNATURE step=${step}"
	echo "detail=${detail}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	trim_2kb "${body}"
	echo
	echo "EOF"
	echo "channel_id=${CHANNEL_ID:-NONE}"
	echo "incident_id=${INCIDENT_ID:-NONE}"
	echo "delivery_id=${DELIVERY_ID:-NONE}"
	echo "delivery_idempotency_key=${DELIVERY_IDEMPOTENCY_KEY:-NONE}"
	echo "first_nonce=${FIRST_NONCE:-NONE}"
	echo "second_nonce=${SECOND_NONCE:-NONE}"
	echo "first_signature=${FIRST_SIGNATURE:-NONE}"
	echo "second_signature=${SECOND_SIGNATURE:-NONE}"
	echo "attempts=${ATTEMPTS:-0}"
	echo "delivery_status=${DELIVERY_STATUS:-NONE}"
	echo "delivery_error=${DELIVERY_ERROR:-NONE}"
	echo "endpoint_host=${CURRENT_ENDPOINT_HOST:-NONE}"
	echo "endpoint_url=${CURRENT_ENDPOINT:-NONE}"
	echo "endpoint_candidates=${ENDPOINT_HOST_CANDIDATES[*]:-NONE}"
	exit 1
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
	fail_l12 "PrecheckTools" "python3/python not found"
}

init_endpoint_hosts() {
	local addrs=()
	local user_supplied=0
	local base_host=""
	base_host="$(printf '%s' "${BASE_URL}" | sed -E 's#^[a-zA-Z]+://##; s#/.*$##; s#:[0-9]+$##' | tr -d '[:space:]')"
	if [[ -n "${base_host}" ]]; then
		addrs+=("${base_host}")
	fi

	if need_cmd hostname; then
		local hn
		hn="$(hostname 2>/dev/null || true)"
		if [[ -n "${hn}" ]]; then
			addrs+=("${hn}")
		fi
	fi
	if need_cmd hostname; then
		local -a hni=()
		# shellcheck disable=SC2207
		hni=($(hostname -I 2>/dev/null || true))
		if [[ ${#hni[@]} -gt 0 ]]; then
			addrs+=("${hni[@]}")
		fi
	fi
	if need_cmd ifconfig; then
		local -a if_addrs=()
		# shellcheck disable=SC2207
		if_addrs=($(ifconfig 2>/dev/null | awk '/inet /{print $2}' | grep -vE '^(127\\.|0\\.0\\.0\\.0)$' || true))
		if [[ ${#if_addrs[@]} -gt 0 ]]; then
			addrs+=("${if_addrs[@]}")
		fi
	fi

	if [[ -n "${MOCK_ENDPOINT_HOSTS}" ]]; then
		user_supplied=1
		local IFS=','
		# shellcheck disable=SC2206
		ENDPOINT_HOST_CANDIDATES=(${MOCK_ENDPOINT_HOSTS})
	elif [[ -n "${MOCK_ENDPOINT_HOST}" ]]; then
		user_supplied=1
		ENDPOINT_HOST_CANDIDATES=("${MOCK_ENDPOINT_HOST}")
	else
		ENDPOINT_HOST_CANDIDATES=("127.0.0.1" "localhost" "host.docker.internal" "gateway.docker.internal" "host.containers.internal")
		if [[ ${#addrs[@]} -gt 0 ]]; then
			ENDPOINT_HOST_CANDIDATES+=("${addrs[@]}")
		fi
	fi
	if (( user_supplied == 1 )); then
		ENDPOINT_HOST_CANDIDATES+=("127.0.0.1" "localhost" "host.docker.internal" "gateway.docker.internal" "host.containers.internal")
		if [[ ${#addrs[@]} -gt 0 ]]; then
			ENDPOINT_HOST_CANDIDATES+=("${addrs[@]}")
		fi
	fi

	# de-duplicate while keeping order
	local -a dedup=()
	local seen="|"
	local item
	for item in "${ENDPOINT_HOST_CANDIDATES[@]}"; do
		item="$(printf '%s' "${item}" | tr -d '[:space:]')"
		if [[ -z "${item}" ]]; then
			continue
		fi
		if [[ "${seen}" == *"|${item}|"* ]]; then
			continue
		fi
		seen="${seen}${item}|"
		dedup+=("${item}")
	done
	if [[ ${#dedup[@]} -eq 0 ]]; then
		dedup=("127.0.0.1")
	fi
	ENDPOINT_HOST_CANDIDATES=("${dedup[@]}")
	CURRENT_ENDPOINT_INDEX=0
	CURRENT_ENDPOINT_HOST="${ENDPOINT_HOST_CANDIDATES[${CURRENT_ENDPOINT_INDEX}]}"
	CURRENT_ENDPOINT="http://${CURRENT_ENDPOINT_HOST}:${MOCK_PORT}${MOCK_WEBHOOK_PATH}"
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
	CURRENT_ENDPOINT="http://${CURRENT_ENDPOINT_HOST}:${MOCK_PORT}${MOCK_WEBHOOK_PATH}"
	return 0
}

build_worker_no_proxy() {
	local existing="${NO_PROXY:-${no_proxy:-}}"
	local -a raw=()
	local -a dedup=()
	local seen="|"
	local item

	if [[ -n "${existing}" ]]; then
		local IFS=','
		# shellcheck disable=SC2206
		raw=(${existing})
	fi
	raw+=("127.0.0.1" "localhost")
	if [[ -n "${CURRENT_ENDPOINT_HOST}" ]]; then
		raw+=("${CURRENT_ENDPOINT_HOST}")
	fi
	if [[ ${#ENDPOINT_HOST_CANDIDATES[@]} -gt 0 ]]; then
		raw+=("${ENDPOINT_HOST_CANDIDATES[@]}")
	fi

	for item in "${raw[@]}"; do
		item="$(printf '%s' "${item}" | tr -d '[:space:]')"
		if [[ -z "${item}" ]]; then
			continue
		fi
		if [[ "${seen}" == *"|${item}|"* ]]; then
			continue
		fi
		seen="${seen}${item}|"
		dedup+=("${item}")
	done

	WORKER_NO_PROXY="$(IFS=,; printf '%s' "${dedup[*]}")"
	if [[ -z "${WORKER_NO_PROXY}" ]]; then
		WORKER_NO_PROXY="127.0.0.1,localhost"
	fi
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
	fail_l12 "PrecheckWorkers" "detected running notice-worker processes; stop them or set ALLOW_CONCURRENT_WORKERS=1"
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
		fail_l12 "${step}" "curl failed"
	fi
	if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
		fail_l12 "${step}"
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
	return 1
}

extract_delivery_id_from_list() {
	printf '%s' "${LAST_BODY}" | jq -r '
		(.noticeDeliveries // .data.noticeDeliveries // [])[] |
		select((.channelID // "") == $channel_id and (.eventType // "") == "incident_created") |
		.deliveryID
	' --arg channel_id "${CHANNEL_ID}" 2>/dev/null | head -n 1
}

assert_mock_alive() {
	if [[ -n "${MOCK_PID}" ]] && ! kill -0 "${MOCK_PID}" >/dev/null 2>&1; then
		LAST_HTTP_CODE="MOCK_EXITED"
		LAST_BODY="$(cat "${MOCK_LOG_FILE}" 2>/dev/null || true)"
		fail_l12 "$1" "mock webhook exited unexpectedly"
	fi
}

assert_worker_alive() {
	if [[ -n "${WORKER_PID}" ]] && ! kill -0 "${WORKER_PID}" >/dev/null 2>&1; then
		LAST_HTTP_CODE="WORKER_EXITED"
		LAST_BODY="$(cat "${WORKER_LOG_FILE}" 2>/dev/null || true)"
		fail_l12 "$1" "worker exited unexpectedly"
	fi
}

stop_worker() {
	if [[ -n "${WORKER_ID}" ]]; then
		local pids
		pids="$(pgrep -f -- "--notice-worker-id ${WORKER_ID}" || true)"
		if [[ -n "${pids}" ]]; then
			# shellcheck disable=SC2086
			kill ${pids} >/dev/null 2>&1 || true
		fi
	fi
	if [[ -n "${WORKER_PID}" ]]; then
		pkill -P "${WORKER_PID}" >/dev/null 2>&1 || true
		kill "${WORKER_PID}" >/dev/null 2>&1 || true
		wait "${WORKER_PID}" >/dev/null 2>&1 || true
		WORKER_PID=""
	fi
	WORKER_ID=""
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
	init_endpoint_hosts

	"${PYBIN}" -u - <<'PY' "${MOCK_PORT}" "${MOCK_EVENTS_FILE}" "${MOCK_FAIL_FIRST_ATTEMPTS}" "${MOCK_WEBHOOK_PATH}" >"${MOCK_LOG_FILE}" 2>&1 &
import json
import sys
import time
from http.server import ThreadingHTTPServer, BaseHTTPRequestHandler

port = int(sys.argv[1])
events_path = sys.argv[2]
fail_first_attempts = int(sys.argv[3])
webhook_path = sys.argv[4]
request_count = 0

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
        global request_count
        if self.path != webhook_path:
            self.send_response(404)
            self.end_headers()
            return
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length)
        request_count += 1
        forced_fail = request_count <= fail_first_attempts
        item = {
            "received_unix": int(time.time()),
            "path": self.path,
            "method": self.command,
            "body": body.decode("utf-8", errors="replace"),
            "x_rca_signature": self.headers.get("X-Rca-Signature", ""),
            "x_rca_timestamp": self.headers.get("X-Rca-Timestamp", ""),
            "x_rca_nonce": self.headers.get("X-Rca-Nonce", ""),
            "x_rca_delivery_id": self.headers.get("X-Rca-Delivery-Id", ""),
            "x_rca_event_type": self.headers.get("X-Rca-Event-Type", ""),
            "idempotency_key": self.headers.get("Idempotency-Key", ""),
            "forced_fail": forced_fail,
            "request_count": request_count,
        }
        with open(events_path, "a", encoding="utf-8") as fp:
            fp.write(json.dumps(item, ensure_ascii=False) + "\n")

        if forced_fail:
            self.send_response(500)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"ok":false,"forced_fail":true}')
            return

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
			fail_l12 "StartMock" "mock webhook start timeout"
		fi
		sleep 0.5
	done
}

start_worker() {
	local worker_id="p2-l12-notice-worker-${rand}-$$"
	WORKER_ID="${worker_id}"
	build_worker_no_proxy
	debug "StartWorker endpoint=${CURRENT_ENDPOINT} NO_PROXY=${WORKER_NO_PROXY}"
	(
		cd "${REPO_ROOT}" && NO_PROXY="${WORKER_NO_PROXY}" no_proxy="${WORKER_NO_PROXY}" bash -lc "${WORKER_CMD} --notice-worker-id ${worker_id}"
	) >"${WORKER_LOG_FILE}" 2>&1 &
	WORKER_PID="$!"
	sleep 0.3
	if ! kill -0 "${WORKER_PID}" >/dev/null 2>&1; then
		LAST_HTTP_CODE="WORKER_EXITED"
		LAST_BODY="$(cat "${WORKER_LOG_FILE}" 2>/dev/null || true)"
		fail_l12 "StartWorker" "worker exited immediately"
	fi
}

wait_delivery_exists_or_fail() {
	local deadline out
	deadline="$(( $(date +%s) + WAIT_TIMEOUT_SEC ))"
	while true; do
		call_or_fail "ListDelivery" GET "${BASE_URL}/v1/notice-deliveries?incident_id=${INCIDENT_ID}&channel_id=${CHANNEL_ID}&event_type=incident_created&offset=0&limit=20"
		out="$(extract_delivery_id_from_list)"
		if [[ -n "${out}" ]]; then
			DELIVERY_ID="${out}"
			return 0
		fi
		if (( $(date +%s) > deadline )); then
			fail_l12 "ListDeliveryParse" "delivery_id missing"
		fi
		sleep 1
	done
}

wait_delivery_status_or_fail() {
	local step="$1"
	local expect_status="$2"
	local min_attempts="$3"
	local deadline status attempts
	deadline="$(( $(date +%s) + WAIT_TIMEOUT_SEC ))"

	while true; do
		assert_worker_alive "${step}"
		assert_mock_alive "${step}"
		call_or_fail "${step}GetDelivery" GET "${BASE_URL}/v1/notice-deliveries/${DELIVERY_ID}"
		status="$(extract_field "${LAST_BODY}" "status" || true)"
		attempts="$(extract_field "${LAST_BODY}" "attempts" || true)"
		[[ -z "${attempts}" ]] && attempts="0"
		if [[ "${status}" == "${expect_status}" ]] && [[ "${attempts}" =~ ^[0-9]+$ ]] && (( attempts >= min_attempts )); then
			ATTEMPTS="${attempts}"
			DELIVERY_STATUS="${status}"
			DELIVERY_IDEMPOTENCY_KEY="$(extract_field "${LAST_BODY}" "idempotencyKey" "idempotency_key" || true)"
			DELIVERY_ERROR="$(extract_field "${LAST_BODY}" "error" || true)"
			return 0
		fi
		if (( $(date +%s) > deadline )); then
			fail_l12 "${step}" "expect status=${expect_status}, got=${status:-EMPTY}, attempts=${attempts:-EMPTY}"
		fi
		sleep 1
	done
}

wait_delivery_terminal_or_fail() {
	local step="$1"
	local min_attempts="$2"
	local deadline status attempts
	deadline="$(( $(date +%s) + WAIT_TIMEOUT_SEC ))"

	while true; do
		assert_worker_alive "${step}"
		assert_mock_alive "${step}"
		call_or_fail "${step}GetDelivery" GET "${BASE_URL}/v1/notice-deliveries/${DELIVERY_ID}"
		status="$(extract_field "${LAST_BODY}" "status" || true)"
		attempts="$(extract_field "${LAST_BODY}" "attempts" || true)"
		[[ -z "${attempts}" ]] && attempts="0"

		if [[ "${status}" =~ ^(succeeded|failed)$ ]] && [[ "${attempts}" =~ ^[0-9]+$ ]] && (( attempts >= min_attempts )); then
			ATTEMPTS="${attempts}"
			DELIVERY_STATUS="${status}"
			DELIVERY_IDEMPOTENCY_KEY="$(extract_field "${LAST_BODY}" "idempotencyKey" "idempotency_key" || true)"
			DELIVERY_ERROR="$(extract_field "${LAST_BODY}" "error" || true)"
			return 0
		fi

		if (( $(date +%s) > deadline )); then
			fail_l12 "${step}" "expect terminal status succeeded|failed, got=${status:-EMPTY}, attempts=${attempts:-EMPTY}"
		fi
		sleep 1
	done
}

count_mock_attempts_by_delivery() {
	if [[ ! -s "${MOCK_EVENTS_FILE}" ]]; then
		echo "0"
		return
	fi
	jq -r --arg d "${DELIVERY_ID}" 'select((.x_rca_delivery_id // "") == $d) | .x_rca_delivery_id' "${MOCK_EVENTS_FILE}" 2>/dev/null | wc -l | tr -d ' '
}

wait_mock_attempts_or_fail() {
	local step="$1"
	local min_count="$2"
	local deadline count
	deadline="$(( $(date +%s) + WAIT_TIMEOUT_SEC ))"
	while true; do
		assert_mock_alive "${step}"
		count="$(count_mock_attempts_by_delivery)"
		if [[ "${count}" =~ ^[0-9]+$ ]] && (( count >= min_count )); then
			return 0
		fi
		if (( $(date +%s) > deadline )); then
			fail_l12 "${step}" "expect mock attempts>=${min_count}, got=${count:-EMPTY}"
		fi
		sleep 1
	done
}

get_mock_attempt_json_or_fail() {
	local index="$1"
	local out
	out="$(jq -c --arg d "${DELIVERY_ID}" 'select((.x_rca_delivery_id // "") == $d)' "${MOCK_EVENTS_FILE}" 2>/dev/null | sed -n "${index}p")"
	if [[ -z "${out}" ]]; then
		fail_l12 "GetMockAttempt${index}" "mock attempt json missing for index=${index}" "ASSERT_FAIL"
	fi
	printf '%s' "${out}"
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

replay_latest_try() {
	local step="$1"
	local max_try="${2:-3}"
	local n replay_status replay_attempts current_status

	for ((n=1; n<=max_try; n++)); do
		if http_json "POST" "${BASE_URL}/v1/notice-deliveries/${DELIVERY_ID}:replay?useLatestChannel=1" '{}'; then
			if [[ "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
				replay_status="$(extract_field "${LAST_BODY}" "status" || true)"
				replay_attempts="$(extract_field "${LAST_BODY}" "attempts" || true)"
				if [[ "${replay_status}" == "pending" ]]; then
					if [[ -z "${replay_attempts}" ]]; then
						replay_attempts="0"
					fi
					if (( replay_attempts == 0 )); then
						return 0
					fi
				fi
			fi
		fi

		# The replay call can fail with transient 500; check current status before retrying.
		if http_json "GET" "${BASE_URL}/v1/notice-deliveries/${DELIVERY_ID}"; then
			if [[ "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
				current_status="$(extract_field "${LAST_BODY}" "status" || true)"
				if [[ "${current_status}" == "pending" || "${current_status}" == "succeeded" ]]; then
					return 0
				fi
			fi
		fi
		sleep 0.4
	done
	return 1
}

is_connectivity_delivery_error() {
	local err_text="$1"
	[[ "${err_text}" == *"connect: connection refused"* ]] ||
		[[ "${err_text}" == *"connection reset by peer"* ]] ||
		[[ "${err_text}" == *"i/o timeout"* ]] ||
		[[ "${err_text}" == *"no route to host"* ]] ||
		[[ "${err_text}" == *"context deadline exceeded"* ]] ||
		[[ "${err_text}" == *"dial tcp"* ]]
}

ensure_delivery_succeeded_with_endpoint_fallback() {
	local step="$1"
	while true; do
		wait_delivery_terminal_or_fail "${step}" 1
		if [[ "${DELIVERY_STATUS}" == "succeeded" ]]; then
			return 0
		fi
		if ! is_connectivity_delivery_error "${DELIVERY_ERROR}"; then
			fail_l12 "${step}" "delivery failed by non-connectivity error: ${DELIVERY_ERROR:-EMPTY}"
		fi
		if ! move_to_next_endpoint_host; then
			fail_l12 "${step}" "delivery failed and no fallback endpoint host remains (last=${CURRENT_ENDPOINT_HOST}, error=${DELIVERY_ERROR})"
		fi
		patch_channel_endpoint_or_fail "${step}PatchEndpointFallback" "${CURRENT_ENDPOINT}"
		if ! replay_latest_try "${step}ReplayAfterFallback" 3; then
			# Keep trying with next host candidate if replay(useLatestChannel=1) still fails.
			continue
		fi
	done
}

ensure_first_failed_attempt_with_headers_or_fallback() {
	local step="$1"
	local count
	while true; do
		wait_delivery_terminal_or_fail "${step}" 1
		count="$(count_mock_attempts_by_delivery)"
		if [[ ! "${count}" =~ ^[0-9]+$ ]]; then
			count="0"
		fi

		if [[ "${DELIVERY_STATUS}" == "failed" && "${count}" =~ ^[0-9]+$ ]] && (( count >= 1 )); then
			return 0
		fi

		# No mock records usually means endpoint is unreachable from worker.
		if [[ "${DELIVERY_STATUS}" == "failed" ]] && (( count == 0 )) && is_connectivity_delivery_error "${DELIVERY_ERROR}"; then
			if ! move_to_next_endpoint_host; then
				fail_l12 "${step}" "delivery failed by connectivity and no fallback endpoint host remains (last=${CURRENT_ENDPOINT_HOST}, error=${DELIVERY_ERROR})"
			fi
			patch_channel_endpoint_or_fail "${step}PatchEndpointFallback" "${CURRENT_ENDPOINT}"
			if ! replay_latest_try "${step}ReplayAfterFallback" 3; then
				continue
			fi
			continue
		fi

		if [[ "${DELIVERY_STATUS}" == "succeeded" ]]; then
			fail_l12 "${step}" "delivery became succeeded before replay path; expected first attempt failed (check MOCK_FAIL_FIRST_ATTEMPTS and competing deliveries on same endpoint path)"
		fi

		fail_l12 "${step}" "unexpected first-attempt state status=${DELIVERY_STATUS:-EMPTY}, mock_attempts=${count}, error=${DELIVERY_ERROR:-NONE}"
	done
}

compute_expected_signature() {
	local secret="$1"
	local timestamp="$2"
	local nonce="$3"
	local method="$4"
	local path="$5"
	local body="$6"
	local body_sha signing sig

	body_sha="$(printf '%s' "${body}" | openssl dgst -sha256 -r | awk '{print $1}')"
	method="$(printf '%s' "${method}" | tr '[:lower:]' '[:upper:]')"
	signing="$(printf 'v1\n%s\n%s\n%s\n%s\n%s' "${timestamp}" "${nonce}" "${method}" "${path}" "${body_sha}")"
	sig="$(printf '%s' "${signing}" | openssl dgst -sha256 -hmac "${secret}" -r | awk '{print $1}')"
	printf 'sha256=%s' "${sig}"
}

assert_attempt_signature_or_fail() {
	local step="$1"
	local attempt_json="$2"
	local expected_secret="$3"
	local expected_event_type="$4"
	local attempt_nonce_var="$5"
	local attempt_signature_var="$6"
	local signature timestamp nonce delivery_id_header event_type_header method path body expected_sig

	signature="$(printf '%s' "${attempt_json}" | jq -r '.x_rca_signature // ""' 2>/dev/null || true)"
	timestamp="$(printf '%s' "${attempt_json}" | jq -r '.x_rca_timestamp // ""' 2>/dev/null || true)"
	nonce="$(printf '%s' "${attempt_json}" | jq -r '.x_rca_nonce // ""' 2>/dev/null || true)"
	delivery_id_header="$(printf '%s' "${attempt_json}" | jq -r '.x_rca_delivery_id // ""' 2>/dev/null || true)"
	event_type_header="$(printf '%s' "${attempt_json}" | jq -r '.x_rca_event_type // ""' 2>/dev/null || true)"
	method="$(printf '%s' "${attempt_json}" | jq -r '.method // ""' 2>/dev/null || true)"
	path="$(printf '%s' "${attempt_json}" | jq -r '.path // ""' 2>/dev/null || true)"
	body="$(printf '%s' "${attempt_json}" | jq -r '.body // ""' 2>/dev/null || true)"

	if [[ -z "${signature}" || ! "${signature}" =~ ^sha256=[0-9a-f]{64}$ ]]; then
		fail_l12 "${step}" "invalid X-Rca-Signature format: ${signature:-EMPTY}" "ASSERT_FAIL" "${attempt_json}"
	fi
	if [[ -z "${timestamp}" || ! "${timestamp}" =~ ^[0-9]+$ ]]; then
		fail_l12 "${step}" "invalid X-Rca-Timestamp: ${timestamp:-EMPTY}" "ASSERT_FAIL" "${attempt_json}"
	fi
	if [[ -z "${nonce}" ]]; then
		fail_l12 "${step}" "X-Rca-Nonce missing" "ASSERT_FAIL" "${attempt_json}"
	fi
	if (( ${#nonce} > 128 )); then
		fail_l12 "${step}" "X-Rca-Nonce too long: len=${#nonce}" "ASSERT_FAIL" "${attempt_json}"
	fi
	if [[ "${delivery_id_header}" != "${DELIVERY_ID}" ]]; then
		fail_l12 "${step}" "X-Rca-Delivery-Id mismatch: got=${delivery_id_header:-EMPTY}" "ASSERT_FAIL" "${attempt_json}"
	fi
	if [[ "${event_type_header}" != "${expected_event_type}" ]]; then
		fail_l12 "${step}" "X-Rca-Event-Type mismatch: got=${event_type_header:-EMPTY}" "ASSERT_FAIL" "${attempt_json}"
	fi
	if [[ "${method}" != "POST" ]]; then
		fail_l12 "${step}" "method expected POST, got=${method:-EMPTY}" "ASSERT_FAIL" "${attempt_json}"
	fi
	if [[ "${path}" != "${MOCK_WEBHOOK_PATH}" ]]; then
		fail_l12 "${step}" "path expected ${MOCK_WEBHOOK_PATH}, got=${path:-EMPTY}" "ASSERT_FAIL" "${attempt_json}"
	fi

	expected_sig="$(compute_expected_signature "${expected_secret}" "${timestamp}" "${nonce}" "${method}" "${path}" "${body}")"
	if [[ "${expected_sig}" != "${signature}" ]]; then
		fail_l12 "${step}" "signature mismatch expected=${expected_sig} got=${signature}" "ASSERT_FAIL" "${attempt_json}"
	fi

	printf -v "${attempt_nonce_var}" '%s' "${nonce}"
	printf -v "${attempt_signature_var}" '%s' "${signature}"
}

if ! need_cmd jq; then
	fail_l12 "PrecheckTools" "jq not found"
fi
if ! need_cmd openssl; then
	fail_l12 "PrecheckTools" "openssl not found"
fi

if [[ -z "${MOCK_WEBHOOK_PATH}" ]]; then
	MOCK_WEBHOOK_PATH="/webhook/l12-${rand}"
fi
if [[ "${MOCK_WEBHOOK_PATH:0:1}" != "/" ]]; then
	MOCK_WEBHOOK_PATH="/${MOCK_WEBHOOK_PATH}"
fi

start_mock
precheck_notice_workers_or_fail

now_epoch="$(date -u +%s)"
secret="p2-l12-secret-${rand}"
endpoint="${CURRENT_ENDPOINT}"

create_channel_body=$(cat <<EOF_JSON
{"name":"p2-l12-notice-signature-${rand}","type":"webhook","enabled":true,"endpointURL":"${endpoint}","timeoutMs":1200,"maxRetries":1,"secret":"${secret}"}
EOF_JSON
)
call_or_fail "CreateNoticeChannel" POST "${BASE_URL}/v1/notice-channels" "${create_channel_body}"
CHANNEL_ID="$(extract_field "${LAST_BODY}" "channelID" "channel_id" || true)"
if [[ -z "${CHANNEL_ID}" ]]; then
	fail_l12 "CreateNoticeChannelParse" "channel_id missing"
fi

ingest_body=$(cat <<EOF_JSON
{"idempotencyKey":"idem-p2-l12-ingest-${rand}","fingerprint":"p2-l12-fp-${rand}","status":"firing","severity":"P1","service":"notice-l12","cluster":"prod-l12","namespace":"default","workload":"checkout-api","lastSeenAt":{"seconds":${now_epoch},"nanos":0}}
EOF_JSON
)
call_or_fail "IngestAlertEvent" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_body}"
INCIDENT_ID="$(extract_field "${LAST_BODY}" "incidentID" "incident_id" || true)"
if [[ -z "${INCIDENT_ID}" ]]; then
	fail_l12 "IngestAlertEventParse" "incident_id missing"
fi

wait_delivery_exists_or_fail
start_worker
ensure_first_failed_attempt_with_headers_or_fallback "WaitFirstDeliveryFailed"
wait_mock_attempts_or_fail "WaitFirstAttemptHeaders" 1

first_attempt_json="$(get_mock_attempt_json_or_fail 1)"
assert_attempt_signature_or_fail "AssertFirstAttemptSignature" "${first_attempt_json}" "${secret}" "incident_created" FIRST_NONCE FIRST_SIGNATURE

call_or_fail "ReplayDelivery" POST "${BASE_URL}/v1/notice-deliveries/${DELIVERY_ID}:replay" '{}'
replay_status="$(extract_field "${LAST_BODY}" "status" || true)"
if [[ "${replay_status}" != "pending" && "${replay_status}" != "succeeded" ]]; then
	fail_l12 "ReplayDeliveryStatus" "replay status expected pending|succeeded, got=${replay_status:-EMPTY}"
fi

ensure_delivery_succeeded_with_endpoint_fallback "WaitReplayDeliverySucceeded"
wait_mock_attempts_or_fail "WaitReplayAttemptHeaders" 2

second_attempt_json="$(get_mock_attempt_json_or_fail 2)"
assert_attempt_signature_or_fail "AssertReplayAttemptSignature" "${second_attempt_json}" "${secret}" "incident_created" SECOND_NONCE SECOND_SIGNATURE

if [[ "${FIRST_NONCE}" == "${SECOND_NONCE}" ]]; then
	fail_l12 "AssertNonceChanged" "nonce should change across attempts: ${FIRST_NONCE}"
fi
if [[ "${FIRST_SIGNATURE}" == "${SECOND_SIGNATURE}" ]]; then
	fail_l12 "AssertSignatureChanged" "signature should change across attempts"
fi

stop_worker

echo "PASS L12-NOTICE-WEBHOOK-SIGNATURE"
echo "channel_id=${CHANNEL_ID}"
echo "incident_id=${INCIDENT_ID}"
echo "delivery_id=${DELIVERY_ID}"
echo "delivery_idempotency_key=${DELIVERY_IDEMPOTENCY_KEY:-NONE}"
echo "first_nonce=${FIRST_NONCE}"
echo "second_nonce=${SECOND_NONCE}"
echo "first_signature=${FIRST_SIGNATURE}"
echo "second_signature=${SECOND_SIGNATURE}"
echo "attempts=${ATTEMPTS}"
echo "endpoint_host=${CURRENT_ENDPOINT_HOST}"
echo "endpoint_url=${CURRENT_ENDPOINT}"
echo "endpoint_candidates=${ENDPOINT_HOST_CANDIDATES[*]}"
