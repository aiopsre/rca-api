#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
DEBUG="${DEBUG:-0}"
MOCK_PORT="${MOCK_PORT:-19093}"
WAIT_TIMEOUT_SEC="${WAIT_TIMEOUT_SEC:-90}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
WORKER_CONFIG="${WORKER_CONFIG:-${REPO_ROOT}/configs/rca-apiserver.yaml}"
WORKER_CMD="${WORKER_CMD:-GOLANG_PROTOBUF_REGISTRATION_CONFLICT=warn go run ./cmd/rca-apiserver --config ${WORKER_CONFIG} notice-worker --notice-worker-poll-interval=200ms --notice-worker-batch-size=8 --notice-worker-lock-timeout=5s}"

LAST_HTTP_CODE=""
LAST_BODY=""

CHANNEL_ID=""
INCIDENT_REPLAY_ID=""
DELIVERY_REPLAY_ID=""
DELIVERY_REPLAY_KEY=""
INCIDENT_CANCEL_ID=""
DELIVERY_CANCEL_ID=""
DELIVERY_CANCEL_KEY=""
REPLAY_ATTEMPTS_BEFORE="0"
REPLAY_ATTEMPTS_AFTER="0"
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

fail_l5() {
	local step="$1"
	local detail="${2:-non-2xx response}"
	local code="${3:-${LAST_HTTP_CODE}}"
	local body="${4:-${LAST_BODY}}"

	echo "FAIL L5-NOTICE-OPS step=${step}"
	echo "detail=${detail}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	trim_2kb "${body}"
	echo
	echo "EOF"
	echo "channel_id=${CHANNEL_ID:-NONE}"
	echo "incident_replay_id=${INCIDENT_REPLAY_ID:-NONE}"
	echo "delivery_replay_id=${DELIVERY_REPLAY_ID:-NONE}"
	echo "incident_cancel_id=${INCIDENT_CANCEL_ID:-NONE}"
	echo "delivery_cancel_id=${DELIVERY_CANCEL_ID:-NONE}"
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
		fail_l5 "${step}" "curl failed"
	fi
	if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
		fail_l5 "${step}"
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
					(.[$k] // .data[$k] // .noticeDelivery[$k] // .data.noticeDelivery[$k] //
					 .noticeChannel[$k] // .data.noticeChannel[$k]) |
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

extract_delivery_from_list() {
	if need_cmd jq; then
		DELIVERY_TMP_ID="$(printf '%s' "${LAST_BODY}" | jq -r '((.noticeDeliveries // .data.noticeDeliveries // []) | .[0].deliveryID // empty)' 2>/dev/null || true)"
	else
		DELIVERY_TMP_ID="$(extract_field "${LAST_BODY}" "deliveryID" "delivery_id" || true)"
	fi
}

extract_delivery_field_by_id() {
	local field="$1"
	local id="$2"
	if need_cmd jq; then
		printf '%s' "${LAST_BODY}" | jq -r --arg id "${id}" --arg f "${field}" '
			((.noticeDeliveries // .data.noticeDeliveries // [])[] | select(.deliveryID == $id) | .[$f]) // empty
		' 2>/dev/null || true
	else
		if [[ "${field}" == "status" ]]; then
			printf '%s' "${LAST_BODY}" | sed -n 's/.*"status"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1
		elif [[ "${field}" == "attempts" ]]; then
			printf '%s' "${LAST_BODY}" | sed -n 's/.*"attempts"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' | head -n 1
		elif [[ "${field}" == "idempotencyKey" ]]; then
			printf '%s' "${LAST_BODY}" | sed -n 's/.*"idempotencyKey"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1
		fi
	fi
}

count_mock_total() {
	if [[ ! -s "${MOCK_EVENTS_FILE}" ]]; then
		echo "0"
		return
	fi
	wc -l <"${MOCK_EVENTS_FILE}" | tr -d ' '
}

count_mock_key() {
	local key="$1"
	if [[ -z "${key}" ]] || [[ ! -s "${MOCK_EVENTS_FILE}" ]]; then
		echo "0"
		return
	fi
	if need_cmd jq; then
		jq -r --arg k "${key}" 'select((.idempotency_key // "") == $k) | .idempotency_key' "${MOCK_EVENTS_FILE}" 2>/dev/null | wc -l | tr -d ' '
	else
		grep -c "\"idempotency_key\"[[:space:]]*:[[:space:]]*\"${key}\"" "${MOCK_EVENTS_FILE}" || true
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
		fail_l5 "StartMock" "python3/python not found"
	fi

	"${pybin}" -u - <<'PY' "${MOCK_PORT}" "${MOCK_EVENTS_FILE}" >"${MOCK_LOG_FILE}" 2>&1 &
import json
import sys
from http.server import ThreadingHTTPServer, BaseHTTPRequestHandler

port = int(sys.argv[1])
events_path = sys.argv[2]
mode = "always_500"
total = 0
counter_by_key = {}

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
        global mode
        global total
        global counter_by_key

        if self.path == "/mode":
            length = int(self.headers.get("Content-Length", "0"))
            body = self.rfile.read(length)
            try:
                payload = json.loads(body.decode("utf-8"))
            except Exception:
                payload = {}
            next_mode = str(payload.get("mode", "")).strip()
            if next_mode not in ("always_500", "always_200"):
                self.send_response(400)
                self.end_headers()
                self.wfile.write(b'{"ok":false,"error":"invalid_mode"}')
                return
            mode = next_mode
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps({"ok": True, "mode": mode}).encode("utf-8"))
            return

        if self.path != "/webhook":
            self.send_response(404)
            self.end_headers()
            return

        total += 1
        idem_key = self.headers.get("Idempotency-Key", "")
        bucket = idem_key if idem_key else "__empty__"
        key_attempt = counter_by_key.get(bucket, 0) + 1
        counter_by_key[bucket] = key_attempt

        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length)
        try:
            payload = json.loads(body.decode("utf-8"))
        except Exception:
            payload = {}
        item = {
            "total": total,
            "attempt": key_attempt,
            "mode": mode,
            "event_type": payload.get("event_type", ""),
            "idempotency_key": idem_key,
        }
        with open(events_path, "a", encoding="utf-8") as fp:
            fp.write(json.dumps(item, ensure_ascii=False) + "\n")

        if mode == "always_500":
            self.send_response(500)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"ok":false,"mode":"always_500"}')
            return

        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{"ok":true,"mode":"always_200"}')

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
			fail_l5 "StartMock" "mock webhook start timeout"
		fi
		sleep 0.5
	done
}

set_mock_mode_or_fail() {
	local mode="$1"
	call_or_fail "SetMockMode" POST "http://127.0.0.1:${MOCK_PORT}/mode" "{\"mode\":\"${mode}\"}"
}

start_worker() {
	(
		cd "${REPO_ROOT}" && bash -lc "${WORKER_CMD}"
	) >"${WORKER_LOG_FILE}" 2>&1 &
	WORKER_PID="$!"
	sleep 0.3
	if ! kill -0 "${WORKER_PID}" >/dev/null 2>&1; then
		LAST_HTTP_CODE="WORKER_EXITED"
		LAST_BODY="$(cat "${WORKER_LOG_FILE}" 2>/dev/null || true)"
		fail_l5 "StartWorker" "worker exited immediately"
	fi
}

stop_worker() {
	if [[ -n "${WORKER_PID}" ]]; then
		kill "${WORKER_PID}" >/dev/null 2>&1 || true
		wait "${WORKER_PID}" >/dev/null 2>&1 || true
		WORKER_PID=""
	fi
}

assert_worker_alive() {
	if [[ -n "${WORKER_PID}" ]] && ! kill -0 "${WORKER_PID}" >/dev/null 2>&1; then
		LAST_HTTP_CODE="WORKER_EXITED"
		LAST_BODY="$(cat "${WORKER_LOG_FILE}" 2>/dev/null || true)"
		fail_l5 "$1" "worker exited unexpectedly"
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
		call_or_fail "${step}GetDelivery" GET "${BASE_URL}/v1/notice-deliveries/${delivery_id}"
		status="$(extract_field "${LAST_BODY}" "status" || true)"
		attempts="$(extract_field "${LAST_BODY}" "attempts" || true)"
		if [[ -z "${attempts}" ]]; then
			attempts="0"
		fi

		if [[ "${status}" == "${expect_status}" ]] && [[ "${attempts}" =~ ^[0-9]+$ ]] && (( attempts >= min_attempts )); then
			printf '%s\n%s' "${status}" "${attempts}"
			return 0
		fi

		if (( $(date +%s) > deadline )); then
			fail_l5 "${step}" "expect status=${expect_status}, got=${status:-EMPTY}, attempts=${attempts:-EMPTY}"
		fi
		sleep 1
	done
}

start_mock
set_mock_mode_or_fail "always_500"

rand="${RAND:-$RANDOM}"
now_epoch="$(date -u +%s)"
endpoint="http://127.0.0.1:${MOCK_PORT}/webhook"

create_channel_body=$(cat <<EOF
{"name":"p1-l5-notice-ops-${rand}","type":"webhook","enabled":true,"endpointURL":"${endpoint}","timeoutMs":1000,"maxRetries":2}
EOF
)
call_or_fail "CreateNoticeChannel" POST "${BASE_URL}/v1/notice-channels" "${create_channel_body}"
CHANNEL_ID="$(extract_field "${LAST_BODY}" "channelID" "channel_id" || true)"
if [[ -z "${CHANNEL_ID}" ]]; then
	fail_l5 "CreateNoticeChannelParse" "channel_id missing"
fi

ingest_replay_body=$(cat <<EOF
{"idempotencyKey":"idem-p1-l5-replay-${rand}","fingerprint":"p1-l5-notice-replay-fp-${rand}","status":"firing","severity":"P1","service":"notice-l5-replay","cluster":"prod-l5","namespace":"default","workload":"notice-l5","lastSeenAt":{"seconds":${now_epoch},"nanos":0}}
EOF
)
call_or_fail "IngestReplayIncident" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_replay_body}"
INCIDENT_REPLAY_ID="$(extract_field "${LAST_BODY}" "incidentID" "incident_id" || true)"
if [[ -z "${INCIDENT_REPLAY_ID}" ]]; then
	fail_l5 "IngestReplayIncidentParse" "incident_id missing"
