#!/usr/bin/env bash
set -euo pipefail

CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
WAIT_TIMEOUT_SEC="${WAIT_TIMEOUT_SEC:-60}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
CONFIG_PATH="${CONFIG_PATH:-${REPO_ROOT}/configs/rca-apiserver.yaml}"
SERVER_CMD_BASE="${SERVER_CMD_BASE:-GOLANG_PROTOBUF_REGISTRATION_CONFLICT=warn go run ./cmd/rca-apiserver --config ${CONFIG_PATH}}"

PORT="${PORT:-$((18980 + RANDOM % 120))}"
BASE_URL="${BASE_URL:-http://127.0.0.1:${PORT}}"

LAST_HTTP_CODE=""
LAST_BODY=""
INCIDENT_ID_SAMPLE=""
JOB_ID_SAMPLE=""
SILENCE_ID_SAMPLE=""
SERVER_PID=""
SERVER_LOG=""
POLICY_FILE=""

truncate_2kb() {
	printf '%s' "${1:-}" | head -c 2048
}

fail_step() {
	local step="$1"
	local code="${2:-${LAST_HTTP_CODE:-UNKNOWN}}"
	local body="${3:-${LAST_BODY:-}}"

	echo "FAIL R4_L4 step=${step}"
	echo "http_code=${code}"
	echo "response_body<<EOF"
	truncate_2kb "${body}"
	echo
	echo "EOF"
	echo "incident_id=${INCIDENT_ID_SAMPLE:-NONE}"
	echo "job_id=${JOB_ID_SAMPLE:-NONE}"
	echo "silence_id=${SILENCE_ID_SAMPLE:-NONE}"
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
			bash -lc "${SERVER_CMD_BASE} --http.addr=127.0.0.1:${PORT} --redis.enabled=false --alerting-policy-path='${POLICY_FILE}' --alerting-policy-strict=true --alerting.ingest_policy.dedup_window_seconds=300"
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
	rm -f "${POLICY_FILE:-}"
}
trap cleanup EXIT

queued_total_or_fail() {
	local step="$1"
	call_or_fail "${step}" GET "${BASE_URL}/v1/ai/jobs?status=queued&offset=0&limit=50"
	local total
	total="$(json_get "${LAST_BODY}" '.totalCount // .data.totalCount // 0')"
	if [[ -z "${total}" || ! "${total}" =~ ^[0-9]+$ ]]; then
		fail_step "${step}" "ASSERT_TOTAL_COUNT" "${LAST_BODY}"
	fi
	printf '%s' "${total}"
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

extract_incident_id_or_empty() {
	json_get "${1}" '.incidentID // .data.incidentID // empty'
}

if ! need_cmd jq; then
	fail_step "Precheck.MissingJQ" "MISSING_JQ" "jq is required"
fi

POLICY_FILE="$(mktemp)"
cat >"${POLICY_FILE}" <<'YAML'
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
      - name: "on-ingest-run"
        match:
          alert_name: "r4-l4-on-ingest"
        action:
          run: true
          pipeline: "basic_rca"
          window_seconds: 1800
YAML

start_server_or_fail

now_epoch="$(date -u +%s)"
rand="${RANDOM}"

baseline_jobs="$(queued_total_or_fail "Baseline.ListQueued")"

# A: silence hit -> blocked_silenced and no AIJob.
silence_fp="fp-r4-l4-silence-${rand}"
silence_create_body="$(cat <<JSON
{"namespace":"default","enabled":true,"startsAt":{"seconds":$((now_epoch - 60)),"nanos":0},"endsAt":{"seconds":$((now_epoch + 3600)),"nanos":0},"reason":"r4-l4-silence","createdBy":"tester","matchers":[{"key":"fingerprint","op":"=","value":"${silence_fp}"}]}
JSON
)"
call_or_fail "A.CreateSilence" POST "${BASE_URL}/v1/silences" "${silence_create_body}"
SILENCE_ID_SAMPLE="$(json_get "${LAST_BODY}" '.silence.silenceID // .data.silence.silenceID // .silenceID // .data.silenceID // empty')"
if [[ -z "${SILENCE_ID_SAMPLE}" ]]; then
	fail_step "A.CreateSilence.ParseSilenceID" "ASSERT_SILENCE_ID" "${LAST_BODY}"
