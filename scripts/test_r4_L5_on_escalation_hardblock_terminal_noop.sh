#!/usr/bin/env bash
set -euo pipefail

CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
WAIT_TIMEOUT_SEC="${WAIT_TIMEOUT_SEC:-60}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
CONFIG_PATH="${CONFIG_PATH:-${REPO_ROOT}/configs/rca-apiserver.yaml}"
SERVER_CMD_BASE="${SERVER_CMD_BASE:-GOLANG_PROTOBUF_REGISTRATION_CONFLICT=warn go run ./cmd/rca-apiserver --config ${CONFIG_PATH}}"

PORT="${PORT:-$((19020 + RANDOM % 120))}"
BASE_URL="${BASE_URL:-http://127.0.0.1:${PORT}}"

LAST_HTTP_CODE=""
LAST_BODY=""
INCIDENT_ID_SAMPLE=""
JOB_ID_SAMPLE=""
SERVER_PID=""
SERVER_LOG=""

truncate_2kb() {
	printf '%s' "${1:-}" | head -c 2048
}

fail_step() {
	local step="$1"
	local code="${2:-${LAST_HTTP_CODE:-UNKNOWN}}"
	local body="${3:-${LAST_BODY:-}}"

	echo "FAIL R4_L5 step=${step}"
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

need_cmd() {
	command -v "$1" >/dev/null 2>&1
}

http_json() {
	local method="$1"
	local url="$2"
	local body="${3:-}"

	local tmp_body tmp_err code rc
	tmp_body="$(mktemp)"
	tmp_err="$(mktemp)"

	local -a cmd
	cmd=("${CURL}" -sS -o "${tmp_body}" -w "%{http_code}" -X "${method}" "${url}" -H "Accept: application/json" -H "X-Scopes: ${SCOPES}")
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

	if ! http_json "${method}" "${url}" "${body}"; then
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
	SERVER_LOG="$(mktemp)"
	(
		cd "${REPO_ROOT}" && \
			bash -lc "${SERVER_CMD_BASE} --http.addr=127.0.0.1:${PORT} --redis.enabled=false"
	) >"${SERVER_LOG}" 2>&1 &
	SERVER_PID="$!"

	local deadline="$(( $(date +%s) + WAIT_TIMEOUT_SEC ))"
	while true; do
		if "${CURL}" -sS "${BASE_URL}/healthz" >/dev/null 2>&1; then
			return 0
		fi
		if ! kill -0 "${SERVER_PID}" >/dev/null 2>&1; then
			LAST_HTTP_CODE="SERVER_EXITED"
			LAST_BODY="$(cat "${SERVER_LOG}" 2>/dev/null || true)"
			fail_step "StartServer"
		fi
		if (( $(date +%s) > deadline )); then
			LAST_HTTP_CODE="SERVER_TIMEOUT"
			LAST_BODY="$(cat "${SERVER_LOG}" 2>/dev/null || true)"
			fail_step "StartServer"
		fi
		sleep 0.5
	done
}

stop_server() {
	if [[ -n "${SERVER_PID}" ]]; then
		kill "${SERVER_PID}" >/dev/null 2>&1 || true
		wait "${SERVER_PID}" >/dev/null 2>&1 || true
		SERVER_PID=""
	fi
	if [[ -n "${SERVER_LOG}" ]]; then
		rm -f "${SERVER_LOG}"
		SERVER_LOG=""
	fi
}

cleanup() {
	stop_server
}
trap cleanup EXIT

extract_incident_id_or_fail() {
	local json="$1"
	local step="$2"
	local incident_id
	incident_id="$(json_get "${json}" '.incidentID // .data.incidentID // .incident.incidentID // .data.incident.incidentID // empty')"
	if [[ -z "${incident_id}" ]]; then
		fail_step "${step}" "ASSERT_INCIDENT_ID" "${json}"
	fi
	printf '%s' "${incident_id}"
}

create_incident_or_fail() {
	local severity="$1"
	local service="$2"
	local step="$3"
	local body
	body="$(cat <<JSON
{"namespace":"default","workloadKind":"Deployment","workloadName":"${service}","service":"${service}","severity":"${severity}"}
JSON
)"
	call_or_fail "${step}" POST "${BASE_URL}/v1/incidents" "${body}"
	extract_incident_id_or_fail "${LAST_BODY}" "${step}"
}

list_jobs_for_incident_or_fail() {
	local incident_id="$1"
	local step="$2"
	call_or_fail "${step}" GET "${BASE_URL}/v1/incidents/${incident_id}/ai?offset=0&limit=50"
}

assert_no_incident_jobs_or_fail() {
	local incident_id="$1"
	local step="$2"
	list_jobs_for_incident_or_fail "${incident_id}" "${step}"
	local total
	total="$(json_get "${LAST_BODY}" '.totalCount // .data.totalCount // 0')"
	if [[ -z "${total}" || ! "${total}" =~ ^[0-9]+$ || "${total}" != "0" ]]; then
		fail_step "${step}" "ASSERT_NO_JOBS" "${LAST_BODY}"
	fi
}

