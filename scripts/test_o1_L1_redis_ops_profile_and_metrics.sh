#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
DEBUG="${DEBUG:-0}"
WAIT_TIMEOUT_SEC="${WAIT_TIMEOUT_SEC:-40}"
MOCK_PORT="${MOCK_PORT:-$((19500 + RANDOM % 500))}"

LAST_HTTP_CODE=""
LAST_BODY=""
INCIDENT_ID=""
JOB_ID=""
DELIVERY_ID=""
CHANNEL_ID=""

MOCK_PID=""
MOCK_LOG_FILE=""

required_metrics=(
	"redis_pubsub_subscribe_ready"
	"redis_pubsub_publish_total"
	"ai_job_longpoll_fallback_total"
	"redis_stream_consume_total"
	"notice_worker_claim_source_total"
	"notice_limiter_allow_total"
	"notice_limiter_deny_total"
	"notice_limiter_fallback_total"
)

debug() {
	if [[ "${DEBUG}" == "1" ]]; then
		echo "[DEBUG] $*" >&2
	fi
}

truncate_2kb() {
	printf '%s' "$1" | head -c 2048
}

fail_step() {
	local step="$1"
	local code="${2:-${LAST_HTTP_CODE}}"
	local body="${3:-${LAST_BODY}}"

	echo "FAIL O1 step=${step}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	truncate_2kb "${body}"
	echo
	echo "EOF"
	echo "incident_id=${INCIDENT_ID:-NONE}"
	echo "job_id=${JOB_ID:-NONE}"
	echo "delivery_id=${DELIVERY_ID:-NONE}"
	if [[ -n "${MOCK_LOG_FILE:-}" ]]; then
		echo "mock_log_tail<<EOF"
		tail -n 40 "${MOCK_LOG_FILE}" 2>/dev/null | head -c 2048
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
				(.[$k] // .data[$k] // .incident[$k] // .data.incident[$k] // .job[$k] // .data.job[$k] // .noticeChannel[$k] // .data.noticeChannel[$k]) |
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

assert_metric_exists() {
	local step="$1"
	local metrics_body="$2"
	local metric_name="$3"
	if ! printf '%s\n' "${metrics_body}" | grep -F "${metric_name}" >/dev/null 2>&1; then
		fail_step "${step}" "MISSING_METRIC" "metric=${metric_name}"
	fi
}

metric_sum() {
	local metrics_body="$1"
	local metric_name="$2"
	printf '%s\n' "${metrics_body}" | awk -v name="${metric_name}" '
		$1 ~ ("^" name "(\\{|$)") {sum += $NF}
		END {printf "%.6f", sum + 0}
	'
}

start_mock() {
	local pybin
	if command -v python3 >/dev/null 2>&1; then
		pybin="python3"
	elif command -v python >/dev/null 2>&1; then
		pybin="python"
	else
		fail_step "Precheck.MissingPython" "MISSING_PYTHON" "python is required"
	fi

	MOCK_LOG_FILE="$(mktemp)"
	"${pybin}" -u - <<'PY' "${MOCK_PORT}" >"${MOCK_LOG_FILE}" 2>&1 &
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

port = int(sys.argv[1])

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
        _ = self.rfile.read(length)
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

cleanup() {
	if [[ -n "${MOCK_PID:-}" ]]; then
		kill "${MOCK_PID}" >/dev/null 2>&1 || true
		wait "${MOCK_PID}" >/dev/null 2>&1 || true
	fi
	rm -f "${MOCK_LOG_FILE:-}"
}
trap cleanup EXIT

if ! command -v jq >/dev/null 2>&1; then
	fail_step "Precheck.MissingJQ" "MISSING_JQ" "jq is required"
fi

start_mock

call_or_fail "Precheck.Health" GET "${BASE_URL}/healthz"

call_or_fail "Metrics.Before" GET "${BASE_URL}/metrics"
metrics_before="${LAST_BODY}"
for m in "${required_metrics[@]}"; do
	assert_metric_exists "Metrics.Before.${m}" "${metrics_before}" "${m}"
done
publish_before="$(metric_sum "${metrics_before}" "redis_pubsub_publish_total")"
claim_before="$(metric_sum "${metrics_before}" "notice_worker_claim_source_total")"
allow_before="$(metric_sum "${metrics_before}" "notice_limiter_allow_total")"

rand="${RANDOM}"
now_epoch="$(date -u +%s)"
incident_body=$(cat <<JSON
{"namespace":"default","workloadKind":"Deployment","workloadName":"o1-workload-${rand}","service":"o1-svc-${rand}","severity":"P1"}
JSON
)
call_or_fail "CreateIncident" POST "${BASE_URL}/v1/incidents" "${incident_body}"
INCIDENT_ID="$(extract_field "${LAST_BODY}" "incidentID" "incident_id" || true)"
if [[ -z "${INCIDENT_ID}" ]]; then
	fail_step "CreateIncident.ParseIncidentID"
fi

run_body=$(cat <<JSON
{"incidentID":"${INCIDENT_ID}","idempotencyKey":"idem-o1-ai-run-${rand}","pipeline":"basic_rca","trigger":"manual","timeRangeStart":{"seconds":$((now_epoch - 1800)),"nanos":0},"timeRangeEnd":{"seconds":${now_epoch},"nanos":0},"createdBy":"o1-script"}
JSON
)
call_or_fail "RunAIJob" POST "${BASE_URL}/v1/incidents/${INCIDENT_ID}/ai:run" "${run_body}"
JOB_ID="$(extract_field "${LAST_BODY}" "jobID" "job_id" || true)"
if [[ -z "${JOB_ID}" ]]; then
	fail_step "RunAIJob.ParseJobID"
fi

call_or_fail "Metrics.AfterAI" GET "${BASE_URL}/metrics"
metrics_after_ai="${LAST_BODY}"
publish_after="$(metric_sum "${metrics_after_ai}" "redis_pubsub_publish_total")"
if ! awk -v before="${publish_before}" -v after="${publish_after}" 'BEGIN{exit !(after > before)}'; then
	fail_step "Metrics.PublishNotIncreased" "ASSERT_FAILED" "before=${publish_before} after=${publish_after}"
fi

channel_body=$(cat <<JSON
{"name":"o1-channel-${rand}","type":"webhook","enabled":true,"endpointURL":"http://127.0.0.1:${MOCK_PORT}/notice","timeoutMs":1000,"maxRetries":3}
JSON
)
call_or_fail "CreateNoticeChannel" POST "${BASE_URL}/v1/notice-channels" "${channel_body}"
CHANNEL_ID="$(extract_field "${LAST_BODY}" "channelID" "channel_id" || true)"
if [[ -z "${CHANNEL_ID}" ]]; then
	fail_step "CreateNoticeChannel.ParseChannelID"
fi

ingest_body=$(cat <<JSON
{"idempotencyKey":"idem-o1-notice-${rand}","fingerprint":"o1-fp-${rand}","status":"firing","severity":"P1","service":"o1-svc-notice","cluster":"prod-o1","namespace":"default","workload":"o1-workload","lastSeenAt":{"seconds":${now_epoch},"nanos":0}}
JSON
)
call_or_fail "IngestAlertForNotice" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_body}"
notice_incident_id="$(extract_field "${LAST_BODY}" "incidentID" "incident_id" || true)"
if [[ -n "${notice_incident_id}" ]]; then
	INCIDENT_ID="${notice_incident_id}"
fi

local_deadline="$(( $(date +%s) + WAIT_TIMEOUT_SEC ))"
while true; do
	call_or_fail "ListNoticeDeliveries" GET "${BASE_URL}/v1/notice-deliveries?incident_id=${INCIDENT_ID}&channel_id=${CHANNEL_ID}&event_type=incident_created&offset=0&limit=20"
	DELIVERY_ID="$(printf '%s' "${LAST_BODY}" | jq -r '(.noticeDeliveries // .data.noticeDeliveries // [])[0] | (.deliveryID // .delivery_id // empty)' 2>/dev/null || true)"
	if [[ -n "${DELIVERY_ID}" ]]; then
		break
	fi
	if (( $(date +%s) > local_deadline )); then
		fail_step "ListNoticeDeliveries.Timeout" "TIMEOUT" "delivery not found in notice-deliveries"
	fi
	sleep 1
done

call_or_fail "Metrics.AfterNotice" GET "${BASE_URL}/metrics"
metrics_after_notice="${LAST_BODY}"
for m in "${required_metrics[@]}"; do
	assert_metric_exists "Metrics.AfterNotice.${m}" "${metrics_after_notice}" "${m}"
done
claim_after="$(metric_sum "${metrics_after_notice}" "notice_worker_claim_source_total")"
allow_after="$(metric_sum "${metrics_after_notice}" "notice_limiter_allow_total")"

if awk -v cb="${claim_before}" -v ca="${claim_after}" -v ab="${allow_before}" -v aa="${allow_after}" 'BEGIN{exit !((ca > cb) || (aa > ab))}'; then
	echo "notice_metric_delta=1"
else
	echo "notice_metric_delta=0"
fi

echo "publish_before=${publish_before}"
echo "publish_after=${publish_after}"
echo "claim_before=${claim_before}"
echo "claim_after=${claim_after}"
echo "allow_before=${allow_before}"
echo "allow_after=${allow_after}"
echo "incident_id=${INCIDENT_ID}"
echo "job_id=${JOB_ID}"
echo "delivery_id=${DELIVERY_ID}"
echo "PASS O1 redis ops profile and metrics"