fi

call_or_fail "ListReplayDeliveryPending" GET "${BASE_URL}/v1/notice-deliveries?incident_id=${INCIDENT_REPLAY_ID}&channel_id=${CHANNEL_ID}&event_type=incident_created&status=pending&offset=0&limit=20"
extract_delivery_from_list
DELIVERY_REPLAY_ID="${DELIVERY_TMP_ID:-}"
if [[ -z "${DELIVERY_REPLAY_ID}" ]]; then
	fail_l5 "ListReplayDeliveryPendingParse" "delivery_id missing"
fi

call_or_fail "GetReplayDeliveryInitial" GET "${BASE_URL}/v1/notice-deliveries/${DELIVERY_REPLAY_ID}"
DELIVERY_REPLAY_KEY="$(extract_field "${LAST_BODY}" "idempotencyKey" "idempotency_key" || true)"
if [[ -z "${DELIVERY_REPLAY_KEY}" ]]; then
	fail_l5 "GetReplayDeliveryInitialParse" "idempotency_key missing"
fi

start_worker
REPLAY_ATTEMPTS_BEFORE="$(wait_delivery_status_or_fail "WaitReplayFailedDLQ" "${DELIVERY_REPLAY_ID}" "failed" 2 | tail -n 1)"
if [[ -z "${REPLAY_ATTEMPTS_BEFORE}" ]] || (( REPLAY_ATTEMPTS_BEFORE < 2 )); then
	fail_l5 "WaitReplayFailedDLQAssert" "failed attempts expected >=2, got=${REPLAY_ATTEMPTS_BEFORE:-EMPTY}"