list_job_by_trigger_or_empty() {
	local incident_id="$1"
	local trigger="$2"
	list_jobs_for_incident_or_fail "${incident_id}" "ListJobs.${incident_id}.${trigger}"
	printf '%s' "${LAST_BODY}" | jq -r --arg t "${trigger}" '
		(.jobs // .data.jobs // [])
		| map(select(((.trigger // "") | ascii_downcase) == ($t | ascii_downcase)))
		| .[0].jobID // empty
	' 2>/dev/null
}

assert_job_queued_or_fail() {
	local job_id="$1"
	local step="$2"
	call_or_fail "${step}" GET "${BASE_URL}/v1/ai/jobs/${job_id}"
	local status
	status="$(json_get "${LAST_BODY}" '.job.status // .data.job.status // .status // .data.status // empty')"
	if [[ "${status}" != "queued" ]]; then
		fail_step "${step}" "ASSERT_JOB_QUEUED" "${LAST_BODY}"
	fi
}

assert_log_contains_or_fail() {
	local step="$1"
	local pattern="$2"
	sleep 0.3
	if ! grep -Fq "${pattern}" "${SERVER_LOG}"; then
		LAST_HTTP_CODE="ASSERT_LOG_MISSING"
		LAST_BODY="missing pattern: ${pattern}"
		fail_step "${step}"
	fi
}

activate_policy_or_fail() {
	local body="$1"
	local policy_id

	call_or_fail "Policy.Create" POST "${BASE_URL}/v1/alerting-policies" "${body}"
	policy_id="$(json_get "${LAST_BODY}" '.alerting_policy.id // .data.alerting_policy.id // empty')"
	if [[ -z "${policy_id}" ]]; then
		fail_step "Policy.Create.ParseID" "ASSERT_POLICY_ID" "${LAST_BODY}"
	fi
	call_or_fail "Policy.Activate" POST "${BASE_URL}/v1/alerting-policies/${policy_id}/activate" '{"operator":"script:r4_l5"}'
}

if ! need_cmd jq; then
	fail_step "Precheck.MissingJQ" "MISSING_JQ" "jq is required"
fi

start_server_or_fail
activate_policy_or_fail "$(cat <<'JSON'
{"name":"r4-l5-on-escalation","description":"On-escalation hardblock regression policy","config":{"version":1,"defaults":{"on_ingest":{"enabled":false},"on_escalation":{"enabled":false},"scheduled":{"enabled":false}},"triggers":{"on_escalation":{"rules":[{"name":"on-escalation-run","match":{"incident_severity":["p1"]},"action":{"run":true,"pipeline":"basic_rca","window_seconds":7200}}]}}}}
JSON
)"
rand="${RANDOM}"

# A: terminal incident -> blocked and no AIJob.
incident_terminal="$(create_incident_or_fail "P3" "svc-r4-l5-terminal-${rand}" "A.CreateIncident")"
INCIDENT_ID_SAMPLE="${incident_terminal}"
call_or_fail "A.UpdateToTerminalWithEscalation" PUT "${BASE_URL}/v1/incidents/${incident_terminal}" '{"status":"resolved","severity":"P1"}'
assert_no_incident_jobs_or_fail "${incident_terminal}" "A.AssertNoJobCreated"
assert_log_contains_or_fail "A.AssertBlockedDecision" "blocked_terminal_incident"
echo "PASS R4_L5 step=A.TerminalBlocked"

# B: no-op escalation -> blocked and no AIJob.
incident_noop="$(create_incident_or_fail "P1" "svc-r4-l5-noop-${rand}" "B.CreateIncident")"
INCIDENT_ID_SAMPLE="${incident_noop}"
call_or_fail "B.UpdateNoOpEscalation" PUT "${BASE_URL}/v1/incidents/${incident_noop}" '{"severity":"P1"}'
assert_no_incident_jobs_or_fail "${incident_noop}" "B.AssertNoJobCreated"
assert_log_contains_or_fail "B.AssertBlockedDecision" "blocked_noop_escalation"
echo "PASS R4_L5 step=B.NoOpBlocked"

# C (optional control): real escalation -> queued AIJob created.
incident_real="$(create_incident_or_fail "P3" "svc-r4-l5-real-${rand}" "C.CreateIncident")"
INCIDENT_ID_SAMPLE="${incident_real}"
call_or_fail "C.UpdateRealEscalation" PUT "${BASE_URL}/v1/incidents/${incident_real}" '{"severity":"P1"}'
JOB_ID_SAMPLE="$(list_job_by_trigger_or_empty "${incident_real}" "on_escalation")"
if [[ -z "${JOB_ID_SAMPLE}" ]]; then
	fail_step "C.AssertJobCreated" "ASSERT_JOB_NOT_FOUND" "${LAST_BODY}"
fi
assert_job_queued_or_fail "${JOB_ID_SAMPLE}" "C.AssertQueuedJob"
echo "PASS R4_L5 step=C.RealEscalationCreated"

echo "PASS R4_L5 on_escalation hardblock terminal noop"
