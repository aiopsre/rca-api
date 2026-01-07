#!/usr/bin/env bash
set -euo pipefail

CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
DEBUG="${DEBUG:-0}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
CONFIG_PATH="${CONFIG_PATH:-${REPO_ROOT}/configs/rca-apiserver.yaml}"
SERVER_CMD_BASE="${SERVER_CMD_BASE:-GOLANG_PROTOBUF_REGISTRATION_CONFLICT=warn go run ./cmd/rca-apiserver --config ${CONFIG_PATH}}"

PORT_LEGACY="${PORT_LEGACY:-$((18600 + RANDOM % 120))}"
PORT_DEDUP="${PORT_DEDUP:-$((PORT_LEGACY + 1))}"
BASE_URL_LEGACY="${BASE_URL_LEGACY:-http://127.0.0.1:${PORT_LEGACY}}"
BASE_URL_DEDUP="${BASE_URL_DEDUP:-http://127.0.0.1:${PORT_DEDUP}}"

WAIT_TIMEOUT_SEC="${WAIT_TIMEOUT_SEC:-60}"
QUEUE_LIMIT="${QUEUE_LIMIT:-50}"

LAST_HTTP_CODE=""
LAST_BODY=""
CURRENT_SCENARIO=""
INCIDENT_ID_SAMPLE=""
JOB_ID_SAMPLE=""

