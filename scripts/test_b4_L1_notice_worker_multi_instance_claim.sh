#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
DEBUG="${DEBUG:-0}"
MOCK_PORT="${MOCK_PORT:-$((19000 + RANDOM % 1000))}"
N="${N:-8}"
WAIT_TIMEOUT_SEC="${WAIT_TIMEOUT_SEC:-120}"
POLL_INTERVAL_SEC="${POLL_INTERVAL_SEC:-1}"
LIST_LIMIT="${LIST_LIMIT:-200}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
WORKER_CONFIG="${WORKER_CONFIG:-${REPO_ROOT}/configs/rca-apiserver.yaml}"
WORKER_CMD_BASE="${WORKER_CMD_BASE:-GOLANG_PROTOBUF_REGISTRATION_CONFLICT=warn go run ./cmd/rca-apiserver --config ${WORKER_CONFIG} notice-worker --notice-worker-poll-interval=200ms --notice-worker-batch-size=8 --notice-worker-lock-timeout=5s --notice-worker-channel-concurrency=2 --notice-worker-global-qps=20}"

LAST_HTTP_CODE=""
LAST_BODY=""
CHANNEL_ID=""
INCIDENT_IDS=()
DELIVERY_COUNT="0"
MOCK_TOTAL="0"
MOCK_UNIQUE="0"
WORKER_PIDS=()
WORKER_LOG=""
MOCK_PID=""
MOCK_EVENTS_FILE=""
MOCK_LOG_FILE=""
EXPECTED_KEYS_FILE=""

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

	echo "FAIL B4 step=${step}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	truncate_2kb "${body}"
	echo
	echo "EOF"
	echo "channel_id=${CHANNEL_ID:-NONE}"
	echo "incident_ids=${INCIDENT_IDS[*]:-NONE}"
	echo "delivery_count=${DELIVERY_COUNT:-0}"
	echo "mock_total=${MOCK_TOTAL:-0}"
	echo "mock_unique=${MOCK_UNIQUE:-0}"
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

