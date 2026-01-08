#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
METRICS_URL="${METRICS_URL:-${BASE_URL}/metrics}"
COMPOSE_FILE="${COMPOSE_FILE:-deploy/compose/docker-compose.redis.yaml}"
REDIS_SERVICE_NAME="${REDIS_SERVICE_NAME:-redis}"
NOTICE_WORKER_SERVICE_NAME="${NOTICE_WORKER_SERVICE_NAME:-notice-worker}"
MOCK_SERVICE_NAME="${MOCK_SERVICE_NAME:-mock-webhook}"
SCOPES="${SCOPES:-*}"
CURL="${CURL:-curl}"
DEBUG="${DEBUG:-0}"

DELIVERY_BATCH="${DELIVERY_BATCH:-40}"
INGEST_CONCURRENCY="${INGEST_CONCURRENCY:-8}"
INGEST_RETRY_ATTEMPTS="${INGEST_RETRY_ATTEMPTS:-6}"
INGEST_RETRY_SLEEP_SEC="${INGEST_RETRY_SLEEP_SEC:-0.2}"
WAIT_TIMEOUT_SEC="${WAIT_TIMEOUT_SEC:-300}"
POLL_INTERVAL_SEC="${POLL_INTERVAL_SEC:-1}"
LIST_LIMIT="${LIST_LIMIT:-200}"
WINDOW_SECONDS="${WINDOW_SECONDS:-10}"
MAX_REQ_IN_WINDOW="${MAX_REQ_IN_WINDOW:-120}"
RESTORE_WORKER_SCALE="${RESTORE_WORKER_SCALE:-1}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

LAST_HTTP_CODE=""
LAST_BODY=""
CHANNEL_ID=""
INCIDENT_ID_SAMPLE=""
DELIVERY_ID_SAMPLE=""
REDIS_STOPPED="0"
ORIGINAL_WORKER_COUNT="0"
SCALED_TO_TWO="0"

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

	echo "FAIL O2-L3 step=${step}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	truncate_2kb "${body}"
	echo
	echo "EOF"
	echo "channel_id=${CHANNEL_ID:-NONE}"
	echo "incident_id=${INCIDENT_ID_SAMPLE:-NONE}"
	echo "delivery_id=${DELIVERY_ID_SAMPLE:-NONE}"
	echo "base_url=${BASE_URL}"
	echo "metrics_url=${METRICS_URL}"
	echo "compose_file=${COMPOSE_FILE}"
	echo "redis_service=${REDIS_SERVICE_NAME}"
	echo "worker_service=${NOTICE_WORKER_SERVICE_NAME}"
	exit 1
}

compose_capture() {
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

metric_exists() {
	local metrics_body="$1"
	local metric_name="$2"
	printf '%s\n' "${metrics_body}" | awk -v name="${metric_name}" '$1 ~ ("^" name "(\\{|$)") {found=1} END {exit(found ? 0 : 1)}'
}

metric_sum_with_label() {
	local metrics_body="$1"
	local metric_name="$2"
	local label_substr="${3:-}"
	printf '%s\n' "${metrics_body}" | awk -v name="${metric_name}" -v label="${label_substr}" '
		$1 ~ ("^" name "(\\{|$)") {
			if (label == "" || index($1, label) > 0) {
				sum += $NF
			}
		}
		END {printf "%.6f", sum + 0}
	'
}

service_running_count() {
	local service="$1"
	local tmp rc count
	tmp="$(mktemp)"
	set +e
	(
		cd "${REPO_ROOT}" && \
			docker compose -f "${COMPOSE_FILE}" ps --status running --format json "${service}"
	) >"${tmp}" 2>/dev/null
	rc=$?
	set -e
	if (( rc == 0 )); then
		count="$(awk 'NF {c++} END {print c+0}' "${tmp}")"
	else
		count="0"
	fi
	rm -f "${tmp}"
	if [[ ! "${count}" =~ ^[0-9]+$ ]]; then
		count="0"
	fi
	printf '%s' "${count}"
}

wait_worker_count_at_least_or_fail() {
	local min_count="$1"
	local timeout_sec="$2"
	local deadline count
	deadline="$(( $(date +%s) + timeout_sec ))"
	while true; do
		count="$(service_running_count "${NOTICE_WORKER_SERVICE_NAME}")"
		if [[ "${count}" =~ ^[0-9]+$ ]] && (( count >= min_count )); then
			return 0
		fi
		if (( $(date +%s) > deadline )); then
			LAST_HTTP_CODE="WORKER_COUNT_TIMEOUT"
			LAST_BODY="service=${NOTICE_WORKER_SERVICE_NAME} expected_at_least=${min_count} got=${count}"
			fail_step "WaitWorkerCount"
		fi
		sleep 1
	done
}

ensure_two_workers_or_fail() {
	ORIGINAL_WORKER_COUNT="$(service_running_count "${NOTICE_WORKER_SERVICE_NAME}")"
	if [[ ! "${ORIGINAL_WORKER_COUNT}" =~ ^[0-9]+$ ]]; then
		ORIGINAL_WORKER_COUNT="0"
	fi
	if (( ORIGINAL_WORKER_COUNT >= 2 )); then
		return 0
	fi
	compose_capture "ScaleWorkersToTwo" up -d --scale "${NOTICE_WORKER_SERVICE_NAME}=2" "${NOTICE_WORKER_SERVICE_NAME}"
	SCALED_TO_TWO="1"
	wait_worker_count_at_least_or_fail 2 120
}

mock_request_count_total() {
	local hook_path="$1"
	local tmp rc count
	tmp="$(mktemp)"
	set +e
	(
		cd "${REPO_ROOT}" && \
			docker compose -f "${COMPOSE_FILE}" logs --no-color "${MOCK_SERVICE_NAME}"
	) >"${tmp}" 2>/dev/null
	rc=$?
	set -e
	if (( rc != 0 )); then
		rm -f "${tmp}"
		printf '0'
		return 0
	fi
	count="$(grep -F -c "\"POST ${hook_path} HTTP/1.1\"" "${tmp}" 2>/dev/null || true)"
	rm -f "${tmp}"
	if [[ -z "${count}" ]]; then
		count="0"
	fi
	printf '%s' "${count}"
}

