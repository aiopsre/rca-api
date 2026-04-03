#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
DEBUG="${DEBUG:-0}"
MOCK_PORT="${MOCK_PORT:-19095}"
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
SNAPSHOT_ENDPOINT=""
ATTEMPTS="0"
MOCK_OLD_HITS="0"
MOCK_NEW_HITS="0"

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
	fail_l7 "PrecheckWorkers" "detected running notice-worker processes; stop them or set ALLOW_CONCURRENT_WORKERS=1"
}

trim_2kb() {
	printf '%s' "$1" | head -c 2048
}

fail_l7() {
	local step="$1"
	local detail="${2:-non-2xx response}"
	local code="${3:-${LAST_HTTP_CODE}}"
	local body="${4:-${LAST_BODY}}"

	echo "FAIL L7-NOTICE-SNAPSHOT step=${step}"
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
	echo "mock_old_hits=${MOCK_OLD_HITS:-0}"
	echo "mock_new_hits=${MOCK_NEW_HITS:-0}"
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
		fail_l7 "${step}" "curl failed"
	fi
	if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
		fail_l7 "${step}"
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

count_mock_path() {
	local path="$1"
	if [[ ! -s "${MOCK_EVENTS_FILE}" ]]; then
		echo "0"
		return
	fi
	if need_cmd jq; then
		jq -r --arg p "${path}" 'select((.path // "") == $p) | .path' "${MOCK_EVENTS_FILE}" 2>/dev/null | wc -l | tr -d ' '
	else
		grep -c "\"path\"[[:space:]]*:[[:space:]]*\"${path}\"" "${MOCK_EVENTS_FILE}" || true
	fi
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
		fail_l7 "$1" "worker exited unexpectedly"
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
		fail_l7 "StartMock" "python3/python not found"
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
        if self.path not in ("/webhook/old", "/webhook/new"):
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
			fail_l7 "StartMock" "mock webhook start timeout"
		fi
		sleep 0.5
	done
}

start_worker() {
	local worker_id="p1-l7-notice-worker-${rand}-$$"
	(
		cd "${REPO_ROOT}" && bash -lc "${WORKER_CMD} --notice-worker-id ${worker_id}"
	) >"${WORKER_LOG_FILE}" 2>&1 &
	WORKER_PID="$!"
	sleep 0.3
	if ! kill -0 "${WORKER_PID}" >/dev/null 2>&1; then
		LAST_HTTP_CODE="WORKER_EXITED"
		LAST_BODY="$(cat "${WORKER_LOG_FILE}" 2>/dev/null || true)"
		fail_l7 "StartWorker" "worker exited immediately"
	fi
}

wait_delivery_succeeded_or_fail() {
	local deadline status attempts snapshot
	deadline="$(( $(date +%s) + WAIT_TIMEOUT_SEC ))"

	while true; do
		assert_worker_alive "WaitDeliverySucceeded"
		call_or_fail "GetDelivery" "GET" "${BASE_URL}/v1/notice-deliveries/${DELIVERY_ID}"
		status="$(extract_field "${LAST_BODY}" "status" || true)"
		attempts="$(extract_field "${LAST_BODY}" "attempts" || true)"
		[[ -z "${attempts}" ]] && attempts="0"
		snapshot="$(extract_field "${LAST_BODY}" "endpointURL" || true)"

		if [[ "${status}" == "succeeded" ]] && [[ "${attempts}" =~ ^[0-9]+$ ]] && (( attempts >= 1 )); then
			ATTEMPTS="${attempts}"
			if [[ -n "${snapshot}" ]]; then
				SNAPSHOT_ENDPOINT="${snapshot}"
			fi
			return 0
		fi

		if (( $(date +%s) > deadline )); then
			fail_l7 "WaitDeliverySucceeded" "status=${status:-EMPTY}, attempts=${attempts:-EMPTY}"
		fi
		sleep 1
	done
}

start_mock
precheck_notice_workers_or_fail

rand="${RAND:-$RANDOM}"
now_epoch="$(date -u +%s)"
old_endpoint="http://127.0.0.1:${MOCK_PORT}/webhook/old"
new_endpoint="http://127.0.0.1:${MOCK_PORT}/webhook/new"

create_channel_body=$(cat <<EOF
{"name":"p1-l7-notice-snapshot-${rand}","type":"webhook","enabled":true,"endpointURL":"${old_endpoint}","timeoutMs":1200,"maxRetries":3,"headers":{"X-Snapshot-Test":"v1"}}
EOF
)
call_or_fail "CreateNoticeChannel" POST "${BASE_URL}/v1/notice-channels" "${create_channel_body}"
CHANNEL_ID="$(extract_field "${LAST_BODY}" "channelID" "channel_id" || true)"
if [[ -z "${CHANNEL_ID}" ]]; then
	fail_l7 "CreateNoticeChannelParse" "channel_id missing"
fi

ingest_body=$(cat <<EOF
{"idempotencyKey":"idem-p1-l7-notice-ingest-${rand}","fingerprint":"p1-l7-notice-snapshot-fp-${rand}","status":"firing","severity":"P1","service":"notice-l7","cluster":"prod-l7","namespace":"default","workload":"checkout-api","lastSeenAt":{"seconds":${now_epoch},"nanos":0}}
EOF
)
call_or_fail "IngestAlertEvent" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_body}"
INCIDENT_ID="$(extract_field "${LAST_BODY}" "incidentID" "incident_id" || true)"
if [[ -z "${INCIDENT_ID}" ]]; then
	fail_l7 "IngestAlertEventParse" "incident_id missing"