fi

mock_replay_before="$(count_mock_key "${DELIVERY_REPLAY_KEY}")"
if [[ -z "${mock_replay_before}" ]] || (( mock_replay_before < 2 )); then
	fail_l5 "ReplayMockBeforeCount" "mock replay key count expected >=2, got=${mock_replay_before:-EMPTY}"
fi

set_mock_mode_or_fail "always_200"
call_or_fail "ReplayDelivery" POST "${BASE_URL}/v1/notice-deliveries/${DELIVERY_REPLAY_ID}:replay" '{}'
replay_status="$(extract_field "${LAST_BODY}" "status" || true)"
replay_attempts="$(extract_field "${LAST_BODY}" "attempts" || true)"
if [[ "${replay_status}" != "pending" ]]; then
	fail_l5 "ReplayDeliveryAssertStatus" "replay status expected pending, got=${replay_status:-EMPTY}"
fi
if [[ -z "${replay_attempts}" ]]; then
	replay_attempts="0"
fi
if (( replay_attempts != 0 )); then
	fail_l5 "ReplayDeliveryAssertAttempts" "replay attempts expected 0, got=${replay_attempts}"
fi

REPLAY_ATTEMPTS_AFTER="$(wait_delivery_status_or_fail "WaitReplaySucceeded" "${DELIVERY_REPLAY_ID}" "succeeded" 1 | tail -n 1)"
if [[ -z "${REPLAY_ATTEMPTS_AFTER}" ]] || (( REPLAY_ATTEMPTS_AFTER < 1 )); then
	fail_l5 "WaitReplaySucceededAssert" "replay attempts after success expected >=1, got=${REPLAY_ATTEMPTS_AFTER:-EMPTY}"
fi