extract_field() {
	local json="$1"
	shift
	local keys=("$@")
	local key value

	if need_cmd jq; then
		for key in "${keys[@]}"; do
			value="$(
				printf '%s' "${json}" | jq -r --arg k "${key}" '
					(.[$k] // .data[$k] // .noticeChannel[$k] // .data.noticeChannel[$k]) |
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
	rm -f "${WORKER_LOG}" "${MOCK_EVENTS_FILE}" "${MOCK_LOG_FILE}" "${EXPECTED_KEYS_FILE}"
}
trap cleanup EXIT

start_mock() {
	MOCK_EVENTS_FILE="$(mktemp)"
	MOCK_LOG_FILE="$(mktemp)"
	local pybin
	if need_cmd python3; then
		pybin="python3"
	elif need_cmd python; then
		pybin="python"
	else
		fail_step "StartMockPythonMissing" "MISSING_PYTHON" ""
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
            payload = {}
        item = {
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

	local deadline now
	deadline="$(( $(date +%s) + 15 ))"
	while true; do
		if "${CURL}" -sS "http://127.0.0.1:${MOCK_PORT}/healthz" >/dev/null 2>&1; then
			return 0
		fi
		now="$(date +%s)"
		if (( now > deadline )); then
			LAST_HTTP_CODE="MOCK_TIMEOUT"
			LAST_BODY="$(cat "${MOCK_LOG_FILE}" 2>/dev/null || true)"
			fail_step "StartMockTimeout"
		fi
		sleep 0.5
	done
}

start_worker() {
	local worker_id="$1"
	(
		cd "${REPO_ROOT}" && bash -lc "${WORKER_CMD_BASE} --notice-worker-id ${worker_id}"
	) >>"${WORKER_LOG}" 2>&1 &
	local pid="$!"
	sleep 0.5
	if ! kill -0 "${pid}" >/dev/null 2>&1; then
		LAST_HTTP_CODE="WORKER_EXITED"
		LAST_BODY="$(cat "${WORKER_LOG}" 2>/dev/null || true)"
		fail_step "StartWorker.${worker_id}"
	fi
	WORKER_PIDS+=("${pid}")
}

wait_deliveries_succeeded() {
	local deadline now
	deadline="$(( $(date +%s) + WAIT_TIMEOUT_SEC ))"
	while true; do
		call_or_fail "ListDeliveries" GET "${BASE_URL}/v1/notice-deliveries?channel_id=${CHANNEL_ID}&event_type=incident_created&offset=0&limit=${LIST_LIMIT}"
		if need_cmd jq; then
			DELIVERY_COUNT="$(printf '%s' "${LAST_BODY}" | jq -r '(.noticeDeliveries // .data.noticeDeliveries // []) | length' 2>/dev/null || true)"
			succeeded_count="$(printf '%s' "${LAST_BODY}" | jq -r '(.noticeDeliveries // .data.noticeDeliveries // []) | map(select((.status // "") == "succeeded")) | length' 2>/dev/null || true)"
			invalid_attempts="$(printf '%s' "${LAST_BODY}" | jq -r '(.noticeDeliveries // .data.noticeDeliveries // []) | map(select((.status // "") == "succeeded" and (.attempts // 0) != 1)) | length' 2>/dev/null || true)"
		else
			DELIVERY_COUNT="0"
			succeeded_count="0"
			invalid_attempts="0"
		fi

		if [[ "${DELIVERY_COUNT}" == "${N}" ]] && [[ "${succeeded_count}" == "${N}" ]] && [[ "${invalid_attempts}" == "0" ]]; then
			return 0
		fi

		now="$(date +%s)"
		if (( now > deadline )); then
			fail_step "WaitDeliveriesSucceededTimeout" "TIMEOUT" "${LAST_BODY}"
		fi
		sleep "${POLL_INTERVAL_SEC}"
	done
}

normalize_deliveries_ready() {
	local deadline now
	local -a ids
	deadline="$(( $(date +%s) + WAIT_TIMEOUT_SEC ))"

	if ! need_cmd jq; then
		fail_step "NormalizeDeliveriesRequiresJQ" "MISSING_JQ" ""
	fi

	while true; do
		call_or_fail "ListDeliveriesForReplay" GET "${BASE_URL}/v1/notice-deliveries?channel_id=${CHANNEL_ID}&event_type=incident_created&offset=0&limit=${LIST_LIMIT}"
		mapfile -t ids < <(printf '%s' "${LAST_BODY}" | jq -r '.noticeDeliveries[]?.deliveryID // empty' 2>/dev/null)
		if [[ "${#ids[@]}" -eq "${N}" ]]; then
			break
		fi
		now="$(date +%s)"
		if (( now > deadline )); then
			fail_step "WaitDeliveriesReadyTimeout" "TIMEOUT" "${LAST_BODY}"
		fi
		sleep "${POLL_INTERVAL_SEC}"
	done

	local delivery_id
	for delivery_id in "${ids[@]}"; do
		call_or_fail "ReplayDelivery.${delivery_id}" POST "${BASE_URL}/v1/notice-deliveries/${delivery_id}/replay"
	done
}

count_mock_total() {
	if [[ ! -s "${MOCK_EVENTS_FILE}" ]]; then
		echo "0"
		return
	fi
	wc -l <"${MOCK_EVENTS_FILE}" | tr -d ' '
}

count_mock_unique_delivery() {
	if [[ ! -s "${MOCK_EVENTS_FILE}" ]]; then
		echo "0"
		return
	fi
	if need_cmd jq; then
		jq -r '.delivery_id' "${MOCK_EVENTS_FILE}" 2>/dev/null | awk 'NF' | sort -u | wc -l | tr -d ' '
	else
		awk -F'"delivery_id":"' '{print $2}' "${MOCK_EVENTS_FILE}" | awk -F'"' '{print $1}' | awk 'NF' | sort -u | wc -l | tr -d ' '
	fi
}

capture_expected_idempotency_keys() {
	if ! need_cmd jq; then
		fail_step "CaptureExpectedKeysRequiresJQ" "MISSING_JQ" ""
	fi
	call_or_fail "ListDeliveriesForExpectedKeys" GET "${BASE_URL}/v1/notice-deliveries?channel_id=${CHANNEL_ID}&event_type=incident_created&offset=0&limit=${LIST_LIMIT}"
	EXPECTED_KEYS_FILE="$(mktemp)"
	printf '%s' "${LAST_BODY}" | jq -r '.noticeDeliveries[]?.idempotencyKey // empty' 2>/dev/null | awk 'NF' | sort -u >"${EXPECTED_KEYS_FILE}"
	local expected_count
	expected_count="$(wc -l <"${EXPECTED_KEYS_FILE}" | tr -d ' ')"
	if [[ "${expected_count}" != "${N}" ]]; then
		fail_step "CaptureExpectedKeysCountMismatch" "COUNT_MISMATCH" "${LAST_BODY}"
	fi
}

count_mock_total_for_expected_keys() {
	if [[ ! -s "${MOCK_EVENTS_FILE}" ]] || [[ ! -s "${EXPECTED_KEYS_FILE}" ]]; then
		echo "0"
		return
	fi
	jq -r '.idempotency_key // empty' "${MOCK_EVENTS_FILE}" 2>/dev/null | grep -Fxf "${EXPECTED_KEYS_FILE}" | wc -l | tr -d ' '
}

count_mock_unique_for_expected_keys() {
	if [[ ! -s "${MOCK_EVENTS_FILE}" ]] || [[ ! -s "${EXPECTED_KEYS_FILE}" ]]; then
		echo "0"
		return
	fi
	jq -r '.idempotency_key // empty' "${MOCK_EVENTS_FILE}" 2>/dev/null | grep -Fxf "${EXPECTED_KEYS_FILE}" | sort -u | wc -l | tr -d ' '
}

if ! [[ "${N}" =~ ^[0-9]+$ ]] || (( N <= 0 )); then
	fail_step "ValidateN" "INVALID_N" "N must be positive integer"
fi

WORKER_LOG="$(mktemp)"
start_mock

rand="${RAND:-$RANDOM}"
now_epoch="$(date -u +%s)"
endpoint="http://127.0.0.1:${MOCK_PORT}/webhook"

create_channel_body=$(cat <<EOF
{"name":"b4-l1-${rand}","type":"webhook","enabled":true,"endpointURL":"${endpoint}","timeoutMs":1000,"maxRetries":0}
EOF
)
call_or_fail "CreateNoticeChannel" POST "${BASE_URL}/v1/notice-channels" "${create_channel_body}"
CHANNEL_ID="$(extract_field "${LAST_BODY}" "channelID" "channel_id" || true)"
if [[ -z "${CHANNEL_ID}" ]]; then
	fail_step "ParseChannelID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

for ((i = 1; i <= N; i++)); do
	fingerprint="b4-l1-fp-${rand}-${i}"
	ingest_body=$(cat <<EOF
{"idempotencyKey":"idem-b4-l1-ingest-${rand}-${i}","fingerprint":"${fingerprint}","status":"firing","severity":"P1","service":"b4-l1-svc-${i}","cluster":"prod-b4","namespace":"default","workload":"b4-workload-${i}","lastSeenAt":{"seconds":${now_epoch},"nanos":0}}
EOF
)
	call_or_fail "IngestAlertEvent.${i}" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_body}"
	incident_id="$(extract_field "${LAST_BODY}" "incidentID" "incident_id" || true)"
	if [[ -z "${incident_id}" ]]; then
		fail_step "ParseIncidentID.${i}" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
	INCIDENT_IDS+=("${incident_id}")
done

normalize_deliveries_ready

start_worker "b4-worker-a-${rand}"
start_worker "b4-worker-b-${rand}"

wait_deliveries_succeeded
capture_expected_idempotency_keys

MOCK_TOTAL="$(count_mock_total_for_expected_keys)"
MOCK_UNIQUE="$(count_mock_unique_for_expected_keys)"
if [[ "${MOCK_TOTAL}" != "${N}" ]]; then
	LAST_BODY="$(cat "${MOCK_EVENTS_FILE}" 2>/dev/null || true)"
	fail_step "MockTotalMismatch" "COUNT_MISMATCH" "${LAST_BODY}"
fi
if [[ "${MOCK_UNIQUE}" != "${N}" ]]; then
	LAST_BODY="$(cat "${MOCK_EVENTS_FILE}" 2>/dev/null || true)"
	fail_step "MockUniqueMismatch" "COUNT_MISMATCH" "${LAST_BODY}"
fi

echo "PASS B4"
echo "channel_id=${CHANNEL_ID}"
echo "deliveries=${N}"
echo "mock_total=${MOCK_TOTAL}"
echo "mock_unique=${MOCK_UNIQUE}"
