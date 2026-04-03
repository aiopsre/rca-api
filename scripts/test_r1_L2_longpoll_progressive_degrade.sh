#!/usr/bin/env bash
set -euo pipefail

CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
WAIT_TIMEOUT_SEC="${WAIT_TIMEOUT_SEC:-90}"

COMPOSE_FILE="${COMPOSE_FILE:-deploy/compose/docker-compose.redis.yaml}"
REDIS_SERVICE_NAME="${REDIS_SERVICE_NAME:-redis}"
REDIS_ADDR="${REDIS_ADDR:-127.0.0.1:6379}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
CONFIG_PATH="${CONFIG_PATH:-${REPO_ROOT}/configs/rca-apiserver.yaml}"
SERVER_CMD_BASE="${SERVER_CMD_BASE:-GOLANG_PROTOBUF_REGISTRATION_CONFLICT=warn go run ./cmd/rca-apiserver --config ${CONFIG_PATH}}"

PORT_A="${PORT_A:-$((19160 + RANDOM % 80))}"
PORT_B1="${PORT_B1:-$((PORT_A + 1))}"
PORT_B2="${PORT_B2:-$((PORT_A + 2))}"
PORT_C="${PORT_C:-$((PORT_A + 3))}"

BASE_A="http://127.0.0.1:${PORT_A}"
BASE_B1="http://127.0.0.1:${PORT_B1}"
BASE_B2="http://127.0.0.1:${PORT_B2}"
BASE_C="http://127.0.0.1:${PORT_C}"

LAST_HTTP_CODE=""
LAST_BODY=""
INCIDENT_ID_SAMPLE=""
JOB_ID_SAMPLE=""
SERVER_LOG=""
PIDS=()

POLL_PID=""
POLL_BODY_FILE=""
POLL_CODE_FILE=""
POLL_ERR_FILE=""
POLL_STARTED_NS=""

REDIS_STOPPED="0"

truncate_2kb() {
	printf '%s' "${1:-}" | head -c 2048
}

need_cmd() {
	command -v "$1" >/dev/null 2>&1
}

fail_step() {
	local step="$1"
	local code="${2:-${LAST_HTTP_CODE:-UNKNOWN}}"
	local body="${3:-${LAST_BODY:-}}"
	echo "FAIL R1_L2 step=${step}"
	echo "http_code=${code}"
	echo "response_body<<EOF"
	truncate_2kb "${body}"
	echo
	echo "EOF"
	echo "incident_id=${INCIDENT_ID_SAMPLE:-NONE}"
	echo "job_id=${JOB_ID_SAMPLE:-NONE}"
	if [[ -n "${SERVER_LOG}" ]]; then
		echo "server_log_tail<<EOF"
		tail -n 120 "${SERVER_LOG}" 2>/dev/null | head -c 2048
		echo
		echo "EOF"
	fi
	exit 1
}

compose_or_fail() {
	local step="$1"
	shift

	local tmp rc
	tmp="$(mktemp)"
	set +e
	(
		cd "${REPO_ROOT}" && \
			docker compose -f "${COMPOSE_FILE}" "$@"
	) >"${tmp}" 2>&1
	rc=$?
	set -e
	LAST_BODY="$(cat "${tmp}")"
	rm -f "${tmp}"
	if (( rc != 0 )); then
		LAST_HTTP_CODE="COMPOSE_${rc}"
		fail_step "${step}"
	fi
}

http_json() {
	local method="$1"
	local url="$2"
	local body="${3:-}"
	local timeout_sec="${4:-20}"

	local tmp_body tmp_err code rc
	tmp_body="$(mktemp)"
	tmp_err="$(mktemp)"

	local -a cmd
	cmd=("${CURL}" -sS --max-time "${timeout_sec}" -o "${tmp_body}" -w "%{http_code}" -X "${method}" "${url}" -H "Accept: application/json" -H "X-Scopes: ${SCOPES}")
	if [[ -n "${body}" ]]; then
		cmd+=(-H "Content-Type: application/json" -d "${body}")
	fi

	set +e
	code="$("${cmd[@]}" 2>"${tmp_err}")"
	rc=$?
	set -e

	LAST_BODY="$(cat "${tmp_body}")"
	LAST_HTTP_CODE="${code}"
	local curl_err
	curl_err="$(cat "${tmp_err}")"
	rm -f "${tmp_body}" "${tmp_err}"
	if (( rc != 0 )); then
		LAST_HTTP_CODE="CURL_${rc}"
		LAST_BODY="${curl_err}"
		return 1
	fi
	return 0
}

call_or_fail() {
	local step="$1"
	local method="$2"
	local url="$3"
	local body="${4:-}"
	local timeout="${5:-20}"

	if ! http_json "${method}" "${url}" "${body}" "${timeout}"; then
		fail_step "${step}"
	fi
	if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
		fail_step "${step}"
	fi
}

