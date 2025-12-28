#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
DEBUG="${DEBUG:-0}"
MOCK_PORT="${MOCK_PORT:-19096}"
WAIT_TIMEOUT_SEC="${WAIT_TIMEOUT_SEC:-90}"
ALLOW_CONCURRENT_WORKERS="${ALLOW_CONCURRENT_WORKERS:-0}"
REUSE_EXISTING_WORKER="${REUSE_EXISTING_WORKER:-1}"

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
SNAPSHOT_ENDPOINT=""
ATTEMPTS="0"
MOCK_A_HITS="0"
MOCK_B_HITS="0"
MOCK_A_BEFORE_REPLAY="0"

MOCK_EVENTS_FILE="$(mktemp)"
MOCK_LOG_FILE="$(mktemp)"
WORKER_LOG_FILE="$(mktemp)"
MOCK_PID=""
WORKER_PID=""
USE_EXTERNAL_WORKER="0"

debug() {
	if [[ "${DEBUG}" == "1" ]]; then
		echo "[DEBUG] $*" >&2
	fi
}

need_cmd() {
	command -v "$1" >/dev/null 2>&1
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
	local worker_count
	worker_count="$(printf '%s\n' "${workers}" | sed '/^[[:space:]]*$/d' | wc -l | tr -d ' ')"
	if [[ "${REUSE_EXISTING_WORKER}" == "1" ]] && [[ "${worker_count}" == "1" ]]; then
		USE_EXTERNAL_WORKER="1"
		debug "reuse existing notice-worker: ${workers}"
		return 0
	fi
	LAST_HTTP_CODE="CONCURRENT_WORKERS"
	LAST_BODY="${workers}"
	fail_l8 "PrecheckWorkers" "detected running notice-worker processes; stop them or set ALLOW_CONCURRENT_WORKERS=1 (or REUSE_EXISTING_WORKER=1 with single worker)"
}

trim_2kb() {
	printf '%s' "$1" | head -c 2048
}

fail_l8() {
	local step="$1"
	local detail="${2:-non-2xx response}"
	local code="${3:-${LAST_HTTP_CODE}}"
	local body="${4:-${LAST_BODY}}"

	echo "FAIL L8-NOTICE-REPLAY-LATEST step=${step}"
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
	echo "snapshot_endpoint=${SNAPSHOT_ENDPOINT:-NONE}"
	echo "mock_a_hits=${MOCK_A_HITS:-0}"
	echo "mock_b_hits=${MOCK_B_HITS:-0}"
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
		fail_l8 "${step}" "curl failed"
	fi
	if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
		fail_l8 "${step}"
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

extract_snapshot_endpoint() {
	local json="$1"
	local value
	if need_cmd jq; then
		value="$(
			printf '%s' "${json}" | jq -r '
				(.noticeDelivery.snapshot.endpointURL //
				 .data.noticeDelivery.snapshot.endpointURL //
				 .notice_delivery.snapshot.endpoint_url //
				 .data.notice_delivery.snapshot.endpoint_url //
				 empty)
			' 2>/dev/null
		)"
	else
		value="$(printf '%s' "${json}" | sed -n 's/.*"endpointURL"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
	fi
	printf '%s' "${value}"
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
		fail_l8 "$1" "worker exited unexpectedly"
	fi
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
		fail_l8 "StartMock" "python3/python not found"
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
        if self.path not in ("/webhook/a", "/webhook/b"):
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

        if self.path == "/webhook/a":
            self.send_response(500)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"ok":false,"target":"a"}')
            return

        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{"ok":true,"target":"b"}')

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
			fail_l8 "StartMock" "mock webhook start timeout"
		fi
		sleep 0.5
	done
}

start_worker() {
	local worker_id="p1-l8-notice-worker-${rand}-$$"
	(
		cd "${REPO_ROOT}" && bash -lc "${WORKER_CMD} --notice-worker-id ${worker_id}"
	) >"${WORKER_LOG_FILE}" 2>&1 &
	WORKER_PID="$!"
	sleep 0.3
	if ! kill -0 "${WORKER_PID}" >/dev/null 2>&1; then
		LAST_HTTP_CODE="WORKER_EXITED"
		LAST_BODY="$(cat "${WORKER_LOG_FILE}" 2>/dev/null || true)"
		fail_l8 "StartWorker" "worker exited immediately"
	fi
}

