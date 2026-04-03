#!/usr/bin/env bash
set -euo pipefail

MODE="${MODE:-redis_on}"
BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
COMPOSE_FILE="${COMPOSE_FILE:-deploy/compose/docker-compose.yml}"
COMPOSE_PROJECT_NAME="${COMPOSE_PROJECT_NAME:-rca-ops0}"
REDIS_SERVICE_NAME="${REDIS_SERVICE_NAME:-redis}"
SCOPES="${SCOPES:-*}"
CURL="${CURL:-curl}"
WAIT_SECONDS="${WAIT_SECONDS:-12}"
HEALTH_TIMEOUT_SEC="${HEALTH_TIMEOUT_SEC:-120}"
AUTO_UP="${AUTO_UP:-1}"
AUTO_BUILD="${AUTO_BUILD:-1}"
AUTO_RESTORE_REDIS="${AUTO_RESTORE_REDIS:-1}"
DEBUG="${DEBUG:-0}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

LAST_HTTP_CODE=""
LAST_BODY=""
LAST_HEADERS=""
LAST_REQUEST_ID=""
INCIDENT_ID=""
JOB_ID=""
DELIVERY_ID=""
TOOL_CALL_ID=""
HEALTH_ENDPOINT=""
REDIS_STOPPED="0"
HAS_JQ="0"

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

capture_request_id() {
	local header_text="$1"
	local rid
	rid="$(printf '%s\n' "${header_text}" | awk -F': *' '
		{
			k = tolower($1)
			gsub(/\r/, "", k)
			if (k == "x-request-id" || k == "x-requestid" || k == "request-id") {
				v = $2
				gsub(/\r/, "", v)
				print v
				exit
			}
		}
	')"
	LAST_REQUEST_ID="${rid:-}"
}

fail_step() {
	local step="$1"
	local code="${2:-${LAST_HTTP_CODE}}"
	local body="${3:-${LAST_BODY}}"

	echo "FAIL OPS0 step=${step}"
	echo "http_code=${code:-UNKNOWN}"
	echo "request_id=${LAST_REQUEST_ID:-NONE}"
	echo "response_body<<EOF"
	truncate_2kb "${body}"
	echo
	echo "EOF"
	echo "incident_id=${INCIDENT_ID:-NONE}"
	echo "job_id=${JOB_ID:-NONE}"
	echo "delivery_id=${DELIVERY_ID:-NONE}"
	echo "tool_call_id=${TOOL_CALL_ID:-NONE}"
	echo "mode=${MODE}"
	echo "compose_file=${COMPOSE_FILE}"
	echo "base_url=${BASE_URL}"
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
			docker compose -f "${COMPOSE_FILE}" -p "${COMPOSE_PROJECT_NAME}" "$@"
	) >"${tmp}" 2>&1
	rc=$?
	set -e

	LAST_BODY="$(cat "${tmp}")"
	LAST_HEADERS=""
	LAST_REQUEST_ID=""
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
	local max_time="${4:-}"

	local tmp_body tmp_headers tmp_err code rc curl_err
	tmp_body="$(mktemp)"
	tmp_headers="$(mktemp)"
	tmp_err="$(mktemp)"

	local -a cmd
	cmd=("${CURL}" -sS -o "${tmp_body}" -D "${tmp_headers}" -w "%{http_code}" -X "${method}" "${url}" -H "Accept: application/json")
	if [[ -n "${SCOPES}" ]]; then
		cmd+=(-H "X-Scopes: ${SCOPES}")
	fi
	if [[ -n "${body}" ]]; then
		cmd+=(-H "Content-Type: application/json" -d "${body}")
	fi
	if [[ -n "${max_time}" ]]; then
		cmd+=(--max-time "${max_time}")
	fi

	set +e
	code="$("${cmd[@]}" 2>"${tmp_err}")"
	rc=$?
	set -e

	LAST_BODY="$(cat "${tmp_body}")"
	LAST_HEADERS="$(cat "${tmp_headers}")"
	curl_err="$(cat "${tmp_err}")"
	rm -f "${tmp_body}" "${tmp_headers}" "${tmp_err}"

	capture_request_id "${LAST_HEADERS}"
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
	if [[ -n "${curl_err}" ]]; then
		debug "curl stderr: ${curl_err}"
	fi
	return 0
}

call_or_fail() {
	local step="$1"
	local method="$2"
	local url="$3"
	local body="${4:-}"
	local max_time="${5:-}"

	if ! http_json "${method}" "${url}" "${body}" "${max_time}"; then
		fail_step "${step}"
	fi
	if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
		fail_step "${step}"
	fi
	debug "${step} code=${LAST_HTTP_CODE} request_id=${LAST_REQUEST_ID:-NONE}"
}

