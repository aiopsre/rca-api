#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
DEBUG="${DEBUG:-0}"
N="${N:-6}"
WAIT_TIMEOUT_SEC="${WAIT_TIMEOUT_SEC:-180}"
POLL_INTERVAL_SEC="${POLL_INTERVAL_SEC:-1}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
ORCH_DIR="${ORCH_DIR:-${REPO_ROOT}/tools/ai-orchestrator}"
ORCH_CMD="${ORCH_CMD:-}"

LAST_HTTP_CODE=""
LAST_BODY=""
INCIDENT_IDS=()
JOB_IDS=()
JOB_OWNER_SUMMARY=()
ORCH_PIDS=()
ORCH_LOGS=()

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

	echo "FAIL B1 step=${step}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	truncate_2kb "${body}"
	echo
	echo "EOF"
	echo "incident_ids=${INCIDENT_IDS[*]:-NONE}"
	echo "job_ids=${JOB_IDS[*]:-NONE}"
	echo "job_owner_summary=${JOB_OWNER_SUMMARY[*]:-NONE}"
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
					(.[$k] // .data[$k] // .job[$k] // .data.job[$k] // .incident[$k] // .data.incident[$k]) |
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
	for pid in "${ORCH_PIDS[@]:-}"; do
		kill "${pid}" >/dev/null 2>&1 || true
	done
	for pid in "${ORCH_PIDS[@]:-}"; do
		wait "${pid}" >/dev/null 2>&1 || true
	done
	for logf in "${ORCH_LOGS[@]:-}"; do
		rm -f "${logf}"
	done
}
trap cleanup EXIT

start_orchestrator() {
	local instance_id="$1"
	local orch_cmd="${ORCH_CMD}"
	if [[ -z "${orch_cmd}" ]]; then
		if need_cmd python3; then
			orch_cmd="python3 -m orchestrator.main"
		elif need_cmd python; then
			orch_cmd="python -m orchestrator.main"
		else
			fail_step "StartOrchestrator.${instance_id}" "MISSING_PYTHON" "python3/python not found"
		fi
	fi
	local logf
	logf="$(mktemp)"
	ORCH_LOGS+=("${logf}")

	(
		cd "${ORCH_DIR}" && \
			BASE_URL="${BASE_URL}" \
			SCOPES="${SCOPES}" \
			RCA_API_SCOPES="${SCOPES}" \
			INSTANCE_ID="${instance_id}" \
			CONCURRENCY=1 \
			POLL_INTERVAL_MS=200 \
			LONG_POLL_WAIT_SECONDS=2 \
			LEASE_HEARTBEAT_INTERVAL_SECONDS=3 \
			DEBUG="${DEBUG}" \
			bash -lc "${orch_cmd}"
	) >"${logf}" 2>&1 &
	local pid="$!"
	ORCH_PIDS+=("${pid}")
	sleep 0.6
	if ! kill -0 "${pid}" >/dev/null 2>&1; then
		LAST_HTTP_CODE="ORCH_EXITED"
		LAST_BODY="$(cat "${logf}" 2>/dev/null || true)"
		fail_step "StartOrchestrator.${instance_id}"
	fi
	debug "orchestrator instance=${instance_id} pid=${pid}"
}

wait_job_succeeded() {
	local job_id="$1"
	local deadline status now
	deadline="$(( $(date +%s) + WAIT_TIMEOUT_SEC ))"

	while true; do
		call_or_fail "PollJob.${job_id}" GET "${BASE_URL}/v1/ai/jobs/${job_id}"
		status="$(extract_field "${LAST_BODY}" "status")" || true
		if [[ -z "${status}" ]]; then
			fail_step "PollJobStatusParse.${job_id}" "${LAST_HTTP_CODE}" "${LAST_BODY}"
		fi
		case "${status}" in
			succeeded)
				return 0
				;;
			failed|canceled)
				fail_step "PollJobTerminal.${job_id}.${status}" "${LAST_HTTP_CODE}" "${LAST_BODY}"
				;;
		esac

		now="$(date +%s)"
		if (( now > deadline )); then
			fail_step "PollJobTimeout.${job_id}" "TIMEOUT" "${LAST_BODY}"
		fi
		sleep "${POLL_INTERVAL_SEC}"
	done
}