mock_request_count_window() {
	local hook_path="$1"
	local since_ts="$2"
	local until_ts="$3"
	local tmp rc count
	tmp="$(mktemp)"
	set +e
	(
		cd "${REPO_ROOT}" && \
			docker compose -f "${COMPOSE_FILE}" logs --no-color --timestamps --since "${since_ts}" --until "${until_ts}" "${MOCK_SERVICE_NAME}"
	) >"${tmp}" 2>/dev/null
	rc=$?
	set -e
	if (( rc != 0 )); then
		rm -f "${tmp}"
		printf '0'
		return 0
	fi
	count="$(grep -F -c "\"POST ${hook_path} HTTP/1.1\"" "${tmp}" 2>/dev/null || true)"
	rm -f "${tmp}"
	if [[ -z "${count}" ]]; then
		count="0"
	fi
	printf '%s' "${count}"
}

disable_existing_channels_or_fail() {
	call_or_fail "Preclean.ListChannels" GET "${BASE_URL}/v1/notice-channels?offset=0&limit=${LIST_LIMIT}"
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
		call_or_fail "Preclean.DisableChannel.${channel_id}" PATCH "${BASE_URL}/v1/notice-channels/${channel_id}" '{"enabled":false}'
	done <<<"${channels}"
}

create_channel_or_fail() {
	local hook_path="$1"
	local payload
	payload=$(cat <<JSON
{"name":"o2-l3-${RANDOM}","type":"webhook","enabled":true,"endpointURL":"http://${MOCK_SERVICE_NAME}:8080${hook_path}","timeoutMs":1200,"maxRetries":3}
JSON
)
	call_or_fail "CreateChannel" POST "${BASE_URL}/v1/notice-channels" "${payload}"
	CHANNEL_ID="$(extract_field "${LAST_BODY}" "channelID" "channel_id" || true)"
	if [[ -z "${CHANNEL_ID}" ]]; then
		fail_step "CreateChannel.ParseChannelID"
	fi
}

disable_channel_or_fail() {
	local channel_id="$1"
	[[ -n "${channel_id}" ]] || return 0
	call_or_fail "DisableChannel.${channel_id}" PATCH "${BASE_URL}/v1/notice-channels/${channel_id}" '{"enabled":false}'
}

