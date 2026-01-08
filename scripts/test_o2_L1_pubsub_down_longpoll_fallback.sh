#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
METRICS_URL="${METRICS_URL:-${BASE_URL}/metrics}"
COMPOSE_FILE="${COMPOSE_FILE:-deploy/compose/docker-compose.redis.yaml}"
REDIS_SERVICE_NAME="${REDIS_SERVICE_NAME:-redis}"
SCOPES="${SCOPES:-*}"
CURL="${CURL:-curl}"
DEBUG="${DEBUG:-0}"

WAIT_SECONDS="${WAIT_SECONDS:-12}"
POLL_TIMEOUT_SEC="${POLL_TIMEOUT_SEC:-35}"
METRIC_WAIT_TIMEOUT_SEC="${METRIC_WAIT_TIMEOUT_SEC:-60}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

LAST_HTTP_CODE=""
LAST_BODY=""
INCIDENT_ID=""
JOB_ID=""
REDIS_STOPPED="0"

POLL_BODY_FILE=""
POLL_CODE_FILE=""
POLL_ERR_FILE=""

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

	echo "FAIL O2-L1 step=${step}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	truncate_2kb "${body}"
	echo
	echo "EOF"
	echo "incident_id=${INCIDENT_ID:-NONE}"
	echo "job_id=${JOB_ID:-NONE}"
	echo "base_url=${BASE_URL}"
	echo "metrics_url=${METRICS_URL}"
	echo "compose_file=${COMPOSE_FILE}"
	echo "redis_service=${REDIS_SERVICE_NAME}"
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
	LAST_HTTP_CODE="200"
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
					(.[$k] // .data[$k] // .incident[$k] // .data.incident[$k] // .job[$k] // .data.job[$k]) |
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

extract_total_count() {
	local json="$1"
	local value
	if need_cmd jq; then
		value="$(
			printf '%s' "${json}" | jq -r '
				(.totalCount // .total_count // .data.totalCount // .data.total_count) |
				if . == null then empty
				elif (type == "number") then tostring
				elif (type == "string") then .
				else empty
				end
			' 2>/dev/null
		)"
	else
		value="$(printf '%s' "${json}" | sed -n 's/.*"totalCount":[[:space:]]*\([0-9][0-9]*\).*/\1/p' | head -n 1)"
		if [[ -z "${value}" ]]; then
			value="$(printf '%s' "${json}" | sed -n 's/.*"total_count":[[:space:]]*\([0-9][0-9]*\).*/\1/p' | head -n 1)"
		fi
	fi
	if [[ "${value:-}" =~ ^[0-9]+$ ]]; then
		printf '%s' "${value}"
		return 0
	fi
	return 1
}

response_has_job_id() {
	local json="$1"
	local target_job_id="$2"
	if [[ -z "${target_job_id}" ]]; then
		return 1
	fi
	if need_cmd jq; then
		printf '%s' "${json}" | jq -e --arg id "${target_job_id}" '
			((.jobs // .data.jobs // []) | any((.jobID // .job_id // "") == $id))
		' >/dev/null 2>&1
		return $?
	fi
	printf '%s' "${json}" | grep -F "\"jobID\":\"${target_job_id}\"" >/dev/null 2>&1 && return 0
	printf '%s' "${json}" | grep -F "\"job_id\":\"${target_job_id}\"" >/dev/null 2>&1 && return 0
	return 1
}

metric_exists() {
	local metrics_body="$1"
	local metric_name="$2"
	printf '%s\n' "${metrics_body}" | awk -v name="${metric_name}" '$1 ~ ("^" name "(\\{|$)") {found=1} END {exit(found ? 0 : 1)}'
}

metric_sum() {
	local metrics_body="$1"
	local metric_name="$2"
	printf '%s\n' "${metrics_body}" | awk -v name="${metric_name}" '
		$1 ~ ("^" name "(\\{|$)") {sum += $NF}
		END {printf "%.6f", sum + 0}
	'
}

wait_pubsub_ready_or_present() {
	local timeout_sec="$1"
	local deadline metrics_body found ready
	deadline="$(( $(date +%s) + timeout_sec ))"
	found="0"
	ready="0"

	while true; do
		if http_json GET "${METRICS_URL}"; then
			if [[ "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
				metrics_body="${LAST_BODY}"
				if metric_exists "${metrics_body}" "redis_pubsub_subscribe_ready"; then
					found="1"
				fi
				if printf '%s\n' "${metrics_body}" | awk '$1 ~ /^redis_pubsub_subscribe_ready(\{|$)/ && $NF + 0 >= 1 {ok=1} END {exit(ok ? 0 : 1)}'; then
					ready="1"
					break
				fi
			fi
		fi

		if (( $(date +%s) > deadline )); then
			break
		fi
		sleep 1
	done

	if [[ "${ready}" == "1" ]]; then
		PUBSUB_READY="1"
		return 0
	fi
	if [[ "${found}" == "1" ]]; then
		PUBSUB_READY="0"
		return 0
	fi
	LAST_HTTP_CODE="MISSING_METRIC"
	LAST_BODY="metric=redis_pubsub_subscribe_ready"
	fail_step "WaitPubSubReadyOrPresent"
}

cleanup() {
	if [[ "${REDIS_STOPPED}" == "1" ]]; then
		set +e
		(
			cd "${REPO_ROOT}" && \
				docker compose -f "${COMPOSE_FILE}" start "${REDIS_SERVICE_NAME}"
		) >/dev/null 2>&1
		set -e
	fi
	rm -f "${POLL_BODY_FILE:-}" "${POLL_CODE_FILE:-}" "${POLL_ERR_FILE:-}"
}
trap cleanup EXIT

if ! need_cmd docker; then
	fail_step "Precheck.MissingDocker" "MISSING_DOCKER" "docker is required"
fi
if ! need_cmd jq; then
	fail_step "Precheck.MissingJQ" "MISSING_JQ" "jq is required"
fi

call_or_fail "Precheck.Health" GET "${BASE_URL}/healthz"
wait_pubsub_ready_or_present "${METRIC_WAIT_TIMEOUT_SEC}"

call_or_fail "Metrics.Before" GET "${METRICS_URL}"
metrics_before="${LAST_BODY}"
if ! metric_exists "${metrics_before}" "ai_job_longpoll_fallback_total"; then
	fail_step "Metrics.Before.FallbackMissing" "MISSING_METRIC" "metric=ai_job_longpoll_fallback_total"
fi
fallback_before="$(metric_sum "${metrics_before}" "ai_job_longpoll_fallback_total")"

compose_or_fail "StopRedis" stop "${REDIS_SERVICE_NAME}"
REDIS_STOPPED="1"
sleep 1

call_or_fail "Baseline.ListAIJobs" GET "${BASE_URL}/v1/ai/jobs?status=queued&offset=0&limit=1"
baseline_total="$(extract_total_count "${LAST_BODY}" || true)"
if [[ -z "${baseline_total}" ]]; then
	baseline_total="0"
fi
if [[ ! "${baseline_total}" =~ ^[0-9]+$ ]]; then
	fail_step "Baseline.ParseTotal" "ASSERT_FAILED" "baseline_total=${baseline_total}"
fi

POLL_BODY_FILE="$(mktemp)"
POLL_CODE_FILE="$(mktemp)"
POLL_ERR_FILE="$(mktemp)"
(
	set +e
	declare -a poll_cmd
	poll_cmd=("${CURL}" -sS -o "${POLL_BODY_FILE}" -w "%{http_code}" -H "Accept: application/json")
	if [[ -n "${SCOPES}" ]]; then
		poll_cmd+=(-H "X-Scopes: ${SCOPES}")
	fi
	poll_cmd+=("${BASE_URL}/v1/ai/jobs?status=queued&offset=${baseline_total}&limit=10&wait_seconds=${WAIT_SECONDS}")
	code="$("${poll_cmd[@]}" 2>"${POLL_ERR_FILE}")"
	rc=$?
	set -e
	if (( rc != 0 )); then
		echo "CURL_${rc}" >"${POLL_CODE_FILE}"
	else
		echo "${code}" >"${POLL_CODE_FILE}"
	fi
) &
poll_pid="$!"

sleep 1

rand="${RANDOM}"
now_epoch="$(date -u +%s)"
incident_body=$(cat <<JSON
{"namespace":"default","workloadKind":"Deployment","workloadName":"o2-l1-workload-${rand}","service":"o2-l1-svc-${rand}","severity":"P1"}
JSON
)
call_or_fail "CreateIncident" POST "${BASE_URL}/v1/incidents" "${incident_body}"
INCIDENT_ID="$(extract_field "${LAST_BODY}" "incidentID" "incident_id" || true)"
if [[ -z "${INCIDENT_ID}" ]]; then
	fail_step "CreateIncident.ParseIncidentID"
fi

run_body=$(cat <<JSON
{"incidentID":"${INCIDENT_ID}","idempotencyKey":"idem-o2-l1-ai-run-${rand}","pipeline":"basic_rca","trigger":"manual","timeRangeStart":{"seconds":$((now_epoch - 1800)),"nanos":0},"timeRangeEnd":{"seconds":${now_epoch},"nanos":0},"createdBy":"o2-l1-script"}
JSON
)
call_or_fail "RunAIJob" POST "${BASE_URL}/v1/incidents/${INCIDENT_ID}/ai:run" "${run_body}"
JOB_ID="$(extract_field "${LAST_BODY}" "jobID" "job_id" || true)"
if [[ -z "${JOB_ID}" ]]; then
	fail_step "RunAIJob.ParseJobID"
fi

deadline="$(( $(date +%s) + POLL_TIMEOUT_SEC ))"
while kill -0 "${poll_pid}" >/dev/null 2>&1; do
	if (( $(date +%s) > deadline )); then
		kill "${poll_pid}" >/dev/null 2>&1 || true
		wait "${poll_pid}" >/dev/null 2>&1 || true
		LAST_HTTP_CODE="TIMEOUT"
		LAST_BODY="$(cat "${POLL_BODY_FILE}" 2>/dev/null || true)"
		fail_step "LongPoll.Timeout"
	fi
	sleep 0.2
done
wait "${poll_pid}" >/dev/null 2>&1 || true

LAST_HTTP_CODE="$(cat "${POLL_CODE_FILE}" 2>/dev/null || true)"
LAST_BODY="$(cat "${POLL_BODY_FILE}" 2>/dev/null || true)"
poll_err="$(cat "${POLL_ERR_FILE}" 2>/dev/null || true)"
if [[ -n "${poll_err}" ]]; then
	if [[ -n "${LAST_BODY}" ]]; then
		LAST_BODY="${LAST_BODY}"$'\n'"${poll_err}"
	else
		LAST_BODY="${poll_err}"
	fi
fi
if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
	fail_step "LongPoll.HTTPCode"
fi
if ! response_has_job_id "${LAST_BODY}" "${JOB_ID}"; then
	fail_step "LongPoll.MissingCreatedJob"
fi

call_or_fail "Metrics.After" GET "${METRICS_URL}"
metrics_after="${LAST_BODY}"
if ! metric_exists "${metrics_after}" "ai_job_longpoll_fallback_total"; then
	fail_step "Metrics.After.FallbackMissing" "MISSING_METRIC" "metric=ai_job_longpoll_fallback_total"
fi
fallback_after="$(metric_sum "${metrics_after}" "ai_job_longpoll_fallback_total")"
fallback_delta="0"
if awk -v before="${fallback_before}" -v after="${fallback_after}" 'BEGIN{exit !(after > before)}'; then
	fallback_delta="1"
fi

compose_or_fail "StartRedis" start "${REDIS_SERVICE_NAME}"
REDIS_STOPPED="0"
wait_pubsub_ready_or_present "${METRIC_WAIT_TIMEOUT_SEC}"

echo "PASS O2-L1"
echo "incident_id=${INCIDENT_ID}"
echo "job_id=${JOB_ID}"
echo "fallback_metric_delta=${fallback_delta}"
echo "pubsub_ready_initial=${PUBSUB_READY:-0}"
