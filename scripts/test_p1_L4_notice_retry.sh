#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
DEBUG="${DEBUG:-0}"
MOCK_PORT="${MOCK_PORT:-19092}"
WAIT_TIMEOUT_SEC="${WAIT_TIMEOUT_SEC:-60}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
WORKER_CONFIG="${WORKER_CONFIG:-${REPO_ROOT}/configs/rca-apiserver.yaml}"
WORKER_CMD="${WORKER_CMD:-GOLANG_PROTOBUF_REGISTRATION_CONFLICT=warn go run ./cmd/rca-apiserver --config ${WORKER_CONFIG} notice-worker --notice-worker-poll-interval=200ms --notice-worker-batch-size=8 --notice-worker-lock-timeout=5s}"

LAST_HTTP_CODE=""
LAST_BODY=""

CHANNEL_ID=""
INCIDENT_ID=""
DELIVERY_ID=""
ATTEMPTS="0"
DELIVERY_STATUS=""
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

fail_l4() {
	local step="$1"
	local detail="${2:-non-2xx response}"
	local code="${3:-${LAST_HTTP_CODE}}"
	local body="${4:-${LAST_BODY}}"

	echo "FAIL L4-NOTICE-RETRY step=${step}"
	echo "detail=${detail}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	trim_2kb "${body}"
	echo
	echo "EOF"
	echo "channel_id=${CHANNEL_ID:-NONE}"
	echo "incident_id=${INCIDENT_ID:-NONE}"
	echo "delivery_id=${DELIVERY_ID:-NONE}"
	echo "attempts=${ATTEMPTS:-0}"
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
		fail_l4 "${step}" "curl failed"
	fi
	if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
		fail_l4 "${step}"
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
		fail_l4 "StartMock" "python3/python not found"
	fi

	"${pybin}" -u - <<'PY' "${MOCK_PORT}" "${MOCK_EVENTS_FILE}" >"${MOCK_LOG_FILE}" 2>&1 &
import json
import sys
from http.server import ThreadingHTTPServer, BaseHTTPRequestHandler

port = int(sys.argv[1])
events_path = sys.argv[2]
counter = 0
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
        global counter
        global counter_by_key
        counter += 1
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
            "count": counter,
            "attempt": key_attempt,
            "event_type": payload.get("event_type", ""),
            "idempotency_key": idem_key,
        }
        with open(events_path, "a", encoding="utf-8") as fp:
            fp.write(json.dumps(item, ensure_ascii=False) + "\n")

        if key_attempt <= 2:
            self.send_response(500)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"ok":false,"attempt":"retry"}')
            return

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
			fail_l4 "StartMock" "mock webhook start timeout"
		fi
		sleep 0.5
	done
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
		fail_l4 "StartWorker" "worker exited immediately"
	fi
}