extract_single_owner_from_toolcalls() {
	local body="$1"
	if ! need_cmd jq; then
		fail_step "OwnerExtractRequiresJQ" "MISSING_JQ" "${body}"
	fi

	local owner_csv owner_count
	owner_csv="$(
		printf '%s' "${body}" | jq -r '
			(.toolCalls // .data.toolCalls // [])
			| map((.requestJSON // .request_json // "{}") | fromjson? | .instance_id // empty)
			| map(select(. != ""))
			| unique
			| join(",")
		' 2>/dev/null
	)"
	owner_count="$(
		printf '%s' "${body}" | jq -r '
			(.toolCalls // .data.toolCalls // [])
			| map((.requestJSON // .request_json // "{}") | fromjson? | .instance_id // empty)
			| map(select(. != ""))
			| unique
			| length
		' 2>/dev/null
	)"
	if [[ -z "${owner_csv}" ]] || [[ "${owner_count}" != "1" ]]; then
		fail_step "OwnerUniquenessCheck" "${LAST_HTTP_CODE}" "${body}"
	fi
	printf '%s' "${owner_csv}"
}

if ! [[ "${N}" =~ ^[0-9]+$ ]] || (( N <= 0 )); then
	fail_step "ValidateN" "INVALID_N" "N must be positive integer"
fi

if [[ ! -d "${ORCH_DIR}" ]]; then
	fail_step "ValidateOrchestratorDir" "MISSING_DIR" "${ORCH_DIR}"
fi

rand="${RAND:-$RANDOM}"
now_epoch="$(date -u +%s)"
start_epoch="$((now_epoch - 1800))"

for ((i = 1; i <= N; i++)); do
	fingerprint="b1-l1-fp-${rand}-${i}"
	ingest_body=$(cat <<EOF
{"idempotencyKey":"idem-b1-l1-ingest-${rand}-${i}","fingerprint":"${fingerprint}","status":"firing","severity":"P1","service":"b1-l1-svc-${i}","cluster":"prod-b1","namespace":"default","workload":"b1-workload-${i}","lastSeenAt":{"seconds":${now_epoch},"nanos":0}}
EOF
)
	call_or_fail "IngestAlertEvent.${i}" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_body}"
	incident_id="$(extract_field "${LAST_BODY}" "incidentID" "incident_id" || true)"
	if [[ -z "${incident_id}" ]]; then
		fail_step "IngestParseIncidentID.${i}" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
	INCIDENT_IDS+=("${incident_id}")

	run_body=$(cat <<EOF
{"incidentID":"${incident_id}","idempotencyKey":"idem-b1-l1-run-${rand}-${i}","pipeline":"basic_rca","trigger":"manual","timeRangeStart":{"seconds":${start_epoch},"nanos":0},"timeRangeEnd":{"seconds":${now_epoch},"nanos":0},"inputHintsJSON":"{\"scenario\":\"B1_L1\",\"index\":${i}}","createdBy":"system"}
EOF
)
	call_or_fail "RunAIJob.${i}" POST "${BASE_URL}/v1/incidents/${incident_id}/ai:run" "${run_body}"
	job_id="$(extract_field "${LAST_BODY}" "jobID" "job_id" || true)"
	if [[ -z "${job_id}" ]]; then
		fail_step "RunParseJobID.${i}" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
	JOB_IDS+=("${job_id}")
done

start_orchestrator "b1-orc-a-${rand}"
start_orchestrator "b1-orc-b-${rand}"

for job_id in "${JOB_IDS[@]}"; do
	wait_job_succeeded "${job_id}"
	call_or_fail "ListToolCalls.${job_id}" GET "${BASE_URL}/v1/ai/jobs/${job_id}/tool-calls?offset=0&limit=50"
	owner="$(extract_single_owner_from_toolcalls "${LAST_BODY}")"
	JOB_OWNER_SUMMARY+=("${job_id}:${owner}")
done

echo "PASS B1"
echo "jobs=${#JOB_IDS[@]}"
echo "job_ids=${JOB_IDS[*]}"
echo "job_owner_summary=${JOB_OWNER_SUMMARY[*]}"