json_get() {
	local json="$1"
	local expr="$2"
	printf '%s' "${json}" | jq -r "${expr}" 2>/dev/null
}

start_server_or_fail() {
	local step="$1"
	local port="$2"
	local redis_enabled="$3"
	local env_prefix="${4:-}"

	local log_file
	log_file="$(mktemp)"
	SERVER_LOG="${log_file}"
	(
		cd "${REPO_ROOT}" && \
			bash -lc "${env_prefix} ${SERVER_CMD_BASE} --http.addr=127.0.0.1:${port} --redis.enabled=${redis_enabled} --redis.addr=${REDIS_ADDR} --redis.fail_open=true --redis.pubsub.enabled=true"
	) >"${log_file}" 2>&1 &
	local pid="$!"
	PIDS+=("${pid}")

	local deadline="$(( $(date +%s) + WAIT_TIMEOUT_SEC ))"
	while true; do
		if "${CURL}" -sS "http://127.0.0.1:${port}/healthz" >/dev/null 2>&1; then
			return 0
		fi
		if ! kill -0 "${pid}" >/dev/null 2>&1; then
			LAST_HTTP_CODE="SERVER_EXITED"
			LAST_BODY="$(cat "${log_file}" 2>/dev/null || true)"
			fail_step "${step}"
		fi
		if (( $(date +%s) > deadline )); then
			LAST_HTTP_CODE="SERVER_TIMEOUT"
			LAST_BODY="$(cat "${log_file}" 2>/dev/null || true)"
			fail_step "${step}"
		fi
		sleep 0.5
	done
}

stop_servers() {
	local pid
	for pid in "${PIDS[@]:-}"; do
		kill "${pid}" >/dev/null 2>&1 || true
		wait "${pid}" >/dev/null 2>&1 || true
	done
	PIDS=()
}

wait_pubsub_ready_metric_or_fail() {
	local step="$1"
	local base_url="$2"
	local deadline="$(( $(date +%s) + 30 ))"
	while true; do
		if http_json GET "${base_url}/metrics" "" 10; then
			if [[ "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]] && printf '%s\n' "${LAST_BODY}" | awk '$1 ~ /^redis_pubsub_subscribe_ready(\{|$)/ && $NF + 0 >= 1 {ok=1} END {exit(ok ? 0 : 1)}'; then
				return 0
			fi
		fi
		if (( $(date +%s) > deadline )); then
			fail_step "${step}" "ASSERT_PUBSUB_READY" "${LAST_BODY}"
		fi
		sleep 1
	done
}

create_incident_or_fail() {
	local step="$1"
	local base_url="$2"
	local service="$3"
	local body
	body="$(cat <<JSON
{"namespace":"default","workloadKind":"Deployment","workloadName":"${service}","service":"${service}","severity":"P1"}
JSON
)"
	call_or_fail "${step}" POST "${base_url}/v1/incidents" "${body}"
	local id
	id="$(json_get "${LAST_BODY}" '.incidentID // .data.incidentID // empty')"
	if [[ -z "${id}" ]]; then
		fail_step "${step}.ParseIncidentID" "ASSERT_INCIDENT_ID" "${LAST_BODY}"
	fi
	printf '%s' "${id}"
}

run_ai_job_or_fail() {
	local step="$1"
	local base_url="$2"
	local incident_id="$3"
	local now_epoch
	now_epoch="$(date -u +%s)"
	local body
	body="$(cat <<JSON
{"incidentID":"${incident_id}","idempotencyKey":"idem-r1-l2-${incident_id}-$(date -u +%s%N)","timeRangeStart":{"seconds":$((now_epoch - 1200)),"nanos":0},"timeRangeEnd":{"seconds":${now_epoch},"nanos":0}}
JSON
)"
	call_or_fail "${step}" POST "${base_url}/v1/incidents/${incident_id}/ai:run" "${body}"
	JOB_ID_SAMPLE="$(json_get "${LAST_BODY}" '.jobID // .data.jobID // empty')"
	if [[ -z "${JOB_ID_SAMPLE}" ]]; then
		fail_step "${step}.ParseJobID" "ASSERT_JOB_ID" "${LAST_BODY}"
	fi
}

start_longpoll_async() {
	local step="$1"
	local base_url="$2"
	local wait_seconds="$3"
	POLL_BODY_FILE="$(mktemp)"
	POLL_CODE_FILE="$(mktemp)"
	POLL_ERR_FILE="$(mktemp)"
	POLL_STARTED_NS="$(date +%s%N)"

	(
		set +e
		"${CURL}" -sS -o "${POLL_BODY_FILE}" -w "%{http_code}" \
			-H "Accept: application/json" -H "X-Scopes: ${SCOPES}" \
			"${base_url}/v1/ai/jobs?status=queued&offset=0&limit=10&wait_seconds=${wait_seconds}" \
			>"${POLL_CODE_FILE}" 2>"${POLL_ERR_FILE}"
		echo "$?" >"${POLL_ERR_FILE}.rc"
	) &
	POLL_PID="$!"
}