wait_delivery_status_or_fail() {
	local step="$1"
	local expect_status="$2"
	local min_attempts="$3"
	local deadline status attempts snapshot
	deadline="$(( $(date +%s) + WAIT_TIMEOUT_SEC ))"

	while true; do
		assert_worker_alive "${step}"
		call_or_fail "${step}GetDelivery" GET "${BASE_URL}/v1/notice-deliveries/${DELIVERY_ID}"
		status="$(extract_field "${LAST_BODY}" "status" || true)"
		attempts="$(extract_field "${LAST_BODY}" "attempts" || true)"
		[[ -z "${attempts}" ]] && attempts="0"
		snapshot="$(extract_snapshot_endpoint "${LAST_BODY}" || true)"

		if [[ "${status}" == "${expect_status}" ]] && [[ "${attempts}" =~ ^[0-9]+$ ]] && (( attempts >= min_attempts )); then
			ATTEMPTS="${attempts}"
			if [[ -n "${snapshot}" ]]; then
				SNAPSHOT_ENDPOINT="${snapshot}"
			fi
			return 0
		fi

		if (( $(date +%s) > deadline )); then
			fail_l8 "${step}" "expect status=${expect_status}, got=${status:-EMPTY}, attempts=${attempts:-EMPTY}"
		fi
		sleep 1
	done
}

start_mock
precheck_notice_workers_or_fail

rand="${RAND:-$RANDOM}"
now_epoch="$(date -u +%s)"
endpoint_a="http://127.0.0.1:${MOCK_PORT}/webhook/a"
endpoint_b="http://127.0.0.1:${MOCK_PORT}/webhook/b"

create_channel_body=$(cat <<EOF
{"name":"p1-l8-notice-replay-latest-${rand}","type":"webhook","enabled":true,"endpointURL":"${endpoint_a}","timeoutMs":1200,"maxRetries":1}
EOF
)
call_or_fail "CreateNoticeChannel" POST "${BASE_URL}/v1/notice-channels" "${create_channel_body}"
CHANNEL_ID="$(extract_field "${LAST_BODY}" "channelID" "channel_id" || true)"
if [[ -z "${CHANNEL_ID}" ]]; then
	fail_l8 "CreateNoticeChannelParse" "channel_id missing"
fi

ingest_body=$(cat <<EOF
{"idempotencyKey":"idem-p1-l8-notice-ingest-${rand}","fingerprint":"p1-l8-notice-replay-latest-fp-${rand}","status":"firing","severity":"P1","service":"notice-l8","cluster":"prod-l8","namespace":"default","workload":"checkout-api","lastSeenAt":{"seconds":${now_epoch},"nanos":0}}
EOF
)
call_or_fail "IngestAlertEvent" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_body}"
INCIDENT_ID="$(extract_field "${LAST_BODY}" "incidentID" "incident_id" || true)"
if [[ -z "${INCIDENT_ID}" ]]; then
	fail_l8 "IngestAlertEventParse" "incident_id missing"
fi

call_or_fail "ListPendingDelivery" GET "${BASE_URL}/v1/notice-deliveries?incident_id=${INCIDENT_ID}&channel_id=${CHANNEL_ID}&event_type=incident_created&status=pending&offset=0&limit=20"
if need_cmd jq; then
	DELIVERY_ID="$(printf '%s' "${LAST_BODY}" | jq -r '((.noticeDeliveries // .data.noticeDeliveries // []) | .[0].deliveryID // empty)' 2>/dev/null || true)"
else
	DELIVERY_ID="$(extract_field "${LAST_BODY}" "deliveryID" "delivery_id" || true)"
fi
if [[ -z "${DELIVERY_ID}" ]]; then
	fail_l8 "ListPendingDeliveryParse" "delivery_id missing"
fi

call_or_fail "GetDeliveryInitial" GET "${BASE_URL}/v1/notice-deliveries/${DELIVERY_ID}"
DELIVERY_IDEMPOTENCY_KEY="$(extract_field "${LAST_BODY}" "idempotencyKey" "idempotency_key" || true)"
SNAPSHOT_ENDPOINT="$(extract_snapshot_endpoint "${LAST_BODY}" || true)"
if [[ -z "${DELIVERY_IDEMPOTENCY_KEY}" ]]; then
	fail_l8 "GetDeliveryInitialParseIdempotencyKey" "idempotency_key missing"
fi
if [[ -z "${SNAPSHOT_ENDPOINT}" ]]; then
	fail_l8 "GetDeliveryInitialParseSnapshot" "snapshot endpoint missing"
fi
if [[ "${SNAPSHOT_ENDPOINT}" != "${endpoint_a}" ]]; then
	fail_l8 "GetDeliveryInitialAssertSnapshot" "initial snapshot endpoint expected ${endpoint_a}, got=${SNAPSHOT_ENDPOINT}"
fi

if [[ "${USE_EXTERNAL_WORKER}" != "1" ]]; then
	start_worker
