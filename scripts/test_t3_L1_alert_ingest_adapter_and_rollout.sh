#!/usr/bin/env bash
set -euo pipefail

CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
DEBUG="${DEBUG:-0}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
CONFIG_PATH="${CONFIG_PATH:-${REPO_ROOT}/configs/rca-apiserver.yaml}"
SERVER_CMD_BASE="${SERVER_CMD_BASE:-GOLANG_PROTOBUF_REGISTRATION_CONFLICT=warn go run ./cmd/rca-apiserver --config ${CONFIG_PATH}}"

PORT_OBSERVE="${PORT_OBSERVE:-$((18740 + RANDOM % 80))}"
PORT_ENFORCE="${PORT_ENFORCE:-$((PORT_OBSERVE + 1))}"
BASE_URL_OBSERVE="${BASE_URL_OBSERVE:-http://127.0.0.1:${PORT_OBSERVE}}"
BASE_URL_ENFORCE="${BASE_URL_ENFORCE:-http://127.0.0.1:${PORT_ENFORCE}}"
WAIT_TIMEOUT_SEC="${WAIT_TIMEOUT_SEC:-90}"

LAST_HTTP_CODE=""
LAST_BODY=""
INCIDENT_ID_SAMPLE=""
EVENT_ID_SAMPLE=""
SILENCE_ID_SAMPLE=""
FINGERPRINT_SAMPLE=""
OBSERVE_LOG=""
ENFORCE_LOG=""
OBSERVE_PID=""
ENFORCE_PID=""

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

	echo "FAIL T3 step=${step}"
	echo "http_code=${code:-UNKNOWN}"
	echo "body<<EOF"
	truncate_2kb "${body}"
	echo
	echo "EOF"
	echo "incident_id=${INCIDENT_ID_SAMPLE:-NONE}"
	echo "event_id=${EVENT_ID_SAMPLE:-NONE}"
	echo "silence_id=${SILENCE_ID_SAMPLE:-NONE}"
	echo "fingerprint=${FINGERPRINT_SAMPLE:-NONE}"
	if [[ -n "${OBSERVE_LOG}" ]]; then
		echo "observe_log_tail<<EOF"
		tail -n 80 "${OBSERVE_LOG}" 2>/dev/null | head -c 2048
		echo
		echo "EOF"
	fi
	if [[ -n "${ENFORCE_LOG}" ]]; then
		echo "enforce_log_tail<<EOF"
		tail -n 80 "${ENFORCE_LOG}" 2>/dev/null | head -c 2048
		echo
		echo "EOF"
	fi
	exit 1
}

