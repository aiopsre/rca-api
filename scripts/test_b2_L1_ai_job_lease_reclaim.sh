#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
DEBUG="${DEBUG:-0}"
ORCHESTRATOR_INSTANCE_ID="${ORCHESTRATOR_INSTANCE_ID:-test-instance}"
LEASE_EXPIRE_WAIT_SEC="${LEASE_EXPIRE_WAIT_SEC:-35}"
WAIT_TIMEOUT_SEC="${WAIT_TIMEOUT_SEC:-240}"
POLL_INTERVAL_SEC="${POLL_INTERVAL_SEC:-1}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
ORCH_DIR="${ORCH_DIR:-${REPO_ROOT}/tools/ai-orchestrator}"
ORCH_CMD="${ORCH_CMD:-}"

LAST_HTTP_CODE=""
LAST_BODY=""
INCIDENT_ID=""
JOB_ID=""
CLAIM_OWNER=""
FINAL_OWNER=""
ORCH_PID=""
ORCH_LOG=""

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

	echo "FAIL B2 step=${step}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	truncate_2kb "${body}"
	echo
	echo "EOF"
	echo "incident_id=${INCIDENT_ID:-NONE}"
	echo "job_id=${JOB_ID:-NONE}"
	echo "claim_owner=${CLAIM_OWNER:-NONE}"
	echo "final_owner=${FINAL_OWNER:-NONE}"
	exit 1
}

http_json() {
	local method="$1"
	local url="$2"
	local body="${3:-}"
	local instance_id="${4:-}"

	local tmp_body tmp_err code rc curl_err
	tmp_body="$(mktemp)"
	tmp_err="$(mktemp)"

	local -a cmd
	cmd=("${CURL}" -sS -o "${tmp_body}" -w "%{http_code}" -X "${method}" "${url}" -H "Accept: application/json")
	if [[ -n "${SCOPES}" ]]; then
		cmd+=(-H "X-Scopes: ${SCOPES}")
	fi
	if [[ "${method}" == "POST" ]] && [[ "${url}" =~ /v1/ai/jobs/[^/]+/(start|tool-calls|finalize|cancel|heartbeat)$ ]]; then
		if [[ -n "${instance_id}" ]]; then
			cmd+=(-H "X-Orchestrator-Instance-ID: ${instance_id}")
		else
			cmd+=(-H "X-Orchestrator-Instance-ID: ${ORCHESTRATOR_INSTANCE_ID}")
		fi
	elif [[ -n "${instance_id}" ]]; then
		cmd+=(-H "X-Orchestrator-Instance-ID: ${instance_id}")
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
	local instance_id="${5:-}"

	if ! http_json "${method}" "${url}" "${body}" "${instance_id}"; then
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
	if [[ -n "${ORCH_PID}" ]]; then
		kill "${ORCH_PID}" >/dev/null 2>&1 || true
		wait "${ORCH_PID}" >/dev/null 2>&1 || true
	fi
	if [[ -n "${ORCH_LOG}" ]]; then
		rm -f "${ORCH_LOG}"
	fi
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
	ORCH_LOG="$(mktemp)"
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
	) >"${ORCH_LOG}" 2>&1 &
	ORCH_PID="$!"
	sleep 0.6
	if ! kill -0 "${ORCH_PID}" >/dev/null 2>&1; then
		LAST_HTTP_CODE="ORCH_EXITED"
		LAST_BODY="$(cat "${ORCH_LOG}" 2>/dev/null || true)"
		fail_step "StartOrchestrator.${instance_id}"
	fi
}