wait_longpoll_or_fail() {
	local step="$1"
	local timeout_sec="$2"
	local deadline="$(( $(date +%s) + timeout_sec ))"
	while true; do
		if ! kill -0 "${POLL_PID}" >/dev/null 2>&1; then
			wait "${POLL_PID}" >/dev/null 2>&1 || true
			local rc code err
			rc="$(cat "${POLL_ERR_FILE}.rc" 2>/dev/null || echo 1)"
			code="$(cat "${POLL_CODE_FILE}" 2>/dev/null || echo "")"
			err="$(cat "${POLL_ERR_FILE}" 2>/dev/null || true)"
			LAST_BODY="$(cat "${POLL_BODY_FILE}" 2>/dev/null || true)"
			if [[ "${rc}" != "0" ]]; then
				LAST_HTTP_CODE="CURL_${rc}"
				fail_step "${step}" "${LAST_HTTP_CODE}" "${err}"
			fi
			LAST_HTTP_CODE="${code}"
			if [[ ! "${code}" =~ ^2[0-9][0-9]$ ]]; then
				fail_step "${step}"
			fi
			POLL_ELAPSED_MS="$(( ( $(date +%s%N) - POLL_STARTED_NS ) / 1000000 ))"
			return 0
		fi
		if (( $(date +%s) > deadline )); then
			kill "${POLL_PID}" >/dev/null 2>&1 || true
			wait "${POLL_PID}" >/dev/null 2>&1 || true
			LAST_HTTP_CODE="POLL_TIMEOUT"
			LAST_BODY="$(cat "${POLL_BODY_FILE}" 2>/dev/null || true)"
			fail_step "${step}"
		fi
		sleep 0.2
	done
}

assert_longpoll_has_jobs_or_fail() {
	local step="$1"
	local count
	count="$(json_get "${LAST_BODY}" '(.jobs // .data.jobs // []) | length')"
	if [[ -z "${count}" || ! "${count}" =~ ^[0-9]+$ || "${count}" == "0" ]]; then
		fail_step "${step}" "ASSERT_JOBS_EMPTY" "${LAST_BODY}"
	fi
}

assert_metric_reason_or_fail() {
	local step="$1"
	local base_url="$2"
	local reason="$3"
	call_or_fail "${step}.Metrics" GET "${base_url}/metrics"
	if ! printf '%s\n' "${LAST_BODY}" | awk -v r="${reason}" '$1 ~ /^ai_job_longpoll_fallback_total\{/ && $0 ~ ("reason=\"" r "\"") && $NF + 0 >= 1 {ok=1} END {exit(ok ? 0 : 1)}'; then
		fail_step "${step}" "ASSERT_METRIC_REASON_${reason}" "${LAST_BODY}"
	fi
}

cleanup() {
	stop_servers
	rm -f "${POLL_BODY_FILE:-}" "${POLL_CODE_FILE:-}" "${POLL_ERR_FILE:-}" "${POLL_ERR_FILE:-}.rc"
	if [[ "${REDIS_STOPPED}" == "1" ]]; then
		(
			cd "${REPO_ROOT}" && \
				docker compose -f "${COMPOSE_FILE}" start "${REDIS_SERVICE_NAME}"
		) >/dev/null 2>&1 || true
	fi
}
trap cleanup EXIT

if ! need_cmd jq; then
	fail_step "Precheck.MissingJQ" "MISSING_JQ" "jq is required"
fi
if ! need_cmd docker; then
	fail_step "Precheck.MissingDocker" "MISSING_DOCKER" "docker is required"
fi

compose_or_fail "Precheck.RedisUp" up -d "${REDIS_SERVICE_NAME}"
REDIS_STOPPED="0"

rand="${RANDOM}"

# A: Redis healthy -> L1 quick wake.
start_server_or_fail "A.StartServer" "${PORT_A}" "true"
wait_pubsub_ready_metric_or_fail "A.WaitPubSubReady" "${BASE_A}"
incident_a="$(create_incident_or_fail "A.CreateIncident" "${BASE_A}" "svc-r1-l2-a-${rand}")"
INCIDENT_ID_SAMPLE="${incident_a}"
start_longpoll_async "A.StartLongPoll" "${BASE_A}" 5
sleep 0.3
run_ai_job_or_fail "A.RunAIJob" "${BASE_A}" "${incident_a}"
wait_longpoll_or_fail "A.WaitLongPoll" 12
assert_longpoll_has_jobs_or_fail "A.AssertLongPollJobs"
if (( POLL_ELAPSED_MS >= 4500 )); then
	fail_step "A.AssertL1FastWake" "ASSERT_ELAPSED_${POLL_ELAPSED_MS}ms" "${LAST_BODY}"