extract_field() {
	local json="$1"
	shift
	local keys=("$@")
	local key value

	if [[ "${HAS_JQ}" == "1" ]]; then
		for key in "${keys[@]}"; do
			value="$(
				printf '%s' "${json}" | jq -r --arg k "${key}" '
					(.[$k] //
					 .data[$k] //
					 .incident[$k] //
					 .data.incident[$k] //
					 .job[$k] //
					 .data.job[$k] //
					 .noticeDelivery[$k] //
					 .data.noticeDelivery[$k] //
					 .notice_delivery[$k] //
					 .data.notice_delivery[$k]) |
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

extract_tool_call_id() {
	local json="$1"
	local id=""

	if [[ "${HAS_JQ}" == "1" ]]; then
		id="$(
			printf '%s' "${json}" | jq -r '
				(
					(.toolCalls // .data.toolCalls // .tool_calls // .data.tool_calls // []) |
					.[0].toolCallID // .[0].tool_call_id // empty
				)
			' 2>/dev/null
		)"
	else
		id="$(printf '%s' "${json}" | sed -n 's/.*"toolCallID"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
		if [[ -z "${id}" ]]; then
			id="$(printf '%s' "${json}" | sed -n 's/.*"tool_call_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
		fi
	fi
	printf '%s' "${id}"
}

apply_mode_env() {
	case "${MODE}" in
	redis_on)
		export REDIS_ENABLED="${REDIS_ENABLED:-true}"
		export REDIS_FAIL_OPEN="${REDIS_FAIL_OPEN:-true}"
		export REDIS_PUBSUB_ENABLED="${REDIS_PUBSUB_ENABLED:-true}"
		export REDIS_LIMITER_ENABLED="${REDIS_LIMITER_ENABLED:-true}"
		export REDIS_STREAMS_ENABLED="${REDIS_STREAMS_ENABLED:-true}"
		export REDIS_ALERTING_ENABLED="${REDIS_ALERTING_ENABLED:-true}"
		;;
	redis_down)
		export REDIS_ENABLED="${REDIS_ENABLED:-true}"
		export REDIS_FAIL_OPEN="${REDIS_FAIL_OPEN:-true}"
		export REDIS_PUBSUB_ENABLED="${REDIS_PUBSUB_ENABLED:-true}"
		export REDIS_LIMITER_ENABLED="${REDIS_LIMITER_ENABLED:-true}"
		export REDIS_STREAMS_ENABLED="${REDIS_STREAMS_ENABLED:-true}"
		export REDIS_ALERTING_ENABLED="${REDIS_ALERTING_ENABLED:-true}"
		;;
	redis_off)
		export REDIS_ENABLED="false"
		export REDIS_FAIL_OPEN="${REDIS_FAIL_OPEN:-true}"
		export REDIS_PUBSUB_ENABLED="false"
		export REDIS_LIMITER_ENABLED="false"
		export REDIS_STREAMS_ENABLED="false"
		export REDIS_ALERTING_ENABLED="false"
		export ALERTING_INGEST_POLICY_REDIS_BACKEND_ENABLED="false"
		;;
	*)
		fail_step "Precheck.Mode" "INVALID_MODE" "MODE must be one of redis_on|redis_down|redis_off"
		;;
	esac
}