fi

ingest_silenced_body="$(cat <<JSON
{"idempotencyKey":"idem-r4-l4-silenced-${rand}","fingerprint":"${silence_fp}","status":"firing","severity":"P1","alertName":"r4-l4-on-ingest","service":"svc-r4-l4-silenced","cluster":"prod-r4","namespace":"default","workload":"svc-r4-l4-silenced","lastSeenAt":{"seconds":${now_epoch},"nanos":0}}
JSON
)"
call_or_fail "A.IngestSilenced" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_silenced_body}"
silenced_flag="$(json_get "${LAST_BODY}" '((.silenced // .data.silenced // false) | tostring)')"
if [[ "${silenced_flag}" != "true" ]]; then
	fail_step "A.IngestSilenced.AssertSilenced" "ASSERT_SILENCED_TRUE" "${LAST_BODY}"
fi
after_silenced_jobs="$(queued_total_or_fail "A.ListQueuedAfterSilenced")"
if (( after_silenced_jobs != baseline_jobs )); then
	fail_step "A.AssertNoJobCreated" "ASSERT_QUEUE_UNCHANGED" "${LAST_BODY}"
fi
assert_log_contains_or_fail "A.AssertBlockedDecision" "blocked_silenced"
echo "PASS R4_L4 step=A.SilencedBlocked"

# B: suppressIncident=true(dedup) -> blocked_suppress_incident and no new AIJob.
fp_dedup="fp-r4-l4-dedup-${rand}"
ingest_first_body="$(cat <<JSON
{"idempotencyKey":"idem-r4-l4-dedup-a-${rand}","fingerprint":"${fp_dedup}","status":"firing","severity":"P1","alertName":"r4-l4-on-ingest","service":"svc-r4-l4-dedup","cluster":"prod-r4","namespace":"default","workload":"svc-r4-l4-dedup","lastSeenAt":{"seconds":$((now_epoch + 1)),"nanos":0}}
JSON
)"
call_or_fail "B.IngestFirst" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_first_body}"
INCIDENT_ID_SAMPLE="$(extract_incident_id_or_empty "${LAST_BODY}")"
if [[ -z "${INCIDENT_ID_SAMPLE}" ]]; then
	fail_step "B.IngestFirst.ParseIncidentID" "ASSERT_INCIDENT_ID" "${LAST_BODY}"
fi
jobs_after_first="$(queued_total_or_fail "B.ListQueuedAfterFirst")"
if (( jobs_after_first != baseline_jobs + 1 )); then
	fail_step "B.AssertFirstCreatedJob" "ASSERT_QUEUE_PLUS_ONE" "${LAST_BODY}"
fi

ingest_second_body="$(cat <<JSON
{"idempotencyKey":"idem-r4-l4-dedup-b-${rand}","fingerprint":"${fp_dedup}","status":"firing","severity":"P1","alertName":"r4-l4-on-ingest","service":"svc-r4-l4-dedup","cluster":"prod-r4","namespace":"default","workload":"svc-r4-l4-dedup","lastSeenAt":{"seconds":$((now_epoch + 2)),"nanos":0}}
JSON
)"
call_or_fail "B.IngestSecondDedup" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_second_body}"
jobs_after_second="$(queued_total_or_fail "B.ListQueuedAfterSecond")"
if (( jobs_after_second != jobs_after_first )); then
	fail_step "B.AssertNoNewJobWhenSuppressIncident" "ASSERT_QUEUE_UNCHANGED" "${LAST_BODY}"
fi
assert_log_contains_or_fail "B.AssertBlockedDecision" "blocked_suppress_incident"
echo "PASS R4_L4 step=B.SuppressIncidentBlocked"

echo "PASS R4_L4 on_ingest hardblock silence suppress"
