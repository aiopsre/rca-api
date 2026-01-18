#!/usr/bin/env bash
set -euo pipefail

CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
WAIT_TIMEOUT_SEC="${WAIT_TIMEOUT_SEC:-60}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
CONFIG_PATH="${CONFIG_PATH:-${REPO_ROOT}/configs/rca-apiserver.yaml}"
SERVER_CMD_BASE="${SERVER_CMD_BASE:-GOLANG_PROTOBUF_REGISTRATION_CONFLICT=warn go run ./cmd/rca-apiserver --config ${CONFIG_PATH}}"

PORT_RUN="${PORT_RUN:-$((18840 + RANDOM % 120))}"
PORT_DEFAULT="${PORT_DEFAULT:-$((PORT_RUN + 1))}"
BASE_URL_RUN="${BASE_URL_RUN:-http://127.0.0.1:${PORT_RUN}}"
BASE_URL_DEFAULT="${BASE_URL_DEFAULT:-http://127.0.0.1:${PORT_DEFAULT}}"

LAST_HTTP_CODE=""
LAST_BODY=""
INCIDENT_ID_SAMPLE=""
JOB_ID_SAMPLE=""
SERVER_PID=""
SERVER_LOG=""
POLICY_RUN=""

truncate_2kb() {
	printf '%s' "${1:-}" | head -c 2048
}