api_ready_once() {
	local endpoint
	local -a candidates
	candidates=("/healthz" "/v1/version" "/v1/incidents?offset=0&limit=1")
	for endpoint in "${candidates[@]}"; do
		if http_json GET "${BASE_URL}${endpoint}" "" "6"; then
			if [[ "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
				HEALTH_ENDPOINT="${endpoint}"
				return 0
			fi
		fi
	done
	return 1
}

wait_api_ready() {
	local deadline
	deadline="$(( $(date +%s) + HEALTH_TIMEOUT_SEC ))"
	while true; do
		if api_ready_once; then
			debug "api ready endpoint=${HEALTH_ENDPOINT}"
			return 0
		fi
		if (( $(date +%s) > deadline )); then
			fail_step "WaitAPIReady" "TIMEOUT" "${LAST_BODY}"
		fi
		sleep 2
	done
}

create_incident_or_ingest_alert() {
	local rand now_epoch
	local incident_body ingest_body
	local first_code first_body first_req

	rand="${RANDOM}"
	now_epoch="$(date -u +%s)"

	incident_body=$(cat <<JSON
{"namespace":"default","workloadKind":"Deployment","workloadName":"ops0-workload-${rand}","service":"ops0-svc-${rand}","severity":"P1"}
JSON
)

	if http_json POST "${BASE_URL}/v1/incidents" "${incident_body}" "15" && [[ "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
		INCIDENT_ID="$(extract_field "${LAST_BODY}" "incidentID" "incident_id" || true)"
		if [[ -n "${INCIDENT_ID}" ]]; then
			return 0
		fi
	fi
	first_code="${LAST_HTTP_CODE}"
	first_body="${LAST_BODY}"
	first_req="${LAST_REQUEST_ID}"

	ingest_body=$(cat <<JSON
{"idempotencyKey":"idem-ops0-ingest-${rand}","fingerprint":"ops0-fp-${rand}","status":"firing","severity":"P1","service":"ops0-svc-${rand}","cluster":"ops0","namespace":"default","workload":"ops0-workload-${rand}","lastSeenAt":{"seconds":${now_epoch},"nanos":0},"labelsJSON":"{\"alertname\":\"OPS0Smoke\",\"service\":\"ops0-svc-${rand}\"}"}
JSON
)

	if http_json POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_body}" "15" && [[ "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
		INCIDENT_ID="$(extract_field "${LAST_BODY}" "incidentID" "incident_id" || true)"
		if [[ -n "${INCIDENT_ID}" ]]; then
			return 0
		fi
	fi

	LAST_HTTP_CODE="${LAST_HTTP_CODE}"
	LAST_BODY="create_incident_failed(code=${first_code},request_id=${first_req:-NONE}): ${first_body}
ingest_alert_failed(code=${LAST_HTTP_CODE},request_id=${LAST_REQUEST_ID:-NONE}): ${LAST_BODY}"
	fail_step "CreateIncidentOrIngestAlert"
}

run_ai_job_or_fail() {
	local now_epoch run_body
	now_epoch="$(date -u +%s)"
	run_body=$(cat <<JSON
{"incidentID":"${INCIDENT_ID}","idempotencyKey":"idem-ops0-ai-run-${RANDOM}","pipeline":"basic_rca","trigger":"manual","timeRangeStart":{"seconds":$((now_epoch - 1800)),"nanos":0},"timeRangeEnd":{"seconds":${now_epoch},"nanos":0},"createdBy":"ops0-smoke"}
JSON
)
	call_or_fail "RunAIJob" POST "${BASE_URL}/v1/incidents/${INCIDENT_ID}/ai:run" "${run_body}" "20"
	JOB_ID="$(extract_field "${LAST_BODY}" "jobID" "job_id" || true)"
	if [[ -z "${JOB_ID}" ]]; then
		fail_step "RunAIJob.ParseJobID"
	fi
}

maybe_collect_tool_call_id() {
	if ! http_json GET "${BASE_URL}/v1/ai/jobs/${JOB_ID}/tool-calls?offset=0&limit=1" "" "10"; then
		return 0
	fi
	if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
		return 0
	fi
	TOOL_CALL_ID="$(extract_tool_call_id "${LAST_BODY}" || true)"
}

cleanup() {
	if [[ "${MODE}" == "redis_down" ]] && [[ "${REDIS_STOPPED}" == "1" ]] && [[ "${AUTO_RESTORE_REDIS}" == "1" ]]; then
		set +e
		(
			cd "${REPO_ROOT}" && \
				docker compose -f "${COMPOSE_FILE}" -p "${COMPOSE_PROJECT_NAME}" start "${REDIS_SERVICE_NAME}"
		) >/dev/null 2>&1
		set -e
	fi
}
trap cleanup EXIT

if ! need_cmd docker; then
	fail_step "Precheck.Docker" "MISSING_DOCKER" "docker is required"
fi
if ! need_cmd "${CURL}"; then
	fail_step "Precheck.Curl" "MISSING_CURL" "curl is required"
fi
if need_cmd jq; then
	HAS_JQ="1"
fi

apply_mode_env

if [[ "${AUTO_UP}" == "1" ]]; then
	if [[ "${AUTO_BUILD}" == "1" ]]; then
		compose_or_fail "Compose.Up" up -d --build
	else
		compose_or_fail "Compose.Up" up -d
	fi
fi

wait_api_ready
call_or_fail "Health.Check" GET "${BASE_URL}${HEALTH_ENDPOINT}" "" "8"

if [[ "${MODE}" == "redis_down" ]]; then
	compose_or_fail "Mode.RedisDown.StopRedis" stop "${REDIS_SERVICE_NAME}"
	REDIS_STOPPED="1"
	sleep 1
	wait_api_ready
	call_or_fail "Health.AfterRedisDown" GET "${BASE_URL}${HEALTH_ENDPOINT}" "" "8"
fi

create_incident_or_ingest_alert
run_ai_job_or_fail

if ! http_json GET "${BASE_URL}/v1/ai/jobs?status=queued&offset=0&limit=20&wait_seconds=${WAIT_SECONDS}" "" "$((WAIT_SECONDS + 8))"; then
	fail_step "LongPollAIJobs"
fi
if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
	fail_step "LongPollAIJobs"
fi

maybe_collect_tool_call_id

echo "SKIP OPS0 step=NoticeDelivery reason=no stable zero-dependency trigger API in OPS0 baseline"
echo "PASS OPS0 mode=${MODE}"
echo "health_endpoint=${HEALTH_ENDPOINT}"
echo "incident_id=${INCIDENT_ID}"
echo "job_id=${JOB_ID}"
echo "delivery_id=${DELIVERY_ID:-NONE}"
echo "tool_call_id=${TOOL_CALL_ID:-NONE}"
echo "request_id=${LAST_REQUEST_ID:-NONE}"
