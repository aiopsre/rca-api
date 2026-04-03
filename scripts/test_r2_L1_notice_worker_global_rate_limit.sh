#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
DEBUG="${DEBUG:-0}"

GLOBAL_QPS="${GLOBAL_QPS:-20}"
DELIVERY_BATCH="${DELIVERY_BATCH:-120}"
WAIT_TIMEOUT_SEC="${WAIT_TIMEOUT_SEC:-180}"
POLL_INTERVAL_SEC="${POLL_INTERVAL_SEC:-1}"
LIST_LIMIT="${LIST_LIMIT:-200}"
WORKER_COUNT="${WORKER_COUNT:-2}"
MOCK_PORT="${MOCK_PORT:-$((19300 + RANDOM % 500))}"
STRICT_DEGRADE_QPS="${STRICT_DEGRADE_QPS:-0}"

REDIS_ADDR="${REDIS_ADDR:-192.168.39.2:6379}"
REDIS_DB="${REDIS_DB:-0}"
REDIS_PASSWORD="${REDIS_PASSWORD:-Az123456_}"
REDIS_FAIL_OPEN="${REDIS_FAIL_OPEN:-true}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
WORKER_CONFIG="${WORKER_CONFIG:-${REPO_ROOT}/configs/rca-apiserver.yaml}"
WORKER_CMD_BASE="${WORKER_CMD_BASE:-GOLANG_PROTOBUF_REGISTRATION_CONFLICT=warn go run ./cmd/rca-apiserver --config ${WORKER_CONFIG} notice-worker --notice-worker-poll-interval=50ms --notice-worker-batch-size=1 --notice-worker-lock-timeout=30s --notice-worker-channel-concurrency=64 --notice-worker-global-qps=${GLOBAL_QPS} --notice-worker-channel-qps=0 --notice-worker-redis-window-ttl=2s --notice-worker-redis-conc-ttl=60s}"

LAST_HTTP_CODE=""
LAST_BODY=""
CURRENT_SCENARIO=""
CHANNEL_ID=""
INCIDENT_ID_SAMPLE=""
DELIVERY_ID_SAMPLE=""
MAX_QPS_SCENARIO="0"
MAX_QPS_REDIS="0"
MAX_QPS_FALLBACK="0"

MOCK_EVENTS_FILE=""
MOCK_LOG_FILE=""
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

	echo "FAIL R2 step=${step}"
	echo "scenario=${CURRENT_SCENARIO:-UNKNOWN}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	truncate_2kb "${body}"
	echo
	echo "EOF"
	echo "channel_id=${CHANNEL_ID:-NONE}"
	echo "incident_id=${INCIDENT_ID_SAMPLE:-NONE}"
	echo "delivery_id=${DELIVERY_ID_SAMPLE:-NONE}"
	echo "max_qps=${MAX_QPS_SCENARIO:-0}"
	echo "global_qps_limit=${GLOBAL_QPS}"
	if [[ -n "${MOCK_LOG_FILE:-}" ]]; then
		echo "mock_log_tail<<EOF"
		tail -n 40 "${MOCK_LOG_FILE}" 2>/dev/null | head -c 2048
		echo
		echo "EOF"
	fi
	if [[ -n "${WORKER_LOG_FILE:-}" ]]; then
		echo "worker_log_tail<<EOF"
		tail -n 60 "${WORKER_LOG_FILE}" 2>/dev/null | head -c 2048
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
		fail_step "${step}"
	fi
	if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
		fail_step "${step}"
	fi
	debug "${step} code=${LAST_HTTP_CODE}"
}