fail_step() {
	local step="$1"
	local code="${2:-${LAST_HTTP_CODE:-UNKNOWN}}"
	local body="${3:-${LAST_BODY:-}}"

	echo "FAIL R4_L3 step=${step}"
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
	local base_url="$1"
	local port="$2"
	local policy_path="$3"
	local strict="$4"
	local name="$5"

	SERVER_LOG="$(mktemp)"
	(
		cd "${REPO_ROOT}" && \
			bash -lc "${SERVER_CMD_BASE} --http.addr=127.0.0.1:${port} --redis.enabled=false --alerting-policy-path='${policy_path}' --alerting-policy-strict=${strict}"
	) >"${SERVER_LOG}" 2>&1 &
	SERVER_PID="$!"

	local deadline="$(( $(date +%s) + WAIT_TIMEOUT_SEC ))"
	while true; do
		if "${CURL}" -sS "${base_url}/healthz" >/dev/null 2>&1; then
			return 0
		fi
		if ! kill -0 "${SERVER_PID}" >/dev/null 2>&1; then
			LAST_HTTP_CODE="SERVER_EXITED"
			LAST_BODY="$(cat "${SERVER_LOG}" 2>/dev/null || true)"
			fail_step "StartServer.${name}"
		fi
		if (( $(date +%s) > deadline )); then
			LAST_HTTP_CODE="SERVER_TIMEOUT"
			LAST_BODY="$(cat "${SERVER_LOG}" 2>/dev/null || true)"
			fail_step "StartServer.${name}"
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

extract_incident_id_or_fail() {
	local json="$1"
	local step="$2"
	local incident_id
	incident_id="$(json_get "${json}" '.incidentID // .data.incidentID // .incident.incidentID // .data.incident.incidentID // empty')"
	if [[ -z "${incident_id}" ]]; then
		fail_step "${step}" "ASSERT_INCIDENT_ID_FAILED" "${json}"
	fi
	printf '%s' "${incident_id}"
}

list_job_by_trigger() {
	local base_url="$1"
	local incident_id="$2"
	local trigger="$3"
	call_or_fail "ListJobs.${incident_id}.${trigger}" GET "${base_url}/v1/incidents/${incident_id}/ai?offset=0&limit=50"
	printf '%s' "${LAST_BODY}" | jq -r --arg t "${trigger}" '
		(.jobs // .data.jobs // [])
		| map(select(((.trigger // "") | ascii_downcase) == ($t | ascii_downcase)))
		| .[0].jobID // empty
	' 2>/dev/null
}

assert_no_incident_jobs_or_fail() {
	local base_url="$1"
	local incident_id="$2"
	local step="$3"
	call_or_fail "${step}" GET "${base_url}/v1/incidents/${incident_id}/ai?offset=0&limit=20"
	local total
	total="$(json_get "${LAST_BODY}" '.totalCount // .data.totalCount // 0')"
	if [[ -z "${total}" || "${total}" != "0" ]]; then
		fail_step "${step}" "ASSERT_NO_JOBS_FAILED" "${LAST_BODY}"
	fi
}

fetch_job_or_fail() {
	local base_url="$1"
	local job_id="$2"
	local step="$3"
	call_or_fail "${step}" GET "${base_url}/v1/ai/jobs/${job_id}"
}

extract_time_range_or_fail() {
	local json="$1"
	local step="$2"
	local pair
	pair="$(printf '%s' "${json}" | jq -r '
		def ts:
			if . == null then empty
			elif (type == "object") and (.seconds != null) then (.seconds | tonumber)
			elif (type == "string") then (try fromdateiso8601 catch empty)
			else empty
			end;
		((.job.timeRangeStart // .data.job.timeRangeStart // .timeRangeStart // .data.timeRangeStart) | ts) as $s
		| ((.job.timeRangeEnd // .data.job.timeRangeEnd // .timeRangeEnd // .data.timeRangeEnd) | ts) as $e
		| if (($s | tostring) == "" or ($e | tostring) == "") then empty else "\($s) \($e)" end
	' 2>/dev/null)"
	if [[ -z "${pair}" ]]; then
		fail_step "${step}" "ASSERT_TIME_RANGE_MISSING" "${json}"
	fi
	printf '%s' "${pair}"
}

assert_time_range_window_or_fail() {
	local json="$1"
	local expected="$2"
	local step="$3"
	local pair start end diff
	pair="$(extract_time_range_or_fail "${json}" "${step}")"
	start="${pair%% *}"
	end="${pair##* }"
	if (( start > end )); then
		fail_step "${step}" "ASSERT_TIME_RANGE_ORDER" "${json}"
	fi
	diff="$(( end - start ))"
	if (( diff != expected )); then
		fail_step "${step}" "ASSERT_TIME_RANGE_WINDOW_${expected}" "${json}"
	fi
}

assert_time_range_bucket_or_fail() {
	local json="$1"
	local bucket="$2"
	local step="$3"
	local pair start end
	pair="$(extract_time_range_or_fail "${json}" "${step}")"
	start="${pair%% *}"
	end="${pair##* }"
	if (( start % bucket != 0 || end % bucket != 0 )); then
		fail_step "${step}" "ASSERT_BUCKET_ALIGN_${bucket}" "${json}"
	fi
}

create_incident_or_fail() {
	local base_url="$1"
	local service="$2"
	local severity="$3"
	local step="$4"
	local body
	body="$(cat <<JSON
{"namespace":"default","workloadKind":"Deployment","workloadName":"${service}","service":"${service}","severity":"${severity}"}
JSON
)"
	call_or_fail "${step}" POST "${base_url}/v1/incidents" "${body}"
	extract_incident_id_or_fail "${LAST_BODY}" "${step}"
}

cleanup() {
	stop_server
	rm -f "${POLICY_RUN:-}"
}
trap cleanup EXIT

if ! need_cmd jq; then
	fail_step "Precheck.MissingJQ" "MISSING_JQ" "jq is required"
fi

POLICY_RUN="$(mktemp)"
cat >"${POLICY_RUN}" <<'YAML'
version: 1
defaults:
  on_ingest:
    enabled: false
  on_escalation:
    enabled: false
  scheduled:
    enabled: false
triggers:
  on_ingest:
    rules:
      - name: "ingest-run"
        match:
          alert_name: "r4-l3-ingest"
        action:
          run: true
          pipeline: "basic_rca"
          window_seconds: 1800
  on_escalation:
    rules:
      - name: "escalation-run"
        match:
          incident_severity: ["p1"]
        action:
          run: true
          pipeline: "basic_rca"
          window_seconds: 7200
  scheduled:
    rules:
      - name: "scheduled-run"
        match: {}
        action:
          run: true
          pipeline: "basic_rca"
          window_seconds: 3600
          idempotency_bucket_seconds: 3600
YAML

start_server_or_fail "${BASE_URL_RUN}" "${PORT_RUN}" "${POLICY_RUN}" "true" "runplan-enabled"

now_epoch="$(date -u +%s)"
rand="${RANDOM}"

# A1: on_ingest rule enabled -> queued AIJob created.
ingest_body="$(cat <<JSON
{"idempotencyKey":"idem-r4-l3-ingest-${rand}","fingerprint":"fp-r4-l3-ingest-${rand}","status":"firing","severity":"P1","alertName":"r4-l3-ingest","service":"svc-r4-l3-ingest","cluster":"prod-r4","namespace":"default","workload":"svc-r4-l3-ingest","lastSeenAt":{"seconds":${now_epoch},"nanos":0},"labelsJSON":"{\"service\":\"svc-r4-l3-ingest\"}"}
JSON
)"
call_or_fail "A1.OnIngest.Ingest" POST "${BASE_URL_RUN}/v1/alert-events:ingest" "${ingest_body}"
INCIDENT_ID_SAMPLE="$(extract_incident_id_or_fail "${LAST_BODY}" "A1.OnIngest.IncidentID")"
JOB_ID_SAMPLE="$(list_job_by_trigger "${BASE_URL_RUN}" "${INCIDENT_ID_SAMPLE}" "on_ingest")"
if [[ -z "${JOB_ID_SAMPLE}" ]]; then
	fail_step "A1.OnIngest.JobCreated" "ASSERT_JOB_NOT_FOUND" "${LAST_BODY}"
fi
fetch_job_or_fail "${BASE_URL_RUN}" "${JOB_ID_SAMPLE}" "A1.OnIngest.GetJob"
assert_time_range_window_or_fail "${LAST_BODY}" 1800 "A1.OnIngest.TimeRange"
echo "PASS R4_L3 step=A1.OnIngest"

# A2: on_escalation rule enabled -> queued AIJob created.
incident_escalation="$(create_incident_or_fail "${BASE_URL_RUN}" "svc-r4-l3-escalation-${rand}" "P3" "A2.OnEscalation.CreateIncident")"
INCIDENT_ID_SAMPLE="${incident_escalation}"
call_or_fail "A2.OnEscalation.UpdateSeverity" PUT "${BASE_URL_RUN}/v1/incidents/${incident_escalation}" '{"severity":"P1"}'
JOB_ID_SAMPLE="$(list_job_by_trigger "${BASE_URL_RUN}" "${incident_escalation}" "on_escalation")"
if [[ -z "${JOB_ID_SAMPLE}" ]]; then
	fail_step "A2.OnEscalation.JobCreated" "ASSERT_JOB_NOT_FOUND" "${LAST_BODY}"
fi
fetch_job_or_fail "${BASE_URL_RUN}" "${JOB_ID_SAMPLE}" "A2.OnEscalation.GetJob"
assert_time_range_window_or_fail "${LAST_BODY}" 7200 "A2.OnEscalation.TimeRange"
echo "PASS R4_L3 step=A2.OnEscalation"

# A3: scheduled rule enabled -> queued AIJob created and bucket aligned.
incident_scheduled="$(create_incident_or_fail "${BASE_URL_RUN}" "svc-r4-l3-scheduled-${rand}" "P2" "A3.Scheduled.CreateIncident")"
INCIDENT_ID_SAMPLE="${incident_scheduled}"
call_or_fail "A3.Scheduled.Trigger" POST "${BASE_URL_RUN}/v1/incidents/${incident_scheduled}/ai/scheduled-run" '{"schedulerName":"r4-l3-cron"}'
should_run="$(json_get "${LAST_BODY}" '
	if has("shouldRun") then .shouldRun
	elif ((.data // null) | type) == "object" and (.data | has("shouldRun")) then .data.shouldRun
	else empty
	end
')"
if [[ "${should_run}" != "true" ]]; then
	fail_step "A3.Scheduled.ShouldRun" "ASSERT_SHOULD_RUN" "${LAST_BODY}"
fi
JOB_ID_SAMPLE="$(json_get "${LAST_BODY}" '.jobID // .data.jobID // empty')"
if [[ -z "${JOB_ID_SAMPLE}" ]]; then
	fail_step "A3.Scheduled.JobID" "ASSERT_JOB_ID" "${LAST_BODY}"
fi
fetch_job_or_fail "${BASE_URL_RUN}" "${JOB_ID_SAMPLE}" "A3.Scheduled.GetJob"
assert_time_range_window_or_fail "${LAST_BODY}" 3600 "A3.Scheduled.TimeRangeWindow"
assert_time_range_bucket_or_fail "${LAST_BODY}" 3600 "A3.Scheduled.TimeRangeBucket"
echo "PASS R4_L3 step=A3.Scheduled"

stop_server

DEFAULT_POLICY="${REPO_ROOT}/configs/alerting_policy.yaml"
start_server_or_fail "${BASE_URL_DEFAULT}" "${PORT_DEFAULT}" "${DEFAULT_POLICY}" "true" "runplan-default"

# B1: default run=false -> on_ingest no AIJob created.
ingest_default_body="$(cat <<JSON
{"idempotencyKey":"idem-r4-l3-default-ingest-${rand}","fingerprint":"fp-r4-l3-default-ingest-${rand}","status":"firing","severity":"P1","alertName":"r4-l3-default-ingest","service":"svc-r4-l3-default-ingest","cluster":"prod-r4","namespace":"default","workload":"svc-r4-l3-default-ingest","lastSeenAt":{"seconds":${now_epoch},"nanos":0}}
JSON
)"
call_or_fail "B1.Default.OnIngest.Ingest" POST "${BASE_URL_DEFAULT}/v1/alert-events:ingest" "${ingest_default_body}"
incident_default_ingest="$(extract_incident_id_or_fail "${LAST_BODY}" "B1.Default.OnIngest.IncidentID")"
INCIDENT_ID_SAMPLE="${incident_default_ingest}"
assert_no_incident_jobs_or_fail "${BASE_URL_DEFAULT}" "${incident_default_ingest}" "B1.Default.OnIngest.NoJob"
echo "PASS R4_L3 step=B1.Default.OnIngest"

# B2: default run=false -> on_escalation no AIJob created.
incident_default_escalation="$(create_incident_or_fail "${BASE_URL_DEFAULT}" "svc-r4-l3-default-escalation-${rand}" "P3" "B2.Default.OnEscalation.CreateIncident")"
call_or_fail "B2.Default.OnEscalation.UpdateSeverity" PUT "${BASE_URL_DEFAULT}/v1/incidents/${incident_default_escalation}" '{"severity":"P1"}'
assert_no_incident_jobs_or_fail "${BASE_URL_DEFAULT}" "${incident_default_escalation}" "B2.Default.OnEscalation.NoJob"
echo "PASS R4_L3 step=B2.Default.OnEscalation"

# B3: default run=false -> scheduled no AIJob created.
incident_default_scheduled="$(create_incident_or_fail "${BASE_URL_DEFAULT}" "svc-r4-l3-default-scheduled-${rand}" "P2" "B3.Default.Scheduled.CreateIncident")"
call_or_fail "B3.Default.Scheduled.Trigger" POST "${BASE_URL_DEFAULT}/v1/incidents/${incident_default_scheduled}/ai/scheduled-run" '{"schedulerName":"r4-l3-default"}'
should_run_default="$(json_get "${LAST_BODY}" '
	if has("shouldRun") then .shouldRun
	elif ((.data // null) | type) == "object" and (.data | has("shouldRun")) then .data.shouldRun
	else empty
	end
')"
if [[ "${should_run_default}" != "false" ]]; then
	fail_step "B3.Default.Scheduled.ShouldRunFalse" "ASSERT_SHOULD_NOT_RUN" "${LAST_BODY}"
fi
job_default="$(json_get "${LAST_BODY}" '.jobID // .data.jobID // empty')"
if [[ -n "${job_default}" ]]; then
	fail_step "B3.Default.Scheduled.NoJobID" "ASSERT_JOB_ID_EMPTY" "${LAST_BODY}"
fi
assert_no_incident_jobs_or_fail "${BASE_URL_DEFAULT}" "${incident_default_scheduled}" "B3.Default.Scheduled.NoJob"
echo "PASS R4_L3 step=B3.Default.Scheduled"

# B4: manual entrypoint remains unaffected.
manual_start="$(( now_epoch - 1800 ))"
manual_body="$(cat <<JSON
{"incidentID":"${incident_default_scheduled}","idempotencyKey":"idem-r4-l3-manual-${rand}","pipeline":"basic_rca","trigger":"manual","timeRangeStart":{"seconds":${manual_start},"nanos":0},"timeRangeEnd":{"seconds":${now_epoch},"nanos":0},"createdBy":"system"}
JSON
)"
call_or_fail "B4.Manual.Run" POST "${BASE_URL_DEFAULT}/v1/incidents/${incident_default_scheduled}/ai:run" "${manual_body}"
manual_job_id="$(json_get "${LAST_BODY}" '.jobID // .data.jobID // empty')"
if [[ -z "${manual_job_id}" ]]; then
	fail_step "B4.Manual.JobID" "ASSERT_JOB_ID" "${LAST_BODY}"
fi
JOB_ID_SAMPLE="${manual_job_id}"
fetch_job_or_fail "${BASE_URL_DEFAULT}" "${manual_job_id}" "B4.Manual.GetJob"
manual_trigger="$(json_get "${LAST_BODY}" '.job.trigger // .data.job.trigger // .trigger // .data.trigger // empty')"
if [[ "${manual_trigger}" != "manual" ]]; then
	fail_step "B4.Manual.Trigger" "ASSERT_TRIGGER_MANUAL" "${LAST_BODY}"
fi
echo "PASS R4_L3 step=B4.Manual"

echo "PASS R4_L3 trigger entrypoints runplan"