fi

call_or_fail "ListPendingDelivery" GET "${BASE_URL}/v1/notice-deliveries?incident_id=${INCIDENT_ID}&channel_id=${CHANNEL_ID}&event_type=incident_created&status=pending&offset=0&limit=20"
if need_cmd jq; then
	DELIVERY_ID="$(printf '%s' "${LAST_BODY}" | jq -r '((.noticeDeliveries // .data.noticeDeliveries // []) | .[0].deliveryID // empty)' 2>/dev/null || true)"
else
	DELIVERY_ID="$(extract_field "${LAST_BODY}" "deliveryID" "delivery_id" || true)"
fi
if [[ -z "${DELIVERY_ID}" ]]; then
	fail_l7 "ListPendingDeliveryParse" "delivery_id missing"
fi

call_or_fail "GetPendingDeliverySnapshot" GET "${BASE_URL}/v1/notice-deliveries/${DELIVERY_ID}"
if need_cmd jq; then
	SNAPSHOT_ENDPOINT="$(printf '%s' "${LAST_BODY}" | jq -r '.noticeDelivery.snapshot.endpointURL // .data.noticeDelivery.snapshot.endpointURL // empty' 2>/dev/null || true)"
else
	SNAPSHOT_ENDPOINT="$(extract_field "${LAST_BODY}" "endpointURL" || true)"
fi
DELIVERY_IDEMPOTENCY_KEY="$(extract_field "${LAST_BODY}" "idempotencyKey" "idempotency_key" || true)"
if [[ -z "${DELIVERY_IDEMPOTENCY_KEY}" ]]; then
	fail_l7 "GetPendingDeliverySnapshotParseIdempotencyKey" "idempotency_key missing"
fi
if [[ -n "${SNAPSHOT_ENDPOINT}" ]] && [[ "${SNAPSHOT_ENDPOINT}" != "${old_endpoint}" ]]; then
	fail_l7 "AssertPendingSnapshotEndpoint" "snapshot endpoint expected old endpoint, got=${SNAPSHOT_ENDPOINT}"
fi

patch_channel_body=$(cat <<EOF
{"endpointURL":"${new_endpoint}"}
EOF
)
call_or_fail "PatchChannelToNewEndpoint" PATCH "${BASE_URL}/v1/notice-channels/${CHANNEL_ID}" "${patch_channel_body}"

start_worker
wait_delivery_succeeded_or_fail

MOCK_OLD_HITS="$(count_mock_path_by_key "/webhook/old" "${DELIVERY_IDEMPOTENCY_KEY}")"
MOCK_NEW_HITS="$(count_mock_path_by_key "/webhook/new" "${DELIVERY_IDEMPOTENCY_KEY}")"

if [[ ! "${MOCK_OLD_HITS}" =~ ^[0-9]+$ ]] || (( MOCK_OLD_HITS < 1 )); then
	fail_l7 "AssertOldEndpointHit" "old endpoint callback expected >=1, got=${MOCK_OLD_HITS:-EMPTY}"
fi
if [[ ! "${MOCK_NEW_HITS}" =~ ^[0-9]+$ ]]; then
	fail_l7 "AssertNewEndpointHitParse" "cannot parse new endpoint hit count"
fi
if (( MOCK_NEW_HITS != 0 )); then
	fail_l7 "AssertNewEndpointNotHit" "new endpoint callback expected 0, got=${MOCK_NEW_HITS}"
fi

call_or_fail "GetDeliveryFinalSnapshot" GET "${BASE_URL}/v1/notice-deliveries/${DELIVERY_ID}"
final_snapshot="$(extract_field "${LAST_BODY}" "endpointURL" || true)"
if [[ -n "${final_snapshot}" ]] && [[ "${final_snapshot}" != "${old_endpoint}" ]]; then
	fail_l7 "AssertFinalSnapshotEndpoint" "final snapshot endpoint expected old endpoint, got=${final_snapshot}"
fi

if [[ -n "${final_snapshot}" ]]; then
	SNAPSHOT_ENDPOINT="${final_snapshot}"
fi

echo "PASS L7-NOTICE-SNAPSHOT"
echo "channel_id=${CHANNEL_ID}"
echo "incident_id=${INCIDENT_ID}"
echo "delivery_id=${DELIVERY_ID}"
echo "delivery_idempotency_key=${DELIVERY_IDEMPOTENCY_KEY}"
echo "snapshot_endpoint=${SNAPSHOT_ENDPOINT:-${old_endpoint}}"
echo "mock_old_hits=${MOCK_OLD_HITS}"
echo "mock_new_hits=${MOCK_NEW_HITS}"
echo "attempts=${ATTEMPTS}"
