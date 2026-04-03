#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
DEBUG="${DEBUG:-0}"

WAIT_TIMEOUT_SEC="${WAIT_TIMEOUT_SEC:-240}"
POLL_INTERVAL_SEC="${POLL_INTERVAL_SEC:-1}"
LIST_LIMIT="${LIST_LIMIT:-200}"
BATCH_STREAM_ENABLED="${BATCH_STREAM_ENABLED:-6}"
BATCH_REDIS_DISABLED="${BATCH_REDIS_DISABLED:-6}"

REDIS_ADDR="${REDIS_ADDR:-192.168.39.2:6379}"
REDIS_DB="${REDIS_DB:-0}"
REDIS_PASSWORD="${REDIS_PASSWORD:-Az123456_}"
REDIS_FAIL_OPEN="${REDIS_FAIL_OPEN:-true}"

STREAM_RECLAIM_IDLE_SECONDS="${STREAM_RECLAIM_IDLE_SECONDS:-2}"
WORKER_LOCK_TIMEOUT_SECONDS="${WORKER_LOCK_TIMEOUT_SECONDS:-3}"
MOCK_SLOW_SECONDS="${MOCK_SLOW_SECONDS:-8}"
WORKER_COUNT="${WORKER_COUNT:-2}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
WORKER_CONFIG="${WORKER_CONFIG:-${REPO_ROOT}/configs/rca-apiserver.yaml}"
WORKER_CMD_BASE="${WORKER_CMD_BASE:-GOLANG_PROTOBUF_REGISTRATION_CONFLICT=warn go run ./cmd/rca-apiserver --config ${WORKER_CONFIG} notice-worker --notice-worker-poll-interval=100ms --notice-worker-batch-size=8 --notice-worker-lock-timeout=${WORKER_LOCK_TIMEOUT_SECONDS}s --notice-worker-channel-concurrency=64 --notice-worker-global-qps=200 --notice-worker-channel-qps=0}"

MOCK_PORT="${MOCK_PORT:-$((19400 + RANDOM % 500))}"

CURRENT_SCENARIO=""
LAST_HTTP_CODE=""
LAST_BODY=""
CHANNEL_ID=""
INCIDENT_ID_SAMPLE=""
DELIVERY_ID_SAMPLE=""

MOCK_LOG_FILE=""
MOCK_EVENTS_FILE=""
MOCK_PID=""
WORKER_LOG_FILE=""
WORKER_PIDS=()

debug() {
	if [[ "${DEBUG}" == "1" ]]; then
		echo "[DEBUG] $*" >&2
	fi
}

need_cmd() {
	command -v "$1" >/dev/null 2>&1
}

truncate_2kb() {
	printf '%s' "$1" | head -c 2048
}