mock_replay_after="$(count_mock_key "${DELIVERY_REPLAY_KEY}")"
if [[ -z "${mock_replay_after}" ]] || (( mock_replay_after <= mock_replay_before )); then
	fail_l5 "ReplayMockAfterCount" "mock replay key count expected increase, before=${mock_replay_before:-0}, after=${mock_replay_after:-0}"
fi

stop_worker

ingest_cancel_body=$(cat <<EOF
{"idempotencyKey":"idem-p1-l5-cancel-${rand}","fingerprint":"p1-l5-notice-cancel-fp-${rand}","status":"firing","severity":"P1","service":"notice-l5-cancel","cluster":"prod-l5","namespace":"default","workload":"notice-l5","lastSeenAt":{"seconds":${now_epoch},"nanos":0}}
EOF
)
call_or_fail "IngestCancelIncident" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_cancel_body}"
INCIDENT_CANCEL_ID="$(extract_field "${LAST_BODY}" "incidentID" "incident_id" || true)"
if [[ -z "${INCIDENT_CANCEL_ID}" ]]; then
	fail_l5 "IngestCancelIncidentParse" "incident_id missing"
fi

call_or_fail "ListCancelDeliveryPending" GET "${BASE_URL}/v1/notice-deliveries?incident_id=${INCIDENT_CANCEL_ID}&channel_id=${CHANNEL_ID}&event_type=incident_created&status=pending&offset=0&limit=20"
extract_delivery_from_list
DELIVERY_CANCEL_ID="${DELIVERY_TMP_ID:-}"
if [[ -z "${DELIVERY_CANCEL_ID}" ]]; then
	fail_l5 "ListCancelDeliveryPendingParse" "delivery_id missing"
fi

call_or_fail "GetCancelDeliveryInitial" GET "${BASE_URL}/v1/notice-deliveries/${DELIVERY_CANCEL_ID}"
DELIVERY_CANCEL_KEY="$(extract_field "${LAST_BODY}" "idempotencyKey" "idempotency_key" || true)"
if [[ -z "${DELIVERY_CANCEL_KEY}" ]]; then
	fail_l5 "GetCancelDeliveryInitialParse" "idempotency_key missing"
fi
cancel_attempts_before="$(extract_field "${LAST_BODY}" "attempts" || true)"
if [[ -z "${cancel_attempts_before}" ]]; then
	cancel_attempts_before="0"
fi

call_or_fail "CancelDelivery" POST "${BASE_URL}/v1/notice-deliveries/${DELIVERY_CANCEL_ID}:cancel" '{}'
cancel_status="$(extract_field "${LAST_BODY}" "status" || true)"
if [[ "${cancel_status}" != "canceled" ]]; then
	fail_l5 "CancelDeliveryAssertStatus" "cancel status expected canceled, got=${cancel_status:-EMPTY}"
fi

mock_cancel_before="$(count_mock_key "${DELIVERY_CANCEL_KEY}")"
start_worker
sleep 3
assert_worker_alive "CancelWorkerAlive"

call_or_fail "GetCancelDeliveryAfterWorker" GET "${BASE_URL}/v1/notice-deliveries/${DELIVERY_CANCEL_ID}"
cancel_status_after="$(extract_field "${LAST_BODY}" "status" || true)"
cancel_attempts_after="$(extract_field "${LAST_BODY}" "attempts" || true)"
if [[ -z "${cancel_attempts_after}" ]]; then
	cancel_attempts_after="0"
fi
if [[ "${cancel_status_after}" != "canceled" ]]; then
	fail_l5 "CancelAfterWorkerStatus" "status expected canceled, got=${cancel_status_after:-EMPTY}"
fi
if (( cancel_attempts_after != cancel_attempts_before )); then
	fail_l5 "CancelAfterWorkerAttempts" "attempts should not grow after cancel, before=${cancel_attempts_before}, after=${cancel_attempts_after}"
fi

mock_cancel_after="$(count_mock_key "${DELIVERY_CANCEL_KEY}")"
if (( mock_cancel_after != mock_cancel_before )); then
	fail_l5 "CancelAfterWorkerMockCount" "mock count should not grow after cancel, before=${mock_cancel_before}, after=${mock_cancel_after}"
fi

MOCK_TOTAL="$(count_mock_total)"
echo "PASS L5-NOTICE-OPS"
echo "channel_id=${CHANNEL_ID}"
echo "incident_replay_id=${INCIDENT_REPLAY_ID}"
echo "delivery_replay_id=${DELIVERY_REPLAY_ID}"
echo "incident_cancel_id=${INCIDENT_CANCEL_ID}"
echo "delivery_cancel_id=${DELIVERY_CANCEL_ID}"