ingest_incident_async() {
	local idx="$1"
	local out_dir="$2"
	local now_epoch payload body_file err_file code rc incident_id attempt

	now_epoch="$(date -u +%s)"
	payload=$(cat <<JSON
{"idempotencyKey":"idem-o2-l3-${idx}-${RANDOM}","fingerprint":"o2-l3-fp-${RANDOM}-${idx}","status":"firing","severity":"P1","service":"o2-l3-svc","cluster":"prod-o2-l3","namespace":"default","workload":"o2-l3-workload","lastSeenAt":{"seconds":${now_epoch},"nanos":0}}
JSON
)

	body_file="${out_dir}/body_${idx}.json"
	err_file="${out_dir}/err_${idx}.log"

	for ((attempt = 1; attempt <= INGEST_RETRY_ATTEMPTS; attempt++)); do
		code="$(${CURL} -sS -o "${body_file}" -w "%{http_code}" -X POST "${BASE_URL}/v1/alert-events:ingest" -H "Accept: application/json" -H "X-Scopes: ${SCOPES}" -H "Content-Type: application/json" -d "${payload}" 2>"${err_file}")"
		rc=$?
		if (( rc == 0 )) && [[ "${code}" =~ ^2[0-9][0-9]$ ]]; then
			break
		fi
		if (( attempt < INGEST_RETRY_ATTEMPTS )); then
			sleep "${INGEST_RETRY_SLEEP_SEC}"
			continue
		fi
		if (( rc != 0 )); then
			echo "CURL_${rc}" >"${out_dir}/fail_${idx}.code"
		else
			echo "${code}" >"${out_dir}/fail_${idx}.code"
		fi
		return 1
	done

	incident_id="$(jq -r '(.incidentID // .incident_id // .data.incidentID // .data.incident_id // empty)' "${body_file}" 2>/dev/null || true)"
	if [[ -n "${incident_id}" ]]; then
		echo "${incident_id}" >"${out_dir}/incident_${idx}.id"
	fi
	return 0
}