fail_step() {
	local step="$1"
	local code="${2:-${LAST_HTTP_CODE}}"
	local body="${3:-${LAST_BODY}}"

	echo "FAIL R3 step=${step}"
	echo "scenario=${CURRENT_SCENARIO:-UNKNOWN}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	truncate_2kb "${body}"
	echo
	echo "EOF"
	echo "channel_id=${CHANNEL_ID:-NONE}"
	echo "incident_id=${INCIDENT_ID_SAMPLE:-NONE}"
	echo "delivery_id=${DELIVERY_ID_SAMPLE:-NONE}"
	if [[ -n "${MOCK_LOG_FILE:-}" ]]; then
		echo "mock_log_tail<<EOF"
		tail -n 80 "${MOCK_LOG_FILE}" 2>/dev/null | head -c 2048
		echo
		echo "EOF"
	fi
	if [[ -n "${WORKER_LOG_FILE:-}" ]]; then
		echo "worker_log_tail<<EOF"
		tail -n 120 "${WORKER_LOG_FILE}" 2>/dev/null | head -c 2048
		echo
		echo "EOF"
	fi
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
			LAST_BODY="${curl_err}"
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
		fail_step "${step}"
	fi
	if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
		fail_step "${step}"
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
				(.[$k] // .data[$k] // .noticeChannel[$k] // .data.noticeChannel[$k] // .incident[$k] // .data.incident[$k]) |
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

cleanup() {
	local pid
	for pid in "${WORKER_PIDS[@]:-}"; do
		kill "${pid}" >/dev/null 2>&1 || true
	done
	for pid in "${WORKER_PIDS[@]:-}"; do
		wait "${pid}" >/dev/null 2>&1 || true
	done
	WORKER_PIDS=()
	if [[ -n "${MOCK_PID}" ]]; then
		kill "${MOCK_PID}" >/dev/null 2>&1 || true
		wait "${MOCK_PID}" >/dev/null 2>&1 || true
	fi
	rm -f "${MOCK_LOG_FILE:-}" "${MOCK_EVENTS_FILE:-}" "${WORKER_LOG_FILE:-}"
}
trap cleanup EXIT

start_mock() {
	local pybin
	if need_cmd python3; then
		pybin="python3"
	elif need_cmd python; then
		pybin="python"
	else
		fail_step "StartMock.MissingPython" "MISSING_PYTHON" ""
	fi

	MOCK_EVENTS_FILE="$(mktemp)"
	MOCK_LOG_FILE="$(mktemp)"
	"${pybin}" -u - <<'PY' "${MOCK_PORT}" "${MOCK_EVENTS_FILE}" "${MOCK_SLOW_SECONDS}" >"${MOCK_LOG_FILE}" 2>&1 &
import json
import sys
import time
from http.server import ThreadingHTTPServer, BaseHTTPRequestHandler

port = int(sys.argv[1])
events_path = sys.argv[2]
slow_seconds = float(sys.argv[3])

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
            payload = {}
        if self.path.startswith("/slow/"):
            time.sleep(slow_seconds)
        item = {
            "ts_ms": int(time.time() * 1000),
            "path": self.path,
            "delivery_id": self.headers.get("X-Rca-Delivery-Id", ""),
            "idempotency_key": self.headers.get("Idempotency-Key", ""),
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

	local deadline
	deadline="$(( $(date +%s) + 20 ))"
	while true; do
		if "${CURL}" -sS "http://127.0.0.1:${MOCK_PORT}/healthz" >/dev/null 2>&1; then
			return 0
		fi
		if (( $(date +%s) > deadline )); then
			LAST_HTTP_CODE="MOCK_TIMEOUT"
			LAST_BODY="$(cat "${MOCK_LOG_FILE}" 2>/dev/null || true)"
			fail_step "StartMock.Timeout"
		fi
		sleep 0.5
	done
}

ensure_mock_healthy_or_fail() {
	if ! "${CURL}" -sS "http://127.0.0.1:${MOCK_PORT}/healthz" >/dev/null 2>&1; then
		LAST_HTTP_CODE="MOCK_UNHEALTHY"
		LAST_BODY="$(cat "${MOCK_LOG_FILE}" 2>/dev/null || true)"
		fail_step "${CURRENT_SCENARIO}.MockHealth"
	fi
}

stop_workers() {
	local pid
	for pid in "${WORKER_PIDS[@]:-}"; do
		kill "${pid}" >/dev/null 2>&1 || true
	done
	for pid in "${WORKER_PIDS[@]:-}"; do
		wait "${pid}" >/dev/null 2>&1 || true
	done
	WORKER_PIDS=()
}

start_worker() {
	local worker_id="$1"
	local redis_enabled="$2"
	local streams_enabled="$3"
	local stream_key="$4"
	local stream_group="$5"

	(
		cd "${REPO_ROOT}" && \
			bash -lc "${WORKER_CMD_BASE} --notice-worker-id='${worker_id}' --redis.enabled=${redis_enabled} --redis.addr=${REDIS_ADDR} --redis.db=${REDIS_DB} --redis.password='${REDIS_PASSWORD}' --redis.fail_open=${REDIS_FAIL_OPEN} --redis.streams.notice_delivery.enabled=${streams_enabled} --redis.streams.notice_delivery.key='${stream_key}' --redis.streams.notice_delivery.group='${stream_group}' --redis.streams.notice_delivery.reclaim_idle_seconds=${STREAM_RECLAIM_IDLE_SECONDS}"
	) >>"${WORKER_LOG_FILE}" 2>&1 &

	local pid="$!"
	sleep 1
	if ! kill -0 "${pid}" >/dev/null 2>&1; then
		LAST_HTTP_CODE="WORKER_EXITED"
		LAST_BODY="$(cat "${WORKER_LOG_FILE}" 2>/dev/null || true)"
		fail_step "${CURRENT_SCENARIO}.StartWorker.${worker_id}"
	fi
	WORKER_PIDS+=("${pid}")
}

kill_one_worker_or_fail() {
	local index="${1:-0}"
	local pid="${WORKER_PIDS[$index]:-}"
	if [[ -z "${pid}" ]]; then
		fail_step "${CURRENT_SCENARIO}.KillWorker" "MISSING_WORKER_PID" ""
	fi
	kill "${pid}" >/dev/null 2>&1 || true
	wait "${pid}" >/dev/null 2>&1 || true
	unset "WORKER_PIDS[$index]"
	WORKER_PIDS=("${WORKER_PIDS[@]}")
}

disable_existing_channels_or_fail() {
	CURRENT_SCENARIO="preclean"
	call_or_fail "preclean.ListChannels" GET "${BASE_URL}/v1/notice-channels?offset=0&limit=${LIST_LIMIT}"
	local channels
	channels="$(
		printf '%s' "${LAST_BODY}" | jq -r '
			(.noticeChannels // .data.noticeChannels // [])[] |
			(.channelID // .channel_id // empty)
		' 2>/dev/null || true
	)"
	if [[ -z "${channels}" ]]; then
		return 0
	fi
	local channel_id
	while IFS= read -r channel_id; do
		[[ -n "${channel_id}" ]] || continue
		call_or_fail "preclean.DisableChannel.${channel_id}" PATCH "${BASE_URL}/v1/notice-channels/${channel_id}" '{"enabled":false}'
	done <<<"${channels}"
}

disable_channel_or_fail() {
	local channel_id="$1"
	[[ -n "${channel_id}" ]] || return 0
	call_or_fail "${CURRENT_SCENARIO}.DisableChannel.${channel_id}" PATCH "${BASE_URL}/v1/notice-channels/${channel_id}" '{"enabled":false}'
}

create_channel_or_fail() {
	local endpoint="$1"
	local payload
	payload=$(cat <<EOF
{"name":"r3-${CURRENT_SCENARIO}-${RANDOM}","type":"webhook","enabled":true,"endpointURL":"${endpoint}","timeoutMs":12000,"maxRetries":3}
EOF
)
	call_or_fail "${CURRENT_SCENARIO}.CreateChannel" POST "${BASE_URL}/v1/notice-channels" "${payload}"
	CHANNEL_ID="$(extract_field "${LAST_BODY}" "channelID" "channel_id" || true)"
	if [[ -z "${CHANNEL_ID}" ]]; then
		fail_step "${CURRENT_SCENARIO}.ParseChannelID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
}

ingest_incident_or_fail() {
	local i="$1"
	local fingerprint="$2"
	local now_epoch
	now_epoch="$(date -u +%s)"
	local payload
	payload=$(cat <<EOF
{"idempotencyKey":"idem-r3-${CURRENT_SCENARIO}-${i}-${RANDOM}","fingerprint":"${fingerprint}","status":"firing","severity":"P1","service":"r3-svc","cluster":"prod-r3","namespace":"default","workload":"r3-workload","lastSeenAt":{"seconds":${now_epoch},"nanos":0}}
EOF
)
	call_or_fail "${CURRENT_SCENARIO}.Ingest.${i}" POST "${BASE_URL}/v1/alert-events:ingest" "${payload}"
	if [[ -z "${INCIDENT_ID_SAMPLE}" ]]; then
		INCIDENT_ID_SAMPLE="$(extract_field "${LAST_BODY}" "incidentID" "incident_id" || true)"
	fi
}

create_incidents_batch_or_fail() {
	local n="$1"
	local i
	for ((i = 1; i <= n; i++)); do
		ingest_incident_or_fail "${i}" "r3-${CURRENT_SCENARIO}-fp-${RANDOM}-${i}"
	done
}

list_deliveries_or_fail() {
	call_or_fail "${CURRENT_SCENARIO}.ListDeliveries" GET "${BASE_URL}/v1/notice-deliveries?channel_id=${CHANNEL_ID}&event_type=incident_created&offset=0&limit=${LIST_LIMIT}"
}

wait_deliveries_succeeded_or_fail() {
	local expected="$1"
	local deadline total succeeded failed all_succeeded
	deadline="$(( $(date +%s) + WAIT_TIMEOUT_SEC ))"

	while true; do
		list_deliveries_or_fail
		total="$(
			printf '%s' "${LAST_BODY}" | jq -r '(.noticeDeliveries // .data.noticeDeliveries // []) | length' 2>/dev/null || true
		)"
		succeeded="$(
			printf '%s' "${LAST_BODY}" | jq -r '(.noticeDeliveries // .data.noticeDeliveries // []) | map(select((.status // "") == "succeeded")) | length' 2>/dev/null || true
		)"
		failed="$(
			printf '%s' "${LAST_BODY}" | jq -r '(.noticeDeliveries // .data.noticeDeliveries // []) | map(select((.status // "") == "failed")) | length' 2>/dev/null || true
		)"
		all_succeeded="$(
			printf '%s' "${LAST_BODY}" | jq -r '(.noticeDeliveries // .data.noticeDeliveries // []) | all((.status // "") == "succeeded")' 2>/dev/null || true
		)"
		DELIVERY_ID_SAMPLE="$(
			printf '%s' "${LAST_BODY}" | jq -r '(.noticeDeliveries // .data.noticeDeliveries // []) | .[0].deliveryID // .[0].delivery_id // empty' 2>/dev/null || true
		)"

		if [[ "${total}" == "${expected}" ]] && [[ "${succeeded}" == "${expected}" ]] && [[ "${all_succeeded}" == "true" ]]; then
			return 0
		fi
		if [[ "${failed}" =~ ^[0-9]+$ ]] && (( failed > 0 )); then
			fail_step "${CURRENT_SCENARIO}.DeliveriesFailed" "${LAST_HTTP_CODE}" "${LAST_BODY}"
		fi
		if (( $(date +%s) > deadline )); then
			fail_step "${CURRENT_SCENARIO}.WaitDeliveriesSucceededTimeout" "TIMEOUT" "${LAST_BODY}"
		fi
		sleep "${POLL_INTERVAL_SEC}"
	done
}