LEGACY_LOG=""
DEDUP_LOG=""
LEGACY_PID=""
DEDUP_PID=""

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

	echo "FAIL R4 step=${step}"
	echo "scenario=${CURRENT_SCENARIO:-UNKNOWN}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	truncate_2kb "${body}"
	echo
	echo "EOF"
	echo "incident_id=${INCIDENT_ID_SAMPLE:-NONE}"
	echo "job_id=${JOB_ID_SAMPLE:-NONE}"
	if [[ -n "${LEGACY_LOG}" ]]; then
		echo "legacy_log_tail<<EOF"
		tail -n 80 "${LEGACY_LOG}" 2>/dev/null | head -c 2048
		echo
		echo "EOF"
	fi
	if [[ -n "${DEDUP_LOG}" ]]; then
		echo "dedup_log_tail<<EOF"
		tail -n 80 "${DEDUP_LOG}" 2>/dev/null | head -c 2048
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
	cmd=("${CURL}" -sS -o "${tmp_body}" -w "%{http_code}" -X "${method}" "${url}" -H "Accept: application/json" -H "X-Scopes: ${SCOPES}")
	if [[ -n "${body}" ]]; then
		cmd+=( -H "Content-Type: application/json" -d "${body}" )
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
		LAST_BODY="${curl_err}"
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
				(.[$k] // .data[$k] // .noticeChannel[$k] // .data.noticeChannel[$k] // .incident[$k] // .data.incident[$k] // .silence[$k] // .data.silence[$k] // .event[$k] // .data.event[$k]) |
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

json_value() {
	local json="$1"
	local expr="$2"
	printf '%s' "${json}" | jq -r "${expr}" 2>/dev/null
}

start_server_or_fail() {
	local name="$1"
	local port="$2"
	local extra_flags="$3"
	local log_file="$4"
	local __pid_var="$5"
	local base_url="http://127.0.0.1:${port}"

	(
		cd "${REPO_ROOT}" && \
			bash -lc "${SERVER_CMD_BASE} --http.addr=127.0.0.1:${port} --redis.enabled=false ${extra_flags}"
	) >"${log_file}" 2>&1 &

	local pid="$!"
	printf -v "${__pid_var}" '%s' "${pid}"

	local deadline
	deadline="$(( $(date +%s) + WAIT_TIMEOUT_SEC ))"
	while true; do
		if "${CURL}" -sS "${base_url}/healthz" >/dev/null 2>&1; then
			return 0
		fi
		if ! kill -0 "${pid}" >/dev/null 2>&1; then
			LAST_HTTP_CODE="SERVER_EXITED"
			LAST_BODY="$(cat "${log_file}" 2>/dev/null || true)"
			fail_step "StartServer.${name}"
		fi
		if (( $(date +%s) > deadline )); then
			LAST_HTTP_CODE="SERVER_TIMEOUT"
			LAST_BODY="$(cat "${log_file}" 2>/dev/null || true)"
			fail_step "StartServer.${name}"
		fi
		sleep 0.5
	done
}

cleanup() {
	if [[ -n "${LEGACY_PID}" ]]; then
		kill "${LEGACY_PID}" >/dev/null 2>&1 || true
		wait "${LEGACY_PID}" >/dev/null 2>&1 || true
	fi
	if [[ -n "${DEDUP_PID}" ]]; then
		kill "${DEDUP_PID}" >/dev/null 2>&1 || true
		wait "${DEDUP_PID}" >/dev/null 2>&1 || true
	fi
	rm -f "${LEGACY_LOG:-}" "${DEDUP_LOG:-}"
}
trap cleanup EXIT

if ! need_cmd jq; then
	fail_step "Precheck.MissingJQ" "MISSING_JQ" "jq is required"
fi

LEGACY_LOG="$(mktemp)"
DEDUP_LOG="$(mktemp)"

start_server_or_fail "legacy" "${PORT_LEGACY}" "--alerting.ingest_policy.dedup_window_seconds=0 --alerting.ingest_policy.burst.window_seconds=0 --alerting.ingest_policy.burst.threshold=0 --alerting.ingest_policy.redis_backend.enabled=false" "${LEGACY_LOG}" LEGACY_PID
start_server_or_fail "dedup" "${PORT_DEDUP}" "--alerting.ingest_policy.dedup_window_seconds=30 --alerting.ingest_policy.burst.window_seconds=0 --alerting.ingest_policy.burst.threshold=0 --alerting.ingest_policy.redis_backend.enabled=false" "${DEDUP_LOG}" DEDUP_PID

now_epoch="$(date -u +%s)"
rand="${RAND:-$RANDOM}"

CURRENT_SCENARIO="legacy_default_policy"
fp_legacy="r4-legacy-${rand}"

ingest_legacy_1=$(cat <<JSON
{"idempotencyKey":"idem-r4-legacy-a-${rand}","fingerprint":"${fp_legacy}","status":"firing","severity":"P1","service":"svc-r4","cluster":"prod-r4","namespace":"default","workload":"svc-r4","lastSeenAt":{"seconds":${now_epoch},"nanos":0}}
JSON
)
call_or_fail "Legacy.Ingest1" POST "${BASE_URL_LEGACY}/v1/alert-events:ingest" "${ingest_legacy_1}"
legacy_incident_1="$(extract_field "${LAST_BODY}" "incidentID" "incident_id" || true)"
if [[ -z "${legacy_incident_1}" ]]; then
	fail_step "Legacy.Ingest1.IncidentMissing"
fi
INCIDENT_ID_SAMPLE="${legacy_incident_1}"

ingest_legacy_2=$(cat <<JSON
{"idempotencyKey":"idem-r4-legacy-b-${rand}","fingerprint":"${fp_legacy}","status":"firing","severity":"P1","service":"svc-r4","cluster":"prod-r4","namespace":"default","workload":"svc-r4","lastSeenAt":{"seconds":$((now_epoch + 5)),"nanos":0}}
JSON
)
call_or_fail "Legacy.Ingest2" POST "${BASE_URL_LEGACY}/v1/alert-events:ingest" "${ingest_legacy_2}"
legacy_incident_2="$(extract_field "${LAST_BODY}" "incidentID" "incident_id" || true)"
if [[ "${legacy_incident_2}" != "${legacy_incident_1}" ]]; then
	fail_step "Legacy.Ingest2.IncidentChanged"
fi
legacy_merge_2="$(json_value "${LAST_BODY}" '.mergeResult // .data.mergeResult // empty')"
if [[ "${legacy_merge_2}" != "current_updated" ]]; then
	fail_step "Legacy.Ingest2.MergeResult" "${LAST_HTTP_CODE}" "expected mergeResult=current_updated, got=${legacy_merge_2}; body=${LAST_BODY}"
fi

call_or_fail "Legacy.ListCurrent" GET "${BASE_URL_LEGACY}/v1/alert-events:current?fingerprint=${fp_legacy}&offset=0&limit=20"
legacy_current_total="$(json_value "${LAST_BODY}" '.totalCount // .data.totalCount // 0')"
if [[ "${legacy_current_total}" != "1" ]]; then
	fail_step "Legacy.ListCurrent.Count"
fi

call_or_fail "Legacy.ListHistory" GET "${BASE_URL_LEGACY}/v1/alert-events:history?fingerprint=${fp_legacy}&offset=0&limit=20"
legacy_history_total="$(json_value "${LAST_BODY}" '.totalCount // .data.totalCount // 0')"
if [[ "${legacy_history_total}" != "2" ]]; then
	fail_step "Legacy.ListHistory.Count"
fi

CURRENT_SCENARIO="dedup_policy_enabled"
fp_dedup="r4-dedup-${rand}"

ingest_dedup_1=$(cat <<JSON
{"idempotencyKey":"idem-r4-dedup-a-${rand}","fingerprint":"${fp_dedup}","status":"firing","severity":"P1","service":"svc-r4","cluster":"prod-r4","namespace":"default","workload":"svc-r4","lastSeenAt":{"seconds":${now_epoch},"nanos":0}}
JSON
)
call_or_fail "Dedup.Ingest1" POST "${BASE_URL_DEDUP}/v1/alert-events:ingest" "${ingest_dedup_1}"
dedup_incident_1="$(extract_field "${LAST_BODY}" "incidentID" "incident_id" || true)"
if [[ -z "${dedup_incident_1}" ]]; then
	fail_step "Dedup.Ingest1.IncidentMissing"
fi
INCIDENT_ID_SAMPLE="${dedup_incident_1}"

for i in 1 2 3; do
	ingest_dedup_n=$(cat <<JSON
{"idempotencyKey":"idem-r4-dedup-${i}-${rand}","fingerprint":"${fp_dedup}","status":"firing","severity":"P1","service":"svc-r4","cluster":"prod-r4","namespace":"default","workload":"svc-r4","lastSeenAt":{"seconds":$((now_epoch + i)),"nanos":0}}
JSON
)
	call_or_fail "Dedup.Ingest$((i + 1))" POST "${BASE_URL_DEDUP}/v1/alert-events:ingest" "${ingest_dedup_n}"
	incident_i="$(extract_field "${LAST_BODY}" "incidentID" "incident_id" || true)"
	if [[ -n "${incident_i}" ]]; then
		fail_step "Dedup.Ingest$((i + 1)).IncidentShouldBeSuppressed"
	fi
	dedup_flag="$(json_value "${LAST_BODY}" '((.silenced // .data.silenced // false) | tostring)')"
	if [[ "${dedup_flag}" != "false" ]]; then
		fail_step "Dedup.Ingest$((i + 1)).SilenceUnexpected"
	fi
done

call_or_fail "Dedup.ListHistory" GET "${BASE_URL_DEDUP}/v1/alert-events:history?fingerprint=${fp_dedup}&offset=0&limit=20"
dedup_history_total="$(json_value "${LAST_BODY}" '.totalCount // .data.totalCount // 0')"
if [[ "${dedup_history_total}" != "4" ]]; then
	fail_step "Dedup.ListHistory.Count"
fi
uniq_history_incidents="$(json_value "${LAST_BODY}" '[.events[]?.incidentID // empty] | unique | length')"
if [[ "${uniq_history_incidents}" != "1" ]]; then
	fail_step "Dedup.ListHistory.UniqueIncident"
fi

call_or_fail "Dedup.ListCurrent" GET "${BASE_URL_DEDUP}/v1/alert-events:current?fingerprint=${fp_dedup}&offset=0&limit=20"
dedup_current_total="$(json_value "${LAST_BODY}" '.totalCount // .data.totalCount // 0')"
if [[ "${dedup_current_total}" != "1" ]]; then
	fail_step "Dedup.ListCurrent.Count"
fi
cur_incident="$(json_value "${LAST_BODY}" '(.events // .data.events // [])[0].incidentID // empty')"
if [[ "${cur_incident}" != "${dedup_incident_1}" ]]; then
	fail_step "Dedup.ListCurrent.IncidentMismatch"
fi

CURRENT_SCENARIO="silence_priority"
fp_silence="r4-silence-${rand}"

call_or_fail "Silence.ListQueued.Before" GET "${BASE_URL_DEDUP}/v1/ai/jobs?status=queued&offset=0&limit=${QUEUE_LIMIT}&wait_seconds=0"
queued_before="$(json_value "${LAST_BODY}" '.totalCount // .data.totalCount // 0')"

silence_body=$(cat <<JSON
{"namespace":"default","enabled":true,"startsAt":{"seconds":$((now_epoch - 60)),"nanos":0},"endsAt":{"seconds":$((now_epoch + 1800)),"nanos":0},"reason":"r4-silence","createdBy":"r4-tester","matchers":[{"key":"fingerprint","op":"=","value":"${fp_silence}"}]}
JSON
)
call_or_fail "Silence.Create" POST "${BASE_URL_DEDUP}/v1/silences" "${silence_body}"
silence_id="$(extract_field "${LAST_BODY}" "silenceID" "silence_id" || true)"
if [[ -z "${silence_id}" ]]; then
	fail_step "Silence.Create.ParseID"
fi

silence_ingest=$(cat <<JSON
{"idempotencyKey":"idem-r4-silence-${rand}","fingerprint":"${fp_silence}","status":"firing","severity":"P1","service":"svc-r4","cluster":"prod-r4","namespace":"default","workload":"svc-r4","lastSeenAt":{"seconds":$((now_epoch + 10)),"nanos":0}}
JSON
)
call_or_fail "Silence.Ingest" POST "${BASE_URL_DEDUP}/v1/alert-events:ingest" "${silence_ingest}"
silenced_flag="$(json_value "${LAST_BODY}" '((.silenced // .data.silenced // false) | tostring)')"
if [[ "${silenced_flag}" != "true" ]]; then
	fail_step "Silence.Ingest.Flag"
fi
incident_after_silence="$(extract_field "${LAST_BODY}" "incidentID" "incident_id" || true)"
if [[ -n "${incident_after_silence}" ]]; then
	fail_step "Silence.Ingest.IncidentShouldBeEmpty"
fi
resp_silence_id="$(extract_field "${LAST_BODY}" "silenceID" "silence_id" || true)"
if [[ "${resp_silence_id}" != "${silence_id}" ]]; then
	fail_step "Silence.Ingest.SilenceIDMismatch"
fi

call_or_fail "Silence.ListQueued.After" GET "${BASE_URL_DEDUP}/v1/ai/jobs?status=queued&offset=0&limit=${QUEUE_LIMIT}&wait_seconds=0"
queued_after="$(json_value "${LAST_BODY}" '.totalCount // .data.totalCount // 0')"
if [[ "${queued_after}" != "${queued_before}" ]]; then
	fail_step "Silence.ListQueued.UnexpectedChange"
fi

call_or_fail "Silence.ListCurrent" GET "${BASE_URL_DEDUP}/v1/alert-events:current?fingerprint=${fp_silence}&offset=0&limit=20"
cur_silenced="$(json_value "${LAST_BODY}" '(((.events // .data.events // [])[0].isSilenced // false) | tostring)')"
if [[ "${cur_silenced}" != "true" ]]; then
	fail_step "Silence.ListCurrent.Flag"
fi
cur_incident_silenced="$(json_value "${LAST_BODY}" '(.events // .data.events // [])[0].incidentID // empty')"
if [[ -n "${cur_incident_silenced}" ]]; then
	fail_step "Silence.ListCurrent.IncidentShouldBeEmpty"
fi

echo "PASS R4"
echo "legacy_incident_id=${legacy_incident_1}"
echo "dedup_incident_id=${dedup_incident_1}"
echo "silence_id=${silence_id}"