run_parallel_ingest_or_fail() {
	local count="$1"
	local out_dir failed i pid
	out_dir="$(mktemp -d)"
	failed="0"

	declare -a pids=()
	for ((i = 1; i <= count; i++)); do
		ingest_incident_async "${i}" "${out_dir}" &
		pid="$!"
		pids+=("${pid}")
		if (( ${#pids[@]} >= INGEST_CONCURRENCY )); then
			for pid in "${pids[@]}"; do
				if ! wait "${pid}"; then
					failed="1"
				fi
			done
			pids=()
		fi
	done
	for pid in "${pids[@]}"; do
		if ! wait "${pid}"; then
			failed="1"
		fi
	done

	INCIDENT_ID_SAMPLE="$(ls "${out_dir}"/incident_*.id 2>/dev/null | head -n 1 | xargs -r cat || true)"
	if [[ "${failed}" == "1" ]]; then
		LAST_HTTP_CODE="INGEST_BATCH_FAILED"
		LAST_BODY="$(
			for f in "${out_dir}"/fail_*.code; do
				[[ -f "${f}" ]] || continue
				idx="$(basename "${f}" | sed -E 's/^fail_([0-9]+)\.code$/\1/')"
				code="$(cat "${f}" 2>/dev/null || true)"
				body="$(cat "${out_dir}/body_${idx}.json" 2>/dev/null || true)"
				err="$(cat "${out_dir}/err_${idx}.log" 2>/dev/null || true)"
				echo "idx=${idx} code=${code} body=$(printf '%s' "${body}" | tr '\n' ' ') err=$(printf '%s' "${err}" | tr '\n' ' ')"
			done
		)"
		rm -rf "${out_dir}"
		fail_step "ParallelIngest"
	fi

	rm -rf "${out_dir}"
}

list_deliveries_or_fail() {
	call_or_fail "ListDeliveries" GET "${BASE_URL}/v1/notice-deliveries?channel_id=${CHANNEL_ID}&event_type=incident_created&offset=0&limit=${LIST_LIMIT}"
}

wait_deliveries_succeeded_or_fail() {
	local expected="$1"
	local deadline total succeeded failed all_succeeded
	deadline="$(( $(date +%s) + WAIT_TIMEOUT_SEC ))"

	while true; do
		list_deliveries_or_fail
		total="$(printf '%s' "${LAST_BODY}" | jq -r '(.noticeDeliveries // .data.noticeDeliveries // []) | length' 2>/dev/null || true)"
		succeeded="$(printf '%s' "${LAST_BODY}" | jq -r '(.noticeDeliveries // .data.noticeDeliveries // []) | map(select((.status // "") == "succeeded")) | length' 2>/dev/null || true)"
		failed="$(printf '%s' "${LAST_BODY}" | jq -r '(.noticeDeliveries // .data.noticeDeliveries // []) | map(select((.status // "") == "failed")) | length' 2>/dev/null || true)"
		all_succeeded="$(printf '%s' "${LAST_BODY}" | jq -r '(.noticeDeliveries // .data.noticeDeliveries // []) | all((.status // "") == "succeeded")' 2>/dev/null || true)"
		DELIVERY_ID_SAMPLE="$(printf '%s' "${LAST_BODY}" | jq -r '(.noticeDeliveries // .data.noticeDeliveries // []) | .[0].deliveryID // .[0].delivery_id // empty' 2>/dev/null || true)"

		if [[ "${total}" == "${expected}" ]] && [[ "${succeeded}" == "${expected}" ]] && [[ "${all_succeeded}" == "true" ]]; then
			return 0
		fi
		if [[ "${failed}" =~ ^[0-9]+$ ]] && (( failed > 0 )); then
			fail_step "WaitDeliveries.Failed"
		fi
		if (( $(date +%s) > deadline )); then
			fail_step "WaitDeliveries.Timeout" "TIMEOUT" "expected=${expected} got_total=${total} got_succeeded=${succeeded}"
		fi
		sleep "${POLL_INTERVAL_SEC}"
	done
}

restore_workers_scale_if_needed() {
	if [[ "${RESTORE_WORKER_SCALE}" != "1" && "${RESTORE_WORKER_SCALE}" != "true" ]]; then
		return 0
	fi
	if [[ "${SCALED_TO_TWO}" != "1" ]]; then
		return 0
	fi
	if [[ ! "${ORIGINAL_WORKER_COUNT}" =~ ^[0-9]+$ ]]; then
		return 0
	fi
	if (( ORIGINAL_WORKER_COUNT <= 0 )); then
		return 0
	fi
	set +e
	(
		cd "${REPO_ROOT}" && \
			docker compose -f "${COMPOSE_FILE}" up -d --scale "${NOTICE_WORKER_SERVICE_NAME}=${ORIGINAL_WORKER_COUNT}" "${NOTICE_WORKER_SERVICE_NAME}"
	) >/dev/null 2>&1
	set -e
}

cleanup() {
	if [[ -n "${CHANNEL_ID}" ]]; then
		set +e
		http_json PATCH "${BASE_URL}/v1/notice-channels/${CHANNEL_ID}" '{"enabled":false}' >/dev/null 2>&1 || true
		set -e
	fi
	if [[ "${REDIS_STOPPED}" == "1" ]]; then
		set +e
		(
			cd "${REPO_ROOT}" && \
				docker compose -f "${COMPOSE_FILE}" start "${REDIS_SERVICE_NAME}"
		) >/dev/null 2>&1
		set -e
	fi
	restore_workers_scale_if_needed
}
trap cleanup EXIT

if ! need_cmd docker; then
	fail_step "Precheck.MissingDocker" "MISSING_DOCKER" "docker is required"
fi
if ! need_cmd jq; then
	fail_step "Precheck.MissingJQ" "MISSING_JQ" "jq is required"
fi
if [[ ! "${DELIVERY_BATCH}" =~ ^[0-9]+$ ]] || (( DELIVERY_BATCH <= 0 )); then
	fail_step "Precheck.InvalidDeliveryBatch" "INVALID_ARG" "DELIVERY_BATCH=${DELIVERY_BATCH}"
fi
if [[ ! "${INGEST_CONCURRENCY}" =~ ^[0-9]+$ ]] || (( INGEST_CONCURRENCY <= 0 )); then
	fail_step "Precheck.InvalidIngestConcurrency" "INVALID_ARG" "INGEST_CONCURRENCY=${INGEST_CONCURRENCY}"
fi
if [[ ! "${INGEST_RETRY_ATTEMPTS}" =~ ^[0-9]+$ ]] || (( INGEST_RETRY_ATTEMPTS <= 0 )); then
	fail_step "Precheck.InvalidIngestRetryAttempts" "INVALID_ARG" "INGEST_RETRY_ATTEMPTS=${INGEST_RETRY_ATTEMPTS}"
fi
if [[ ! "${WINDOW_SECONDS}" =~ ^[0-9]+$ ]] || (( WINDOW_SECONDS <= 0 )); then
	fail_step "Precheck.InvalidWindowSeconds" "INVALID_ARG" "WINDOW_SECONDS=${WINDOW_SECONDS}"
fi
if [[ ! "${MAX_REQ_IN_WINDOW}" =~ ^[0-9]+$ ]] || (( MAX_REQ_IN_WINDOW <= 0 )); then
	fail_step "Precheck.InvalidMaxReq" "INVALID_ARG" "MAX_REQ_IN_WINDOW=${MAX_REQ_IN_WINDOW}"
fi

call_or_fail "Precheck.Health" GET "${BASE_URL}/healthz"
compose_capture "Precheck.MockUp" --profile mock up -d "${MOCK_SERVICE_NAME}"
disable_existing_channels_or_fail
ensure_two_workers_or_fail

call_or_fail "Metrics.Before" GET "${METRICS_URL}"
metrics_before="${LAST_BODY}"
if ! metric_exists "${metrics_before}" "notice_limiter_fallback_total"; then
	fail_step "Metrics.Before.LimiterFallbackMissing" "MISSING_METRIC" "metric=notice_limiter_fallback_total"
fi
limiter_fallback_before="$(metric_sum_with_label "${metrics_before}" "notice_limiter_fallback_total" '')"

compose_capture "StopRedis" stop "${REDIS_SERVICE_NAME}"
REDIS_STOPPED="1"
wait_worker_count_at_least_or_fail 2 60

hook_path="/o2-l3-limiter-fallback-${RANDOM}"
create_channel_or_fail "${hook_path}"
mock_before_total="$(mock_request_count_total "${hook_path}")"
window_start_ts="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
window_end_ts="$(date -u -d "${window_start_ts} + ${WINDOW_SECONDS} seconds" +%Y-%m-%dT%H:%M:%SZ)"

run_parallel_ingest_or_fail "${DELIVERY_BATCH}"
wait_deliveries_succeeded_or_fail "${DELIVERY_BATCH}"

unique_delivery_count="$(
	printf '%s' "${LAST_BODY}" | jq -r '
		(.noticeDeliveries // .data.noticeDeliveries // []) |
		map(.deliveryID // .delivery_id // empty) |
		map(select(type == "string" and length > 0)) |
		unique |
		length
	' 2>/dev/null || true
)"
if [[ "${unique_delivery_count}" != "${DELIVERY_BATCH}" ]]; then
	fail_step "AssertUniqueDeliveryCount" "ASSERT_FAILED" "expected=${DELIVERY_BATCH} unique=${unique_delivery_count}"
fi

mock_after_total="$(mock_request_count_total "${hook_path}")"
mock_delta_total="$((mock_after_total - mock_before_total))"
if (( mock_delta_total != DELIVERY_BATCH )); then
	fail_step "AssertTotalWebhookCount" "ASSERT_FAILED" "expected=${DELIVERY_BATCH} got=${mock_delta_total}"
fi

mock_window_count="$(mock_request_count_window "${hook_path}" "${window_start_ts}" "${window_end_ts}")"
if (( mock_window_count > MAX_REQ_IN_WINDOW )); then
	fail_step "AssertWindowRequestUpperBound" "ASSERT_FAILED" "window_count=${mock_window_count} max=${MAX_REQ_IN_WINDOW} window_seconds=${WINDOW_SECONDS}"
fi

call_or_fail "Metrics.After" GET "${METRICS_URL}"
metrics_after="${LAST_BODY}"
if ! metric_exists "${metrics_after}" "notice_limiter_fallback_total"; then
	fail_step "Metrics.After.LimiterFallbackMissing" "MISSING_METRIC" "metric=notice_limiter_fallback_total"
fi
limiter_fallback_after="$(metric_sum_with_label "${metrics_after}" "notice_limiter_fallback_total" '')"
limiter_fallback_delta="0"
if awk -v before="${limiter_fallback_before}" -v after="${limiter_fallback_after}" 'BEGIN{exit !(after > before)}'; then
	limiter_fallback_delta="1"
fi

compose_capture "StartRedis" start "${REDIS_SERVICE_NAME}"
REDIS_STOPPED="0"
wait_worker_count_at_least_or_fail 2 60

disable_channel_or_fail "${CHANNEL_ID}"

echo "PASS O2-L3"
echo "channel_id=${CHANNEL_ID}"
echo "incident_id=${INCIDENT_ID_SAMPLE:-NONE}"
echo "delivery_id=${DELIVERY_ID_SAMPLE:-NONE}"
echo "delivery_batch=${DELIVERY_BATCH}"
echo "window_seconds=${WINDOW_SECONDS}"
echo "window_request_count=${mock_window_count}"
echo "max_req_in_window=${MAX_REQ_IN_WINDOW}"
echo "limiter_fallback_metric_delta=${limiter_fallback_delta}"
echo "worker_running_count=$(service_running_count "${NOTICE_WORKER_SERVICE_NAME}")"