wait_job_status() {
	local expected="$1"
	local deadline now status
	deadline="$(( $(date +%s) + WAIT_TIMEOUT_SEC ))"
	while true; do
		call_or_fail "GetJob.${expected}" GET "${BASE_URL}/v1/ai/jobs/${JOB_ID}"
		status="$(extract_field "${LAST_BODY}" "status" || true)"
		if [[ "${status}" == "${expected}" ]]; then
			return 0
		fi
		if [[ "${expected}" == "succeeded" ]] && [[ "${status}" =~ ^(failed|canceled)$ ]]; then
			fail_step "JobUnexpectedTerminal.${status}" "${LAST_HTTP_CODE}" "${LAST_BODY}"
		fi
		now="$(date +%s)"
		if (( now > deadline )); then
			fail_step "WaitJobStatusTimeout.${expected}" "TIMEOUT" "${LAST_BODY}"
		fi
		sleep "${POLL_INTERVAL_SEC}"
	done
}

extract_owner_from_toolcalls() {
	local body="$1"
	if ! need_cmd jq; then
		fail_step "OwnerExtractRequiresJQ" "MISSING_JQ" "${body}"
	fi
	printf '%s' "${body}" | jq -r '
		(.toolCalls // .data.toolCalls // [])
		| map((.requestJSON // .request_json // "{}") | fromjson? | .instance_id // empty)
		| map(select(. != ""))
		| unique
		| join(",")
	' 2>/dev/null
}

rand="${RAND:-$RANDOM}"
now_epoch="$(date -u +%s)"
start_epoch="$((now_epoch - 1800))"
fingerprint="b2-l1-fp-${rand}"
CLAIM_OWNER="b2-claimer-${rand}"
FINAL_OWNER="b2-finalizer-${rand}"

ingest_body=$(cat <<EOF
{"idempotencyKey":"idem-b2-l1-ingest-${rand}","fingerprint":"${fingerprint}","status":"firing","severity":"P1","service":"b2-l1-svc","cluster":"prod-b2","namespace":"default","workload":"b2-workload","lastSeenAt":{"seconds":${now_epoch},"nanos":0}}
EOF
)
call_or_fail "IngestAlertEvent" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_body}"
INCIDENT_ID="$(extract_field "${LAST_BODY}" "incidentID" "incident_id" || true)"
if [[ -z "${INCIDENT_ID}" ]]; then
	fail_step "ParseIncidentID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

run_body=$(cat <<EOF
{"incidentID":"${INCIDENT_ID}","idempotencyKey":"idem-b2-l1-run-${rand}","pipeline":"basic_rca","trigger":"manual","timeRangeStart":{"seconds":${start_epoch},"nanos":0},"timeRangeEnd":{"seconds":${now_epoch},"nanos":0},"inputHintsJSON":"{\"scenario\":\"B2_L1\"}","createdBy":"system"}
EOF
)
call_or_fail "RunAIJob" POST "${BASE_URL}/v1/incidents/${INCIDENT_ID}/ai:run" "${run_body}"
JOB_ID="$(extract_field "${LAST_BODY}" "jobID" "job_id" || true)"
if [[ -z "${JOB_ID}" ]]; then
	fail_step "ParseJobID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

call_or_fail "ClaimJobByOwnerA" POST "${BASE_URL}/v1/ai/jobs/${JOB_ID}/start" "" "${CLAIM_OWNER}"
wait_job_status "running"

sleep "${LEASE_EXPIRE_WAIT_SEC}"

# Trigger reclaim path via queued list.
call_or_fail "TriggerReclaimByList" GET "${BASE_URL}/v1/ai/jobs?status=queued&offset=0&limit=20"

wait_job_status "queued"

start_orchestrator "${FINAL_OWNER}"
wait_job_status "succeeded"

call_or_fail "ListToolCalls" GET "${BASE_URL}/v1/ai/jobs/${JOB_ID}/tool-calls?offset=0&limit=50"
owner="$(extract_owner_from_toolcalls "${LAST_BODY}")"
if [[ -z "${owner}" ]] || [[ "${owner}" != "${FINAL_OWNER}" ]]; then
	fail_step "FinalOwnerMismatch" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

echo "PASS B2"
echo "incident_id=${INCIDENT_ID}"
echo "job_id=${JOB_ID}"
echo "claim_owner=${CLAIM_OWNER}"
echo "final_owner=${FINAL_OWNER}"
