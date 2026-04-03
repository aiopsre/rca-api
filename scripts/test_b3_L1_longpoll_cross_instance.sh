#!/usr/bin/env bash
set -euo pipefail

SCOPES="${SCOPES:-*}"
CURL="${CURL:-curl}"
DEBUG="${DEBUG:-0}"
WAIT_SECONDS="${WAIT_SECONDS:-8}"
POLL_TIMEOUT_SEC="${POLL_TIMEOUT_SEC:-20}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
APISERVER_CONFIG="${APISERVER_CONFIG:-${REPO_ROOT}/configs/rca-apiserver.yaml}"
# Keep command override support, but always pass per-instance --config at runtime.
APISERVER_CMD_BASE="${APISERVER_CMD_BASE:-GOLANG_PROTOBUF_REGISTRATION_CONFLICT=warn go run ./cmd/rca-apiserver}"
PORT_A="${PORT_A:-15555}"
PORT_B="${PORT_B:-15556}"
BASE_URL_A="${BASE_URL_A:-http://127.0.0.1:${PORT_A}}"
BASE_URL_B="${BASE_URL_B:-http://127.0.0.1:${PORT_B}}"

LAST_HTTP_CODE=""
LAST_BODY=""
INCIDENT_ID=""
JOB_ID=""
SERVER_A_PID=""
SERVER_B_PID=""
SERVER_A_LOG=""
SERVER_B_LOG=""
SERVER_A_CONFIG=""
SERVER_B_CONFIG=""
POLL_BODY_FILE=""
POLL_CODE_FILE=""
POLL_ERR_FILE=""

debug() {
	if [[ "${DEBUG}" == "1" ]]; then
		echo "[DEBUG] $*" >&2
	fi
}

now_ms() {
	local raw
	raw="$(date +%s%3N 2>/dev/null || true)"
	if [[ "${raw}" =~ ^[0-9]+$ ]]; then
		printf '%s' "${raw}"
		return 0
	fi
	printf '%s' "$(( $(date +%s) * 1000 ))"
}

truncate_2kb() {
	printf '%s' "$1" | head -c 2048
}