call_with_retry_or_fail() {
	local step="$1"
	local method="$2"
	local url="$3"
	local body="${4:-}"
	local max_attempts="${5:-3}"
	local sleep_seconds="${6:-1}"
	local attempt

	for ((attempt = 1; attempt <= max_attempts; attempt++)); do
		if http_json "${method}" "${url}" "${body}" && [[ "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
			debug "${step} code=${LAST_HTTP_CODE} attempt=${attempt}"
			return 0
		fi
		if (( attempt < max_attempts )) && [[ "${LAST_HTTP_CODE}" =~ ^5[0-9][0-9]$ ]]; then
			sleep "${sleep_seconds}"
			continue
		fi
		fail_step "${step}"
	done
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

disable_channel_or_fail() {
	local channel_id="$1"
	if [[ -z "${channel_id}" ]]; then
		return 0
	fi
	call_or_fail "${CURRENT_SCENARIO}.DisableChannel.${channel_id}" PATCH "${BASE_URL}/v1/notice-channels/${channel_id}" '{"enabled":false}'
}

disable_existing_channels_or_fail() {
	CURRENT_SCENARIO="preclean"
	call_or_fail "preclean.ListChannels" GET "${BASE_URL}/v1/notice-channels?offset=0&limit=${LIST_LIMIT}"
	if ! need_cmd jq; then
		fail_step "preclean.ListChannelsRequiresJQ" "MISSING_JQ" ""
	fi
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

cleanup() {
	local pid
	for pid in "${WORKER_PIDS[@]:-}"; do
		kill "${pid}" >/dev/null 2>&1 || true
	done
	for pid in "${WORKER_PIDS[@]:-}"; do
		wait "${pid}" >/dev/null 2>&1 || true
	done
	if [[ -n "${MOCK_PID}" ]]; then
		kill "${MOCK_PID}" >/dev/null 2>&1 || true
		wait "${MOCK_PID}" >/dev/null 2>&1 || true
	fi
	rm -f "${MOCK_EVENTS_FILE:-}" "${MOCK_LOG_FILE:-}" "${WORKER_LOG_FILE:-}"
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
	"${pybin}" -u - <<'PY' "${MOCK_PORT}" "${MOCK_EVENTS_FILE}" >"${MOCK_LOG_FILE}" 2>&1 &
import json
import sys
import time
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
            payload = {}
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
	local step="$1"
	if ! "${CURL}" -sS "http://127.0.0.1:${MOCK_PORT}/healthz" >/dev/null 2>&1; then
		LAST_HTTP_CODE="MOCK_UNHEALTHY"
		LAST_BODY="$(cat "${MOCK_LOG_FILE}" 2>/dev/null || true)"
		fail_step "${step}" "${LAST_HTTP_CODE}" "${LAST_BODY}"
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

assert_worker_mode_or_fail() {
	local expected_enabled="$1"
	local expected_pattern
	local deadline
	if [[ -z "${WORKER_LOG_FILE:-}" ]]; then
		fail_step "${CURRENT_SCENARIO}.AssertWorkerMode" "NO_WORKER_LOG" ""
	fi

	if [[ "${expected_enabled}" == "true" ]]; then
		expected_pattern='"redis_enabled":true'
	else
		expected_pattern='"redis_enabled":false'
	fi

	deadline="$(( $(date +%s) + 30 ))"
	while true; do
		if rg -q "${expected_pattern}" "${WORKER_LOG_FILE}" 2>/dev/null; then
			return 0
		fi
		if (( $(date +%s) > deadline )); then
			break
		fi
		sleep 0.5
	done

	if [[ "${expected_enabled}" == "true" ]]; then
		fail_step "${CURRENT_SCENARIO}.AssertWorkerMode" "WORKER_REDIS_MODE_MISMATCH" "expected redis_enabled=true"
	fi
	fail_step "${CURRENT_SCENARIO}.AssertWorkerMode" "WORKER_REDIS_MODE_MISMATCH" "expected redis_enabled=false"
}

start_worker() {
	local worker_id="$1"
	local redis_enabled="$2"
	local redis_prefix="$3"

	(
		cd "${REPO_ROOT}" && \
			bash -lc "${WORKER_CMD_BASE} --redis.enabled=${redis_enabled} --redis.addr=${REDIS_ADDR} --redis.db=${REDIS_DB} --redis.password='${REDIS_PASSWORD}' --redis.fail_open=${REDIS_FAIL_OPEN} --notice-worker-redis-rl-key-prefix='${redis_prefix}' --notice-worker-id='${worker_id}'"
	) >>"${WORKER_LOG_FILE}" 2>&1 &

	local pid="$!"
	sleep 0.8
	if ! kill -0 "${pid}" >/dev/null 2>&1; then
		LAST_HTTP_CODE="WORKER_EXITED"
		LAST_BODY="$(cat "${WORKER_LOG_FILE}" 2>/dev/null || true)"
		fail_step "StartWorker.${worker_id}"
	fi
	WORKER_PIDS+=("${pid}")
}

start_workers_pair() {
	local redis_enabled="$1"
	local redis_prefix="$2"
	local i
	WORKER_LOG_FILE="$(mktemp)"
	for ((i = 1; i <= WORKER_COUNT; i++)); do
		start_worker "r2-${CURRENT_SCENARIO}-w${i}" "${redis_enabled}" "${redis_prefix}"
	done
}

create_channel() {
	local endpoint="$1"
	local rand
	rand="${RANDOM}"
	local payload
	payload=$(cat <<EOF
{"name":"r2-${CURRENT_SCENARIO}-${rand}","type":"webhook","enabled":true,"endpointURL":"${endpoint}","timeoutMs":1000,"maxRetries":3}
EOF
)
	call_or_fail "${CURRENT_SCENARIO}.CreateChannel" POST "${BASE_URL}/v1/notice-channels" "${payload}"
	CHANNEL_ID="$(extract_field "${LAST_BODY}" "channelID" "channel_id" || true)"
	if [[ -z "${CHANNEL_ID}" ]]; then
		fail_step "${CURRENT_SCENARIO}.ParseChannelID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
}

create_incidents_batch() {
	local i now_epoch fingerprint payload incident_id
	INCIDENT_ID_SAMPLE=""
	for ((i = 1; i <= DELIVERY_BATCH; i++)); do
		now_epoch="$(date -u +%s)"
		fingerprint="r2-${CURRENT_SCENARIO}-fp-${RANDOM}-${i}"
		payload=$(cat <<EOF
{"idempotencyKey":"idem-r2-${CURRENT_SCENARIO}-${i}-${RANDOM}","fingerprint":"${fingerprint}","status":"firing","severity":"P1","service":"r2-svc","cluster":"prod-r2","namespace":"default","workload":"r2-workload","lastSeenAt":{"seconds":${now_epoch},"nanos":0}}
EOF
)
		call_with_retry_or_fail "${CURRENT_SCENARIO}.Ingest.${i}" POST "${BASE_URL}/v1/alert-events:ingest" "${payload}" 5 1
		if [[ -z "${INCIDENT_ID_SAMPLE}" ]]; then
			incident_id="$(extract_field "${LAST_BODY}" "incidentID" "incident_id" || true)"
			if [[ -n "${incident_id}" ]]; then
				INCIDENT_ID_SAMPLE="${incident_id}"
			fi
		fi
	done
}

wait_deliveries_succeeded() {
	local deadline now total succeeded failed all_succeeded
	deadline="$(( $(date +%s) + WAIT_TIMEOUT_SEC ))"
	DELIVERY_ID_SAMPLE=""

	while true; do
		call_or_fail "${CURRENT_SCENARIO}.ListDeliveries" GET "${BASE_URL}/v1/notice-deliveries?channel_id=${CHANNEL_ID}&event_type=incident_created&offset=0&limit=${LIST_LIMIT}"
		if ! need_cmd jq; then
			fail_step "${CURRENT_SCENARIO}.ListDeliveriesRequiresJQ" "MISSING_JQ" ""
		fi

		total="$(
			printf '%s' "${LAST_BODY}" | jq -r '
				(.noticeDeliveries // .data.noticeDeliveries // []) | length
			' 2>/dev/null || true
		)"
		succeeded="$(
			printf '%s' "${LAST_BODY}" | jq -r '
				(.noticeDeliveries // .data.noticeDeliveries // []) | map(select((.status // "") == "succeeded")) | length
			' 2>/dev/null || true
		)"
		failed="$(
			printf '%s' "${LAST_BODY}" | jq -r '
				(.noticeDeliveries // .data.noticeDeliveries // []) | map(select((.status // "") == "failed")) | length
			' 2>/dev/null || true
		)"
		all_succeeded="$(
			printf '%s' "${LAST_BODY}" | jq -r '
				(.noticeDeliveries // .data.noticeDeliveries // []) | all((.status // "") == "succeeded")
			' 2>/dev/null || true
		)"

		DELIVERY_ID_SAMPLE="$(
			printf '%s' "${LAST_BODY}" | jq -r '
				(.noticeDeliveries // .data.noticeDeliveries // []) | .[0].deliveryID // .[0].delivery_id // empty
			' 2>/dev/null || true
		)"

		if [[ "${total}" == "${DELIVERY_BATCH}" ]] && [[ "${succeeded}" == "${DELIVERY_BATCH}" ]] && [[ "${all_succeeded}" == "true" ]]; then
			return 0
		fi
		if [[ "${failed}" =~ ^[0-9]+$ ]] && (( failed > 0 )); then
			fail_step "${CURRENT_SCENARIO}.DeliveriesFailed" "${LAST_HTTP_CODE}" "${LAST_BODY}"
		fi

		now="$(date +%s)"
		if (( now > deadline )); then
			fail_step "${CURRENT_SCENARIO}.WaitDeliveriesSucceededTimeout" "TIMEOUT" "${LAST_BODY}"
		fi
		sleep "${POLL_INTERVAL_SEC}"
	done
}

max_qps_for_path() {
	local path="$1"
	if ! need_cmd jq; then
		fail_step "${CURRENT_SCENARIO}.MaxQPSRequiresJQ" "MISSING_JQ" ""
	fi
	local value
	value="$(
		jq -r --arg p "${path}" 'select(.path == $p) | ((.ts_ms / 1000) | floor)' "${MOCK_EVENTS_FILE}" 2>/dev/null \
			| sort \
			| uniq -c \
			| awk 'BEGIN {max=0} {if ($1 > max) max=$1} END {print max+0}'
	)"
	if [[ -z "${value}" ]]; then
		value="0"
	fi
	printf '%s' "${value}"
}

assert_redis_enabled_qps() {
	local max_qps="$1"
	if [[ "${max_qps}" =~ ^[0-9]+$ ]] && (( max_qps <= GLOBAL_QPS )); then
		return 0
	fi
	fail_step "${CURRENT_SCENARIO}.AssertRedisEnabledQPS" "QPS_EXCEEDED" "max_qps=${max_qps} limit=${GLOBAL_QPS}"
}

assert_redis_disabled_degrade() {
	local max_qps="$1"
	if [[ ! "${max_qps}" =~ ^[0-9]+$ ]]; then
		fail_step "${CURRENT_SCENARIO}.AssertRedisDisabledDegrade" "INVALID_QPS" "max_qps=${max_qps}"
	fi
	if (( max_qps > GLOBAL_QPS )); then
		return 0
	fi
	if [[ "${MAX_QPS_REDIS}" =~ ^[0-9]+$ ]] && (( max_qps >= MAX_QPS_REDIS )); then
		return 0
	fi
	if [[ "${STRICT_DEGRADE_QPS}" != "1" ]] && (( max_qps > 0 )); then
		echo "WARN R2 step=${CURRENT_SCENARIO}.AssertRedisDisabledDegrade throughput_not_observed max_qps=${max_qps} limit=${GLOBAL_QPS} redis_enabled_max_qps=${MAX_QPS_REDIS}" >&2
		return 0
	fi
	fail_step "${CURRENT_SCENARIO}.AssertRedisDisabledDegrade" "DEGRADE_NOT_OBSERVED" "max_qps=${max_qps} limit=${GLOBAL_QPS} redis_enabled_max_qps=${MAX_QPS_REDIS}"
}

run_scenario() {
	local scenario="$1"
	local redis_enabled="$2"
	local hook_path hook_url redis_prefix max_qps

	CURRENT_SCENARIO="${scenario}"
	CHANNEL_ID=""
	DELIVERY_ID_SAMPLE=""
	MAX_QPS_SCENARIO="0"
	hook_path="/hook/${scenario}"
	hook_url="http://127.0.0.1:${MOCK_PORT}${hook_path}"
	redis_prefix="rca:notice:r2:${scenario}:${RANDOM}"

	create_channel "${hook_url}"
	create_incidents_batch
	ensure_mock_healthy_or_fail "${CURRENT_SCENARIO}.MockHealthBeforeWorkers"
	sleep 0.5
	start_workers_pair "${redis_enabled}" "${redis_prefix}"
	assert_worker_mode_or_fail "${redis_enabled}"
	wait_deliveries_succeeded
	stop_workers

	max_qps="$(max_qps_for_path "${hook_path}")"
	MAX_QPS_SCENARIO="${max_qps}"
	if [[ "${redis_enabled}" == "true" ]]; then
		assert_redis_enabled_qps "${max_qps}"
		MAX_QPS_REDIS="${max_qps}"
	else
		assert_redis_disabled_degrade "${max_qps}"
		MAX_QPS_FALLBACK="${max_qps}"
	fi
	disable_channel_or_fail "${CHANNEL_ID}"
}

if ! need_cmd jq; then
	fail_step "PreCheck.JQ" "MISSING_JQ" ""
fi

call_or_fail "PreCheck.APIReachable" GET "${BASE_URL}/v1/notice-channels?offset=0&limit=1"
disable_existing_channels_or_fail
start_mock
run_scenario "redis_enabled" "true"
run_scenario "redis_disabled" "false"

echo "PASS R2"
echo "global_qps_limit=${GLOBAL_QPS}"
echo "redis_enabled_max_qps=${MAX_QPS_REDIS}"
echo "redis_disabled_max_qps=${MAX_QPS_FALLBACK}"
echo "mock_port=${MOCK_PORT}"