wait_delivery_locked_by_worker_or_fail() {
	local worker_id="$1"
	local deadline locked_worker delivery_id
	deadline="$(( $(date +%s) + WAIT_TIMEOUT_SEC ))"
	while true; do
		list_deliveries_or_fail
		locked_worker="$(
			printf '%s' "${LAST_BODY}" | jq -r '
				(.noticeDeliveries // .data.noticeDeliveries // []) |
				map(select((.status // "") == "pending")) |
				.[0] |
				(.lockedBy // .locked_by // empty)
			' 2>/dev/null || true
		)"
		delivery_id="$(
			printf '%s' "${LAST_BODY}" | jq -r '
				(.noticeDeliveries // .data.noticeDeliveries // []) |
				map(select((.status // "") == "pending")) |
				.[0] |
				(.deliveryID // .delivery_id // empty)
			' 2>/dev/null || true
		)"
		if [[ "${locked_worker}" == "${worker_id}" ]] && [[ -n "${delivery_id}" ]]; then
			DELIVERY_ID_SAMPLE="${delivery_id}"
			return 0
		fi
		if (( $(date +%s) > deadline )); then
			fail_step "${CURRENT_SCENARIO}.WaitLockedByWorkerTimeout" "TIMEOUT" "${LAST_BODY}"
		fi
		sleep 0.5
	done
}

run_streams_enabled_reclaim_scenario() {
	CURRENT_SCENARIO="streams_enabled_reclaim"
	CHANNEL_ID=""
	INCIDENT_ID_SAMPLE=""
	DELIVERY_ID_SAMPLE=""
	local stream_key="rca:notice:r3:stream:${RANDOM}"
	local stream_group="notice_delivery_workers_r3_${RANDOM}"

	create_channel_or_fail "http://127.0.0.1:${MOCK_PORT}/slow/${CURRENT_SCENARIO}"
	ingest_incident_or_fail "seed" "r3-${CURRENT_SCENARIO}-seed-${RANDOM}"

	start_worker "r3-${CURRENT_SCENARIO}-w1" "true" "true" "${stream_key}" "${stream_group}"
	wait_delivery_locked_by_worker_or_fail "r3-${CURRENT_SCENARIO}-w1"
	kill_one_worker_or_fail 0

	call_or_fail "${CURRENT_SCENARIO}.PatchFastEndpoint" PATCH "${BASE_URL}/v1/notice-channels/${CHANNEL_ID}" "{\"endpointURL\":\"http://127.0.0.1:${MOCK_PORT}/fast/${CURRENT_SCENARIO}\"}"
	start_worker "r3-${CURRENT_SCENARIO}-w2" "true" "true" "${stream_key}" "${stream_group}"
	wait_deliveries_succeeded_or_fail "1"

	create_incidents_batch_or_fail "${BATCH_STREAM_ENABLED}"
	wait_deliveries_succeeded_or_fail "$((1 + BATCH_STREAM_ENABLED))"
	disable_channel_or_fail "${CHANNEL_ID}"
	stop_workers
}

run_redis_disabled_fallback_scenario() {
	CURRENT_SCENARIO="redis_disabled_fallback"
	CHANNEL_ID=""
	INCIDENT_ID_SAMPLE=""
	DELIVERY_ID_SAMPLE=""

	create_channel_or_fail "http://127.0.0.1:${MOCK_PORT}/fast/${CURRENT_SCENARIO}"
	create_incidents_batch_or_fail "${BATCH_REDIS_DISABLED}"
	start_worker "r3-${CURRENT_SCENARIO}-w1" "false" "true" "rca:notice:r3:disabled:${RANDOM}" "notice_delivery_workers_r3_disabled_${RANDOM}"
	wait_deliveries_succeeded_or_fail "${BATCH_REDIS_DISABLED}"
	disable_channel_or_fail "${CHANNEL_ID}"
	stop_workers
}

if ! need_cmd jq; then
	fail_step "PreCheck.JQ" "MISSING_JQ" ""
fi

call_or_fail "PreCheck.APIReachable" GET "${BASE_URL}/v1/notice-channels?offset=0&limit=1"
disable_existing_channels_or_fail
start_mock
ensure_mock_healthy_or_fail

WORKER_LOG_FILE="$(mktemp)"
run_streams_enabled_reclaim_scenario
run_redis_disabled_fallback_scenario

echo "PASS R3"
echo "streams_enabled_batch=${BATCH_STREAM_ENABLED}"
echo "redis_disabled_batch=${BATCH_REDIS_DISABLED}"
echo "mock_port=${MOCK_PORT}"