fi
echo "PASS R1_L2 step=A.RedisHealthyL1"
stop_servers

# B: Redis unavailable -> L2 watermark wake around ~1s.
compose_or_fail "B.StopRedis" stop "${REDIS_SERVICE_NAME}"
REDIS_STOPPED="1"
start_server_or_fail "B.StartServerB1" "${PORT_B1}" "true"
start_server_or_fail "B.StartServerB2" "${PORT_B2}" "true"
incident_b="$(create_incident_or_fail "B.CreateIncident" "${BASE_B1}" "svc-r1-l2-b-${rand}")"
INCIDENT_ID_SAMPLE="${incident_b}"
start_longpoll_async "B.StartLongPoll" "${BASE_B2}" 6
sleep 0.3
run_ai_job_or_fail "B.RunAIJob" "${BASE_B1}" "${incident_b}"
wait_longpoll_or_fail "B.WaitLongPoll" 12
assert_longpoll_has_jobs_or_fail "B.AssertLongPollJobs"
if (( POLL_ELAPSED_MS < 700 || POLL_ELAPSED_MS > 3500 )); then
	fail_step "B.AssertL2Approx1sWake" "ASSERT_ELAPSED_${POLL_ELAPSED_MS}ms" "${LAST_BODY}"
fi
assert_metric_reason_or_fail "B.AssertFallbackMetric" "${BASE_B2}" "redis_unavailable"
echo "PASS R1_L2 step=B.RedisDownL2"
stop_servers

# C: Self-protect threshold -> L3 fallback reason observed.
start_server_or_fail "C.StartServer" "${PORT_C}" "false" "RCA_AI_JOB_LONGPOLL_MAX_POLLING_WAITERS=1"
incident_c="$(create_incident_or_fail "C.CreateIncident" "${BASE_C}" "svc-r1-l2-c-${rand}")"
INCIDENT_ID_SAMPLE="${incident_c}"

# long-poll #1 keeps one waiter active.
poll1_body="$(mktemp)"
poll1_code="$(mktemp)"
poll1_err="$(mktemp)"
poll1_rc="${poll1_err}.rc"
poll1_started_ns="$(date +%s%N)"
(
	set +e
	"${CURL}" -sS -o "${poll1_body}" -w "%{http_code}" \
		-H "Accept: application/json" -H "X-Scopes: ${SCOPES}" \
		"${BASE_C}/v1/ai/jobs?status=queued&offset=0&limit=10&wait_seconds=3" \
		>"${poll1_code}" 2>"${poll1_err}"
	echo "$?" >"${poll1_rc}"
) &
poll1_pid="$!"
sleep 0.2

# long-poll #2 should hit self-protect level3 when threshold=1.
start_longpoll_async "C.StartLongPoll2" "${BASE_C}" 2
wait_longpoll_or_fail "C.WaitLongPoll2" 8
assert_metric_reason_or_fail "C.AssertL3Metric" "${BASE_C}" "adaptive_l3_waiters"

deadline_c1="$(( $(date +%s) + 8 ))"
while true; do
	if ! kill -0 "${poll1_pid}" >/dev/null 2>&1; then
		wait "${poll1_pid}" >/dev/null 2>&1 || true
		rc1="$(cat "${poll1_rc}" 2>/dev/null || echo 1)"
		code1="$(cat "${poll1_code}" 2>/dev/null || echo "")"
		body1="$(cat "${poll1_body}" 2>/dev/null || true)"
		if [[ "${rc1}" != "0" || ! "${code1}" =~ ^2[0-9][0-9]$ ]]; then
			LAST_HTTP_CODE="${code1:-CURL_${rc1}}"
			LAST_BODY="${body1}"
			fail_step "C.WaitLongPoll1"
		fi
		break
	fi
	if (( $(date +%s) > deadline_c1 )); then
		kill "${poll1_pid}" >/dev/null 2>&1 || true
		wait "${poll1_pid}" >/dev/null 2>&1 || true
		fail_step "C.WaitLongPoll1" "POLL_TIMEOUT" "$(cat "${poll1_body}" 2>/dev/null || true)"
	fi
	sleep 0.2
done

rm -f "${poll1_body}" "${poll1_code}" "${poll1_err}" "${poll1_rc}"

assert_metric_reason_or_fail "C.AssertL3Metric" "${BASE_C}" "adaptive_l3_waiters"
if ! grep -Fq "adaptive self protect" "${SERVER_LOG}"; then
	fail_step "C.AssertL3Log" "ASSERT_LOG_MISSING" "missing adaptive self protect"
fi
echo "PASS R1_L2 step=C.SelfProtectL3"

echo "PASS R1_L2 longpoll progressive degrade"