fail_step() {
	local step="$1"
	local code="${2:-${LAST_HTTP_CODE}}"
	local body="${3:-${LAST_BODY}}"

	echo "FAIL B3 step=${step}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	truncate_2kb "${body}"
	echo
	echo "EOF"
	echo "incident_id=${INCIDENT_ID:-NONE}"
	echo "job_id=${JOB_ID:-NONE}"
	echo "base_url_a=${BASE_URL_A}"
	echo "base_url_b=${BASE_URL_B}"
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

	if command -v jq >/dev/null 2>&1; then
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

cleanup() {
	if [[ -n "${SERVER_A_PID}" ]]; then
		kill "${SERVER_A_PID}" >/dev/null 2>&1 || true
		wait "${SERVER_A_PID}" >/dev/null 2>&1 || true
	fi
	if [[ -n "${SERVER_B_PID}" ]]; then
		kill "${SERVER_B_PID}" >/dev/null 2>&1 || true
		wait "${SERVER_B_PID}" >/dev/null 2>&1 || true
	fi
	rm -f "${SERVER_A_LOG}" "${SERVER_B_LOG}" "${SERVER_A_CONFIG}" "${SERVER_B_CONFIG}" "${POLL_BODY_FILE}" "${POLL_CODE_FILE}" "${POLL_ERR_FILE}"
}
trap cleanup EXIT

render_apiserver_config_for_port() {
	local port="$1"
	local tmp_cfg raw_cfg
	raw_cfg="$(mktemp)"
	tmp_cfg="${raw_cfg}.yaml"
	if ! mv "${raw_cfg}" "${tmp_cfg}" >/dev/null 2>&1; then
		rm -f "${raw_cfg}" "${tmp_cfg}"
		fail_step "RenderAPIServerConfigTmp.${port}" "CONFIG_TMP_FAILED" "${APISERVER_CONFIG}"
	fi

	if ! awk -v port="${port}" '
		BEGIN { done = 0 }
		{
			if (!done && $0 ~ /^[[:space:]]*addr:[[:space:]]/) {
				print "  addr: 127.0.0.1:" port " # script override"
				done = 1
				next
			}
			print
		}
		END {
			if (!done) {
				exit 2
			}
		}
	' "${APISERVER_CONFIG}" >"${tmp_cfg}"; then
		rm -f "${tmp_cfg}"
		fail_step "RenderAPIServerConfig.${port}" "CONFIG_RENDER_FAILED" "${APISERVER_CONFIG}"
	fi
	if ! grep -q "127.0.0.1:${port}" "${tmp_cfg}"; then
		rm -f "${tmp_cfg}"
		fail_step "RenderAPIServerConfigVerify.${port}" "CONFIG_VERIFY_FAILED" "${APISERVER_CONFIG}"
	fi
	printf '%s' "${tmp_cfg}"
}

wait_server_ready() {
	local name="$1"
	local base_url="$2"
	local deadline now
	deadline="$(( $(date +%s) + 40 ))"
	while true; do
		if http_json GET "${base_url}/v1/ai/jobs?status=queued&offset=0&limit=1"; then
			if [[ "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
				debug "server ${name} ready base_url=${base_url}"
				return 0
			fi
		fi
		now="$(date +%s)"
		if (( now > deadline )); then
			fail_step "WaitServerReady.${name}" "${LAST_HTTP_CODE}" "${LAST_BODY}"
		fi
		sleep 1
	done
}

start_apiserver() {
	local name="$1"
	local port="$2"
	local logf cfg
	logf="$(mktemp)"
	cfg="$(render_apiserver_config_for_port "${port}")"

	(
		cd "${REPO_ROOT}" && \
			bash -lc "${APISERVER_CMD_BASE} --config ${cfg}"
	) >"${logf}" 2>&1 &
	local pid="$!"
	sleep 0.8
	if ! kill -0 "${pid}" >/dev/null 2>&1; then
		LAST_HTTP_CODE="APISERVER_EXITED"
		LAST_BODY="$(cat "${logf}" 2>/dev/null || true)"
		fail_step "StartAPIServer.${name}"
	fi

	if [[ "${name}" == "A" ]]; then
		SERVER_A_PID="${pid}"
		SERVER_A_LOG="${logf}"
		SERVER_A_CONFIG="${cfg}"
	else
		SERVER_B_PID="${pid}"
		SERVER_B_LOG="${logf}"
		SERVER_B_CONFIG="${cfg}"
	fi
}

rand="${RAND:-$RANDOM}"
now_epoch="$(date -u +%s)"
start_epoch="$((now_epoch - 1800))"

start_apiserver "A" "${PORT_A}"
start_apiserver "B" "${PORT_B}"
wait_server_ready "A" "${BASE_URL_A}"
wait_server_ready "B" "${BASE_URL_B}"

POLL_BODY_FILE="$(mktemp)"
POLL_CODE_FILE="$(mktemp)"
POLL_ERR_FILE="$(mktemp)"

poll_started_ms="$(now_ms)"

(
	set +e
	code="$("${CURL}" -sS -o "${POLL_BODY_FILE}" -w "%{http_code}" \
		-H "Accept: application/json" \
		-H "X-Scopes: ${SCOPES}" \
		"${BASE_URL_B}/v1/ai/jobs?status=queued&offset=0&limit=10&wait_seconds=${WAIT_SECONDS}" \
		2>"${POLL_ERR_FILE}")"
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

fingerprint="b3-l1-fp-${rand}"
ingest_body=$(cat <<EOF
{"idempotencyKey":"idem-b3-l1-ingest-${rand}","fingerprint":"${fingerprint}","status":"firing","severity":"P1","service":"b3-l1-svc","cluster":"prod-b3","namespace":"default","workload":"b3-workload","lastSeenAt":{"seconds":${now_epoch},"nanos":0}}
EOF
)
call_or_fail "IngestAlertEventA" POST "${BASE_URL_A}/v1/alert-events:ingest" "${ingest_body}"
INCIDENT_ID="$(extract_field "${LAST_BODY}" "incidentID" "incident_id" || true)"
if [[ -z "${INCIDENT_ID}" ]]; then
	fail_step "ParseIncidentIDA" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

run_body=$(cat <<EOF
{"incidentID":"${INCIDENT_ID}","idempotencyKey":"idem-b3-l1-run-${rand}","pipeline":"basic_rca","trigger":"manual","timeRangeStart":{"seconds":${start_epoch},"nanos":0},"timeRangeEnd":{"seconds":${now_epoch},"nanos":0},"inputHintsJSON":"{\"scenario\":\"B3_L1\"}","createdBy":"system"}
EOF
)
call_or_fail "RunAIJobA" POST "${BASE_URL_A}/v1/incidents/${INCIDENT_ID}/ai:run" "${run_body}"
JOB_ID="$(extract_field "${LAST_BODY}" "jobID" "job_id" || true)"
if [[ -z "${JOB_ID}" ]]; then
	fail_step "ParseJobIDA" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

deadline="$(( $(date +%s) + POLL_TIMEOUT_SEC ))"
while kill -0 "${poll_pid}" >/dev/null 2>&1; do
	if (( $(date +%s) > deadline )); then
		kill "${poll_pid}" >/dev/null 2>&1 || true
		wait "${poll_pid}" >/dev/null 2>&1 || true
		fail_step "LongPollTimeout" "TIMEOUT" "$(cat "${POLL_BODY_FILE}" 2>/dev/null || true)"
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
	fail_step "LongPollResultCode" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

if ! printf '%s' "${LAST_BODY}" | grep -q "${JOB_ID}"; then
	fail_step "LongPollMissingJobID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

poll_finished_ms="$(now_ms)"
elapsed_ms="$((poll_finished_ms - poll_started_ms))"
max_expected_ms="$((WAIT_SECONDS * 1000 + 1000))"
if (( elapsed_ms > max_expected_ms )); then
	fail_step "LongPollWakeTooLate" "${LAST_HTTP_CODE}" "elapsed_ms=${elapsed_ms}"
fi

echo "PASS B3"
echo "incident_id=${INCIDENT_ID}"
echo "job_id=${JOB_ID}"
echo "long_poll_elapsed_ms=${elapsed_ms}"
echo "base_url_a=${BASE_URL_A}"
echo "base_url_b=${BASE_URL_B}"