latest_delivery_from_body() {
	if need_cmd jq; then
		DELIVERY_ID="$(printf '%s' "${LAST_BODY}" | jq -r '((.noticeDeliveries // .data.noticeDeliveries // []) | .[0].deliveryID // empty)' 2>/dev/null || true)"
		ATTEMPTS="$(printf '%s' "${LAST_BODY}" | jq -r '((.noticeDeliveries // .data.noticeDeliveries // []) | .[0].attempts // 0)' 2>/dev/null || true)"
		DELIVERY_STATUS="$(printf '%s' "${LAST_BODY}" | jq -r '((.noticeDeliveries // .data.noticeDeliveries // []) | .[0].status // empty)' 2>/dev/null || true)"
	else
		DELIVERY_ID="$(extract_field "${LAST_BODY}" "deliveryID" "delivery_id" || true)"
		ATTEMPTS="$(printf '%s' "${LAST_BODY}" | sed -n 's/.*"attempts"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' | head -n 1)"
		[[ -z "${ATTEMPTS}" ]] && ATTEMPTS="0"
		if printf '%s' "${LAST_BODY}" | grep -q '"status"[[:space:]]*:[[:space:]]*"succeeded"'; then
			DELIVERY_STATUS="succeeded"
		else
			DELIVERY_STATUS=""
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

count_mock_idempotency_nonempty() {
	if [[ ! -s "${MOCK_EVENTS_FILE}" ]]; then
		echo "0"
		return
	fi
	if need_cmd jq; then
		jq -r 'select((.idempotency_key // "") != "") | .idempotency_key' "${MOCK_EVENTS_FILE}" 2>/dev/null | wc -l | tr -d ' '
	else
		grep -c '"idempotency_key"[[:space:]]*:[[:space:]]*"[^\"]\+"' "${MOCK_EVENTS_FILE}" || true
	fi
}

start_mock

rand="${RAND:-$RANDOM}"
now_epoch="$(date -u +%s)"
fingerprint="p1-l4-notice-retry-fp-${rand}"
endpoint="http://127.0.0.1:${MOCK_PORT}/webhook"

create_channel_body=$(cat <<EOF
{"name":"p1-l4-notice-retry-${rand}","type":"webhook","enabled":true,"endpointURL":"${endpoint}","timeoutMs":1000,"maxRetries":3}
EOF
)
call_or_fail "CreateNoticeChannel" POST "${BASE_URL}/v1/notice-channels" "${create_channel_body}"
CHANNEL_ID="$(extract_field "${LAST_BODY}" "channelID" "channel_id" || true)"
if [[ -z "${CHANNEL_ID}" ]]; then
	fail_l4 "CreateNoticeChannelParseChannelID" "channel_id missing"
fi

ingest_body=$(cat <<EOF
{"idempotencyKey":"idem-p1-l4-notice-retry-${rand}","fingerprint":"${fingerprint}","status":"firing","severity":"P1","service":"retry-svc","cluster":"prod-retry","namespace":"default","workload":"retry-workload","lastSeenAt":{"seconds":${now_epoch},"nanos":0}}
EOF
)
call_or_fail "IngestAlertEvent" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_body}"
INCIDENT_ID="$(extract_field "${LAST_BODY}" "incidentID" "incident_id" || true)"
if [[ -z "${INCIDENT_ID}" ]]; then
	fail_l4 "IngestAlertEventParseIncidentID" "incident_id missing"
fi

call_or_fail "ListDeliveriesPending" GET "${BASE_URL}/v1/notice-deliveries?incident_id=${INCIDENT_ID}&channel_id=${CHANNEL_ID}&event_type=incident_created&status=pending&offset=0&limit=20"
DELIVERY_ID="$(extract_field "${LAST_BODY}" "deliveryID" "delivery_id" || true)"
if [[ -z "${DELIVERY_ID}" ]]; then
	if need_cmd jq; then
		DELIVERY_ID="$(printf '%s' "${LAST_BODY}" | jq -r '((.noticeDeliveries // .data.noticeDeliveries // []) | .[0].deliveryID // empty)' 2>/dev/null || true)"
	fi
fi
if [[ -z "${DELIVERY_ID}" ]]; then
	fail_l4 "ListDeliveriesPendingParseDeliveryID" "delivery_id missing"
fi

start_worker

deadline="$(( $(date +%s) + WAIT_TIMEOUT_SEC ))"
while true; do
	if ! kill -0 "${WORKER_PID}" >/dev/null 2>&1; then
		LAST_HTTP_CODE="WORKER_EXITED"
		LAST_BODY="$(cat "${WORKER_LOG_FILE}" 2>/dev/null || true)"
		fail_l4 "WorkerExited" "worker process exited before success"
	fi

	call_or_fail "PollDelivery" GET "${BASE_URL}/v1/notice-deliveries?incident_id=${INCIDENT_ID}&channel_id=${CHANNEL_ID}&event_type=incident_created&offset=0&limit=20"
		latest_delivery_from_body
		delivery_status="${DELIVERY_STATUS}"
	MOCK_TOTAL="$(count_mock_total)"
	idem_count="$(count_mock_idempotency_nonempty)"

	if [[ "${delivery_status}" == "succeeded" ]] && [[ "${ATTEMPTS}" =~ ^[0-9]+$ ]] && (( ATTEMPTS >= 3 )) && [[ "${MOCK_TOTAL}" =~ ^[0-9]+$ ]] && (( MOCK_TOTAL >= 3 )) && [[ "${idem_count}" =~ ^[0-9]+$ ]] && (( idem_count >= 1 )); then
		break
	fi

	if (( $(date +%s) > deadline )); then
		fail_l4 "WaitSucceeded" "delivery status=${delivery_status:-EMPTY}, attempts=${ATTEMPTS:-0}, mock_total=${MOCK_TOTAL:-0}, idem_count=${idem_count:-0}"
	fi
	sleep 1
done

echo "PASS L4-NOTICE-RETRY"
echo "channel_id=${CHANNEL_ID}"
echo "incident_id=${INCIDENT_ID}"
echo "delivery_id=${DELIVERY_ID}"
echo "attempts=${ATTEMPTS}"