http_json() {
	local method="$1"
	local url="$2"
	local body="${3:-}"

	local tmp_body tmp_err rc code curl_err
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

json_value() {
	local json="$1"
	local expr="$2"
	printf '%s' "${json}" | jq -r "${expr}" 2>/dev/null
}

extract_field() {
	local json="$1"
	shift
	local key
	for key in "$@"; do
		local value
		value="$(printf '%s' "${json}" | jq -r --arg k "${key}" '.[$k] // .data[$k] // empty' 2>/dev/null)"
		if [[ -n "${value}" && "${value}" != "null" ]]; then
			printf '%s' "${value}"
			return 0
		fi
	done
	return 1
}

metric_sum() {
	local base_url="$1"
	local metric="$2"
	local raw
	raw="$(${CURL} -sS "${base_url}/metrics")"
	printf '%s\n' "${raw}" | awk -v metric="${metric}" '
		$1 ~ ("^" metric "({|$)") { sum += $NF }
		END { printf "%.0f\n", sum + 0 }
	'
}

metric_exists_or_fail() {
	local step="$1"
	local base_url="$2"
	local metric="$3"
	local raw
	raw="$(${CURL} -sS "${base_url}/metrics")"
	if ! printf '%s\n' "${raw}" | grep -E "^${metric}(\{|[[:space:]])" >/dev/null 2>&1; then
		fail_step "${step}" "METRIC_MISSING" "${metric}"
	fi
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
	if [[ -n "${OBSERVE_PID}" ]]; then
		kill "${OBSERVE_PID}" >/dev/null 2>&1 || true
		wait "${OBSERVE_PID}" >/dev/null 2>&1 || true
	fi
	if [[ -n "${ENFORCE_PID}" ]]; then
		kill "${ENFORCE_PID}" >/dev/null 2>&1 || true
		wait "${ENFORCE_PID}" >/dev/null 2>&1 || true
	fi
	rm -f "${OBSERVE_LOG:-}" "${ENFORCE_LOG:-}"
}
trap cleanup EXIT

if ! need_cmd jq; then
	fail_step "Precheck.MissingJQ" "MISSING_JQ" "jq is required"
fi

OBSERVE_LOG="$(mktemp)"
ENFORCE_LOG="$(mktemp)"

start_server_or_fail "observe" "${PORT_OBSERVE}" "--alerting.rollout.enabled=true --alerting.rollout.mode=observe --alerting.rollout.allowed_namespaces=trial-ns --alerting.rollout.allowed_services=trial-svc --alerting.ingest_policy.dedup_window_seconds=0 --alerting.ingest_policy.burst.window_seconds=0 --alerting.ingest_policy.burst.threshold=0 --alerting.ingest_policy.redis_backend.enabled=false" "${OBSERVE_LOG}" OBSERVE_PID
start_server_or_fail "enforce" "${PORT_ENFORCE}" "--alerting.rollout.enabled=true --alerting.rollout.mode=enforce --alerting.rollout.allowed_namespaces=trial-ns --alerting.rollout.allowed_services=trial-svc --alerting.ingest_policy.dedup_window_seconds=0 --alerting.ingest_policy.burst.window_seconds=0 --alerting.ingest_policy.burst.threshold=0 --alerting.ingest_policy.redis_backend.enabled=false" "${ENFORCE_LOG}" ENFORCE_PID

now_epoch="$(date -u +%s)"
rand="${RAND:-$RANDOM}"

# (a) adapter mapping + stable fingerprint.
payload_a_1=$(cat <<JSON
{"namespace":"trial-ns","service":"trial-svc","severity":"critical","alertname":"LatencyHigh","summary":"trial latency high","labels":{"alertname":"LatencyHigh","namespace":"trial-ns","service":"trial-svc","pod":"trial-pod-a","ip":"10.0.0.1"},"annotations":{"summary":"trial latency high"}}
JSON
)
call_or_fail "A.AdapterIngest1" POST "${BASE_URL_OBSERVE}/v1/alerts/ingest/generic_v1" "${payload_a_1}"
fp_a_1="$(extract_field "${LAST_BODY}" "fingerprint" || true)"
event_a_1="$(extract_field "${LAST_BODY}" "eventID" "event_id" || true)"
EVENT_ID_SAMPLE="${event_a_1}"
FINGERPRINT_SAMPLE="${fp_a_1}"
if [[ -z "${fp_a_1}" ]]; then
	fail_step "A.AdapterIngest1.FingerprintMissing"
fi

payload_a_2=$(cat <<JSON
{"namespace":"trial-ns","service":"trial-svc","severity":"critical","alertname":"LatencyHigh","summary":"trial latency high","labels":{"alertname":"LatencyHigh","namespace":"trial-ns","service":"trial-svc","pod":"trial-pod-b","ip":"10.0.0.2"},"annotations":{"summary":"trial latency high"}}
JSON
)
call_or_fail "A.AdapterIngest2" POST "${BASE_URL_OBSERVE}/v1/alerts/ingest/generic_v1" "${payload_a_2}"
fp_a_2="$(extract_field "${LAST_BODY}" "fingerprint" || true)"
if [[ "${fp_a_1}" != "${fp_a_2}" ]]; then
	fail_step "A.StableFingerprint" "${LAST_HTTP_CODE}" "fingerprint changed: ${fp_a_1} vs ${fp_a_2}"
fi

call_or_fail "A.ListCurrent" GET "${BASE_URL_OBSERVE}/v1/alert-events:current?fingerprint=${fp_a_1}&offset=0&limit=20"
a_total="$(json_value "${LAST_BODY}" '.totalCount // .data.totalCount // 0')"
if [[ "${a_total}" == "0" || -z "${a_total}" ]]; then
	fail_step "A.ListCurrent.Empty"
fi

# (b) observe mode miss allowlist: write but no incident progression.
obs_total_before="$(metric_sum "${BASE_URL_OBSERVE}" "alert_ingest_total")"
obs_drop_before="$(metric_sum "${BASE_URL_OBSERVE}" "alert_ingest_dropped_total")"
payload_b=$(cat <<JSON
{"namespace":"other-ns","service":"other-svc","severity":"warning","alertname":"ErrorRateHigh","summary":"observe-only path","labels":{"alertname":"ErrorRateHigh","namespace":"other-ns","service":"other-svc"},"annotations":{"summary":"observe-only path"}}
JSON
)
call_or_fail "B.ObserveMissIngest" POST "${BASE_URL_OBSERVE}/v1/alerts/ingest/generic_v1" "${payload_b}"
b_incident_id="$(extract_field "${LAST_BODY}" "incidentID" "incident_id" || true)"
fp_b="$(extract_field "${LAST_BODY}" "fingerprint" || true)"
if [[ -n "${b_incident_id}" ]]; then
	fail_step "B.ObserveMiss.ShouldNotProgress"
fi
call_or_fail "B.ListCurrent" GET "${BASE_URL_OBSERVE}/v1/alert-events:current?fingerprint=${fp_b}&offset=0&limit=20"
b_total="$(json_value "${LAST_BODY}" '.totalCount // .data.totalCount // 0')"
if [[ "${b_total}" == "0" || -z "${b_total}" ]]; then
	fail_step "B.ListCurrent.Empty"
fi
obs_total_after="$(metric_sum "${BASE_URL_OBSERVE}" "alert_ingest_total")"
obs_drop_after="$(metric_sum "${BASE_URL_OBSERVE}" "alert_ingest_dropped_total")"
if (( obs_total_after <= obs_total_before )); then
	fail_step "B.Metrics.TotalNotGrown" "METRIC_ASSERT" "before=${obs_total_before} after=${obs_total_after}"
fi
if (( obs_drop_after <= obs_drop_before )); then
	fail_step "B.Metrics.DropNotGrown" "METRIC_ASSERT" "before=${obs_drop_before} after=${obs_drop_after}"
fi

# (c) enforce mode hit allowlist: should progress and create/bind incident.
enf_progress_before="$(metric_sum "${BASE_URL_ENFORCE}" "alert_ingest_progressed_total")"
enf_new_before="$(metric_sum "${BASE_URL_ENFORCE}" "alert_ingest_new_incident_total")"
payload_c=$(cat <<JSON
{"namespace":"trial-ns","service":"trial-svc","severity":"critical","alertname":"CPUHigh","summary":"enforce path","labels":{"alertname":"CPUHigh","namespace":"trial-ns","service":"trial-svc"},"annotations":{"summary":"enforce path"}}
JSON
)
call_or_fail "C.EnforceHitIngest" POST "${BASE_URL_ENFORCE}/v1/alerts/ingest/generic_v1" "${payload_c}"
c_incident_id="$(extract_field "${LAST_BODY}" "incidentID" "incident_id" || true)"
INCIDENT_ID_SAMPLE="${c_incident_id}"
if [[ -z "${c_incident_id}" ]]; then
	fail_step "C.EnforceHit.IncidentMissing"
fi
enf_progress_after="$(metric_sum "${BASE_URL_ENFORCE}" "alert_ingest_progressed_total")"
enf_new_after="$(metric_sum "${BASE_URL_ENFORCE}" "alert_ingest_new_incident_total")"
if (( enf_progress_after <= enf_progress_before )); then
	fail_step "C.Metrics.ProgressNotGrown" "METRIC_ASSERT" "before=${enf_progress_before} after=${enf_progress_after}"
fi
if (( enf_new_after <= enf_new_before )); then
	fail_step "C.Metrics.NewIncidentNotGrown" "METRIC_ASSERT" "before=${enf_new_before} after=${enf_new_after}"
fi

# (d) silence path: should be silenced and not progress.
fp_silence="t3-silence-${rand}"
silence_body=$(cat <<JSON
{"namespace":"trial-ns","enabled":true,"startsAt":{"seconds":$((now_epoch - 60)),"nanos":0},"endsAt":{"seconds":$((now_epoch + 1800)),"nanos":0},"reason":"t3-silence","createdBy":"t3-script","matchers":[{"key":"fingerprint","op":"=","value":"${fp_silence}"}]}
JSON
)
call_or_fail "D.SilenceCreate" POST "${BASE_URL_ENFORCE}/v1/silences" "${silence_body}"
SILENCE_ID_SAMPLE="$(extract_field "${LAST_BODY}" "silenceID" "silence_id" || true)"
enf_silenced_before="$(metric_sum "${BASE_URL_ENFORCE}" "alert_ingest_silenced_total")"

payload_d=$(cat <<JSON
{"fingerprint":"${fp_silence}","namespace":"trial-ns","service":"trial-svc","severity":"warning","alertname":"DiskHigh","summary":"should be silenced","labels":{"alertname":"DiskHigh","namespace":"trial-ns","service":"trial-svc"},"annotations":{"summary":"should be silenced"}}
JSON
)
call_or_fail "D.SilenceIngest" POST "${BASE_URL_ENFORCE}/v1/alerts/ingest/generic_v1" "${payload_d}"
d_incident_id="$(extract_field "${LAST_BODY}" "incidentID" "incident_id" || true)"
d_silenced="$(json_value "${LAST_BODY}" '((.silenced // .data.silenced // false) | tostring)')"
if [[ "${d_silenced}" != "true" ]]; then
	fail_step "D.SilenceIngest.ExpectedSilenced"
fi
if [[ -n "${d_incident_id}" ]]; then
	fail_step "D.SilenceIngest.ShouldNotProgress"
fi
enf_silenced_after="$(metric_sum "${BASE_URL_ENFORCE}" "alert_ingest_silenced_total")"
if (( enf_silenced_after <= enf_silenced_before )); then
	fail_step "D.Metrics.SilencedNotGrown" "METRIC_ASSERT" "before=${enf_silenced_before} after=${enf_silenced_after}"
fi

# (e) metrics families existence.
for metric in \
	alert_ingest_total \
	alert_ingest_allowed_total \
	alert_ingest_progressed_total \
	alert_ingest_dropped_total \
	alert_ingest_silenced_total \
	alert_ingest_merged_total \
	alert_ingest_new_incident_total; do
	metric_exists_or_fail "E.MetricsExists.${metric}" "${BASE_URL_ENFORCE}" "${metric}"
done

echo "PASS T3 incident_id=${INCIDENT_ID_SAMPLE:-NONE} event_id=${EVENT_ID_SAMPLE:-NONE} silence_id=${SILENCE_ID_SAMPLE:-NONE} fingerprint=${FINGERPRINT_SAMPLE:-NONE}"