fi
wait_delivery_status_or_fail "WaitDeliveryFailedOnA" "failed" 1

MOCK_A_BEFORE_REPLAY="$(count_mock_path_by_key "/webhook/a" "${DELIVERY_IDEMPOTENCY_KEY}")"
if [[ ! "${MOCK_A_BEFORE_REPLAY}" =~ ^[0-9]+$ ]] || (( MOCK_A_BEFORE_REPLAY < 1 )); then
	fail_l8 "AssertFailedHitA" "mock A count before replay expected >=1, got=${MOCK_A_BEFORE_REPLAY:-EMPTY}"
fi

patch_channel_body=$(cat <<EOF
{"endpointURL":"${endpoint_b}"}
EOF
)
call_or_fail "PatchChannelToB" PATCH "${BASE_URL}/v1/notice-channels/${CHANNEL_ID}" "${patch_channel_body}"

call_or_fail "ReplayUseLatest" POST "${BASE_URL}/v1/notice-deliveries/${DELIVERY_ID}:replay?use_latest_channel=1" '{}'
replay_status="$(extract_field "${LAST_BODY}" "status" || true)"
replay_attempts="$(extract_field "${LAST_BODY}" "attempts" || true)"
replay_snapshot_endpoint="$(extract_snapshot_endpoint "${LAST_BODY}" || true)"
if [[ "${replay_status}" != "pending" ]]; then
	fail_l8 "ReplayUseLatestAssertStatus" "replay status expected pending, got=${replay_status:-EMPTY}"
fi
if [[ -z "${replay_attempts}" ]]; then
	replay_attempts="0"
fi
if (( replay_attempts != 0 )); then
	fail_l8 "ReplayUseLatestAssertAttempts" "replay attempts expected 0, got=${replay_attempts}"
fi
if [[ -z "${replay_snapshot_endpoint}" ]]; then
	fail_l8 "ReplayUseLatestAssertSnapshotMissing" "replay snapshot endpoint missing"
fi
if [[ "${replay_snapshot_endpoint}" != "${endpoint_b}" ]]; then
	fail_l8 "ReplayUseLatestAssertSnapshot" "snapshot endpoint expected ${endpoint_b}, got=${replay_snapshot_endpoint}"
fi

wait_delivery_status_or_fail "WaitDeliverySucceededOnB" "succeeded" 1

MOCK_A_HITS="$(count_mock_path_by_key "/webhook/a" "${DELIVERY_IDEMPOTENCY_KEY}")"
MOCK_B_HITS="$(count_mock_path_by_key "/webhook/b" "${DELIVERY_IDEMPOTENCY_KEY}")"
if [[ ! "${MOCK_A_HITS}" =~ ^[0-9]+$ ]] || (( MOCK_A_HITS != MOCK_A_BEFORE_REPLAY )); then
	fail_l8 "AssertAUnchangedAfterReplay" "mock A count should not grow after replay, before=${MOCK_A_BEFORE_REPLAY}, after=${MOCK_A_HITS:-EMPTY}"
fi
if [[ ! "${MOCK_B_HITS}" =~ ^[0-9]+$ ]] || (( MOCK_B_HITS < 1 )); then
	fail_l8 "AssertBHitAfterReplay" "mock B count expected >=1, got=${MOCK_B_HITS:-EMPTY}"
fi

call_or_fail "GetDeliveryFinal" GET "${BASE_URL}/v1/notice-deliveries/${DELIVERY_ID}"
final_snapshot_endpoint="$(extract_snapshot_endpoint "${LAST_BODY}" || true)"
if [[ -z "${final_snapshot_endpoint}" ]]; then
	fail_l8 "AssertFinalSnapshotEndpointMissing" "final snapshot endpoint missing"
fi
if [[ "${final_snapshot_endpoint}" != "${endpoint_b}" ]]; then
	fail_l8 "AssertFinalSnapshotEndpoint" "final snapshot endpoint expected ${endpoint_b}, got=${final_snapshot_endpoint}"
fi
SNAPSHOT_ENDPOINT="${final_snapshot_endpoint}"

echo "PASS L8-NOTICE-REPLAY-LATEST"
echo "channel_id=${CHANNEL_ID}"
echo "incident_id=${INCIDENT_ID}"
echo "delivery_id=${DELIVERY_ID}"
echo "delivery_idempotency_key=${DELIVERY_IDEMPOTENCY_KEY}"
echo "snapshot_endpoint=${SNAPSHOT_ENDPOINT}"
echo "mock_a_hits=${MOCK_A_HITS}"
echo "mock_b_hits=${MOCK_B_HITS}"
echo "attempts=${ATTEMPTS}"
